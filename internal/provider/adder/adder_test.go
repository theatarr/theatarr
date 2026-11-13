package adder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
)

// The fixtures in testdata are hand-written from the providers' API docs and decypharr's response types; hashes are
// repeated letters, addresses use example.invalid, and there are no keys.

const key = "FIXTURE-KEY-0123456789"

func h(c string) string { return strings.Repeat(c, 40) }

type request struct {
	host, method, path, query string
	form                      url.Values
	auth                      string
}

// fakeNet maps test host names to local servers through the gateway's DialContext, so nothing leaves the machine.
type fakeNet struct {
	t     *testing.T
	mu    sync.Mutex
	hosts map[string]string
	reqs  []request
}

func newFakeNet(t *testing.T) *fakeNet { return &fakeNet{t: t, hosts: map[string]string{}} }

func (f *fakeNet) serve(name string, h http.HandlerFunc) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(b))
		f.mu.Lock()
		f.reqs = append(f.reqs, request{host: name, method: r.Method, path: r.URL.Path, query: r.URL.RawQuery,
			form: form, auth: r.Header.Get("Authorization")})
		f.mu.Unlock()
		if strings.Contains(r.URL.RawQuery, key) || strings.Contains(string(b), key) {
			f.t.Errorf("%s: API key outside the Authorization header", name)
		}
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
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

func (f *fakeNet) requests() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]request(nil), f.reqs...)
}

func testPolicy() *policy.Policy {
	return &policy.Policy{
		Indexers: map[string]*policy.Indexer{
			"pub":  {Kind: "torznab", URL: "http://pub.test/api", Privacy: policy.PrivacyPublic},
			"priv": {Kind: "torznab", URL: "http://priv.test/api", Privacy: policy.PrivacyPrivate},
			"unk":  {Kind: "torznab", URL: "http://unk.test/api"},
		},
		Providers: map[string]*policy.Provider{
			"rd": {Kind: provider.KindRealDebrid, Order: 1, URL: "http://rd.test/rest/1.0", APIKeyEnv: "THEATARR_RD_KEY"},
			"ad": {Kind: provider.KindAllDebrid, Order: 2, URL: "http://ad.test/v4", APIKeyEnv: "THEATARR_AD_KEY"},
			"tb": {Kind: provider.KindTorBox, Order: 3, URL: "http://tb.test/v1", APIKeyEnv: "THEATARR_TB_KEY"},
		},
		Budgets: map[string]*policy.Budget{},
	}
}

