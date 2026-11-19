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
	// rdConvertReads keeps new Real-Debrid torrents in magnet_conversion for that many status reads, then waiting
	// for a file selection.
	rdConvertReads int
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
	convLeft     int    // status reads left in magnet_conversion (rdConvertReads)
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
		if f.rdConvertReads > 0 {
			f.rd[id].status, f.rd[id].convLeft = "magnet_conversion", f.rdConvertReads
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
		status := t.status
		if t.convLeft > 0 {
			if t.convLeft--; t.convLeft == 0 {
				t.status = "waiting_files_selection"
			}
		}
		progress := 3.0
		if status == "downloaded" {
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
			"hash": t.hash, "bytes": 9000, "progress": progress, "status": status, "links": []string{},
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
func newDebridEnv(t *testing.T, enabled bool, cachedOnly bool) (*env, *fakeDebrid, *int) {
	t.Helper()
	e := newEnv(t, enabled)
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
	e, f, sleeps := newDebridEnv(t, true, true)
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
		AND source = 'add:rd' AND expires_at = ?`, h("a"), e.clock.Add(policy.DefaultCacheEvidenceTTL).UnixMilli()); n != 1 {
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
	e, f, sleeps := newDebridEnv(t, true, true)
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
	if st, _ := e.r.cacheState(e.ctx, h("a"), "rd", e.clock, time.Time{}); st != "uncached" {
		t.Errorf("rd state %q", st)
	}

	// Uncached on both, with router.alldebrid_delete_uncached off: RD's add is deleted, AD's is kept
	// (uncached_kept, it finishes there) and never deleted, and TorBox (add only if cached) takes it, unchanged.
	if !e.r.set.AllDebridDeleteUncached {
		t.Fatal("alldebrid_delete_uncached is off by default")
	}
	e.r.set.AllDebridDeleteUncached = false
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
	e, f, _ := newDebridEnv(t, true, true)
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
	e, f, _ = newDebridEnv(t, true, true)
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
	e, f, _ = newDebridEnv(t, true, true)
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

// AllDebrid: a magnet of the hash on the account in an error state is not reused and the hash is not uploaded
// there again: another usable magnet of the hash is reused; otherwise AllDebrid ends for the release (held_error)
// and the next provider is tried. Nothing is deleted.
func TestCachedOnlyErroredADMagnetNotReused(t *testing.T) {
	t.Parallel()
	title := "Movie.2024.2160p.WEB-DL.DDP5.1-GRP" // RD's name rule refuses it: AD is tried first
	e, f, _ := newDebridEnv(t, true, true)
	e.r.set.AllDebridDeleteUncached = true
	f.ad[77] = &fakeAD{hash: h("a"), code: 7}
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), title)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider == "ad" || !strings.Contains(steps(res), "ad/skipped/held_error") {
		t.Fatalf("%+v: %s", res, steps(res))
	}
	if f.count("ad.test POST") != 0 || f.ad[77] == nil {
		t.Error("an add or a delete was sent to ad")
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE provider = 'ad' AND info_hash = ?`, h("a")); n != 0 {
		t.Errorf("a cache verdict was written for ad")
	}

	e, f, _ = newDebridEnv(t, true, true)
	f.ad[77] = &fakeAD{hash: h("a"), code: 7}
	f.ad[78] = &fakeAD{hash: h("a"), code: 4}
	res, err = e.r.Route(e.ctx, movieReq(cand(h("a"), title)))
	if err != nil || res.Provider != "ad" || !res.Reused || res.TorrentID != "78" {
		t.Fatalf("%+v %v: %s", res, err, steps(res))
	}
	if f.count("ad.test POST") != 0 {
		t.Error("an add or a delete was sent to ad")
	}
}

// A hash the registry or our downloads place on the account is never deleted.
func TestCachedOnlyNeverDeletesKnownHash(t *testing.T) {
	t.Parallel()
	// Registry with a torrent id: reused after one read of it, nothing added.
	e, f, _ := newDebridEnv(t, true, true)
	e.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (7, ?, 'present', 0, 0)`, h("a"))
	e.exec(`INSERT INTO provider_torrent (provider, torrent_id, info_hash) VALUES ('rd', 'HELD', ?)`, h("a"))
	f.rd["HELD"] = &fakeRD{hash: h("a"), status: "downloaded", selected: true}
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || !res.Reused || f.requests() != 1 || f.count("rd.test GET /rest/1.0/torrents/info/HELD") != 1 {
		t.Fatalf("%+v %v, %d requests", res, err, f.requests())
	}
	// Registry present without a torrent id: added (uncached), kept.
	e, f, _ = newDebridEnv(t, true, true)
	e.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (7, ?, 'present', 0, 0)`, h("a"))
	e.exec(`INSERT INTO provider_torrent (provider, torrent_id, info_hash) VALUES ('rd', '', ?)`, h("a"))
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
	e, f, _ = newDebridEnv(t, true, true)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, created_at, updated_at) VALUES (5, 2, 20, 'series', ?, 'x', 'pub', 'rd', 'OLD',
		'failed', 0, 0)`, h("a"))
	e.r.set.AllDebridDeleteUncached = false
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
	e, f, _ = newDebridEnv(t, true, true)
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
	e, f, sleeps := newDebridEnv(t, true, false)
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

// The gate: the kill switch sends nothing to RD or AD (no list read, no add, no delete), and
// the dry run says what cached-only would do.
func TestCachedOnlyGate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		enabled bool
	}{{false}} {
		e, f, _ := newDebridEnv(t, tc.enabled, true)
		res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
		if err != nil || res.Outcome != OutcomeWouldAdd {
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
	e, _, _ := newDebridEnv(t, true, true)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, created_at, updated_at) VALUES (5, 2, 20, 'series', ?, 'x', 'pub', 'rd', 'OLD',
		'failed', 0, 0)`, h("a"))
	res, _ := e.r.Plan(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if want := "would add the bare magnet; cached_only: if it is not ready at once it is deleted (" +
		codeUncachedDeleted + ") and the next provider tried"; res.Steps[0].Message != want {
		t.Errorf("dry-run message %q, want %q", res.Steps[0].Message, want)
	}
	// The dry run names a hash the registry shows on the account (another client's): it would stay.
	e, _, _ = newDebridEnv(t, true, true)
	e.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (7, ?, 'present', 0, 0)`, h("a"))
	e.exec(`INSERT INTO provider_torrent (provider, torrent_id, info_hash) VALUES ('rd', '', ?)`, h("a"))
	res, _ = e.r.Plan(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if !strings.Contains(res.Steps[0].Message, codeUncachedKeptShared) {
		t.Errorf("dry-run message %q", res.Steps[0].Message)
	}
}

// The delete is refused by the adder itself when the router's guard is bypassed: only an add of this attempt.
func TestDeleteNeedsTheAddersOwnAdd(t *testing.T) {
	t.Parallel()
	e, f, _ := newDebridEnv(t, true, true)
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
			e.exec(`INSERT INTO provider_torrent (provider, torrent_id, info_hash) VALUES ('ad', '', ?)`, h("a"))
		}},
		{"registry id", "known already (registry)", func(e *env, _ *fakeDebrid) {
			e.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (7, ?, 'present', 0, 0)`, h("b"))
			e.exec(`INSERT INTO provider_torrent (provider, torrent_id, info_hash) VALUES ('ad', '1001', ?)`, h("b"))
		}},
		{"download id", "known already (download 5)", func(e *env, _ *fakeDebrid) {
			e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
				provider_torrent_id, state, created_at, updated_at) VALUES (5, 2, 20, 'series', ?, 'x', 'pub', 'ad', '1001', 'failed', 0, 0)`, h("b"))
		}},
		{"delete failed", "delete failed", func(_ *env, f *fakeDebrid) { f.adDeleteFails = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, f, _ := newDebridEnv(t, true, true)
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
			e := newEnv(t, true)
			e.pol.Router.AllDebridDeleteUncached = &tc.enabled
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

// A Real-Debrid magnet still converting is re-read after each grace, up to four reads, before it is called uncached
// (the poller's rule: no selection sent, no verdict); one that reaches the file selection within them goes on as
// usual.
func TestCachedOnlyRDConvertingUsesTheReads(t *testing.T) {
	t.Parallel()
	t.Run("still converting", func(t *testing.T) {
		e, f, sleeps := newDebridEnv(t, true, true)
		f.rdStuck = true
		f.cached["ad:"+h("a")] = true
		res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
		if err != nil {
			t.Fatal(err)
		}
		if got := steps(res); got != "aaaa/rd/refused/uncached_deleted; aaaa/ad/accepted/added; " {
			t.Fatalf("steps %s", got)
		}
		if n := f.count("rd.test GET /rest/1.0/torrents/info/RD1001"); n != 4 || *sleeps != 3 ||
			f.count("rd.test DELETE /rest/1.0/torrents/delete/RD1001") != 1 {
			t.Errorf("reads %d, sleeps %d, requests %v", n, *sleeps, f.list())
		}
		if !strings.Contains(res.Steps[0].Message, "still converting") {
			t.Errorf("message %q", res.Steps[0].Message)
		}
		if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE info_hash = ? AND provider = 'rd'
			AND state = 'uncached'`, h("a")); n != 1 {
			t.Error("rd not recorded uncached")
		}
	})
	t.Run("converts by the third read", func(t *testing.T) {
		e, f, sleeps := newDebridEnv(t, true, true)
		f.rdConvertReads = 2
		f.cached["rd:"+h("a")] = true
		res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != OutcomeAdded || res.Provider != "rd" || res.Steps[len(res.Steps)-1].Code != codeAddedCached {
			t.Fatalf("%+v: %s", res, steps(res))
		}
		if f.deletes() != 0 || f.count("rd.test POST /rest/1.0/torrents/selectFiles/RD1001") != 1 || *sleeps != 3 {
			t.Errorf("deletes %d, sleeps %d, requests %v", f.deletes(), *sleeps, f.list())
		}
		if n := e.count(`SELECT COUNT(*) FROM download WHERE state = 'ready' AND cached = 1
			AND provider_torrent_id = 'RD1001'`); n != 1 {
			t.Error("download not ready and cached")
		}
	})
	// Conversion ends exactly at the last read: the selection is still followed by a read after its grace.
	for _, cached := range []bool{true, false} {
		t.Run(fmt.Sprintf("selected on the last read, cached %v", cached), func(t *testing.T) {
			e, f, sleeps := newDebridEnv(t, true, true)
			f.rdConvertReads = 3
			f.cached["rd:"+h("a")] = cached
			f.cached["ad:"+h("a")] = true
			res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
			if err != nil {
				t.Fatal(err)
			}
			want := "aaaa/rd/accepted/added; "
			if !cached {
				want = "aaaa/rd/refused/uncached_deleted; aaaa/ad/accepted/added; "
			}
			if got := steps(res); got != want || *sleeps != 4 ||
				f.count("rd.test GET /rest/1.0/torrents/info/RD1001") != 5 ||
				f.count("rd.test POST /rest/1.0/torrents/selectFiles/RD1001") != 1 ||
				cachedCheck(e, res.DownloadID) != "" {
				t.Fatalf("steps %s; sleeps %d, check %q, requests %v", got, *sleeps, cachedCheck(e, res.DownloadID),
					f.list())
			}
			if cached && e.count(`SELECT COUNT(*) FROM download WHERE state = 'ready' AND cached = 1
				AND provider_torrent_id = 'RD1001'`) != 1 {
				t.Error("download not ready and cached")
			}
		})
	}
}

