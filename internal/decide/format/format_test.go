package format

import (
	"testing"
)

// The arr has no fixture for CustomFormatCalculationService or SpecificationMatchesGroup; these cases follow
// SpecificationMatchesGroup.DidMatch and the grouping in CustomFormatCalculationService.ParseCustomFormat.

func title(t *testing.T, name, pattern string, negate, required bool) *Spec {
	t.Helper()
	c, err := NewReleaseTitle(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return &Spec{Name: name, Negate: negate, Required: required, Condition: c}
}

func TestGroupDidMatch(t *testing.T) {
	r := func(req bool) *Spec { return &Spec{Required: req, Condition: &Source{}} }
	cases := []struct {
		name    string
		specs   []*Spec
		results []bool
		want    bool
	}{
		{"one true", []*Spec{r(false)}, []bool{true}, true},
		{"one false", []*Spec{r(false)}, []bool{false}, false},
		{"any of optional", []*Spec{r(false), r(false)}, []bool{false, true}, true},
		{"required fails", []*Spec{r(true), r(false)}, []bool{false, true}, false},
		{"required holds, optional fails", []*Spec{r(true), r(false)}, []bool{true, false}, true},
		{"all required hold", []*Spec{r(true), r(true)}, []bool{true, true}, true},
	}
	for _, c := range cases {
		g := Group{Specs: c.specs, Results: c.results}
		if g.DidMatch() != c.want {
			t.Errorf("%s: got %v", c.name, !c.want)
		}
	}
}

func TestFormatMatching(t *testing.T) {
	// TRaSH "1.0 Mono": one required match and three required negations, all Release Title.
	mono := &Format{ID: 1, Name: "1.0 Mono", Specs: []*Spec{
		title(t, "Mono", `[^0-9]1[ .]0\b|\bMono\b|\[PCM \]`, false, true),
		title(t, "Not 3.0ch", `[^0-9]3[ .]0\b`, true, true),
		title(t, "Not 4.0ch", `[^0-9]4[ .]0\b`, true, true),
		title(t, "Not High Channel Count", `[^0-9][5-9][ .][0-1]\b`, true, true),
	}}
	// Groups of different types: (WEB-DL or WEBRip source) and 2160p and not group X.
	ng, _ := NewReleaseGroup(`^X$`)
	web4k := &Format{ID: 2, Name: "WEB 2160p", Specs: []*Spec{
		{Name: "webdl", Condition: &Source{Value: 7}},
		{Name: "webrip", Condition: &Source{Value: 8}},
		{Name: "2160p", Required: true, Condition: &Resolution{Value: 2160}},
		{Name: "not X", Negate: true, Required: true, Condition: ng},
	}}
	empty := &Format{ID: 3, Name: "Always"}
	all := []*Format{web4k, mono, empty}

	cases := []struct {
		name string
		in   Input
		want []string
	}{
		{"mono", Input{ReleaseTitle: Str("A.Movie.1080p.FLAC.1.0-G")}, []string{"1.0 Mono", "Always"}},
		{"mono excluded by 5.1", Input{ReleaseTitle: Str("A.Movie.DTS.5.1.FLAC.1.0-G")}, []string{"Always"}},
		{"web 2160p rip", Input{ReleaseTitle: Str("x"), Source: 8, Resolution: 2160, ReleaseGroup: Str("Y")}, []string{"Always", "WEB 2160p"}},
		{"web 2160p wrong group", Input{ReleaseTitle: Str("x"), Source: 7, Resolution: 2160, ReleaseGroup: Str("x")}, []string{"Always"}},
		{"web 1080p", Input{ReleaseTitle: Str("x"), Source: 7, Resolution: 1080}, []string{"Always"}},
		{"bluray 2160p", Input{ReleaseTitle: Str("x"), Source: 9, Resolution: 2160}, []string{"Always"}},
	}
	for _, c := range cases {
		got, err := Calculate(all, &c.in)
		if err != nil {
			t.Fatal(err)
		}
		if names(got) != join(c.want) {
			t.Errorf("%s: got %s, want %s", c.name, names(got), join(c.want))
		}
	}

	p := &Profile{FormatItems: []FormatItem{{Format: 1, Score: -10}, {Format: 2, Score: 100}, {Format: 3, Score: 5}, {Format: 9, Score: 1000}}}
	got, _ := Calculate(all, &Input{ReleaseTitle: Str("x.1.0"), Source: 7, Resolution: 2160})
	if s := p.Score(got); s != 95 {
		t.Errorf("score = %d, want 95", s)
	}
}

func names(fs []*Format) string {
	var s []string
	for _, f := range fs {
		s = append(s, f.Name)
	}
	return join(s)
}

func join(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += "|"
		}
		out += x
	}
	return out
}
