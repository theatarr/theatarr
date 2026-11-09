package indexer

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

const (
	rdKeyEnv = "THEATARR_TEST_RD_KEY"
	adKeyEnv = "THEATARR_TEST_AD_KEY"
	tbKeyEnv = "THEATARR_TEST_TB_KEY"
	rdKey    = "rd-secret-key-0123456789abcdef"
	adKey    = "ad-secret-key-0123456789abcdef"
	tbKey    = "tb-secret-key-0123456789abcdef"
)

func hx(c string) string { return strings.Repeat(c, 40) }

// evidencePolicy is testPolicy plus DMM's AllDebrid feed and the three providers.
func evidencePolicy() *policy.Policy {
	p := testPolicy()
	p.Indexers["dmm-ad"] = &policy.Indexer{Kind: "dmm", URL: "http://dmm.test/api/torznab/ad/cached",
		APIKeyEnv: dmmKeyEnv, Privacy: policy.PrivacyPublic, Budget: "dmm-key"}
	p.Providers = map[string]*policy.Provider{
		"rd": {Kind: provider.KindRealDebrid, Order: 1, URL: "http://rd.test/rest/1.0", APIKeyEnv: rdKeyEnv},
		"ad": {Kind: provider.KindAllDebrid, Order: 2, URL: "http://ad.test/v4", APIKeyEnv: adKeyEnv},
		"tb": {Kind: provider.KindTorBox, Order: 3, URL: "http://tb.test/v1", APIKeyEnv: tbKeyEnv},
	}
	return p
}

