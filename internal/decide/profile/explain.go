package profile

import (
	"fmt"
	"strings"
)

// Why one release ranked before another, in words (D4, docs/design/ui.md "Releases page"): the step that decided is
// the one the ranking walk itself returns (compareStep for Compare, compareMergedStep for a merged search), so the
// words cannot disagree with the order. Operands are given only where they are plain: the qualities, the revisions,
// the format scores (with the formats only the first has), the seeders or peers and the cached provider; every other
// step reads by its name. "" when the walk does not put x first.

// Explain says why Compare ranks x before y.
func (e *Engine) Explain(x, y *Subject) string {
	c, step := e.compareStep(x, y)
	if c <= 0 {
		return ""
	}
	return stepWords(step, x, y)
}

// ExplainMerged says why compareMerged (a merged search of several seasons) ranks x before y.
func (e *Engine) ExplainMerged(x, y *Subject, scoped func(*Subject) int) string {
	c, step := e.compareMergedStep(x, y, scoped)
	if c <= 0 {
		return ""
	}
	return stepWords(step, x, y)
}

func stepWords(step string, x, y *Subject) string {
	switch step {
	case StepSeaDex:
		return "SeaDex ranks it higher"
	case StepQuality:
		return "higher quality (" + x.Release.Quality.Quality.Name + " over " + y.Release.Quality.Quality.Name + ")"
	case StepRevision:
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
			if it.Score > 0 && !ids[it.Format] && hasFormat(x, it.Format) {
				more = append(more, it.Name)
			}
		}
		s := fmt.Sprintf("higher score (%d over %d", x.Release.FormatScore, y.Release.FormatScore)
		if len(more) > 0 {
			s += ": " + strings.Join(more, ", ")
		}
		return s + ")"
	case StepCached, StepCachedFirst:
		if name := providerName(x.Cached.FirstKind); name != "" {
			return "cached on " + name
		}
		return "cached"
	case StepCachedCount:
		return "cached on more providers"
	case StepProtocol:
		return "the preferred protocol"
	case StepEpisodeCount:
		return "the episode count"
	case StepEpisodeNumber:
		return "an earlier first episode"
	case StepIndexerPriority:
		return "a higher indexer priority"
	case StepIndexerFlags:
		return "better indexer flags"
	case StepSeeders:
		return fmt.Sprintf("more seeders (%d over %d)", count(x.Release.Seeders), count(y.Release.Seeders))
	case StepPeers:
		return fmt.Sprintf("more peers (%d over %d)", count(x.Release.Peers), count(y.Release.Peers))
	case StepAge:
		return "its age"
	case StepSize:
		return "its size"
	case StepPack:
		return "a pack"
	case StepScoped:
		return "more of the searched episodes"
	}
	return ""
}

func count(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func hasFormat(s *Subject, id int) bool {
	for _, f := range s.Release.Formats {
		if f.ID == id {
			return true
		}
	}
	return false
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
