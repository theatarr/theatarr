package decide

import (
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	animemeta "github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
)

// The anime mapping seam (docs/design/anime.md): for a series with franchise mappings (Title.AnimeMap),
// episodes come from the franchise first and from Sonarr's mapping second.
//
//  1. An alternate cut (overrides alt_cuts) the release title names: its numbers map through the cut's table.
//  2. A franchise entry whose name is the release's series title (Sonarr's clean title), with absolute numbers: the
//     entry's own episode n is the n-th TVDB episode of its range (outside season 0). An entry that cannot hold every
//     number, or two entries that both can, leave it to Sonarr.
//  3. Otherwise Sonarr's mapping (absolute, scene, season and episode) as before. A release only a franchise name
//     matched (Sonarr alone would not know the series) gets Sonarr's resolution for the known series, as its import
//     would: series-wide absolute numbers, scene and XEM mappings.
//
// A franchise mapping that is not importable (conflict, or rejected in review) is never used: a release that names
// such an entry (step 2, or a SeaDex pack's entry) maps through Sonarr instead, and so does an episode that only such
// ranges cover. Nothing is held; the decision lists those episodes (AnimeFallback) with the entry and its status,
// and the review item waits in Admin › Reviews. The best-scoring candidate of a conflict is never imported under.
// The entry a release names (step 2) stays on the decision (AnimeEntry, and EpisodeMap.Fallback on the download), so
// an accept before the import re-scopes the download and one after it re-links the files through the entry.

// Mapping sources in Decision.AnimeMapping.
const (
	AnimeMapFranchise = "franchise"
	AnimeMapAltCut    = "alt_cut"
	AnimeMapSonarr    = "sonarr"
)

// AnimeFallback is an episode that used Sonarr's numbering because its franchise mapping is not importable: the
// TVDB episode, the entry and the mapping's status (conflict, rejected).
type AnimeFallback struct {
	Episode string `json:"episode"`
	Entry   string `json:"entry"`
	Status  string `json:"status"`
}

// franchiseEpisodes maps a release through the franchise. ok is false when the franchise has no say (Sonarr's mapping
// stays); with ok and no episodes, the release named an alternate cut whose table lacks a number. skipped is the
// entry the release named whose mapping is not importable (Sonarr's mapping stays).
func (s *series) franchiseEpisodes(p *sonarr.ParsedEpisodeInfo, releaseTitle string, am *animemap.Series) (eps []*Episode, how, entry string, ok bool, skipped *animemap.Entry, err error) {
	nums := p.AbsoluteEpisodeNumbers
	for _, c := range am.AltCuts {
		if !c.Matches(releaseTitle) {
			continue
		}
		n := nums
		if len(n) == 0 {
			n = p.EpisodeNumbers
		}
		if len(n) == 0 {
			return nil, AnimeMapAltCut + ":" + c.Name, c.Entry, true, nil, nil
		}
		for _, x := range n {
			ses, has := c.Episodes[x]
			if !has {
				return nil, AnimeMapAltCut + ":" + c.Name, c.Entry, true, nil, nil
			}
			for _, se := range ses {
				e, err := s.find(se[0], se[1])
				if err != nil {
					return nil, "", "", false, nil, err
				}
				if e == nil {
					return nil, AnimeMapAltCut + ":" + c.Name, c.Entry, true, nil, nil
				}
				eps = append(eps, e)
			}
		}
		return eps, AnimeMapAltCut + ":" + c.Name, c.Entry, true, nil, nil
	}
	if len(nums) == 0 || p.FullSeason || p.Special {
		return nil, "", "", false, nil, nil
	}
	clean := s.ver.CleanSeriesTitle(p.SeriesTitle)
	var fit *animemap.Entry
	for _, e := range am.Entries {
		named := false
		for _, n := range e.Names {
			if s.ver.CleanSeriesTitle(n) == clean {
				named = true
				break
			}
		}
		if !named {
			continue
		}
		reg := e.Regular()
		holds := len(reg) > 0
		for _, x := range nums {
			holds = holds && x >= 1 && x <= len(reg)
		}
		if !holds {
			continue
		}
		if fit != nil {
			return nil, "", "", false, nil, nil // two entries of that name hold the numbers: Sonarr decides
		}
		fit = e
	}
	if fit == nil {
		return nil, "", "", false, nil, nil
	}
	if !fit.Importable() {
		return nil, "", "", false, fit, nil // not reviewed: Sonarr's numbering, never the conflict's candidate
	}
	reg := fit.Regular()
	for _, x := range nums {
		se := reg[x-1]
		e, err := s.find(se[0], se[1])
		if err != nil {
			return nil, "", "", false, nil, err
		}
		if e == nil {
			return nil, "", "", false, nil, nil // the series lacks the episode: Sonarr decides
		}
		eps = append(eps, e)
	}
	return eps, AnimeMapFranchise, fit.Key, true, nil, nil
}

