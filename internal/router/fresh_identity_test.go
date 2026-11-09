package router

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
)

// A producer's new route carries the identity validation version and its origin: in the queued payload, on the
// download row it opens, and back on the request rebuilt from that row. The caller's candidates are not changed.
func TestFreshRouteCarriesIdentityAndOrigin(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	q := jobs.NewQueue(e.st)
	q.SetClock(func() time.Time { return e.clock })
	w := jobs.NewWorker(q, jobs.WorkerOptions{})
	e.r.RegisterJobs(w, q)
	req := grabReq(cand(h("a"), "Movie.2024.2160p.BluRay-GRP"))
	id, err := e.r.Enqueue(e.ctx, req, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if c := req.Candidates[0]; c.Identity != 0 || c.Origin != "" {
		t.Fatalf("enqueue changed the caller's candidate: %+v", c)
	}
	var ident int
	var origin string
	if err := e.st.Reader().QueryRow(`SELECT json_extract(payload, '$.candidates[0].identity'),
		json_extract(payload, '$.candidates[0].origin') FROM job WHERE id = ?`, id).Scan(&ident, &origin); err != nil {
		t.Fatal(err)
	}
	if ident != RouteIdentityVersion || origin != OriginRSS {
		t.Fatalf("queued payload identity=%d origin=%q, want %d %q", ident, origin, RouteIdentityVersion, OriginRSS)
	}
	if ok, err := w.RunOne(e.ctx); err != nil || !ok {
		t.Fatalf("worker: %v %v", ok, err)
	}
	var dl int64
	if err := e.st.Reader().QueryRow(`SELECT id, json_extract(release_data, '$.route_identity'),
		json_extract(release_data, '$.route_origin') FROM download`).Scan(&dl, &ident, &origin); err != nil {
		t.Fatal(err)
	}
	if ident != RouteIdentityVersion || origin != OriginRSS {
		t.Fatalf("download row identity=%d origin=%q", ident, origin)
	}
	back, err := e.r.requestOf(e.ctx, dl)
	if err != nil || back == nil || len(back.Candidates) != 1 {
		t.Fatalf("requestOf: %+v %v", back, err)
	}
	if c := back.Candidates[0]; c.Identity != RouteIdentityVersion || c.Origin != OriginRSS {
		t.Fatalf("rebuilt candidate identity=%d origin=%q", c.Identity, c.Origin)
	}
}

// Origin follows the producer: a wanted movie search is a search, a feed pick is RSS, a series offer is a search.
// A resumed request (a download or hold behind it) keeps what it carried: an older build's candidate stays unstamped.
func TestStampFreshOrigin(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	for _, tc := range []struct {
		version, source string
		search          bool
		want            string
	}{
		{"movie", "wanted", false, OriginSearch},
		{"movie", "shadow.rss", false, OriginRSS},
		{"movie", "offer", false, OriginRSS},
		{"series", "offer", false, OriginSearch},
		{"series", "shadow.rss", true, OriginSearch},
		{"movie", "ui:grab:x", false, OriginRSS},
	} {
		req := Request{Version: tc.version, Source: tc.source, Candidates: []Candidate{{Search: tc.search}}}
		e.r.stampFresh(&req)
		if c := req.Candidates[0]; c.Identity != RouteIdentityVersion || c.Origin != tc.want {
			t.Fatalf("%s/%s: identity=%d origin=%q, want %q", tc.version, tc.source, c.Identity, c.Origin, tc.want)
		}
	}
	for _, req := range []Request{{Version: "movie", Source: "job", DownloadID: 3, Candidates: []Candidate{{}}},
		{Version: "movie", Source: "upgrade:4", Hold: 4, Candidates: []Candidate{{}}}} {
		e.r.stampFresh(&req)
		if c := req.Candidates[0]; c.Identity != 0 || c.Origin != "" {
			t.Fatalf("resumed request stamped: %+v", c)
		}
	}
}
