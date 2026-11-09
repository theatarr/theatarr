package decide

import "github.com/theatarr/theatarr/internal/decide/profile"

// Rank orders decisions per version: within each version the accepted decisions come first, best first by the arr's
// comparer (profile.Engine.Prioritize: quality, format score, protocol, indexer priority, flags, peers, age, size),
// then the rejected ones by the same comparer, as the arrs' DownloadDecisionPriorizationService sorts every parsed
// release for their interactive search; rejected ones without a parsed subject go last in their given order. Only the
// accepted order picks grabs. Versions keep the order in which they first appear. The input is not changed.
func Rank(decisions []*Decision) []*Decision {
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
			for _, s := range append(eng.Prioritize(accepted), eng.Prioritize(rejected)...) {
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
