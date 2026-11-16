package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"sync/atomic"
	"testing"
)

// A JSON import list is fetched from a public address on its own budget; a private address is refused before any
// dial, even one the live allowlist holds, and a redirect is not followed but reported as ErrListRedirect.
func TestListGetPublicOnlyNoRedirect(t *testing.T) {
	e, g, dns := probeEnv(t, nil)
	dns["lists.test"] = []netip.Addr{netip.MustParseAddr("203.0.113.30")}
	dns["lan.test"] = []netip.Addr{netip.MustParseAddr("192.168.1.5")}
	var redirected atomic.Int32
	e.serve("203.0.113.30", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved.json" {
			http.Redirect(w, r, "http://dmm.test/api", http.StatusFound)
			return
		}
		w.Write([]byte(`[{"tmdb_id":1}]`))
	}))
	e.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	ctx := context.Background()
	b, err := g.ListGet(ctx, testPolicy(), "http://lists.test/list.json", 1<<20)
	if err != nil || string(b) != `[{"tmdb_id":1}]` {
		t.Fatalf("list: %q %v", b, err)
	}
	if _, err := g.ListGet(ctx, testPolicy(), "http://lists.test/moved.json", 1<<20); !errors.Is(err, ErrListRedirect) {
		t.Fatalf("redirect: %v", err)
	}
	if redirected.Load() != 0 {
		t.Fatal("the redirect was followed")
	}
	for _, u := range []string{"http://lan.test/list.json", "http://127.0.0.1:9876/list.json", "http://10.0.0.5/list.json"} {
		if _, err := g.ListGet(ctx, testPolicy(), u, 1<<20); refusedReason(err) != ReasonHost {
			t.Fatalf("%s: %v", u, err)
		}
	}
	if e.dialCount("127.0.0.1:9876") != 0 || e.dialCount("192.168.1.5:80") != 0 {
		t.Fatal("a private host was dialed")
	}
	// Too large an answer is refused.
	if _, err := g.ListGet(ctx, testPolicy(), "http://lists.test/list.json", 4); err == nil {
		t.Fatal("an answer over the limit was read")
	}
}
