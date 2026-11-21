package decide

import (
	"context"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
)

// Anime packs of several seasons (docs/design/series.md, "Anime packs of several seasons"). Only a decision made
// for a search of several seasons (SeriesSearch.severalSeasons) reads one as a span: a name that states the seasons
// (indexer.AnimePackSeasons) or an absolute batch whose numbers fill two or more whole seasons. The decision stores
// what the pack claims (PackClaim, on its EpisodeMap) and the import proves the files against it before any link
// (library/animepack.go). An anime absolute batch across seasons gets a claim in every search and RSS too, with no
// span: its decision is today's, its import is proved.

// Pack claim modes.
const (
	// PackSeasons: a name that states its seasons; each file must name (or sit in a folder of) one of them.
	PackSeasons = "seasons"
	// PackAbsolute: an absolute batch; each file must carry bare absolute numbers, number n being Numbers' n.
	PackAbsolute = "absolute"
)

// PackClaim is what an anime pack claims, as decided: the mode, the span's seasons (two or more for a pack of
// several seasons; none for an absolute batch across seasons that is no span) and every regular episode it claims
// (the whole pack's, also when the download is narrowed to some seasons), with, for an absolute batch, the
// episode each number mapped to.
type PackClaim struct {
	Mode     string       `json:"mode"`
	Seasons  []int        `json:"seasons,omitempty"`
	Episodes []int64      `json:"episodes"`
	Numbers  []PackNumber `json:"numbers,omitempty"`
}

// PackNumber is one absolute number of a batch and the episode it mapped to.
type PackNumber struct {
	N       int   `json:"n"`
	Episode int64 `json:"episode"`
}

// Span says whether the claim is a pack of several seasons (bucket, cache-check order, multi_season, ranking).
func (c *PackClaim) Span() bool { return c != nil && len(c.Seasons) >= 2 }

// SeveralSeasons says whether a series decision is a pack of several seasons: an anime release by its claim (a
// span), any other by its name (indexer.HoldsSeasons), as part 1 reads it; either one mapping episodes.
func (d *Decision) SeveralSeasons() bool {
	if d == nil || len(d.EpisodeIDs) == 0 {
		return false
	}
	if d.anime {
		return d.Pack.Span()
	}
	return indexer.HoldsSeasons(d.Release)
}

// AnimeWholeCandidate says whether a result of the query without a season can be a pack of several seasons for
// anime series t under search: a name that states a span the library has, or an absolute batch that fills two or
// more whole seasons. The rest is left out before deciding.
func AnimeWholeCandidate(ctx context.Context, rel indexer.Release, t *Title, search *SeriesSearch) bool {
	s := seriesFor(ctx, t)
	p := s.parseRelease(rel, search)
	switch {
	case p == nil:
		return false
	case p.FullSeason:
		return len(s.packSeasons(p, search)) >= 2
	case len(p.AbsoluteEpisodeNumbers) >= 2:
		_, _, sceneSource := s.seasonMapping(p.SeriesTitle, p.SeasonNumber)
		eps, err := s.animeEpisodes(p, sceneSource)
		return err == nil && s.absoluteClaim(p, eps, true).Span()
	}
	return false
}

// severalSeasons says whether a decision is made for a search of several seasons: a merged search's criterion (two
// or more Seasons) or the whole-series criterion. Only there does an anime pack read as a span.
func (ss *SeriesSearch) severalSeasons() bool {
	return ss != nil && (len(ss.Seasons) >= 2 || ss.Kind == profile.SearchSeries)
}

// animeSpanSeasons is the span an anime pack's name states (indexer.AnimePackSeasons) when the library has it:
// every season from first to last has regular episodes, none of them is numbered differently by the scene (a
// scene season or number, or a scene mapping's season offset) and none sits in a franchise entry marked as
// conflicting. A tracker season that is not the library's season is where a wrong link comes from. nil otherwise.
func (s *series) animeSpanSeasons(title, seriesTitle string) []int {
	_, first, last, ok := indexer.AnimePackSeasons(title)
	if !ok {
		return nil
	}
	regular := map[int]bool{}
	for _, e := range s.episodes() {
		if e.Season < first || e.Season > last {
			continue
		}
		if e.SceneSeason != nil && *e.SceneSeason != e.Season || e.SceneNumber != nil && *e.SceneNumber != e.Number {
			return nil
		}
		if s.t.AnimeMap != nil {
			if _, blocked := s.t.AnimeMap.Blocked(e.Season, e.Number); blocked {
				return nil
			}
		}
		regular[e.Season] = true
	}
	var out []int
	for n := first; n <= last; n++ {
		if !regular[n] {
			return nil
		}
		if _, mapped, _ := s.seasonMapping(seriesTitle, n); mapped != n {
			return nil
		}
		out = append(out, n)
	}
	return out
}

