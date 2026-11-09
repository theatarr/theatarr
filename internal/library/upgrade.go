package library

import (
	"context"
	"fmt"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
)

// unsafeSeriesReplacements finds old files whose removal would lose episodes. Only accepted incoming files and
// active links that stay count as coverage; a rejected file or another file being replaced cannot save an episode.
func unsafeSeriesReplacements(links []PlannedLink, existing []existingFile) map[string]bool {
	covered := map[int64]bool{}
	replaced := map[string]bool{}
	for _, lk := range links {
		for _, id := range lk.Episodes {
			covered[id] = true
		}
		for _, old := range existing {
			if old.Path == lk.Path || overlaps(old.Episodes, lk.Episodes) {
				replaced[old.Path] = true
			}
		}
	}
	for _, old := range existing {
		if old.LinkID != 0 && !replaced[old.Path] {
			for _, id := range old.Episodes {
				covered[id] = true
			}
		}
	}
	unsafe := map[string]bool{}
	for _, old := range existing {
		if replaced[old.Path] {
			for _, id := range old.Episodes {
				if !covered[id] {
					unsafe[old.Path] = true
				}
			}
		}
	}
	return unsafe
}

// planDeadReplacements moves aside the version's dead links (their file left the mount) whose episodes a planned
// link holds: revived later, each would be a second file next to the new one. A dead link covers nothing, so its
// removal never uncovers an episode.
func planDeadReplacements(plan *Plan, dead []existingFile) {
	paths := map[string]bool{}
	for _, lk := range plan.Links {
		paths[lk.Path] = true
	}
	for _, old := range dead {
		if paths[old.Path] {
			continue
		}
		for i := range plan.Links {
			if overlaps(old.Episodes, plan.Links[i].Episodes) {
				plan.Links[i].Replaces = append(plan.Links[i].Replaces, old.Path)
				plan.Links[i].ReplaceIDs = append(plan.Links[i].ReplaceIDs, old.LinkID)
				break
			}
		}
	}
}

// planSeriesReplacements applies Sonarr's SameEpisodesImportSpecification after the per-file checks, across the
// accepted batch. A partial replacement is refused, not linked alongside the old multi-episode file.
func planSeriesReplacements(plan *Plan, existing []existingFile) {
	for {
		unsafe := unsafeSeriesReplacements(plan.Links, existing)
		if len(unsafe) == 0 {
			break
		}
		var kept []PlannedLink
		for _, lk := range plan.Links {
			why := ""
			for _, old := range existing {
				if unsafe[old.Path] && (old.Path == lk.Path || overlaps(old.Episodes, lk.Episodes)) {
					why = fmt.Sprintf("Episode file on disk contains more episodes than this file contains: %s", old.Path)
					break
				}
			}
			if why == "" {
				kept = append(kept, lk)
				continue
			}
			for i := range plan.Files {
				if plan.Files[i].Rel == lk.File {
					skipNotUpgrade(plan, &plan.Files[i], why)
					break
				}
			}
		}
		plan.Links = kept
	}
	paths := map[string]bool{}
	for _, lk := range plan.Links {
		paths[lk.Path] = true
	}
	for _, old := range existing {
		if paths[old.Path] {
			continue // replaced in place, never remove a newly made link
		}
		for i := len(plan.Links) - 1; i >= 0; i-- {
			if overlaps(old.Episodes, plan.Links[i].Episodes) {
				plan.Links[i].Replaces = append(plan.Links[i].Replaces, old.Path)
				plan.Links[i].ReplaceIDs = append(plan.Links[i].ReplaceIDs, old.LinkID)
				break // a batch moves each old file aside once
			}
		}
	}
}

// upgradeCheck is the import's UpgradeSpecification (decide.Engine.ImportUpgrade): a file of the download is linked
// over the version's current files only when it upgrades every one it would replace, as Sonarr's and Radarr's
// imports refuse a file that is "Not an upgrade for existing episode file(s)". A season pack accepted because one
// episode was missing (packs: any_upgradable) so fills that episode and upgrades only the files it betters.
//
// The current files are the ones the decision compares with: per episode, the active link, else the arr's file
// (decide.LoadEpisodes); for a movie, decide.LoadExisting. A link already pointing at the file is the same file and
// needs no check (a retried or repeated import). A client's explicit files (manual import) are not checked, as the
// arrs' ManualImport executes its choices without the import specifications.
type upgradeCheck struct {
	engine  *decide.Engine
	version decide.Version
	file    *decide.ExistingFile // the download's file inputs (linkedMetadata), per file completed by withFacts
	title   *decide.Title
	byEp    map[int64]heldFile // series: each episode's current file
	movie   *heldFile
	targets map[int64]string // active link id -> target
}

