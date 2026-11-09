package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
)

// Which file is which episode (docs/design/library.md): a series file's episodes come, in order, from
//  1. the client's explicit choice (download_file, source manual: a manual import; one Change episodes replaced,
//     how replaced, holds none);
//  2. what an earlier import of the download linked it as (download_file, source import), so a retry agrees;
//  3. the resolver the decision shares (decide.ResolveFile, Sonarr's AggregateEpisodes over ParsingService): scene
//     numbering, daily air dates, absolute numbers, specials by title, and the anime franchise entry or alternate
//     cut, with the decision's own mapping (download.episode_map) for files that do not name it;
//  4. else the file's canonical season and episode numbers (and, for anime, TVDB absolute numbers).
//
// Files with a choice made by hand (1, and a recorded one an admin's Change episodes wrote, how manual) take their
// episodes before the other files: an automatic mapping of another file of the download never takes them first.
//
// The resolver's episodes must be the download's (the episodes it was grabbed for; every episode for a pack the
// router did not narrow down). A manual or recorded choice may name any episode of the title. When the mapping of the
// franchise entry the download stored changed between the grab and the import, the download's episodes are
// reconciled first (rescope). Any other change of the franchise mappings leaves them alone: a file that now maps
// outside them is held (out of scope), with nothing moved.

// rescope reconciles the download's episodes with the mapping of the franchise entry it stored (EpisodeMap), in two
// cases:
//   - the entry it was grabbed through is no longer importable (decide.EpisodeMap.Stale): its files now map through
//     Sonarr's numbering, so the episodes become the ones the decision now gives the stored release, else, for a
//     release that places nothing (a pack named by the entry), the ones its files resolve to;
//   - it was grabbed through Sonarr's numbering for an entry that was not importable (EpisodeMap.Fallback) and is
//     now: each file the entry now places, whose Sonarr placement is among the download's episodes, swaps those for
//     the entry's. Episodes no such file holds stay, and the release title is not decided again, so a pack the router
//     narrowed is not widened and a file named for another entry moves nothing.
//
// A file that cannot pass the import guards (a sample) moves nothing. A download with no stored entry is never
// re-scoped. And when a new episode already has a file of the version (a link, a dead link or the arr's file) the
// download is not re-scoped at all: its files are held as out of scope instead of replacing a file the grab did not
// cover. The stored mapping is kept: a later accept re-links the imported files through it (animerelink.go).
func (l *Library) rescope(ctx context.Context, d *importDownload, files []PlannedFile, guard func(PlannedFile, []episodeInfo) string) error {
	v := &d.Version
	m := d.EpisodeMap
	if l.engine == nil || d.rescoped != nil || m == nil || m.How != decide.AnimeMapFranchise || m.Entry == "" {
		return nil
	}
	db := l.st.Reader()
	t, err := decide.LoadTitle(ctx, db, v.Title.ID)
	if err != nil {
		return err
	}
	if err := decide.LoadSeriesVersion(ctx, db, &t, v.VersionID); err != nil {
		return err
	}
	if m.Fallback {
		importable := false
		if t.AnimeMap != nil {
			for _, en := range t.AnimeMap.Entries {
				importable = importable || en.Key == m.Entry && en.Importable()
			}
		}
		if !importable {
			return nil
		}
	} else if !m.Stale(t.AnimeMap) {
		return nil
	}
	all, err := loadEpisodes(ctx, db, v.Title.ID, nil)
	if err != nil {
		return err
	}
	byID := map[int64]episodeInfo{}
	for _, e := range all {
		byID[e.ID] = e
	}
	sv := ""
	if v.Policy != nil {
		sv = v.Policy.Sonarr
	}
	// placed is where a file that passes the import guards maps (nothing for one that does not).
	placed := func(t *decide.Title, f PlannedFile, m *decide.EpisodeMap) (decide.FileEpisodes, bool) {
		res, err := decide.ResolveFile(t, sv, decide.FileInput{Path: f.Rel, Release: d.ReleaseTitle, Folder: d.Name,
			OtherVideoFiles: len(files) > 1, Mapping: m})
		if err != nil || res.Refused != "" || len(res.EpisodeIDs) == 0 {
			return res, false
		}
		var eps []episodeInfo
		for _, id := range res.EpisodeIDs {
			eps = append(eps, byID[id])
		}
		return res, guard(f, eps) == ""
	}
	scope := map[int64]bool{}
	for _, e := range v.Episodes {
		scope[e.ID] = true
	}
	next := map[int64]bool{}
	if m.Fallback {
		sonarr := t // the series without its franchise mappings: where Sonarr's numbering places a file
		sonarr.AnimeMap = nil
		var in []int64
		for id := range scope {
			next[id] = true
		}
		for _, f := range files {
			if _, ok := d.Manual[f.Rel]; ok {
				continue
			}
			if _, ok := d.Recorded[f.Rel]; ok {
				continue
			}
			res, ok := placed(&t, f, m)
			if !ok || !strings.HasSuffix(res.How, "+"+decide.AnimeMapFranchise+":"+m.Entry) {
				continue
			}
			was, ok := placed(&sonarr, f, nil)
			if !ok || slices.ContainsFunc(was.EpisodeIDs, func(id int64) bool { return !scope[id] }) {
				continue
			}
			for _, id := range was.EpisodeIDs {
				delete(next, id)
			}
			in = append(in, res.EpisodeIDs...)
		}
		for _, id := range in {
			next[id] = true
		}
	} else {
		rel, err := l.storedRelease(ctx, d)
		if err != nil {
			return err
		}
		dec, err := l.engine.Decide(ctx, decide.Release{Release: rel}, t,
			decide.Version{ID: v.VersionID, Key: v.Key, Policy: v.Policy}, nil)
		if err != nil {
			return err
		}
		if dec != nil {
			for _, id := range dec.EpisodeIDs {
				next[id] = true
			}
		}
		if len(next) == 0 {
			for _, f := range files {
				if res, ok := placed(&t, f, m); ok {
					for _, id := range res.EpisodeIDs {
						next[id] = true
					}
				}
			}
		}
	}
	ids := slices.Sorted(maps.Keys(next))
	if len(ids) == 0 || slices.Equal(ids, slices.Sorted(maps.Keys(scope))) {
		return nil
	}
	existing, err := existingFiles(ctx, db, v.VersionID, v.Title.ID)
	if err != nil {
		return err
	}
	dead, err := deadFiles(ctx, db, v.VersionID, v.Title.ID)
	if err != nil {
		return err
	}
	for _, old := range append(existing, dead...) {
		for _, id := range old.Episodes {
			if next[id] && !scope[id] {
				l.log.Warn("library: franchise mapping changed since the grab, but a new episode already has a file: "+
					"the download keeps its episodes", "download", d.ID, "entry", m.Entry, "episodes", ids, "file", old.Path)
				return nil
			}
		}
	}
	eps, err := loadEpisodes(ctx, db, v.Title.ID, ids)
	if err != nil {
		return err
	}
	l.log.Info("library: franchise mapping changed since the grab, episodes reconciled", "download", d.ID,
		"entry", m.Entry, "fallback", m.Fallback, "episodes", ids)
	v.Episodes, d.rescoped = eps, ids
	return nil
}

