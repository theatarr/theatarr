package decide

import (
	"slices"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/profile"
)

// Rank orders decisions per version: within each version the accepted decisions come first, best first by the arr's
// comparer (profile.Engine.Prioritize: quality, format score, cache standing, protocol, indexer priority, flags, peers,
// age, size; Sonarr: quality, format score, protocol, episode count, lowest episode, cache standing, indexer priority,
// peers, age, size), then the rejected ones by the same comparer, as the arrs' DownloadDecisionPriorizationService
// sorts every parsed release for their interactive search; rejected ones without a parsed subject go last in their
// given order. Cache standing is Theatarr's own step (the arrs have none): among releases tied so far, the one cached
// on the version's earlier provider, then on more of its providers, wins (with rd, ad, tb: rd+ad+tb, rd, ad+tb, ad,
// tb, none; provider_preference: none counts providers only). Only the accepted order picks grabs. Versions keep the
// order in which they first appear. The input is not changed.
func Rank(decisions []*Decision) []*Decision {
	return rank(decisions, false)
}

// RankMerged is Rank for a merged search of several seasons (profile.Engine.PrioritizeMerged): between full-season
// releases cache evidence first, then more of the searched episodes (DownloadEpisodes) first.
func RankMerged(decisions []*Decision) []*Decision {
	return rank(decisions, true)
}

func rank(decisions []*Decision, merged bool) []*Decision {
	var order []string
	byVersion := map[string][]*Decision{}
	for _, d := range decisions {
		if _, ok := byVersion[d.Version]; !ok {
			order = append(order, d.Version)
		}
		byVersion[d.Version] = append(byVersion[d.Version], d)
	}
	out := make([]*Decision, 0, len(decisions))
	for _, v := range order {
		var unparsed []*Decision
		var accepted, rejected []*profile.Subject
		var eng *profile.Engine
		bySubject := map[*profile.Subject]*Decision{}
		for _, d := range byVersion[v] {
			switch {
			case d.subject == nil || d.engine == nil:
				unparsed = append(unparsed, d)
				continue
			case d.Accepted:
				accepted = append(accepted, d.subject)
			default:
				rejected = append(rejected, d.subject)
			}
			bySubject[d.subject] = d
			eng = d.engine
		}
		if eng != nil {
			prioritize := eng.Prioritize
			if merged {
				scoped := func(s *profile.Subject) int { return len(bySubject[s].DownloadEpisodes()) }
				prioritize = func(ss []*profile.Subject) []*profile.Subject { return eng.PrioritizeMerged(ss, scoped) }
			}
			ordered := prioritize(accepted)
			explain := eng.Explain
			if merged {
				scoped := func(s *profile.Subject) int { return len(bySubject[s].DownloadEpisodes()) }
				explain = func(x, y *profile.Subject) string { return eng.ExplainMerged(x, y, scoped) }
			}
			recordOver(ordered, bySubject, explain)
			for _, s := range append(ordered, prioritize(rejected)...) {
				out = append(out, bySubject[s])
			}
		}
		out = append(out, unparsed...)
	}
	return out
}

// SeaDexPreference is the native comparison standing stored with shadow summaries.
func (d *Decision) SeaDexPreference() int {
	if d.subject == nil || d.subject.Release == nil || d.engine == nil || !d.engine.Config.PreferSeaDex {
		return 0
	}
	return d.subject.Release.Quality.SeaDexPreference
}

// Over says why a ranked accepted release beat the next accepted torrent after it for the same episodes (D4): that
// release's name and the comparer's own words for the step that decided ("higher quality (Remux-2160p over
// WEBDL-2160p)"). Set by Rank; nil for the last one, or when the comparer ties them.
type Over struct {
	Release string
	Reason  string
	// Episodes are that release's (Decision.Episodes).
	Episodes []string
}

// ChosenOver is Over, recorded when the decisions were ranked; nil when none.
func (d *Decision) ChosenOver() *Over { return d.over }

// recordOver sets each accepted decision's Over against the next one in order that is another torrent and shares an
// episode with it (a season's search ranks every episode's releases together: another episode's is no alternative).
func recordOver(ordered []*profile.Subject, bySubject map[*profile.Subject]*Decision, explain func(x, y *profile.Subject) string) {
	for i, s := range ordered {
		d := bySubject[s]
		d.over = nil
		for _, n := range ordered[i+1:] {
			nd := bySubject[n]
			if h := s.Release.InfoHash; h != "" && strings.EqualFold(h, n.Release.InfoHash) {
				continue
			}
			if len(d.Episodes) > 0 && len(nd.Episodes) > 0 && !slices.ContainsFunc(nd.Episodes, func(e string) bool {
				return slices.Contains(d.Episodes, e)
			}) {
				continue
			}
			if why := explain(s, n); why != "" {
				d.over = &Over{Release: nd.Release, Reason: why, Episodes: nd.Episodes}
			}
			break
		}
	}
}
