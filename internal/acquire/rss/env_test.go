package rss

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// The test network: .test host names map to local httptest servers through the gateway's DialContext, so every
// indexer request passes a real gateway (VPN guard, allowlist, budgets) and nothing leaves the machine.

const vpnIP = "45.0.0.1"

type testNet struct {
	t     *testing.T
	mu    sync.Mutex
	hosts map[string]string
	reqs  []string
}

func newNet(t *testing.T) *testNet {
	n := &testNet{t: t, hosts: map[string]string{}}
	n.serve("echo.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, vpnIP) }))
	return n
}

func (n *testNet) serve(name string, h http.Handler) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.mu.Lock()
		q := r.URL.Query()
		q.Del("apikey")
		n.reqs = append(n.reqs, name+r.URL.Path+"?"+q.Encode())
		n.mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	n.t.Cleanup(srv.Close)
	n.mu.Lock()
	n.hosts[name+":80"] = srv.Listener.Addr().String()
	n.mu.Unlock()
}

func (n *testNet) requests(prefix string) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for _, r := range n.reqs {
		if strings.HasPrefix(r, prefix) {
			out = append(out, r)
		}
	}
	return out
}

func (n *testNet) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	n.mu.Lock()
	real, ok := n.hosts[addr]
	n.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("dial %s: no such host in the test network", addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, real)
}

// item is one torznab result.
type item struct {
	title, hash, imdb string
	size              int64
	pub               time.Time
	tv                bool // category 5000
	private           bool // the item's own privacy (Prowlarr's prowlarrindexer type) is private
}

// feed serves a Prowlarr-shaped torznab indexer: caps, movie searches by IMDb id, and RSS.
type feed struct {
	mu     sync.Mutex
	movies map[string][]item // by IMDb digits
	shows  map[string][]item // by "tvdbid:season"
	rss    []item
	dmm    bool
	// rssPath, when set, serves the RSS by request path instead (DMM's per-provider lists).
	rssPath map[string][]item
	// rssFail answers 500 to the RSS on these paths.
	rssFail map[string]bool
}

func (f *feed) setRSS(items ...item) {
	f.mu.Lock()
	f.rss = items
	f.mu.Unlock()
}

const capsXML = `<?xml version="1.0" encoding="UTF-8"?>
<caps><limits default="100" max="100" /><searching><search available="yes" supportedParams="q" />
<movie-search available="yes" supportedParams="q,imdbid,tmdbid" />
<tv-search available="yes" supportedParams="q,tvdbid,season,ep" /></searching>
<categories><category id="2000" name="Movies" /><category id="5000" name="TV" /></categories></caps>`

