package adapt

import (
	"reflect"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
)

// The cases of Radarr's and Sonarr's AggregateLanguagesFixture that apply without a release (the indexer cases need
// one), with the fixture's movie title or first episode title "Jimmy The Greek" where the case sets it.
func TestAggregateLanguagesFixture(t *testing.T) {
	const simple = "Series.Title.S01E01.xyz-RlsGroup"
	en, fr, el := radarr.LanguageEnglish.ID, radarr.LanguageFrench.ID, radarr.LanguageGreek.ID
	cases := []struct {
		name          string
		parsed        []int
		title, tokens string
		mediaTitle    string
		want          []int
		wantContains  int
		containsMeans bool
	}{
		{name: "should_return_existing_language_if_episode_title_does_not_have_language", parsed: []int{langOriginal},
			title: simple, mediaTitle: "Some Movie", wantContains: en, containsMeans: true},
		{name: "should_return_parsed_language", parsed: []int{fr}, title: simple, mediaTitle: "Some Movie",
			want: []int{fr}},
		{name: "should_return_original_when_no_indexer_value", parsed: []int{},
			title: "Series.Title.S01E01.MULTi.1080p.WEB.H265-RlsGroup", mediaTitle: "Some Movie", want: []int{en}},
		{name: "should_exclude_language_that_is_part_of_episode_title_when_release_tokens_contains_episode_title",
			parsed: []int{el}, title: "Series.Title.S01E01.Jimmy.The.Greek.xyz-RlsGroup",
			tokens: ".Jimmy.The.Greek.xyz-RlsGroup", mediaTitle: "Jimmy The Greek", want: []int{en}},
		{name: "should_remove_parsed_language_that_is_part_of_episode_title_when_release_tokens_contains_episode_title",
			parsed: []int{el, fr}, title: "Series.Title.S01E01.Jimmy.The.Greek.French.xyz-RlsGroup",
			tokens: ".Jimmy.The.Greek.French.xyz-RlsGroup", mediaTitle: "Jimmy The Greek", want: []int{fr}},
		{name: "should_not_exclude_language_that_is_part_of_episode_title_when_release_tokens_does_not_contain_episode_title",
			parsed: []int{el}, title: "Series.Title.S01E01.xyz-RlsGroup", tokens: ".xyz-RlsGroup",
			mediaTitle: "Jimmy The Greek", want: []int{el}},
		{name: "should_use_reparse_language_after_determining_languages_that_are_in_episode_titles",
			parsed: []int{el}, title: "Series.Title.S01E01.Jimmy.The.Greek.Greek.xyz-RlsGroup",
			tokens: ".Jimmy.The.Greek.Greek.xyz-RlsGroup", mediaTitle: "Jimmy The Greek", want: []int{el}},
	}
	for _, fl := range []struct {
		name string
		ops  langOps
	}{{"radarr", radarrLangs}, {"sonarr", sonarrLangs}} {
		for _, c := range cases {
			tokens := c.tokens
			if tokens == "" {
				tokens = c.title
			}
			got, _, err := aggregateLanguages(fl.ops, c.parsed, tokens, true, []string{c.mediaTitle}, en, nil)
			if err != nil {
				t.Fatalf("%s %s: %v", fl.name, c.name, err)
			}
			if c.containsMeans {
				if !contains(got, c.wantContains) {
					t.Errorf("%s %s: %v does not contain %d", fl.name, c.name, got, c.wantContains)
				}
			} else if !reflect.DeepEqual(got, c.want) {
				t.Errorf("%s %s: got %v, want %v", fl.name, c.name, got, c.want)
			}
		}
	}
}

