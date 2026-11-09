package decide

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	animemeta "github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
)

// The anime mapping seam (docs/design/anime.md): for a series with franchise mappings (Title.AnimeMap),
// episodes come from the franchise first and from Sonarr's mapping second, and a release that maps only into
// ranges whose mapping is not reviewed (conflict, rejected) is refused.
//
//  1. An alternate cut (overrides alt_cuts) the release title names: its numbers map through the cut's table.
//  2. A franchise entry whose name is the release's series title (Sonarr's clean title), with absolute numbers: the
//     entry's own episode n is the n-th TVDB episode of its range (outside season 0). An entry that cannot hold every
//     number, or two entries that both can, leave it to Sonarr.
//  3. Otherwise Sonarr's mapping (absolute, scene, season and episode) as before.
//
// Then every mapped episode is checked: one that only conflict or rejected ranges cover is held. All held: the
// release is rejected MappingNotReviewed. Some held: the decision lists them (AnimeHeld) and the library imports the
// others only.

// Mapping sources in Decision.AnimeMapping.
const (
	AnimeMapFranchise = "franchise"
	AnimeMapAltCut    = "alt_cut"
	AnimeMapSonarr    = "sonarr"
)

// ReasonMappingNotReviewed is our rejection for a release inside unreviewed mapping ranges.
const ReasonMappingNotReviewed = "MappingNotReviewed"

// franchiseEpisodes maps a release through the franchise. ok is false when the franchise has no say (Sonarr's mapping
// stays); with ok and no episodes, the release named an alternate cut whose table lacks a number.
func (s *series) franchiseEpisodes(p *sonarr.ParsedEpisodeInfo, releaseTitle string, am *animemap.Series) (eps []*Episode, how, entry string, ok bool, err error) {
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
			return nil, AnimeMapAltCut + ":" + c.Name, c.Entry, true, nil
		}
		for _, x := range n {
			ses, has := c.Episodes[x]
			if !has {
				return nil, AnimeMapAltCut + ":" + c.Name, c.Entry, true, nil
			}
			for _, se := range ses {
				e, err := s.find(se[0], se[1])
				if err != nil {
					return nil, "", "", false, err
				}
				if e == nil {
					return nil, AnimeMapAltCut + ":" + c.Name, c.Entry, true, nil
				}
				eps = append(eps, e)
			}
		}
		return eps, AnimeMapAltCut + ":" + c.Name, c.Entry, true, nil
	}
	if len(nums) == 0 || p.FullSeason || p.Special {
		return nil, "", "", false, nil
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
			return nil, "", "", false, nil // two entries of that name hold the numbers: Sonarr decides
		}
		fit = e
	}
	if fit == nil {
		return nil, "", "", false, nil
	}
	reg := fit.Regular()
	for _, x := range nums {
		se := reg[x-1]
		e, err := s.find(se[0], se[1])
		if err != nil {
			return nil, "", "", false, err
		}
		if e == nil {
			return nil, "", "", false, nil // the series lacks the episode: Sonarr decides
		}
		eps = append(eps, e)
	}
	return eps, AnimeMapFranchise, fit.Key, true, nil
}

// applyAnimeMap runs the seam on a mapped release: it may replace r.episodes, records how they were mapped, and
// returns the MappingNotReviewed rejection when every episode is held.
func (s *series) applyAnimeMap(d *Decision, p *sonarr.ParsedEpisodeInfo, rel Release, r *remoteEpisode, am *animemap.Series) ([]Rejection, error) {
	if am == nil {
		return nil, nil
	}
	eps, how, entry, ok, err := s.franchiseEpisodes(p, rel.Title, am)
	if err != nil {
		return nil, err
	}
	// SeaDex calls each AniList entry a season pack. A TVDB season may contain several cours; its synthetic
	// Sxx title cannot claim episodes outside the entry's mapping.
	if rel.Kind == "seadex" && p.FullSeason {
		if cour, key, mapped, mapErr := s.seadexCour(rel.Extras["anilist"], p.SeasonNumber, am); mapped || mapErr != nil {
			eps, entry, ok, err = cour, key, true, mapErr
			how = AnimeMapFranchise
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
	}
	if len(r.episodes) == 0 {
		return nil, nil
	}
	var held []string
	var first string
	for _, ep := range r.episodes {
		if e, blocked := am.Blocked(ep.Season, ep.Number); blocked {
			se := animemeta.SE{ep.Season, ep.Number}
			held = append(held, se.String())
			if first == "" {
				first = animemap.Reason(se, e)
			}
		}
	}
	d.AnimeHeld = held
	if len(held) == len(r.episodes) {
		msg := first
		if len(held) > 1 {
			msg = fmt.Sprintf("every episode (%s) is in a range whose mapping is not reviewed; %s", strings.Join(held, ", "), first)
		}
		return []Rejection{{Reason: ReasonMappingNotReviewed, Ours: true, Message: msg}}, nil
	}
	return nil, nil
}

// seadexCour narrows a season pack to the regular episodes mapped to its AniList entry.
func (s *series) seadexCour(id string, season int, am *animemap.Series) (eps []*Episode, key string, mapped bool, err error) {
	n, err := strconv.Atoi(id)
	if err != nil || n <= 0 {
		return nil, "", false, nil
	}
	en, mapped := seaDexEntry(am, n)
	if en == nil {
		return nil, "", mapped, nil
	}
	key = en.Key
	seen := map[int64]bool{}
	for _, se := range en.Regular() {
		if se[0] != season {
			continue
		}
		ep, err := s.find(se[0], se[1])
		if err != nil {
			return nil, "", true, err
		}
		if ep != nil && !seen[ep.ID] {
			seen[ep.ID] = true
			eps = append(eps, ep)
		}
	}
	return eps, key, mapped, nil
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
