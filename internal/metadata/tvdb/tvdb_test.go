package tvdb

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

const (
	base    = "https://api4.thetvdb.example/v4"
	testKey = "fixture-api-key-SECRET"
	testPIN = "fixture-pin-SECRET"
)

// fakeTVDB answers from the hand-written fixtures in testdata by path. No network.
type fakeTVDB struct {
	t  *testing.T
	mu sync.Mutex
	// down makes every request fail at the transport.
	down bool
	// status, if set for a path, replaces the fixture's answer.
	status map[string]int
	// revoke makes the next authorized request answer 401 (a revoked token).
	revoke   bool
	tokens   int
	reqs     []string
	auth     []string
	targets  []gateway.Target
	loginReq []string
	fixtures map[string]string // "path?query" or "path" -> file
}

func newFake(t *testing.T) *fakeTVDB {
	return &fakeTVDB{t: t, status: map[string]int{}, fixtures: map[string]string{
		"/series/900001/extended":                    "series_extended.json",
		"/series/900001/episodes/default?page=0":     "episodes_default_0.json",
		"/series/900001/episodes/default?page=1":     "episodes_default_1.json",
		"/series/900001/episodes/default/eng?page=0": "episodes_default_eng.json",
		"/series/900001/episodes/absolute?page=0":    "episodes_absolute.json",
		"/episodes/7000/extended":                    "episode_7000_extended.json",
		"/search":                                    "search.json",
		"/search/remoteid/tt0000001":                 "remoteid.json",
		"/seasons/types":                             "seasontypes.json",
		"/updates?type=series":                       "updates_series.json",
		"/updates?type=episodes":                     "updates_episodes.json",
		"/updates":                                   "updates_empty.json",
	}}
}

