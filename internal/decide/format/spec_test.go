package format

import (
	"encoding/json"
	"os"
	"testing"
)

func TestArrLanguageFixtures(t *testing.T) {
	b, err := os.ReadFile("testdata/arr_language_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Fixture, Test, Flavour string
		OriginalLanguage       int
		Languages              []int
		Value                  int
		ExceptLanguage, Negate bool
		Expected               bool
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Flavour+"/"+c.Fixture+"/"+c.Test, func(t *testing.T) {
			in := &Input{Flavour: Flavour(c.Flavour), Languages: c.Languages, Filename: Str("Movie.Title.2024"),
				Size: 100 << 20, Media: &Media{OriginalLanguage: c.OriginalLanguage}}
			s := &Spec{Negate: c.Negate, Condition: &Language{Value: c.Value, ExceptLanguage: c.ExceptLanguage}}
			got, err := s.IsSatisfiedBy(in)
			if err != nil || got != c.Expected {
				t.Fatalf("got %v, %v; want %v", got, err, c.Expected)
			}
		})
	}
}

func TestConditions(t *testing.T) {
	rt, err := NewReleaseTitle(`\bREMUX\b`)
	if err != nil {
		t.Fatal(err)
	}
	blank, _ := NewReleaseTitle("  ")
	ed, _ := NewEdition(`IMAX`)
	rgEmpty, _ := NewReleaseGroup(`^$`)
	rg, _ := NewReleaseGroup(`^(FLUX|NTb)$`)
	ci, _ := NewReleaseTitle(`remux`)
	look, _ := NewReleaseTitle(`^(?!.*\bHDR\b).*\bDV\b`) // .NET lookahead

	base := func() *Input {
		return &Input{Flavour: Radarr, ReleaseTitle: Str("A.Movie.2024.2160p.UHD.BluRay.REMUX.DV-FraMeSToR"),
			ReleaseGroup: Str("FraMeSToR"), Edition: Str(""), Source: 9, Resolution: 2160, Modifier: 5,
			Languages: []int{1}, Size: 50 << 30, Year: 2024}
	}
	tests := []struct {
		name string
		c    Condition
		neg  bool
		in   func(*Input)
		want bool
	}{
		{"title matches case-insensitively", ci, false, nil, true},
		{"title regex", rt, false, nil, true},
		{"title negated", rt, true, nil, false},
		{"title nil never matches", rt, false, func(i *Input) { i.ReleaseTitle = nil }, false},
		{"title nil negated matches", rt, true, func(i *Input) { i.ReleaseTitle = nil }, true},
		{"filename also matched", rt, false, func(i *Input) { i.ReleaseTitle = Str("x"); i.Filename = Str("y.REMUX.mkv") }, true},
		{"blank pattern never matches", blank, false, nil, false},
		{"lookahead", look, false, nil, true},
		{"lookahead excludes", look, false, func(i *Input) { i.ReleaseTitle = Str("x.DV.HDR-g") }, false},
		{"edition empty string", ed, false, nil, false},
		{"edition", ed, false, func(i *Input) { i.Edition = Str("IMAX Enhanced") }, true},
		{"group empty-string pattern on empty", rgEmpty, false, func(i *Input) { i.ReleaseGroup = Str("") }, true},
		{"group empty-string pattern on null", rgEmpty, false, func(i *Input) { i.ReleaseGroup = nil }, false},
		{"group", rg, false, func(i *Input) { i.ReleaseGroup = Str("flux") }, true},
		{"source", &Source{Value: 9}, false, nil, true},
		{"source other", &Source{Value: 7}, false, nil, false},
		{"resolution", &Resolution{Value: 2160}, false, nil, true},
		{"modifier", &QualityModifier{Value: 5}, false, nil, true},
		{"modifier none", &QualityModifier{Value: 0}, false, nil, false},
		{"indexer flag set", &IndexerFlag{Value: 1}, false, func(i *Input) { i.IndexerFlags = 1 | 128 }, true},
		{"indexer flag unset", &IndexerFlag{Value: 2}, false, func(i *Input) { i.IndexerFlags = 1 }, false},
		{"indexer flag zero always", &IndexerFlag{Value: 0}, false, nil, true},
		{"size inside", &Size{Min: 40, Max: 50}, false, nil, true},
		{"size min exclusive", &Size{Min: 50, Max: 60}, false, nil, false},
		{"size zero", &Size{Min: 0, Max: 1}, false, func(i *Input) { i.Size = 0 }, false},
		{"year parsed", &Year{Min: 2020, Max: 2024}, false, nil, true},
		{"year from movie", &Year{Min: 1990, Max: 1999}, false, func(i *Input) { i.Year = 0; i.Media = &Media{Year: 1995} }, true},
		{"year none", &Year{Min: 0, Max: 3000}, false, func(i *Input) { i.Year = 0 }, false},
		{"year none negated", &Year{Min: 0, Max: 3000}, true, func(i *Input) { i.Year = 0 }, true},
		{"release type", &ReleaseType{Value: 3}, false, func(i *Input) { i.ReleaseType = 3 }, true},
		{"language null", &Language{Value: 1}, false, func(i *Input) { i.Languages = nil }, false},
		{"language null negated", &Language{Value: 1}, true, func(i *Input) { i.Languages = nil }, false},
		{"language original unknown media", &Language{Value: -2}, false, func(i *Input) { i.Languages = []int{-2} }, true},
		{"language original resolved", &Language{Value: -2}, false, func(i *Input) { i.Media = &Media{OriginalLanguage: 1} }, true},
		{"language original with unknown original", &Language{Value: -2}, false, func(i *Input) { i.Media = &Media{OriginalLanguage: 0} }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			if tc.in != nil {
				tc.in(in)
			}
			s := &Spec{Name: tc.name, Negate: tc.neg, Condition: tc.c}
			got, err := s.IsSatisfiedBy(in)
			if err != nil || got != tc.want {
				t.Fatalf("got %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestGigabytes(t *testing.T) {
	for _, c := range []struct {
		g    float64
		want int64
	}{{0, 0}, {1, 1 << 30}, {0.5, 1 << 29}, {1.5e9, 1610612736000000000}} {
		if got := gigabytes(c.g); got != c.want {
			t.Errorf("gigabytes(%v) = %d, want %d", c.g, got, c.want)
		}
	}
	if got := gigabytes(1e300); got <= 0 {
		t.Errorf("huge size did not saturate: %d", got)
	}
}

func TestEnums(t *testing.T) {
	for _, c := range []struct {
		fl   Flavour
		s    string
		want int
	}{{Radarr, "webdl", 7}, {Radarr, "bluray", 9}, {Radarr, "tv", 6}, {Sonarr, "web", 3}, {Sonarr, "webRip", 4},
		{Sonarr, "blurayRaw", 7}, {Sonarr, "television", 1}, {Radarr, "5", 5}} {
		if got, err := c.fl.SourceValue(c.s); err != nil || got != c.want {
			t.Errorf("%s source %q = %d, %v; want %d", c.fl, c.s, got, err, c.want)
		}
	}
	if v, _ := ModifierValue("remux"); v != 5 {
		t.Errorf("remux = %d", v)
	}
	if v, _ := ReleaseTypeValue("seasonPack"); v != 3 {
		t.Errorf("seasonPack = %d", v)
	}
	if v, ok := Radarr.IndexerFlagsValue("G_Freeleech, G_Scene"); !ok || v != 129 {
		t.Errorf("radarr flags = %d %v", v, ok)
	}
	if v, ok := Sonarr.IndexerFlagsValue("freeleech, scene"); !ok || v != 17 {
		t.Errorf("sonarr flags = %d %v", v, ok)
	}
	if _, ok := Sonarr.IndexerFlagsValue("G_Freeleech"); ok {
		t.Error("radarr flag name read as sonarr")
	}
	if id, ok := Radarr.LanguageID("Portuguese (Brazil)"); !ok || id != 30 {
		t.Errorf("radarr pt-br = %d", id)
	}
	if id, ok := Sonarr.LanguageID("Portuguese (Brazil)"); !ok || id != 33 {
		t.Errorf("sonarr pt-br = %d", id)
	}
}

func TestSeaDex(t *testing.T) {
	body := []byte(`{"name": "Seadex", "specifications": [
		{"name": "SeaDex best", "implementation": "SeaDexSpecification", "required": true, "fields": {"value": "best"}}]}`)
	var j FormatJSON
	if err := json.Unmarshal(body, &j); err != nil {
		t.Fatal(err)
	}
	for _, fl := range []Flavour{Radarr, Sonarr} {
		f, err := j.Build(fl)
		if err != nil {
			t.Fatalf("%s: %v", fl, err)
		}
		if !f.Matches(&Input{Flavour: fl, SeaDex: SeaDexBest}) || f.Matches(&Input{Flavour: fl, SeaDex: SeaDexAlt}) ||
			f.Matches(&Input{Flavour: fl}) {
			t.Errorf("%s: best", fl)
		}
	}
	for _, c := range []struct {
		value  string
		negate bool
		want   [3]bool // none, alt, best
	}{
		{"best", false, [3]bool{false, false, true}},
		{"alt", false, [3]bool{false, true, false}},
		{"Any", false, [3]bool{false, true, true}},
		{"any", true, [3]bool{true, false, false}},
	} {
		c2, err := NewSeaDex(c.value)
		if err != nil {
			t.Fatal(err)
		}
		s := &Spec{Name: "x", Negate: c.negate, Condition: c2}
		for r, want := range c.want {
			if got, _ := s.IsSatisfiedBy(&Input{SeaDex: SeaDexRank(r)}); got != want {
				t.Errorf("%s negate %v on %s: %v", c.value, c.negate, SeaDexRank(r), got)
			}
		}
	}
	if _, err := NewSeaDex("top"); err == nil {
		t.Error("value top: want an error")
	}
}
