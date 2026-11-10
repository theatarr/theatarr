package library

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/library/decypharrtest"
	"github.com/theatarr/theatarr/internal/policy"
)

// decyDoer sends requests straight to the fake, honouring Target.NoFollow as the gateway does, and counts them.
type decyDoer struct {
	mu      sync.Mutex
	n       int
	targets []gateway.Target
}

func (d *decyDoer) Do(ctx context.Context, t gateway.Target, req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	d.n++
	d.targets = append(d.targets, t)
	d.mu.Unlock()
	c := &http.Client{}
	if t.NoFollow {
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return c.Do(req.WithContext(ctx))
}

func (d *decyDoer) count() int { d.mu.Lock(); defer d.mu.Unlock(); return d.n }

// decyEnv is a client on a fake decypharr holding the fixture, with a clock the test moves.
type decyEnv struct {
	srv  *decypharrtest.Server
	doer *decyDoer
	d    *Decypharr
	now  time.Time
}

func newDecyEnv(t *testing.T) *decyEnv {
	t.Helper()
	e := &decyEnv{srv: decypharrtest.New(t), doer: &decyDoer{}, now: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
	e.srv.Set(func(s *decypharrtest.Server) { s.Providers, s.Torrents = decypharrtest.Fixture() })
	e.d = &Decypharr{Config: DecypharrConfig{URL: e.srv.URL(), Interval: time.Minute}, GW: e.doer,
		Now: func() time.Time { return e.now }}
	return e
}

// TestDecypharrAllowlist: only PROPFIND, GET and HEAD, and only on the listed routes; everything else is refused
// before a request exists.
func TestDecypharrAllowlist(t *testing.T) {
	t.Parallel()
	e := newDecyEnv(t)
	ctx := context.Background()
	refused := []struct {
		method string
		route  decypharrRoute
		segs   []string
	}{
		// Methods that change decypharr.
		{http.MethodDelete, decypharrDAV, []string{"__all__", decypharrtest.SharedName}},
		{"MOVE", decypharrDAV, []string{"__all__", decypharrtest.SharedName}},
		{"COPY", decypharrDAV, []string{"__all__", decypharrtest.SharedName}},
		{http.MethodPut, decypharrDAV, []string{"__all__", "x", "y.mkv"}},
		{"MKCOL", decypharrDAV, []string{"__all__", "x"}},
		{"PROPPATCH", decypharrDAV, []string{"__all__", "x"}},
		{http.MethodPost, decypharrBrowse, nil},
		{http.MethodDelete, decypharrBrowse, []string{"torrents", decypharrtest.SharedFirst}},
		{http.MethodDelete, decypharrBrowse, []string{"torrents", "batch"}},
		{http.MethodPost, decypharrTorrentFiles, nil},
		{http.MethodOptions, decypharrDAV, nil},
		// The download route, at any depth, and the groups that are not read.
		{http.MethodGet, decypharrBrowse, []string{"download"}},
		{http.MethodGet, decypharrBrowse, []string{"download", decypharrtest.SharedName}},
		{http.MethodGet, decypharrBrowse, []string{"download", decypharrtest.SharedName, "a.mkv"}},
		{http.MethodGet, decypharrBrowse, []string{"__bad__"}},
		{http.MethodGet, decypharrBrowse, []string{"nzbs"}},
		{http.MethodGet, decypharrBrowse, []string{"__all__", "a", "b"}},
		{http.MethodHead, decypharrBrowse, []string{"__all__"}},
		{methodPropfind, decypharrBrowse, []string{"__all__"}},
		// The qBittorrent API has one route here; its other GETs change state.
		{http.MethodGet, decypharrTorrentFiles, []string{"pause"}},
		{http.MethodGet, decypharrTorrentFiles, []string{"..", "..", "app", "shutdown"}},
		{http.MethodHead, decypharrTorrentFiles, nil},
		// WebDAV: a folder is never sent GET or HEAD, a file is never listed, the stream alias is not used.
		{http.MethodGet, decypharrDAV, nil},
		{http.MethodGet, decypharrDAV, []string{"__all__"}},
		{http.MethodGet, decypharrDAV, []string{"__all__", decypharrtest.SharedName}},
		{http.MethodHead, decypharrDAV, []string{"__all__", decypharrtest.SharedName}},
		{http.MethodGet, decypharrDAV, []string{"stream", "__all__", "x"}},
		{http.MethodGet, decypharrDAV, []string{"stream", "__all__", "x", "y.mkv"}},
		{methodPropfind, decypharrDAV, []string{"__all__", "x", "y.mkv"}},
		// Path elements that would reach another route.
		{http.MethodGet, decypharrBrowse, []string{"__all__", "../download/x"}},
		{http.MethodGet, decypharrBrowse, []string{"..", "v2"}},
		{http.MethodGet, decypharrBrowse, []string{"__all__", ""}},
		{methodPropfind, decypharrDAV, []string{".", "x"}},
		{methodPropfind, decypharrDAV, []string{"a/b"}},
		{http.MethodGet, decypharrRoute(99), nil},
	}
	for _, c := range refused {
		resp, err := e.d.do(ctx, c.method, c.route, c.segs, nil)
		if resp != nil {
			resp.Body.Close()
		}
		if !errors.Is(err, ErrDecypharrRefused) {
			t.Errorf("%s %d %q: got %v, want a refusal", c.method, c.route, c.segs, err)
		}
	}
	if n, reqs := e.doer.count(), e.srv.Requests(); n != 0 || len(reqs) != 0 {
		t.Fatalf("refused requests were sent: %d through the gateway, server saw %q", n, reqs)
	}
	allowed := []struct {
		method string
		route  decypharrRoute
		segs   []string
	}{
		{http.MethodGet, decypharrBrowse, nil},
		{http.MethodGet, decypharrBrowse, []string{"__all__"}},
		{http.MethodGet, decypharrBrowse, []string{"torrents"}},
		{http.MethodGet, decypharrBrowse, []string{"torrents", decypharrtest.SharedName}},
		{http.MethodGet, decypharrBrowse, []string{"realdebrid"}},
		{http.MethodGet, decypharrTorrentFiles, nil},
		{methodPropfind, decypharrDAV, nil},
		{methodPropfind, decypharrDAV, []string{"__all__"}},
		{methodPropfind, decypharrDAV, []string{"__all__", decypharrtest.SharedName}},
		{http.MethodGet, decypharrDAV, []string{"__all__", decypharrtest.SharedName, "a.mkv"}},
		{http.MethodHead, decypharrDAV, []string{"__all__", decypharrtest.SharedName, "a.mkv"}},
	}
	for _, c := range allowed {
		if err := decypharrAllowed(c.method, c.route, c.segs); err != nil {
			t.Errorf("%s %d %q: %v", c.method, c.route, c.segs, err)
		}
	}
	// One place builds requests and one sends them, so nothing gets round the list.
	src, err := os.ReadFile("decypharrapi.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"http.NewRequest", ".Do("} {
		if n := strings.Count(string(src), s); n != 1 {
			t.Errorf("decypharrapi.go has %d of %q, want 1 (in do)", n, s)
		}
	}
}

// TestDecypharrListings: paging at 100, a merged folder (the newer torrent's file wins), flat names, names that
// need escaping, a missing folder over both the browse API and PROPFIND, and the per-hash file list.
func TestDecypharrListings(t *testing.T) {
	t.Parallel()
	e := newDecyEnv(t)
	ctx := context.Background()

	groups, err := e.d.Groups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, g := range groups {
		kinds[g.Name] = g.Kind
	}
	if kinds["__all__"] != "system" || kinds["realdebrid"] != "provider" || kinds["torbox"] != "provider" {
		t.Fatalf("groups: %v", kinds)
	}

	// 10 named torrents, two of them sharing a name with another, plus the generated ones: three pages.
	e.srv.Reset()
	all, found, err := e.d.GroupEntries(ctx, "__all__")
	want := 8 + decypharrtest.FixtureSeries
	if err != nil || !found || len(all) != want {
		t.Fatalf("__all__: %d entries, found %v, err %v; want %d", len(all), found, err, want)
	}
	reqs := e.srv.Requests()
	if len(reqs) != 3 || reqs[0] != "GET /api/browse/__all__?limit=100&page=1" || reqs[2] != "GET /api/browse/__all__?limit=100&page=3" {
		t.Fatalf("paging: %q", reqs)
	}
	names := map[string]DecypharrEntry{}
	for _, en := range all {
		if _, dup := names[en.Name]; dup {
			t.Errorf("%q listed twice", en.Name)
		}
		names[en.Name] = en
	}
	if en := names[decypharrtest.SharedName]; en.Hash != decypharrtest.SharedFirst || !en.IsDir || en.Kind != "entry" {
		t.Errorf("shared name: %+v", en)
	}
	if en := names[decypharrtest.OddName]; en.Hash != decypharrtest.OddHash || en.Provider != "torbox" {
		t.Errorf("odd name: %+v", en)
	}
	if _, found, err := e.d.GroupEntries(ctx, "no-such-debrid"); err != nil || found {
		t.Errorf("unknown group: found %v, err %v", found, err)
	}

	// The merged folder: the newer torrent's copy of the shared file, and the older torrent's other file.
	merged := []DecypharrFile{{Name: "Marrow.Lantern.2014.1080p.BluRay.x264-FERNWOOD.mkv", Size: 8_100_000_777},
		{Name: "Marrow.Lantern.2014.Extras.mkv", Size: 700_000_001}}
	odd := []DecypharrFile{{Name: "Glass & Thistle #2 100% <Remux>.mkv", Size: 31_000_000_005}}
	for name, list := range map[string]func(context.Context, string, string) ([]DecypharrFile, bool, error){
		"browse": e.d.FolderFiles, "propfind": e.d.PropfindFolder} {
		files, found, err := list(ctx, "__all__", decypharrtest.SharedName)
		if err != nil || !found || len(files) != 2 || files[0] != merged[0] || files[1] != merged[1] {
			t.Errorf("%s merged folder: %+v, found %v, err %v", name, files, found, err)
		}
		files, found, err = list(ctx, "torbox", decypharrtest.OddName)
		if err != nil || !found || len(files) != 1 || files[0] != odd[0] {
			t.Errorf("%s odd name: %+v, found %v, err %v", name, files, found, err)
		}
		files, found, err = list(ctx, "__all__", "Nothing.By.This.Name.2001")
		if err != nil || found || files != nil {
			t.Errorf("%s missing folder: %+v, found %v, err %v", name, files, found, err)
		}
	}
	// The missing folder over WebDAV was a 207, not an error status.
	if n := e.srv.Count("PROPFIND", "/webdav/__all__/Nothing.By.This.Name.2001"); n != 1 {
		t.Errorf("PROPFIND of the missing folder: %d requests", n)
	}

	files, found, err := e.d.TorrentFiles(ctx, strings.ToUpper(decypharrtest.QueuedHash))
	if err != nil || !found || len(files) != 2 || files[1].Index != 1 || files[1].Size != 2_200_000_008 ||
		files[1].Name != "Pale.Ferry.S02E02.1080p.AMZN.WEB-DL.DDP2.0.H.264-TILLER.mkv" {
		t.Errorf("queued torrent's files: %+v, found %v, err %v", files, found, err)
	}
	// Not queued (any more): gone, not an error.
	if files, found, err := e.d.TorrentFiles(ctx, decypharrtest.SharedFirst); err != nil || found || files != nil {
		t.Errorf("unqueued torrent's files: %+v, found %v, err %v", files, found, err)
	}
	e.srv.Set(func(s *decypharrtest.Server) { s.Fail = http.StatusInternalServerError })
	if _, found, err := e.d.TorrentFiles(ctx, decypharrtest.QueuedHash); err == nil || found {
		t.Errorf("failing torrents/files: found %v, err %v", found, err)
	}
	for _, tg := range e.doer.targets {
		if tg.Class != gateway.ClassDecypharr || tg.Key != gateway.DecypharrKey || !tg.NoFollow {
			t.Fatalf("target %+v", tg)
		}
	}
}

// TestDecypharrAuth: the Bearer token goes to the API only and Basic to WebDAV only, each when set; a 401 is its
// own error. The fake fails the test when credentials cross.
func TestDecypharrAuth(t *testing.T) {
	t.Parallel()
	e := newDecyEnv(t)
	ctx := context.Background()
	e.srv.Set(func(s *decypharrtest.Server) {
		s.Token, s.DAVUser, s.DAVPassword = "made-up-token", "dav", "made-up-pass"
	})

	if _, err := e.d.Groups(ctx); !errors.Is(err, ErrDecypharrAuth) {
		t.Errorf("browse without a token: %v", err)
	}
	if _, _, err := e.d.TorrentFiles(ctx, decypharrtest.QueuedHash); !errors.Is(err, ErrDecypharrAuth) {
		t.Errorf("torrents/files without a token: %v", err)
	}
	if _, _, err := e.d.PropfindFolder(ctx, "__all__", decypharrtest.SharedName); !errors.Is(err, ErrDecypharrAuth) {
		t.Errorf("PROPFIND without credentials: %v", err)
	}
	if _, err := e.d.Index(ctx); !errors.Is(err, ErrDecypharrAuth) || !errors.Is(err, ErrDecypharrIndexUnknown) {
		t.Errorf("index without a token: %v", err)
	}

	e.d.Config.Token, e.d.Config.Username, e.d.Config.Password = "made-up-token", "dav", "made-up-pass"
	if _, err := e.d.Groups(ctx); err != nil {
		t.Errorf("browse with the token: %v", err)
	}
	if _, found, err := e.d.TorrentFiles(ctx, decypharrtest.QueuedHash); err != nil || !found {
		t.Errorf("torrents/files with the token: found %v, err %v", found, err)
	}
	if _, found, err := e.d.PropfindFolder(ctx, "__all__", decypharrtest.SharedName); err != nil || !found {
		t.Errorf("PROPFIND with credentials: found %v, err %v", found, err)
	}

	// Auth off on decypharr: configured credentials are still accepted.
	e.srv.Set(func(s *decypharrtest.Server) { s.Token, s.DAVUser = "", "" })
	if _, err := e.d.Groups(ctx); err != nil {
		t.Errorf("browse, auth off: %v", err)
	}
	if _, found, err := e.d.PropfindFolder(ctx, "__all__", decypharrtest.SharedName); err != nil || !found {
		t.Errorf("PROPFIND, auth off: found %v, err %v", found, err)
	}
}

// TestDecypharrRedirect: a 3xx is refused by the client and never followed, whatever target it was given.
func TestDecypharrRedirect(t *testing.T) {
	t.Parallel()
	e := newDecyEnv(t)
	ctx := context.Background()
	e.srv.Set(func(s *decypharrtest.Server) { s.Redirect = "/login" })
	e.d.Target = gateway.Target{Class: gateway.ClassDecypharr, Key: gateway.DecypharrKey} // NoFollow not set
	if _, err := e.d.Groups(ctx); !errors.Is(err, ErrDecypharrRedirect) {
		t.Errorf("browse: %v", err)
	}
	if _, _, err := e.d.TorrentFiles(ctx, decypharrtest.QueuedHash); !errors.Is(err, ErrDecypharrRedirect) {
		t.Errorf("torrents/files: %v", err)
	}
	if _, _, err := e.d.PropfindFolder(ctx, "__all__", decypharrtest.SharedName); !errors.Is(err, ErrDecypharrRedirect) {
		t.Errorf("PROPFIND: %v", err)
	}
	if _, err := e.d.Index(ctx); !errors.Is(err, ErrDecypharrRedirect) || !errors.Is(err, ErrDecypharrIndexUnknown) {
		t.Errorf("index: %v", err)
	}
	if n := e.srv.Count(http.MethodGet, "/login") + e.srv.Count(methodPropfind, "/login"); n != 0 {
		t.Errorf("the redirect was followed %d times", n)
	}
}

// TestDecypharrReady: WebDAV's 503 is "loading", reused briefly; anything else is no answer.
func TestDecypharrReady(t *testing.T) {
	t.Parallel()
	e := newDecyEnv(t)
	ctx := context.Background()
	e.srv.Set(func(s *decypharrtest.Server) { s.Loading = true })
	if err := e.d.Ready(ctx); !errors.Is(err, ErrDecypharrLoading) {
		t.Fatalf("loading: %v", err)
	}
	if _, _, err := e.d.PropfindFolder(ctx, "__all__", decypharrtest.SharedName); !errors.Is(err, ErrDecypharrLoading) {
		t.Errorf("PROPFIND while loading: %v", err)
	}
	// The browse API answers while WebDAV loads.
	if _, err := e.d.Groups(ctx); err != nil {
		t.Errorf("browse while loading: %v", err)
	}
	e.srv.Set(func(s *decypharrtest.Server) { s.Loading = false })
	if err := e.d.Ready(ctx); !errors.Is(err, ErrDecypharrLoading) {
		t.Errorf("the loading answer is reused for a moment: %v", err)
	}
	e.now = e.now.Add(readyLoadingTTL)
	if err := e.d.Ready(ctx); err != nil {
		t.Errorf("loaded: %v", err)
	}
	// Refused credentials are not "loading": the mount decides.
	e.srv.Set(func(s *decypharrtest.Server) { s.DAVUser = "dav" })
	e.now = e.now.Add(time.Minute)
	if err := e.d.Ready(ctx); err != nil {
		t.Errorf("401: %v", err)
	}
	var none *Decypharr
	if err := none.Ready(ctx); err != nil {
		t.Errorf("no client: %v", err)
	}
}

// TestDecypharrIndex: hash and name lookups from the browse API, the unconditional maximum age, the group totals
// as an early hint only, and "unknown" kept apart from "not listed".
func TestDecypharrIndex(t *testing.T) {
	t.Parallel()
	e := newDecyEnv(t)
	ctx := context.Background()

	ix, err := e.d.Index(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Root, three pages of __all__, and the two debrids' folders.
	built := len(e.srv.Requests())
	if e.srv.Count(http.MethodGet, "/api/browse/") != 1 || e.srv.Count(http.MethodGet, "/api/browse/__all__") != 3 ||
		e.srv.Count(http.MethodGet, "/api/browse/realdebrid") == 0 || e.srv.Count(http.MethodGet, "/api/browse/torbox") == 0 ||
		e.srv.Count(http.MethodGet, "/api/browse/torrents") != 0 {
		t.Fatalf("index requests: %q", e.srv.Requests())
	}
	names := func(ix *DecypharrIndex, hash string) string {
		n, known := ix.Names(hash)
		if !known {
			return "unknown"
		}
		return strings.Join(n, "|")
	}
	hashes := func(ix *DecypharrIndex, name string) string {
		h, known := ix.Hashes(name)
		if !known {
			return "unknown"
		}
		return strings.Join(h, "|")
	}
	if got := names(ix, strings.ToUpper(decypharrtest.SharedFirst)); got != decypharrtest.SharedName {
		t.Errorf("names of the first sharing hash: %q", got)
	}
	// The second debrid's folder shows the hash __all__ hides.
	if got := names(ix, decypharrtest.SharedSecond); got != decypharrtest.SharedName {
		t.Errorf("names of the second sharing hash: %q", got)
	}
	if got := hashes(ix, decypharrtest.SharedName); got != decypharrtest.SharedFirst+"|"+decypharrtest.SharedSecond {
		t.Errorf("hashes of the shared name: %q", got)
	}
	// Two torrents of one name on one debrid: decypharr lists one hash, so the other is "not listed", and known.
	if got := hashes(ix, decypharrtest.HiddenName); got != decypharrtest.HiddenFirst {
		t.Errorf("hashes of the hidden name: %q", got)
	}
	if got := names(ix, decypharrtest.HiddenSecond); got != "" {
		t.Errorf("names of the hidden hash: %q", got)
	}
	if got := names(ix, decypharrtest.OddHash); got != decypharrtest.OddName {
		t.Errorf("exact odd name: %q", got)
	}
	if got := names(ix, decypharrtest.BareHash); got != decypharrtest.BareHash {
		t.Errorf("bare hash name: %q", got)
	}
	if got := hashes(ix, "Nothing.By.This.Name.2001"); got != "" {
		t.Errorf("unlisted name: %q", got)
	}
	// 10 named torrents less the hidden one, plus the generated ones (the usenet entry has a hash too).
	if want := 9 + decypharrtest.FixtureSeries; ix.Len() != want {
		t.Errorf("index holds %d hashes, want %d", ix.Len(), want)
	}
	var none *DecypharrIndex
	if names(none, decypharrtest.SharedFirst) != "unknown" || hashes(none, decypharrtest.SharedName) != "unknown" {
		t.Error("a nil index must answer unknown")
	}

	// Reused within the interval; FreshIndex does not even compare totals for the first seconds.
	e.now = e.now.Add(4 * time.Second)
	if again, err := e.d.Index(ctx); err != nil || again != ix {
		t.Fatalf("index within the interval: %v", err)
	}
	if again, err := e.d.FreshIndex(ctx); err != nil || again != ix || len(e.srv.Requests()) != built {
		t.Fatalf("fresh index at once: err %v, %d new requests", err, len(e.srv.Requests())-built)
	}
	// Later it compares totals: the root and one entry per group, and no new reading when they are the same.
	e.now = e.now.Add(2 * time.Second)
	e.srv.Reset()
	if again, err := e.d.FreshIndex(ctx); err != nil || again != ix {
		t.Fatalf("fresh index, nothing changed: %v", err)
	}
	if reqs := e.srv.Requests(); len(reqs) != 4 || reqs[1] != "GET /api/browse/__all__?limit=1&page=1" {
		t.Fatalf("change check: %q", reqs)
	}
	// A new torrent changes a total: read again at once.
	newHash := decypharrtest.Hash(200)
	e.srv.Set(func(s *decypharrtest.Server) {
		s.Torrents = append(s.Torrents, decypharrtest.Torrent{Hash: newHash, Name: "Amber.Switchback.2023.1080p.WEB.H264-ROWAN",
			Provider: "torbox", AddedOn: e.now, Files: []decypharrtest.File{{Name: "Amber.Switchback.2023.mkv", Size: 5}}})
	})
	e.now = e.now.Add(decypharrHintFloor)
	ix2, err := e.d.FreshIndex(ctx)
	if err != nil || ix2 == ix || names(ix2, newHash) != "Amber.Switchback.2023.1080p.WEB.H264-ROWAN" {
		t.Fatalf("fresh index after an add: err %v, names %q", err, names(ix2, newHash))
	}
	// One torrent replaced by another leaves every total as it was: the hint misses it...
	swapped := decypharrtest.Hash(201)
	e.srv.Set(func(s *decypharrtest.Server) {
		last := &s.Torrents[len(s.Torrents)-1]
		last.Hash, last.Name = swapped, "Slate.Causeway.2022.1080p.WEB.H264-ROWAN"
	})
	e.now = e.now.Add(decypharrHintFloor)
	if ix3, err := e.d.FreshIndex(ctx); err != nil || ix3 != ix2 {
		t.Fatalf("fresh index, equal totals: %v", err)
	}
	// ...so the interval is the maximum age whatever the totals say.
	e.now = ix2.At.Add(time.Minute)
	ix4, err := e.d.FreshIndex(ctx)
	if err != nil || ix4 == ix2 || names(ix4, swapped) != "Slate.Causeway.2022.1080p.WEB.H264-ROWAN" || names(ix4, newHash) != "" {
		t.Fatalf("index after the interval: err %v, names %q", err, names(ix4, swapped))
	}

	// decypharr stops answering: within the interval the reading stands, past it the index is unknown, not stale.
	e.srv.Set(func(s *decypharrtest.Server) { s.Fail = http.StatusInternalServerError })
	e.now = e.now.Add(30 * time.Second)
	if again, err := e.d.FreshIndex(ctx); err != nil || again != ix4 {
		t.Fatalf("failing change check within the interval: %v", err)
	}
	e.now = ix4.At.Add(time.Minute)
	gone, err := e.d.Index(ctx)
	if !errors.Is(err, ErrDecypharrIndexUnknown) || gone != nil || names(gone, decypharrtest.SharedFirst) != "unknown" {
		t.Fatalf("index past its age with decypharr failing: %v, %v", gone, err)
	}
	// A failed reading is not retried at once.
	e.srv.Reset()
	if _, err := e.d.FreshIndex(ctx); !errors.Is(err, ErrDecypharrIndexUnknown) || len(e.srv.Requests()) != 0 {
		t.Fatalf("retry at once: err %v, requests %q", err, e.srv.Requests())
	}
	e.srv.Set(func(s *decypharrtest.Server) { s.Fail = 0 })
	e.now = e.now.Add(decypharrRetryFloor)
	if back, err := e.d.Index(ctx); err != nil || names(back, decypharrtest.SharedFirst) != decypharrtest.SharedName {
		t.Fatalf("index after decypharr came back: %v", err)
	}
}

// TestDecypharrThroughGateway: the client behind a real gateway (the zurg API target stands in until the policy
// block registers decypharr's own), with a url_base: PROPFIND passes, a redirect comes back unfollowed, a 404 is
// "gone".
func TestDecypharrThroughGateway(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := decypharrtest.New(t)
	srv.Set(func(s *decypharrtest.Server) {
		s.Base = "/decy"
		s.Providers, s.Torrents = decypharrtest.Fixture()
		s.Token, s.DAVUser, s.DAVPassword = "made-up-token", "dav", "made-up-pass"
	})
	p := &policy.Policy{
		Budgets: map[string]*policy.Budget{"zurg-api": {Requests: 6000, Per: policy.Duration(time.Minute)}},
		Library: &policy.Library{Mount: policy.LibraryMount{ZurgAPI: srv.URL(), ZurgAPIBudget: "zurg-api"}},
	}
	gw, err := gateway.New(ctx, p, gateway.Options{})
	if err != nil {
		t.Fatal(err)
	}
	d := &Decypharr{Config: DecypharrConfig{URL: srv.URL() + "/", Token: "made-up-token", Username: "dav",
		Password: "made-up-pass"}, GW: gw, Target: ZurgAPITarget}

	ix, err := d.Index(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if h, known := ix.Hashes(decypharrtest.SharedName); !known || len(h) != 2 {
		t.Errorf("hashes of the shared name: %q, known %v", h, known)
	}
	if files, found, err := d.PropfindFolder(ctx, "torbox", decypharrtest.OddName); err != nil || !found || len(files) != 1 {
		t.Errorf("PROPFIND: %+v, found %v, err %v", files, found, err)
	}
	if _, found, err := d.PropfindFolder(ctx, "__all__", "Nothing.By.This.Name.2001"); err != nil || found {
		t.Errorf("PROPFIND of a missing folder: found %v, err %v", found, err)
	}
	if _, found, err := d.TorrentFiles(ctx, decypharrtest.SharedFirst); err != nil || found {
		t.Errorf("torrents/files of an unqueued torrent: found %v, err %v", found, err)
	}
	if err := d.Ready(ctx); err != nil {
		t.Errorf("ready: %v", err)
	}
	srv.Set(func(s *decypharrtest.Server) { s.Redirect = "/login" })
	if _, err := d.Groups(ctx); !errors.Is(err, ErrDecypharrRedirect) {
		t.Errorf("redirect through the gateway: %v", err)
	}
	// WebDAV's 503 carries Retry-After, which the gateway takes as a rate limit: the loading answer still comes
	// through. (The gateway then holds the next request to decypharr back for those seconds; not waited for here.)
	srv.Set(func(s *decypharrtest.Server) { s.Redirect, s.Loading = "", true })
	if _, _, err := d.PropfindFolder(ctx, "__all__", decypharrtest.SharedName); !errors.Is(err, ErrDecypharrLoading) {
		t.Errorf("PROPFIND while loading, through the gateway: %v", err)
	}
}
