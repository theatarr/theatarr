package router

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
)

// Cached-only on Real-Debrid and AllDebrid, end to end: the real adders and read-only clients, through a real
// gateway, against httptest fakes of both providers that behave as their docs and decypharr describe. TorBox stays
// the logging fake.

const debridKey = "FIXTURE-DEBRID-KEY"

// fakeDebrid is one RD and one AD account. cached says which hashes a provider holds at once.
type fakeDebrid struct {
	t      *testing.T
	mu     sync.Mutex
	hosts  map[string]string
	cached map[string]bool // "rd:<hash>", "ad:<hash>"
	rd     map[string]*fakeRD
	ad     map[int]*fakeAD
	nextID int
	// adListFails makes AllDebrid's magnet list (no id) answer 500.
	adListFails bool
	// adNoHashInList leaves hashes out of AllDebrid's list (status without id).
	adNoHashInList bool
	// adDeleteFails refuses AllDebrid deletes without removing the magnet.
	adDeleteFails bool
	// adMaint, when set, answers every AllDebrid call with the MAINTENANCE envelope on this HTTP status.
	adMaint int
	// rdStuck keeps new Real-Debrid torrents in magnet_conversion (no peers).
	rdStuck bool
	// rdFiles replaces the one file of every Real-Debrid torrent; rdCachedSel, when set, says which selections
	// ("<hash>|<file ids as sent>") are cached, as Real-Debrid caches per selected set.
	rdFiles     []rdFile
	rdCachedSel map[string]bool
	// rdInfoFails makes the next that many Real-Debrid status reads answer 503.
	rdInfoFails int
	reqs        []string // "host METHOD path"
}

type fakeRD struct {
	hash, status string
	selected     bool
	sel          string // the file ids selected, as sent
}

type rdFile struct {
	id    int
	path  string
	bytes int64
}

type fakeAD struct {
	hash string
	code int
}

func newFakeDebrid(t *testing.T) *fakeDebrid {
	f := &fakeDebrid{t: t, hosts: map[string]string{}, cached: map[string]bool{}, rd: map[string]*fakeRD{},
		ad: map[int]*fakeAD{}, nextID: 1000}
	f.serve("rd.test", f.realDebrid)
	f.serve("ad.test", f.allDebrid)
	return f
}

func (f *fakeDebrid) serve(name string, h http.HandlerFunc) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.reqs = append(f.reqs, name+" "+r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+debridKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		f.mu.Lock()
		defer f.mu.Unlock()
		h(w, r)
	}))
	f.t.Cleanup(srv.Close)
	f.hosts[name+":80"] = srv.Listener.Addr().String()
}

func (f *fakeDebrid) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	f.mu.Lock()
	real, ok := f.hosts[addr]
	f.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("dial %s: not in the test network", addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, real)
}

