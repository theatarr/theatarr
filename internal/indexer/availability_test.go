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

	"github.com/theatarr/theatarr/internal/gateway"
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
	items  map[string]string   // service+hash -> the answer's row, instead of the default one
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
			if row, ok := s.items[body.Service+h]; ok {
				items = append(items, row)
			} else if slices.Contains(s.listed[body.Service], h) {
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

// A hash DMM lists on rd gains no rd evidence when Real-Debrid's name rule refuses its root name (filename or
// originalFilename) or a video the selection takes; samples, non-video files and the ad answer are not judged.
func TestDMMAvailabilityRDNames(t *testing.T) {
	n := newNet(t)
	row := func(c, filename, original string, paths ...string) string {
		var files []string
		for i, p := range paths {
			files = append(files, fmt.Sprintf(`{"file_id": %d, "path": %q, "bytes": 1}`, i+1, p))
		}
		return fmt.Sprintf(`{"hash": %q, "filename": %q, "originalFilename": %q, "files": [%s]}`, hx(c), filename,
			original, strings.Join(files, ","))
	}
	clean := "Show.S01.1080p.BluRay.x265"
	dmm := &availServer{items: map[string]string{
		"rd" + hx("1"): row("1", "Movie.2020.1080p.WEB-DL.mkv", clean, "/Movie.mkv"),
		"rd" + hx("2"): row("2", clean, "Movie.2020.720p.WEBRip.x264", "/Movie.mkv"),
		"rd" + hx("3"): row("3", clean, clean, "/Show/S01E01.mkv", "/Show/Show.S01E02.HDTV.x264.mkv"),
		"rd" + hx("4"): row("4", clean, clean, "/Show/S01E01.mkv", "/Show/Sample/WEB-DL.sample.mkv", "/Show/WEB-DL.nfo"),
		"rd" + hx("5"): row("5", clean, clean),
		"ad" + hx("1"): row("1", "Movie.2020.1080p.WEB-DL.mkv", "Movie.2020.1080p.WEB-DL.mkv", "/Movie.WEB-DL.mkv"),
	}}
	dmm.serve(t, n)
	h := newHarness(t, n, evidencePolicy(), false)
	src := h.svc.availabilitySource()
	if src == nil {
		t.Fatal("no availability source")
	}
	hashes := []string{hx("1"), hx("2"), hx("3"), hx("4"), hx("5")}
	got, err := src.availability(context.Background(), "rd", hashes)
	if err != nil || !slices.Equal(got, []string{hx("4"), hx("5")}) {
		t.Errorf("rd: %v %v", got, err)
	}
	got, err = src.availability(context.Background(), "ad", hashes)
	if err != nil || !slices.Equal(got, []string{hx("1")}) {
		t.Errorf("ad: %v %v", got, err)
	}
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

// One call per provider kind, rd then ad: the first 100 hashes in the order given that are left after the skips.
// Nothing beyond them is asked. Hashes asked lately do not use up the 100: the next search asks the next ones. A
// release with rd evidence is asked about on ad only; a release with evidence for both is not asked about.
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
	calls := func(from int) []string {
		var out []string
		for _, r := range dmm.requests()[from:] {
			out = append(out, fmt.Sprintf("%s:%d:%s", r.service, len(r.hashes), r.hashes[0][36:]))
		}
		return out
	}
	h.svc.CheckAvailability(ctx, availVersions("rd"), rs)
	if got := calls(0); !slices.Equal(got, []string{"rd:100:0001"}) {
		t.Fatalf("rd calls %v", got)
	}
	// The first 100 were just asked on rd: the next search asks the next 100, on rd; ad is new.
	h.svc.CheckAvailability(ctx, availVersions("rd", "ad"), rs)
	if got := calls(1); !slices.Equal(got, []string{"rd:100:0065", "ad:100:0001"}) { // 0x65 = 101
		t.Fatalf("calls after the skips %v", got)
	}

	// Fresh hashes, in reverse: the order given is the order asked.
	slices.Reverse(rs)
	for i := range rs {
		rs[i].InfoHash = fmt.Sprintf("%040x", 1000+i)
	}
	rs[0].CachedOn = []string{"rd"}
	rs[1].CachedOn = []string{"rd", "ad"}
	h.svc.CheckAvailability(ctx, availVersions("rd", "ad"), rs)
	reqs := dmm.requests()[3:]
	if len(reqs) != 2 || reqs[0].service != "rd" || reqs[1].service != "ad" {
		t.Fatalf("calls %+v", reqs)
	}
	if rd, ad := reqs[0].hashes, reqs[1].hashes; len(rd) != 100 || len(ad) != 100 || rd[0] != rs[2].InfoHash ||
		ad[0] != rs[0].InfoHash || ad[1] != rs[2].InfoHash || slices.Contains(rd, rs[0].InfoHash) ||
		slices.Contains(rd, rs[1].InfoHash) || slices.Contains(ad, rs[1].InfoHash) {
		t.Fatalf("rd %v\nad %v", rd[:3], ad[:3])
	}

	// Nothing lacks evidence: nothing is asked.
	for i := range rs {
		rs[i].CachedOn = []string{"rd", "ad"}
	}
	h.svc.CheckAvailability(ctx, availVersions("rd", "ad"), rs)
	if got := len(dmm.requests()); got != 5 {
		t.Fatalf("%d calls with every hash known, want none more", got-5)
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

// A manual search's pass (PurposeInteractive) does not wait for the DMM budget's pace: with one token it makes one
// call and stops, with none it makes no call; the hashes not asked stay unasked, and nothing logs a refusal. A cap
// still ends the pass with today's line, and an automatic pass still waits for its tokens.
func TestDMMAvailabilityManualSkipsPaceWait(t *testing.T) {
	releases := func(from int) []Release {
		var rs []Release
		for i := range 150 {
			rs = append(rs, Release{Indexer: "torrentio", InfoHash: fmt.Sprintf("%040x", from+i), Privacy: policy.PrivacyPublic})
		}
		return rs
	}
	setup := func(b *policy.Budget) (*availServer, *harness, *testNet) {
		n := newNet(t)
		dmm := &availServer{}
		dmm.serve(t, n)
		p := evidencePolicy()
		p.Budgets["dmm-key"] = b
		h := newHarness(t, n, p, false)
		h.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: openStore(t), Policy: p, Now: h.clock,
			Logger: slog.New(slog.NewJSONHandler(&n.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}))
		return dmm, h, n
	}
	manual := gateway.WithPurpose(context.Background(), gateway.PurposeInteractive)
	asked := func(h *harness) int {
		e := h.svc.evidence()
		e.mu.Lock()
		defer e.mu.Unlock()
		return len(e.asked)
	}

	// One token per 1.5 s (within the gateway's wait bound): the first call is made, the next is not due, and the
	// pass ends at once instead of waiting.
	dmm, h, n := setup(&policy.Budget{Requests: 1, Per: policy.Duration(1500 * time.Millisecond), Burst: 1})
	start := time.Now()
	h.svc.CheckAvailability(manual, availVersions("rd", "ad"), releases(1))
	if got := len(dmm.requests()); got != 1 {
		t.Fatalf("one token: %d calls, want 1", got)
	}
	if got := asked(h); got != 100 {
		t.Fatalf("one token: %d hashes marked asked, want the first call's 100", got)
	}
	// The bucket is empty: no call, nothing marked.
	h.svc.CheckAvailability(manual, availVersions("rd"), releases(1000))
	if got := len(dmm.requests()); got != 1 {
		t.Fatalf("empty bucket: %d calls, want none more", got-1)
	}
	if got := asked(h); got != 100 {
		t.Fatalf("empty bucket: %d hashes marked asked", got)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("the manual passes took %v", d)
	}
	logs := n.logs.String()
	if strings.Contains(logs, "DMM availability not checked") || strings.Contains(logs, "request refused") {
		t.Errorf("a pace skip logged as a refusal: %s", logs)
	}

	// An hourly cap of one: the second call is refused for the cap and logs as today.
	dmm, h, n = setup(&policy.Budget{Requests: 600, Per: policy.Duration(time.Minute), Burst: 100, Hourly: 1})
	h.svc.CheckAvailability(manual, availVersions("rd", "ad"), releases(1))
	if got := len(dmm.requests()); got != 1 {
		t.Fatalf("cap: %d calls, want 1", got)
	}
	if !strings.Contains(n.logs.String(), "DMM availability not checked") {
		t.Error("a cap refusal did not log the usual line")
	}

	// Automatic: ten tokens a second, burst one; the pass waits for the token of its second call.
	dmm, h, _ = setup(&policy.Budget{Requests: 10, Per: policy.Duration(time.Second), Burst: 1})
	start = time.Now()
	h.svc.CheckAvailability(gateway.WithPurpose(context.Background(), gateway.PurposeWanted), availVersions("rd", "ad"),
		releases(1))
	if got := len(dmm.requests()); got != 2 {
		t.Fatalf("automatic: %d calls, want 2", got)
	}
	if d := time.Since(start); d < 50*time.Millisecond {
		t.Errorf("automatic pass took %v: it did not wait for its tokens", d)
	}
}
