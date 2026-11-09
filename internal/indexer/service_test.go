package indexer

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

func TestSingleFlightAndCache(t *testing.T) {
	loadFixtures(t, "torrentio_movie.json")
	n := newNet(t)
	var hits atomic.Int32
	release := make(chan struct{})
	n.serve("torrentio.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release
		w.Write(fixtureBytes("torrentio_movie.json"))
	}))
	h := newHarness(t, n, testPolicy(), false)
	q := Query{Type: QueryMovie, IMDb: "tt0133093", Title: "The Matrix"}

	const callers = 6
	var wg sync.WaitGroup
	results := make([]*Result, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// The same query, written differently: the normalised key is the same.
			qi := q
			if i%2 == 1 {
				qi.IMDb, qi.Title = "0133093", "  the   MATRIX "
			}
			results[i], errs[i] = h.svc.Search(context.Background(), "torrentio", qi)
		}()
	}
	// Wait until the one upstream request is in, then let it answer.
	deadline := time.Now().Add(5 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // let the other callers join
	close(release)
	wg.Wait()
	if hits.Load() != 1 {
		t.Fatalf("%d upstream requests for %d identical searches", hits.Load(), callers)
	}
	shared := 0
	for i, r := range results {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if len(r.Releases) != 3 {
			t.Fatalf("caller %d: %d releases", i, len(r.Releases))
		}
		if r.Shared {
			shared++
		}
	}
	if shared != callers-1 {
		t.Errorf("%d shared results, want %d", shared, callers-1)
	}
	// Each caller has its own copy.
	results[0].Releases[0].Title = "changed"
	if results[1].Releases[0].Title == "changed" {
		t.Error("callers share the slice")
	}

	// Within the TTL: cached, no request.
	r, err := h.svc.Search(context.Background(), "torrentio", q)
	if err != nil || !r.Cached || hits.Load() != 1 || r.Releases[0].Title == "changed" {
		t.Fatalf("cached: %v cached=%v hits=%d", err, r != nil && r.Cached, hits.Load())
	}
	// After the TTL: upstream again.
	h.advance(DefaultCacheTTL + time.Second)
	r, err = h.svc.Search(context.Background(), "torrentio", q)
	if err != nil || r.Cached || hits.Load() != 2 {
		t.Fatalf("after TTL: %v cached=%v hits=%d", err, r != nil && r.Cached, hits.Load())
	}
	// Another query is another key.
	if _, err := h.svc.Search(context.Background(), "torrentio", Query{Type: QueryMovie, IMDb: "tt0000002"}); err != nil || hits.Load() != 3 {
		t.Fatalf("other query: %v hits=%d", err, hits.Load())
	}
}

func TestSingleFlightCallerGivesUp(t *testing.T) {
	loadFixtures(t, "torrentio_movie.json")
	n := newNet(t)
	release := make(chan struct{})
	var hits atomic.Int32
	n.serve("torrentio.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release
		w.Write(fixtureBytes("torrentio_movie.json"))
	}))
	h := newHarness(t, n, testPolicy(), false)
	q := Query{Type: QueryMovie, IMDb: "tt0133093"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := h.svc.Search(ctx, "torrentio", q)
		done <- err
	}()
	for hits.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	// A caller already waiting keeps the shared search alive when the first caller leaves.
	got := make(chan *Result, 1)
	go func() {
		r, _ := h.svc.Search(context.Background(), "torrentio", q)
		got <- r
	}()
	for {
		h.svc.mu.Lock()
		waiting := h.svc.inflight["torrentio\x00"+q.Key()].waiters == 2
		h.svc.mu.Unlock()
		if waiting {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("cancelled caller: %v", err)
	}
	close(release)
	r := <-got
	if r == nil || len(r.Releases) != 3 || hits.Load() != 1 {
		t.Fatalf("joined result %v, hits %d", r, hits.Load())
	}
}

func TestErrorsAreNotCached(t *testing.T) {
	n := newNet(t)
	var hits atomic.Int32
	n.serve("torrentio.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			http.Error(w, "nope", http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, `{"streams":[]}`)
	}))
	h := newHarness(t, n, testPolicy(), false)
	q := Query{Type: QueryMovie, IMDb: "tt0133093"}
	if _, err := h.svc.Search(context.Background(), "torrentio", q); err == nil {
		t.Fatal("502 accepted")
	}
	if _, err := h.svc.Search(context.Background(), "torrentio", q); err != nil || hits.Load() != 2 {
		t.Fatalf("retry: %v hits %d", err, hits.Load())
	}
}

