package library

import (
	"testing"

	"github.com/theatarr/theatarr/internal/outcome"
)

// A library made for a new settings generation still reports the outcomes of files that broke while playing.
func TestWithPolicyKeepsPlayingHook(t *testing.T) {
	e := newEnv(t, true)
	var got []string
	e.lib.OnPlayingOutcome(func(o outcome.Outcome) { got = append(got, o.Kind) })
	next := e.lib.WithPolicy(e.pol, e.lib.engine, nil, nil)
	if next.onPlaying == nil {
		t.Fatal("the new generation has no outcome hook")
	}
	next.onPlaying(outcome.Outcome{Kind: outcome.Restored})
	if len(got) != 1 {
		t.Errorf("outcomes %v", got)
	}
}
