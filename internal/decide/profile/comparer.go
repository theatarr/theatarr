package profile

// Port of Radarr src/NzbDrone.Core/DecisionEngine/{DownloadDecisionComparer,DownloadDecisionPriorizationService}.cs
// @ a96bf7c and the Sonarr files @ cab419a.
//
// Radarr: quality, format score, protocol, indexer priority, indexer flags (when preferred), peers (torrents),
// age (usenet), size.
// Sonarr: quality, format score, protocol, episode count (season packs first; anime prefers more episodes, others
// fewer), lowest episode number, indexer priority, peers, age, size.
//
// Theatarr adds one step the arrs don't have, cache standing (compareCachedAny, compareCachedFirst, compareCachedCount): for Radarr right after the format score,
// for Sonarr after the lowest episode number (a season pack still wins at an equal score).

import (
	"math"
	"sort"

	"github.com/theatarr/theatarr/internal/decide/format"
)

// Compare is DownloadDecisionComparer.Compare: > 0 when x is preferred over y.
func (e *Engine) Compare(x, y *Subject) int {
	c, _ := e.compareStep(x, y)
	return c
}

// The comparer's steps, as compareStep names the one that decided (Explain words it).
const (
	StepSeaDex          = "seadex"
	StepQuality         = "quality"
	StepRevision        = "revision" // with an equal quality: a proper, repack or REAL
	StepScore           = "score"
	StepCached          = "cached"       // cached against not cached
	StepCachedFirst     = "cached_first" // cached on an earlier provider of the version's list
	StepCachedCount     = "cached_count" // cached on more of them
	StepProtocol        = "protocol"
	StepEpisodeCount    = "episode_count"
	StepEpisodeNumber   = "episode_number"
	StepIndexerPriority = "indexer_priority"
	StepIndexerFlags    = "indexer_flags"
	StepSeeders         = "seeders"
	StepPeers           = "peers"
	StepAge             = "age"
	StepSize            = "size"
	StepPack            = "pack"   // merged search: a pack before any other release
	StepScoped          = "scoped" // merged search: more of the searched episodes
)

type compareFunc struct {
	step string
	cmp  func(x, y *Subject) int
}

// compareStep is Compare's walk: the order, and the step that decided it ("" for a tie).
func (e *Engine) compareStep(x, y *Subject) (int, string) {
	if c, ok := SeaDexOrder(e.Config.PreferSeaDex, x.Release.Quality.SeaDexPreference, y.Release.Quality.SeaDexPreference); ok {
		return c, StepSeaDex
	}
	quality := []compareFunc{{StepQuality, e.compareQualityIndex}, {StepRevision, e.compareRevision}}
	cached := []compareFunc{{StepCached, compareCachedAny}, {StepCachedFirst, compareCachedFirst}, {StepCachedCount, compareCachedCount}}
	peers := []compareFunc{{StepSeeders, e.compareSeeders}, {StepPeers, e.comparePeerCount}}
	var cmps []compareFunc
	if e.Flavour == format.Sonarr {
		cmps = append(append(append(append(quality, compareFunc{StepScore, compareFormatScore}, compareFunc{StepProtocol, e.compareProtocol},
			compareFunc{StepEpisodeCount, compareEpisodeCount}, compareFunc{StepEpisodeNumber, compareEpisodeNumber}), cached...),
			compareFunc{StepIndexerPriority, compareIndexerPriority}), peers...)
	} else {
		cmps = append(append(append(append(quality, compareFunc{StepScore, compareFormatScore}), cached...),
			compareFunc{StepProtocol, e.compareProtocol}, compareFunc{StepIndexerPriority, compareIndexerPriority},
			compareFunc{StepIndexerFlags, e.compareIndexerFlags}), peers...)
	}
	cmps = append(cmps, compareFunc{StepAge, e.compareAge}, compareFunc{StepSize, e.compareSize})
	for _, c := range cmps {
		if r := c.cmp(x, y); r != 0 {
			return r, c.step
		}
	}
	return 0, ""
}

// compareMerged is Compare for the releases of a merged search of several seasons (Theatarr): quality, format score
// and protocol as Compare; then a pack (a full season or a span of several seasons) before any other release; then,
// between packs only, a release with cache evidence before one without, then more scoped episodes (scoped: what the
// release is for in this search) first; everything else in Compare's order. Pack status is its own step, so the order
// is a proper one (Compare's season-pack step reads FullSeason only).
func (e *Engine) compareMerged(x, y *Subject, scoped func(*Subject) int) int {
	c, _ := e.compareMergedStep(x, y, scoped)
	return c
}

