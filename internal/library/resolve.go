package library

import (
	"context"
	"fmt"
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
// router did not narrow down). A manual or recorded choice may name any episode of the title. A download grabbed
// through a franchise entry whose mapping is no longer importable (rejected or in conflict since the grab) maps its
// files through Sonarr's numbering, so its episodes are reconciled first (rescope).

// rescope reconciles the download's episodes when the franchise mapping it was grabbed under is abandoned: they
// become the episodes Sonarr's numbering gives the release title (the decision's, which now skips the entry), else,
// for a release title Sonarr cannot place (a pack named by the entry), the episodes it gives the release's files.
// When neither places anything the episodes stay. The stored mapping is kept: a later accept re-links the imported
// files through it (animerelink.go).
func (l *Library) rescope(ctx context.Context, d *importDownload, files []PlannedFile) error {
	v := &d.Version
	if d.EpisodeMap == nil || l.engine == nil || d.rescoped != nil {
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
	if !d.EpisodeMap.Abandoned(t.AnimeMap) {
		return nil
	}
	dec, err := l.engine.Decide(ctx, decide.Release{Release: indexer.Release{Title: d.ReleaseTitle, InfoHash: d.InfoHash,
		Indexer: d.Indexer}}, t, decide.Version{ID: v.VersionID, Key: v.Key, Policy: v.Policy}, nil)
	if err != nil {
		return err
	}
	var ids []int64
	if dec != nil {
		ids = append(ids, dec.EpisodeIDs...)
	}
	if len(ids) == 0 {
		sv := ""
		if v.Policy != nil {
			sv = v.Policy.Sonarr
		}
		for _, f := range files {
			res, err := decide.ResolveFile(&t, sv, decide.FileInput{Path: f.Rel, Release: d.ReleaseTitle, Folder: d.Name,
				OtherVideoFiles: len(files) > 1, Mapping: d.EpisodeMap})
			if err == nil && res.Refused == "" {
				ids = append(ids, res.EpisodeIDs...)
			}
		}
	}
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	var cur []int64
	for _, e := range v.Episodes {
		cur = append(cur, e.ID)
	}
	slices.Sort(cur)
	if len(ids) == 0 || slices.Equal(cur, ids) {
		return nil
	}
	eps, err := loadEpisodes(ctx, db, v.Title.ID, ids)
	if err != nil {
		return err
	}
	l.log.Info("library: franchise mapping no longer importable, episodes through Sonarr's numbering", "download", d.ID,
		"entry", d.EpisodeMap.Entry, "episodes", ids)
	v.Episodes, d.rescoped = eps, ids
	return nil
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
