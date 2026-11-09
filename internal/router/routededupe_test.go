package router

import (
	"testing"
	"time"
)

// Two RSS reads of the same release before the worker opens a download queue one route; other episode coverage,
// another candidate or a non-RSS source keeps its own job.
func TestRSSRouteJobsDeduplicated(t *testing.T) {
	t.Parallel()
	at := time.Unix(1000, 0)
	key := func(src string, c Candidate) string {
		req := movieReq(c)
		req.Source = src
		return routeOptions(routePayload{Request: req}, at).DedupeKey
	}
	a := cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")
	if k := key("rss", a); k == "" || k != key("rss", a) {
		t.Fatalf("same RSS request: keys %q %q", k, key("rss", a))
	}
	if key("rss", a) == key("rss", cand(h("b"), "Movie.2024.2160p.WEB.H265-OTHER")) {
		t.Error("another candidate shares the key")
	}
	e1, e2 := a, a
	e1.Episodes, e2.Episodes = []int64{101}, []int64{101, 102}
	if key("rss", e1) == key("rss", e2) {
		t.Error("other episode coverage shares the key")
	}
	if key("ui:grab:1", a) != "" {
		t.Error("a non-RSS new route got a dedupe key")
	}
}
