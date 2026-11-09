package shadow

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/policy"
)

// Result is one grab replayed.
type Result struct {
	Grab     Grab
	Decision *decide.Decision
	Err      error
	// Cause is the main reason the engine disagrees with the arr, "" when it agrees on everything compared.
	Cause string
}

// Report is the agreement over one instance's grabs.
type Report struct {
	Instance string
	Version  string
	Kind     policy.Kind
	Grabs    int
	Errors   int
	Accepted int
	// QualityAgree counts grabs whose parsed quality is the arr's.
	QualityAgree int
	// GrabScoreAgree: our score equals the arr's score at grab time, of GrabScores grabs that recorded one.
	GrabScoreAgree, GrabScores int
	// NowScoreAgree: our score equals the arr's score now (current profile), of NowScores grabs.
	NowScoreAgree, NowScores int
	// EpisodesAgree (series): the release maps to the episodes the arr grabbed, of EpisodeGrabs grabs.
	EpisodesAgree, EpisodeGrabs int
	// Agree: accepted, same quality, and the same score as at grab time (or as now, when no grab-time score); for
	// series also the same episodes.
	Agree int
	// AgreeWithoutFile is Agree, plus the grabs refused only by the upgrade checks against the file rebuilt from
	// history that agree fully when decided with no file: the engine's agreement apart from the file state.
	AgreeWithoutFile int
	// Rejections counts rejection reasons over rejected grabs (every reason of a decision).
	Rejections map[string]int
	// Causes counts the main disagreement cause per grab; Examples keeps a few release titles per cause.
	Causes   map[string]int
	Examples map[string][]string
}

const examplesPerCause = 4

// Run decides every grab for version with engine e and compares with what the arr recorded. The
// version-indexer rule (ours, not the arr's) is left out of the comparison: history releases carry the arr's
// indexer names, not policy keys.
func Run(ctx context.Context, e *decide.Engine, version string, grabs []Grab) (*Report, []Result) {
	profileID := 0
	kind := policy.KindMovie
	if v := e.Policy.Versions[version]; v != nil {
		kind = v.Kind
		fl := format.Radarr
		if kind == policy.KindSeries {
			fl = format.Sonarr
		}
		if p := e.Profile(v.Profile, fl); p != nil {
			profileID = p.Profile.ID
		}
	}
	rep := &Report{Version: version, Kind: kind, Rejections: map[string]int{}, Causes: map[string]int{},
		Examples: map[string][]string{}}
	decideOne := func(g Grab) (*decide.Decision, error) {
		if g.Kind == policy.KindSeries {
			return e.DecideSearch(ctx, g.Release, g.Title, decide.Version{Key: version}, g.Search)
		}
		return e.Decide(ctx, g.Release, g.Title, decide.Version{Key: version}, g.Existing)
	}
	var results []Result
	for _, g := range grabs {
		rep.Instance = g.Instance
		rep.Grabs++
		d, err := decideOne(g)
		r := Result{Grab: g, Decision: d, Err: err}
		if err != nil {
			rep.Errors++
			r.Cause = "error: " + err.Error()
		} else {
			d.Rejections = withoutOurs(d.Rejections)
			d.Accepted = len(d.Rejections) == 0
			again := func(g Grab) *decide.Decision {
				d, err := decideOne(g)
				if err != nil {
					return nil
				}
				d.Rejections = withoutOurs(d.Rejections)
				d.Accepted = len(d.Rejections) == 0
				return d
			}
			score(rep, &r, profileID, again)
		}
		if r.Cause != "" {
			rep.Causes[r.Cause]++
			if len(rep.Examples[r.Cause]) < examplesPerCause {
				rep.Examples[r.Cause] = append(rep.Examples[r.Cause], example(&r))
			}
		}
		results = append(results, r)
	}
	return rep, results
}

