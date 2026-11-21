package decide

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/format"
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
		if t.AnimeMap != nil {
			// A batch the franchise map (or an alternative cut) places is that engine's, never a span.
			if _, _, _, ok, _, err := s.franchiseEpisodes(p, rel.Title, t.AnimeMap); err != nil || ok {
				return false
			}
		}
		_, _, sceneSource := s.seasonMapping(p.SeriesTitle, p.SeasonNumber)
		byNumber, err := s.animeByNumber(p, sceneSource)
		var eps []*Episode
		for _, x := range byNumber {
			eps = append(eps, x...)
		}
		return err == nil && s.absoluteClaim(p, byNumber, eps, true).Span()
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
// more seasons (today's mapping, whatever places it: scene absolute, then absolute; byNumber as the mapping gave it):
// two or more numbers, not a special, no season marker in the name, every number mapped to exactly one regular
// episode, the numbers' episodes exactly the release's (eps: nothing else replaced them). It is a span (Seasons) only when those episodes are every episode of
// each season they touch and span is asked for. nil when the batch makes no such claim.
func (s *series) absoluteClaim(p *sonarr.ParsedEpisodeInfo, byNumber [][]*Episode, eps []*Episode, span bool) *PackClaim {
	if len(p.AbsoluteEpisodeNumbers) < 2 || len(byNumber) != len(p.AbsoluteEpisodeNumbers) || p.Special ||
		len(p.EpisodeNumbers) > 0 || seasonMarkerRE.MatchString(p.ReleaseTitle) {
		return nil
	}
	claim := &PackClaim{Mode: PackAbsolute}
	seen := map[int64]bool{}
	seasons := map[int]bool{}
	for i, n := range p.AbsoluteEpisodeNumbers {
		got := byNumber[i]
		if len(got) != 1 || got[0].Season <= 0 || seen[got[0].ID] {
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
	if c := s.absoluteClaim(p, r.byNumber, r.episodes, search.severalSeasons()); c != nil {
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

// SpanSeaDexRelease is the SeaDex standing a pack of several seasons keeps (docs/design/series.md), wherever the
// standing is refreshed by hash (deciding, the route, the import): the weakest across the episodes it is downloaded
// for (eps, by season and number), each episode resolved as an incoming file of that episode alone is (the active
// listings of the entries whose importable mapping covers it), in place of the standing of the hash as a whole (which
// a pack listed under several entries at different ranks does not have). None when one episode has none; rank (Best)
// and preference are tracked apart, Best only when it holds for every episode. The release comes back with its SeaDex
// flags set to it. Only for a library that prefers SeaDex (the caller's).
func SpanSeaDexRelease(ctx context.Context, rel indexer.Release, titleID int64, eps [][2]int) indexer.Release {
	if len(eps) == 0 {
		return rel
	}
	state := seaDexView(ctx)
	hash := indexer.NormalizeHash(rel.InfoHash)
	pref, best := 3, true
	for _, ep := range eps {
		x := resolveStanding(state, titleID, &ExistingFile{InfoHash: hash, seaDexEpisodes: [][2]int{ep}}, resolveImport)
		pref = min(pref, x.SeaDexPreference)
		best = best && x.SeaDex >= format.SeaDexBest
		if pref == 0 {
			break
		}
	}
	rel.Flags = slices.DeleteFunc(slices.Clone(rel.Flags), func(f string) bool {
		return f == indexer.FlagSeaDex || f == indexer.FlagSeaDexBest || f == indexer.FlagSeaDexPreferred
	})
	if pref > 0 {
		rel.Flags = append(rel.Flags, indexer.FlagSeaDex)
		if best {
			rel.Flags = append(rel.Flags, indexer.FlagSeaDexBest)
		}
		if pref >= 3 {
			rel.Flags = append(rel.Flags, indexer.FlagSeaDexPreferred)
		}
	}
	return rel
}

// spanNumbers are a span's episodes as (season, number).
func spanNumbers(eps []*Episode) [][2]int {
	out := make([][2]int, 0, len(eps))
	for _, e := range eps {
		out = append(out, [2]int{e.Season, e.Number})
	}
	return out
}
