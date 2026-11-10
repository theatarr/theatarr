// Package e2e exercises the active pipeline with real services and HTTP/filesystem edges.
package e2e

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/arrview"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/metadata/art"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/metadata/tvdb"
	"github.com/theatarr/theatarr/internal/metadata/tvrefresh"
	"github.com/theatarr/theatarr/internal/pipeline"
	"github.com/theatarr/theatarr/internal/plex"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/qbit/qbittest"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
	"github.com/theatarr/theatarr/internal/web"
	"go.yaml.in/yaml/v3"
)

func init() { auth.SetFastHashing() }

type release struct {
	name, hash string
	files      []string
	private    bool
	season     int
	size       int64
	seeders    int // -1 is unknown; positive values are emitted, zero uses the fixture default.
	seedersSet bool
}

func movieRelease(n int, quality string) release {
	return release{name: "Arrival.2016." + quality + "-GRP", hash: fmt.Sprintf("%040x", n), files: []string{"Arrival.2016." + quality + "-GRP.mkv"}}
}

type edges struct {
	mu                     sync.Mutex
	releases               []release
	torrents               map[string]release
	adds, selects, deletes []string
	scans                  []string
	requests               []string
	searches               int
	refuse, stalled        bool
	tvdb                   map[string][]byte
	client                 *qbittest.Fake // the "fixture" download client's qBittorrent API, under /client
}

const caps = `<caps><limits default="100" max="100"/><searching><search available="yes" supportedParams="q"/><movie-search available="yes" supportedParams="q,imdbid,tmdbid"/><tv-search available="yes" supportedParams="q,tvdbid,season,ep"/></searching><categories><category id="2000" name="Movies"/><category id="5000" name="TV"/></categories></caps>`