func TestPlanSearch(t *testing.T) {
	n := newNet(t)
	p := testPolicy()
	h := newHarness(t, n, p, false)

	plan := PlanSearch(p, h.svc, Title{Kind: policy.KindMovie, Title: "The Matrix", Year: 1999, IMDb: "tt0133093", TMDb: 603})
	var order []string
	for _, s := range plan.Steps {
		order = append(order, s.Indexer)
	}
	// By kind (torznab, dmm, torrentio, zilean), then key; privacy is a label and does not order the plan. The
	// private tracker serves the versions that list it.
	if fmt.Sprint(order) != "[prowlarr tracker dmm torrentio zilean]" {
		t.Fatalf("order %v (skipped %v)", order, plan.Skipped)
	}
	if fmt.Sprint(plan.Steps[1].Versions) != "[movie-seedbox]" {
		t.Errorf("tracker step %+v", plan.Steps[1])
	}
	if fmt.Sprint(plan.Steps[0].Versions) != "[movie-remux movie-seedbox]" {
		t.Errorf("prowlarr versions %v", plan.Steps[0].Versions)
	}
	if len(plan.Steps[0].Queries) != 1 || plan.Steps[0].Queries[0].Type != QueryMovie {
		t.Errorf("queries %+v", plan.Steps[0].Queries)
	}
	prioritySvc, err := New(p, h.gw, Options{Priority: map[string]int{"zilean": -1}})
	if err != nil {
		t.Fatal(err)
	}
	priorityPlan := PlanSearch(p, prioritySvc, Title{Kind: policy.KindMovie, Title: "The Matrix", IMDb: "tt0133093"})
	if len(priorityPlan.Steps) == 0 || priorityPlan.Steps[0].Indexer != "zilean" {
		t.Errorf("settings priority was not applied: %+v", priorityPlan.Steps)
	}

	// Anime movie with an AniList id: SeaDex first. Without it, SeaDex is skipped with a reason.
	plan = PlanSearch(p, h.svc, Title{Kind: policy.KindMovie, Anime: true, Title: "Akira", AniList: 47})
	if plan.Steps[0].Indexer != "seadex" {
		t.Errorf("anime plan %+v", plan.Steps)
	}
	plan = PlanSearch(p, h.svc, Title{Kind: policy.KindMovie, Anime: true, Title: "Akira"})
	if _, ok := plan.Skipped["seadex"]; !ok {
		t.Errorf("seadex not skipped without an AniList id: %+v", plan)
	}

	// Series: one query per season, no episode fan-out; Torrentio needs the IMDb id.
	plan = PlanSearch(p, h.svc, Title{Kind: policy.KindSeries, Title: "Show", TVDB: 1, Seasons: []int{1, 2}})
	for _, s := range plan.Steps {
		if s.Indexer == "torrentio" || s.Indexer == "dmm" {
			t.Errorf("%s planned without an IMDb id", s.Indexer)
		}
		for _, q := range s.Queries {
			if q.Episode != 0 {
				t.Errorf("episode fan-out: %+v", q)
			}
		}
	}
	if len(plan.Steps) != 1 || plan.Steps[0].Indexer != "zilean" || len(plan.Steps[0].Queries) != 2 {
		t.Errorf("series plan %+v skipped %v", plan.Steps, plan.Skipped)
	}

	// Privacy is a label: a debrid version that lists the private tracker searches it (the user's choice).
	p.Versions["movie-remux"].Indexers = append(p.Versions["movie-remux"].Indexers, "tracker")
	delete(p.Versions, "movie-seedbox")
	plan = PlanSearch(p, h.svc, Title{Kind: policy.KindMovie, Title: "The Matrix", IMDb: "tt0133093"})
	planned := false
	for _, s := range plan.Steps {
		if s.Indexer == "tracker" {
			planned = fmt.Sprint(s.Versions) == "[movie-remux]"
		}
	}
	if !planned {
		t.Errorf("the listed private tracker was not planned: %+v skipped %v", plan.Steps, plan.Skipped)
	}
}

func TestLinkKeyed(t *testing.T) {
	for u, want := range map[string]bool{
		"":                                  false,
		"magnet:?xt=urn:btih:" + hex40("a"): false,
		"magnet:?xt=urn:btih:" + hex40("a") + "&dn=x&tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337%2Fannounce":        false,
		"magnet:?xt=urn:btih:" + hex40("a") + "&tr=https%3A%2F%2Ftracker.test%2Fannounce%3Fpasskey%3Dabc":            true,
		"magnet:?xt=urn:btih:" + hex40("a") + "&tr=https%3A%2F%2Ftracker.test%2Fa1b2c3d4e5f6a7b8c9d0e1f2%2Fannounce": true,
		"https://nyaa.si/download/123.torrent":                               false,
		"http://prowlarr.test/1/download?apikey=x&link=y":                    true,
		"https://tracker.test/rss/download/a1b2c3d4e5f6a7b8c9d0e1/1.torrent": true,
		"https://user:pass@tracker.test/x.torrent":                           true,
		"https://torrentio.test/realdebrid=k/stream":                         true,
	} {
		if got := NewLink(u).Keyed(); got != want {
			t.Errorf("%q: keyed %v, want %v", u, got, want)
		}
	}
}