func magnetHash(v string) string {
	return strings.ToLower(strings.TrimPrefix(strings.SplitN(v, "&", 2)[0], "magnet:?xt=urn:btih:"))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// realDebrid: addMagnet always makes a new torrent, waiting for a file selection; the selection makes it downloaded
// when cached, downloading otherwise.
func (f *fakeDebrid) realDebrid(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/rest/1.0")
	switch {
	case r.Method == http.MethodPost && p == "/torrents/addMagnet":
		f.nextID++
		id := "RD" + strconv.Itoa(f.nextID)
		f.rd[id] = &fakeRD{hash: magnetHash(r.PostForm.Get("magnet")), status: "waiting_files_selection"}
		if f.rdStuck {
			f.rd[id].status = "magnet_conversion"
		}
		writeJSON(w, http.StatusCreated, map[string]string{"id": id, "uri": "https://example.invalid/" + id})
	case r.Method == http.MethodPost && strings.HasPrefix(p, "/torrents/selectFiles/"):
		t := f.rd[strings.TrimPrefix(p, "/torrents/selectFiles/")]
		if t == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown_ressource", "error_code": 7})
			return
		}
		t.selected, t.status, t.sel = true, "downloading", r.PostForm.Get("files")
		if f.cached["rd:"+t.hash] && f.rdCachedSel == nil || f.rdCachedSel[t.hash+"|"+t.sel] {
			t.status = "downloaded"
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/torrents/info/"):
		if f.rdInfoFails > 0 {
			f.rdInfoFails--
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "service_unavailable", "error_code": 25})
			return
		}
		id := strings.TrimPrefix(p, "/torrents/info/")
		t := f.rd[id]
		if t == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown_ressource", "error_code": 7})
			return
		}
		progress := 3.0
		if t.status == "downloaded" {
			progress = 100
		}
		files := []map[string]any{{"id": 1, "path": "/Movie.2024.2160p.WEB.H265-GRP.mkv", "bytes": 9000,
			"selected": map[bool]int{true: 1}[t.selected]}}
		if f.rdFiles != nil {
			files = nil
			for _, rf := range f.rdFiles {
				in := strings.Contains(","+t.sel+",", ","+strconv.Itoa(rf.id)+",")
				files = append(files, map[string]any{"id": rf.id, "path": rf.path, "bytes": rf.bytes,
					"selected": map[bool]int{true: 1}[in]})
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "filename": "Movie.2024.2160p.WEB.H265-GRP",
			"hash": t.hash, "bytes": 9000, "progress": progress, "status": t.status, "links": []string{},
			"files": files})
	case r.Method == http.MethodDelete && strings.HasPrefix(p, "/torrents/delete/"):
		delete(f.rd, strings.TrimPrefix(p, "/torrents/delete/"))
		w.WriteHeader(http.StatusNoContent)
	default:
		f.t.Errorf("rd: unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeDebrid) adMagnet(id int, m *fakeAD, withHash bool) map[string]any {
	out := map[string]any{"id": id, "filename": "Movie.2024.2160p.WEB.H265-GRP", "size": 9000,
		"status": "Downloading", "statusCode": m.code, "downloaded": 0, "uploadDate": 1761480000}
	if m.code == 4 {
		out["status"], out["downloaded"] = "Ready", 9000
	}
	if withHash {
		out["hash"] = m.hash
	}
	return out
}