// A not-cached trial add that is deleted and requeued leaves no Grabbed row in History; one a provider took before
// keeps it, dated when that provider accepted it.
func TestUnmadeAddLeavesNoGrab(t *testing.T) {
	t.Parallel()
	e, _, _ := newDebridEnv(t, true, true)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at) VALUES (900, 1, 10, 'movie', ?, 'Movie.2024.2160p.WEB.H265-GRP', 'x', 'queued', 1, 1)`,
		h("z"))
	grabs := func() int { return e.count(`SELECT COUNT(*) FROM history WHERE ref = 'dl:900' AND event = 'grabbed'`) }
	if err := e.r.markAdded(e.ctx, 900, "rd", &adder.Added{ID: "RD9"}, false, addFence{}); err != nil {
		t.Fatal(err)
	}
	if grabs() != 1 {
		t.Fatal("no grab row for the add")
	}
	if err := e.r.unmarkAdded(e.ctx, 900, "rd", "RD9", "uncached on rd; our add was deleted"); err != nil {
		t.Fatal(err)
	}
	if grabs() != 0 {
		t.Error("the unmade add is still a grab")
	}
	e.exec(`INSERT INTO download_attempt (download_id, provider, outcome, code, message, attempted_at)
		VALUES (900, 'ad', 'accepted', 'added', '', 5)`)
	if err := e.r.markAdded(e.ctx, 900, "rd", &adder.Added{ID: "RD10"}, false, addFence{}); err != nil {
		t.Fatal(err)
	}
	if err := e.r.unmarkAdded(e.ctx, 900, "rd", "RD10", "uncached on rd; our add was deleted"); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM history WHERE ref = 'dl:900' AND at = 5`); n != 1 {
		t.Error("an earlier accepted add lost its grab row")
	}
}

