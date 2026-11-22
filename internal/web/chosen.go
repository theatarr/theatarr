package web

import (
	"fmt"
	"slices"
	"strings"

	"github.com/theatarr/theatarr/internal/journal/snapshot"
)

// Why the chosen release beat the next (docs/design/ui.md "Releases page"): the comparer's own first difference
// between the release a run took and the next accepted torrent after it, recorded with the run (snapshot.Over), in
// one line where the run's choice is shown (snapshot.ChosenOver).

// chosenLine is the Last search's line for the release a run took (taken, by info hash): first how many better-ranked
// accepted releases were not taken ("2 better releases were not cached"; one that was tried and failed is counted
// as failed), then snapshot.ChosenOver against the next one after it. "" when taken is not among the stored candidates or
// nothing is to be said.
func chosenLine(cands []snapshot.Candidate, taken string, outcomes map[string]ReleaseOutcome) string {
	i := slices.IndexFunc(cands, func(c snapshot.Candidate) bool {
		return c.Accepted && taken != "" && strings.EqualFold(c.InfoHash, taken)
	})
	if i < 0 {
		return ""
	}
	missed, failed := 0, 0
	seen := map[string]bool{strings.ToLower(taken): true}
	eps := cands[i].Episodes
	for _, c := range cands[:i] {
		h := strings.ToLower(c.InfoHash)
		if !c.Accepted || h == "" || seen[h] {
			continue
		}
		// In a season's search only a release of the same episodes was a better choice.
		if len(eps) > 0 && len(c.Episodes) > 0 && !slices.ContainsFunc(c.Episodes, func(e string) bool { return slices.Contains(eps, e) }) {
			continue
		}
		seen[h] = true
		if o, ok := outcomes[h]; ok && o.Outcome == "failed" {
			failed++
		} else {
			missed++
		}
	}
	var parts []string
	if missed > 0 {
		parts = append(parts, countWords(missed, "better release was", "better releases were")+" not cached")
	}
	if failed > 0 {
		parts = append(parts, countWords(failed, "better release", "better releases")+" failed")
	}
	line := strings.Join(parts, "; ")
	if line != "" {
		line += "."
	}
	if w := snapshot.ChosenOver(cands[i]); w != "" {
		line = strings.TrimSpace(line + " " + w)
	}
	return line
}

// countWords is "1 better release was" or "2 better releases were".
func countWords(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