// dmmFeed answers a DMM feed with one item per hash.
func dmmFeed(hashes ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>`)
	fmt.Fprintf(&b, `<torznab:response offset="0" total="%d"/>`, len(hashes))
	for i, h := range hashes {
		fmt.Fprintf(&b, `<item><title>DMM.Release.%d.2160p {imdb-tt0133093}</title><guid>d%d</guid><size>%d</size>`+
			`<torznab:attr name="infohash" value="%s"/><torznab:attr name="seeders" value="1"/></item>`, i, i, 1000+i, h)
	}
	b.WriteString(`</channel></rss>`)
	return b.String()
}

func serveDMM(n *testNet, rec *recorder, byPath map[string][]string) {
	n.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		fmt.Fprint(w, dmmFeed(byPath[r.URL.Path]...))
	}))
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

func byHash(rs []Release, hash, indexer string) *Release {
	for i := range rs {
		if rs[i].InfoHash == hash && rs[i].Indexer == indexer {
			return &rs[i]
		}
	}
	return nil
}

func cacheRows(t *testing.T, st *store.Store) map[string]string {
	t.Helper()
	rows, err := st.Reader().Query(`SELECT info_hash, provider, state, source FROM provider_cache`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var h, p, s, src string
		if err := rows.Scan(&h, &p, &s, &src); err != nil {
			t.Fatal(err)
		}
		out[h[:4]+"/"+p] = s + " " + src
	}
	return out
}

// A Torrentio release whose hash DMM's AllDebrid feed lists in the same search is cached on ad; the evidence is
// stored and survives into a later search that never asks DMM, until the TTL ends.
func TestCrossFeedCachedOn(t *testing.T) {
	loadFixtures(t, "torrentio_movie.json")
	n := newNet(t)
	n.serve("torrentio.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixtureBytes("torrentio_movie.json"))
	}))
	serveDMM(n, &recorder{}, map[string][]string{
		"/api/torznab/ad/cached/api": {hx("1"), hx("4")},
		"/api/torznab/rd/cached/api": {hx("2")},
	})
	p := evidencePolicy()
	h := newHarness(t, n, p, false)
	st := openStore(t)
	h.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: st, Policy: p, Now: h.clock}))
	q := Query{Type: QueryMovie, IMDb: "tt0133093", Title: "The Matrix"}

	var rels []Release
	for _, k := range []string{"torrentio", "dmm-ad", "dmm"} {
		rels = append(rels, search(t, h, k, q).Releases...)
	}
	h.svc.ApplyCachedOn(context.Background(), rels)
	if r := byHash(rels, hx("1"), "torrentio"); r == nil || !slices.Equal(r.CachedOn, []string{"ad"}) ||
		r.CachedVia["ad"] != "dmm-ad" {
		t.Fatalf("torrentio 111: %+v", r)
	}
	if r := byHash(rels, hx("2"), "torrentio"); r == nil || !slices.Equal(r.CachedOn, []string{"rd"}) {
		t.Fatalf("torrentio 222: %+v", r)
	}
	if r := byHash(rels, hx("3"), "torrentio"); r == nil || r.CachedOn != nil || r.CachedList() != nil {
		t.Fatalf("torrentio 333 has no evidence: %+v", r)
	}
	if r := byHash(rels, hx("4"), "dmm-ad"); r == nil || !slices.Equal(r.CachedList(), []string{"ad"}) {
		t.Fatalf("dmm-ad 444: %+v", r)
	}
	want := map[string]string{"1111/ad": "cached feed:dmm-ad", "4444/ad": "cached feed:dmm-ad",
		"2222/rd": "cached feed:dmm"}
	if got := cacheRows(t, st); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("provider_cache %v, want %v", got, want)
	}

	// A later search without DMM: the stored evidence fills cachedOn.
	h.advance(2 * time.Hour)
	later := search(t, h, "torrentio", q).Releases
	h.svc.ApplyCachedOn(context.Background(), later)
	if r := byHash(later, hx("1"), "torrentio"); r == nil || !slices.Equal(r.CachedOn, []string{"ad"}) ||
		r.CachedVia["ad"] != "feed:dmm-ad" {
		t.Fatalf("later 111: %+v", r)
	}
	if r := byHash(later, hx("2"), "torrentio"); r == nil || !slices.Equal(r.CachedOn, []string{"rd"}) {
		t.Fatalf("later 222: %+v", r)
	}
	// Past the TTL: nothing.
	h.advance(policy.DefaultCacheEvidenceTTL)
	expired := search(t, h, "torrentio", q).Releases
	h.svc.ApplyCachedOn(context.Background(), expired)
	for _, r := range expired {
		if r.CachedOn != nil {
			t.Errorf("expired evidence still applied: %s %v", r.InfoHash, r.CachedOn)
		}
	}
}

// Feed evidence never replaces a live first-party verdict or a miss; it does replace expired rows and its own.
func TestFeedEvidenceKeepsFirstPartyRows(t *testing.T) {
	p := evidencePolicy()
	st := openStore(t)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	e := NewEvidence(EvidenceConfig{Store: st, Policy: p, Now: func() time.Time { return now }})
	ms := store.Millis
	for _, q := range []struct {
		hash, prov, state, src string
		exp                    time.Time
	}{
		{hx("a"), "ad", "uncached", "router:add", now.Add(time.Hour)},
		{hx("b"), "ad", "miss", "router:miss", now.Add(time.Hour)},
		{hx("c"), "ad", "uncached", "router:add", now.Add(-time.Minute)},
		{hx("d"), "ad", "cached", "feed:dmm-ad", now.Add(time.Hour)},
	} {
		if _, err := st.Writer().Exec(`INSERT INTO provider_cache (info_hash, provider, state, source, checked_at,
			expires_at) VALUES (?, ?, ?, ?, ?, ?)`, q.hash, q.prov, q.state, q.src, ms(now), ms(q.exp)); err != nil {
			t.Fatal(err)
		}
	}
	var rs []Release
	for _, c := range []string{"a", "b", "c", "d"} {
		rs = append(rs, Release{Indexer: "dmm-ad", Kind: "dmm", InfoHash: hx(c), Privacy: policy.PrivacyPublic,
			Extras: map[string]string{"cached_on": "ad"}})
	}
	// A private-labelled release is stored like any other (privacy is a label).
	rs = append(rs, Release{Indexer: "dmm-ad", Kind: "dmm", InfoHash: hx("e"), Privacy: policy.PrivacyPrivate,
		Extras: map[string]string{"cached_on": "ad"}})
	e.recordFeed(context.Background(), rs, false)
	want := map[string]string{"aaaa/ad": "uncached router:add", "bbbb/ad": "miss router:miss",
		"cccc/ad": "cached feed:dmm-ad", "dddd/ad": "cached feed:dmm-ad", "eeee/ad": "cached feed:dmm-ad"}
	if got := cacheRows(t, st); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("provider_cache %v, want %v", got, want)
	}
}

// tbServer is a TorBox checkcached endpoint: cached answers the hashes it holds.
type tbServer struct {
	mu     sync.Mutex
	asks   [][]string
	cached map[string]bool
}

func (s *tbServer) serve(t *testing.T, n *testNet) { n.serve("tb.test", s.handler(t)) }

func (s *tbServer) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/api/torrents/checkcached" {
			t.Errorf("torbox: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+tbKey {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"success": false, "error": "BAD_TOKEN", "detail": "bad token"}`)
			return
		}
		hashes := strings.Split(r.URL.Query().Get("hash"), ",")
		s.mu.Lock()
		s.asks = append(s.asks, hashes)
		s.mu.Unlock()
		var parts []string
		for _, h := range hashes {
			if s.cached[h] {
				parts = append(parts, fmt.Sprintf(`%q: {"name": "x", "size": 1000, "hash": %q}`, h, h))
			}
		}
		fmt.Fprintf(w, `{"success": true, "detail": "ok", "data": {%s}}`, strings.Join(parts, ","))
	})
}