func hex40(c string) string {
	s := ""
	for range 40 {
		s += c
	}
	return s
}

func TestHashes(t *testing.T) {
	if NormalizeHash("AEBAGBAFAYDQQCIKBMGA2DQPCAIREEYU") != "0102030405060708090a0b0c0d0e0f1011121314" {
		t.Error("base32")
	}
	if NormalizeHash("ABCDEF0123456789ABCDEF0123456789ABCDEF01") != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Error("hex")
	}
	if NormalizeHash("<redacted>") != "" || NormalizeHash("abc") != "" {
		t.Error("junk accepted")
	}
	if BareMagnet("ABCDEF0123456789ABCDEF0123456789ABCDEF01").URL() != "magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01" {
		t.Error("bare magnet")
	}
	if HashFromMagnet("magnet:?dn=x&xt=urn:btih:AEBAGBAFAYDQQCIKBMGA2DQPCAIREEYU&tr=x") != "0102030405060708090a0b0c0d0e0f1011121314" {
		t.Error("magnet hash")
	}
}

func TestRealDebridRefuses(t *testing.T) {
	for _, c := range []struct {
		name  string
		loose bool
		want  bool
	}{
		{"Movie.2025.2160p.WEB-DL.DDP5.1.H.265-GRP", false, true},
		{"Movie.2025.2160p.web-dl.DDP5.1-GRP", false, false}, // exact case since 2026-10-14
		{"Movie.2025.2160p.Web-Dl-GRP", false, false},
		{"Movie.2025.1080p.WEB.H264-GRP", false, true},
		{"Movie.2025.1080p.WEB.h264-GRACE", false, false},
		{"Movie.2025.1080p.WEB.x264-GRP", false, true},
		{"Movie.2025.1080p.WEB.X264-GRP", false, false},
		{"Movie.2025.1080p.WEBRip.x264", false, true},
		{"Movie.2025.1080p.webrip", false, true}, // the rip family in any case
		{"Movie.2025.PreDVDRip", false, true},
		{"Movie.2025.1080p.BluRay.DTS-GRP", false, true},
		{"Movie.2025.UHD.BluRay.DTS-FraMeSToR", false, false},
		{"Movie.2025.BluRay.DTS", false, false},
		{"Movie.2025.1080p.bluray.x264", false, true},
		{"Movie 2025 1080p BluRay x264", false, false},
		{"Movie 2025 1080p BluRay x264", true, true},
		{"Movie 2025 1080p WEB DL", true, false}, // WEB.DL is taken
		{"Movie.2025.2160p.WEB.DL.DV", false, false},
		{"Movie.2025.2160p.BluRay.REMUX", false, false},
	} {
		if got := RealDebridRefuses(c.name, c.loose); got != c.want {
			t.Errorf("%q loose=%v: %v, want %v", c.name, c.loose, got, c.want)
		}
	}
	if !RealDebridRefusesRelease(&Release{Kind: "zilean", Title: "X 2025 HDTV x264"}) ||
		!RealDebridRefusesRelease(&Release{Kind: "torrentio", Title: "X 2025 HDTV x264"}) ||
		!RealDebridRefusesRelease(&Release{Kind: "dmm", Title: "X 2025 HDTV x264", Extras: map[string]string{"cached_on": "rd,ad"}}) ||
		RealDebridRefusesRelease(&Release{Kind: "dmm", Title: "X 2025 HDTV x264", Extras: map[string]string{"cached_on": "ad"}}) {
		t.Error("loose by kind")
	}
}

func TestQueryKey(t *testing.T) {
	a := Query{Type: QueryMovie, IMDb: "0133093", Title: " The  Matrix", Categories: []int{2045, 2000}}
	b := Query{Type: QueryMovie, IMDb: "tt0133093", Title: "the matrix", Categories: []int{2000, 2045}}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	if a.Key() != b.Key() {
		t.Errorf("%q != %q", a.Key(), b.Key())
	}
	c := b
	c.Year = 1999
	if c.Key() == b.Key() {
		t.Error("year ignored")
	}
	for _, q := range []Query{
		{Type: QueryMovie},
		{Type: QueryMovie, Title: "x", Season: 1},
		{Type: QuerySeries, Title: "x", Episode: 2},
		{Type: QueryText},
		{Type: "bogus", Title: "x"},
		{Type: QueryMovie, IMDb: "tt12"},
	} {
		if err := q.Validate(); err == nil {
			t.Errorf("%+v accepted", q)
		}
	}
}
