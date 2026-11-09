package shadow

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/importarr"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
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

func (f *feed) set(imdb string, items ...item) {
	f.mu.Lock()
	f.movies[strings.TrimPrefix(imdb, "tt")] = items
	f.mu.Unlock()
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

// fakeArr is an importarr.Getter holding one Radarr instance in memory.
type fakeArr struct {
	mu      sync.Mutex
	movies  []map[string]any
	files   map[int]map[string]any
	history []map[string]any
}

func (f *fakeArr) GetJSON(ctx context.Context, ep string, q url.Values, v any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out any
	switch ep {
	case "blocklist":
		out = map[string]any{"totalRecords": 0, "records": []any{}}
	case "system/status":
		out = map[string]any{"appName": "Radarr", "version": "6.4.4.10685"}
	case "rootfolder":
		out = []map[string]any{{"id": 1, "path": "/movies"}}
	case "config/mediamanagement", "config/indexer", "config/naming":
		out = map[string]any{}
	case "tag", "delayprofile", "releaseprofile", "customformat", "qualitydefinition":
		out = []map[string]any{}
	case "qualityprofile":
		out = []map[string]any{{"id": 7, "name": "Remux + WEB 2160p", "upgradeAllowed": true, "cutoff": 31,
			"items": []map[string]any{{"quality": map[string]any{"id": 31, "name": "Remux-2160p"}}}}}
	case "movie":
		out = f.movies
	case "moviefile":
		list := []map[string]any{}
		for _, s := range q["movieId"] {
			var id int
			fmt.Sscan(s, &id)
			if mf, ok := f.files[id]; ok {
				list = append(list, mf)
			}
		}
		out = list
	case "history":
		recs := append([]map[string]any(nil), f.history...)
		// newest first, as the arr pages
		for i, j := 0, len(recs)-1; i < j; i, j = i+1, j-1 {
			recs[i], recs[j] = recs[j], recs[i]
		}
		out = map[string]any{"page": 1, "pageSize": 250, "totalRecords": len(recs), "records": recs}
	default:
		return fmt.Errorf("fake arr: no %s", ep)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (f *fakeArr) add(h map[string]any) {
	f.mu.Lock()
	f.history = append(f.history, h)
	f.mu.Unlock()
}

func quality(name string, id int) map[string]any {
	return map[string]any{"quality": map[string]any{"id": id, "name": name}, "revision": map[string]any{"version": 1, "real": 0}}
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

func newArr(withFiles bool) *fakeArr {
	f := &fakeArr{files: map[int]map[string]any{}}
	for _, m := range []movieDef{dune, argylle, wonka, alien} {
		hasFile := withFiles && m.id == dune.id
		mv := map[string]any{"id": m.id, "title": m.title, "sortTitle": strings.ToLower(m.title), "year": m.year,
			"tmdbId": m.tmdb, "imdbId": m.imdb, "monitored": true, "hasFile": hasFile, "qualityProfileId": 7,
			"status": "released", "path": "/movies/" + m.title, "rootFolderPath": "/movies", "tags": []int{},
			"digitalRelease": "2024-01-01T00:00:00Z",
			"added":          "2026-01-01T00:00:00Z"}
		if hasFile {
			mv["movieFileId"] = 11
		}
		f.movies = append(f.movies, mv)
	}
	if withFiles {
		f.files[dune.id] = map[string]any{"id": 11, "movieId": dune.id,
			"relativePath": "Dune Part Two (2024) [WEBDL-2160p]-FLUX.mkv", "path": "/movies/Dune/x.mkv",
			"size": 26843545600, "dateAdded": "2026-10-01T00:00:00Z",
			"sceneName": "Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-FLUX", "releaseGroup": "FLUX",
			"languages": []map[string]any{{"id": 1, "name": "English"}}, "quality": quality("WEBDL-2160p", 18),
			"qualityCutoffNotMet": true}
		f.history = []map[string]any{{"id": 1, "movieId": dune.id, "eventType": "downloadFolderImported",
			"date": "2026-10-01T00:00:00Z", "sourceTitle": "Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-FLUX",
			"quality": quality("WEBDL-2160p", 18), "data": map[string]any{"fileId": "11"}}}
	}
	return f
}

func grabEvent(id int, m movieDef, at time.Time, title, hash, q string, qid, score int, source string) map[string]any {
	return map[string]any{"id": id, "movieId": m.id, "eventType": "grabbed", "date": at.Format(time.RFC3339),
		"downloadId": strings.ToUpper(hash), "sourceTitle": title, "quality": quality(q, qid),
		"languages": []map[string]any{{"id": 1, "name": "English"}},
		"data": map[string]any{"indexer": "Example (Prowlarr)", "protocol": "2", "size": "40000000000",
			"torrentInfoHash": hash, "customFormatScore": fmt.Sprint(score), "releaseSource": source,
			"movieMatchType": "Title"}}
}

// env is a runner over a temporary database, the test network and two fake arrs.
type env struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	pol    *policy.Policy
	net    *testNet
	prow   *feed
	dmm    *feed
	zurg   *fakeArr
	web    *fakeArr
	tv     *fakeSonarr
	r      *Runner
	reg    *prometheus.Registry
	q      *jobs.Queue
	mu     sync.Mutex
	now    time.Time
	gw     *gateway.Gateway
	warns  []string
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

// newEnv builds the runner; withQueue gives it a job queue (grabs are then enqueued, not run inline).
func newEnv(t *testing.T, withQueue bool) *env {
	t.Helper()
	ctx := context.Background()
	e := &env{t: t, ctx: ctx, now: t0, net: newNet(t), prow: &feed{movies: map[string][]item{}},
		dmm: &feed{movies: map[string][]item{}, dmm: true}, zurg: newArr(true), web: newArr(false), tv: &fakeSonarr{}}
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

// build (re)builds the runner, with rt as its router (nil: none).
func (e *env) build(rt Router) {
	e.reg = prometheus.NewRegistry()
	e.r, e.warns = New(Config{Store: e.st, Policy: e.pol, Engine: e.eng, Queue: e.q, Indexers: e.svc, Budgets: e.gw,
		Router: rt, Arr: func(inst string) (importarr.Getter, error) {
			if inst == "radarr_zurg" {
				return e.zurg, nil
			}
			if inst == "sonarr_tv" {
				return e.tv, nil
			}
			return e.web, nil
		}, Logger: e.log, Registerer: e.reg, Now: e.clock})
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