func (s *tbServer) requests() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]string(nil), s.asks...)
}

func tbChecker(t *testing.T, p *policy.Policy, h *harness) map[string]provider.Provider {
	t.Helper()
	t.Setenv(tbKeyEnv, tbKey)
	c, err := provider.New("tb", p.Providers["tb"], tbKey, h.gw)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]provider.Provider{"tb": c}
}

// The TorBox check: the best hashes of indexers a debrid version being searched lists with TorBox allowed, batched
// per account, bounded by top and per_hour; answers stored, positives on every release of the hash, nothing asked
// twice while the answer lives, an off switch.
func TestTorBoxCheckAtSearch(t *testing.T) {
	n := newNet(t)
	tb := &tbServer{cached: map[string]bool{hx("1"): true, hx("3"): true}}
	tb.serve(t, n)
	p := evidencePolicy()
	p.Router = &policy.Router{CacheEvidence: &policy.CacheEvidence{TorBoxCheck: &policy.TorBoxCheck{Top: 2, PerHour: 2}}}
	h := newHarness(t, n, p, false)
	st := openStore(t)
	h.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: st, Policy: p, Checkers: tbChecker(t, p, h), Now: h.clock}))
	rel := func(ix, c string) *Release {
		return &Release{Indexer: ix, InfoHash: hx(c), Privacy: policy.PrivacyPublic}
	}
	// The private-labelled tracker is not in the version searched: its hash is not sent.
	private := &Release{Indexer: "tracker", InfoHash: hx("9"), Privacy: policy.PrivacyPrivate}
	onTB := rel("dmm", "8")
	onTB.CachedOn = []string{"tb"}
	r1, r2, r3, other1 := rel("torrentio", "1"), rel("torrentio", "2"), rel("zilean", "3"), rel("prowlarr", "1")
	ctx := context.Background()
	vs := []*policy.VersionPolicy{{Storage: policy.StorageDebrid, Indexers: []string{"dmm", "torrentio", "zilean", "prowlarr"},
		Providers: []string{"rd", "tb"}}}

	if n := h.svc.CheckCached(ctx, vs, []*Release{private, onTB, r1, r2, r3}, []*Release{other1}); n != 2 {
		t.Fatalf("checked %d hashes, want 2 (top)", n)
	}
	asks := tb.requests()
	if len(asks) != 1 || !slices.Equal(asks[0], []string{hx("1"), hx("2")}) {
		t.Fatalf("requests %v", asks)
	}
	if !slices.Equal(r1.CachedOn, []string{"tb"}) || !slices.Equal(other1.CachedOn, []string{"tb"}) ||
		r1.CachedVia["tb"] != "check:torbox" || r2.CachedOn != nil || r3.CachedOn != nil {
		t.Fatalf("after the check: r1 %v other %v r2 %v r3 %v", r1.CachedOn, other1.CachedOn, r2.CachedOn, r3.CachedOn)
	}
	want := map[string]string{"1111/tb": "cached check:torbox", "2222/tb": "uncached check:torbox"}
	if got := cacheRows(t, st); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("provider_cache %v, want %v", got, want)
	}

	// Again: 1 and 2 are known (stored), so only 3 is asked, from its own indexer.
	r1b, r2b := rel("torrentio", "1"), rel("torrentio", "2")
	if n := h.svc.CheckCached(ctx, vs, []*Release{r1b, r2b, r3}, nil); n != 1 {
		t.Fatalf("second check: %d hashes", n)
	}
	if asks = tb.requests(); len(asks) != 2 || !slices.Equal(asks[1], []string{hx("3")}) {
		t.Fatalf("requests %v", asks)
	}
	if !slices.Equal(r3.CachedOn, []string{"tb"}) {
		t.Errorf("r3 %v", r3.CachedOn)
	}
	// The hourly cap (2) is spent: nothing is asked.
	if n := h.svc.CheckCached(ctx, vs, []*Release{rel("torrentio", "5")}, nil); n != 0 || len(tb.requests()) != 2 {
		t.Fatalf("over the cap: %d hashes, %d requests", n, len(tb.requests()))
	}
	h.advance(time.Hour + time.Second)
	if n := h.svc.CheckCached(ctx, vs, []*Release{rel("torrentio", "5")}, nil); n != 1 {
		t.Fatalf("after an hour: %d hashes", n)
	}

	// Off switch.
	p.Router.CacheEvidence.TorBoxCheck.Enabled = boolp(false)
	h.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: st, Policy: p, Checkers: tbChecker(t, p, h), Now: h.clock}))
	before := len(tb.requests())
	if n := h.svc.CheckCached(ctx, vs, []*Release{rel("torrentio", "6")}, nil); n != 0 || len(tb.requests()) != before {
		t.Fatalf("disabled: %d hashes, %d requests", n, len(tb.requests())-before)
	}
	// No evidence keeper: nothing either.
	h.svc.SetEvidence(nil)
	if n := h.svc.CheckCached(ctx, vs, []*Release{rel("torrentio", "6")}, nil); n != 0 {
		t.Fatal("checked without an evidence keeper")
	}
}

