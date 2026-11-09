package anime

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Overrides are one franchise's curated corrections (docs/design/anime.md "Overrides"): overrides/<slug>.yaml in the policy
// directory. JSON is YAML too, so overrides/<slug>.json files load unchanged.
//
//	source: https://myanimelist.net/stacks/53018
//	moves:
//	  - {entry: "al:100049#1", after: "al:21355@S01E11", why: "Memory Snow sits between arcs 2 and 3"}
//	mappings:   # a fixed TVDB mapping for an entry; its status becomes "override"
//	  - {entry: "al:12345", tvdb: 305089, episodes: "S00E05, S01E01-E03", why: "..."}
//	exclude: ["al:999"]     # entries never wanted in the orders
//	alt_cuts:   # the Director's Cut, which no source numbers (routes.go)
//	  - {name: "Director's Cut", tvdb: 305089, entry: "kitsu:11111", map: {DC1: S01E01, DC2: [S01E02, S01E03]}}
//	routes: [al:356, al:19603, al:20791]   # route order (routes.go)
//	line_position: last                    # the rest of the line after the routes
type Overrides struct {
	Source       any            `yaml:"source" json:"source"`
	Moves        []Move         `yaml:"moves" json:"moves"`
	Mappings     []MappingFix   `yaml:"mappings" json:"mappings,omitempty"`
	Exclude      []string       `yaml:"exclude" json:"exclude,omitempty"`
	AltCuts      []AltCut       `yaml:"alt_cuts" json:"alt_cuts,omitempty"`
	Routes       []string       `yaml:"routes" json:"routes,omitempty"`
	LinePosition string         `yaml:"line_position" json:"line_position,omitempty"`
	Extra        map[string]any `yaml:",inline" json:"-"`
	File         string         `yaml:"-" json:"-"`
}

// Move places an entry, or a slice of one ("key@S03E06-S03E09"), after another ("key", "key@SxxEyy", a slice, or
// "start").
type Move struct {
	Entry string `yaml:"entry" json:"entry"`
	After string `yaml:"after" json:"after"`
	Why   string `yaml:"why" json:"why,omitempty"`
}

// pyRepr renders the move as Python prints a dict, the form the golden notes quote.
func (m Move) pyRepr() string {
	s := "{'entry': " + pyRepr(m.Entry) + ", 'after': " + pyRepr(m.After)
	if m.Why != "" {
		s += ", 'why': " + pyRepr(m.Why)
	}
	return s + "}"
}

// MappingFix pins an entry's TVDB mapping.
type MappingFix struct {
	Entry    string `yaml:"entry" json:"entry"`
	TVDB     int    `yaml:"tvdb" json:"tvdb"`
	Episodes string `yaml:"episodes" json:"episodes"` // a ranges() label: "S00E05, S01E01-E03"
	Why      string `yaml:"why" json:"why,omitempty"`
}

// AltCut is an alternate cut's episode map (DC1 -> S01E01, DC2 -> [S01E02, S01E03]) into one TVDB series.
type AltCut struct {
	Name string `yaml:"name" json:"name"`
	// TVDB is the series the map numbers into.
	TVDB int `yaml:"tvdb" json:"tvdb"`
	// Entry is the cut's own entry, when the franchise has one (Kitsu's Director's Cut); optional.
	Entry string `yaml:"entry" json:"entry,omitempty"`
	// Match is a regular expression (case-insensitive) a release title of the cut matches; default: every word of
	// Name.
	Match string         `yaml:"match" json:"match,omitempty"`
	Map   map[string]any `yaml:"map" json:"map"`
}

var keyRE = regexp.MustCompile(`^(al:\d+(#\d+)?|adb:\d+|kitsu:\d+|tvdb:\d+:S\d+E\d+)$`)
var sliceRE = regexp.MustCompile(`^S\d+E\d+(-(S\d+)?E\d+)?$`)