// animeSpanParse is parseRelease's reading of an anime name that states a span, under a search of several seasons:
// the name with the span phrase read as "S<first>" when that parses as a clean full season of the first season (no
// episode or absolute numbers left outside the phrase), with the real name kept. nil when it does not.
func (s *series) animeSpanParse(title string) *sonarr.ParsedEpisodeInfo {
	seasons := s.animeSpanSeasons(title, "")
	if seasons == nil {
		return nil
	}
	named, first, _, _ := indexer.AnimePackSeasons(title)
	p := s.parseTitle(named)
	if p == nil || !p.FullSeason || p.SeasonNumber != first || len(p.EpisodeNumbers) > 0 ||
		len(p.AbsoluteEpisodeNumbers) > 0 || p.IsMultiSeason || strings.TrimSpace(p.SeriesTitle) == "" {
		return nil
	}
	if s.animeSpanSeasons(title, p.SeriesTitle) == nil {
		return nil
	}
	c := *p
	c.ReleaseTitle = title
	return &c
}

// absoluteClaim is the claim of an anime absolute batch whose numbers map, one each, to regular episodes of two or
// more seasons (today's mapping, whatever places it: scene absolute, then absolute): two or more numbers, not a
// special, no season marker in the name, every number mapped on its own to exactly one regular episode, the
// numbers' episodes exactly the release's. It is a span (Seasons) only when those episodes are every episode of
// each season they touch and span is asked for. nil when the batch makes no such claim.
func (s *series) absoluteClaim(p *sonarr.ParsedEpisodeInfo, eps []*Episode, span bool) *PackClaim {
	if len(p.AbsoluteEpisodeNumbers) < 2 || p.Special || len(p.EpisodeNumbers) > 0 ||
		seasonMarkerRE.MatchString(p.ReleaseTitle) {
		return nil
	}
	_, _, sceneSource := s.seasonMapping(p.SeriesTitle, p.SeasonNumber)
	claim := &PackClaim{Mode: PackAbsolute}
	seen := map[int64]bool{}
	seasons := map[int]bool{}
	for _, n := range p.AbsoluteEpisodeNumbers {
		one := *p
		one.AbsoluteEpisodeNumbers = []int{n}
		got, err := s.animeEpisodes(&one, sceneSource)
		if err != nil || len(got) != 1 || got[0].Season <= 0 || seen[got[0].ID] {
			return nil
		}
		seen[got[0].ID] = true
		seasons[got[0].Season] = true
		claim.Numbers = append(claim.Numbers, PackNumber{N: n, Episode: got[0].ID})
		claim.Episodes = append(claim.Episodes, got[0].ID)
	}
	if len(seasons) < 2 || len(eps) != len(seen) || slices.ContainsFunc(eps, func(e *Episode) bool { return !seen[e.ID] }) {
		return nil
	}
	if !span {
		return claim
	}
	for _, e := range s.episodes() {
		if seasons[e.Season] && !seen[e.ID] {
			return claim // not whole seasons: a claim, no span
		}
	}
	for n := range seasons {
		claim.Seasons = append(claim.Seasons, n)
	}
	sort.Ints(claim.Seasons)
	return claim
}

// claimPack sets an anime release's pack claim (Decision.Pack) from its mapping: a named span (r.seasons, kept only
// while its episodes still hold each of its seasons), else an absolute batch placed by the plain numbering (not by
// a franchise entry, an alternate cut or a SeaDex mapping; nor a SeaDex feed result), whose span sets r.seasons
// under a search of several seasons.
func (s *series) claimPack(d *Decision, p *sonarr.ParsedEpisodeInfo, rel indexer.Release, r *remoteEpisode,
	search *SeriesSearch) {
	if rel.Kind == "seadex" {
		r.seasons = nil
		return
	}
	if len(r.seasons) >= 2 {
		for _, n := range r.seasons {
			if !slices.ContainsFunc(r.episodes, func(e *Episode) bool { return e.Season == n }) {
				r.seasons = nil // another engine placed it elsewhere: no span
				return
			}
		}
		d.Pack = seasonsClaim(r.seasons, r.episodes)
		return
	}
	if d.AnimeMapping != "" && d.AnimeMapping != AnimeMapSonarr {
		return
	}
	if c := s.absoluteClaim(p, r.episodes, search.severalSeasons()); c != nil {
		d.Pack = c
		if c.Span() {
			r.seasons = c.Seasons
		}
	}
}