func withoutOurs(rs []decide.Rejection) []decide.Rejection {
	var out []decide.Rejection
	for _, r := range rs {
		if !r.Ours {
			out = append(out, r)
		}
	}
	return out
}

func example(r *Result) string {
	s := r.Grab.Release.Title
	if d := r.Decision; d != nil {
		s += fmt.Sprintf(" | ours %s %d", d.Quality, d.Score)
		if len(d.Rejections) > 0 {
			s += " " + d.Rejections[0].String()
		}
	}
	s += " | arr " + r.Grab.Quality
	if r.Grab.GrabScore != nil {
		s += fmt.Sprintf(" grab %d", *r.Grab.GrabScore)
	}
	if r.Grab.NowScore != nil {
		s += fmt.Sprintf(" now %d", *r.Grab.NowScore)
	}
	if r.Grab.ReleaseSource != "" {
		s += " (" + r.Grab.ReleaseSource + ")"
	}
	return s
}

func score(rep *Report, r *Result, profileID int, again func(Grab) *decide.Decision) {
	d, g := r.Decision, r.Grab
	if d.Accepted {
		rep.Accepted++
	} else {
		for _, rj := range d.Rejections {
			rep.Rejections[rj.Reason]++
		}
	}
	qualityOK := d.Quality == g.Quality
	if qualityOK {
		rep.QualityAgree++
	}
	grabOK, nowOK := true, true
	if g.GrabScore != nil {
		rep.GrabScores++
		grabOK = d.Score == *g.GrabScore
		if grabOK {
			rep.GrabScoreAgree++
		}
	}
	if g.NowScore != nil {
		rep.NowScores++
		nowOK = d.Score == *g.NowScore
		if nowOK {
			rep.NowScoreAgree++
		}
	}
	scoreOK := grabOK
	if g.GrabScore == nil {
		scoreOK = nowOK
	}
	episodesOK := true
	if g.Kind == policy.KindSeries {
		rep.EpisodeGrabs++
		episodesOK = sameIDs(d.EpisodeIDs, g.Episodes)
		if episodesOK {
			rep.EpisodesAgree++
		}
	}
	if d.Accepted && qualityOK && scoreOK && episodesOK {
		rep.Agree++
		rep.AgreeWithoutFile++
		return
	}
	r.Cause = cause(d, g, qualityOK, grabOK, nowOK, episodesOK, profileID, again)
	if hasFile(g) && !d.Accepted && (strings.HasPrefix(d.Rejections[0].Reason, "Disk") ||
		d.Rejections[0].Reason == "ExistingFileHasMoreEpisodes") {
		g = withoutFiles(g)
		if d2 := again(g); d2 != nil && d2.Accepted && d2.Quality == g.Quality && (g.GrabScore == nil && nowOK ||
			g.GrabScore != nil && d2.Score == *g.GrabScore) && episodesOK {
			rep.AgreeWithoutFile++
		}
	}
}

