package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
)

// The fixtures in testdata are hand-written from the providers' API docs and decypharr's response types; hashes are
// repeated letters, links and addresses use example.invalid, and there are no keys.

const (
	keyRD = "FIXTURE-RD-KEY-0123456789"
	keyAD = "FIXTURE-AD-KEY-0123456789"
	keyTB = "FIXTURE-TB-KEY-0123456789"
)

func h(c string) string { return strings.Repeat(c, 40) }

// fakeNet maps test host names to local servers through the gateway's DialContext, so nothing leaves the machine.
type fakeNet struct {
	t     *testing.T
	mu    sync.Mutex
	hosts map[string]string
	reqs  []string // "host METHOD path?query"
	logs  bytes.Buffer
}

func newFakeNet(t *testing.T) *fakeNet { return &fakeNet{t: t, hosts: map[string]string{}} }

// serve starts a server for name:80. Every request must be a GET with the bearer key; anything else fails the test.
func (f *fakeNet) serve(name, key string, h http.HandlerFunc) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reqs = append(f.reqs, name+" "+r.Method+" "+r.URL.RequestURI())
		f.mu.Unlock()
		if r.Method != http.MethodGet {
			f.t.Errorf("%s: %s %s: only GET is allowed", name, r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if strings.Contains(r.URL.RawQuery, key) {
			f.t.Errorf("%s: API key in the query string", name)
		}
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"detail": "Not authenticated"}`)
			return
		}
		h(w, r)
	}))
	f.t.Cleanup(srv.Close)
	f.mu.Lock()
	f.hosts[name+":80"] = srv.Listener.Addr().String()
	f.mu.Unlock()
}

func (f *fakeNet) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	f.mu.Lock()
	real, ok := f.hosts[addr]
	f.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("dial %s: not in the test network", addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, real)
}

func (f *fakeNet) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reqs...)
}

func testPolicy() *policy.Policy {
	return &policy.Policy{
		Indexers: map[string]*policy.Indexer{
			"pub":  {Kind: "torznab", URL: "http://pub.test/api", Privacy: policy.PrivacyPublic},
			"priv": {Kind: "torznab", URL: "http://priv.test/api", Privacy: policy.PrivacyPrivate},
		},
		Providers: map[string]*policy.Provider{
			"rd": {Kind: KindRealDebrid, Order: 1, URL: "http://rd.test/rest/1.0", APIKeyEnv: "THEATARR_RD_KEY"},
			"ad": {Kind: KindAllDebrid, Order: 2, URL: "http://ad.test/v4", APIKeyEnv: "THEATARR_AD_KEY"},
			"tb": {Kind: KindTorBox, Order: 3, URL: "http://tb.test/v1", APIKeyEnv: "THEATARR_TB_KEY"},
		},
		Budgets: map[string]*policy.Budget{},
	}
}

func (f *fakeNet) gateway() *gateway.Gateway {
	f.t.Helper()
	log := slog.New(slog.NewJSONHandler(&f.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	g, err := gateway.New(context.Background(), testPolicy(), gateway.Options{Logger: log, Version: "test",
		DialContext: f.dial, Timeout: 5 * time.Second, MaxWait: time.Second})
	if err != nil {
		f.t.Fatal(err)
	}
	return g
}

func (f *fakeNet) client(key, apiKey string) Provider {
	f.t.Helper()
	p, err := New(key, testPolicy().Providers[key], apiKey, f.gateway())
	if err != nil {
		f.t.Fatal(err)
	}
	return p
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeFixture(t *testing.T, w http.ResponseWriter, status int, name string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(fixture(t, name))
}

// noLinks fails if anything derived from provider answers carries a download link.
func noLinks(t *testing.T, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if bytes.Contains(b, []byte("example.invalid")) {
		t.Errorf("a link or e-mail leaked into %T: %s", v, b)
	}
}

func TestRealDebrid(t *testing.T) {
	defer func(n int) { rdPageSize = n }(rdPageSize)
	rdPageSize = 2
	f := newFakeNet(t)
	f.serve("rd.test", keyRD, func(w http.ResponseWriter, r *http.Request) {
		switch p := r.URL.Path; {
		case p == "/rest/1.0/user":
			writeFixture(t, w, 200, "rd/user.json")
		case p == "/rest/1.0/torrents/activeCount":
			writeFixture(t, w, 200, "rd/active_count.json")
		case p == "/rest/1.0/torrents":
			w.Header().Set("X-Total-Count", "4")
			switch r.URL.Query().Get("offset") {
			case "":
				writeFixture(t, w, 200, "rd/torrents_p1.json")
			case "2":
				writeFixture(t, w, 200, "rd/torrents_p2.json")
			default:
				w.WriteHeader(http.StatusNoContent)
			}
		case p == "/rest/1.0/torrents/info/RDID0002":
			writeFixture(t, w, 200, "rd/torrent_info.json")
		default:
			writeFixture(t, w, 404, "rd/error_unknown_ressource.json")
		}
	})
	p := f.client("rd", keyRD)
	ctx := context.Background()

	acc, err := p.Account(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Plan != "premium" || !acc.Premium || acc.ActiveTorrents != 3 || acc.MaxActiveTorrents != 25 ||
		acc.Points != 1200 {
		t.Errorf("account: %+v", acc)
	}
	// "2026-12-01T10:00:00.000Z" is Paris wall-clock time: 09:00 UTC in winter.
	if want := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC); !acc.Expires.Equal(want) {
		t.Errorf("expires %v, want %v", acc.Expires, want)
	}
	noLinks(t, acc)

	list, err := p.Torrents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 4 {
		t.Fatalf("got %d torrents, want 4", len(list))
	}
	if list[0].Hash != h("a") || list[0].State != StateDownloaded || list[0].Links != 1 || list[0].Files != nil {
		t.Errorf("first: %+v", list[0])
	}
	if list[2].Hash != h("c") || list[2].State != StateDownloading || list[2].Progress != 42.5 {
		t.Errorf("upper-case hash or state: %+v", list[2])
	}
	// 2026-10-01 10:00 Paris is 08:00 UTC (summer time).
	if want := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC); !list[0].Added.Equal(want) {
		t.Errorf("added %v, want %v", list[0].Added, want)
	}
	noLinks(t, list)

	one, err := p.Torrent(ctx, "RDID0002")
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Files) != 2 || one.Files[0].Path != "Movie.B.2021.1080p.BluRay.mkv" || !one.Files[0].Selected ||
		one.Files[1].Selected || one.Links != 2 {
		t.Errorf("info: %+v", one)
	}
	noLinks(t, one)

	if _, err := p.Torrent(ctx, "NOPE"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing torrent: %v, want ErrNotFound", err)
	}
	if _, err := p.CheckCached(ctx, CacheQuery{Hashes: []string{h("a")}}); !errors.Is(err, ErrCacheUnsupported) {
		t.Errorf("CheckCached: %v", err)
	}
	if p.Cache().Supported {
		t.Error("RD claims a cache check")
	}

	bad := f.client("rd", "WRONG-KEY")
	if _, err := bad.Account(ctx); !errors.Is(err, ErrAuth) {
		t.Errorf("wrong key: %v, want ErrAuth", err)
	}
	for _, r := range f.requests() {
		if !strings.Contains(r, " GET ") {
			t.Errorf("non-GET request %s", r)
		}
	}
}

func TestRealDebridShiftingList(t *testing.T) {
	defer func(n int) { rdPageSize = n }(rdPageSize)
	rdPageSize = 2
	f := newFakeNet(t)
	reads := 0
	f.serve("rd.test", keyRD, func(w http.ResponseWriter, r *http.Request) {
		// X-Total-Count says 5, but only 4 distinct torrents ever arrive: a delete at a page boundary.
		w.Header().Set("X-Total-Count", "5")
		switch r.URL.Query().Get("offset") {
		case "":
			reads++
			writeFixture(t, w, 200, "rd/torrents_p1.json")
		case "2":
			writeFixture(t, w, 200, "rd/torrents_p2.json")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	_, err := f.client("rd", keyRD).Torrents(context.Background())
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("short list: %v, want ErrIncomplete", err)
	}
	if reads != rdListAttempts {
		t.Errorf("read the list %d times, want %d", reads, rdListAttempts)
	}
}

func TestAllDebrid(t *testing.T) {
	f := newFakeNet(t)
	f.serve("ad.test", keyAD, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v4.1/user":
			writeFixture(t, w, 200, "ad/user.json")
		case "/v4.1/magnet/status":
			if r.URL.Query().Get("id") == "101" {
				writeFixture(t, w, 200, "ad/magnet_one.json")
				return
			}
			writeFixture(t, w, 200, "ad/magnets.json")
		case "/v4.1/magnet/files":
			if r.URL.Query()["id[]"][0] != "101" {
				t.Errorf("files for %v", r.URL.Query())
			}
			writeFixture(t, w, 200, "ad/magnet_files.json")
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	p := f.client("ad", keyAD)
	ctx := context.Background()

	acc, err := p.Account(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Plan != "premium" || !acc.Expires.Equal(time.Unix(1798761600, 0)) || len(acc.Notes) != 2 ||
		acc.Notes[0] != "hoster quota hostera: 500" {
		t.Errorf("account: %+v", acc)
	}
	noLinks(t, acc)

	list, err := p.Torrents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d magnets", len(list))
	}
	if list[0].ID != "101" || list[0].Hash != h("b") || list[0].State != StateDownloaded || list[0].Progress != 100 ||
		list[0].Links != -1 {
		t.Errorf("ready magnet: %+v", list[0])
	}
	if list[1].State != StateDownloading || list[1].Progress != 25 {
		t.Errorf("downloading magnet: %+v", list[1])
	}
	if list[2].State != StateError || list[2].Hash != "" {
		t.Errorf("failed magnet: %+v", list[2])
	}

	one, err := p.Torrent(ctx, "101")
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Files) != 2 || one.Files[1].Path != "Movie.B.2021.1080p.BluRay/Subs/en.srt" || one.Links != 2 {
		t.Errorf("one: %+v", one)
	}
	noLinks(t, one)
}

func TestAllDebridErrorsWithHTTP200(t *testing.T) {
	f := newFakeNet(t)
	f.serve("ad.test", keyAD, func(w http.ResponseWriter, r *http.Request) {
		writeFixture(t, w, 200, "ad/error_auth.json")
	})
	_, err := f.client("ad", keyAD).Torrents(context.Background())
	var ae *APIError
	if !errors.Is(err, ErrAuth) || !errors.As(err, &ae) || ae.Code != "AUTH_BAD_APIKEY" {
		t.Errorf("got %v, want ErrAuth with AUTH_BAD_APIKEY", err)
	}
}

func TestTorBox(t *testing.T) {
	defer func(n int) { tbPageSize = n }(tbPageSize)
	tbPageSize = 2
	f := newFakeNet(t)
	f.serve("tb.test", keyTB, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/v1/api/user/me":
			writeFixture(t, w, 200, "tb/user.json")
		case "/v1/api/torrents/mylist":
			if q.Get("bypass_cache") != "true" {
				t.Error("mylist without bypass_cache")
			}
			switch {
			case q.Get("id") == "201":
				writeFixture(t, w, 200, "tb/mylist_one.json")
			case q.Get("id") != "":
				w.WriteHeader(404)
				fmt.Fprint(w, `{"success": false, "error": "ITEM_NOT_FOUND", "detail": "not found", "data": null}`)
			case q.Get("offset") == "0":
				writeFixture(t, w, 200, "tb/mylist_p1.json")
			case q.Get("offset") == "1":
				// The next page re-reads one entry of the previous one (overlap), then ends.
				fmt.Fprint(w, `{"success": true, "data": [`+string(firstTB(t))+`]}`)
			default:
				t.Errorf("mylist offset %q", q.Get("offset"))
			}
		case "/v1/api/torrents/checkcached":
			if q.Get("format") != "object" || q.Get("list_files") != "false" {
				t.Errorf("checkcached query %v", q)
			}
			writeFixture(t, w, 200, "tb/checkcached.json")
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	p := f.client("tb", keyTB)
	ctx := context.Background()

	acc, err := p.Account(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Plan != "pro" || !acc.Premium || acc.MaxActiveTorrents != 10 || acc.Expires.Year() != 2027 {
		t.Errorf("account: %+v", acc)
	}
	noLinks(t, acc)

	list, err := p.Torrents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d torrents", len(list))
	}
	if list[0].State != StateDownloaded || list[0].Progress != 100 || list[0].Links != -1 ||
		list[0].Files[0].Path != "Movie.E.2023.2160p/Movie.E.2023.2160p.mkv" {
		t.Errorf("first: %+v", list[0])
	}
	if list[1].State != StateQueued || list[1].Status != "stalled (no seeds)" || list[1].Added.Hour() != 8 {
		t.Errorf("stalled: %+v", list[1])
	}
	noLinks(t, list)

	one, err := p.Torrent(ctx, "201")
	if err != nil || one.ID != "201" {
		t.Fatalf("one: %+v %v", one, err)
	}
	if _, err := p.Torrent(ctx, "999"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}

	got, err := p.CheckCached(ctx, CacheQuery{Hashes: []string{strings.ToUpper(h("e")), h("f")}})
	if err != nil {
		t.Fatal(err)
	}
	if !got[h("e")] || got[h("f")] || len(got) != 2 {
		t.Errorf("checkcached: %v", got)
	}
}

func firstTB(t *testing.T) []byte {
	var env struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(fixture(t, "tb/mylist_p1.json"), &env); err != nil {
		t.Fatal(err)
	}
	return env.Data[1]
}

// A cache check takes hashes only.
func TestCacheCheckRefusesNonHashes(t *testing.T) {
	f := newFakeNet(t)
	f.serve("tb.test", keyTB, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("request reached the provider: %s", r.URL.Path)
	})
	p := f.client("tb", keyTB)
	if _, err := p.CheckCached(context.Background(), CacheQuery{Hashes: []string{"magnet:?xt=urn:btih:x"}}); err == nil {
		t.Error("not a hash: accepted")
	}
}

func TestTorBoxErrors(t *testing.T) {
	f := newFakeNet(t)
	f.serve("tb.test", keyTB, func(w http.ResponseWriter, r *http.Request) {
		writeFixture(t, w, 200, "tb/error_auth.json")
	})
	if _, err := f.client("tb", keyTB).Account(context.Background()); !errors.Is(err, ErrAuth) {
		t.Errorf("BAD_TOKEN with HTTP 200: %v", err)
	}
	if _, err := f.client("tb", "WRONG").Account(context.Background()); !errors.Is(err, ErrAuth) {
		t.Errorf("HTTP 401: %v", err)
	}
}

// Keys never show in errors or the gateway's logs.
func TestKeysStayOutOfLogsAndErrors(t *testing.T) {
	f := newFakeNet(t)
	f.serve("rd.test", keyRD, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	})
	p := f.client("rd", keyRD)
	_, err := p.Torrents(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), keyRD) || strings.Contains(f.logs.String(), keyRD) {
		t.Error("the API key leaked into an error or a log")
	}
	if _, err := New("rd", testPolicy().Providers["rd"], "", f.gateway()); !errors.Is(err, ErrNoKey) {
		t.Errorf("empty key: %v", err)
	}
}
