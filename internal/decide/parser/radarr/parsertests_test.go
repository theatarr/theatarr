package radarr

import (
	"fmt"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/parser/parsertest"
)

// caller answers the extracted cases (testdata/parsertests, written by testdata/extract.py) with this port.
type caller struct{}

func (caller) Call(name, input string, args map[string]any) (any, error) {
	return Call(name, input, args)
}

func (caller) Compare(name string, v any) (any, error) {
	switch name {
	case "cleanMovieTitle":
		s, ok := v.(string)
		if !ok {
			return v, nil
		}
		return CleanMovieTitle(s), nil
	}
	return nil, fmt.Errorf("unknown compare %q", name)
}

// TestParserTests runs every extracted Radarr ParserTests case. A case marked known_failure must
// still fail (so the mark is removed once it passes); every other case must pass. The pass rate
// per fixture and per call is logged (go test -v -run TestParserTests).
func TestParserTests(t *testing.T) {
	r := parsertest.Run(caller{}, parsertest.MustLoad(t, "testdata/parsertests"))
	r.Verify(t)
	t.Log("pass rate:\n" + r.String())
}

// Helpers shared with corpus_test.go.

func lookup(v any, path string) (any, bool) { return parsertest.Lookup(v, path) }

func show(v any) string { return parsertest.Show(v) }

func pct(a, b int) float64 { return parsertest.Pct(a, b) }
