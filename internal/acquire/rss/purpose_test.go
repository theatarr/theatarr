package rss

import (
	"testing"

	"github.com/theatarr/theatarr/internal/gateway"
)

// The RSS sync's indexer requests count as rss, through the real gateway (docs/design/gateway.md).
func TestRequestPurposes(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	e.seed(t)
	e.prow.setRSS(item{title: wonkaRemux, hash: hWonkaRemux, size: 60e9, pub: t0})
	if _, err := e.r.RSS(e.ctx); err != nil {
		t.Fatal(err)
	}
	if len(e.gw.State().Requests) == 0 {
		t.Fatal("the RSS sync sent no counted request")
	}
	for k, r := range e.gw.State().Requests {
		if len(r.Today) != 1 || r.Today[gateway.PurposeRSS] == 0 {
			t.Fatalf("%s after the RSS sync: %v", k, r.Today)
		}
	}
}