// The guards read the record (provider_torrent). A torrent of the hash on the account before our add (G1) is
// reused, never added over or deleted; one the record lists without an id it can reuse keeps the add (as
// TestCachedOnlyNeverDeletesKnownHash); a recorded torrent with the id our add returned (G2) keeps the add; with
// neither, our own just-made add is deleted, whatever else the record holds.
func TestCachedOnlyGuardsReadTheRecord(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, row, hash, want string
	}{
		{"hash on the account before", "RD0001", h("a"), "rd/accepted/reused"},
		{"returned id in the record", "RD1001", h("b"), "rd/accepted/uncached_kept_shared"},
		{"our own add", "RD0001", h("b"), "rd/refused/uncached_deleted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, f, _ := newDebridEnv(t, true, true)
			e.exec(`INSERT INTO provider_torrent (provider, torrent_id, info_hash, progress) VALUES ('rd', ?, ?, 100)`,
				tc.row, tc.hash)
			if tc.hash == h("a") {
				f.rd[tc.row] = &fakeRD{hash: h("a"), status: "downloaded", selected: true}
			}
			res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
			if err != nil || !strings.Contains(steps(res)+reusedStep(res), tc.want) {
				t.Fatalf("%+v %v: %s", res, err, steps(res))
			}
			deleted := f.count("rd.test DELETE")
			if deleted != map[bool]int{true: 1, false: 0}[strings.HasSuffix(tc.want, "uncached_deleted")] {
				t.Errorf("rd deletes %d", deleted)
			}
		})
	}
}

