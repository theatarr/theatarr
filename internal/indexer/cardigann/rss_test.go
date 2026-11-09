package cardigann

import (
	"strings"
	"testing"
)

// rssDef is a minimal public JSON site for the RSS checks: cats are its category mappings, paths its search paths.
func rssDef(t *testing.T, cats, paths string) *Definition {
	t.Helper()
	def, err := ParseDefinition([]byte(`---
id: rsssite
name: RSS Site
type: public
links: [https://rss.fixture.test/]
caps:
  categorymappings:
` + cats + `
  modes:
    search: [q]
search:
  paths:
` + paths + `
  rows:
    selector: results
  fields:
    title:
      selector: name
    infohash:
      selector: hash
    category:
      selector: cat
`))
	if err != nil {
		t.Fatal(err)
	}
	return def
}

const rssMovieCats = `    - {id: 1, cat: Movies, desc: "Movies"}
    - {id: 9, cat: Audio, desc: "Music"}`

// The hand-made definitions all read their latest releases without keywords; a definition that cannot is named with
// the reason, and the catalog summary carries it.
func TestRSSProblem(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"fixturepublic", "fixturejson", "fixtureprivate", "fixturecookie"} {
		def, err := ParseDefinition(fixtureDef(t, name))
		if err != nil {
			t.Fatal(err)
		}
		if p := RSSProblem(def); p != "" {
			t.Errorf("%s: %s", name, p)
		}
		if s := Summarize(def); !s.RSS || s.RSSProblem != "" {
			t.Errorf("%s summary: rss %v %q", name, s.RSS, s.RSSProblem)
		}
	}
	jsonPath := "    - path: \"{{ if .Keywords }}q/{{ .Keywords }}{{ else }}recent{{ end }}\"\n      response:\n        type: json"
	cases := []struct {
		name, cats, paths, want string
	}{
		{"runs", rssMovieCats, jsonPath, ""},
		{"music only", `    - {id: 9, cat: Audio, desc: "Music"}`, jsonPath, "no movie or TV category"},
		{"music paths only", rssMovieCats,
			"    - path: music\n      categories: [9]\n      response:\n        type: json", "no search path covers movies or TV"},
		{"inverted path category", rssMovieCats,
			"    - path: other\n      categories: [\"!\", 9]\n      response:\n        type: json", ""},
		{"template fails without keywords", rssMovieCats,
			"    - path: \"q/{{ index .Categories 5 }}\"\n      response:\n        type: json", "does not render without keywords"},
	}
	for _, c := range cases {
		def := rssDef(t, c.cats, c.paths)
		got := RSSProblem(def)
		if c.want == "" && got != "" || c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("%s: RSSProblem = %q, want %q", c.name, got, c.want)
		}
		if s := Summarize(def); s.RSS != (got == "") || s.RSSProblem != got {
			t.Errorf("%s summary: rss %v %q", c.name, s.RSS, s.RSSProblem)
		}
	}
	// A definition the engine can't run cannot feed the RSS sync either.
	def := rssDef(t, rssMovieCats, jsonPath)
	def.Search.Rows.Selector = ""
	if len(def.Problems()) == 0 {
		t.Fatal("no problem without a rows selector")
	}
	if RSSProblem(def) == "" {
		t.Error("an unsupported definition reads RSS")
	}
}
