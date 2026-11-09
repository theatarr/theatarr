package anime

import (
	"context"
	"strings"
	"testing"
)

func TestPyHelpers(t *testing.T) {
	if d := toDate("2021"); d.iso() != "2021-01-01" {
		t.Errorf("toDate year: %s", d.iso())
	}
	if d := toDate("2021-04"); d.iso() != "2021-04-01" {
		t.Errorf("toDate month: %s", d.iso())
	}
	if toDate("2019-01-04T00:00:00Z") != 0 || toDate("") != 0 || toDate("2021-13-01") != 0 {
		t.Error("toDate accepts what date.fromisoformat refuses")
	}
	if toDate("2019-11-08")-toDate("2018-10-06") != 398 {
		t.Error("date difference")
	}
	for in, want := range map[string]string{"2021": "2021-00-00", "2021-04": "2021-04-00", "2021-04-06": "2021-04-06"} {
		if got := sortDate(sp(in)); got != want {
			t.Errorf("sortDate(%s) = %s", in, got)
		}
	}
	if sortDate(nil) != "9999" {
		t.Error("sortDate(None)")
	}
	if got := ranges([]SE{{1, 1}, {1, 2}, {1, 3}, {0, 5}}); got != "S01E01-E03, S00E05" {
		t.Errorf("ranges: %s", got)
	}
	for f, want := range map[float64]string{1: "1.0", 0.5: "0.5", 2.0 / 3: "0.6666666666666666", 0: "0.0"} {
		if got := pyFloat(f); got != want {
			t.Errorf("pyFloat(%v) = %s", f, got)
		}
	}
	if round2(0.675) != 0.68 && round2(0.675) != 0.67 {
		t.Error("round2")
	}
	if got := stem("Fate/stay night: Unlimited Blade Works"); got != "fate stay night" {
		t.Errorf("stem: %q", got)
	}
	if got := stem("Kimetsu no Yaiba Movie: Mugen Ressha-hen"); got != "kimetsu no yaiba" {
		t.Errorf("stem movie: %q", got)
	}
	// Python's \b is Unicode-aware: a Japanese letter next to "PV" is a word character
	if extraWords.MatchString("進撃の巨人PV第1弾") {
		t.Error(`\b matched between a Japanese letter and PV`)
	}
	if !extraWords.MatchString("Attack on Titan PV") || !recapWords.MatchString("Soushuuhen") || !recapWords.MatchString("Tokubetsu Henshuuban") {
		t.Error("word alternatives")
	}
	if pyRepr("it's") != `"it's"` || pyRepr("a") != "'a'" {
		t.Error("pyRepr")
	}
	if pyQuote("Re:Zero Memory Snow/x") != "Re%3AZero%20Memory%20Snow/x" {
		t.Error("pyQuote")
	}
}