// storedRelease is the release as the download stored it (release_data: its TVDB id, kind and extras, which a title
// alone may not carry), for deciding it again.
func (l *Library) storedRelease(ctx context.Context, d *importDownload) (indexer.Release, error) {
	var rel indexer.Release
	if d.ID != 0 {
		var data sql.NullString
		if err := l.st.Reader().QueryRowContext(ctx, `SELECT release_data FROM download WHERE id = ?`, d.ID).Scan(&data); err != nil {
			return rel, err
		}
		if data.Valid {
			_ = json.Unmarshal([]byte(data.String), &rel) // a row from before release_data: the title alone
		}
	}
	rel.Title, rel.InfoHash, rel.Indexer = d.ReleaseTitle, d.InfoHash, d.Indexer
	return rel, nil
}

// franchisePlaced says the resolver placed a file through the franchise (an entry or an alternate cut).
func franchisePlaced(how string) bool {
	return strings.Contains(how, "+"+decide.AnimeMapFranchise+":") || strings.Contains(how, "+"+decide.AnimeMapAltCut+":")
}

type resolver struct {
	d       *importDownload
	pack    bool
	allowed map[int64]episodeInfo
	all     map[int64]episodeInfo
	title   *decide.Title
}

func (l *Library) newResolver(ctx context.Context, d *importDownload, pack bool) (*resolver, error) {
	v := &d.Version
	r := &resolver{d: d, pack: pack, allowed: map[int64]episodeInfo{}, all: map[int64]episodeInfo{}}
	for _, e := range v.Episodes {
		r.allowed[e.ID] = e
	}
	if len(d.Manual) > 0 || len(d.Recorded) > 0 {
		all, err := loadEpisodes(ctx, l.st.Reader(), v.Title.ID, nil)
		if err != nil {
			return nil, err
		}
		for _, e := range all {
			r.all[e.ID] = e
		}
	}
	t, err := decide.LoadTitle(ctx, l.st.Reader(), v.Title.ID)
	if err != nil {
		return nil, err
	}
	if err := decide.LoadSeriesVersion(ctx, l.st.Reader(), &t, v.VersionID); err != nil {
		return nil, err
	}
	r.title = &t
	return r, nil
}