func (f *feed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	defer f.mu.Unlock()
	var items []item
	switch q.Get("t") {
	case "caps":
		fmt.Fprint(w, capsXML)
		return
	case "movie":
		items = f.movies[strings.TrimPrefix(q.Get("imdbid"), "tt")]
	case "tvsearch":
		items = f.shows[q.Get("tvdbid")+":"+q.Get("season")]
	case "search":
		items = f.rss
		if f.rssPath != nil {
			items = f.rssPath[r.URL.Path]
		}
		if f.rssFail[r.URL.Path] {
			http.Error(w, "broken", http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "bad t", http.StatusBadRequest)
		return
	}
	if q.Get("t") == "search" {
		offset, _ := strconv.Atoi(q.Get("offset"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		if offset > len(items) {
			offset = len(items)
		}
		items = items[offset:]
		if limit > 0 && limit < len(items) {
			items = items[:limit]
		}
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><rss version="1.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>`)
	if f.dmm {
		fmt.Fprintf(&b, `<torznab:response offset="0" total="%d" />`, len(items))
	}
	for _, it := range items {
		typ := "public"
		if it.private {
			typ = "private"
		}
		fmt.Fprintf(&b, `<item><title>%s</title><guid>guid-%s</guid><prowlarrindexer id="1" type="%s">X</prowlarrindexer>`,
			html.EscapeString(it.title), it.hash, typ)
		cat := 2000
		if it.tv {
			cat = 5000
		}
		fmt.Fprintf(&b, `<pubDate>%s</pubDate><size>%d</size><category>%d</category>`, it.pub.Format(time.RFC1123Z), it.size, cat)
		fmt.Fprintf(&b, `<torznab:attr name="seeders" value="20" /><torznab:attr name="peers" value="25" />`)
		fmt.Fprintf(&b, `<torznab:attr name="infohash" value="%s" />`, it.hash)
		if it.imdb != "" {
			fmt.Fprintf(&b, `<torznab:attr name="imdb" value="%s" />`, strings.TrimPrefix(it.imdb, "tt"))
		}
		b.WriteString(`</item>`)
	}
	b.WriteString(`</channel></rss>`)
	w.Header().Set("Content-Type", "application/rss+xml")
	fmt.Fprint(w, b.String())
}

type movieDef struct {
	id         int
	title      string
	year, tmdb int
	imdb       string
}

var (
	dune    = movieDef{1, "Dune: Part Two", 2024, 693134, "tt15239678"}
	argylle = movieDef{2, "Argylle", 2024, 848538, "tt15009428"}
	wonka   = movieDef{3, "Wonka", 2023, 787699, "tt6166392"}
	alien   = movieDef{4, "Alien: Romulus", 2024, 945961, "tt18412256"}
)

// seed writes the films with plain inserts, each wanted in movie-web (version id = film id) and movie-remux (film id
// + 4), out at home since 2024-01-01; Dune's movie-remux copy holds a WEB-DL file below its cutoff. No films: all four.
func (e *env) seed(t *testing.T, films ...movieDef) {
	t.Helper()
	if len(films) == 0 {
		films = []movieDef{dune, argylle, wonka, alien}
	}
	at := store.Millis(t0)
	for _, m := range films {
		remux := "wanted"
		if m.id == dune.id {
			remux = "filled"
		}
		e.exec(t, `INSERT INTO title (id, kind, name, sort_name, year, tmdb_id, imdb_id, monitored, created_at, updated_at)
			VALUES (?, 'movie', ?, ?, ?, ?, ?, 1, ?, ?)`, m.id, m.title, strings.ToLower(m.title), m.year, m.tmdb, m.imdb, at, at)
		e.exec(t, `INSERT INTO title_release_date (title_id, source, country, type, date) VALUES (?, 'tmdb', '', 'digital', '2024-01-01')`, m.id)
		e.exec(t, `INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
			(?, ?, 'movie-web', 1, 'wanted', ?, ?), (?, ?, 'movie-remux', 1, ?, ?, ?)`, m.id, m.id, at, at, m.id+4, m.id, remux, at, at)
		if m.id == dune.id {
			e.exec(t, `INSERT INTO version_file (version_id, title_id, instance, arr_file_id, path, relative_path, size_bytes,
				quality, quality_revision, release_group, scene_name, languages, cutoff_not_met, added_at, release_title)
				VALUES (?, ?, 'test', 11, '/movies/Dune/x.mkv', 'Dune Part Two (2024) [WEBDL-2160p]-FLUX.mkv', 26843545600,
				'WEBDL-2160p', 1, 'FLUX', ?, '["English"]', 1, ?, ?)`, m.id+4, m.id,
				"Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-FLUX", store.Millis(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)),
				"Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-FLUX")
		}
	}
}

const (
	hShowPack = "7777777777777777777777777777777777777777"
	hShowE03  = "8888888888888888888888888888888888888888"
	showPack  = "The.Show.S01.2160p.WEB-DL.DDP5.1.H.265-GRP"
)

// seedShow writes "The Show" (title 5, tvdb 1001, scene name "Show Alt") wanted in tv (version 9): S01E01 with an
// HDTV file, S01E02 and S01E03 aired, no file.
func (e *env) seedShow(t *testing.T) {
	t.Helper()
	at := store.Millis(t0)
	e.exec(t, `INSERT INTO title (id, kind, name, sort_name, year, tvdb_id, imdb_id, monitored, series_type,
		runtime_minutes, created_at, updated_at) VALUES (5, 'series', 'The Show', 'show', 2020, 1001, 'tt1001001', 1,
		'standard', 45, ?, ?)`, at, at)
	e.exec(t, `INSERT INTO title_scene_mapping (title_id, source, name, scene_season_number, scene_origin, comment)
		VALUES (5, 'test', 'Show Alt', -1, '', '')`)
	e.exec(t, `INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at)
		VALUES (9, 5, 'tv', 1, 'wanted', ?, ?)`, at, at)
	for n := 1; n <= 3; n++ {
		air := time.Date(2026, 9, n, 2, 0, 0, 0, time.UTC)
		e.exec(t, `INSERT INTO episode (id, title_id, season, number, name, air_date, air_date_utc_at, runtime_minutes,
			monitored, created_at, updated_at) VALUES (?, 5, 1, ?, ?, ?, ?, 45, 1, ?, ?)`, n, n, fmt.Sprint("Ep ", n),
			air.Format(time.DateOnly), store.Millis(air), at, at)
		state := "wanted"
		if n == 1 {
			state = "filled"
		}
		e.exec(t, `INSERT INTO episode_version_state (version_id, episode_id, monitored, state, cutoff_met, updated_at)
			VALUES (9, ?, 1, ?, 0, ?)`, n, state, at)
	}
	e.exec(t, `INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path, relative_path, size_bytes,
		quality, quality_revision, release_group, scene_name, languages, cutoff_not_met, added_at, release_title, release_type)
		VALUES (2, 9, 5, 'test', 90, '/media/shows/The Show/Season 01/The Show S01E01.mkv', 'Season 01/The Show S01E01.mkv',
		8000000000, 'HDTV-2160p', 1, 'GRP', 'The.Show.S01E01.2160p.HDTV-GRP', '[]', 1, ?, 'The.Show.S01E01.2160p.HDTV-GRP',
		'singleEpisode')`, store.Millis(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)))
	e.exec(t, `INSERT INTO version_file_episode (version_file_id, episode_id) VALUES (2, 1)`)
}

func (e *env) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := e.st.Writer().ExecContext(e.ctx, q, args...); err != nil {
		t.Fatal(err)
	}
}

// recRouter is the default router: it records each route request and writes nothing.
type recRouter struct {
	mu   sync.Mutex
	reqs []router.Request
}

func (r *recRouter) Route(context.Context, router.Request) (*router.Result, error) {
	return &router.Result{}, nil
}

func (r *recRouter) Enqueue(_ context.Context, req router.Request, _ time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	return int64(len(r.reqs)), nil
}

// candidates lists "<version> | <indexer> | <release title>" of every routed candidate, in order.
func (r *recRouter) candidates() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, req := range r.reqs {
		for _, c := range req.Candidates {
			out = append(out, req.Version+" | "+c.Release.Indexer+" | "+c.Release.Title)
		}
	}
	return out
}