// cause names the main disagreement, most basic first: a different quality, a refusal, then the score.
func cause(d *decide.Decision, g Grab, qualityOK, grabOK, nowOK, episodesOK bool, profileID int,
	again func(Grab) *decide.Decision) string {
	what := "movie"
	if g.Kind == policy.KindSeries {
		what = "series"
	}
	if g.ArrProfileID != 0 && profileID != 0 && g.ArrProfileID != profileID {
		return fmt.Sprintf("the %s is on another arr profile (id %d) than the one compared", what, g.ArrProfileID)
	}
	langsOK := sameSet(d.Languages, g.Languages) || len(g.Languages) == 0
	if !qualityOK {
		return fmt.Sprintf("quality differs (arr %s, ours %s)", g.Quality, d.Quality)
	}
	if !d.Accepted {
		rj := d.Rejections[0]
		manual := g.ReleaseSource == "InteractiveSearch"
		prefix := "rejected"
		if manual {
			prefix = "rejected, grabbed by hand (interactive search)"
		}
		switch {
		case g.Kind == policy.KindSeries && strings.HasPrefix(rj.Reason, "Disk") && d.Missing > 0:
			return fmt.Sprintf("%s: %s for some episodes of a pack that also fills episodes without a file (the "+
				"fork's ACCEPT_RELEASE_ANY_UPGRADABLE takes it)", prefix, rj.Reason)
		case strings.HasPrefix(rj.Reason, "Disk") || rj.Reason == "QualityUpgradesDisabled" ||
			rj.Reason == "ExistingFileHasMoreEpisodes":
			return fmt.Sprintf("%s: %s against the file rebuilt from history", prefix, rj.Reason)
		case (rj.Reason == "UnknownSeries" || rj.Reason == "UnknownEpisode") && g.MatchType == "Unknown":
			return fmt.Sprintf("%s: %s, the arr's parse does not map it either", prefix, rj.Reason)
		case rj.Reason == "UnknownSeries":
			if len(g.Title.SceneMappings) == 0 && g.Release.TVDB == 0 {
				return prefix + ": UnknownSeries, a series name we do not hold (no scene mappings, no TVDB id from the indexer)"
			}
			return prefix + ": UnknownSeries"
		case rj.Reason == "UnknownEpisode" || rj.Reason == "EpisodeNotMonitored" || rj.Reason == "SeriesNotMonitored" ||
			rj.Reason == "FullSeasonNotAired":
			return fmt.Sprintf("%s: %s (%s)", prefix, rj.Reason, rj.Message)
		case rj.Reason == "QualityNotWanted":
			return fmt.Sprintf("%s: QualityNotWanted (%s not in today's profile)", prefix, d.Quality)
		case rj.Reason == "WantedLanguage" && !langsOK:
			return fmt.Sprintf("%s: WantedLanguage, the arr had other languages (from the indexer)", prefix)
		case rj.Reason == "CustomFormatMinimumScore" && g.GrabScore != nil && *g.GrabScore >= 0 && nowOK:
			return fmt.Sprintf("%s: CustomFormatMinimumScore, the arr's profile scores changed since the grab", prefix)
		case rj.Reason == "UnknownMovie":
			if g.MatchType == "Unknown" {
				return prefix + ": UnknownMovie, the arr did not map it either"
			}
			if d.Year > 1800 && d.Year != g.Title.Year {
				g2 := g
				g2.Title.SecondaryYear = d.Year
				if d2 := again(g2); d2 != nil && d2.MatchType != decide.MatchNone {
					return fmt.Sprintf("%s: UnknownMovie, the release year is the arr's year or secondary year then, not ours", prefix)
				}
			}
			return prefix + ": UnknownMovie, a title we do not hold (translation or alternative title)"
		}
		return prefix + ": " + rj.Reason
	}
	if !episodesOK {
		if laterEpisodes(d, g) {
			return "episodes differ: the pack maps today's season, which has episodes that aired after the grab"
		}
		return fmt.Sprintf("episodes differ (arr %d, ours %s)", len(g.Episodes), strings.Join(d.Episodes, ","))
	}
	if !langsOK {
		return "score: the arr had other languages (from the indexer)"
	}
	if g.GrabScore != nil && !grabOK {
		if g.NowScore != nil && nowOK {
			return "score: the arr's profile scores changed since the grab (ours equals the arr's score now)"
		}
		return "score differs from both the grab-time and the current arr score"
	}
	return "score differs from the arr's current score"
}