// allDebrid: an upload of a hash already on the account returns that magnet's id; a new one is ready (4) when
// cached, downloading (1) otherwise.
func (f *fakeDebrid) allDebrid(w http.ResponseWriter, r *http.Request) {
	ok := func(data any) { writeJSON(w, http.StatusOK, map[string]any{"status": "success", "data": data}) }
	if f.adMaint != 0 {
		writeJSON(w, f.adMaint, map[string]any{"status": "error",
			"error": map[string]string{"code": "MAINTENANCE", "message": "AllDebrid is under maintenance"}})
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v4.1/magnet/upload":
		hash := magnetHash(r.PostForm.Get("magnets[]"))
		for id, m := range f.ad {
			if m.hash == hash {
				ok(map[string]any{"magnets": []any{map[string]any{"hash": hash, "name": hash, "ready": m.code == 4,
					"id": id}}})
				return
			}
		}
		f.nextID++
		m := &fakeAD{hash: hash, code: 1}
		if f.cached["ad:"+hash] {
			m.code = 4
		}
		f.ad[f.nextID] = m
		ok(map[string]any{"magnets": []any{map[string]any{"hash": hash, "name": hash, "ready": m.code == 4,
			"id": f.nextID}}})
	case r.Method == http.MethodGet && r.URL.Path == "/v4.1/magnet/status":
		if s := r.URL.Query().Get("id"); s != "" {
			id, _ := strconv.Atoi(s)
			m := f.ad[id]
			if m == nil {
				ok := map[string]any{"status": "error", "error": map[string]string{"code": "MAGNET_INVALID_ID"}}
				writeJSON(w, http.StatusOK, ok)
				return
			}
			ok(map[string]any{"magnets": f.adMagnet(id, m, true)})
			return
		}
		if f.adListFails {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		list := []any{}
		for id, m := range f.ad {
			list = append(list, f.adMagnet(id, m, !f.adNoHashInList))
		}
		ok(map[string]any{"magnets": list})
	case r.Method == http.MethodGet && r.URL.Path == "/v4.1/magnet/files":
		id := r.URL.Query().Get("id[]")
		ok(map[string]any{"magnets": []any{map[string]any{"id": id, "files": []any{
			map[string]any{"n": "Movie.2024.2160p.WEB.H265-GRP.mkv", "s": 9000}}}}})
	case r.Method == http.MethodPost && r.URL.Path == "/v4/magnet/delete":
		id, _ := strconv.Atoi(r.PostForm.Get("id"))
		if f.adDeleteFails || f.ad[id] == nil {
			writeJSON(w, http.StatusOK, map[string]any{"status": "error",
				"error": map[string]string{"code": "MAGNET_INVALID_ID"}})
			return
		}
		delete(f.ad, id)
		ok(map[string]string{"message": "Magnet was successfully deleted"})
	default:
		f.t.Errorf("ad: unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

// count counts requests matching "host METHOD path-prefix".
func (f *fakeDebrid) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeDebrid) deletes() int {
	return f.count("rd.test DELETE") + f.count("ad.test POST /v4/magnet/delete")
}

func (f *fakeDebrid) list() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reqs...)
}

func (f *fakeDebrid) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

// newDebridEnv is newEnv with real RD and AD clients against the fakes. cachedOnly sets router.cached_only.
func newDebridEnv(t *testing.T, enabled bool, mode policy.Mode, cachedOnly bool) (*env, *fakeDebrid, *int) {
	t.Helper()
	e := newEnv(t, enabled, mode)
	e.pol.Router.CachedOnly = &cachedOnly
	f := newFakeDebrid(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	gw, err := gateway.New(context.Background(), e.pol, gateway.Options{Logger: log, Version: "test",
		DialContext: f.dial, Timeout: 5 * time.Second, MaxWait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	adders := map[string]adder.Adder{"tb": e.ad["tb"]}
	provs := map[string]provider.Provider{"tb": e.pr["tb"]}
	for _, k := range []string{"rd", "ad"} {
		if adders[k], err = adder.New(k, e.pol.Providers[k], debridKey, gw); err != nil {
			t.Fatal(err)
		}
		if provs[k], err = provider.New(k, e.pol.Providers[k], debridKey, gw); err != nil {
			t.Fatal(err)
		}
	}
	e.r, err = New(Config{Store: e.st, Policy: e.pol, Adders: adders, Providers: provs, Logger: log,
		Now: func() time.Time { return e.clock }})
	if err != nil {
		t.Fatal(err)
	}
	sleeps := new(int)
	e.r.sleep = func(_ context.Context, d time.Duration) error {
		if d != 2*time.Second {
			t.Errorf("grace %v, want the 2s default", d)
		}
		*sleeps++
		return nil
	}
	return e, f, sleeps
}

func attempts(e *env, dl int64) string {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT provider, outcome, code FROM download_attempt WHERE download_id = ?
		ORDER BY id`, dl)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var p, o, c string
		if err := rows.Scan(&p, &o, &c); err != nil {
			e.t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s/%s/%s; ", p, o, c)
	}
	return b.String()
}

// A cached add is accepted, and the provider's instant answer is stored as evidence (add:<provider>).
func TestCachedOnlyCachedAdd(t *testing.T) {
	t.Parallel()
	e, f, sleeps := newDebridEnv(t, true, policy.ModeActive, true)
	f.cached["rd:"+h("a")] = true
	res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || res.Provider != "rd" || res.TorrentID != "RD1001" {
		t.Fatalf("%+v: %s", res, steps(res))
	}
	if f.deletes() != 0 || f.count("rd.test POST /rest/1.0/torrents/selectFiles/RD1001") != 1 || *sleeps != 1 {
		t.Errorf("deletes %d, selections %d, sleeps %d", f.deletes(),
			f.count("rd.test POST /rest/1.0/torrents/selectFiles"), *sleeps)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE state = 'ready' AND cached = 1 AND files_selected = 1
		AND provider_torrent_id = 'RD1001'`); n != 1 {
		t.Error("download not ready and cached")
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE info_hash = ? AND provider = 'rd' AND state = 'cached'
		AND source = 'add:rd' AND expires_at = ?`, h("a"), e.clock.Add(24*time.Hour).UnixMilli()); n != 1 {
		t.Error("no cached evidence from the add")
	}
	// AllDebrid: the upload answers ready; no status read is needed.
	f.cached["ad:"+h("b")] = true
	res, err = e.r.Route(e.ctx, grabReq(cand(h("b"), "Movie.2024.2160p.WEB-DL.DDP5.1-GRP"))) // RD's rule refuses
	if err != nil || res.Provider != "ad" || res.Outcome != OutcomeAdded {
		t.Fatalf("%+v %v: %s", res, err, steps(res))
	}
	if f.count("ad.test GET /v4.1/magnet/status") != 1 || f.deletes() != 0 { // the list read before the add
		t.Errorf("ad requests %v", f.list())
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE info_hash = ? AND provider = 'ad' AND state = 'cached'
		AND source = 'add:ad'`, h("b")); n != 1 {
		t.Error("no cached evidence from the AD add")
	}
}

// An uncached add of ours is deleted, recorded uncached and counted, and the next provider is tried.
func TestCachedOnlyUncachedAddDeleted(t *testing.T) {
	t.Parallel()
	e, f, sleeps := newDebridEnv(t, true, policy.ModeActive, true)
	f.cached["ad:"+h("a")] = true
	res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || res.Provider != "ad" {
		t.Fatalf("%+v: %s", res, steps(res))
	}
	if got := steps(res); got != "aaaa/rd/refused/uncached_deleted; aaaa/ad/accepted/added; " {
		t.Errorf("steps %s", got)
	}
	if got := attempts(e, res.DownloadID); got != "rd/refused/uncached_deleted; ad/accepted/added; " {
		t.Errorf("attempts %s", got)
	}
	// RD: read (waiting for files), select, grace, read (downloading): uncached; deleted. decypharr reads after the
	// selection once too.
	if f.count("rd.test DELETE /rest/1.0/torrents/delete/RD1001") != 1 || len(f.rd) != 0 || *sleeps != 1 {
		t.Errorf("rd delete %d, left %d, sleeps %d", f.count("rd.test DELETE"), len(f.rd), *sleeps)
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE info_hash = ? AND provider = 'rd'
		AND state = 'uncached' AND source = 'router:add' AND expires_at = ?`, h("a"),
		e.clock.Add(6*time.Hour).UnixMilli()); n != 1 {
		t.Error("rd not recorded uncached for miss_ttl")
	}
	if p, _ := loadPace(e.ctx, e.st.Reader(), "rd"); p.HourUncached != 1 {
		t.Errorf("rd uncached adds this hour %d", p.HourUncached)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND provider = 'ad' AND state = 'added'
		AND provider_torrent_id = '1002'`, res.DownloadID); n != 1 {
		t.Error("download not moved to ad")
	}
	// A later plan puts AD first (its add:ad evidence) and skips RD (known uncached), without a request.
	before := f.requests()
	res, _ = e.r.Plan(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series",
		Candidates: []Candidate{cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")}})
	if f.requests() != before || !strings.Contains(res.Steps[0].Message, "ad (add:ad)") {
		t.Errorf("plan: %s %+v", steps(res), res.Steps)
	}
	if st, _ := e.r.cacheState(e.ctx, h("a"), "rd", e.clock); st != "uncached" {
		t.Errorf("rd state %q", st)
	}

	// Uncached on both, with the default router.alldebrid_delete_uncached false: RD's add is deleted, AD's is kept
	// (uncached_kept, it finishes there) and never deleted, and TorBox (add only if cached) takes it, unchanged.
	if e.r.set.AllDebridDeleteUncached {
		t.Fatal("alldebrid_delete_uncached is on by default")
	}
	res, err = e.r.Route(e.ctx, grabReq(cand(h("c"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(res); got != "cccc/rd/refused/uncached_deleted; cccc/ad/refused/uncached_kept; cccc/tb/accepted/added; " {
		t.Errorf("steps %s", got)
	}
	if len(f.ad) != 2 || f.count("ad.test POST /v4/magnet/delete") != 0 {
		t.Errorf("ad magnets %d, deletes %d", len(f.ad), f.count("ad.test POST /v4/magnet/delete"))
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE info_hash = ? AND provider = 'ad'
		AND state = 'uncached' AND source = 'router:add'`, h("c")); n != 1 {
		t.Error("ad not recorded uncached")
	}
	if p, _ := loadPace(e.ctx, e.st.Reader(), "ad"); p.HourUncached != 1 {
		t.Errorf("ad uncached adds this hour %d", p.HourUncached)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND provider = 'tb' AND state = 'added'`,
		res.DownloadID); n != 1 {
		t.Error("download not on tb")
	}

	// The switch on: AD deletes its own uncached add too.
	e.r.set.AllDebridDeleteUncached = true
	res, err = e.r.Route(e.ctx, grabReq(cand(h("e"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(res); got != "eeee/rd/refused/uncached_deleted; eeee/ad/refused/uncached_deleted; eeee/tb/accepted/added; " {
		t.Errorf("steps %s", got)
	}
	if len(f.ad) != 2 || f.count("ad.test POST /v4/magnet/delete") != 1 {
		t.Errorf("ad magnets %d, deletes %d", len(f.ad), f.count("ad.test POST /v4/magnet/delete"))
	}
	// Uncached everywhere and TorBox refuses, with the switch off again: a miss on every provider, as before (after
	// RD's burst cooldown), and AllDebrid keeps its add.
	e.r.set.AllDebridDeleteUncached = false
	e.clock = e.clock.Add(15 * time.Minute)
	e.ad["tb"].add = refuse(adder.CodeNotCached)
	res, _ = e.r.Route(e.ctx, grabReq(cand(h("d"), "Movie.2024.2160p.WEB.H265-GRP")))
	if res.Outcome != OutcomeExhausted || !strings.HasSuffix(steps(res), "dddd//miss/miss; ") {
		t.Errorf("%+v: %s", res, steps(res))
	}
	if len(f.rd) != 0 || len(f.ad) != 3 || f.count("ad.test POST /v4/magnet/delete") != 1 {
		t.Errorf("left on the accounts: rd %d, ad %d; ad deletes %d", len(f.rd), len(f.ad),
			f.count("ad.test POST /v4/magnet/delete"))
	}
}

// AllDebrid: an upload that returns a magnet already on the account is never deleted, and neither is one the router
// cannot check.
func TestCachedOnlyNeverDeletesExistingADMagnet(t *testing.T) {
	t.Parallel()
	title := "Movie.2024.2160p.WEB-DL.DDP5.1-GRP" // RD's name rule refuses it: AD is tried first
	// The fresh list shows the hash: reused, nothing added.
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
	e.r.set.AllDebridDeleteUncached = true
	f.ad[77] = &fakeAD{hash: h("a"), code: 1}
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), title)))
	if err != nil || res.Provider != "ad" || !res.Reused || res.TorrentID != "77" {
		t.Fatalf("%+v %v: %s", res, err, steps(res))
	}
	if f.count("ad.test POST") != 0 || f.ad[77] == nil {
		t.Error("an add or a delete was sent")
	}

	// The list does not show hashes (or lags): the upload returns id 77, which the list had. Kept.
	e, f, _ = newDebridEnv(t, true, policy.ModeActive, true)
	e.r.set.AllDebridDeleteUncached = true
	f.ad[77] = &fakeAD{hash: h("a"), code: 1}
	f.adNoHashInList = true
	res, err = e.r.Route(e.ctx, movieReq(cand(h("a"), title)))
	if err != nil || res.Provider != "ad" || res.TorrentID != "77" {
		t.Fatalf("%+v %v: %s", res, err, steps(res))
	}
	if got := steps(res); !strings.Contains(got, "ad/accepted/uncached_kept_shared") ||
		!strings.Contains(res.Steps[len(res.Steps)-1].Message, "was on the account before the add") {
		t.Errorf("steps %s %+v", got, res.Steps)
	}
	if f.deletes() != 0 || f.ad[77] == nil {
		t.Error("the existing magnet was deleted")
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'downloading' AND cached = 0
		AND provider_torrent_id = '77'`, res.DownloadID); n != 1 {
		t.Error("the download does not wait for the kept magnet")
	}
	if p, _ := loadPace(e.ctx, e.st.Reader(), "ad"); p.HourUncached != 1 {
		t.Errorf("uncached adds %d", p.HourUncached)
	}

	// The list cannot be read: the router cannot prove the magnet is new. Kept.
	e, f, _ = newDebridEnv(t, true, policy.ModeActive, true)
	e.r.set.AllDebridDeleteUncached = true
	f.ad[77] = &fakeAD{hash: h("a"), code: 1}
	f.adListFails = true
	res, err = e.r.Route(e.ctx, movieReq(cand(h("a"), title)))
	if err != nil || res.TorrentID != "77" || !strings.Contains(steps(res), "ad/accepted/uncached_kept_shared") {
		t.Fatalf("%+v %v: %s", res, err, steps(res))
	}
	if f.deletes() != 0 || f.ad[77] == nil {
		t.Error("deleted without a list")
	}
}

// A hash the registry or our downloads place on the account is never deleted.
func TestCachedOnlyNeverDeletesKnownHash(t *testing.T) {
	t.Parallel()
	// Registry with a torrent id: reused after one read of it, nothing added.
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
	e.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (7, ?, 'present', 0, 0)`, h("a"))
	e.exec(`INSERT INTO content_provider (content_id, provider, torrent_id, seen_at) VALUES (7, 'rd', 'HELD', 0)`)
	f.rd["HELD"] = &fakeRD{hash: h("a"), status: "downloaded", selected: true}
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || !res.Reused || f.requests() != 1 || f.count("rd.test GET /rest/1.0/torrents/info/HELD") != 1 {
		t.Fatalf("%+v %v, %d requests", res, err, f.requests())
	}
	// Registry present without a torrent id: added (uncached), kept.
	e, f, _ = newDebridEnv(t, true, policy.ModeActive, true)
	e.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (7, ?, 'present', 0, 0)`, h("a"))
	e.exec(`INSERT INTO content_provider (content_id, provider, seen_at) VALUES (7, 'rd', 0)`)
	res, err = e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.Provider != "rd" || !strings.Contains(steps(res), "rd/accepted/uncached_kept_shared") {
		t.Fatalf("%+v %v: %s", res, err, steps(res))
	}
	if f.deletes() != 0 || len(f.rd) != 1 {
		t.Error("deleted a hash the registry holds")
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'downloading' AND files_selected = 1`,
		res.DownloadID); n != 1 {
		t.Error("the kept download")
	}
	// A failed download of ours left a torrent of this hash on RD (nothing is deleted when a download fails). RD makes
	// a new torrent for every add: our new add RD1001 is ours, uncached, and
	// deleted; the old torrent is never touched, and the next provider is tried.
	e, f, _ = newDebridEnv(t, true, policy.ModeActive, true)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, created_at, updated_at) VALUES (5, 2, 20, 'series', ?, 'x', 'pub', 'rd', 'OLD',
		'failed', 0, 0)`, h("a"))
	res, err = e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || steps(res) != "aaaa/rd/refused/uncached_deleted; aaaa/ad/refused/uncached_kept; aaaa/tb/accepted/added; " {
		t.Fatalf("%+v %v: %s", res, err, steps(res))
	}
	if f.deletes() != 1 || f.count("rd.test DELETE /rest/1.0/torrents/delete/RD1001") != 1 ||
		f.count("rd.test DELETE /rest/1.0/torrents/delete/OLD") != 0 || len(f.rd) != 0 {
		t.Errorf("deletes %v", f.list())
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = 5 AND state = 'failed' AND provider_torrent_id = 'OLD'`); n != 1 {
		t.Error("the old download changed")
	}
	// On AllDebrid the same old download still makes the hash shared (an upload of a hash already there returns the
	// existing magnet): kept, nothing deleted, even with deletes on.
	e, f, _ = newDebridEnv(t, true, policy.ModeActive, true)
	e.r.set.AllDebridDeleteUncached = true
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, created_at, updated_at) VALUES (5, 2, 20, 'series', ?, 'x', 'pub', 'ad', 'OLD',
		'failed', 0, 0)`, h("a"))
	res, err = e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB-DL.DDP5.1-GRP")))
	if err != nil || !strings.Contains(steps(res), "ad/accepted/uncached_kept_shared") || f.deletes() != 0 {
		t.Fatalf("%+v %v: %s, %d deletes", res, err, steps(res), f.deletes())
	}
	if !strings.Contains(res.Steps[len(res.Steps)-1].Message, "download 5 (failed): torrent OLD") {
		t.Errorf("message %q", res.Steps[len(res.Steps)-1].Message)
	}
}

// cached_only off: nothing changes. The add stays, no list read, no re-read, no delete.
func TestCachedOnlyOffUnchanged(t *testing.T) {
	t.Parallel()
	e, f, sleeps := newDebridEnv(t, true, policy.ModeActive, false)
	e.r.set.AllDebridDeleteUncached = true
	res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.Provider != "rd" || res.Outcome != OutcomeAdded {
		t.Fatalf("%+v %v", res, err)
	}
	if got := steps(res); got != "aaaa/rd/accepted/added; " {
		t.Errorf("steps %s", got)
	}
	// One read after the add selects the files (as before); nothing else.
	if f.count("rd.test GET") != 1 || f.count("rd.test POST /rest/1.0/torrents/selectFiles") != 1 ||
		f.deletes() != 0 || *sleeps != 0 {
		t.Errorf("requests %v, sleeps %d", f.list(), *sleeps)
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache`); n != 0 {
		t.Errorf("%d provider_cache rows", n)
	}
	res, err = e.r.Route(e.ctx, grabReq(cand(h("b"), "Movie.2024.2160p.WEB-DL.DDP5.1-GRP")))
	if err != nil || res.Provider != "ad" || f.count("ad.test GET") != 0 || f.deletes() != 0 {
		t.Errorf("%+v %v, requests %v", res, err, f.list())
	}
}

// The gate: a shadow version and the kill switch send nothing to RD or AD (no list read, no add, no delete), and
// the dry run says what cached-only would do.
func TestCachedOnlyGate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		enabled bool
		mode    policy.Mode
	}{{true, policy.ModeShadow}, {false, policy.ModeActive}} {
		e, f, _ := newDebridEnv(t, tc.enabled, tc.mode, true)
		res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
		if err != nil || res.Outcome != OutcomeWouldAdd || res.PlanID == 0 {
			t.Fatalf("%+v %v", res, err)
		}
		if !strings.Contains(res.Steps[0].Message, "cached_only: if it is not ready at once it is deleted") {
			t.Errorf("dry-run message %q", res.Steps[0].Message)
		}
		// A download in hand, uncached on RD: the poller reads it and never deletes or selects.
		f.rd["RDX"] = &fakeRD{hash: h("b"), status: "downloading", selected: true}
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
			provider, provider_torrent_id, files_selected, state, created_at, updated_at) VALUES (5, 1, 10, 'movie', ?,
			'x', 'pub', 'rd', 'RDX', 1, 'added', 0, 0)`, h("b"))
		if _, err := e.r.Poll(e.ctx); err != nil {
			t.Fatal(err)
		}
		if f.requests() != 1 || f.count("rd.test GET /rest/1.0/torrents/info/RDX") != 1 || f.rd["RDX"] == nil {
			t.Errorf("requests %v", f.list())
		}
		if _, err := e.r.Plan(e.ctx, movieReq(cand(h("c"), "Movie.2024.2160p.WEB.H265-GRP"))); err != nil ||
			f.requests() != 1 {
			t.Errorf("plan sent requests: %v %v", f.list(), err)
		}
	}
	// The dry run on RD with an old failed download of ours holding the hash: the new add would be ours, deleted when
	// uncached.
	e, _, _ := newDebridEnv(t, true, policy.ModeShadow, true)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, created_at, updated_at) VALUES (5, 2, 20, 'series', ?, 'x', 'pub', 'rd', 'OLD',
		'failed', 0, 0)`, h("a"))
	res, _ := e.r.Plan(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if want := "would add the bare magnet; cached_only: if it is not ready at once it is deleted (" +
		codeUncachedDeleted + ") and the next provider tried"; res.Steps[0].Message != want {
		t.Errorf("dry-run message %q, want %q", res.Steps[0].Message, want)
	}
	// The dry run names a hash the registry shows on the account (another client's): it would stay.
	e, _, _ = newDebridEnv(t, true, policy.ModeShadow, true)
	e.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (7, ?, 'present', 0, 0)`, h("a"))
	e.exec(`INSERT INTO content_provider (content_id, provider, seen_at) VALUES (7, 'rd', 0)`)
	res, _ = e.r.Plan(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if !strings.Contains(res.Steps[0].Message, codeUncachedKeptShared) {
		t.Errorf("dry-run message %q", res.Steps[0].Message)
	}
}

// The delete is refused by the adder itself when the router's guard is bypassed: only an add of this attempt.
func TestDeleteNeedsTheAddersOwnAdd(t *testing.T) {
	t.Parallel()
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
	del := e.r.adders["rd"].(adder.Deleter)
	err := del.DeleteOwnUncachedAdd(e.ctx, &adder.Added{ID: "RDX", Hash: h("a")},
		adder.OwnAdd{NotOnAccountBefore: true, NewEntry: true})
	if err == nil || f.requests() != 0 {
		t.Errorf("%v, %d requests", err, f.requests())
	}
}

// The opt-in never bypasses registry/download ownership checks or a failed delete.
func TestAllDebridUncachedDeleteUnprovable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, why string
		setup     func(*env, *fakeDebrid)
	}{
		{"registry hash", "hash was on the account", func(e *env, _ *fakeDebrid) {
			e.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (7, ?, 'present', 0, 0)`, h("a"))
			e.exec(`INSERT INTO content_provider (content_id, provider, seen_at) VALUES (7, 'ad', 0)`)
		}},
		{"registry id", "known already (registry)", func(e *env, _ *fakeDebrid) {
			e.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (7, ?, 'present', 0, 0)`, h("b"))
			e.exec(`INSERT INTO content_provider (content_id, provider, torrent_id, state, seen_at) VALUES (7, 'ad', '1001', 'absent', 0)`)
		}},
		{"download id", "known already (download 5)", func(e *env, _ *fakeDebrid) {
			e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
				provider_torrent_id, state, created_at, updated_at) VALUES (5, 2, 20, 'series', ?, 'x', 'pub', 'ad', '1001', 'failed', 0, 0)`, h("b"))
		}},
		{"delete failed", "delete failed", func(_ *env, f *fakeDebrid) { f.adDeleteFails = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
			e.r.set.AllDebridDeleteUncached = true
			tc.setup(e, f)
			res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB-DL.DDP5.1-GRP")))
			if err != nil || res.Provider != "ad" || res.TorrentID != "1001" ||
				!strings.Contains(steps(res), "ad/accepted/uncached_kept_shared") ||
				!strings.Contains(res.Steps[len(res.Steps)-1].Message, tc.why) {
				t.Fatalf("%+v %v: %s", res, err, steps(res))
			}
			wantDeletes := 0
			if f.adDeleteFails {
				wantDeletes = 1
			}
			if len(f.ad) != 1 || f.count("ad.test POST /v4/magnet/delete") != wantDeletes {
				t.Fatalf("magnets %d, delete requests %d", len(f.ad), f.count("ad.test POST /v4/magnet/delete"))
			}
			if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'downloading'
				AND cached = 0 AND provider_torrent_id = '1001'`, res.DownloadID); n != 1 {
				t.Error("the download does not wait for the kept add")
			}
		})
	}
}

// Exercise the opt-in and its list proof with the in-memory provider too: no provider HTTP is needed to decide.
func TestAllDebridUncachedDeleteOptIn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                          string
		enabled, listFailed, listedID bool
		code, provider                string
		deletes                       int
	}{
		{name: "off", code: "uncached_kept", provider: "tb"},
		{name: "on", enabled: true, code: "uncached_deleted", provider: "tb", deletes: 1},
		{name: "list failed", enabled: true, listFailed: true, code: "uncached_kept_shared", provider: "ad"},
		{name: "id already listed", enabled: true, listedID: true, code: "uncached_kept_shared", provider: "ad"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			e.pol.Router.AllDebridDeleteUncached = tc.enabled
			e.r.set = e.pol.RouterSettings()
			e.r.sleep = func(context.Context, time.Duration) error { return nil }
			e.pr["ad"].set("ad-aaaa", provider.Torrent{Hash: h("a"), State: provider.StateDownloading, Status: "Downloading"})
			if tc.listFailed {
				e.pr["ad"].listErr = fmt.Errorf("fixture list failed")
			}
			if tc.listedID {
				e.pr["ad"].list = []provider.Torrent{{ID: "ad-aaaa", Hash: h("b")}}
			}
			res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB-DL.DDP5.1-GRP")))
			if err != nil || res.Provider != tc.provider || !strings.Contains(steps(res), "ad/") || !strings.Contains(steps(res), tc.code) {
				t.Fatalf("%+v %v: %s", res, err, steps(res))
			}
			if len(e.ad["ad"].deletes) != tc.deletes {
				t.Fatalf("deletes %v, want %d", e.ad["ad"].deletes, tc.deletes)
			}
			if got := attempts(e, res.DownloadID); !strings.Contains(got, tc.code) {
				t.Errorf("attempts %s", got)
			}
		})
	}
}
