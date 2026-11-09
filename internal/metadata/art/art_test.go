package art

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
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
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

const testKey = "tmdb-test-key-0123"

var testNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

// fakeTMDb answers api.themoviedb.org (as tmdb.test) from the fixtures and image.tmdb.org (as img.test) with a
// small PNG. Every request reaches it through the gateway's dialer, so hosts are what the gateway sent.
type fakeTMDb struct {
	t    *testing.T
	mu   sync.Mutex
	reqs []string
	fail bool // TMDb answers 500
	png  []byte
}

func (f *fakeTMDb) count(prefix string) int {
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

func (f *fakeTMDb) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.reqs = append(f.reqs, r.Host+r.URL.Path)
	fail := f.fail
	f.mu.Unlock()
	switch r.Host {
	case "img.test":
		if !strings.HasPrefix(r.URL.Path, "/t/p/w") && !strings.HasPrefix(r.URL.Path, "/t/p/original/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(f.png)
		return
	case "tmdb.test":
	default:
		f.t.Errorf("request to %s", r.Host)
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("api_key") != testKey {
		http.Error(w, `{"status_message":"Invalid API key"}`, http.StatusUnauthorized)
		return
	}
	if fail {
		http.Error(w, "down", http.StatusInternalServerError)
		return
	}
	q := r.URL.Query()
	fixture := func(name string) {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			f.t.Fatal(err)
		}
		w.Write(b)
	}
	switch r.URL.Path {
	case "/3/movie/603":
		if q.Get("append_to_response") != tmdb.MovieAppendDetails || q.Get("include_image_language") != "en,null" {
			http.Error(w, "want append_to_response", http.StatusBadRequest)
			return
		}
		fixture("movie_603.json")
	case "/3/tv/1396":
		if q.Get("append_to_response") != tmdb.TVAppendDetails {
			http.Error(w, "want append_to_response", http.StatusBadRequest)
			return
		}
		fixture("tv_1396.json")
	case "/3/tv/2000":
		fixture("tv_airing.json")
	case "/3/find/400000":
		if q.Get("external_source") != "tvdb_id" {
			http.Error(w, "want tvdb_id", http.StatusBadRequest)
			return
		}
		io.WriteString(w, `{"movie_results": [], "tv_results": [{"id": 2000}]}`)
	case "/3/find/tt7777777":
		io.WriteString(w, `{"movie_results": [], "tv_results": []}`)
	default:
		http.Error(w, `{"status_code": 34, "status_message": "The resource you requested could not be found."}`,
			http.StatusNotFound)
	}
}

type testEnv struct {
	st     *store.Store
	q      *jobs.Queue
	fake   *fakeTMDb
	gw     *gateway.Gateway
	svc    *Service
	images *Images
	dir    string
}

func testPolicy() *policy.Policy {
	return &policy.Policy{
		Budgets: map[string]*policy.Budget{
			"tmdb":        {Requests: 1000, Per: policy.Duration(time.Second), Burst: 100, Daily: 100000},
			"tmdb-images": {Requests: 1000, Per: policy.Duration(time.Second), Burst: 100, Daily: 20000},
		},
		Metadata: &policy.Metadata{Sources: map[string]*policy.MetadataSource{
			"tmdb":                  {URL: "http://tmdb.test/3", APIKeyEnv: "THEATARR_TMDB_KEY", Budget: "tmdb"},
			policy.TMDbImagesSource: {URL: "http://img.test/t/p", Budget: "tmdb-images"},
		}},
	}
}

func testPNG(t *testing.T) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 20, 30))
	for x := range 20 {
		for y := range 30 {
			img.Set(x, y, color.RGBA{uint8(x * 10), uint8(y * 8), 90, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	fake := &fakeTMDb{t: t, png: testPNG(t)}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, srv.Listener.Addr().String())
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	gw, err := gateway.New(ctx, testPolicy(), gateway.Options{Store: st, DialContext: dial, Logger: log,
		Timeout: 5 * time.Second, MaxWait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	tc, err := tmdb.New(tmdb.Options{BaseURL: "http://tmdb.test/3", APIKey: testKey, MinInterval: time.Millisecond,
		Backoff: time.Millisecond, Cache: &tmdb.SQLCache{Store: st, Source: "tmdb"},
		Doer: doer{gw, gateway.Target{Class: gateway.ClassMetadata, Key: "tmdb"}}})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "images")
	im, err := NewImages(ImagesConfig{Settings: policy.ImageSettings{Dir: dir, MaxBytes: 1 << 20},
		BaseURL: "http://img.test/t/p", Logger: log,
		Doer: doer{gw, gateway.Target{Class: gateway.ClassMetadata, Key: policy.TMDbImagesSource, MaxBytes: MaxImageBytes}}})
	if err != nil {
		t.Fatal(err)
	}
	q := jobs.NewQueue(st)
	q.SetClock(func() time.Time { return testNow })
	svc, err := New(Config{Store: st, Queue: q, TMDb: tc, Images: im, Logger: log,
		Settings: policy.ArtSettings{PerRun: 50, Interval: 6 * time.Hour, Prewarm: "w342"},
		Now:      func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	return &testEnv{st: st, q: q, fake: fake, gw: gw, svc: svc, images: im, dir: dir}
}

type doer struct {
	gw *gateway.Gateway
	t  gateway.Target
}

func (d doer) Do(r *http.Request) (*http.Response, error) { return d.gw.Do(r.Context(), d.t, r) }

func (e *testEnv) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := e.st.Writer().Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// seed: 1 a movie with a TMDb id, 2 a series with one, 3 a series with only a TVDB id (a Sonarr add), 4 a movie
// TMDb doesn't know, 5 a series with nothing to find it by but an IMDb id TMDb doesn't know, 6 a movie with no id.
func (e *testEnv) seed(t *testing.T) {
	now := store.Millis(testNow)
	e.exec(t, `INSERT INTO title (id, kind, name, year, tmdb_id, tvdb_id, imdb_id, created_at, updated_at) VALUES
		(1, 'movie', 'The Matrix', 1999, 603, NULL, 'tt0133093', ?, ?),
		(2, 'series', 'Breaking Bad', 2008, 1396, 81189, NULL, ?, ?),
		(3, 'series', 'Airing Show', 2024, NULL, 400000, NULL, ?, ?),
		(4, 'movie', 'Gone', 2001, 999999, NULL, NULL, ?, ?),
		(5, 'series', 'Unknown Show', 2001, NULL, NULL, 'tt7777777', ?, ?),
		(6, 'movie', 'No Ids', 2001, NULL, NULL, NULL, ?, ?)`,
		now, now, now, now, now, now, now, now, now, now, now, now)
}

func (e *testEnv) meta(t *testing.T) map[int64]Meta {
	t.Helper()
	m, err := LoadMeta(context.Background(), e.st.Reader(), []int64{1, 2, 3, 4, 5, 6})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// A run fills title_meta for every title from the fixtures, through the gateway only, and pre-warms posters.
func TestRunFillsEveryTitle(t *testing.T) {
	e := newTestEnv(t)
	e.seed(t)
	ctx := context.Background()
	sum, err := e.svc.RunBatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Filled != 3 || sum.NotFound != 3 {
		t.Errorf("summary %+v", sum)
	}
	m := e.meta(t)
	if len(m) != 6 {
		t.Fatalf("%d rows, want 6", len(m))
	}
	mx := m[1]
	if mx.TMDbID != 603 || !strings.HasPrefix(mx.Overview, "Set in the 22nd century") || mx.Tagline != "Believe the unbelievable." ||
		mx.PosterPath != "/f89U3ADr1oiB1s9GkdPOEpXUk5H.jpg" || mx.BackdropPath != "/fNG7i7RqMErkcqhohV2a6cV1Ehy.jpg" ||
		mx.LogoPath != "/enLogoHigh012345678.png" || mx.Certification != "R" || mx.RuntimeMinutes != 136 ||
		mx.Rating != 8.2 || mx.RatingVotes != 26000 || mx.Network != "Village Roadshow Pictures" ||
		mx.Status != "released" || mx.ReleaseDate != "1999-03-31" || mx.OriginalLanguage != "en" ||
		strings.Join(mx.Genres, ",") != "Action,Science Fiction" || mx.Error != "" || mx.RefreshedAt == nil ||
		mx.ArtHash != ArtHash(mx.PosterPath, mx.BackdropPath, mx.LogoPath) || len(mx.ArtHash) != 10 {
		t.Errorf("movie: %+v", mx)
	}
	if want := testNow.Add(180 * 24 * time.Hour); !mx.RefreshAfter.Equal(want) {
		t.Errorf("movie refresh_after %v, want %v", mx.RefreshAfter, want)
	}
	bb := m[2]
	if bb.Certification != "TV-MA" || bb.Network != "AMC" || bb.RuntimeMinutes != 55 || bb.Status != "Ended" ||
		bb.ReleaseDate != "2008-01-20" || bb.LastAirDate != "2013-09-29" || bb.LogoPath != "/ggFHVNu6YYI5L9pCfOacjizRGt.png" ||
		!testNow.Add(OldAge+spread(2)).Equal(bb.RefreshAfter) {
		t.Errorf("series: %+v", bb)
	}
	air := m[3]
	if air.TMDbID != 2000 || air.NextAirDate != "2026-10-27" || air.RuntimeMinutes != 42 || air.PosterPath != "" ||
		air.LogoPath != "/jaLogo0123456789abc.png" || !testNow.Add(RecentAge+spread(3)).Equal(air.RefreshAfter) {
		t.Errorf("airing series found by TVDB id: %+v", air)
	}
	if g := m[4]; !strings.Contains(g.Error, "no movie id 999999") || !testNow.Add(NotFoundRetry).Equal(g.RefreshAfter) ||
		g.RefreshedAt != nil {
		t.Errorf("404: %+v", g)
	}
	for _, id := range []int64{5, 6} {
		if g := m[id]; !strings.Contains(g.Error, "no TMDb id") || !testNow.Add(NoIDRetry).Equal(g.RefreshAfter) {
			t.Errorf("title %d: %+v", id, g)
		}
	}
	// Two posters pre-warmed (the airing show has none), into the cache layout.
	if n := e.fake.count("img.test/t/p/w342/"); n != 2 || sum.Prewarm != 2 {
		t.Errorf("poster fetches %d, prewarm %d", n, sum.Prewarm)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "w342", "f8", "f89U3ADr1oiB1s9GkdPOEpXUk5H.jpg")); err != nil {
		t.Error(err)
	}
	// Nothing is due: a second run sends nothing.
	before := e.fake.count("")
	if sum, err := e.svc.RunBatch(ctx); err != nil || sum.Filled+sum.NotFound != 0 {
		t.Errorf("second run: %+v %v", sum, err)
	}
	if n := e.fake.count(""); n != before {
		t.Errorf("second run sent %d requests", n-before)
	}
	// The gateway counted the requests against both budgets.
	var tm, ti int
	for _, b := range e.gw.State().Budgets {
		switch b.Key {
		case "tmdb":
			tm = b.UsedDay
		case "tmdb-images":
			ti = b.UsedDay
		}
	}
	if tm != e.fake.count("tmdb.test") || ti != 2 {
		t.Errorf("budget use tmdb %d (requests %d), tmdb-images %d", tm, e.fake.count("tmdb.test"), ti)
	}
}

// A run keeps a good row's art when a later try fails, stops at an error that is not about one title, and pushes
// that title back so the next try starts with the others.
func TestRunStopsOnError(t *testing.T) {
	e := newTestEnv(t)
	e.seed(t)
	ctx := context.Background()
	if err := e.svc.FillTitle(ctx, 1); err != nil {
		t.Fatal(err)
	}
	e.fake.mu.Lock()
	e.fake.fail = true
	e.fake.mu.Unlock()
	if _, err := e.svc.RunBatch(ctx); err == nil {
		t.Fatal("want an error")
	}
	m := e.meta(t)
	if len(m) != 2 {
		t.Fatalf("rows %d, want 2 (the filled one and the failed one)", len(m))
	}
	if g := m[2]; g.Error == "" || !testNow.Add(ErrorRetry).Equal(g.RefreshAfter) || g.RefreshedAt != nil {
		t.Errorf("failed title: %+v", g)
	}
	// A forced refresh of title 1 fails too, and keeps its art.
	if err := e.svc.FillTitle(ctx, 1); err == nil {
		t.Error("refresh: want an error")
	}
	if g := e.meta(t)[1]; g.PosterPath == "" || g.Error == "" || g.RefreshedAt == nil {
		t.Errorf("art lost on a failed refresh: %+v", g)
	}
}

func TestRefreshJob(t *testing.T) {
	e := newTestEnv(t)
	e.seed(t)
	ctx := context.Background()
	w := jobs.NewWorker(e.q, jobs.WorkerOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	e.svc.RegisterJobs(w)
	if _, err := e.svc.Refresh(ctx, 404); !errors.Is(err, ErrNoTitle) {
		t.Errorf("unknown title: %v", err)
	}
	id, err := e.svc.Refresh(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := e.svc.Refresh(ctx, 2); err != nil || again != id {
		t.Errorf("second refresh: job %d (first %d) err %v", again, id, err)
	}
	if ran, err := w.RunOne(ctx); !ran || err != nil {
		t.Fatalf("ran %v err %v", ran, err)
	}
	if in, err := e.q.Get(ctx, id); err != nil || in.State != jobs.StateDone {
		t.Errorf("job %+v err %v", in, err)
	}
	if m := e.meta(t); len(m) != 1 || m[2].Network != "AMC" {
		t.Errorf("rows %+v", m)
	}
	if (&Service{}).Enabled() {
		t.Error("enabled without TMDb")
	}
	if _, err := (&Service{st: e.st}).Refresh(ctx, 2); !errors.Is(err, ErrNoTMDb) {
		t.Errorf("no TMDb: %v", err)
	}
}

// Run enqueues a run soon after start and when a title has no row.
func TestRunSchedules(t *testing.T) {
	e := newTestEnv(t)
	e.svc.delay, e.svc.poll, e.svc.now = 5*time.Millisecond, 5*time.Millisecond, time.Now
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.svc.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitJob := func() {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			var n int
			e.st.Reader().QueryRow(`SELECT COUNT(*) FROM job WHERE kind = ? AND state = 'queued'`, JobKind).Scan(&n)
			if n > 0 {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("no meta.art job queued")
	}
	waitJob() // the first run, due after the delay
	e.exec(t, `DELETE FROM job`)
	e.seed(t) // new titles: queued again although the interval has not passed
	waitJob()
}

func TestRefreshAge(t *testing.T) {
	day := 24 * time.Hour
	for _, tc := range []struct {
		kind string
		m    Meta
		want time.Duration
	}{
		{"movie", Meta{Status: "released", ReleaseDate: "2026-09-01"}, 12 * time.Hour},
		{"movie", Meta{Status: "announced", ReleaseDate: "2027-05-01"}, 12 * time.Hour},
		{"movie", Meta{Status: "inCinemas"}, 12 * time.Hour},
		{"movie", Meta{Status: "released"}, 12 * time.Hour},
		{"movie", Meta{Status: "released", physicalRelease: testNow.AddDate(0, 0, -30)}, 12 * time.Hour},
		{"movie", Meta{Status: "released", physicalRelease: testNow.AddDate(0, 0, -30).Add(-time.Second)}, 180 * day},
		{"movie", Meta{Status: "released", ReleaseDate: "2026-03-01"}, 180 * day},
		{"movie", Meta{Status: "released", ReleaseDate: "2020-03-01"}, 180 * day},
		{"series", Meta{Status: "Returning Series", LastAirDate: "2020-01-01"}, RecentAge},
		{"series", Meta{Status: "Ended", NextAirDate: "2026-11-01"}, RecentAge},
		{"series", Meta{Status: "Ended", LastAirDate: "2026-10-01"}, RecentAge},
		{"series", Meta{Status: "Ended", LastAirDate: "2026-01-01"}, YearAge},
		{"series", Meta{Status: "Canceled", LastAirDate: "2019-01-01"}, OldAge},
		{"series", Meta{Status: "Ended", ReleaseDate: "2010-01-01"}, OldAge},
	} {
		if got := RefreshAge(tc.kind, tc.m, testNow); got != tc.want {
			t.Errorf("%s %+v: %v, want %v", tc.kind, tc.m, got/day, tc.want/day)
		}
	}
}

func TestPickLogoAndPaths(t *testing.T) {
	logos := []tmdb.Image{{FilePath: "/deLogo0123456789ab.png", Language: "de", VoteAverage: 9},
		{FilePath: "/svgLogo0123456789ab.svg", Language: "en", VoteAverage: 9}}
	if got := pickLogo(logos, "fr"); got != "" {
		t.Errorf("only a foreign and an SVG logo: %q", got)
	}
	if got := pickLogo(logos, "de"); got != "/deLogo0123456789ab.png" {
		t.Errorf("original language: %q", got)
	}
	for _, p := range []string{"", "abc.jpg", "/a.jpg", "/../../etc/passwd", "/abcdefghijk.gif", "/abc/defghijklmn.jpg"} {
		if imagePath(p) != "" {
			t.Errorf("path %q kept", p)
		}
	}
	if ArtHash("", "", "") != "" || ArtHash("/a.jpg", "", "") == ArtHash("", "/a.jpg", "") {
		t.Error("art hash")
	}
}

func TestMovieIdentityRefresh(t *testing.T) {
	e := newTestEnv(t)
	e.seed(t)
	e.exec(t, `UPDATE title SET name='Old title', sort_name='old title', year=2000, runtime_minutes=1, original_language='fr', monitored=0 WHERE id=1`)
	if err := e.svc.FillTitle(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	var name, sortName, language string
	var year, runtime int
	var monitored bool
	if err := e.st.Reader().QueryRow(`SELECT name,sort_name,year,runtime_minutes,original_language,monitored FROM title WHERE id=1`).Scan(&name, &sortName, &year, &runtime, &language, &monitored); err != nil {
		t.Fatal(err)
	}
	if name != "The Matrix" || sortName != "the matrix" || year != 1999 || runtime != 136 || language != "en" || monitored {
		t.Fatalf("identity %q %q %d %d %q monitored=%v", name, sortName, year, runtime, language, monitored)
	}
}