func (f *fakeTVDB) Do(_ context.Context, tg gateway.Target, r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets = append(f.targets, tg)
	if f.down {
		return nil, errors.New("connection refused")
	}
	path := strings.TrimPrefix(r.URL.Path, "/v4")
	resp := func(code int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	if path == "/login" {
		b, _ := io.ReadAll(r.Body)
		f.loginReq = append(f.loginReq, string(b))
		var in struct{ APIKey, PIN string }
		_ = json.Unmarshal(b, &in)
		if in.APIKey != testKey {
			return resp(401, `{"status":"failure","message":"Unauthorized"}`)
		}
		f.tokens++
		return resp(200, `{"status":"success","data":{"token":"tok-`+string(rune('0'+f.tokens))+`"}}`)
	}
	f.reqs = append(f.reqs, r.URL.RequestURI())
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	if f.revoke {
		f.revoke = false
		return resp(401, `{"status":"failure","message":"Unauthorized"}`)
	}
	if want := "Bearer tok-" + string(rune('0'+f.tokens)); r.Header.Get("Authorization") != want {
		return resp(401, `{"status":"failure"}`)
	}
	if code := f.status[path]; code != 0 {
		return resp(code, `{"status":"failure","message":"x"}`)
	}
	q := r.URL.Query()
	keys := []string{path + "?page=" + q.Get("page"), path + "?type=" + q.Get("type"), path}
	for _, k := range keys {
		if file, ok := f.fixtures[k]; ok {
			if !filepath.IsAbs(file) {
				file = filepath.Join("testdata", file)
			}
			b, err := os.ReadFile(file)
			if err != nil {
				f.t.Fatal(err)
			}
			return resp(200, string(b))
		}
	}
	return resp(404, `{"status":"failure","message":"NotFoundException"}`)
}

func (f *fakeTVDB) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newClient(t *testing.T, f *fakeTVDB) (*Client, *store.Store, *clock) {
	t.Helper()
	st, err := store.Open(context.Background(), storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	ck := &clock{time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	c, err := New(Options{BaseURL: base, APIKey: testKey, PIN: testPIN, Doer: f, Store: st, Now: ck.now,
		Sleep: func(_ context.Context, d time.Duration) error { ck.t = ck.t.Add(d); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	return c, st, ck
}

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func num(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}

func TestShow(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	c, st, _ := newClient(t, f)
	sh, err := c.Show(ctx, 900001)
	if err != nil {
		t.Fatal(err)
	}
	if str(sh.Title) != "Test Anime" || sh.TVDBID != 900001 || !sh.HasEpisodes || sh.AniListIDs != nil {
		t.Fatalf("series: %+v title %q", sh, str(sh.Title))
	}
	type row struct {
		s, e                  int
		title, air            string
		abs, after, bs, be, r int
		overview              string
	}
	want := []row{
		{0, 1, "Recap Special", "2020-12-20", -1, -1, 2, 1, 48, "A recap of season 1."},
		{0, 2, "Bonus", "2021-04-01", -1, 2, -1, -1, 25, "<nil>"},
		{1, 1, "The Beginning", "2020-01-05", 1, -1, -1, -1, 24, "It starts."},
		{1, 2, "The Middle", "2020-01-12", 2, -1, -1, -1, 24, "<nil>"},
		{2, 1, "第3話", "2021-01-10", 3, -1, -1, -1, 24, "<nil>"},
		{2, 2, "The End", "<nil>", 4, -1, -1, -1, -1, "<nil>"},
	}
	if len(sh.Episodes) != len(want) {
		t.Fatalf("%d episodes, want %d", len(sh.Episodes), len(want))
	}
	for i, w := range want {
		e := sh.Episodes[i]
		got := row{e.SeasonNumber, e.EpisodeNumber, str(e.Title), str(e.AirDate), num(e.AbsoluteEpisodeNumber),
			num(e.AiredAfterSeasonNumber), num(e.AiredBeforeSeasonNumber), num(e.AiredBeforeEpisodeNumber), num(e.Runtime),
			str(e.Overview)}
		if got != w {
			t.Errorf("episode %d: got %+v, want %+v", i, got, w)
		}
	}
	// requests: extended, default p0+p1, eng, absolute, the one special without airs fields
	if n := f.count(); n != 6 {
		t.Errorf("%d requests: %v", n, f.reqs)
	}
	for i, a := range f.auth {
		if a != "Bearer tok-1" {
			t.Errorf("request %d: Authorization %q", i, a)
		}
	}
	for _, tg := range f.targets {
		if tg.Class != gateway.ClassMetadata || tg.Key != Source {
			t.Errorf("target %+v", tg)
		}
	}
	if c.Logins() != 1 || len(f.loginReq) != 1 || !strings.Contains(f.loginReq[0], `"pin":"`+testPIN+`"`) {
		t.Errorf("logins %d %v", c.Logins(), len(f.loginReq))
	}
	// cached: a second build sends nothing
	if _, err := c.Show(ctx, 900001); err != nil || f.count() != 6 {
		t.Fatalf("second show: %v, %d requests", err, f.count())
	}
	// nothing secret in the cache, and the ended series is cached for a month
	rows, err := st.Reader().QueryContext(ctx, `SELECT key, expires_at, fetched_at FROM metadata_cache WHERE source = 'tvdb'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var key string
		var exp, fetched int64
		if err := rows.Scan(&key, &exp, &fetched); err != nil {
			t.Fatal(err)
		}
		n++
		for _, s := range []string{testKey, testPIN, "tok-"} {
			if strings.Contains(key, s) {
				t.Errorf("secret in cache key %q", key)
			}
		}
		if d := time.Duration(exp-fetched) * time.Millisecond; (d - TTLEnded).Abs() > time.Second { // pacing shifts fetched_at
			t.Errorf("%s: ttl %s", key, d)
		}
	}
	if n != 6 {
		t.Errorf("%d cache rows", n)
	}
}

func TestTTLAiring(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	b, _ := os.ReadFile("testdata/series_extended.json")
	dir := t.TempDir()
	airing := strings.Replace(string(b), `"name": "Ended"`, `"name": "Continuing"`, 1)
	if err := os.WriteFile(filepath.Join(dir, "x.json"), []byte(airing), 0o644); err != nil {
		t.Fatal(err)
	}
	f.fixtures["/series/900001/extended"] = filepath.Join(dir, "x.json")
	c, st, _ := newClient(t, f)
	s, err := c.Series(ctx, 900001)
	if err != nil || !s.Airing() {
		t.Fatalf("%v %+v", err, s)
	}
	var exp, fetched int64
	if err := st.Reader().QueryRow(`SELECT expires_at, fetched_at FROM metadata_cache WHERE source = 'tvdb'`).Scan(&exp, &fetched); err != nil {
		t.Fatal(err)
	}
	if d := time.Duration(exp-fetched) * time.Millisecond; (d - TTLAiring).Abs() > time.Second {
		t.Errorf("ttl %s", d)
	}
}

func TestTokenRenewedOn401(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	c, _, _ := newClient(t, f)
	if _, err := c.SeasonTypes(ctx); err != nil {
		t.Fatal(err)
	}
	f.revoke = true
	got, err := c.Search(ctx, "test anime", "series")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TVDBID != "900001" || got[0].Translations["eng"] != "Test Anime" {
		t.Errorf("search %+v", got)
	}
	if c.Logins() != 2 || f.auth[len(f.auth)-1] != "Bearer tok-2" {
		t.Errorf("logins %d, auth %v", c.Logins(), f.auth)
	}
	if !strings.Contains(f.reqs[len(f.reqs)-1], "query=test+anime") || !strings.Contains(f.reqs[len(f.reqs)-1], "type=series") {
		t.Errorf("search request %q", f.reqs[len(f.reqs)-1])
	}
}

func TestTokenExpires(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	c, _, ck := newClient(t, f)
	if _, err := c.SeasonTypes(ctx); err != nil {
		t.Fatal(err)
	}
	ck.t = ck.t.Add(TokenLifetime + time.Hour)
	if _, err := c.Search(ctx, "x", ""); err != nil {
		t.Fatal(err)
	}
	if c.Logins() != 2 {
		t.Errorf("logins %d", c.Logins())
	}
}

func TestLoginRefused(t *testing.T) {
	f := newFake(t)
	st, _ := store.Open(context.Background(), storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	defer st.Close()
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, err := New(Options{BaseURL: base, APIKey: "wrong-key-SECRET", Doer: f, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Series(context.Background(), 900001)
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("key in error: %v", err)
	}
	if !strings.Contains(f.loginReq[0], `"apikey":"wrong-key-SECRET"`) || strings.Contains(f.loginReq[0], "pin") {
		t.Errorf("login body %s", f.loginReq[0])
	}
}

func TestNotFoundStaleAndBreaker(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	c, _, ck := newClient(t, f)
	if _, err := c.Show(ctx, 424242); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown series: %v", err)
	}
	if _, err := c.Show(ctx, 900001); err != nil {
		t.Fatal(err)
	}
	// a month later TVDB is down: stale answers are served, and the breaker opens after three failures
	ck.t = ck.t.Add(TTLEnded + time.Hour)
	f.down = true
	sh, err := c.Show(ctx, 900001)
	if err != nil || len(sh.Episodes) != 6 {
		t.Fatalf("stale show: %v", err)
	}
	if !c.F.BreakerOpen(Source) || c.F.Stale == 0 {
		t.Errorf("breaker %v stale %d", c.F.BreakerOpen(Source), c.F.Stale)
	}
	// nothing cached: the call fails, and the chain asks the next backend
	if _, err := c.Show(ctx, 900002); !errors.Is(err, ErrSourceDown) {
		t.Errorf("uncached while down: %v", err)
	}
	next := &anime.Show{TVDBID: 900002}
	got, err := anime.TVDBChain{c, fixedTVDB{next}}.Show(ctx, 900002)
	if err != nil || got != next {
		t.Errorf("chain: %v %v", got, err)
	}
}

type fixedTVDB struct{ s *anime.Show }

func (f fixedTVDB) Show(context.Context, int) (*anime.Show, error) { return f.s, nil }

func TestRemoteIDAndSeasons(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	c, _, _ := newClient(t, f)
	r, err := c.SearchRemoteID(ctx, "tt0000001")
	if err != nil || len(r) != 1 || r[0].Series == nil || r[0].Series.ID != 900001 {
		t.Fatalf("remote id: %v %+v", err, r)
	}
	if _, err := c.SearchRemoteID(ctx, "../x"); err == nil {
		t.Error("bad remote id accepted")
	}
	ts, err := c.SeasonTypes(ctx)
	if err != nil || len(ts) != 4 || ts[2].Type != "absolute" {
		t.Errorf("season types %v %+v", err, ts)
	}
	s, err := c.Series(ctx, 900001)
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasSeasonType("absolute") || s.HasSeasonType("dvd") || s.Airing() || len(s.RemoteIDs) != 2 || *s.AverageRuntime != 24 {
		t.Errorf("series %+v", s)
	}
	if _, _, err := c.Episodes(ctx, 900001, "Absolute;", ""); err == nil {
		t.Error("bad season type accepted")
	}
	_, eps, err := c.Episodes(ctx, 900001, "absolute", "")
	if err != nil || len(eps) != 4 || eps[3].Number != 4 {
		t.Errorf("absolute order: %v %d", err, len(eps))
	}
}

func TestRefresh(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	c, st, ck := newClient(t, f)
	first, err := c.Refresh(ctx)
	if err != nil || !first.First {
		t.Fatalf("first refresh: %v %+v", err, first)
	}
	if f.count() != 0 {
		t.Errorf("first refresh sent %d requests", f.count())
	}
	if _, err := c.Show(ctx, 900001); err != nil {
		t.Fatal(err)
	}
	// another series cached, not in the updates
	if _, err := st.Writer().Exec(`INSERT INTO metadata_cache (source, key, status, body, fetched_at, expires_at)
		VALUES ('tvdb', ?, 200, x'', 0, 9999999999999)`, "GET "+base+"/series/9000011/extended?meta=translations&short=true"); err != nil {
		t.Fatal(err)
	}
	ck.t = ck.t.Add(time.Hour)
	before := f.count()
	res, err := c.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.First || res.Records != 3 || res.Series != 2 || res.Episodes != 1 || res.Truncated {
		t.Errorf("refresh %+v", res)
	}
	// six series answers (extended, two default pages, eng, absolute) + the special's extended record
	if res.Invalidated != 6 {
		t.Errorf("invalidated %d", res.Invalidated)
	}
	if f.count()-before != len(updateTypes) {
		t.Errorf("%d update requests", f.count()-before)
	}
	if !strings.Contains(f.reqs[before], "since=") {
		t.Errorf("updates request %q", f.reqs[before])
	}
	var fresh, updates int
	_ = st.Reader().QueryRow(`SELECT count(*) FROM metadata_cache WHERE source = 'tvdb' AND expires_at > 0`).Scan(&fresh)
	_ = st.Reader().QueryRow(`SELECT count(*) FROM metadata_cache WHERE source = 'tvdb' AND key LIKE '%/updates?%'`).Scan(&updates)
	if fresh != 1 || updates != 0 {
		t.Errorf("fresh %d, updates rows %d", fresh, updates)
	}
	var since int64
	_ = st.Reader().QueryRow(`SELECT since_at FROM tvdb_sync`).Scan(&since)
	if store.FromMillis(since) != ck.t.Truncate(time.Second) {
		t.Errorf("watermark %v", store.FromMillis(since))
	}
	// the stale answers are revalidated on next use
	before = f.count()
	if _, err := c.Show(ctx, 900001); err != nil {
		t.Fatal(err)
	}
	if f.count()-before != 6 {
		t.Errorf("revalidation sent %d", f.count()-before)
	}
	// a long gap is clamped and marked truncated
	ck.t = ck.t.Add(30 * 24 * time.Hour)
	res, err = c.Refresh(ctx)
	if err != nil || !res.Truncated || res.Until.Sub(res.Since) != MaxUpdatesWindow {
		t.Errorf("long gap: %v %+v", err, res)
	}
	// a failure keeps the watermark
	f.status["/updates"] = 500
	ck.t = ck.t.Add(time.Hour)
	if _, err := c.Refresh(ctx); err == nil {
		t.Error("failed refresh reported success")
	}
	var after int64
	_ = st.Reader().QueryRow(`SELECT since_at FROM tvdb_sync`).Scan(&after)
	if after == store.Millis(ck.t.Truncate(time.Second)) {
		t.Error("watermark moved on failure")
	}
}

func TestNewValidates(t *testing.T) {
	f := newFake(t)
	for _, o := range []Options{{BaseURL: "ftp://x", APIKey: "k", Doer: f}, {BaseURL: base, Doer: f}, {BaseURL: base, APIKey: "k"}} {
		if _, err := New(o); err == nil {
			t.Errorf("accepted %+v", o)
		}
	}
}

func TestSeriesEpisodes(t *testing.T) {
	f := newFake(t)
	c, _, _ := newClient(t, f)
	s, eps, err := c.SeriesEpisodes(context.Background(), 900001, false)
	if err != nil || s.ID != 900001 || len(eps) != 6 {
		t.Fatalf("%+v %d %v", s, len(eps), err)
	}
	// Ids kept, English names, absolute numbers from the absolute order, no extended fetch for specials.
	if eps[2].ID == 0 || str(eps[2].Name) != "The Beginning" || num(eps[2].AbsoluteNumber) != 1 || eps[0].AbsoluteNumber != nil {
		t.Errorf("episodes %+v %+v", eps[2], eps[0])
	}
	if n := f.count(); n != 5 { // extended, default p0+p1, eng, absolute
		t.Errorf("%d requests: %v", n, f.reqs)
	}
}