// The indexer cases of Radarr's and Sonarr's AggregateLanguagesFixture (the same in both): the indexer's Multi
// Languages setting, found by id or by name in the arr, is the release's MultiLanguages here. The arr compares with
// BeEquivalentTo (any order); we check the arr's order too.
func TestAggregateLanguagesIndexerFixture(t *testing.T) {
	en, fr, de := radarr.LanguageEnglish.ID, radarr.LanguageFrench.ID, radarr.LanguageGerman.ID
	multi := []int{langOriginal, fr}
	const mt = "Series.Title.S01E01.MULTi.1080p.WEB.H265-RlsGroup"
	cases := []struct {
		name   string
		parsed []int
		rel    *Release
		want   []int
	}{
		{"should_return_multi_languages_when_indexer_id_has_multi_languages_configuration", []int{},
			&Release{Title: mt, MultiLanguages: multi}, []int{fr, en}},
		{"should_return_multi_languages_when_release_as_unknown_as_default_language_and_indexer_has_multi_languages_configuration",
			[]int{langUnknown}, &Release{Title: mt, MultiLanguages: multi}, []int{fr, en}},
		{"should_return_multi_languages_when_release_as_specified_language_and_indexer_has_multi_languages_configuration",
			[]int{fr}, &Release{Title: "Some.Movie.2024.MULTi.VFF.VFQ.1080p.BluRay.DTS.HDMA.x264-RlsGroup", MultiLanguages: multi},
			[]int{fr, en}},
		{"should_return_multi_languages_when_release_as_other_language_and_indexer_has_multi_languages_configuration",
			[]int{de}, &Release{Title: "Some.Movie.2024.MULTi.GERMAN.1080p.BluRay.DTS.HDMA.x264-RlsGroup", MultiLanguages: multi},
			[]int{de, fr, en}},
		{"should_return_original_when_indexer_has_no_multi_languages_configuration", []int{},
			&Release{Title: mt}, []int{en}},
		{"should_return_original_when_no_indexer_value", []int{}, &Release{Title: mt}, []int{en}},
		// Not in the fixture: the indexer's own languages replace the parsed ones, mapped or not.
		{"release languages win", []int{fr}, &Release{Title: mt, Languages: []int{de}}, []int{de}},
		{"no MULTI in the title", []int{}, &Release{Title: "Some.Movie.2024.1080p.WEB-GRP", MultiLanguages: multi}, []int{en}},
	}
	for _, fl := range []struct {
		name string
		ops  langOps
	}{{"radarr", radarrLangs}, {"sonarr", sonarrLangs}} {
		for _, c := range cases {
			got, _, err := aggregateLanguages(fl.ops, c.parsed, c.rel.Title, true, []string{"Some Movie"}, en, c.rel)
			if err != nil || !reflect.DeepEqual(got, c.want) {
				t.Errorf("%s %s: got %v %v, want %v", fl.name, c.name, got, err, c.want)
			}
		}
	}
	// Unmapped: the indexer's languages, else the parse.
	got, _, _ := aggregateLanguages(radarrLangs, []int{fr}, mt, false, nil, 0, &Release{Title: mt, Languages: []int{de}})
	if !reflect.DeepEqual(got, []int{de}) {
		t.Errorf("unmapped with release languages: %v", got)
	}
	got, _, _ = aggregateLanguages(radarrLangs, []int{fr}, mt, false, nil, 0, &Release{Title: mt, MultiLanguages: multi})
	if !reflect.DeepEqual(got, []int{fr}) {
		t.Errorf("unmapped without release languages: %v", got)
	}
}

