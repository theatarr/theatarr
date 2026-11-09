package shadow

import (
	"maps"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
)

// The RSS sync's indexer requests count as rss and the shadow comparison's search as shadow, through the real gateway
// (docs/design/gateway.md).
func TestRequestPurposes(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	used := func() map[string]map[gateway.Purpose]int {
		out := map[string]map[gateway.Purpose]int{}
		for k, r := range e.gw.State().Requests {
			out[k] = r.Today
		}
		return out
	}
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.prow.setRSS(item{title: wonkaRemux, hash: hWonkaRemux, size: 60e9, pub: t0})
	if _, err := e.r.RSS(e.ctx); err != nil {
		t.Fatal(err)
	}
	rss := used()
	if len(rss) == 0 {
		t.Fatal("the RSS sync sent no counted request")
	}
	for k, by := range rss {
		if len(by) != 1 || by[gateway.PurposeRSS] == 0 {
			t.Fatalf("%s after the RSS sync: %v", k, by)
		}
	}

	e.prow.set(argylle.imdb, item{title: argWeb, hash: hArgWeb, size: 20e9, pub: t0.Add(-time.Hour)})
	e.zurg.add(grabEvent(2, argylle, t0.Add(-time.Hour), argWeb, hArgWeb, "WEBDL-2160p", 18, 1700, "Search"))
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	after := used()
	if after["prowlarr"][gateway.PurposeShadow] == 0 {
		t.Fatalf("the shadow search was not counted: %v", after)
	}
	for k, by := range after {
		by = maps.Clone(by)
		delete(by, gateway.PurposeShadow)
		if !maps.Equal(by, rss[k]) {
			t.Fatalf("%s after the shadow search: %v (before %v)", k, after[k], rss[k])
		}
	}
}
