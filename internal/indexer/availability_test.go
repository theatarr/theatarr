package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// availReq is one availability request as DMM saw it.
type availReq struct {
	service, key, query, contentType string
	imdb                             bool // the body carried an imdbId
	hashes                           []string
}

// availServer is DMM: its availability answers the hashes listed for the service; anything else is a feed.
type availServer struct {
	mu     sync.Mutex
	reqs   []availReq
	listed map[string][]string // service -> hashes
	status int                 // non-zero: every availability call answers this
	fail   string              // a service whose calls answer 500
}

func (s *availServer) serve(t *testing.T, n *testNet) {
	n.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sponsor/availability" {
			fmt.Fprint(w, dmmFeed())
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("availability: %s", r.Method)
		}
		var body struct {
			Service string   `json:"service"`
			IMDb    *string  `json:"imdbId"`
			Hashes  []string `json:"hashes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("availability body: %v", err)
		}
		s.mu.Lock()
		s.reqs = append(s.reqs, availReq{service: body.Service, imdb: body.IMDb != nil, key: r.Header.Get("x-api-key"),
			query: r.URL.RawQuery, contentType: r.Header.Get("Content-Type"), hashes: body.Hashes})
		status := s.status
		if body.Service == s.fail {
			status = http.StatusInternalServerError
		}
		s.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		var items []string
		for _, h := range body.Hashes {
			if slices.Contains(s.listed[body.Service], h) {
				items = append(items, fmt.Sprintf(`{"hash": %q, "files": [{"file_id": 1, "path": "/a.mkv", "bytes": 1}]}`, h))
			}
		}
		fmt.Fprintf(w, `{"available": [%s]}`, strings.Join(items, ","))
	}))
}

func (s *availServer) requests() []availReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]availReq(nil), s.reqs...)
}

func availVersions(providers ...string) []*policy.VersionPolicy {
	return []*policy.VersionPolicy{{Storage: policy.StorageDebrid, Indexers: []string{"dmm", "torrentio", "prowlarr", "tracker"},
		Providers: providers}}
}

// DMM's availability: the releases without rd or ad evidence are asked about, with DMM's key and no IMDb id
// (DMM lists more hashes unscoped), whatever their privacy label (private and unset included); listed hashes become
// feed evidence (in the search and stored), unknown ones stay unknown. Releases with evidence or a live verdict of
// ours, and indexers no debrid version lists are not sent.
func TestDMMAvailabilityEvidence(t *testing.T) {
	n := newNet(t)
	dmm := &availServer{listed: map[string][]string{"rd": {hx("2"), hx("3"), hx("8")}, "ad": {hx("1"), hx("3"), hx("6")}}}
	dmm.serve(t, n)
	p := evidencePolicy()
	h := newHarness(t, n, p, false)
	st := openStore(t)
	h.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: st, Policy: p, Now: h.clock}))
	now := h.clock()
	if _, err := st.Writer().Exec(`INSERT INTO provider_cache (info_hash, provider, state, source, checked_at,
		expires_at) VALUES (?, 'rd', 'uncached', 'router:add', ?, ?)`, hx("6"), store.Millis(now),
		store.Millis(now.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	rel := func(ix, c string, privacy policy.Privacy) Release {
		return Release{Indexer: ix, InfoHash: hx(c), Privacy: privacy}
	}
	releases := func() []Release {
		return []Release{
			rel("torrentio", "1", policy.PrivacyPublic),
			rel("prowlarr", "2", policy.PrivacyPublic),
			rel("tracker", "3", policy.PrivacyPrivate), // private-labelled: asked like any other
			{Indexer: "dmm", Kind: "dmm", InfoHash: hx("4"), Privacy: policy.PrivacyPublic, Extras: map[string]string{"cached_on": "rd"}},
			rel("torrentio", "4", policy.PrivacyPublic),
			rel("zilean", "5", policy.PrivacyPublic), // no version searched lists zilean
			rel("torrentio", "6", policy.PrivacyPublic),
			rel("torrentio", "7", policy.PrivacyPublic), // DMM does not list it: unknown
			rel("prowlarr", "8", policy.PrivacyUnknown), // no privacy set: asked like any other
		}
	}
	ctx := context.Background()
	rs := releases()
	h.svc.ApplyCachedOn(ctx, rs)
	if got := h.svc.CheckAvailability(ctx, availVersions("rd", "ad", "tb"), rs); got != 6 {
		t.Fatalf("gained %d, want 6", got)
	}
	reqs := dmm.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests %+v", reqs)
	}
	want := map[string][]string{"rd": {hx("1"), hx("2"), hx("3"), hx("7"), hx("8")},
		"ad": {hx("1"), hx("2"), hx("3"), hx("4"), hx("6"), hx("7"), hx("8")}}
	for _, r := range reqs {
		if !slices.Equal(r.hashes, want[r.service]) {
			t.Errorf("%s asked %v, want %v", r.service, r.hashes, want[r.service])
		}
		if r.key != dmmKey || r.query != "" || r.imdb || r.contentType != "application/json" {
			t.Errorf("request %+v", r)
		}
	}
	const via = "feed:dmm/availability"
	for _, c := range []struct {
		ix, hash string
		on       []string
	}{
		{"torrentio", "1", []string{"ad"}}, {"prowlarr", "2", []string{"rd"}}, {"tracker", "3", []string{"rd", "ad"}},
		{"torrentio", "4", []string{"rd"}}, {"zilean", "5", nil}, {"torrentio", "6", []string{"ad"}},
		{"torrentio", "7", nil}, {"prowlarr", "8", []string{"rd"}},
	} {
		r := byHash(rs, hx(c.hash), c.ix)
		if !slices.Equal(r.CachedOn, c.on) {
			t.Errorf("%s %s: cached on %v, want %v", c.ix, c.hash, r.CachedOn, c.on)
		}
		for _, code := range c.on {
			if code != "rd" || c.hash != "4" {
				if r.CachedVia[code] != via {
					t.Errorf("%s %s: via %v", c.ix, c.hash, r.CachedVia)
				}
			}
		}
	}
	// Stored like a feed's positives; our own uncached verdict at rd stays; nothing for unknown hashes.
	rows := map[string]string{"1111/ad": "cached " + via, "2222/rd": "cached " + via, "3333/ad": "cached " + via,
		"3333/rd": "cached " + via, "6666/ad": "cached " + via, "6666/rd": "uncached router:add",
		"8888/rd": "cached " + via}
	if got := cacheRows(t, st); fmt.Sprint(got) != fmt.Sprint(rows) {
		t.Fatalf("provider_cache %v, want %v", got, rows)
	}

	// The next search: stored evidence fills the positives, and DMM was asked about 7 lately: no request.
	rs = releases()
	h.svc.ApplyCachedOn(ctx, rs)
	if got := h.svc.CheckAvailability(ctx, availVersions("rd", "ad", "tb"), rs); got != 0 ||
		len(dmm.requests()) != 2 {
		t.Fatalf("again: gained %d, %d requests", got, len(dmm.requests()))
	}
	if r := byHash(rs, hx("1"), "torrentio"); !slices.Equal(r.CachedOn, []string{"ad"}) || r.CachedVia["ad"] != via {
		t.Fatalf("stored evidence: %+v", r)
	}
	// Past the memo: only the unknown hashes are asked again.
	h.advance(availabilityAskedTTL)
	rs = releases()
	h.svc.ApplyCachedOn(ctx, rs)
	h.svc.CheckAvailability(ctx, availVersions("rd", "ad", "tb"), rs)
	reqs = dmm.requests()[2:]
	want = map[string][]string{"rd": {hx("1"), hx("7")}, "ad": {hx("2"), hx("4"), hx("7"), hx("8")}}
	if len(reqs) != 2 {
		t.Fatalf("after the memo: %+v", reqs)
	}
	for _, r := range reqs {
		if !slices.Equal(r.hashes, want[r.service]) || r.imdb {
			t.Errorf("after the memo: %s asked %v (imdb %v), want %v", r.service, r.hashes, r.imdb, want[r.service])
		}
	}
}

// Only the providers the version may use are asked about: a TorBox-only version asks nothing, an RD-only one rd only.
func TestDMMAvailabilityFollowsVersionProviders(t *testing.T) {
	n := newNet(t)
	dmm := &availServer{}
	dmm.serve(t, n)
	p := evidencePolicy()
	h := newHarness(t, n, p, false)
	h.svc.SetEvidence(NewEvidence(EvidenceConfig{Policy: p, Now: h.clock}))
	ctx := context.Background()
	rs := []Release{{Indexer: "torrentio", InfoHash: hx("1"), Privacy: policy.PrivacyPublic}}
	h.svc.CheckAvailability(ctx, availVersions("tb"), rs)
	h.svc.CheckAvailability(ctx, []*policy.VersionPolicy{{Storage: policy.StorageSeedbox, Indexers: []string{"torrentio"}}}, rs)
	if reqs := dmm.requests(); len(reqs) != 0 {
		t.Fatalf("requests %+v", reqs)
	}
	p.Indexers["torrentio"].Providers = []string{"rd"}
	h.svc.CheckAvailability(ctx, availVersions("rd", "ad"), rs)
	if reqs := dmm.requests(); len(reqs) != 1 || reqs[0].service != "rd" {
		t.Fatalf("requests %+v", reqs)
	}
}

// Hashes go 100 a call, rd and ad taking turns, at most availabilityCalls calls a search.
func TestDMMAvailabilityBatches(t *testing.T) {
	n := newNet(t)
	dmm := &availServer{}
	dmm.serve(t, n)
	p := evidencePolicy()
	h := newHarness(t, n, p, false)
	h.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: openStore(t), Policy: p, Now: h.clock}))
	var rs []Release
	for i := range 250 {
		rs = append(rs, Release{Indexer: "torrentio", InfoHash: fmt.Sprintf("%040x", i+1), Privacy: policy.PrivacyPublic})
	}
	ctx := context.Background()
	h.svc.CheckAvailability(ctx, availVersions("rd"), rs)
	var sizes []int
	for _, r := range dmm.requests() {
		sizes = append(sizes, len(r.hashes))
	}
	if !slices.Equal(sizes, []int{100, 100, 50}) {
		t.Fatalf("rd batches %v", sizes)
	}
	if got := dmm.requests()[1].hashes[0]; got != fmt.Sprintf("%040x", 101) {
		t.Fatalf("second batch starts at %s", got)
	}

	// Both providers, fresh hashes: rd, ad, rd, ad, then the bound.
	for i := range rs {
		rs[i].InfoHash = fmt.Sprintf("%040x", 1000+i)
	}
	h.svc.CheckAvailability(ctx, availVersions("rd", "ad"), rs)
	var order []string
	for _, r := range dmm.requests()[3:] {
		order = append(order, fmt.Sprintf("%s:%d", r.service, len(r.hashes)))
	}
	if !slices.Equal(order, []string{"rd:100", "ad:100", "rd:100", "ad:100"}) {
		t.Fatalf("calls %v", order)
	}
}

// A failing call ends the pass: the search keeps what it had, nothing is stored, and the hashes are asked again
// next time. Without a DMM indexer with a key, or without the evidence keeper, nothing is asked.
func TestDMMAvailabilityFailureAndNoKey(t *testing.T) {
	n := newNet(t)
	dmm := &availServer{status: http.StatusInternalServerError, listed: map[string][]string{"rd": {hx("1")}}}
	dmm.serve(t, n)
	p := evidencePolicy()
	h := newHarness(t, n, p, false)
	st := openStore(t)
	h.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: st, Policy: p, Now: h.clock,
		Logger: slog.New(slog.NewJSONHandler(&n.logs, nil))}))
	ctx := context.Background()
	var rs []Release
	for i := range 150 {
		rs = append(rs, Release{Indexer: "torrentio", InfoHash: fmt.Sprintf("%040x", i+1), Privacy: policy.PrivacyPublic})
	}
	if got := h.svc.CheckAvailability(ctx, availVersions("rd", "ad"), rs); got != 0 || len(dmm.requests()) != 1 {
		t.Fatalf("failed call: gained %d, %d requests", got, len(dmm.requests()))
	}
	for _, r := range rs {
		if r.CachedOn != nil {
			t.Fatalf("evidence after a failure: %+v", r)
		}
	}
	if rows := cacheRows(t, st); len(rows) != 0 {
		t.Fatalf("provider_cache %v", rows)
	}
	if !strings.Contains(n.logs.String(), "DMM availability not checked") {
		t.Error("no log line for the failure")
	}

	dmm.mu.Lock()
	dmm.status = 0
	dmm.mu.Unlock()
	rs = rs[:1]
	rs[0].InfoHash = hx("1")
	if got := h.svc.CheckAvailability(ctx, availVersions("rd"), rs); got != 1 || !slices.Equal(rs[0].CachedOn, []string{"rd"}) {
		t.Fatalf("after recovery: gained %d, %+v", got, rs[0])
	}

	before := len(dmm.requests())
	other := []Release{{Indexer: "torrentio", InfoHash: hx("2"), Privacy: policy.PrivacyPublic}}
	t.Setenv(dmmKeyEnv, "")
	if got := h.svc.CheckAvailability(ctx, availVersions("rd"), other); got != 0 || len(dmm.requests()) != before {
		t.Fatalf("no key: gained %d, %d requests", got, len(dmm.requests())-before)
	}
	t.Setenv(dmmKeyEnv, dmmKey)
	h.svc.SetEvidence(nil)
	if got := h.svc.CheckAvailability(ctx, availVersions("rd"), other); got != 0 || len(dmm.requests()) != before {
		t.Fatal("asked without an evidence keeper")
	}
	if strings.Contains(n.logs.String(), dmmKey) {
		t.Error("DMM's key reached the logs")
	}
}

// Each call's answer stands on its own: rd answering and ad failing keeps rd's positives (applied and stored); only
// ad's hashes stay unmarked, and only they are asked again on the next search.
func TestDMMAvailabilityPartialFailureKeepsAnswers(t *testing.T) {
	n := newNet(t)
	dmm := &availServer{fail: "ad", listed: map[string][]string{"rd": {hx("1")}, "ad": {hx("2")}}}
	dmm.serve(t, n)
	p := evidencePolicy()
	h := newHarness(t, n, p, false)
	st := openStore(t)
	h.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: st, Policy: p, Now: h.clock}))
	ctx := context.Background()
	releases := func() []Release {
		return []Release{{Indexer: "torrentio", InfoHash: hx("1"), Privacy: policy.PrivacyPublic},
			{Indexer: "torrentio", InfoHash: hx("2"), Privacy: policy.PrivacyPublic}}
	}
	rs := releases()
	if got := h.svc.CheckAvailability(ctx, availVersions("rd", "ad"), rs); got != 1 {
		t.Fatalf("gained %d, want 1", got)
	}
	if !slices.Equal(rs[0].CachedOn, []string{"rd"}) || rs[1].CachedOn != nil {
		t.Fatalf("marks %v %v", rs[0].CachedOn, rs[1].CachedOn)
	}
	if got, want := cacheRows(t, st), map[string]string{"1111/rd": "cached feed:dmm/availability"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("provider_cache %v, want %v", got, want)
	}
	dmm.mu.Lock()
	dmm.fail = ""
	dmm.mu.Unlock()
	rs = releases()
	h.svc.ApplyCachedOn(ctx, rs)
	h.svc.CheckAvailability(ctx, availVersions("rd", "ad"), rs)
	reqs := dmm.requests()[2:]
	if len(reqs) != 1 || reqs[0].service != "ad" || !slices.Equal(reqs[0].hashes, []string{hx("1"), hx("2")}) {
		t.Fatalf("asked again %+v", reqs)
	}
	if !slices.Equal(rs[1].CachedOn, []string{"ad"}) {
		t.Fatalf("after recovery %v", rs[1].CachedOn)
	}
}

// A busy writer does not hold the search: storing DMM's answer gives up after availabilityStoreTimeout, and the
// search still gets this pass's marks.
func TestDMMAvailabilityStoreBounded(t *testing.T) {
	n := newNet(t)
	dmm := &availServer{listed: map[string][]string{"rd": {hx("1")}}}
	dmm.serve(t, n)
	p := evidencePolicy()
	h := newHarness(t, n, p, false)
	st := openStore(t)
	h.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: st, Policy: p, Now: h.clock}))
	old := availabilityStoreTimeout
	availabilityStoreTimeout = 200 * time.Millisecond
	t.Cleanup(func() { availabilityStoreTimeout = old })
	tx, err := st.Writer().Begin()
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		gained int
		rs     []Release
	}
	done := make(chan result, 1)
	go func() {
		rs := []Release{{Indexer: "torrentio", InfoHash: hx("1"), Privacy: policy.PrivacyPublic}}
		done <- result{h.svc.CheckAvailability(context.Background(), availVersions("rd"), rs), rs}
	}()
	var res result
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		_ = tx.Rollback()
		<-done
		t.Fatal("the search waited for the busy writer")
	}
	_ = tx.Rollback()
	if res.gained != 1 || !slices.Equal(res.rs[0].CachedOn, []string{"rd"}) {
		t.Fatalf("gained %d, %+v", res.gained, res.rs[0])
	}
	if rows := cacheRows(t, st); len(rows) != 0 {
		t.Fatalf("provider_cache %v", rows)
	}
}

// Availability evidence is feed evidence: it never replaces a live verdict of ours or a miss, and it replaces
// expired rows.
func TestAvailabilityEvidenceKeepsFirstPartyRows(t *testing.T) {
	p := evidencePolicy()
	st := openStore(t)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	e := NewEvidence(EvidenceConfig{Store: st, Policy: p, Now: func() time.Time { return now }})
	for _, q := range []struct {
		hash, state, src string
		exp              time.Time
	}{
		{hx("a"), "uncached", "router:add", now.Add(time.Hour)},
		{hx("b"), "miss", "router:miss", now.Add(time.Hour)},
		{hx("c"), "uncached", "router:add", now.Add(-time.Minute)},
	} {
		if _, err := st.Writer().Exec(`INSERT INTO provider_cache (info_hash, provider, state, source, checked_at,
			expires_at) VALUES (?, 'rd', ?, ?, ?, ?)`, q.hash, q.state, q.src, store.Millis(now), store.Millis(q.exp)); err != nil {
			t.Fatal(err)
		}
	}
	e.recordAvailability(context.Background(), map[string][]string{hx("a"): {"rd"}, hx("b"): {"rd"}, hx("c"): {"rd"}},
		"feed:dmm/availability")
	want := map[string]string{"aaaa/rd": "uncached router:add", "bbbb/rd": "miss router:miss",
		"cccc/rd": "cached feed:dmm/availability"}
	if got := cacheRows(t, st); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("provider_cache %v, want %v", got, want)
	}
}