// reusedStep names a reuse as a step ("rd/accepted/reused; ").
func reusedStep(res *Result) string {
	if res == nil || !res.Reused {
		return ""
	}
	return res.Provider + "/accepted/reused; "
}

// rdRefusedFiles is a clean pack with one video whose own name Real-Debrid's name rule refuses (HDTV.x264); the
// sample and the .nfo are refused names too but are never selected, so they are not judged.
var rdRefusedFiles = []rdFile{{1, "/Movie/Movie.2024.2160p.BluRay.x265-GRP.mkv", 9000},
	{2, "/Movie/Extra.720p.HDTV.x264-GRP.mkv", 900}, {3, "/Movie/Sample/WEB-DL.sample.mkv", 9}, {4, "/Movie/WEB-DL.nfo", 1}}

const rdRefusedWhy = "Real-Debrid refuses a file name in this release (Extra.720p.HDTV.x264-GRP.mkv)"

// A fresh Real-Debrid add whose selection would hold a video Real-Debrid refuses by name is a miss there, with
// cached-only on or off: provably ours, it is deleted with nothing selected, the miss is recorded (not uncached, not
// counted, nothing blocklisted) and the next provider is tried; the next route skips Real-Debrid for it.
func TestRDRefusedFileNameInRoute(t *testing.T) {
	t.Parallel()
	for _, cachedOnly := range []bool{true, false} {
		e, f, _ := newDebridEnv(t, true, cachedOnly)
		f.rdFiles = rdRefusedFiles
		f.cached["ad:"+h("a")] = true
		res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
		if err != nil || res.Provider != "ad" || res.Outcome != OutcomeAdded ||
			!strings.HasPrefix(steps(res), "aaaa/rd/refused/rd_name_refused; aaaa/ad/accepted/") {
			t.Fatalf("cached_only %v: %+v %v: %s", cachedOnly, res, err, steps(res))
		}
		if msg := res.Steps[0].Message; !strings.HasPrefix(msg, rdRefusedWhy+"; our add RD1001 was deleted") {
			t.Errorf("cached_only %v: message %q", cachedOnly, msg)
		}
		if f.count("rd.test DELETE /rest/1.0/torrents/delete/RD1001") != 1 || len(f.rd) != 0 ||
			f.count("rd.test POST /rest/1.0/torrents/selectFiles") != 0 {
			t.Errorf("cached_only %v: rd requests %v", cachedOnly, f.list())
		}
		if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE info_hash = ? AND provider = 'rd' AND state = 'miss'
			AND source = 'router:rd_name' AND expires_at = ?`, h("a"), e.clock.Add(6*time.Hour).UnixMilli()); n != 1 {
			t.Errorf("cached_only %v: rd miss not recorded for miss_ttl", cachedOnly)
		}
		if p, _ := loadPace(e.ctx, e.st.Reader(), "rd"); p.HourUncached != 0 {
			t.Errorf("cached_only %v: counted as an uncached add", cachedOnly)
		}
		if n := e.count(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
			t.Errorf("cached_only %v: blocklisted", cachedOnly)
		}
		// No add/delete loop: a later route of the release skips Real-Debrid as a miss.
		if st, _ := e.r.cacheState(e.ctx, h("a"), "rd", e.clock, time.Time{}); st != "miss" {
			t.Errorf("cached_only %v: rd state %q", cachedOnly, st)
		}
	}
}

// Not provably ours (another record knows the id Real-Debrid returned): nothing is deleted and nothing held for
// review; the miss is recorded and the route goes on. A reused torrent is not checked and never touched.
func TestRDRefusedFileNameNotOurs(t *testing.T) {
	t.Parallel()
	e, f, _ := newDebridEnv(t, true, false)
	f.rdFiles = rdRefusedFiles
	e.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (8, ?, 'present', 0, 0)`, h("b"))
	e.exec(`INSERT INTO provider_torrent (provider, torrent_id, info_hash) VALUES ('rd', 'RD1001', ?)`, h("b"))
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.Provider != "ad" || !strings.HasPrefix(steps(res), "aaaa/rd/refused/rd_name_refused; aaaa/ad/accepted/") {
		t.Fatalf("%+v %v: %s", res, err, steps(res))
	}
	if msg := res.Steps[0].Message; !strings.Contains(msg, "our add RD1001 was kept (torrent RD1001 is known already") {
		t.Errorf("message %q", msg)
	}
	if f.deletes() != 0 || len(f.rd) != 1 {
		t.Errorf("deleted a torrent not provably ours: %v", f.list())
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE info_hash = ? AND provider = 'rd' AND state = 'miss'
		AND source = 'router:rd_name'`, h("a")); n != 1 {
		t.Error("miss not recorded")
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND provider = 'ad'`, res.DownloadID); n != 1 {
		t.Error("the route did not go on")
	}

	// Reused: already on the account and selected; not checked, nothing deleted, no miss.
	e, f, _ = newDebridEnv(t, true, true)
	f.rdFiles = rdRefusedFiles
	e.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (7, ?, 'present', 0, 0)`, h("a"))
	e.exec(`INSERT INTO provider_torrent (provider, torrent_id, info_hash) VALUES ('rd', 'HELD', ?)`, h("a"))
	f.rd["HELD"] = &fakeRD{hash: h("a"), status: "downloaded", selected: true, sel: "1,2"}
	res, err = e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || !res.Reused || res.Provider != "rd" {
		t.Fatalf("%+v %v: %s", res, err, steps(res))
	}
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if f.deletes() != 0 || len(f.rd) != 1 || e.count(`SELECT COUNT(*) FROM provider_cache WHERE state = 'miss'`) != 0 {
		t.Errorf("the reused torrent was touched: %v", f.list())
	}
}

// From the poller: the route's read after the add failed, so the poller meets the waiting selection. Our add is
// deleted, the miss recorded and the download routed again (to AllDebrid), nothing blocklisted; cached-only on or off.
func TestRDRefusedFileNameFromPoller(t *testing.T) {
	t.Parallel()
	for _, cachedOnly := range []bool{true, false} {
		e, f, _ := newDebridEnv(t, true, cachedOnly)
		w := withWorker(e)
		f.rdFiles = rdRefusedFiles
		f.cached["ad:"+h("a")] = true
		f.rdInfoFails = 1
		res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
		if err != nil || res.Outcome != OutcomeAdded || res.Provider != "rd" {
			t.Fatalf("cached_only %v: %+v %v: %s", cachedOnly, res, err, steps(res))
		}
		dl := res.DownloadID
		if _, err := e.r.Poll(e.ctx); err != nil {
			t.Fatal(err)
		}
		if f.count("rd.test DELETE /rest/1.0/torrents/delete/RD1001") != 1 || len(f.rd) != 0 ||
			f.count("rd.test POST /rest/1.0/torrents/selectFiles") != 0 {
			t.Fatalf("cached_only %v: rd requests %v", cachedOnly, f.list())
		}
		if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'queued' AND provider IS NULL
			AND error = ?`, dl, rdRefusedWhy+"; our add was deleted"); n != 1 {
			t.Errorf("cached_only %v: download not queued again", cachedOnly)
		}
		if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE info_hash = ? AND provider = 'rd' AND state = 'miss'
			AND source = 'router:rd_name'`, h("a")); n != 1 {
			t.Errorf("cached_only %v: miss not recorded", cachedOnly)
		}
		if ran, err := w.RunOne(e.ctx); err != nil || !ran {
			t.Fatalf("route job: %v %v", ran, err)
		}
		if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'added' AND provider = 'ad'`, dl); n != 1 {
			t.Errorf("cached_only %v: download not routed to ad: %s", cachedOnly, attempts(e, dl))
		}
		if n := e.count(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
			t.Errorf("cached_only %v: blocklisted", cachedOnly)
		}
	}
}