func (f *fakeNet) adder(k string) Adder {
	f.t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	g, err := gateway.New(context.Background(), testPolicy(), gateway.Options{Logger: log, Version: "test",
		DialContext: f.dial, Timeout: 5 * time.Second, MaxWait: time.Second})
	if err != nil {
		f.t.Fatal(err)
	}
	a, err := New(k, testPolicy().Providers[k], key, g)
	if err != nil {
		f.t.Fatal(err)
	}
	return a
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func reply(t *testing.T, w http.ResponseWriter, status int, name string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if name != "" {
		w.Write(fixture(t, name))
	}
}

func publicRelease(hash, title string) *indexer.Release {
	return &indexer.Release{Indexer: "pub", Kind: "torznab", Title: title, InfoHash: hash,
		Privacy: policy.PrivacyPublic}
}

func magnet(t *testing.T, hash, title string) Magnet {
	t.Helper()
	m, err := NewMagnet(publicRelease(hash, title), nil)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func code(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestNewMagnet(t *testing.T) {
	m, err := NewMagnet(publicRelease(strings.ToUpper(h("a")), "Movie.2024.2160p.WEB.H265-GRP"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.URI() != "magnet:?xt=urn:btih:"+h("a") || m.Indexer() != "pub" || m.Hash() != h("a") {
		t.Errorf("magnet %q %q", m.URI(), m.Indexer())
	}
	if s := fmt.Sprint(m); strings.Contains(s, "tr=") {
		t.Errorf("String shows trackers: %s", s)
	}
	m, err = NewMagnet(publicRelease(h("a"), "x"), []string{"udp://tracker.opentrackr.org:1337/announce"})
	if err != nil || m.URI() != "magnet:?xt=urn:btih:"+h("a")+"&tr="+url.QueryEscape("udp://tracker.opentrackr.org:1337/announce") {
		t.Errorf("with tracker: %q %v", m.URI(), err)
	}
	// Privacy is a label only: a private-labelled release gets its bare-hash magnet too.
	if m, err := NewMagnet(&indexer.Release{Indexer: "priv", InfoHash: h("b"), Privacy: policy.PrivacyPrivate},
		nil); err != nil || m.URI() != "magnet:?xt=urn:btih:"+h("b") {
		t.Errorf("private-labelled: %q %v", m.URI(), err)
	}
	for name, tc := range map[string]struct {
		r        *indexer.Release
		trackers []string
		want     error
	}{
		"no hash":       {publicRelease("", "x"), nil, ErrNoHash},
		"bad hash":      {publicRelease("xyz", "x"), nil, ErrNoHash},
		"nil":           {nil, nil, ErrNoHash},
		"keyed tracker": {publicRelease(h("a"), "x"), []string{"http://t.example.invalid/announce?passkey=0123456789abcdef0123"}, nil},
	} {
		_, err := NewMagnet(tc.r, tc.trackers)
		if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
	}
	// The zero Magnet is refused by every adder without a request.
	f := newFakeNet(t)
	for _, k := range []string{"rd", "ad", "tb"} {
		f.serve(k+".test", func(w http.ResponseWriter, r *http.Request) { t.Errorf("%s: a request was sent", k) })
		if _, err := f.adder(k).AddMagnet(context.Background(), Magnet{}, AddOptions{}); code(err) != CodeBlocked {
			t.Errorf("%s: zero magnet: %v", k, err)
		}
	}
}

func TestRealDebridAdd(t *testing.T) {
	ctx := context.Background()
	var status int
	var file string
	f := newFakeNet(t)
	f.serve("rd.test", func(w http.ResponseWriter, r *http.Request) {
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "120")
		}
		reply(t, w, status, file)
	})
	a := f.adder("rd")
	if !a.SelectsFiles() || a.Kind() != provider.KindRealDebrid || a.Key() != "rd" {
		t.Error("identity")
	}
	status, file = http.StatusCreated, "rd/add_magnet.json"
	got, err := a.AddMagnet(ctx, magnet(t, h("a"), "Movie.2024.2160p.WEB.H265-GRP"), AddOptions{OnlyIfCached: true})
	if err != nil || got.ID != "RDTORRENTAAAA" || got.Hash != h("a") || got.Ready {
		t.Fatalf("add: %+v %v", got, err)
	}
	reqs := f.requests()
	last := reqs[len(reqs)-1]
	if last.method != http.MethodPost || last.path != "/rest/1.0/torrents/addMagnet" || last.query != "" ||
		last.form.Get("magnet") != "magnet:?xt=urn:btih:"+h("a") || len(last.form) != 1 {
		t.Errorf("request %+v", last)
	}
	for _, tc := range []struct {
		status int
		file   string
		title  string
		want   Code
	}{
		{509, "rd/error_too_many_active.json", "x", CodeTooManyActive},
		{451, "rd/error_infringing.json", "Movie.2024.1080p.WEB-DL.DDP5.1-GRP", CodeBlocked},
		{451, "rd/error_infringing.json", "Movie.2024.1080p.web-dl.h264-GRP", CodeAccountHold},
		{451, "", "Movie.2024.2160p.WEB.H265-GRP", CodeUnknown},
		{429, "rd/error_too_many_requests.json", "x", CodeRateLimited},
		{401, "rd/error_bad_token.json", "x", CodeAuth},
		{403, "rd/error_traffic.json", "x", CodeTrafficExceeded},
		{503, "rd/error_service_unavailable.json", "x", CodeUnavailable},
		{400, "rd/error_torrent_too_big.json", "x", CodeBlocked},
		{418, "", "x", CodeUnknown},
	} {
		status, file = tc.status, tc.file
		// A fresh gateway each time: a 429 backs the target off in the gateway too.
		_, err := f.adder("rd").AddMagnet(ctx, magnet(t, h("a"), tc.title), AddOptions{})
		var e *Error
		if !errors.As(err, &e) || e.Code != tc.want || e.Status != tc.status {
			t.Errorf("%d %s %s: got %v, want %s", tc.status, tc.file, tc.title, err, tc.want)
			continue
		}
		if tc.status == 429 && time.Until(e.RetryAt) < time.Minute {
			t.Errorf("429: RetryAt %v", e.RetryAt)
		}
		if strings.Contains(e.Error(), key) || strings.Contains(e.Error(), "magnet:") {
			t.Errorf("error leaks: %v", e)
		}
	}
	if Code("torrent_not_cached").Content() != true || CodeAccountHold.Content() || CodeTooManyActive.Content() {
		t.Error("Content()")
	}
}

func TestRealDebridSelect(t *testing.T) {
	ctx := context.Background()
	status := http.StatusNoContent
	f := newFakeNet(t)
	f.serve("rd.test", func(w http.ResponseWriter, r *http.Request) { reply(t, w, status, "") })
	a := f.adder("rd")
	if err := a.SelectFiles(ctx, "RDTORRENTAAAA", []string{"1", "3"}); err != nil {
		t.Fatal(err)
	}
	last := f.requests()[0]
	if last.method != http.MethodPost || last.path != "/rest/1.0/torrents/selectFiles/RDTORRENTAAAA" ||
		last.form.Get("files") != "1,3" {
		t.Errorf("request %+v", last)
	}
	status = http.StatusAccepted // already done
	if err := a.SelectFiles(ctx, "RDTORRENTAAAA", []string{"1"}); err != nil {
		t.Error(err)
	}
	status = 509
	if err := a.SelectFiles(ctx, "RDTORRENTAAAA", []string{"1"}); code(err) != CodeTooManyActive {
		t.Errorf("509: %v", err)
	}
	if err := a.SelectFiles(ctx, "RDTORRENTAAAA", []string{"1,2"}); code(err) != CodeInvalid {
		t.Errorf("bad id: %v", err)
	}
	if err := a.SelectFiles(ctx, "", []string{"1"}); code(err) != CodeInvalid {
		t.Errorf("no id: %v", err)
	}
}

func TestAllDebridAdd(t *testing.T) {
	ctx := context.Background()
	var status int
	var file string
	f := newFakeNet(t)
	f.serve("ad.test", func(w http.ResponseWriter, r *http.Request) { reply(t, w, status, file) })
	a := f.adder("ad")
	if a.SelectsFiles() || !errors.Is(a.SelectFiles(ctx, "1", []string{"1"}), ErrNoSelection) {
		t.Error("AllDebrid selects nothing")
	}
	status, file = 200, "ad/upload_ready.json"
	got, err := a.AddMagnet(ctx, magnet(t, h("b"), "x"), AddOptions{})
	if err != nil || got.ID != "412345678" || got.Hash != h("b") || !got.Ready || got.Name == "" {
		t.Fatalf("add: %+v %v", got, err)
	}
	last := f.requests()[0]
	if last.method != http.MethodPost || last.path != "/v4.1/magnet/upload" || last.query != "" ||
		last.form.Get("magnets[]") != "magnet:?xt=urn:btih:"+h("b") {
		t.Errorf("request %+v", last)
	}
	file = "ad/upload_not_ready.json"
	if got, err := a.AddMagnet(ctx, magnet(t, h("b"), "x"), AddOptions{}); err != nil || got.Ready || got.ID != "412345679" {
		t.Errorf("not ready: %+v %v", got, err)
	}
	for _, tc := range []struct {
		status int
		file   string
		want   Code
	}{
		{200, "ad/upload_too_many_active.json", CodeTooManyActive},
		{200, "ad/upload_invalid.json", CodeInvalid},
		{200, "ad/auth_blocked.json", CodeAuth},
		{401, "ad/auth_blocked.json", CodeAuth},
		{200, "ad/must_be_premium.json", CodeAuth},
		{429, "", CodeRateLimited},
		{502, "", CodeUnavailable},
	} {
		status, file = tc.status, tc.file
		_, err := f.adder("ad").AddMagnet(ctx, magnet(t, h("b"), "x"), AddOptions{})
		if code(err) != tc.want {
			t.Errorf("%d %s: got %v, want %s", tc.status, tc.file, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), "example.invalid") {
			t.Errorf("%s: error names the account's address: %v", tc.file, err)
		}
	}
}

func TestTorBoxAdd(t *testing.T) {
	ctx := context.Background()
	var status int
	var file string
	f := newFakeNet(t)
	f.serve("tb.test", func(w http.ResponseWriter, r *http.Request) { reply(t, w, status, file) })
	a := f.adder("tb")
	if a.SelectsFiles() || !errors.Is(a.SelectFiles(ctx, "1", []string{"1"}), ErrNoSelection) {
		t.Error("TorBox selects nothing")
	}
	status, file = 200, "tb/create_cached.json"
	got, err := a.AddMagnet(ctx, magnet(t, h("c"), "x"), AddOptions{OnlyIfCached: true})
	if err != nil || got.ID != "5551234" || !got.Ready || got.Hash != h("c") {
		t.Fatalf("add: %+v %v", got, err)
	}
	last := f.requests()[0]
	if last.method != http.MethodPost || last.path != "/v1/api/torrents/createtorrent" ||
		last.form.Get("magnet") != "magnet:?xt=urn:btih:"+h("c") || last.form.Get("add_only_if_cached") != "true" ||
		last.form.Get("allow_zip") != "false" {
		t.Errorf("request %+v", last)
	}
	file = "tb/create_array.json"
	got, err = a.AddMagnet(ctx, magnet(t, h("c"), "x"), AddOptions{})
	if err != nil || got.ID != "5551235" || got.Ready {
		t.Errorf("array: %+v %v", got, err)
	}
	if reqs := f.requests(); reqs[len(reqs)-1].form.Has("add_only_if_cached") {
		t.Error("add_only_if_cached sent without OnlyIfCached")
	}
	// Never seed (seed=3), on every add, cached only or not.
	for i, r := range f.requests() {
		if r.form.Get("seed") != "3" {
			t.Errorf("add %d: seed %q, want 3 (never seed)", i, r.form.Get("seed"))
		}
	}
	for _, tc := range []struct {
		status int
		file   string
		want   Code
	}{
		{400, "tb/not_cached.json", CodeNotCached},
		{403, "tb/active_limit.json", CodeTooManyActive},
		{403, "tb/monthly_limit.json", CodeTrafficExceeded},
		{400, "tb/too_large.json", CodeBlocked},
		{429, "", CodeRateLimited},
		{500, "", CodeUnavailable},
	} {
		status, file = tc.status, tc.file
		_, err := f.adder("tb").AddMagnet(ctx, magnet(t, h("c"), "x"), AddOptions{OnlyIfCached: true})
		if code(err) != tc.want {
			t.Errorf("%d %s: got %v, want %s", tc.status, tc.file, err, tc.want)
		}
	}
}

// Nothing here can restart or unrestrict, and the only deletes are in delete.go, outside the Adder interface: the
// router's two (DeleteOwnUncachedAdd on Real-Debrid and AllDebrid, DeleteOwnStalledAdd on all three: AllDebrid and
// TorBox for copies to other providers) and the torrent manager's DeleteUserChosen (all three; who may call it is
// boundary_test.go's).
func TestNoDeletes(t *testing.T) {
	bad := regexp.MustCompile(`(?i)delete|remove|unrestrict|restart|requestdl|controltorrent|magnet/delete|links`)
	allowed := map[reflect.Type][]string{
		reflect.TypeFor[*realDebrid](): {"DeleteOwnUncachedAdd", "DeleteOwnStalledAdd", "deleteTorrent", "DeleteUserChosen"},
		reflect.TypeFor[*allDebrid]():  {"DeleteOwnUncachedAdd", "DeleteOwnStalledAdd", "DeleteUserChosen"},
		reflect.TypeFor[*torBox]():     {"DeleteOwnStalledAdd", "DeleteUserChosen"}}
	types := []reflect.Type{reflect.TypeFor[Adder](), reflect.TypeFor[*realDebrid](), reflect.TypeFor[*allDebrid](),
		reflect.TypeFor[*torBox](), reflect.TypeFor[*poster](), reflect.TypeFor[*ledger]()}
	for _, typ := range types {
		for i := range typ.NumMethod() {
			if m := typ.Method(i).Name; bad.MatchString(m) && !slices.Contains(allowed[typ], m) {
				t.Errorf("%v has a method %s", typ, m)
			}
		}
	}
	if _, ok := any(&allDebrid{}).(Deleter); !ok {
		t.Error("AllDebrid cannot delete its own just-made uncached add")
	}
	if _, ok := any(&torBox{}).(Deleter); ok {
		t.Error("TorBox can delete")
	}
	if _, ok := any(&allDebrid{}).(StalledDeleter); !ok {
		t.Error("AllDebrid cannot delete a copy of ours")
	}
	if _, ok := any(&torBox{}).(StalledDeleter); !ok {
		t.Error("TorBox cannot delete a copy of ours")
	}
	paths := regexp.MustCompile(`"/[^"]*"`)
	entries, _ := os.ReadDir(".")
	deletes := map[string]bool{}
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte("http.MethodPut")) || bytes.Contains(b, []byte("http.MethodPatch")) {
			t.Errorf("%s: a PUT or PATCH request", n)
		}
		if bytes.Contains(b, []byte("http.MethodDelete")) && n != "delete.go" {
			t.Errorf("%s: a DELETE request outside delete.go", n)
		}
		for _, p := range paths.FindAll(b, -1) {
			if !bad.Match(p) {
				continue
			}
			if n == "delete.go" && (string(p) == `"/torrents/delete/"` || string(p) == `"/v4/magnet/delete"` ||
				string(p) == `"/api/torrents/controltorrent"`) {
				deletes[string(p)] = true
				continue
			}
			t.Errorf("%s: path %s", n, p)
		}
	}
	if len(deletes) != 3 {
		t.Errorf("delete paths %v", deletes)
	}
	if reflect.TypeFor[Adder]().Implements(reflect.TypeFor[UserDeleter]()) {
		t.Error("Adder includes the user-chosen delete")
	}
}

// DeleteOwnUncachedAdd deletes only an *Added this adder just returned, once, with the full proof; anything else is
// refused before a request.
func TestDeleteOwnUncachedAdd(t *testing.T) {
	ctx := context.Background()
	proof := OwnAdd{NotOnAccountBefore: true, NewEntry: true}
	f := newFakeNet(t)
	rdStatus := http.StatusNoContent
	f.serve("rd.test", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/1.0/torrents/addMagnet" {
			reply(t, w, http.StatusCreated, "rd/add_magnet.json")
			return
		}
		reply(t, w, rdStatus, "")
	})
	adFile := "ad/delete_ok.json"
	f.serve("ad.test", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v4.1/magnet/upload" {
			reply(t, w, 200, "ad/upload_not_ready.json")
			return
		}
		reply(t, w, 200, adFile)
	})
	count := func(method, path string) int {
		n := 0
		for _, r := range f.requests() {
			if r.method == method && r.path == path {
				n++
			}
		}
		return n
	}

	rd := f.adder("rd")
	del, ok := rd.(Deleter)
	if !ok {
		t.Fatal("the RD adder is not a Deleter")
	}
	a, err := rd.AddMagnet(ctx, magnet(t, h("a"), "x"), AddOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Refused before any request: incomplete proof, a copy of the add, an id from elsewhere.
	for name, tc := range map[string]struct {
		a *Added
		p OwnAdd
	}{
		"no proof":   {a, OwnAdd{}},
		"not new":    {a, OwnAdd{NotOnAccountBefore: true}},
		"on account": {a, OwnAdd{NewEntry: true}},
		"copy":       {&Added{ID: a.ID, Hash: a.Hash}, proof},
		"other id":   {&Added{ID: "RDOTHER"}, proof},
		"nil":        {nil, proof},
	} {
		if err := del.DeleteOwnUncachedAdd(ctx, tc.a, tc.p); !errors.Is(err, ErrNotOwnAdd) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := count(http.MethodDelete, "/rest/1.0/torrents/delete/RDTORRENTAAAA"); n != 0 {
		t.Fatalf("%d deletes sent for refused calls", n)
	}
	if err := del.DeleteOwnUncachedAdd(ctx, a, proof); err != nil {
		t.Fatal(err)
	}
	if n := count(http.MethodDelete, "/rest/1.0/torrents/delete/RDTORRENTAAAA"); n != 1 {
		t.Errorf("%d deletes", n)
	}
	// Once only.
	if err := del.DeleteOwnUncachedAdd(ctx, a, proof); !errors.Is(err, ErrNotOwnAdd) {
		t.Errorf("second delete: %v", err)
	}
	// Too late: outside the route attempt's window.
	a2, _ := rd.AddMagnet(ctx, magnet(t, h("a"), "x"), AddOptions{})
	rd.(*realDebrid).own.now = func() time.Time { return time.Now().Add(ownAddWindow + time.Second) }
	if err := del.DeleteOwnUncachedAdd(ctx, a2, proof); !errors.Is(err, ErrNotOwnAdd) {
		t.Errorf("late delete: %v", err)
	}
	rd.(*realDebrid).own.now = time.Now
	// A provider refusal is an *Error, and the key never leaks.
	a3, _ := rd.AddMagnet(ctx, magnet(t, h("a"), "x"), AddOptions{})
	rdStatus = http.StatusUnauthorized
	if err := del.DeleteOwnUncachedAdd(ctx, a3, proof); code(err) != CodeAuth || strings.Contains(err.Error(), key) {
		t.Errorf("401: %v", err)
	}

	ad := f.adder("ad")
	adDel := ad.(Deleter)
	b, err := ad.AddMagnet(ctx, magnet(t, h("b"), "x"), AddOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := adDel.DeleteOwnUncachedAdd(ctx, &Added{ID: b.ID}, proof); !errors.Is(err, ErrNotOwnAdd) {
		t.Errorf("copy: %v", err)
	}
	if err := adDel.DeleteOwnUncachedAdd(ctx, b, OwnAdd{NotOnAccountBefore: true}); !errors.Is(err, ErrNotOwnAdd) {
		t.Errorf("existing id: %v", err)
	}
	if err := adDel.DeleteOwnUncachedAdd(ctx, b, proof); err != nil {
		t.Fatal(err)
	}
	var last request
	for _, r := range f.requests() {
		if r.host == "ad.test" {
			last = r
		}
	}
	if last.method != http.MethodPost || last.path != "/v4/magnet/delete" || last.form.Get("id") != b.ID ||
		last.query != "" || len(last.form) != 1 {
		t.Errorf("request %+v", last)
	}
	b2, _ := ad.AddMagnet(ctx, magnet(t, h("b"), "x"), AddOptions{})
	adFile = "ad/delete_invalid_id.json"
	if err := adDel.DeleteOwnUncachedAdd(ctx, b2, proof); err == nil {
		t.Error("an AllDebrid error answer was taken as deleted")
	}
	// A stalled add of an earlier route: RD only, with the full proof.
	sd := rd.(StalledDeleter)
	rdStatus = http.StatusNoContent
	for _, p := range []OwnAdd{{}, {NotOnAccountBefore: true}, {NewEntry: true}} {
		if err := sd.DeleteOwnStalledAdd(ctx, "RDOLD", p); !errors.Is(err, ErrNotOwnAdd) {
			t.Errorf("stalled %+v: %v", p, err)
		}
	}
	if count(http.MethodDelete, "/rest/1.0/torrents/delete/RDOLD") != 0 {
		t.Fatal("a refused stalled delete was sent")
	}
	if err := sd.DeleteOwnStalledAdd(ctx, "RDOLD", proof); err != nil ||
		count(http.MethodDelete, "/rest/1.0/torrents/delete/RDOLD") != 1 {
		t.Errorf("stalled delete: %v", err)
	}
	// A copy of ours on AllDebrid (router copy.go): the same request, with the full proof only.
	adSD := ad.(StalledDeleter)
	adFile = "ad/delete_ok.json"
	if err := adSD.DeleteOwnStalledAdd(ctx, "77", OwnAdd{NotOnAccountBefore: true}); !errors.Is(err, ErrNotOwnAdd) {
		t.Errorf("AD stalled without proof: %v", err)
	}
	if err := adSD.DeleteOwnStalledAdd(ctx, "77", proof); err != nil {
		t.Errorf("AD stalled: %v", err)
	}
	var adLast request
	for _, r := range f.requests() {
		if r.host == "ad.test" {
			adLast = r
		}
	}
	if adLast.path != "/v4/magnet/delete" || adLast.form.Get("id") != "77" {
		t.Errorf("AD stalled request %+v", adLast)
	}
	// The two adders' ledgers are separate: an RD add can't be deleted through AD.
	a4, _ := rd.AddMagnet(ctx, magnet(t, h("a"), "x"), AddOptions{})
	if err := adDel.DeleteOwnUncachedAdd(ctx, a4, proof); !errors.Is(err, ErrNotOwnAdd) {
		t.Errorf("cross-adder: %v", err)
	}
}

// TestErrorEnvelopeIsRefusal: an AllDebrid or TorBox error envelope with a code is a refusal (Refused),
// even for a code we do not know and at HTTP 200, so the router does not take it as an add that may have landed.
// TorBox's own server errors and answers without a code stay open.
func TestErrorEnvelopeIsRefusal(t *testing.T) {
	ctx := context.Background()
	var status int
	var file string
	f := newFakeNet(t)
	f.serve("ad.test", func(w http.ResponseWriter, r *http.Request) { reply(t, w, status, file) })
	f.serve("tb.test", func(w http.ResponseWriter, r *http.Request) { reply(t, w, status, file) })
	for _, tc := range []struct {
		key, file string
		status    int
		refused   bool
	}{
		{"ad", "ad/upload_unknown_code.json", 200, true},
		{"ad", "ad/upload_invalid.json", 200, true},
		{"ad", "", 200, false},
		{"tb", "tb/unknown_code.json", 200, true},
		{"tb", "tb/too_large.json", 400, true},
		{"tb", "tb/database_error.json", 200, false},
		{"tb", "", 502, false},
	} {
		status, file = tc.status, tc.file
		_, err := f.adder(tc.key).AddMagnet(ctx, magnet(t, h("b"), "x"), AddOptions{})
		var e *Error
		if !errors.As(err, &e) || e.Refused != tc.refused {
			t.Errorf("%s %d %s: %v refused %v, want %v", tc.key, tc.status, tc.file, err, e != nil && e.Refused,
				tc.refused)
		}
	}
}

func TestAllDebridAccountLevelRefusals(t *testing.T) {
	for _, tc := range []struct {
		code string
		want Code
	}{
		{"NO_SERVER", CodeAuth}, {"GENERIC", CodeUnavailable}, {"MAINTENANCE", CodeUnavailable},
		{"AUTH_BAD_APIKEY", CodeAuth}, {"MAGNET_NO_SERVER", CodeAuth},
	} {
		t.Run(tc.code, func(t *testing.T) {
			f := newFakeNet(t)
			f.serve("ad.test", func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "error": map[string]string{"code": tc.code}})
			})
			_, err := f.adder("ad").AddMagnet(context.Background(), magnet(t, h("b"), "x"), AddOptions{})
			if code(err) != tc.want {
				t.Fatalf("%s: %v, want %s", tc.code, err, tc.want)
			}
		})
	}
}