// Format writes the report as text: the agreement lines and the causes, largest first.
func (r *Report) Format() string {
	var b strings.Builder
	pct := func(n, of int) string {
		if of == 0 {
			return "-"
		}
		return fmt.Sprintf("%d / %d (%.1f%%)", n, of, 100*float64(n)/float64(of))
	}
	fmt.Fprintf(&b, "%s -> %s: %d grabs\n", r.Instance, r.Version, r.Grabs)
	fmt.Fprintf(&b, "  accepted            %s\n", pct(r.Accepted, r.Grabs-r.Errors))
	fmt.Fprintf(&b, "  same quality        %s\n", pct(r.QualityAgree, r.Grabs-r.Errors))
	if r.EpisodeGrabs > 0 {
		fmt.Fprintf(&b, "  same episodes       %s\n", pct(r.EpisodesAgree, r.EpisodeGrabs))
	}
	fmt.Fprintf(&b, "  score = grab-time   %s\n", pct(r.GrabScoreAgree, r.GrabScores))
	fmt.Fprintf(&b, "  score = arr now     %s\n", pct(r.NowScoreAgree, r.NowScores))
	fmt.Fprintf(&b, "  full agreement      %s\n", pct(r.Agree, r.Grabs))
	fmt.Fprintf(&b, "  ... without file    %s\n", pct(r.AgreeWithoutFile, r.Grabs))
	if r.Errors > 0 {
		fmt.Fprintf(&b, "  errors              %d\n", r.Errors)
	}
	if len(r.Rejections) > 0 {
		b.WriteString("  rejection reasons:")
		for _, k := range sortedByCount(r.Rejections) {
			fmt.Fprintf(&b, " %s %d,", k, r.Rejections[k])
		}
		b.WriteString("\n")
	}
	b.WriteString("  disagreement causes:\n")
	for _, k := range sortedByCount(r.Causes) {
		fmt.Fprintf(&b, "    %5d  %s\n", r.Causes[k], k)
		for _, ex := range r.Examples[k] {
			fmt.Fprintf(&b, "             e.g. %s\n", ex)
		}
	}
	return b.String()
}

// laterEpisodes: ours holds every episode the arr grabbed, and the extra ones aired after the grab (or have no air
// date): a season pack mapped to a season that grew since.
func laterEpisodes(d *decide.Decision, g Grab) bool {
	arr := map[int64]bool{}
	for _, id := range g.Episodes {
		arr[id] = true
	}
	ours := map[int64]bool{}
	for _, id := range d.EpisodeIDs {
		ours[id] = true
	}
	for id := range arr {
		if !ours[id] {
			return false
		}
	}
	for _, e := range g.Title.Episodes {
		if ours[e.ID] && !arr[e.ID] && e.AirDateUTC != nil && e.AirDateUTC.Before(g.Date) {
			return false
		}
	}
	return true
}

// hasFile reports whether the grab was decided against an existing file (the movie's, or any episode's).
func hasFile(g Grab) bool {
	if g.Existing != nil {
		return true
	}
	for _, e := range g.Title.Episodes {
		if e.File != nil {
			return true
		}
	}
	return false
}

// withoutFiles is the grab with no existing file.
func withoutFiles(g Grab) Grab {
	g.Existing = nil
	if len(g.Title.Episodes) > 0 {
		eps := append([]decide.Episode(nil), g.Title.Episodes...)
		for i := range eps {
			eps[i].File, eps[i].FileID = nil, 0
		}
		g.Title.Episodes = eps
	}
	return g
}

func sameIDs(a, b []int64) bool {
	m := map[int64]bool{}
	for _, x := range a {
		m[x] = true
	}
	n := map[int64]bool{}
	for _, x := range b {
		if !m[x] {
			return false
		}
		n[x] = true
	}
	return len(m) == len(n)
}

func sameSet(a, b []string) bool {
	m := map[string]bool{}
	for _, x := range a {
		m[x] = true
	}
	n := map[string]bool{}
	for _, x := range b {
		if !m[x] {
			return false
		}
		n[x] = true
	}
	return len(m) == len(n)
}

func sortedByCount(m map[string]int) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool {
		if m[ks[i]] != m[ks[j]] {
			return m[ks[i]] > m[ks[j]]
		}
		return ks[i] < ks[j]
	})
	return ks
}