// compareMergedStep is compareMerged's walk: the order and the step that decided it.
func (e *Engine) compareMergedStep(x, y *Subject, scoped func(*Subject) int) (int, string) {
	if c, ok := SeaDexOrder(e.Config.PreferSeaDex, x.Release.Quality.SeaDexPreference, y.Release.Quality.SeaDexPreference); ok {
		return c, StepSeaDex
	}
	for _, c := range []compareFunc{{StepQuality, e.compareQualityIndex}, {StepRevision, e.compareRevision},
		{StepScore, compareFormatScore}, {StepProtocol, e.compareProtocol}} {
		if r := c.cmp(x, y); r != 0 {
			return r, c.step
		}
	}
	if c := cmpBool(x.Release.pack(), y.Release.pack()); c != 0 {
		return c, StepPack
	}
	if x.Release.pack() {
		if c := compareCachedAny(x, y); c != 0 {
			return c, StepCached
		}
		if c := cmpInt(scoped(x), scoped(y)); c != 0 {
			return c, StepScoped
		}
	}
	return e.compareStep(x, y)
}

// PrioritizeMerged is Prioritize with compareMerged's order.
func (e *Engine) PrioritizeMerged(subjects []*Subject, scoped func(*Subject) int) []*Subject {
	out := append([]*Subject(nil), subjects...)
	sort.SliceStable(out, func(i, j int) bool { return e.compareMerged(out[i], out[j], scoped) > 0 })
	return out
}

// Prioritize is DownloadDecisionPriorizationService for the releases of one title: ordered best first by
// Compare, keeping the input order among equals (OrderByDescending is stable).
func (e *Engine) Prioritize(subjects []*Subject) []*Subject {
	out := append([]*Subject(nil), subjects...)
	sort.SliceStable(out, func(i, j int) bool { return e.Compare(out[i], out[j]) > 0 })
	return out
}

// compareQualityIndex and compareRevision are the arr's quality comparison in its two steps: the quality, then (with
// propers and repacks preferred) the revision.
func (e *Engine) compareQualityIndex(x, y *Subject) int {
	qi := func(s *Subject) QualityIndex { return s.Profile.GetIndex(s.Release.Quality.Quality.ID, false) }
	// QualityIndex's IComparable compares with respectGroupOrder = true; GetIndex leaves GroupIndex 0 here.
	return qi(x).CompareTo(qi(y), true)
}

func (e *Engine) compareRevision(x, y *Subject) int {
	if e.Config.DownloadPropersAndRepacks == DoNotPrefer {
		return 0
	}
	return x.Release.Quality.Revision.CompareTo(y.Release.Quality.Revision)
}

func compareFormatScore(x, y *Subject) int {
	return cmpInt(x.Release.FormatScore, y.Release.FormatScore)
}

// compareCached (Theatarr): among releases the arr would tie this far, a debrid release cached on more of the
// version's providers has fallbacks when one fails. Cached beats not cached; then cached on an earlier provider of the
// version's list wins (Cached.First, 0 with no provider preference), then cached on more of them. With rd, ad, tb:
// rd+ad+tb, rd, ad+tb, ad, tb, none. Releases without evidence keep their order among themselves.
// It is three steps: cached or not (compareCachedAny), the earlier provider (compareCachedFirst), more providers
// (compareCachedCount); the last two only between releases both cached.
func compareCachedAny(x, y *Subject) int { return cmpBool(x.Cached.Count > 0, y.Cached.Count > 0) }

func compareCachedFirst(x, y *Subject) int {
	if x.Cached.Count == 0 || y.Cached.Count == 0 || x.Cached.First == y.Cached.First {
		return 0
	}
	return -cmpInt(x.Cached.First, y.Cached.First)
}

func compareCachedCount(x, y *Subject) int {
	if x.Cached.Count == 0 || y.Cached.Count == 0 {
		return 0
	}
	return cmpInt(x.Cached.Count, y.Cached.Count)
}

func (e *Engine) compareProtocol(x, y *Subject) int {
	px := x.Release.Protocol == e.Config.PreferredProtocol
	py := y.Release.Protocol == e.Config.PreferredProtocol
	return cmpBool(px, py)
}

func compareIndexerPriority(x, y *Subject) int {
	return -cmpInt(x.Release.IndexerPriority, y.Release.IndexerPriority)
}

