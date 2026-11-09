package anime

import (
	"strings"
	"testing"
)

func keysOf2(rs []*Row) string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Key)
	}
	return strings.Join(out, " ")
}

// fateRows is the golden Fate watch order, shortened: the line (Zero, UBW), side stories, and three alternates.
func fateRows() (main, alts []*Row) {
	for _, k := range []string{"al:10087", "al:13263#1", "al:11741", "al:14829", "al:19603", "al:20792", "al:106918"} {
		main = append(main, &Row{Key: k, Role: "mainline", Optional: bp(false)})
	}
	alts = []*Row{
		{Key: "al:356", Role: "alternate", AlternateOf: "al:19603", Optional: bp(true)},
		{Key: "tvdb:79151:S00E01", Role: "alternate", AlternateOf: "al:19603", Route: "al:356", Optional: bp(true)},
		{Key: "al:6922", Role: "alternate", AlternateOf: "al:19603", Optional: bp(true)},
		{Key: "al:20791", Role: "alternate", AlternateOf: "al:19603", Optional: bp(true)},
		{Key: "al:21718", Role: "alternate", AlternateOf: "al:19603", Route: "al:20791", Optional: bp(true)},
	}
	return
}

func TestApplyRoutes(t *testing.T) {
	ov := &Overrides{Routes: []string{"al:356", "al:19603", "al:20791"}, LinePosition: "last"}
	main, alts := fateRows()
	m, a, res := applyRoutes(main, alts, ov)
	want := "al:356 tvdb:79151:S00E01 al:19603 al:20792 al:106918 al:20791 al:21718 al:10087 al:13263#1 al:11741 al:14829"
	if got := keysOf2(m); got != want {
		t.Errorf("order\n got %s\nwant %s", got, want)
	}
	if keysOf2(a) != "al:6922" || len(res.Applied) != 3 || len(res.Failed) != 0 {
		t.Errorf("alternates %s, %+v", keysOf2(a), res)
	}
	for i, r := range m {
		if *r.N != i+1 {
			t.Errorf("row %s numbered %d", r.Key, *r.N)
		}
	}
	if m[0].Role != "route" || *m[0].Optional || m[1].Route != "al:356" || m[2].Route != "al:19603" || m[4].Route != "al:19603" || m[7].Route != "" {
		t.Errorf("marks: %+v %+v %+v", m[0], m[1], m[7])
	}

	// first (the default): the line, then the routes; an unknown key is reported
	main, alts = fateRows()
	m, _, res = applyRoutes(main, alts, &Overrides{Routes: []string{"al:20791", "al:999"}})
	if got := keysOf2(m); got != "al:10087 al:13263#1 al:11741 al:14829 al:19603 al:20792 al:106918 al:20791 al:21718" {
		t.Errorf("first: %s", got)
	}
	if len(res.Failed) != 1 || res.Failed[0] != "al:999" {
		t.Errorf("failed: %+v", res)
	}
}

func TestAltCut(t *testing.T) {
	ov, err := ParseOverrides([]byte(`
alt_cuts:
  - {name: "Director's Cut", tvdb: 305089, entry: "kitsu:42", map: {DC1: S01E01, DC2: [S01E02, S01E03], DC3: S01E04}}
`))
	if err != nil {
		t.Fatal(err)
	}
	cuts := ov.AltCutMaps()
	if len(cuts) != 1 {
		t.Fatal(cuts)
	}
	c := cuts[0]
	if len(c.Episodes[2]) != 2 || c.Episodes[2][1] != (SE{1, 3}) || c.Range() != "S01E01-E04" {
		t.Errorf("%+v %s", c.Episodes, c.Range())
	}
	for title, want := range map[string]bool{
		"[Group] Re ZERO Starting Life in Another World - Directors Cut - 02 [1080p]": true,
		"Re.Zero.Directors.Cut.S01E02.1080p":                                          true,
		"[SubsPlease] Re Zero - 02 (1080p)":                                           false,
	} {
		if c.Matches(title) != want {
			t.Errorf("%s: want %v", title, want)
		}
	}
	rows := []*Row{{Key: "kitsu:42"}, {Key: "al:1"}}
	markAltCuts(cuts, rows)
	if rows[0].AltCut != "Director's Cut" || rows[0].AltCutTVDB != "305089 S01E01-E04" || rows[1].AltCut != "" {
		t.Errorf("%+v", rows[0])
	}
}

func TestRowEpisodes(t *testing.T) {
	eps := []SE{{1, 11}, {1, 12}, {1, 13}, {1, 14}}
	for part, want := range map[string]string{
		"":                                "S01E11-E14",
		"up to before S01E13":             "S01E11-E12",
		"from S01E12 up to before S01E13": "S01E12",
		"from S01E13":                     "S01E13-E14",
		"S01E11, S01E14":                  "S01E11, S01E14",
	} {
		r := &Row{}
		if part != "" {
			r.Part = sp(part)
		}
		if got := ranges(RowEpisodes(r, eps)); got != want {
			t.Errorf("%q: %s, want %s", part, got, want)
		}
	}
}