// The TorBox check follows the user's choice: a private-labelled indexer's hashes go to TorBox once a debrid version
// lists it with TorBox allowed, and not when its providers allowlist leaves TorBox out or the version is not debrid.
func TestTorBoxCheckFollowsIndexerProviders(t *testing.T) {
	n := newNet(t)
	tb := &tbServer{cached: map[string]bool{}}
	tb.serve(t, n)
	p := evidencePolicy()
	h := newHarness(t, n, p, false)
	st := openStore(t)
	h.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: st, Policy: p, Checkers: tbChecker(t, p, h), Now: h.clock}))
	ctx := context.Background()
	rel := &Release{Indexer: "tracker", InfoHash: hx("7"), Privacy: policy.PrivacyPrivate}
	debrid := &policy.VersionPolicy{Storage: policy.StorageDebrid, Indexers: []string{"tracker"}, Providers: []string{"rd", "tb"}}
	seedbox := &policy.VersionPolicy{Storage: policy.StorageSeedbox, Indexers: []string{"tracker"}}

	p.Indexers["tracker"].Providers = []string{"rd"}
	if n := h.svc.CheckCached(ctx, []*policy.VersionPolicy{debrid, seedbox}, []*Release{rel}, nil); n != 0 || len(tb.requests()) != 0 {
		t.Fatalf("TorBox not allowed: %d hashes, %d requests", n, len(tb.requests()))
	}
	p.Indexers["tracker"].Providers = nil
	if n := h.svc.CheckCached(ctx, []*policy.VersionPolicy{seedbox}, []*Release{rel}, nil); n != 0 {
		t.Fatalf("seedbox version: %d hashes", n)
	}
	if n := h.svc.CheckCached(ctx, []*policy.VersionPolicy{debrid}, []*Release{rel}, nil); n != 1 {
		t.Fatalf("listed with TorBox allowed: %d hashes", n)
	}
	if asks := tb.requests(); len(asks) != 1 || !slices.Equal(asks[0], []string{hx("7")}) {
		t.Fatalf("requests %v", asks)
	}
}

// keyNet records every request on every host with its URL and headers.
type keyNet struct {
	mu   sync.Mutex
	seen []string // "host URL headers"
}

func (k *keyNet) wrap(host string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k.mu.Lock()
		k.seen = append(k.seen, fmt.Sprintf("%s %s %v", host, r.URL.String(), r.Header))
		k.mu.Unlock()
		h.ServeHTTP(w, r)
	})
}