func (e *Engine) compareIndexerFlags(x, y *Subject) int {
	if !e.Config.PreferIndexerFlags {
		return 0
	}
	return cmpInt(scoreFlags(x.Release.IndexerFlags), scoreFlags(y.Release.IndexerFlags))
}

// scoreFlags is Radarr's ScoreFlags: freeleech, double upload, PTP golden/approved and internal score 2;
// halfleech 1.
func scoreFlags(flags int) int {
	const (
		freeleech    = 1
		halfleech    = 2
		doubleUpload = 4
		ptpGolden    = 8
		ptpApproved  = 16
		internal     = 32
	)
	score := 0
	for _, v := range []int{freeleech, doubleUpload, ptpGolden, ptpApproved, internal} {
		if flags&v == v {
			score += 2
		}
	}
	if flags&halfleech == halfleech {
		score++
	}
	return score
}

// compareSeeders and comparePeerCount are the arr's peers comparison in its two steps (torrents only): seeders,
// then peers, each by its rounded order of magnitude.
func (e *Engine) compareSeeders(x, y *Subject) int {
	if x.Release.Protocol != ProtocolTorrent || y.Release.Protocol != ProtocolTorrent {
		return 0
	}
	return cmpFloat(logRound(x.Release.Seeders), logRound(y.Release.Seeders))
}

func (e *Engine) comparePeerCount(x, y *Subject) int {
	if x.Release.Protocol != ProtocolTorrent || y.Release.Protocol != ProtocolTorrent {
		return 0
	}
	return cmpFloat(logRound(x.Release.Peers), logRound(y.Release.Peers))
}

func logRound(v *int) float64 {
	if v != nil && *v > 0 {
		return math.RoundToEven(math.Log10(float64(*v)))
	}
	return 0
}

func (e *Engine) compareAge(x, y *Subject) int {
	if x.Release.Protocol != ProtocolUsenet || y.Release.Protocol != ProtocolUsenet {
		return 0
	}
	now := e.now()
	score := func(s *Subject) float64 {
		d := now.Sub(s.Release.PublishDate)
		ageHours := d.Hours()
		age := int(d.Hours() / 24) // TimeSpan.Days truncates
		switch {
		case ageHours < 1:
			return 1000
		case ageHours <= 24:
			return 100
		case age <= 7:
			return 10
		}
		if e.Flavour == format.Sonarr {
			return 1
		}
		return -math.RoundToEven(math.Log10(float64(age)))
	}
	return cmpFloat(score(x), score(y))
}

func (e *Engine) compareSize(x, y *Subject) int {
	const level = 200 * 1024 * 1024
	value := func(s *Subject) int64 {
		pref := e.definition(s.Release.Quality.Quality).PreferredSize
		runtime := 0
		if s.Media != nil {
			runtime = s.Media.Runtime
			if pref != nil && e.Flavour == format.Sonarr && len(s.Episodes) > 0 {
				runtime = episodesRuntime(s.Episodes, s.Media.Runtime) // Sonarr v5: the release's summed runtime
			}
		}
		if pref != nil && runtime > 0 {
			preferred := int64(runtime) * Megabytes(*pref)
			v := roundTo(e.Flavour, s.Release.Size-preferred, level)
			if v < 0 {
				v = -v
			}
			return -v
		}
		return roundTo(e.Flavour, s.Release.Size, level)
	}
	vx, vy := value(x), value(y)
	switch {
	case vx < vy:
		return -1
	case vx > vy:
		return 1
	}
	return 0
}

// compareEpisodeCount (Sonarr): season packs first; then, for anime, more episodes, otherwise fewer.
func compareEpisodeCount(x, y *Subject) int {
	if c := cmpBool(x.Release.FullSeason, y.Release.FullSeason); c != 0 {
		return c
	}
	if x.Media != nil && y.Media != nil && x.Media.Anime && y.Media.Anime {
		return cmpInt(len(x.Episodes), len(y.Episodes))
	}
	return -cmpInt(len(x.Episodes), len(y.Episodes))
}

// compareEpisodeNumber (Sonarr): the lower first episode number wins (MinOrDefault: 0 for none).
func compareEpisodeNumber(x, y *Subject) int {
	minEp := func(s *Subject) int {
		if len(s.Episodes) == 0 {
			return 0
		}
		m := s.Episodes[0].EpisodeNumber
		for _, ep := range s.Episodes[1:] {
			if ep.EpisodeNumber < m {
				m = ep.EpisodeNumber
			}
		}
		return m
	}
	return -cmpInt(minEp(x), minEp(y))
}

func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
