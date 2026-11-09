package profile

// Port of Radarr src/NzbDrone.Core/DecisionEngine/{DownloadDecisionComparer,DownloadDecisionPriorizationService}.cs
// @ a96bf7c and the Sonarr files @ cab419a.
//
// Radarr: quality, format score, protocol, indexer priority, indexer flags (when preferred), peers (torrents),
// age (usenet), size.
// Sonarr: quality, format score, protocol, episode count (season packs first; anime prefers more episodes, others
// fewer), lowest episode number, indexer priority, peers, age, size.

import (
	"math"
	"sort"

	"github.com/theatarr/theatarr/internal/decide/format"
)

// Compare is DownloadDecisionComparer.Compare: > 0 when x is preferred over y.
func (e *Engine) Compare(x, y *Subject) int {
	if c, ok := SeaDexOrder(e.Config.PreferSeaDex, x.Release.Quality.SeaDexPreference, y.Release.Quality.SeaDexPreference); ok {
		return c
	}
	var cmps []func(x, y *Subject) int
	if e.Flavour == format.Sonarr {
		cmps = []func(x, y *Subject) int{e.compareQuality, compareFormatScore, e.compareProtocol, compareEpisodeCount,
			compareEpisodeNumber, compareIndexerPriority, e.comparePeers, e.compareAge, e.compareSize}
	} else {
		cmps = []func(x, y *Subject) int{e.compareQuality, compareFormatScore, e.compareProtocol, compareIndexerPriority,
			e.compareIndexerFlags, e.comparePeers, e.compareAge, e.compareSize}
	}
	for _, c := range cmps {
		if r := c(x, y); r != 0 {
			return r
		}
	}
	return 0
}

// Prioritize is DownloadDecisionPriorizationService for the releases of one title: ordered best first by
// Compare, keeping the input order among equals (OrderByDescending is stable).
func (e *Engine) Prioritize(subjects []*Subject) []*Subject {
	out := append([]*Subject(nil), subjects...)
	sort.SliceStable(out, func(i, j int) bool { return e.Compare(out[i], out[j]) > 0 })
	return out
}

func (e *Engine) compareQuality(x, y *Subject) int {
	qi := func(s *Subject) QualityIndex { return s.Profile.GetIndex(s.Release.Quality.Quality.ID, false) }
	// QualityIndex's IComparable compares with respectGroupOrder = true; GetIndex leaves GroupIndex 0 here.
	c := qi(x).CompareTo(qi(y), true)
	if e.Config.DownloadPropersAndRepacks == DoNotPrefer || c != 0 {
		return c
	}
	return x.Release.Quality.Revision.CompareTo(y.Release.Quality.Revision)
}

func compareFormatScore(x, y *Subject) int {
	return cmpInt(x.Release.FormatScore, y.Release.FormatScore)
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

func (e *Engine) comparePeers(x, y *Subject) int {
	if x.Release.Protocol != ProtocolTorrent || y.Release.Protocol != ProtocolTorrent {
		return 0
	}
	logRound := func(v *int) float64 {
		if v != nil && *v > 0 {
			return math.RoundToEven(math.Log10(float64(*v)))
		}
		return 0
	}
	if c := cmpFloat(logRound(x.Release.Seeders), logRound(y.Release.Seeders)); c != 0 {
		return c
	}
	return cmpFloat(logRound(x.Release.Peers), logRound(y.Release.Peers))
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