// No debrid key goes anywhere but its own provider: Torrentio is asked with the plain config (no key, no debrid
// option), DMM with DMM's key only, and TorBox's check carries the TorBox key in its header only.
func TestDebridKeysOnlyGoToTheirProvider(t *testing.T) {
	t.Setenv(rdKeyEnv, rdKey)
	t.Setenv(adKeyEnv, adKey)
	loadFixtures(t, "torrentio_movie.json", "torrentio_series.json")
	n := newNet(t)
	kn := &keyNet{}
	n.serve("torrentio.test", kn.wrap("torrentio.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/series/") {
			w.Write(fixtureBytes("torrentio_series.json"))
			return
		}
		w.Write(fixtureBytes("torrentio_movie.json"))
	})))
	dmmRec := &recorder{}
	n.serve("dmm.test", kn.wrap("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dmmRec.add(r)
		fmt.Fprint(w, dmmFeed(hx("1")))
	})))
	tb := &tbServer{cached: map[string]bool{hx("1"): true}}
	n.serve("tb.test", kn.wrap("tb.test", tb.handler(t)))
	p := evidencePolicy()
	h := newHarness(t, n, p, false)
	st := openStore(t)
	h.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: st, Policy: p, Checkers: tbChecker(t, p, h), Now: h.clock}))

	var rels []Release
	for _, s := range []struct {
		key string
		q   Query
	}{
		{"torrentio", Query{Type: QueryMovie, IMDb: "tt0133093"}},
		{"torrentio", Query{Type: QuerySeries, IMDb: "tt0903747", Season: 1, Episode: 2}},
		{"dmm", Query{Type: QueryMovie, IMDb: "tt0133093"}},
		{"dmm-ad", Query{Type: QueryMovie, IMDb: "tt0133093"}},
	} {
		rels = append(rels, search(t, h, s.key, s.q).Releases...)
	}
	h.svc.ApplyCachedOn(context.Background(), rels)
	var ptrs []*Release
	for i := range rels {
		ptrs = append(ptrs, &rels[i])
	}
	vs := []*policy.VersionPolicy{{Storage: policy.StorageDebrid, Indexers: []string{"torrentio", "dmm", "dmm-ad"},
		Providers: []string{"tb"}}}
	if h.svc.CheckCached(context.Background(), vs, ptrs, nil) == 0 {
		t.Fatal("no TorBox check made")
	}

	kn.mu.Lock()
	seen := append([]string(nil), kn.seen...)
	kn.mu.Unlock()
	hosts := map[string]int{}
	for _, s := range seen {
		host, rest, _ := strings.Cut(s, " ")
		hosts[host]++
		for name, key := range map[string]string{"rd": rdKey, "ad": adKey, "tb": tbKey} {
			if strings.Contains(rest, key) && !(host == "tb.test" && name == "tb") {
				t.Errorf("the %s key reached %s: %s", name, host, rest)
			}
		}
		if host == "torrentio.test" {
			u, _, _ := strings.Cut(rest, " ")
			lu := strings.ToLower(u)
			if strings.Contains(u, "?") {
				t.Errorf("torrentio URL with a query: %s", u)
			}
			for _, opt := range []string{"realdebrid", "alldebrid", "torbox", "premiumize", "debridlink", "offcloud",
				"putio", "easydebrid", "apikey", "key="} {
				if strings.Contains(lu, opt) {
					t.Errorf("torrentio URL carries %q: %s", opt, u)
				}
			}
		}
		if host == "tb.test" && strings.Contains(strings.SplitN(rest, " ", 2)[0], tbKey) {
			t.Errorf("TorBox key in the URL: %s", rest)
		}
	}
	if hosts["torrentio.test"] != 2 || hosts["dmm.test"] < 2 || hosts["tb.test"] != 1 {
		t.Errorf("requests by host %v", hosts)
	}
	for _, r := range dmmRec.all() {
		if r.URL.Query().Get("apikey") != dmmKey {
			t.Errorf("DMM asked without its own key: %s", r.URL.Path)
		}
	}
	for _, key := range []string{rdKey, adKey, tbKey, dmmKey} {
		if strings.Contains(n.logs.String(), key) {
			t.Errorf("a key reached the logs")
		}
	}
}

func TestCachedCodes(t *testing.T) {
	r := Release{Indexer: "dmm", Extras: map[string]string{"cached_on": "AD, rd,xx"}}
	if got := r.CachedList(); !slices.Equal(got, []string{"rd", "ad"}) {
		t.Errorf("extras only: %v", got)
	}
	r.addCached("tb", "check:torbox")
	if got := r.CachedCodes(); got["tb"] != "check:torbox" || got["rd"] != "dmm" || len(got) != 3 {
		t.Errorf("codes %v", got)
	}
	if (&Release{}).CachedList() != nil {
		t.Error("no evidence must be nil")
	}
}