func (f *edges) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasPrefix(r.URL.Path, "/client/"):
		http.StripPrefix("/client", f.client.Server.Config.Handler).ServeHTTP(w, r)
	case r.URL.Path == "/echo":
		fmt.Fprint(w, "192.0.2.1")
	case r.URL.Path == "/indexer":
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Query().Get("t") == "caps" {
			fmt.Fprint(w, caps)
			return
		}
		f.searches++
		fmt.Fprint(w, `<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>`)
		for _, rel := range f.releases {
			if season := r.URL.Query().Get("season"); season != "" && rel.season > 0 && season != strconv.Itoa(rel.season) {
				continue
			}
			privacy := "public"
			if rel.private {
				privacy = "private"
			}
			cat := 2000
			if r.URL.Query().Get("t") != "movie" {
				cat = 5000
			}
			size := rel.size
			if size == 0 {
				size = 20 << 30
			}
			seeders := rel.seeders
			if !rel.seedersSet {
				seeders = 20
			}
			if seeders < 0 {
				fmt.Fprintf(w, `<item><title>%s</title><guid>%s</guid><prowlarrindexer id="1" type="%s">Fixture</prowlarrindexer><pubDate>Mon, 26 Oct 2026 11:00:00 +0000</pubDate><size>%d</size><category>%d</category><torznab:attr name="infohash" value="%s"/></item>`, html.EscapeString(rel.name), rel.hash, privacy, size, cat, rel.hash)
				continue
			}
			fmt.Fprintf(w, `<item><title>%s</title><guid>%s</guid><prowlarrindexer id="1" type="%s">Fixture</prowlarrindexer><pubDate>Mon, 26 Oct 2026 11:00:00 +0000</pubDate><size>%d</size><category>%d</category><torznab:attr name="infohash" value="%s"/><torznab:attr name="seeders" value="%d"/></item>`, html.EscapeString(rel.name), rel.hash, privacy, size, cat, rel.hash, seeders)
		}
		fmt.Fprint(w, `</channel></rss>`)
	case strings.HasSuffix(r.URL.Path, "/torrents/addMagnet"):
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		magnet, err := url.Parse(r.Form.Get("magnet"))
		if err != nil {
			http.Error(w, "bad magnet", 400)
			return
		}
		hash := strings.TrimPrefix(magnet.Query().Get("xt"), "urn:btih:")
		f.adds = append(f.adds, r.URL.Path+" "+hash)
		if f.refuse && strings.HasPrefix(r.URL.Path, "/rd/") {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"infringing_file","error_code":26}`)
			return
		}
		for _, rel := range f.releases {
			if rel.hash == hash {
				id := strconv.Itoa(len(f.torrents) + 1)
				f.torrents[id] = rel
				w.WriteHeader(201)
				json.NewEncoder(w).Encode(map[string]string{"id": id})
				return
			}
		}
		http.Error(w, "unknown fixture hash", 400)
	case strings.Contains(r.URL.Path, "/torrents/selectFiles/"):
		_ = r.ParseForm()
		f.selects = append(f.selects, r.Form.Get("files"))
		w.WriteHeader(204)
	case strings.Contains(r.URL.Path, "/torrents/delete/"):
		f.deletes = append(f.deletes, r.URL.Path)
		delete(f.torrents, filepath.Base(r.URL.Path))
		w.WriteHeader(204)
	case strings.Contains(r.URL.Path, "/torrents/info/"):
		id := filepath.Base(r.URL.Path)
		rel, ok := f.torrents[id]
		if !ok {
			http.Error(w, `{"error":"unknown_ressource","error_code":7}`, 404)
			return
		}
		status := "downloaded"
		if f.stalled {
			status = "magnet_conversion"
		}
		files := []map[string]any{}
		for i, name := range rel.files {
			files = append(files, map[string]any{"id": i + 1, "path": "/" + name, "bytes": 20 << 30, "selected": 1})
		}
		json.NewEncoder(w).Encode(map[string]any{"id": id, "filename": rel.name, "hash": rel.hash, "bytes": 20 << 30, "progress": 100, "status": status, "added": "2026-10-26T12:00:00", "files": files, "links": []string{}})
	case strings.HasSuffix(r.URL.Path, "/torrents"):
		// A fresh account: reuse within Theatarr is proved by its own downloads, not a pre-seeded registry.
		w.Header().Set("X-Total-Count", "0")
		fmt.Fprint(w, `[]`)
	case strings.HasPrefix(r.URL.Path, "/tvdb/"):
		path := strings.TrimPrefix(r.URL.Path, "/tvdb")
		if path == "/login" {
			fmt.Fprint(w, `{"status":"success","data":{"token":"fixture"}}`)
			return
		}
		if p := r.URL.Query().Get("page"); p != "" {
			path += "?page=" + p
		}
		if b, ok := f.tvdb[path]; ok {
			w.Write(b)
			return
		}
		w.WriteHeader(404)
		fmt.Fprint(w, `{"status":"failure","message":"NotFoundException"}`)
	case r.URL.Path == "/tmdb/movie/329865":
		fmt.Fprint(w, `{"id":329865,"title":"Arrival","release_date":"2016-11-11","imdb_id":"tt2543164","runtime":116,"original_language":"en"}`)
	case r.URL.Path == "/plex/library/sections/18/refresh":
		if r.Header.Get("X-Plex-Token") != "fixture" || r.URL.Query().Get("path") == "" {
			http.Error(w, "not a partial scan", 400)
			return
		}
		f.scans = append(f.scans, r.URL.Query().Get("path"))
		w.WriteHeader(200)
	default:
		http.Error(w, "unexpected fixture request: "+r.URL.Path, 500)
	}
}

func (f *edges) set(rels ...release) { f.mu.Lock(); defer f.mu.Unlock(); f.releases = rels }
func (f *edges) snapshot() (adds, scans, selects []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.adds...), append([]string(nil), f.scans...), append([]string(nil), f.selects...)
}

func (f *edges) searchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.searches
}

type environment struct {
	t            *testing.T
	ctx          context.Context
	st           *store.Store
	pol          *policy.Policy
	q            *jobs.Queue
	worker       *jobs.Worker
	ac           *acquire.Acquirer
	rt           *router.Router
	av           *arrview.Service
	lib          *library.Library
	tv           *tvrefresh.Service
	art          *art.Service
	mux          *http.ServeMux
	key          string
	now          time.Time
	edge         *edges
	mount, trash string
	logs         bytes.Buffer
}

type metadataDoer struct{ gw *gateway.Gateway }

func (d metadataDoer) Do(r *http.Request) (*http.Response, error) {
	return d.gw.Do(r.Context(), gateway.Target{Class: gateway.ClassMetadata, Key: "tmdb"}, r)
}

// newEnvironment follows serve.go's constructors, without its periodic goroutines. RunOne and the injected
// clock drive the same jobs; only remote HTTP services and the zurg filesystem are fixtures.
func newEnvironment(t *testing.T, configure func(*policy.Policy)) *environment {
	t.Helper()
	e := &environment{t: t, ctx: context.Background(), now: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC), edge: &edges{torrents: map[string]release{}, tvdb: map[string][]byte{}}}
	dir := t.TempDir()
	e.mount, e.trash = filepath.Join(dir, "mount"), filepath.Join(dir, "trash")
	must(t, os.MkdirAll(filepath.Join(e.mount, "__all__"), 0755))
	srv := httptest.NewServer(e.edge)
	t.Cleanup(srv.Close)
	e.edge.client = qbittest.New()
	t.Cleanup(e.edge.client.Close)
	root := filepath.Join(dir, "movies")
	e.pol = &policy.Policy{
		Revision:  "e2e",
		Versions:  map[string]*policy.VersionPolicy{"movie": {Mode: policy.ModeActive, Grab: true, Kind: policy.KindMovie, Enable: "request", Profile: "movie", Storage: policy.StorageDebrid, Import: "symlink", Library: root, Indexers: []string{"public"}, Providers: []string{"rd"}, Naming: policy.Naming{Folder: "{Movie CleanTitle} ({Release Year})", File: "{Movie CleanTitle} {(Release Year)} - {[Quality Full]}{-Release Group}"}}},
		Profiles:  map[string]*policy.Profile{"movie": {Qualities: []policy.QualityItem{{Name: "Remux-2160p"}, {Name: "WEBDL-2160p"}}, Cutoff: "Remux-2160p"}},
		Indexers:  map[string]*policy.Indexer{"public": {Kind: "torznab", URL: srv.URL + "/indexer", Privacy: policy.PrivacyPublic}},
		Providers: map[string]*policy.Provider{"rd": {Kind: provider.KindRealDebrid, URL: srv.URL + "/rd", APIKeyEnv: "FIXTURE_RD", Order: 1, Pacing: policy.Pacing{AddsPerMinute: 100, MinGap: new(policy.Duration)}}}, // back-to-back adds: no min_gap
		Router:    &policy.Router{Enabled: true, Wanted: &policy.Wanted{PerHour: 100}},
		Library:   &policy.Library{Enabled: true, Mount: policy.LibraryMount{Path: e.mount}, Trash: policy.LibraryTrash{Dir: e.trash}, Plex: &policy.Plex{URL: srv.URL + "/plex", TokenEnv: "FIXTURE_PLEX", Debounce: policy.Duration(time.Second)}, Versions: map[string]*policy.LibraryVersion{"movie": {Roots: []string{root}, PlexSection: 18}}},
		Metadata:  &policy.Metadata{Sources: map[string]*policy.MetadataSource{"tvdb": {URL: srv.URL + "/tvdb"}, "tmdb": {URL: srv.URL + "/tmdb"}}},
		Egress:    &policy.Egress{CheckURL: srv.URL + "/echo", VPNIPs: []string{"192.0.2.1"}, RequireVPNFor: []string{policy.EgressIndexers}, Interval: policy.Duration(time.Hour), MaxAge: policy.Duration(365 * 24 * time.Hour)},
	}
	if configure != nil {
		configure(e.pol)
	}
	for _, v := range e.pol.Versions {
		must(t, os.MkdirAll(v.Library, 0755))
	}
	var err error
	e.st, err = store.Open(e.ctx, storetest.Seed(t, filepath.Join(dir, "theatarr.db")), store.Options{})
	must(t, err)
	t.Cleanup(func() { e.st.Close() })
	_, err = e.st.Migrate(e.ctx)
	must(t, err)
	// Exercise the existing write-gate, follow, naming and view assertions with DB-backed version settings.
	e.pol.Clients = map[string]*policy.Client{"fixture": {Kind: "decypharr", URL: srv.URL + "/client", Category: "theatarr", CallbackView: "fixture"}}
	e.pol.Budgets = map[string]*policy.Budget{"fixture": {Requests: 100, Per: policy.Duration(time.Minute)}}
	e.pol.Views = map[string]*policy.View{}
	versionKeys := make([]string, 0, len(e.pol.Versions))
	for key := range e.pol.Versions {
		versionKeys = append(versionKeys, key)
	}
	sort.Strings(versionKeys)
	for _, key := range versionKeys {
		version := e.pol.Versions[key]
		version.Client = "fixture"
		flavour := "radarr"
		if version.Kind == policy.KindSeries {
			flavour = "sonarr"
		}
		view := e.pol.Views[flavour]
		if view == nil {
			view = &policy.View{Flavour: flavour, Path: "/" + flavour, APIKeyEnv: "FIXTURE_VIEW"}
			e.pol.Views[flavour] = view
		}
		view.Versions = append(view.Versions, key)
	}
	e.pol.Views["fixture"] = e.pol.Views["radarr"]
	if e.pol.Views["fixture"] == nil {
		e.pol.Views["fixture"] = e.pol.Views["sonarr"]
	}
	// The callback view has its own route; the default views still expose the same versions.
	callback := *e.pol.Views["fixture"]
	callback.Path = "/fixture"
	e.pol.Views["fixture"] = &callback
	policyDir := filepath.Join(dir, "policy")
	must(t, os.Mkdir(policyDir, 0o700))
	policyYAML, err := yaml.Marshal(e.pol)
	must(t, err)
	must(t, os.WriteFile(filepath.Join(policyDir, "policy.yaml"), policyYAML, 0o600))
	base, err := policy.Load(policyDir)
	must(t, err)
	settingsStore, err := settings.New(e.st)
	must(t, err)
	snapshot, err := settingsStore.Bootstrap(e.ctx, base, settings.ImportRequest{Dir: policyDir, Actor: "e2e", Lookup: func(string) (string, bool) { return "fixture", true }})
	must(t, err)
	e.pol = snapshot.Policy
	log := slog.New(slog.NewTextHandler(&e.logs, nil))
	clock := func() time.Time { return e.now }
	gw, err := gateway.New(e.ctx, e.pol, gateway.Options{Store: e.st, Logger: log, Now: clock, Timeout: 2 * time.Second, MaxWait: time.Millisecond, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != srv.Listener.Addr().String() {
			return nil, fmt.Errorf("external dial forbidden: %s", addr)
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}})
	must(t, err)
	gw.CheckEgress(e.ctx)
	e.q = jobs.NewQueue(e.st)
	e.q.SetClock(clock)
	e.worker = jobs.NewWorker(e.q, jobs.WorkerOptions{Owner: "e2e", Logger: log})
	provs := map[string]provider.Provider{}
	for k, p := range e.pol.Providers {
		provs[k], err = provider.New(k, p, "fixture", gw)
		must(t, err)
	}
	adders, skipped := router.BuildAdders(e.pol, gw, func(string) string { return "fixture" }, log)
	if len(skipped) > 0 {
		t.Fatal(skipped)
	}
	e.rt, err = router.New(router.Config{Store: e.st, Policy: e.pol, Adders: adders, Providers: provs, Logger: log, Now: clock,
		Clients: router.BuildClients(e.pol, gw, func(string) string { return "" }, log)})
	must(t, err)
	e.rt.RegisterJobs(e.worker, e.q)
	eng, err := decide.New(e.pol, decide.Options{Now: clock})
	must(t, err)
	idx, err := indexer.New(e.pol, gw, indexer.Options{Logger: log, CacheTTL: -1})
	must(t, err)
	e.lib = library.New(library.Options{Store: e.st, Policy: e.pol, Engine: eng, Queue: e.q, Logger: log, Now: clock})
	px, err := plex.New(gw, e.pol.Library.Plex.URL, "fixture")
	must(t, err)
	e.lib.RegisterJobs(e.worker, px)
	tc, err := tvdb.New(tvdb.Options{BaseURL: e.pol.Metadata.Sources["tvdb"].URL, APIKey: "fixture", Doer: gw, Store: e.st, Now: clock, MinInterval: time.Nanosecond, NoSpecialsAirs: true, Sleep: func(context.Context, time.Duration) error { return nil }})
	must(t, err)
	e.tv, err = tvrefresh.New(tvrefresh.Config{Store: e.st, Policy: e.pol, Queue: e.q, TVDB: tc, Logger: log, Now: clock})
	must(t, err)
	e.tv.RegisterJobs(e.worker)
	e.ac, err = pipeline.NewAcquirer(acquire.Config{Store: e.st, Policy: e.pol, Queue: e.q, Router: e.rt, Engine: eng, Indexers: idx, Logger: log, Now: clock}, e.tv)
	must(t, err)
	e.ac.RegisterJobs(e.worker)
	tm, err := tmdb.New(tmdb.Options{BaseURL: e.pol.Metadata.Sources["tmdb"].URL, APIKey: "fixture", Doer: metadataDoer{gw}, Now: clock, MinInterval: time.Nanosecond})
	must(t, err)
	e.art, err = art.New(art.Config{Store: e.st, Queue: e.q, TMDb: tm, Logger: log, Now: clock})
	must(t, err)
	au, _, err := auth.New(e.ctx, e.st, auth.Options{Logger: log, Now: clock, DataDir: dir})
	must(t, err)
	e.key, _, err = au.CreateKey(e.ctx, "e2e", 1)
	must(t, err)
	av, err := arrview.New(e.ctx, arrview.Config{Store: e.st, Auth: au, Policy: e.pol, Logger: log, Now: clock, Getenv: func(string) string { return "" }, Searcher: e.ac, Decider: e.ac, Router: e.rt, Library: e.lib})
	must(t, err)
	e.av = av
	av.SetMaintenance(e.q, nil)
	av.RegisterJobs(e.worker)
	ui, err := web.New(web.Config{Store: e.st, Auth: au, Policy: e.pol, Logger: log, Now: clock, Devices: av, Add: web.AddConfig{Acquirer: e.ac, TMDb: tm}, Jobs: e.q, Ops: viewOps{av}})
	must(t, err)
	e.mux = http.NewServeMux()
	av.Mount(e.mux)
	ui.Mount(e.mux)
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(e.logs.String())
		}
	})
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func (e *environment) request(method, path string, body any, out any) {
	e.t.Helper()
	var b io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		must(e.t, err)
		b = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, b)
	req.Header.Set("X-Api-Key", e.key)
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	e.mux.ServeHTTP(rw, req)
	if rw.Code/100 != 2 {
		e.t.Fatalf("%s %s: %d %s", method, path, rw.Code, rw.Body.String())
	}
	if out != nil {
		must(e.t, json.Unmarshal(rw.Body.Bytes(), out))
	}
}
func (e *environment) drain() {
	e.t.Helper()
	for range 100 {
		ran, err := e.worker.RunOne(e.ctx)
		must(e.t, err)
		if !ran {
			if n := e.count(`SELECT count(*) FROM job WHERE state='failed' OR (state='queued' AND attempts>0)`); n > 0 {
				e.t.Fatalf("%d failed/retrying jobs\n%s", n, e.logs.String())
			}
			return
		}
	}
	e.t.Fatal("job queue did not quiesce")
}
func (e *environment) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	must(e.t, e.st.Reader().QueryRowContext(e.ctx, q, args...).Scan(&n))
	return n
}
func (e *environment) text(q string, args ...any) string {
	e.t.Helper()
	var s string
	must(e.t, e.st.Reader().QueryRowContext(e.ctx, q, args...).Scan(&s))
	return s
}
func (e *environment) addMovie(native bool, versions ...string) int64 {
	e.t.Helper()
	if native {
		var result web.AddResult
		e.request("POST", web.APIBase+"/titles", web.AddRequest{Kind: "movie", TMDbID: 329865, Versions: versions}, &result)
		if !result.Created || result.Title.Name != "Arrival" {
			e.t.Fatalf("native add: %+v", result)
		}
		must(e.t, e.art.FillTitle(e.ctx, result.Title.ID))
		return result.Title.ID
	}
	var result struct {
		ID int64 `json:"id"`
	}
	e.request("POST", "/v/"+versions[0]+"/radarr/api/v3/movie", map[string]any{"tmdbId": 329865, "imdbId": "tt2543164", "title": "Arrival", "year": 2016, "monitored": true}, &result)
	must(e.t, e.art.FillTitle(e.ctx, result.ID-arrview.IDBase))
	return result.ID - arrview.IDBase
}
func (e *environment) wanted() {
	e.t.Helper()
	res, err := e.ac.Wanted(e.ctx)
	must(e.t, err)
	if res.Queued == 0 {
		e.t.Fatalf("wanted queued nothing: %+v", res)
	}
	e.drain()
}
func (e *environment) mountRelease(rel release) {
	e.t.Helper()
	for _, name := range rel.files {
		path := filepath.Join(e.mount, "__all__", rel.name, name)
		must(e.t, os.MkdirAll(filepath.Dir(path), 0755))
		must(e.t, os.WriteFile(path, video(), 0644))
	}
}
func (e *environment) finish(want int) []*library.Plan {
	e.t.Helper()
	_, err := e.rt.Poll(e.ctx)
	must(e.t, err)
	rows, err := e.st.Reader().QueryContext(e.ctx, `SELECT id FROM download WHERE state IN ('ready','importing') ORDER BY id`)
	must(e.t, err)
	var ids []int64
	for rows.Next() {
		var id int64
		must(e.t, rows.Scan(&id))
		ids = append(ids, id)
	}
	must(e.t, rows.Err())
	rows.Close()
	if len(ids) != want {
		e.t.Fatalf("ready downloads %v, want %d\n%s", ids, want, e.logs.String())
	}
	var plans []*library.Plan
	for _, id := range ids {
		plan, err := e.lib.Import(e.ctx, id, false)
		must(e.t, err)
		plans = append(plans, plan)
		if plan.Waiting || plan.DryRun || len(plan.Links) == 0 || len(plan.Rejected) > 0 || len(plan.Unmatched) > 0 {
			e.t.Fatalf("import: %+v", plan)
		}
		for _, link := range plan.Links {
			if !link.Done {
				e.t.Fatalf("link not imported: %+v", link)
			}
			target, err := os.Readlink(link.Path)
			must(e.t, err)
			if target != link.Target {
				e.t.Fatalf("link %s -> %s, want %s", link.Path, target, link.Target)
			}
			_, err = os.Stat(link.Path)
			must(e.t, err)
		}
	}
	e.now = e.now.Add(2 * time.Second)
	e.drain()
	return plans
}

// A minimal Matroska header, as in library/probe's tests: the real probe reads HEVC, 2160p and two hours.
func video() []byte {
	el := func(id uint32, data ...[]byte) []byte {
		var h [4]byte
		binary.BigEndian.PutUint32(h[:], id)
		b := bytes.TrimLeft(h[:], "\x00")
		body := bytes.Join(data, nil)
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(body)))
		size[0] = 1
		out := append([]byte(nil), b...)
		out = append(out, size[:]...)
		return append(out, body...)
	}
	u := func(id uint32, n uint64) []byte {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], n)
		return el(id, b[:])
	}
	info := el(0x1549A966, u(0x2AD7B1, 1000000), u(0x4489, math.Float64bits(7200000)))
	track := el(0xAE, u(0xD7, 1), u(0x83, 1), el(0x86, []byte("V_MPEGH/ISO/HEVC")), el(0xE0, u(0xB0, 3840), u(0xBA, 2160)))
	return append(el(0x1A45DFA3, u(0x4286, 1)), el(0x18538067, info, el(0x1654AE6B, track))...)
}
