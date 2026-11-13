package sonarr

import (
	"fmt"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/parser/parsertest"
)

// The Sonarr ParserTests cases, extracted to testdata/parsertests by testdata/extract.py. The schema and matcher
// are the shared ones in internal/decide/parser/parsertest.

type testCase = parsertest.Case

// caller answers the cases with this port's v4 base (v4d), the Sonarr v4 parser V4Plus patches.
type caller struct{ d *dialect }

func (c caller) Call(name, input string, args map[string]any) (any, error) {
	return c.d.Call(name, input, args)
}

// Compare applies the case's compare function to one side.
func (c caller) Compare(name string, v any) (any, error) {
	switch name {
	case "cleanSeriesTitle":
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("cleanSeriesTitle: %s is not a string", parsertest.Show(v))
		}
		return c.d.cleanSeriesTitle(s), nil
	}
	return nil, fmt.Errorf("unknown compare %q", name)
}

// TestParserTests runs every extracted case. A failing case must carry known_failure; a known failure that passes
// fails the test too, so the rate stays honest.
func TestParserTests(t *testing.T) {
	t.Parallel()
	r := parsertest.Run(caller{v4d}, parsertest.MustLoad(t, "testdata/parsertests"))
	r.Verify(t)
	t.Log("pass rate:\n" + r.String())
}

func runCase(c testCase) error { return parsertest.RunCase(caller{v4d}, c) }

// Helpers shared with corpus_test.go.

func show(v any) string { return parsertest.Show(v) }

func pct(a, b int) float64 { return parsertest.Pct(a, b) }
