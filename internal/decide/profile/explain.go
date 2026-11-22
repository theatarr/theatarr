package profile

import (
	"fmt"
	"math"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/format"
)

// Why one release ranked before another, in words, from the comparer's own deciding step and its operands (D4,
// docs/design/ui.md "Releases page"): Explain follows Compare's order, ExplainMerged compareMerged's. Each step's
// words read the same operands the step compares, down to the sub-step that decided ("a newer revision" when the
// quality is equal, "more peers" when the seeders tie). "" when the comparer does not put x first.

// Explain says why Compare ranks x before y.
func (e *Engine) Explain(x, y *Subject) string {
	if e.Compare(x, y) <= 0 {
		return ""
	}
	if _, ok := SeaDexOrder(e.Config.PreferSeaDex, x.Release.Quality.SeaDexPreference, y.Release.Quality.SeaDexPreference); ok {
		return "SeaDex ranks it higher"
	}
	_, step := e.compareStep(x, y)
	return e.stepWords(step, x, y)
}

// ExplainMerged says why compareMerged (a merged search of several seasons) ranks x before y.
func (e *Engine) ExplainMerged(x, y *Subject, scoped func(*Subject) int) string {
	if e.compareMerged(x, y, scoped) <= 0 {
		return ""
	}
	if _, ok := SeaDexOrder(e.Config.PreferSeaDex, x.Release.Quality.SeaDexPreference, y.Release.Quality.SeaDexPreference); ok {
		return "SeaDex ranks it higher"
	}
	for _, c := range []compareFunc{{StepQuality, e.compareQuality}, {StepScore, compareFormatScore}, {StepProtocol, e.compareProtocol}} {
		if c.cmp(x, y) != 0 {
			return e.stepWords(c.step, x, y)
		}
	}
	if cmpBool(x.Release.pack(), y.Release.pack()) != 0 {
		return "a pack"
	}
	if x.Release.pack() {
		if cmpBool(x.Cached.Count > 0, y.Cached.Count > 0) != 0 {
			return cachedWords(x)
		}
		if a, b := scoped(x), scoped(y); a != b {
			return fmt.Sprintf("more of the searched episodes (%d over %d)", a, b)
		}
	}
	return e.Explain(x, y)
}

// stepWords are a deciding step's words with its operands.
func (e *Engine) stepWords(step string, x, y *Subject) string {
	switch step {
	case StepQuality:
		qi := func(s *Subject) QualityIndex { return s.Profile.GetIndex(s.Release.Quality.Quality.ID, false) }
		if qi(x).CompareTo(qi(y), true) != 0 {
			return "higher quality (" + x.Release.Quality.Quality.Name + " over " + y.Release.Quality.Quality.Name + ")"
		}
		rx, ry := x.Release.Quality.Revision, y.Release.Quality.Revision
		if rx.Real != ry.Real {
			return fmt.Sprintf("a REAL release (REAL %d over %d)", rx.Real, ry.Real)
		}
		return fmt.Sprintf("a newer revision, a proper or repack (v%d over v%d)", rx.Version, ry.Version)
	case StepScore:
		ids := map[int]bool{}
		for _, f := range y.Release.Formats {
			ids[f.ID] = true
		}
		var more []string
		for _, it := range x.Profile.Profile.FormatItems {
			if it.Score > 0 && !ids[it.Format] && hasFormat(x.Release.Formats, it.Format) {
				more = append(more, it.Name)
			}
		}
		s := fmt.Sprintf("higher score (+%d", x.Release.FormatScore-y.Release.FormatScore)
		if len(more) > 0 {
			s += ": " + strings.Join(more, ", ")
		}
		return s + ")"
	case StepCached:
		cx, cy := x.Cached, y.Cached
		switch {
		case cy.Count == 0:
			return cachedWords(x)
		case cx.First != cy.First:
			return cachedWords(x) + ", earlier in the library's provider order"
		}
		return fmt.Sprintf("cached on more providers (%d over %d)", cx.Count, cy.Count)
	case StepProtocol:
		return "the preferred protocol"
	case StepEpisodeCount:
		if x.Release.FullSeason != y.Release.FullSeason {
			return "a season pack"
		}
		if len(x.Episodes) > len(y.Episodes) {
			return fmt.Sprintf("more episodes (%d over %d)", len(x.Episodes), len(y.Episodes))
		}
		return fmt.Sprintf("fewer episodes (%d over %d)", len(x.Episodes), len(y.Episodes))
	case StepEpisodeNumber:
		return "an earlier first episode"
	case StepIndexerPriority:
		return fmt.Sprintf("a higher indexer priority (%d over %d; lower is higher)", x.Release.IndexerPriority, y.Release.IndexerPriority)
	case StepIndexerFlags:
		return "better indexer flags"
	case StepPeers:
		logRound := func(v *int) float64 {
			if v != nil && *v > 0 {
				return math.RoundToEven(math.Log10(float64(*v)))
			}
			return 0
		}
		n := func(v *int) int {
			if v == nil {
				return 0
			}
			return *v
		}
		if logRound(x.Release.Seeders) != logRound(y.Release.Seeders) {
			return fmt.Sprintf("more seeders (%d over %d)", n(x.Release.Seeders), n(y.Release.Seeders))
		}
		return fmt.Sprintf("more peers (%d over %d)", n(x.Release.Peers), n(y.Release.Peers))
	case StepAge:
		return "published more recently"
	case StepSize:
		if e.definition(x.Release.Quality.Quality).PreferredSize != nil {
			return "a size closer to the preferred size"
		}
		return fmt.Sprintf("larger (%s over %s)", gib(x.Release.Size), gib(y.Release.Size))
	}
	return ""
}

func hasFormat(fs []*format.Format, id int) bool {
	for _, f := range fs {
		if f.ID == id {
			return true
		}
	}
	return false
}

// cachedWords: "cached on Real-Debrid", by the earliest provider of the version's list holding it cached.
func cachedWords(s *Subject) string {
	if name := providerName(s.Cached.FirstKind); name != "" {
		return "cached on " + name
	}
	return "cached"
}

func providerName(kind string) string {
	switch kind {
	case "realdebrid":
		return "Real-Debrid"
	case "alldebrid":
		return "AllDebrid"
	case "torbox":
		return "TorBox"
	}
	return kind
}

func gib(n int64) string { return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30)) }