func TestOverridesParse(t *testing.T) {
	ov, err := ParseOverrides([]byte(`
source: https://myanimelist.net/stacks/53018
moves:
  - {entry: "al:100049#1", after: "al:21355@S01E11", why: "Memory Snow"}
  - {entry: "al:17074@S03E06-S03E09", after: "start"}
mappings:
  - {entry: "al:146065", tvdb: 371310, episodes: "S00E02, S02E01-E12", why: "ep 0 is a special"}
exclude: ["kitsu:42903"]
routes: [al:356, al:19603]
line_position: last
alt_cuts:
  - {name: "Director's Cut", tvdb: 305089, map: {DC1: S01E01, DC2: [S01E02, S01E03]}}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.Moves) != 2 || ov.Moves[0].After != "al:21355@S01E11" || len(ov.Mappings) != 1 || ov.Exclude[0] != "kitsu:42903" {
		t.Errorf("%+v", ov)
	}
	eps, _ := parseRangeLabel(ov.Mappings[0].Episodes)
	if len(eps) != 13 || eps[0] != (SE{0, 2}) || eps[12] != (SE{2, 12}) {
		t.Errorf("episodes %v", eps)
	}
	for _, bad := range []string{
		`moves: [{entry: "x:1", after: start}]`,
		`moves: [{entry: "al:1@S1", after: start}]`,
		`mappings: [{entry: "al:1", tvdb: 0, episodes: "S01E01"}]`,
		`mappings: [{entry: "al:1", tvdb: 5, episodes: "S01E05-E01"}]`,
		`line_position: middle`,
		`line_position: last`,
		`routes: [al:1, al:1]`,
		`alt_cuts: [{name: DC, map: {DC1: S01E01}}]`,
		`alt_cuts: [{name: DC, tvdb: 5, map: {DC: S01E01}}]`,
		`alt_cuts: [{name: DC, tvdb: 5, map: {DC1: 1x01}}]`,
		`alt_cuts: [{name: DC, tvdb: 5, match: "(", map: {DC1: S01E01}}]`,
		`unknown_key: 1`,
	} {
		if _, err := ParseOverrides([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	// JSON overrides files load as they are
	ov, err = ParseOverrides([]byte(`{"source": "https://x", "moves": [{"entry": "al:1", "after": "al:2", "why": "w"}]}`))
	if err != nil || ov.Moves[0].pyRepr() != "{'entry': 'al:1', 'after': 'al:2', 'why': 'w'}" {
		t.Errorf("json overrides: %v %+v", err, ov)
	}
}

func TestClassify(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	a := MappingCandidate{Source: "anibridge", DateFit: f(1), Score: 4}
	s := MappingCandidate{Source: "scudlee", DateFit: f(1), Score: 3.85}
	if st, _ := classify([]MappingCandidate{a, s}, true, f(0.15), "", "anibridge"); st != StatusConflict {
		t.Errorf("small margin: %s", st)
	}
	if st, _ := classify([]MappingCandidate{a, s}, true, f(0.6), "", "anibridge"); st != StatusArbitrated {
		t.Errorf("clear margin: %s", st)
	}
	a.Anchor = AnchorMatch
	if st, _ := classify([]MappingCandidate{a, s}, true, f(0.15), "", "anibridge"); st != StatusArbitrated {
		t.Errorf("anchor match: %s", st)
	}
	a.Anchor = AnchorMismatch
	if st, _ := classify([]MappingCandidate{a, s}, true, f(0.9), "", "anibridge"); st != StatusConflict {
		t.Errorf("anchor mismatch: %s", st)
	}
	if st, _ := classify([]MappingCandidate{s, a}, false, nil, "", "scudlee"); st != StatusAuto {
		t.Errorf("agreeing lists: %s", st)
	}
	if st, _ := classify([]MappingCandidate{s}, false, nil, "", "scudlee"); st != StatusAuto {
		t.Errorf("one list with a date fit: %s", st)
	}
	if st, _ := classify([]MappingCandidate{s}, false, nil, "it is a direct AniDB join", "scudlee"); st != StatusArbitrated {
		t.Errorf("kept despite dates: %s", st)
	}
}

// fakeTVDB serves one series.
type fakeTVDB struct{ show *Show }

func (f fakeTVDB) Show(context.Context, int) (*Show, error) { return f.show, nil }

func TestAnchorSignal(t *testing.T) {
	before := ";1-13;"
	season := "1"
	rec := &ScudLeeRec{AniDBID: 5, TVDBID: ip(100), TVDBSeason: &season, Before: &before,
		Mappings: []ScudLeeMapping{{AniDBSeason: 0, TVDBSeason: 0, Pairs: []ScudLeePair{{Src: 1, Dst: []int{3}}}}}}
	l := &Lists{ScudLee: map[int]*ScudLeeRec{5: rec}}
	mk := func(airsBefore int) *builder {
		sh := &Show{Title: sp("S"), HasEpisodes: true, Episodes: []ShowEpisode{
			{SeasonNumber: 0, EpisodeNumber: 3, AiredBeforeSeasonNumber: ip(1), AiredBeforeEpisodeNumber: ip(airsBefore)},
			{SeasonNumber: 1, EpisodeNumber: 13}}}
		src := &Sources{TVDB: fakeTVDB{sh}, Lists: l}
		return &builder{ctx: context.Background(), src: src, m: newMapper(context.Background(), src)}
	}
	e := &Entry{AniDBID: ip(5)}
	if got := mk(13).anchor(e, 100, []SE{{1, 1}, {0, 3}}); got != AnchorMatch {
		t.Errorf("TVDB airsBefore S01E13 = ScudLee before 13: %q", got)
	}
	if got := mk(20).anchor(e, 100, []SE{{1, 1}, {0, 3}}); got != AnchorMismatch {
		t.Errorf("TVDB airsBefore S01E20: %q", got)
	}
	if got := mk(13).anchor(e, 100, []SE{{1, 1}}); got != "" {
		t.Errorf("candidate without the special: %q", got)
	}
}

func TestMappingOverrideAndExclude(t *testing.T) {
	b := &builder{excluded: map[string]bool{"al:2": true}}
	e1 := &Entry{Key: "al:1", TVDB: &TVDBMap{SeriesID: 9, Episodes: []SE{{1, 1}}, Method: "scudlee"},
		Mapping: &MappingOutcome{Status: StatusConflict, Candidates: []MappingCandidate{{Source: "scudlee"}}}}
	e2 := &Entry{Key: "al:2"}
	b.applyMappingOverrides([]*Entry{e1, e2}, &Overrides{Mappings: []MappingFix{{Entry: "al:1", TVDB: 10, Episodes: "S00E02", Why: "w"}}})
	if e1.TVDB.SeriesID != 10 || e1.TVDB.Method != "override" || e1.Mapping.Status != StatusOverride || len(e1.Mapping.Candidates) != 1 {
		t.Errorf("override: %+v %+v", e1.TVDB, e1.Mapping)
	}
	if got := b.inOrders([]*Entry{e1, e2}); len(got) != 1 || got[0] != e1 {
		t.Error("exclude")
	}
	if !strings.Contains(e1.Mapping.Reason, "w") {
		t.Error("reason")
	}
}

type errTVDB struct{}

func (errTVDB) Show(context.Context, int) (*Show, error) { return nil, context.Canceled }

func TestTVDBChain(t *testing.T) {
	sh := &Show{Title: sp("x")}
	if got, err := (TVDBChain{errTVDB{}, fakeTVDB{sh}}).Show(context.Background(), 1); err != nil || got != sh {
		t.Errorf("fallback: %v %v", got, err)
	}
	if _, err := (TVDBChain{errTVDB{}}).Show(context.Background(), 1); err == nil {
		t.Error("no backend has it")
	}
}