// LoadOverrides reads <dir>/<slug>.yaml, .yml or .json. A missing file is no overrides (nil, nil).
func LoadOverrides(dir, slug string) (*Overrides, error) {
	if dir == "" {
		return nil, nil
	}
	for _, ext := range []string{".yaml", ".yml", ".json"} {
		p := filepath.Join(dir, slug+ext)
		b, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		ov, err := ParseOverrides(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		ov.File = p
		return ov, nil
	}
	return nil, nil
}

// ParseOverrides parses and validates one overrides file.
func ParseOverrides(b []byte) (*Overrides, error) {
	var ov Overrides
	if err := yaml.Unmarshal(b, &ov); err != nil {
		return nil, err
	}
	if len(ov.Extra) > 0 {
		var ks []string
		for k := range ov.Extra {
			ks = append(ks, k)
		}
		return nil, fmt.Errorf("unknown keys: %s", strings.Join(ks, ", "))
	}
	var errs []string
	checkRef := func(what, v string, allowStart bool) {
		if allowStart && v == "start" {
			return
		}
		k, sl, has := strings.Cut(v, "@")
		if !keyRE.MatchString(k) || has && !sliceRE.MatchString(sl) {
			errs = append(errs, fmt.Sprintf("%s %q: want al:<id>[#n], adb:<id>, kitsu:<id> or tvdb:<id>:S00Exx, optionally @SxxEyy[-SxxEyy]", what, v))
		}
	}
	for i, m := range ov.Moves {
		checkRef(fmt.Sprintf("moves[%d].entry", i), m.Entry, false)
		checkRef(fmt.Sprintf("moves[%d].after", i), m.After, true)
	}
	for i, m := range ov.Mappings {
		checkRef(fmt.Sprintf("mappings[%d].entry", i), m.Entry, false)
		if m.TVDB <= 0 {
			errs = append(errs, fmt.Sprintf("mappings[%d].tvdb: want a TVDB series id", i))
		}
		if _, err := parseRangeLabel(m.Episodes); err != nil {
			errs = append(errs, fmt.Sprintf("mappings[%d].episodes: %v", i, err))
		}
	}
	for i, k := range ov.Exclude {
		checkRef(fmt.Sprintf("exclude[%d]", i), k, false)
	}
	seenRoute := map[string]bool{}
	for i, k := range ov.Routes {
		checkRef(fmt.Sprintf("routes[%d]", i), k, false)
		if seenRoute[k] {
			errs = append(errs, fmt.Sprintf("routes[%d]: %s listed twice", i, k))
		}
		seenRoute[k] = true
	}
	if ov.LinePosition != "" && ov.LinePosition != "first" && ov.LinePosition != "last" {
		errs = append(errs, "line_position: want first or last")
	}
	if ov.LinePosition != "" && len(ov.Routes) == 0 {
		errs = append(errs, "line_position: needs routes")
	}
	names := map[string]bool{}
	for i, a := range ov.AltCuts {
		if a.Entry != "" {
			checkRef(fmt.Sprintf("alt_cuts[%d].entry", i), a.Entry, false)
		}
		_, es := a.resolve()
		for _, e := range es {
			errs = append(errs, fmt.Sprintf("alt_cuts[%d].%s", i, e))
		}
		if names[a.Name] {
			errs = append(errs, fmt.Sprintf("alt_cuts[%d].name: %q listed twice", i, a.Name))
		}
		names[a.Name] = true
	}
	if len(errs) > 0 {
		return nil, errors.New(strings.Join(errs, "; "))
	}
	return &ov, nil
}

// parseRangeLabel reads a ranges() label ("S00E05, S01E01-E03") back into episodes.
func parseRangeLabel(s string) ([]SE, error) {
	var out []SE
	for _, seg := range strings.Split(s, ",") {
		seg = strings.TrimSpace(seg)
		m := labelSegRE.FindStringSubmatch(seg)
		if m == nil || m[0] != seg {
			return nil, fmt.Errorf("%q is not SxxEyy or SxxEyy-Ezz", seg)
		}
		var a, b, season int
		fmt.Sscan(m[1], &season)
		fmt.Sscan(m[2], &a)
		b = a
		if m[3] != "" {
			fmt.Sscan(m[3], &b)
		}
		if b < a {
			return nil, fmt.Errorf("%q runs backwards", seg)
		}
		for x := a; x <= b; x++ {
			out = append(out, SE{season, x})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no episodes")
	}
	return out, nil
}