func TestAggregateLanguagesRules(t *testing.T) {
	en, fr, ja := 1, 2, 8
	cases := []struct {
		name     string
		parsed   []int
		mapped   bool
		original int
		want     []int
		assumed  bool // the original language stood in for none found
	}{
		{"unmapped keeps the parse", []int{langUnknown}, false, ja, []int{langUnknown}, false},
		{"unmapped keeps duplicates and Original", []int{fr, fr, langOriginal}, false, ja, []int{fr, fr, langOriginal}, false},
		{"Unknown falls back to the original language", []int{langUnknown}, true, ja, []int{ja}, true},
		{"empty falls back", []int{}, true, ja, []int{ja}, true},
		{"mapped dedupes (Except)", []int{fr, en, fr}, true, ja, []int{fr, en}, false},
		{"Original becomes the original language", []int{fr, langOriginal}, true, ja, []int{fr, ja}, false},
		{"Original alone is stated, not assumed", []int{langOriginal}, true, ja, []int{ja}, false},
		{"Original already present becomes Unknown", []int{ja, langOriginal}, true, ja, []int{ja, langUnknown}, false},
		{"Unknown with another stays", []int{langUnknown, fr}, true, ja, []int{langUnknown, fr}, false},
	}
	for _, c := range cases {
		got, assumed, err := aggregateLanguages(radarrLangs, c.parsed, "Movie.2020.1080p.WEB-DL-GRP", c.mapped, []string{"Movie"},
			c.original, nil)
		if err != nil || !reflect.DeepEqual(got, c.want) || assumed != c.assumed {
			t.Errorf("%s: got %v %v %v, want %v %v", c.name, got, assumed, err, c.want, c.assumed)
		}
	}
	// The normalised index applied to the raw tokens can run past the end: the arr throws, we return an error.
	if _, _, err := aggregateLanguages(langOps{
		parse:     func(s string) []int { return []int{fr} },
		normalize: func(s string) string { return "padding padding french" },
	}, []int{fr}, "french", true, []string{"French"}, en, nil); err == nil {
		t.Error("Remove past the end: no error")
	}
}

func TestIndexFold(t *testing.T) {
	for _, c := range []struct {
		s, n string
		want int
	}{{"abc", "", 0}, {"abc", "BC", 1}, {"xyz", "a", -1}, {"éé jimmy", "JIMMY", 3}, {"ab", "abc", -1}} {
		if got := indexFold(c.s, c.n); got != c.want {
			t.Errorf("indexFold(%q, %q) = %d, want %d", c.s, c.n, got, c.want)
		}
	}
}

func TestRadarrInput(t *testing.T) {
	p := radarr.ParseMovieTitle("The.French.Dispatch.2021.2160p.UHD.BluRay.REMUX.HDR.HEVC.Atmos-GRP", false)
	if p == nil {
		t.Fatal("no parse")
	}
	in, err := Radarr(p, &Movie{Title: "The French Dispatch", Year: 2021, OriginalLanguage: 1})
	if err != nil {
		t.Fatal(err)
	}
	if in.Flavour != format.Radarr || *in.ReleaseTitle != p.SimpleReleaseTitle || *in.ReleaseGroup != "GRP" ||
		in.Source != 9 || in.Modifier != 5 || in.Resolution != 2160 || in.Year != 2021 ||
		in.Media == nil || in.Media.Year != 2021 {
		t.Errorf("input %+v", in)
	}
	// Radarr parses languages from the simplified title, so "French" in the movie name is not one: [Unknown], which
	// the aggregation replaces with the original language.
	if !reflect.DeepEqual(in.Languages, []int{1}) {
		t.Errorf("languages %v", in.Languages)
	}
	// Unmapped: the parsed languages as they are, and no media.
	in, _ = Radarr(p, nil)
	if !reflect.DeepEqual(in.Languages, []int{0}) || in.Media != nil {
		t.Errorf("unmapped: %v %+v", in.Languages, in.Media)
	}
	if in, err := Radarr(nil, nil); in != nil || err != nil {
		t.Errorf("nil parse: %v %v", in, err)
	}
}

func TestSonarrInput(t *testing.T) {
	p := sonarr.ParseTitle("Series.Title.S01.1080p.WEB-DL.DDP5.1.H.264-GRP")
	if p == nil {
		t.Fatal("no parse")
	}
	in, err := Sonarr(p, &Series{Year: 2020, OriginalLanguage: 8}, []Episode{{Title: "Pilot"}})
	if err != nil {
		t.Fatal(err)
	}
	if in.Flavour != format.Sonarr || *in.ReleaseTitle != p.ReleaseTitle || in.ReleaseType != 3 || in.Source != 3 ||
		in.Resolution != 1080 || !reflect.DeepEqual(in.Languages, []int{8}) {
		t.Errorf("input %+v", in)
	}
}