type heldFile struct {
	id   int64 // minus the link id for a link, else the version_file id
	file *decide.ExistingFile
}

// newUpgradeCheck prepares actual-quality and replacement checks, except for explicit manual imports or when
// no decision engine is configured. Series episode coverage is checked separately.
func (l *Library) newUpgradeCheck(ctx context.Context, d *importDownload, existing []existingFile) (*upgradeCheck, error) {
	if len(d.Manual) > 0 || l.engine == nil {
		return nil, nil
	}
	if len(existing) == 0 && !d.Version.Policy.PreferSeaDex {
		return nil, nil // a prefer-off version's first import is not checked, as before
	}
	var err error
	if d.Version.Policy.PreferSeaDex {
		ctx, err = decide.WithStoredSeaDex(ctx, l.st.Reader())
		if err != nil {
			return nil, err
		}
	}
	ctx = decide.WithSeaDexTitle(decide.WithSeaDexPolicy(ctx, l.engine.Policy, d.Version.Policy), d.Version.Title.ID)
	file, title, err := l.linkedMetadata(ctx, d)
	if err != nil || title == nil {
		return nil, err
	}
	v := &d.Version
	u := &upgradeCheck{engine: l.engine, version: decide.Version{ID: v.VersionID, Key: v.Key, Policy: v.Policy},
		file: file, title: title, targets: map[int64]string{}}
	for _, e := range existing {
		if e.LinkID != 0 {
			u.targets[e.LinkID] = e.Target
		}
	}
	db := l.st.Reader()
	if v.Title.Kind == "movie" {
		f, id, err := decide.LoadExistingFile(ctx, db, v.VersionID)
		if err != nil {
			return nil, err
		}
		if f != nil {
			u.movie = &heldFile{id: id, file: f}
		}
		return u, nil
	}
	eps, err := decide.LoadEpisodes(ctx, db, v.Title.ID, v.VersionID)
	if err != nil {
		return nil, err
	}
	u.byEp = map[int64]heldFile{}
	for _, e := range eps {
		if e.File != nil {
			u.byEp[e.ID] = heldFile{id: e.FileID, file: e.File}
		}
	}
	return u, nil
}

// check returns why the release file rel (linked at target, holding episodes, aggregated as facts) is not an upgrade
// of a current file it would replace, or "" when it may be linked.
func (u *upgradeCheck) check(episodes []int64, target, rel string, size int64, facts decide.FileFacts) (string, error) {
	var held []heldFile
	if u.movie != nil {
		held = append(held, *u.movie)
	}
	seen := map[int64]bool{}
	for _, ep := range episodes {
		if h, ok := u.byEp[ep]; ok && !seen[h.id] {
			seen[h.id] = true
			held = append(held, h)
		}
	}
	nf := withFacts(*u.file, facts, rel, target, size)
	if u.version.Policy != nil && u.version.Policy.PreferSeaDex {
		if ok, why, err := u.engine.ImportUpgrade(u.title, u.version, &nf, nil); err != nil || !ok {
			return why, err
		}
	}
	for _, h := range held {
		if h.id < 0 && u.targets[-h.id] == target {
			continue // this very file is linked already
		}
		x := *h.file
		// As the decision: a current file from the same torrent takes the release's SeaDex standing.
		if x.SeaDex == format.SeaDexNone && x.InfoHash != "" && x.InfoHash == nf.InfoHash {
			x.SeaDex = nf.SeaDex
		}
		if decide.SameTorrent(&x, &nf) && u.version.Policy != nil && u.version.Policy.PreferSeaDex {
			x.SeaDexPreference = nf.SeaDexPreference
		}
		ok, why, err := u.engine.ImportUpgrade(u.title, u.version, &nf, &x)
		if err != nil {
			return "", err
		}
		if !ok {
			return why, nil
		}
	}
	return "", nil
}