// order is the order the files take their episodes in (indexes into files): those chosen by hand first, the
// others after, each in the files' own order.
func (r *resolver) order(files []PlannedFile) []int {
	var first, rest []int
	for i, f := range files {
		if _, ok := r.d.Manual[f.Rel]; ok || r.d.Recorded[f.Rel].How == HowManual {
			first = append(first, i)
		} else {
			rest = append(rest, i)
		}
	}
	return append(first, rest...)
}

// episodes are a file's episodes (by its path in the release folder) and how they were found. A client's choice
// (how is HowManual) is authoritative: one that cannot be applied in full (an episode another chosen file took, or
// one the title no longer has) leaves the file unmatched instead of reinterpreting its name. An episode another file
// of the release took is not given twice.
//
// Only a file the resolver genuinely finds nothing for (no parse, no mapping) is left to the caller's canonical
// numbers. When the resolver's answer is authoritative but unusable, unresolved says why and the file stays
// unmatched: the arr's mapping error (several episodes match: ambiguous), episodes outside the download's (a scene or
// franchise mapping that lands elsewhere: out of scope), or episodes another file of the release took (conflict).
// Falling back to the canonical numbers there would link the file as an episode it is not (an XEM swap: scene
// S01E01 is canonical E02, and its canonical reading E01 is the wrong episode).
func (r *resolver) episodes(rel string, taken map[int64]bool) (eps []episodeInfo, how, unresolved string, err error) {
	pick := func(ids []int64, from map[int64]episodeInfo) []episodeInfo {
		var out []episodeInfo
		for _, id := range ids {
			if e, ok := from[id]; ok && !taken[id] {
				out = append(out, e)
			}
		}
		return out
	}
	if f, ok := r.d.Manual[rel]; ok && f.How == HowReplaced {
		return nil, HowManual, "replaced: Change episodes gave the episodes this file held to another file", nil
	} else if ok && len(f.Episodes) > 0 {
		eps := pick(f.Episodes, r.all)
		if len(eps) != len(slices.Compact(slices.Sorted(slices.Values(f.Episodes)))) {
			return nil, HowManual, "", nil
		}
		return eps, HowManual, "", nil
	}
	if f, ok := r.d.Recorded[rel]; ok {
		if eps := pick(f.Episodes, r.all); len(eps) > 0 {
			return eps, f.How, "", nil
		}
	}
	sv := ""
	if p := r.d.Version.Policy; p != nil {
		sv = p.Sonarr
	}
	res, err := decide.ResolveFile(r.title, sv, decide.FileInput{Path: rel, Release: r.d.ReleaseTitle,
		Folder: r.d.Name, OtherVideoFiles: r.pack, Mapping: r.d.EpisodeMap})
	if err != nil {
		return nil, "", "ambiguous: " + err.Error(), nil
	}
	if res.Refused != "" {
		return nil, res.How, "refused: " + res.Refused, nil
	}
	if len(res.EpisodeIDs) == 0 {
		return nil, "", "", nil // nothing authoritative: the canonical numbers decide
	}
	var outside, other []string
	for _, id := range res.EpisodeIDs {
		switch {
		case taken[id]:
			other = append(other, r.label(id))
		case r.allowed[id].ID == 0:
			outside = append(outside, r.label(id))
		}
	}
	switch {
	case len(outside) > 0:
		return nil, res.How, "out of scope: maps to " + strings.Join(outside, ", ") + " (" + res.How +
			"), not an episode of the download", nil
	case len(other) > 0:
		return nil, res.How, "conflict: " + strings.Join(other, ", ") + " is another file's", nil
	}
	return pick(res.EpisodeIDs, r.allowed), res.How, "", nil
}

// label names an episode of the title as S01E02 (its id when the title has no such episode).
func (r *resolver) label(id int64) string {
	for _, e := range r.title.Episodes {
		if e.ID == id {
			return fmt.Sprintf("S%02dE%02d", e.Season, e.Number)
		}
	}
	return fmt.Sprintf("episode %d", id)
}