// applyAnimeMap runs the seam on a mapped release: it may replace r.episodes, records how they were mapped, and lists
// the episodes that used Sonarr's numbering because their franchise mapping is not importable. It rejects nothing.
func (s *series) applyAnimeMap(d *Decision, p *sonarr.ParsedEpisodeInfo, rel Release, r *remoteEpisode, am *animemap.Series,
	search *SeriesSearch) ([]Rejection, error) {
	if am == nil {
		return nil, nil
	}
	eps, how, entry, ok, skipped, err := s.franchiseEpisodes(p, rel.Title, am)
	if err != nil {
		return nil, err
	}
	named := skipped // the entry the release title names, whose mapping is not importable
	// SeaDex calls each AniList entry a season pack. A TVDB season may contain several cours; its synthetic
	// Sxx title cannot claim episodes outside the entry's mapping.
	if rel.Kind == "seadex" && p.FullSeason {
		cour, key, mapped, notReviewed, mapErr := s.seadexCour(rel.Extras["anilist"], p.SeasonNumber, am)
		if mapped || mapErr != nil {
			eps, entry, ok, err = cour, key, true, mapErr
			how = AnimeMapFranchise
		} else if notReviewed != nil {
			skipped = notReviewed
		}
	}
	if err != nil {
		return nil, err
	}
	if ok {
		r.episodes = eps
		d.AnimeMapping, d.AnimeEntry = how, entry
	} else {
		d.AnimeMapping = AnimeMapSonarr
		if named != nil {
			d.AnimeEntry = named.Key // recognised, not used: a later accept re-scopes or re-links through it
		}
		if r.matchType == MatchAlias && (skipped != nil || len(r.episodes) == 0) {
			// Sonarr's numbering for a release only a franchise name matched, when the franchise gives no placement
			// (its entry is not importable or cannot hold the numbers): the series is known, so Sonarr's episode
			// resolution runs as an import of the series would (series-wide absolute numbers, scene and XEM mappings).
			known, err := s.mapKnown(p, search)
			if err != nil {
				return nil, err
			}
			r.mappedSeason, r.noMappedSeason, r.sceneMapping, r.episodes = known.mappedSeason, known.noMappedSeason,
				known.sceneMapping, known.episodes
		}
	}
	var fallback []AnimeFallback
	for _, ep := range r.episodes {
		se := animemeta.SE{ep.Season, ep.Number}.String()
		if skipped != nil {
			fallback = append(fallback, AnimeFallback{Episode: se, Entry: skipped.Key, Status: skipped.Status})
		} else if e, blocked := am.Blocked(ep.Season, ep.Number); blocked {
			fallback = append(fallback, AnimeFallback{Episode: se, Entry: e.Key, Status: e.Status})
		}
	}
	d.AnimeFallback = fallback
	return nil, nil
}

// seadexCour narrows a season pack to the regular episodes mapped to its AniList entry. notReviewed is the entry
// when its mapping (or a split part's) is not importable: the pack is not narrowed (Sonarr's numbering).
func (s *series) seadexCour(id string, season int, am *animemap.Series) (eps []*Episode, key string, mapped bool, notReviewed *animemap.Entry, err error) {
	n, err := strconv.Atoi(id)
	if err != nil || n <= 0 {
		return nil, "", false, nil, nil
	}
	en, mapped := seaDexEntry(am, n)
	if en == nil {
		return nil, "", mapped, nil, nil
	}
	prefix := "al:" + id
	for _, part := range am.Entries {
		if base, _, _ := strings.Cut(part.Key, "#"); base == prefix && !part.Importable() {
			return nil, "", false, part, nil
		}
	}
	key = en.Key
	if key == "" {
		key = prefix // a union of split parts: the AniList entry they share
	}
	seen := map[int64]bool{}
	for _, se := range en.Regular() {
		if se[0] != season {
			continue
		}
		ep, err := s.find(se[0], se[1])
		if err != nil {
			return nil, "", true, nil, err
		}
		if ep != nil && !seen[ep.ID] {
			seen[ep.ID] = true
			eps = append(eps, ep)
		}
	}
	return eps, key, mapped, nil, nil
}

// seaDexEntry unions disjoint split parts; an exact key beside parts or overlapping parts is ambiguous.
// mapped distinguishes missing evidence from an ambiguous id, which must fail closed.
func seaDexEntry(am *animemap.Series, id int) (entry *animemap.Entry, mapped bool) {
	if am == nil || id <= 0 {
		return nil, false
	}
	prefix := "al:" + strconv.Itoa(id)
	seen := map[animemeta.SE]bool{}
	exact := false
	for _, en := range am.Entries {
		base, _, _ := strings.Cut(en.Key, "#")
		if base != prefix {
			continue
		}
		if mapped && (exact || en.Key == prefix) {
			return nil, true
		}
		for _, se := range en.Episodes {
			if seen[se] {
				return nil, true
			}
		}
		for _, se := range en.Episodes {
			seen[se] = true
		}
		if !mapped {
			merged := *en
			merged.Episodes = append([]animemeta.SE(nil), en.Episodes...)
			entry = &merged
		} else {
			entry.Key = "" // a union has no single entry key for ordinal file mapping
			entry.Episodes = append(entry.Episodes, en.Episodes...)
		}
		mapped, exact = true, en.Key == prefix
	}
	return
}