// routed is whether a candidate with this info hash was routed.
func (r *recRouter) routed(hash string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, req := range r.reqs {
		for _, c := range req.Candidates {
			if strings.EqualFold(c.Release.InfoHash, hash) {
				return true
			}
		}
	}
	return false
}

// env is a runner over a temporary database and the test network.
type env struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	pol    *policy.Policy
	net    *testNet
	prow   *feed
	dmm    *feed
	rec    *recRouter
	r      *Runner
	reg    *prometheus.Registry
	q      *jobs.Queue
	mu     sync.Mutex
	now    time.Time
	gw     *gateway.Gateway
	logBuf strings.Builder
	eng    *decide.Engine
	svc    *indexer.Service
	log    *slog.Logger
}

func (e *env) clock() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *env) advance(d time.Duration) {
	e.mu.Lock()
	e.now = e.now.Add(d)
	e.mu.Unlock()
}

var t0 = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

// newEnv builds the runner over an empty library (seed adds the films); withQueue gives it a job queue.
func newEnv(t *testing.T, withQueue bool) *env {
	t.Helper()
	ctx := context.Background()
	e := &env{t: t, ctx: ctx, now: t0, net: newNet(t), prow: &feed{movies: map[string][]item{}},
		dmm: &feed{movies: map[string][]item{}, dmm: true}, rec: &recRouter{}}
	e.prow.shows = map[string][]item{}
	e.net.serve("prowlarr.test", e.prow)
	e.net.serve("dmm.test", e.dmm)
	pol, err := policy.Load("testdata/policy")
	if err != nil {
		t.Fatal(err)
	}
	e.pol = pol
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	e.st = st
	log := slog.New(slog.NewTextHandler(&lockedWriter{b: &e.logBuf, mu: &e.mu}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	gw, err := gateway.New(ctx, pol, gateway.Options{Logger: log, Version: "test", DialContext: e.net.dial,
		Timeout: 5 * time.Second, MaxWait: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range gw.CheckEgress(ctx) {
		if !s.OK {
			t.Fatalf("egress in the test network: %+v", s)
		}
	}
	e.gw = gw
	svc, err := indexer.New(pol, gw, indexer.Options{Logger: log, CacheTTL: -1})
	if err != nil {
		t.Fatal(err)
	}
	eng, err := decide.New(pol, decide.Options{Now: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	if withQueue {
		e.q = jobs.NewQueue(st)
		e.q.SetClock(e.clock)
	}
	e.eng, e.svc, e.log = eng, svc, log
	e.build(nil)
	return e
}

// build (re)builds the runner, with rt as its router (nil: the recording router).
func (e *env) build(rt Router) {
	if rt == nil {
		rt = e.rec
	}
	e.reg = prometheus.NewRegistry()
	e.r = New(Config{Store: e.st, Policy: e.pol, Engine: e.eng, Queue: e.q, Indexers: e.svc, Budgets: e.gw,
		Router: rt, Logger: e.log, Registerer: e.reg, Now: e.clock})
}

type lockedWriter struct {
	b  *strings.Builder
	mu *sync.Mutex
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func one[T any](t *testing.T, st *store.Store, q string, args ...any) T {
	t.Helper()
	var v T
	if err := st.Reader().QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

func gatewayTarget(k string) gateway.Target {
	return gateway.Target{Class: gateway.ClassIndexer, Key: k}
}