// spanDownload is a span's episodes this search downloads: the searched seasons' of a merged search (scoped), else
// all.
func spanDownload(r *remoteEpisode, search *SeriesSearch) []*Episode {
	if !scoped(search, r) {
		return r.episodes
	}
	var out []*Episode
	for _, e := range r.episodes {
		if slices.Contains(search.Seasons, e.Season) {
			out = append(out, e)
		}
	}
	return out
}

// seasonsClaim is the claim of a named span: every regular episode of its seasons by the library's numbering.
func seasonsClaim(seasons []int, eps []*Episode) *PackClaim {
	claim := &PackClaim{Mode: PackSeasons, Seasons: slices.Clone(seasons)}
	for _, e := range eps {
		if e.Season > 0 {
			claim.Episodes = append(claim.Episodes, e.ID)
		}
	}
	return claim
}

// spanSeaDex is the SeaDex standing a span keeps (docs/design/series.md): the weakest across the episodes it is
// downloaded for. None when any of them is not covered by an entry SeaDex actively lists the torrent under (the
// mark is per hash, so a pack listed for season 1 only would carry season 1's standing into the others), else the
// lowest standing among them. It never raises the release's own. The release is returned with its SeaDex flags
// set to that standing.
func (s *series) spanSeaDex(st seaDexState, rel indexer.Release, eps []*Episode) indexer.Release {
	pref := indexer.SeaDexPreference(&rel)
	if pref == 0 {
		return rel
	}
	type standing struct{ pref int }
	byEntry := map[int]standing{}
	for _, m := range st.hashes[indexer.NormalizeHash(rel.InfoHash)] {
		if m.torrent.Preference > 0 && m.torrent.MissingSince == 0 {
			if m.torrent.Preference > byEntry[m.entry].pref {
				byEntry[m.entry] = standing{m.torrent.Preference}
			}
		}
	}
	if len(byEntry) == 0 {
		// No stored standing for the hash: the entries the search's own answer listed it under, at its own standing.
		ids := indexer.SeaDexEntries(rel.Extras[indexer.ExtraSeaDexEntries])
		if id, err := strconv.Atoi(rel.Extras["anilist"]); err == nil && id > 0 && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
		for _, id := range ids {
			byEntry[id] = standing{pref}
		}
	}
	low := pref
	for _, e := range eps {
		var got standing
		if am := s.t.AnimeMap; am != nil {
			for _, en := range am.Covering(e.Season, e.Number) {
				id, ok := anilistID(en.Key)
				if x, listed := byEntry[id]; ok && listed && x.pref > got.pref {
					got = x
				}
			}
		}
		if got.pref == 0 {
			low = 0
			break
		}
		low = min(low, got.pref)
	}
	rel.Flags = slices.DeleteFunc(slices.Clone(rel.Flags), func(f string) bool {
		return f == indexer.FlagSeaDex || f == indexer.FlagSeaDexBest || f == indexer.FlagSeaDexPreferred
	})
	if low > 0 {
		rel.Flags = append(rel.Flags, indexer.FlagSeaDex)
	}
	if low >= 2 {
		rel.Flags = append(rel.Flags, indexer.FlagSeaDexBest)
	}
	if low >= 3 {
		rel.Flags = append(rel.Flags, indexer.FlagSeaDexPreferred)
	}
	return rel
}

// anilistID reads a franchise entry key "al:<id>" or "al:<id>#<n>".
func anilistID(key string) (int, bool) {
	rest, ok := strings.CutPrefix(key, "al:")
	if !ok {
		return 0, false
	}
	rest, _, _ = strings.Cut(rest, "#")
	id, err := strconv.Atoi(rest)
	return id, err == nil && id > 0
}
