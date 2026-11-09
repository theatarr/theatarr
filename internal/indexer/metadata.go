package indexer

import (
	"context"
	"errors"
)

// Lookups some sources need, filled in by the service so callers don't have to (seadexerr does its
// own anibridge AniList ids for SeaDex; Torrentio pack sizes need TMDb episode counts).

// AniListResolver maps a movie (TMDb or IMDb id) or a series season (TVDB id and season) to the AniList ids SeaDex
// files it under. No mapping is an empty answer, not an error.
type AniListResolver interface {
	AniListIDs(ctx context.Context, q Query) ([]int, error)
}

// EpisodeCounter returns the regular episodes per season of a series (season 0 for specials), from metadata.
type EpisodeCounter interface {
	EpisodeCounts(ctx context.Context, q Query) (map[int]int, error)
}

// AniListFunc adapts a function to AniListResolver.
type AniListFunc func(ctx context.Context, q Query) ([]int, error)

func (f AniListFunc) AniListIDs(ctx context.Context, q Query) ([]int, error) { return f(ctx, q) }

// EpisodeCountsFunc adapts a function to EpisodeCounter.
type EpisodeCountsFunc func(ctx context.Context, q Query) (map[int]int, error)

func (f EpisodeCountsFunc) EpisodeCounts(ctx context.Context, q Query) (map[int]int, error) {
	return f(ctx, q)
}

// AniListTarget is a series season (TVDB id and season) or a movie (TMDb id) an AniList entry is filed under.
// Exact marks a season named only because exact episode mappings put some of the entry's episodes in it: it files
// the entry under the season (the recent feed, stored-standing provenance) but never covers the whole season; the
// episodes it covers come from the mappings themselves. Stored targets keep the mark.
type AniListTarget struct {
	TVDB   int
	Season int
	TMDb   int
	Exact  bool `json:",omitempty"`
}

// valid says whether the target names a title at all.
func (t AniListTarget) valid() bool { return t.TVDB > 0 || t.TMDb > 0 }

// AniListMapper maps an AniList id back to the series seasons and movies it is filed under (seadexerr's reverse
// anibridge index, mapping.rs resolve_tvdb_mappings and resolve_tmdb_id). No mapping is an empty answer.
type AniListMapper interface {
	AniListTargets(ctx context.Context, anilist int) ([]AniListTarget, error)
}

// TitleNamer names a title by its ids (a series by TVDB id, a movie by TMDb id): the name and year seadexerr asks
// Sonarr and Radarr for. ok is false for a title we do not hold.
type TitleNamer interface {
	TitleName(ctx context.Context, q Query) (name string, year int, ok bool, err error)
}

// TorrentNamer returns provider-reported torrent names for an exact hash already held in the registry.
type TorrentNamer interface {
	TorrentNames(ctx context.Context, hash string) ([]string, error)
}

// Metadata holds the lookups. Any may be nil.
type Metadata struct {
	AniList  AniListResolver
	Episodes EpisodeCounter
	// Targets and Names let SeaDex file its recent feed under our titles (seadexerr's generic search), and name a
	// search's releases when the query has no title.
	Targets  AniListMapper
	Names    TitleNamer
	Torrents TorrentNamer
}

// SetMetadata gives the sources that need them their lookups: SeaDex resolves AniList ids for queries without one,
// Torrentio counts a series' episodes for queries without EpisodeCounts. Call it before the first search.
func (s *Service) SetMetadata(m Metadata) {
	for _, sr := range s.searchers {
		switch x := sr.(type) {
		case *seadex:
			x.resolver, x.targets, x.names = m.AniList, m.Targets, m.Names
			x.torrents = m.Torrents
		case *torrentio:
			x.counter = m.Episodes
		}
	}
}

// FirstAniList asks each resolver in turn and returns the first non-empty answer. Errors are returned only when no
// resolver answered.
func FirstAniList(rs ...AniListResolver) AniListResolver {
	return AniListFunc(func(ctx context.Context, q Query) ([]int, error) {
		var errs []error
		for _, r := range rs {
			if r == nil {
				continue
			}
			ids, err := r.AniListIDs(ctx, q)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if len(ids) > 0 {
				return ids, nil
			}
		}
		return nil, errors.Join(errs...)
	})
}

// FirstEpisodeCounts asks each counter in turn and returns the first non-empty answer, like FirstAniList.
func FirstEpisodeCounts(cs ...EpisodeCounter) EpisodeCounter {
	return EpisodeCountsFunc(func(ctx context.Context, q Query) (map[int]int, error) {
		var errs []error
		for _, c := range cs {
			if c == nil {
				continue
			}
			m, err := c.EpisodeCounts(ctx, q)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if len(m) > 0 {
				return m, nil
			}
		}
		return nil, errors.Join(errs...)
	})
}

// FirstAniListTargets asks each mapper in turn and returns the first non-empty answer, like FirstAniList.
func FirstAniListTargets(ms ...AniListMapper) AniListMapper {
	return aniListTargetsFunc(func(ctx context.Context, anilist int) ([]AniListTarget, error) {
		var errs []error
		for _, m := range ms {
			if m == nil {
				continue
			}
			ts, err := m.AniListTargets(ctx, anilist)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if len(ts) > 0 {
				return ts, nil
			}
		}
		return nil, errors.Join(errs...)
	})
}

type aniListTargetsFunc func(ctx context.Context, anilist int) ([]AniListTarget, error)

func (f aniListTargetsFunc) AniListTargets(ctx context.Context, anilist int) ([]AniListTarget, error) {
	return f(ctx, anilist)
}
