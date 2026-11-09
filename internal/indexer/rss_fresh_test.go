package indexer

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// e2eperf 1: an RSS sync reads the feed fresh even within the search cache's TTL; a search is still cached.
func TestRSSIsNeverServedFromTheSearchCache(t *testing.T) {
	n := newNet(t)
	var hits atomic.Int32
	n.serve("prowlarr.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") == "caps" {
			fmt.Fprint(w, `<caps><limits default="100" max="100"/><searching><search available="yes" supportedParams="q"/></searching></caps>`)
			return
		}
		hits.Add(1)
		fmt.Fprint(w, `<?xml version="1.0"?><rss><channel></channel></rss>`)
	}))
	h := newHarness(t, n, testPolicy(), false)
	q := Query{Type: QueryRSS, Categories: []int{2000}, RSSLimit: 100}
	if _, err := h.svc.Search(context.Background(), "prowlarr", q); err != nil {
		t.Fatal(err)
	}
	h.advance(10 * time.Minute)
	b, err := h.svc.Search(context.Background(), "prowlarr", q)
	if err != nil {
		t.Fatal(err)
	}
	if b.Cached || hits.Load() != 2 {
		t.Fatalf("second RSS read cached=%v, upstream reads=%d, want a fresh read", b.Cached, hits.Load())
	}
	s := Query{Type: QueryText, Title: "x", Categories: []int{2000}}
	for range 2 {
		if _, err := h.svc.Search(context.Background(), "prowlarr", s); err != nil {
			t.Fatal(err)
		}
	}
	if hits.Load() != 3 {
		t.Fatalf("a repeated search was not cached: %d upstream reads", hits.Load())
	}
}
