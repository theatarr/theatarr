package shadow

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// TestSonarrV5Diff measures what a versions.<k>.sonarr mode changes against another (docs/design/sonarr-v5.md): every
// Sonarr corpus title (THEATARR_CORPUS, parse.jsonl of every sonarr instance) parsed by both parsers, decided under both
// for the series the arr's parse mapped it to, and the series grabs replayed (corpus, and THEATARR_DECIDE_DB when set). The
// modes are THEATARR_SONARR_A and THEATARR_SONARR_B (default v4 and v5); the TSV columns "v4" and "v5" hold A and B. It writes
// every difference to THEATARR_V5DIFF_OUT (TSV) and logs the counts; it does not fail on differences.
func TestSonarrV5Diff(t *testing.T) {
	corpus, out := os.Getenv("THEATARR_CORPUS"), os.Getenv("THEATARR_V5DIFF_OUT")
	if corpus == "" || out == "" {
		t.Skip("THEATARR_CORPUS and THEATARR_V5DIFF_OUT not set")
	}
	modeA, modeB := cmpOr(os.Getenv("THEATARR_SONARR_A"), sonarr.VersionNameV4), cmpOr(os.Getenv("THEATARR_SONARR_B"), sonarr.VersionNameV5)
	verA, err := sonarr.ParseVersion(modeA)
	if err != nil {
		t.Fatal(err)
	}
	verB, err := sonarr.ParseVersion(modeB)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("comparing %s (column v4) with %s (column v5)", modeA, modeB)
	e4 := engineFor(t, corpus, modeA)
	e5 := engineFor(t, corpus, modeB)
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()
	fmt.Fprintln(w, "kind\tinstance\twhat\trelease\tv4\tv5")
	ctx := context.Background()

	versionOf := func(instance string) string {
		if strings.Contains(instance, "anime") {
			return "anime-tv"
		}
		return "tv-2160p"
	}
	for _, instance := range []string{"sonarr_4k", "sonarr_4k_debrid", "sonarr_anime", "sonarr_anime_debrid"} {
		var aliases map[int][]decide.SceneMapping
		if dir := os.Getenv("THEATARR_SERIES_ALIASES"); dir != "" {
			if _, err := os.Stat(filepath.Join(dir, instance+".series.json")); err == nil {
				aliases = seriesAliases(t, instance)
			}
		}
		titles, err := corpusTitles(corpus, instance, aliases)
		if err != nil {
			t.Fatal(err)
		}
		key := versionOf(instance)
		v4 := decide.Version{Key: key, Policy: e4.Policy.Versions[key]}
		v5 := decide.Version{Key: key, Policy: e5.Policy.Versions[key]}
		parsed, parseDiff, decided, decideDiff := 0, 0, 0, 0
		fields, causes := map[string]int{}, map[string]int{}
		seen := map[string]bool{}
		err = eachLine(filepath.Join(corpus, instance, "parse.jsonl"), func(b []byte) error {
			var l struct {
				Title string `json:"title"`
				Body  struct {
					Series *struct {
						ID int `json:"id"`
					} `json:"series"`
				} `json:"body"`
			}
			if err := json.Unmarshal(b, &l); err != nil {
				return err
			}
			if seen[l.Title] {
				return nil
			}
			seen[l.Title] = true
			parsed++
			p4, p5 := parseSummary(verA, l.Title), parseSummary(verB, l.Title)
			if d := diffFields(p4, p5); d != "" {
				parseDiff++
				for _, x := range strings.Split(d, ",") {
					fields[x]++
				}
				fmt.Fprintf(w, "parse\t%s\t%s\t%s\t%s\t%s\n", instance, d, l.Title, joinSummary(p4), joinSummary(p5))
			}
			if l.Body.Series == nil {
				return nil
			}
			tt, ok := titles[l.Body.Series.ID]
			if !ok {
				return nil
			}
			decided++
			rel := decide.Release{}
			rel.Title = l.Title
			d4, err := e4.Decide(ctx, rel, tt, v4, nil)
			if err != nil {
				return err
			}
			d5, err := e5.Decide(ctx, rel, tt, v5, nil)
			if err != nil {
				return err
			}
			s4, s5 := decisionSummary(d4), decisionSummary(d5)
			if c := diffFields(s4, s5); c != "" {
				decideDiff++
				causes[c]++
				fmt.Fprintf(w, "decide\t%s\t%s\t%s\t%s\t%s\n", instance, c, l.Title, joinSummary(s4), joinSummary(s5))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s (%s): %d titles, %d parse differently %v; %d decided for the arr's series, %d decide differently %v",
			instance, key, parsed, parseDiff, sortedCounts(fields), decided, decideDiff, sortedCounts(causes))
	}

	// The grab replays (docs/design/series.md), corpus and database.
	type source struct {
		name  string
		grabs func(instance string) ([]Grab, error)
	}
	sources := []source{{"replay-corpus", func(instance string) ([]Grab, error) {
		return LoadCorpusSeries(corpus, instance, seriesAliases(t, instance))
	}}}
	if dbPath := os.Getenv("THEATARR_DECIDE_DB"); dbPath != "" {
		st, err := store.Open(ctx, dbPath, store.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		lib, err := LoadLibrary(ctx, st.Reader())
		if err != nil {
			t.Fatal(err)
		}
		e4.SetLibrary(lib)
		e5.SetLibrary(lib)
		sources = append(sources, source{"replay-db", func(instance string) ([]Grab, error) {
			return LoadSeriesDB(ctx, st.Reader(), instance, seriesAliases(t, instance))
		}})
	}
	for _, src := range sources {
		for _, in := range seriesInstances {
			grabs, err := src.grabs(in.name)
			if err != nil {
				t.Fatal(err)
			}
			rep4, res4 := Run(ctx, e4, in.version, grabs)
			rep5, res5 := Run(ctx, e5, in.version, grabs)
			n := 0
			causes := map[string]int{}
			for i := range res4 {
				s4, s5 := decisionSummary(res4[i].Decision), decisionSummary(res5[i].Decision)
				if c := diffFields(s4, s5); c != "" {
					n++
					causes[c]++
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", src.name, in.name, c, grabs[i].Release.Title, joinSummary(s4), joinSummary(s5))
				}
			}
			t.Logf("%s %s: %d grabs, %d decide differently %v; agreement v4 %d, v5 %d", src.name, in.name, len(grabs), n,
				sortedCounts(causes), rep4.Agree, rep5.Agree)
		}
	}
}

func cmpOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// engineFor is the corpus engine with every series version on the Sonarr mode.
func engineFor(t *testing.T, corpus, mode string) *decide.Engine {
	t.Helper()
	pol, err := policy.Load("testdata/policy")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range pol.Versions {
		if v.Kind == policy.KindSeries {
			v.Sonarr = mode
		}
	}
	e, err := decide.New(pol, decide.Options{Arr: decide.SnapshotDir(corpus)})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// corpusTitles are the series of an instance's parse answers with their episodes, as LoadCorpusSeries builds them.
func corpusTitles(dir, instance string, aliases map[int][]decide.SceneMapping) (map[int]decide.Title, error) {
	series, episodes, err := corpusSeriesData(dir, instance)
	if err != nil {
		return nil, err
	}
	out := map[int]decide.Title{}
	for id, s := range series {
		t := s.title()
		if len(t.SceneMappings) == 0 {
			t.SceneMappings = aliases[s.TvdbID]
		}
		for _, e := range episodes[id] {
			t.Episodes = append(t.Episodes, e.episode())
		}
		sort.SliceStable(t.Episodes, func(i, j int) bool {
			a, b := t.Episodes[i], t.Episodes[j]
			return a.Season < b.Season || a.Season == b.Season && a.Number < b.Number
		})
		out[id] = t
	}
	return out, nil
}

// summary is a parse or decision as named fields, compared field by field.
type summary [][2]string

func joinSummary(s summary) string {
	var b []string
	for _, f := range s {
		b = append(b, f[0]+"="+f[1])
	}
	return strings.Join(b, " ")
}

// diffFields names the fields that differ, "" when none.
func diffFields(a, b summary) string {
	var d []string
	for i := range a {
		if a[i][1] != b[i][1] {
			d = append(d, a[i][0])
		}
	}
	return strings.Join(d, ",")
}

func ints(xs []int) string {
	s := make([]string, len(xs))
	for i, x := range xs {
		s[i] = fmt.Sprint(x)
	}
	return strings.Join(s, "|")
}

// parseSummary is the parse as DownloadDecisionMaker takes it (v5: with the season-title fallback).
func parseSummary(v sonarr.Version, title string) summary {
	p := v.ParseTitle(title)
	if p == nil {
		p = v.ParseSeasonTitle(title)
	}
	if p == nil {
		return summary{{"parsed", "no"}, {"series", ""}, {"seasons", ""}, {"episodes", ""}, {"absolute", ""},
			{"type", ""}, {"quality", ""}, {"languages", ""}, {"group", ""}}
	}
	seasons := []int{p.SeasonNumber}
	if p.Parser == sonarr.V5 {
		seasons = p.SeasonNumbers
	}
	if p.IsDaily() {
		seasons = []int{-2} // the air date stands for the season
	}
	var langs []string
	for _, l := range p.Languages {
		langs = append(langs, l.Name)
	}
	group := ""
	if p.ReleaseGroup != nil {
		group = *p.ReleaseGroup
	}
	return summary{{"parsed", "yes"}, {"series", p.SeriesTitle}, {"seasons", ints(seasons)},
		{"episodes", ints(p.EpisodeNumbers) + daily(p)}, {"absolute", ints(p.AbsoluteEpisodeNumbers)},
		{"type", p.ReleaseType().String()}, {"quality", fmt.Sprintf("%s v%d", p.Quality.Quality.Name, p.Quality.Revision.Version)},
		{"languages", strings.Join(langs, "|")}, {"group", group}}
}

func daily(p *sonarr.ParsedEpisodeInfo) string {
	if p.AirDate == nil {
		return ""
	}
	return "@" + *p.AirDate
}

func decisionSummary(d *decide.Decision) summary {
	if d == nil {
		return summary{{"decided", "no"}, {"match", ""}, {"episodes", ""}, {"verdict", ""}, {"quality", ""},
			{"score", ""}, {"languages", ""}}
	}
	verdict := "accepted"
	if !d.Accepted {
		var r []string
		for _, x := range d.Rejections {
			r = append(r, x.Reason)
		}
		verdict = strings.Join(r, "|")
	}
	return summary{{"decided", "yes"}, {"match", string(d.MatchType)}, {"episodes", strings.Join(d.Episodes, "|")},
		{"verdict", verdict}, {"quality", d.Quality}, {"score", fmt.Sprint(d.Score)}, {"languages", strings.Join(d.Languages, "|")}}
}

func sortedCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return m[keys[i]] > m[keys[j]] || m[keys[i]] == m[keys[j]] && keys[i] < keys[j] })
	var b []string
	for _, k := range keys {
		b = append(b, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(b, ", ")
}
