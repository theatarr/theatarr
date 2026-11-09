package live

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// fakeDoer answers requests from a function and records them. No network.
type fakeDoer struct {
	reqs    []*http.Request
	bodies  []string
	targets []gateway.Target
	answer  func(r *http.Request) (int, http.Header, string, error)
}

func (f *fakeDoer) Do(_ context.Context, t gateway.Target, r *http.Request) (*http.Response, error) {
	f.reqs = append(f.reqs, r)
	f.targets = append(f.targets, t)
	var b string
	if r.Body != nil {
		x, _ := io.ReadAll(r.Body)
		b = string(x)
		r.Body = io.NopCloser(strings.NewReader(b))
	}
	f.bodies = append(f.bodies, b)
	code, h, body, err := f.answer(r)
	if err != nil {
		return nil, err
	}
	if h == nil {
		h = http.Header{}
	}
	return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader(body))}, nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newFetcher(t *testing.T, d Doer) (*Fetcher, *clock, *[]time.Duration) {
	t.Helper()
	st, err := store.Open(context.Background(), storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := &clock{time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	var slept []time.Duration
	f := &Fetcher{Doer: d, Store: st, Now: c.now, Sleep: func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		c.t = c.t.Add(d)
		return nil
	}}
	return f, c, &slept
}

func TestFetcherCacheConditionalStaleBreaker(t *testing.T) {
	ctx := context.Background()
	status := 200
	d := &fakeDoer{answer: func(r *http.Request) (int, http.Header, string, error) {
		if status == 200 && r.Header.Get("If-None-Match") == `"v1"` {
			return 304, nil, "", nil
		}
		if status == 0 {
			return 0, nil, "", errors.New("connection refused")
		}
		return status, http.Header{"Etag": {`"v1"`}, "Last-Modified": {"Sun, 25 Oct 2026 00:00:00 GMT"}}, `{"a":1}`, nil
	}}
	f, c, _ := newFetcher(t, d)
	req := Request{Source: SourceXEM, URL: "https://thexem.info/map/all?id=1&origin=tvdb"}
	for i := 0; i < 2; i++ {
		b, err := f.Get(ctx, req)
		if err != nil || string(b) != `{"a":1}` {
			t.Fatalf("get %d: %q %v", i, b, err)
		}
	}
	if len(d.reqs) != 1 || f.Hits != 1 {
		t.Fatalf("requests %d, hits %d", len(d.reqs), f.Hits)
	}
	if d.targets[0] != (gateway.Target{Class: gateway.ClassMetadata, Key: SourceXEM}) {
		t.Errorf("target %+v", d.targets[0])
	}
	// expired: a conditional request, 304 extends the entry
	c.t = c.t.Add(8 * 24 * time.Hour)
	if b, err := f.Get(ctx, req); err != nil || string(b) != `{"a":1}` {
		t.Fatalf("after 304: %q %v", b, err)
	}
	if len(d.reqs) != 2 || d.reqs[1].Header.Get("If-None-Match") != `"v1"` || d.reqs[1].Header.Get("If-Modified-Since") == "" {
		t.Fatalf("conditional request: %v", d.reqs[1].Header)
	}
	if _, err := f.Get(ctx, req); err != nil || len(d.reqs) != 2 {
		t.Fatalf("304 did not refresh the TTL: %d requests", len(d.reqs))
	}
	// the source fails: stale answers, and the breaker opens after three failures
	c.t = c.t.Add(8 * 24 * time.Hour)
	status = 503
	for i := 0; i < 3; i++ {
		if b, err := f.Get(ctx, req); err != nil || string(b) != `{"a":1}` {
			t.Fatalf("stale %d: %q %v", i, b, err)
		}
	}
	if !f.BreakerOpen(SourceXEM) || f.Stale != 3 {
		t.Fatalf("breaker open %v, stale %d", f.BreakerOpen(SourceXEM), f.Stale)
	}
	n := len(d.reqs)
	if b, err := f.Get(ctx, req); err != nil || string(b) != `{"a":1}` || len(d.reqs) != n {
		t.Fatalf("open breaker still sent a request (%d -> %d) or failed: %v", n, len(d.reqs), err)
	}
	// nothing cached and the breaker open: ErrSourceDown
	if _, err := f.Get(ctx, Request{Source: SourceXEM, URL: "https://thexem.info/map/all?id=2&origin=tvdb"}); !errors.Is(err, ErrSourceDown) {
		t.Errorf("uncached with open breaker: %v", err)
	}
	// it closes after its time and a success resets it
	c.t = c.t.Add(BreakerOpen + time.Second)
	status = 200
	if _, err := f.Get(ctx, Request{Source: SourceXEM, URL: "https://thexem.info/map/all?id=2&origin=tvdb"}); err != nil || f.BreakerOpen(SourceXEM) {
		t.Errorf("after the breaker: %v", err)
	}
}

func TestFetcherNotFoundAndCheck(t *testing.T) {
	ctx := context.Background()
	d := &fakeDoer{answer: func(r *http.Request) (int, http.Header, string, error) {
		if strings.Contains(r.URL.Path, "missing") {
			return 404, nil, "", nil
		}
		return 200, nil, `{"errors":[{"message":"bad"}]}`, nil
	}}
	f, _, _ := newFetcher(t, d)
	for i := 0; i < 2; i++ {
		if _, err := f.Get(ctx, Request{Source: SourceKitsu, URL: "https://kitsu.app/missing"}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("404: %v", err)
		}
	}
	if len(d.reqs) != 1 {
		t.Errorf("404 not cached: %d requests", len(d.reqs))
	}
	bad := Request{Source: SourceAniList, Method: http.MethodPost, URL: "https://graphql.anilist.co", Body: []byte(`{}`),
		Check: func([]byte) error { return errors.New("graphql error") }}
	if _, err := f.Get(ctx, bad); err == nil {
		t.Fatal("check did not reject")
	}
	if _, err := f.Get(ctx, bad); err == nil || len(d.reqs) != 3 {
		t.Errorf("a rejected answer was cached (%d requests)", len(d.reqs))
	}
}

func TestAniListPacedAndBatched(t *testing.T) {
	ctx := context.Background()
	d := &fakeDoer{answer: func(r *http.Request) (int, http.Header, string, error) {
		var body struct {
			Query     string `json:"query"`
			Variables struct {
				IDs []int `json:"ids"`
			} `json:"variables"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		var media []map[string]any
		for _, id := range body.Variables.IDs {
			st := "FINISHED"
			if id == 7 {
				st = "RELEASING"
			}
			media = append(media, map[string]any{"id": id, "type": "ANIME", "status": st, "title": map[string]any{"romaji": "x"}})
		}
		out, _ := json.Marshal(map[string]any{"data": map[string]any{"Page": map[string]any{"media": media}}})
		return 200, nil, string(out), nil
	}}
	// the fake reads the body itself, so give it a copy through a wrapper
	f, c, slept := newFetcher(t, doerFunc(func(ctx context.Context, tg gateway.Target, r *http.Request) (*http.Response, error) {
		if r.Header.Get("Content-Type") != "application/json" || r.Method != http.MethodPost {
			t.Errorf("request %s %v", r.Method, r.Header)
		}
		return d.Do(ctx, tg, r)
	}))
	ids := make([]int, 0, 60)
	for i := 60; i >= 1; i-- {
		ids = append(ids, i)
	}
	ms, err := AniList{F: f}.Media(ctx, ids)
	if err != nil || len(ms) != 60 {
		t.Fatalf("media %d %v", len(ms), err)
	}
	if len(d.reqs) != 2 {
		t.Fatalf("batches: %d requests", len(d.reqs))
	}
	if !strings.Contains(d.bodies[0], `"ids":[1,2,3`) || !strings.Contains(d.bodies[1], `"ids":[51,`) {
		t.Errorf("batch bodies %q", d.bodies)
	}
	if len(*slept) != 1 || (*slept)[0] != 2100*time.Millisecond {
		t.Errorf("pacing %v", *slept)
	}
	// the first batch has a releasing title: a day; the second only finished ones: a month
	c.t = c.t.Add(2 * 24 * time.Hour)
	if _, err := (AniList{F: f}).Media(ctx, ids); err != nil {
		t.Fatal(err)
	}
	if len(d.reqs) != 3 {
		t.Errorf("TTL by status: %d requests, want 3", len(d.reqs))
	}
}

type doerFunc func(context.Context, gateway.Target, *http.Request) (*http.Response, error)

func (f doerFunc) Do(ctx context.Context, t gateway.Target, r *http.Request) (*http.Response, error) {
	return f(ctx, t, r)
}

func TestBatches(t *testing.T) {
	b := batches([]int{3, 1, 3, 2})
	if len(b) != 1 || len(b[0]) != 3 || b[0][0] != 1 {
		t.Errorf("%v", b)
	}
}
