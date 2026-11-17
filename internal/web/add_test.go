package web

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/arrview"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/metadata/tvrefresh"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/router/routertest"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// fakeAcquirer records every search; it is the only path to indexers the web layer has.
type fakeAcquirer struct {
	mu          sync.Mutex
	submits     []acquire.Selection
	interactive []string
	// noAnswer: every indexer of an interactive search fails.
	noAnswer bool
	// next is what NextSearches answers (the title page's next search); nextTitles the titles it was asked for.
	next       []acquire.NextSearch
	nextTitles []int64
	// later: the titles put on the search schedule, as "id:key,key".
	later []string
	// notRequested makes Submit report that no named library was queued (Selection.RequestedQueued).
	notRequested bool
}

func (f *fakeAcquirer) NextSearches(_ context.Context, titleID int64) ([]acquire.NextSearch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextTitles = append(f.nextTitles, titleID)
	return f.next, nil
}

func (f *fakeAcquirer) Submit(ctx context.Context, _ int64, sel acquire.Selection) (int, int, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err // the real queue writes with the context
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submits = append(f.submits, sel)
	if sel.RequestedQueued != nil {
		*sel.RequestedQueued = !f.notRequested
	}
	return len(sel.Versions), len(sel.Versions), nil
}

func (f *fakeAcquirer) Interactive(_ context.Context, titleID int64, version string, _ *int, _ []int64) (*router.Searched,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.interactive = append(f.interactive, fmt.Sprintf("%d/%s", titleID, version))
	if f.noAnswer {
		return &router.Searched{NoAnswer: true, Notes: []string{"dmm: timeout"}}, nil
	}
	good := &decide.Decision{Release: "Alpha.2020.2160p.WEB-DL.DV-GRP", Accepted: true, Quality: "WEBDL-2160p", Score: 1500}
	bad := &decide.Decision{Release: "Alpha.2020.720p.HDTV-X", Quality: "HDTV-720p",
		Rejections: []decide.Rejection{{Reason: "quality", Message: "HDTV-720p is not wanted"}}}
	return &router.Searched{Ranked: []*decide.Decision{good, bad}, Releases: map[*decide.Decision]indexer.Release{
		good: {Title: good.Release, Indexer: "dmm", Size: 9e9}, bad: {Title: bad.Release, Indexer: "dmm"}}}, nil
}

// RequestSearchLater records a title put on the paced search schedule (Bring in sync).
func (f *fakeAcquirer) RequestSearchLater(_ context.Context, titleID int64, versions []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.later = append(f.later, fmt.Sprintf("%d:%s", titleID, strings.Join(versions, ",")))
	return nil
}

func (f *fakeAcquirer) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.submits), len(f.interactive)
}

// fakeTMDb answers from fixtures; it never touches the network.
type fakeTMDb struct {
	mu       sync.Mutex
	searches int
	details  int
	fail     bool
	// answers, when set, answers each search by its lowercased query (none: nothing found); queries records them.
	answers map[string][]tmdb.MultiResult
	queries []string
}

func (f *fakeTMDb) SearchMulti(_ context.Context, q string) ([]tmdb.MultiResult, error) {
	f.mu.Lock()
	f.searches++
	f.queries = append(f.queries, q)
	f.mu.Unlock()
	if f.fail {
		return nil, errors.New("tmdb: HTTP 503")
	}
	if f.answers != nil {
		return f.answers[strings.ToLower(q)], nil
	}
	return []tmdb.MultiResult{
		{MediaType: "movie", ID: 101, Title: "Alpha", ReleaseDate: "2020-05-01", PosterPath: "/abcdefghij12.jpg",
			Overview: "A film.", VoteAverage: 7.1, VoteCount: 900},
		{MediaType: "movie", ID: 555, Title: "Alpha Two", ReleaseDate: "2024-01-01", PosterPath: "/../../etc/passwd"},
		{MediaType: "tv", ID: 777, Name: "Alpha Anime", OriginalName: "アルファ", OriginalLanguage: "ja", GenreIDs: []int{16},
			FirstAirDate: "2023-04-01", PosterPath: "/zyxwvutsrq98.png"},
	}, nil
}

func (f *fakeTMDb) MovieDetails(_ context.Context, id int) (*tmdb.Movie, error) {
	f.mu.Lock()
	f.details++
	f.mu.Unlock()
	return &tmdb.Movie{ID: id, Title: "Alpha Two", ReleaseDate: "2024-01-01", IMDbID: "tt0000555"}, nil
}

func (f *fakeTMDb) TVDetails(_ context.Context, id int) (*tmdb.TV, error) {
	f.mu.Lock()
	f.details++
	f.mu.Unlock()
	t := &tmdb.TV{ID: id, Name: "Alpha Anime", FirstAirDate: "2023-04-01"}
	t.ExternalIDs.TVDbID = 7770
	return t, nil
}

func (f *fakeTMDb) TVDBID(_ context.Context, id int) (int, error) {
	if id == 777 {
		return 7770, nil
	}
	return 0, tmdb.ErrNotFound
}

// fakeSeriesLookup is the series lookup (tvrefresh.Service.LookupSeries): TVDB 7770 (TMDb 777) has the seasons
// given, from source ("tvdb" when empty).
type fakeSeriesLookup struct {
	seasons []int
	source  string
}

func (f *fakeSeriesLookup) Enabled() bool { return true }

func (f *fakeSeriesLookup) LookupSeries(_ context.Context, id int) (tvrefresh.LookupResult, error) {
	if id != 7770 {
		return tvrefresh.LookupResult{}, errors.New("tvrefresh: no such series")
	}
	src := f.source
	if src == "" {
		src = "tvdb"
	}
	return tvrefresh.LookupResult{Title: "Alpha Anime", Seasons: slices.Clone(f.seasons), Source: src}, nil
}

// addEnv is the native API and the arr views over one database, as in theatarr serve.
type addEnv struct {
	// sqlSources: as env's.
	sqlSources bool
	t          *testing.T
	st         *store.Store
	pol        *policy.Policy
	mux        *http.ServeMux
	key        string
	acq        *fakeAcquirer
	tm         *fakeTMDb
	web        *Web
	au         *auth.Service
}

var addNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func newAddEnv(t *testing.T, mutate func(*policy.Policy)) *addEnv {
	return newAddEnvWith(t, mutate, false)
}

func newAddEnvWith(t *testing.T, mutate func(*policy.Policy), noTMDb bool) *addEnv {
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
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	now := func() time.Time { return addNow }
	au, _, err := auth.New(ctx, st, auth.Options{Logger: log, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	// The first start creates no account; the admin (id 1) is made here.
	if err := auth.ResetPassword(ctx, st, auth.AdminUsername, testAdminPassword); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(filepath.Join("..", "..", "examples", "policy"))
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(pol)
	}
	e := &addEnv{t: t, st: st, pol: pol, mux: http.NewServeMux(), acq: &fakeAcquirer{}, tm: &fakeTMDb{}}
	e.seed()
	av, err := arrview.New(ctx, arrview.Config{Store: st, Auth: au, Policy: pol, Logger: log, Now: now,
		Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	av.Mount(e.mux)
	ac := AddConfig{Acquirer: e.acq, TMDb: e.tm}
	if noTMDb {
		// The arr views never ask TMDb, so the comparison runs without it.
		ac.TMDb = nil
	}
	w, err := New(Config{Store: st, Auth: au, Policy: pol, Logger: log, Now: now, UI: testUI, Logs: testLogs, Add: ac})
	if err != nil {
		t.Fatal(err)
	}
	w.Mount(e.mux)
	e.web, e.au = w, au
	if e.key, _, err = au.CreateKey(ctx, "test", 1); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *addEnv) exec(q string, args ...any) {
	e.t.Helper()
	e.sqlSources = e.sqlSources || writesHistorySources(q)
	if _, err := e.st.Writer().Exec(q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

// seed: movie "Alpha" (TMDb 101) with movie-remux; series "The Show" (TVDB 201) with tv-2160p and four episodes in
// seasons 0 and 1, one of them unmonitored.
func (e *addEnv) seed() {
	now := store.Millis(addNow)
	e.exec(`INSERT INTO title (id, kind, name, sort_name, year, tmdb_id, tvdb_id, anime, created_at, updated_at) VALUES
		(1, 'movie', 'Alpha', 'alpha', 2020, 101, NULL, 0, ?, ?), (10, 'series', 'The Show', 'show', 2019, NULL, 201, 0, ?, ?)`,
		now, now, now, now)
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
		(1, 1, 'movie-remux', 1, 'cutoff_met', ?, ?), (10, 10, 'tv-2160p', 1, 'wanted', ?, ?)`, now, now, now, now)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, monitored, created_at, updated_at) VALUES
		(100, 10, 0, 1, 'Special', 0, ?, ?), (101, 10, 1, 1, 'Pilot', 1, ?, ?), (102, 10, 1, 2, 'Second', 1, ?, ?),
		(103, 10, 2, 1, 'Third', 1, ?, ?)`, now, now, now, now, now, now, now, now)
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at) VALUES
		(10, 100, 0, 'wanted', ?), (10, 101, 1, 'cutoff_met', ?), (10, 102, 1, 'wanted', ?), (10, 103, 1, 'wanted', ?)`,
		now, now, now, now)
}

func (e *addEnv) call(method, path string, body any, want int, out any) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, rd)
	r.Header.Set(auth.APIKeyHeader, e.key)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	storeHistory(e.t, e.st, r, &e.sqlSources)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, r)
	if rec.Code != want {
		e.t.Fatalf("%s %s: %d, want %d\n%s", method, path, rec.Code, want, rec.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			e.t.Fatalf("%s %s: %v\n%s", method, path, err, rec.Body.String())
		}
	}
}

func (e *addEnv) profile(view, name string) int64 {
	e.t.Helper()
	var id int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM arrview_profile WHERE view = ? AND name = ?`, view, name).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// rows dumps what an add writes to the shared tables (title, version, episode_version_state), without row ids or
// times, keyed by the titles' external ids. arrview_title (a view's own record of the title) is the view's, not
// the add's, and is left out.
func (e *addEnv) rows() string {
	e.t.Helper()
	var b strings.Builder
	q := func(query string) {
		rows, err := e.st.Reader().Query(query)
		if err != nil {
			e.t.Fatal(err)
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				e.t.Fatal(err)
			}
			fmt.Fprintln(&b, vals...)
		}
	}
	q(`SELECT kind, name, sort_name, year, tmdb_id, tvdb_id, imdb_id, anime, monitored FROM title
		ORDER BY kind, COALESCE(tmdb_id, 0), COALESCE(tvdb_id, 0)`)
	q(`SELECT t.kind, COALESCE(t.tmdb_id, 0), COALESCE(t.tvdb_id, 0), v.policy_key, v.monitored, v.state,
		EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id) FROM version v
		JOIN title t ON t.id = v.title_id ORDER BY 1, 2, 3, 4`)
	q(`SELECT COALESCE(t.tvdb_id, 0), e.season, e.number, v.policy_key, s.monitored, s.state FROM episode_version_state s
		JOIN version v ON v.id = s.version_id JOIN episode e ON e.id = s.episode_id JOIN title t ON t.id = e.title_id
		ORDER BY 1, 2, 3, 4`)
	return b.String()
}

// TestAddMatchesArrViews is acceptance 3a: an add through the native API creates the same rows as POST
// /radarr/api/v3/movie and /sonarr/api/v3/series with the same versions.
func TestAddMatchesArrViews(t *testing.T) {
	t.Parallel()
	type arrAdd struct {
		path string
		body func(e *addEnv) map[string]any
	}
	cases := []struct {
		name   string
		arr    arrAdd
		native map[string]any
	}{
		{"new movie, Rules default", arrAdd{"/radarr/api/v3/movie", func(e *addEnv) map[string]any {
			return map[string]any{"title": "Epsilon", "year": 2024, "tmdbId": 105, "monitored": true,
				"qualityProfileId": e.profile("radarr", "Rules default"), "rootFolderPath": "/media/movies"}
		}}, map[string]any{"kind": "movie", "tmdb_id": 105, "title": "Epsilon", "year": 2024,
			"versions": []string{"movie-remux", "movie-web"}}},
		{"new movie, default versions", arrAdd{"/radarr/api/v3/movie", func(e *addEnv) map[string]any {
			return map[string]any{"title": "Epsilon", "year": 2024, "tmdbId": 105}
		}}, map[string]any{"kind": "movie", "tmdb_id": 105, "title": "Epsilon", "year": 2024}},
		{"new movie, WEB only", arrAdd{"/radarr/api/v3/movie", func(e *addEnv) map[string]any {
			return map[string]any{"title": "Epsilon", "tmdbId": 105, "qualityProfileId": e.profile("radarr", "WEB only")}
		}}, map[string]any{"kind": "movie", "tmdb_id": 105, "title": "Epsilon", "versions": []string{"movie-web"}}},
		{"new movie, unmonitored", arrAdd{"/v/movie-web/radarr/api/v3/movie", func(e *addEnv) map[string]any {
			return map[string]any{"title": "Epsilon", "tmdbId": 105, "monitored": false}
		}}, map[string]any{"kind": "movie", "tmdb_id": 105, "title": "Epsilon", "versions": []string{"movie-web"},
			"monitored": false}},
		{"anime movie", arrAdd{"/radarr/api/v3/movie", func(e *addEnv) map[string]any {
			return map[string]any{"title": "Zeta", "tmdbId": 106, "qualityProfileId": e.profile("radarr", "Anime"),
				"rootFolderPath": "/media/anime-movies"}
		}}, map[string]any{"kind": "movie", "tmdb_id": 106, "title": "Zeta", "anime": true}},
		{"existing movie, one more version", arrAdd{"/v/movie-web/radarr/api/v3/movie", func(e *addEnv) map[string]any {
			return map[string]any{"title": "Alpha", "tmdbId": 101}
		}}, map[string]any{"kind": "movie", "tmdb_id": 101, "versions": []string{"movie-web"}}},
		{"new series", arrAdd{"/sonarr/api/v3/series", func(e *addEnv) map[string]any {
			return map[string]any{"title": "Kappa", "year": 2025, "tvdbId": 301,
				"qualityProfileId": e.profile("sonarr", "Rules default"), "seasons": []any{}}
		}}, map[string]any{"kind": "series", "tvdb_id": 301, "title": "Kappa", "year": 2025,
			"versions": []string{"tv-2160p"}}},
		{"anime series", arrAdd{"/sonarr/api/v3/series", func(e *addEnv) map[string]any {
			return map[string]any{"title": "Kaiju", "tvdbId": 302, "seriesType": "anime",
				"qualityProfileId": e.profile("sonarr", "Anime")}
		}}, map[string]any{"kind": "series", "tvdb_id": 302, "title": "Kaiju", "anime": true}},
		{"existing series with seasons", arrAdd{"/sonarr/api/v3/series", func(e *addEnv) map[string]any {
			return map[string]any{"title": "The Show", "tvdbId": 201, "qualityProfileId": e.profile("sonarr", "Rules default"),
				"seasons": []map[string]any{{"seasonNumber": 0, "monitored": true}, {"seasonNumber": 1, "monitored": false}}}
		}}, map[string]any{"kind": "series", "tvdb_id": 201, "versions": []string{"tv-2160p"},
			"seasons": []map[string]any{{"season": 0, "monitored": true}, {"season": 1, "monitored": false}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := newAddEnvWith(t, nil, true)
			a.call("POST", c.arr.path, c.arr.body(a), 201, nil)
			n := newAddEnvWith(t, nil, true)
			before := n.rows()
			var res AddResult
			n.call("POST", APIBase+"/titles", c.native, 201, &res)
			if got, want := n.rows(), a.rows(); got != want {
				t.Errorf("rows differ\nnative:\n%s\narr view:\n%s", got, want)
			}
			if before == n.rows() {
				t.Error("the add changed nothing")
			}
			if res.Search != nil {
				t.Errorf("a search ran without being asked: %+v", res.Search)
			}
			if s, i := n.acq.counts(); s+i != 0 {
				t.Errorf("searches: %d submits, %d interactive", s, i)
			}
		})
	}
}

func searchCalls(e *addEnv) []acquire.Selection {
	e.acq.mu.Lock()
	defer e.acq.mu.Unlock()
	return slices.Clone(e.acq.submits)
}

// TestAddNeverSearchesWhilePaused: search now (on an add or by itself) reaches the acquirer only while
// router.enabled is true; otherwise every version is answered with the reason.
func TestAddNeverSearchesWhilePaused(t *testing.T) {
	t.Parallel()
	// The example policy: router off.
	e := newAddEnv(t, nil)
	var res AddResult
	e.call("POST", APIBase+"/titles", map[string]any{"kind": "movie", "tmdb_id": 105, "title": "Epsilon", "search": true},
		201, &res)
	if res.Search == nil || res.Search.Started || len(res.Search.Versions) != 0 || len(res.Search.Skipped) != 2 ||
		!strings.Contains(res.Search.Message, "Paused") {
		t.Fatalf("paused add: %+v", res.Search)
	}
	var so SearchOutcome
	e.call("POST", fmt.Sprintf("%s/titles/%d/search", APIBase, res.Title.ID), nil, 200, &so)
	if so.Started || !strings.Contains(so.Message, "Paused") {
		t.Errorf("paused search now: %+v", so)
	}
	if n := len(searchCalls(e)); n != 0 {
		t.Fatalf("paused: %d searches", n)
	}

	// Router on: both movie libraries are searched.
	e = newAddEnv(t, func(p *policy.Policy) {
		p.Router = &policy.Router{Enabled: true}
	})
	// An add without search: still nothing.
	res = AddResult{}
	e.call("POST", APIBase+"/titles", map[string]any{"kind": "movie", "tmdb_id": 106, "title": "Zeta"}, 201, &res)
	if res.Search != nil || len(searchCalls(e)) != 0 {
		t.Fatalf("add without search searched: %+v", res.Search)
	}
	res = AddResult{}
	e.call("POST", APIBase+"/titles", map[string]any{"kind": "movie", "tmdb_id": 105, "search": true}, 201, &res)
	calls := searchCalls(e)
	if len(calls) != 1 || !slices.Equal(calls[0].Versions, []string{"movie-remux", "movie-web"}) ||
		!slices.Equal(calls[0].TitleIDs, []int64{res.Title.ID}) {
		t.Fatalf("add: %+v", calls)
	}
	if !res.Search.Started || len(res.Search.Skipped) != 0 {
		t.Errorf("outcome: %+v", res.Search)
	}
	// The message names the libraries for people, without keys or "item(s)".
	if want := "Searching " + e.web.labelList([]string{"movie-remux", "movie-web"}); res.Search.Message != want {
		t.Errorf("message %q, want %q", res.Search.Message, want)
	}
	// Search now on Alpha (movie-remux only): its one library; naming a version it lacks is refused.
	e.call("POST", APIBase+"/titles/1/search", map[string]any{}, 200, &so)
	if calls = searchCalls(e); !so.Started || len(calls) != 2 || !slices.Equal(calls[1].Versions, []string{"movie-remux"}) {
		t.Errorf("search now: %+v %+v", so, calls)
	}
	e.call("POST", APIBase+"/titles/1/search", map[string]any{"versions": []string{"movie-web"}}, 400, nil)
	// Turning movie-web on never searches; search now then does, for both.
	var at AddedTitle
	e.call("PUT", APIBase+"/titles/1/versions/movie-web", map[string]any{"monitored": true}, 200, &at)
	if len(searchCalls(e)) != 2 {
		t.Fatal("turning a version on searched")
	}
	e.call("POST", APIBase+"/titles/1/search", nil, 200, &so)
	calls = searchCalls(e)
	if !so.Started || len(calls) != 3 || !slices.Equal(calls[2].Versions, []string{"movie-remux", "movie-web"}) {
		t.Errorf("search now: %+v %+v", so, calls)
	}
}

// TestAddMovieUnmonitored: the add sheet's Monitored switch off sends monitored false for a new movie: every copy it
// adds is unmonitored and nothing is searched.
func TestAddMovieUnmonitored(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, func(p *policy.Policy) {
		p.Router = &policy.Router{Enabled: true}
	})
	var res AddResult
	e.call("POST", APIBase+"/titles", map[string]any{"kind": "movie", "tmdb_id": 105, "title": "Epsilon",
		"versions": []string{"movie-remux", "movie-web"}, "search": false, "monitored": false}, 201, &res)
	if !res.Created || !slices.Equal(res.New, []string{"movie-remux", "movie-web"}) || res.Search != nil {
		t.Fatalf("add: %+v", res)
	}
	if n := e.count(`SELECT COUNT(*) FROM version WHERE title_id = ? AND monitored = 0`, res.Title.ID); n != 2 {
		t.Errorf("%d unmonitored copies, want 2", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM title WHERE id = ? AND monitored = 0`, res.Title.ID); n != 1 {
		t.Error("title monitored")
	}
	if n := len(searchCalls(e)); n != 0 {
		t.Errorf("%d searches", n)
	}
}

func TestSetVersion(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	var at AddedTitle
	e.call("PUT", APIBase+"/titles/1/versions/movie-web", map[string]any{"monitored": true}, 200, &at)
	if len(at.Versions) != 2 || at.Versions[1].Key != "movie-web" || at.Versions[1].State != "wanted" || !at.Monitored {
		t.Fatalf("on: %+v", at)
	}
	e.call("PUT", APIBase+"/titles/1/versions/movie-remux", map[string]any{"monitored": false}, 200, &at)
	e.call("PUT", APIBase+"/titles/1/versions/movie-web", map[string]any{"monitored": false}, 200, &at)
	if at.Monitored || at.Versions[0].Monitored || len(at.Versions) != 2 {
		t.Errorf("off: %+v", at)
	}
	e.call("PUT", APIBase+"/titles/1/versions/tv-2160p", map[string]any{"monitored": true}, 400, nil)
	e.call("PUT", APIBase+"/titles/1/versions/nope", map[string]any{"monitored": true}, 400, nil)
	e.call("PUT", APIBase+"/titles/1/versions/movie-web", map[string]any{}, 400, nil)
	e.call("PUT", APIBase+"/titles/99/versions/movie-web", map[string]any{"monitored": true}, 404, nil)
	if s, i := e.acq.counts(); s+i != 0 {
		t.Errorf("searches: %d %d", s, i)
	}
}

func TestAddValidationAndTMDb(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	for _, body := range []map[string]any{
		{"kind": "film", "tmdb_id": 1},
		{"kind": "movie"},
		{"kind": "movie", "tvdb_id": 5},
		{"kind": "movie", "tmdb_id": 1, "versions": []string{}},
		{"kind": "movie", "tmdb_id": 1, "versions": []string{"tv-2160p"}},
		{"kind": "movie", "tmdb_id": 1, "versions": []string{"nope"}},
		{"kind": "series", "tvdb_id": 5, "seasons": []map[string]any{{"season": -1, "monitored": true}}},
		{"kind": "movie", "tmdb_id": 1, "extra": true},
	} {
		e.call("POST", APIBase+"/titles", body, 400, nil)
	}
	// Name, year and IMDb id from TMDb when the request lacks them.
	var res AddResult
	e.call("POST", APIBase+"/titles", map[string]any{"kind": "movie", "tmdb_id": 555}, 201, &res)
	if res.Title.Name != "Alpha Two" || res.Title.Year == nil || *res.Title.Year != 2024 || !res.Created ||
		!slices.Equal(res.Versions, []string{"movie-remux", "movie-web"}) {
		t.Errorf("movie from TMDb: %+v", res)
	}
	// A series from a TMDb result: the TVDB id comes from TMDb; anime gets the anime versions.
	e.call("POST", APIBase+"/titles", map[string]any{"kind": "series", "tmdb_id": 777, "anime": true}, 201, &res)
	if res.Title.TVDbID == nil || *res.Title.TVDbID != 7770 || !res.Title.Anime ||
		!slices.Equal(res.Versions, []string{"anime-tv"}) {
		t.Errorf("series from TMDb: %+v", res.Title)
	}
	// Adding it again by TVDB id finds it.
	e.call("POST", APIBase+"/titles", map[string]any{"kind": "series", "tvdb_id": 7770, "title": "x", "year": 1,
		"versions": []string{"anime-tv"}}, 201, &res)
	if res.Created || len(res.New) != 0 {
		t.Errorf("second add created: %+v", res)
	}
}

// TestAddByTitleID: the title page's "Add versions" posts the title id. A series stored with only a TVDB id and a
// movie whose TMDb id is only in title_meta both get the versions; nothing new is created and TMDb is not asked.
func TestAddByTitleID(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	now := store.Millis(addNow)
	e.exec(`INSERT INTO title (id, kind, name, sort_name, year, anime, created_at, updated_at) VALUES
		(20, 'movie', 'Gamma', 'gamma', 2021, 0, ?, ?)`, now, now)
	e.exec(`INSERT INTO title_meta (title_id, tmdb_id, attempted_at, refresh_after) VALUES (20, 909, 0, 0)`)
	count := func() (n int) {
		if err := e.st.Reader().QueryRow(`SELECT COUNT(*) FROM title`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before, details := count(), e.tm.details
	var res AddResult
	// The TVDB-only series, without kind, with a season turned off.
	e.call("POST", APIBase+"/titles", map[string]any{"title_id": 10, "versions": []string{"tv-2160p"},
		"seasons": []map[string]any{{"season": 2, "monitored": false}}}, 201, &res)
	if res.Created || res.Title.ID != 10 || !slices.Equal(res.Versions, []string{"tv-2160p"}) {
		t.Errorf("series by id: %+v", res)
	}
	var mon int
	if err := e.st.Reader().QueryRow(`SELECT monitored FROM episode_version_state WHERE episode_id = 103`).Scan(&mon); err != nil || mon != 0 {
		t.Errorf("season 2 still monitored: %d %v", mon, err)
	}
	// A movie with a new version, kind given and matching.
	e.call("POST", APIBase+"/titles", map[string]any{"title_id": 1, "kind": "movie", "versions": []string{"movie-web"}},
		201, &res)
	if res.Created || res.Title.ID != 1 || !slices.Equal(res.New, []string{"movie-web"}) {
		t.Errorf("movie by id: %+v", res)
	}
	// No TMDb id in the title row: still found by id, not created again.
	e.call("POST", APIBase+"/titles", map[string]any{"title_id": 20, "versions": []string{"movie-web"}}, 201, &res)
	if res.Created || res.Title.ID != 20 || !slices.Equal(res.New, []string{"movie-web"}) {
		t.Errorf("movie without ids: %+v", res)
	}
	if n := count(); n != before {
		t.Errorf("titles: %d, want %d", n, before)
	}
	if e.tm.details != details {
		t.Errorf("TMDb details asked %d times", e.tm.details-details)
	}
	e.call("POST", APIBase+"/titles", map[string]any{"title_id": 99, "versions": []string{"movie-web"}}, 404, nil)
	e.call("POST", APIBase+"/titles", map[string]any{"title_id": 1, "kind": "series"}, 400, nil)
	e.call("POST", APIBase+"/titles", map[string]any{"title_id": 10, "versions": []string{"movie-web"}}, 400, nil)
	if s, i := e.acq.counts(); s+i != 0 {
		t.Errorf("searches: %d %d", s, i)
	}
}

func TestSearchEndpoint(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	var s SearchAnswer
	e.call("GET", APIBase+"/search?q=alpha", nil, 200, &s)
	if s.LibraryTotal != 1 || s.Library[0].Name != "Alpha" || s.TMDbState != "ok" || len(s.TMDb) != 3 {
		t.Fatalf("search: %+v", s)
	}
	a := s.TMDb[0]
	if !a.InLibrary || a.TitleID == nil || *a.TitleID != 1 || len(a.Versions) != 1 || a.Poster != "/img/tmdb/w342/abcdefghij12.jpg" ||
		a.Rating == nil {
		t.Errorf("in library: %+v", a)
	}
	if s.TMDb[1].InLibrary || s.TMDb[1].Poster != "" {
		t.Errorf("a bad poster path must not become a URL: %+v", s.TMDb[1])
	}
	if s.TMDb[2].Kind != "series" || !s.TMDb[2].Anime || s.TMDb[2].Original != "アルファ" {
		t.Errorf("series hit: %+v", s.TMDb[2])
	}
	// The title_meta TMDb id marks a series stored with its TVDB id only.
	e.exec(`INSERT INTO title_meta (title_id, tmdb_id, attempted_at, refresh_after) VALUES (10, 777, 0, 0)`)
	e.call("GET", APIBase+"/search?q=alpha&kind=series&scope=tmdb", nil, 200, &s)
	// The In library hit's title is not found by the name "alpha": scope tmdb answers it as the library part.
	if len(s.TMDb) != 1 || !s.TMDb[0].InLibrary || *s.TMDb[0].TitleID != 10 || len(s.Library) != 1 || s.Library[0].ID != 10 ||
		s.LibraryTotal != 1 {
		t.Errorf("series via title_meta: %+v", s)
	}
	e.call("GET", APIBase+"/search?q=a", nil, 200, &s)
	if s.TMDbState != "short" {
		t.Errorf("short: %s", s.TMDbState)
	}
	e.call("GET", APIBase+"/search?q=alpha&scope=library", nil, 200, &s)
	if s.TMDbState != "skipped" || len(s.TMDb) != 0 {
		t.Errorf("library scope: %+v", s)
	}
	n := e.tm.searches
	e.tm.fail = true
	e.call("GET", APIBase+"/search?q=alpha", nil, 200, &s)
	if s.TMDbState != "error" || s.LibraryTotal != 1 || e.tm.searches != n+1 {
		t.Errorf("TMDb down: %+v", s)
	}
	e.call("GET", APIBase+"/search?q=alpha&kind=book", nil, 400, nil)
	if s, i := e.acq.counts(); s+i != 0 {
		t.Errorf("search reached the acquirer: %d %d", s, i)
	}
}

// TestSearchKindAndPaging: the kind applies before the limit (a movie-heavy match still returns its series), offset
// pages the library part, and library_total counts every match of the kind.
// A query ending in a year TMDb's multi search finds nothing for is searched again without the year, keeping that
// year's hits; a query that finds something and a numeric title alone are searched once, as typed.
func TestSearchTrailingYearFallback(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	thing := []tmdb.MultiResult{
		{MediaType: "movie", ID: 60935, Title: "The Thing", ReleaseDate: "2011-10-12"},
		{MediaType: "tv", ID: 4001, Name: "The Thing", FirstAirDate: "1982-01-01"},
		{MediaType: "movie", ID: 1091, Title: "The Thing", ReleaseDate: "1982-06-25"},
	}
	e.tm.answers = map[string][]tmdb.MultiResult{
		"the thing":         thing,
		"blade runner 2049": {{MediaType: "movie", ID: 335984, Title: "Blade Runner 2049", ReleaseDate: "2017-10-04"}},
		"blade runner":      {{MediaType: "movie", ID: 78, Title: "Blade Runner", ReleaseDate: "1982-06-25"}},
		"1917":              {{MediaType: "movie", ID: 530915, Title: "1917", ReleaseDate: "2019-12-25"}},
	}
	search := func(q string) (SearchAnswer, []string) {
		t.Helper()
		e.tm.mu.Lock()
		e.tm.queries = nil
		e.tm.mu.Unlock()
		var s SearchAnswer
		e.call("GET", APIBase+"/search?scope=tmdb&q="+url.QueryEscape(q), nil, 200, &s)
		e.tm.mu.Lock()
		defer e.tm.mu.Unlock()
		return s, append([]string(nil), e.tm.queries...)
	}
	s, qs := search("The Thing 1982")
	if s.TMDbState != "ok" || len(s.TMDb) != 2 || s.TMDb[0].TMDbID != 4001 || s.TMDb[1].TMDbID != 1091 ||
		!slices.Equal(qs, []string{"The Thing 1982", "The Thing"}) {
		t.Errorf("the thing 1982: %+v, searched %q", s.TMDb, qs)
	}
	// The year is applied before the kind and the limit.
	var k SearchAnswer
	e.call("GET", APIBase+"/search?scope=tmdb&kind=movie&limit=1&q=The+Thing+1982", nil, 200, &k)
	if len(k.TMDb) != 1 || k.TMDb[0].TMDbID != 1091 {
		t.Errorf("the thing 1982, movies, limit 1: %+v", k.TMDb)
	}
	s, qs = search("Blade Runner 2049")
	if len(s.TMDb) != 1 || s.TMDb[0].TMDbID != 335984 || len(qs) != 1 {
		t.Errorf("blade runner 2049: %+v, searched %q", s.TMDb, qs)
	}
	s, qs = search("1917")
	if len(s.TMDb) != 1 || s.TMDb[0].TMDbID != 530915 || len(qs) != 1 {
		t.Errorf("1917: %+v, searched %q", s.TMDb, qs)
	}
	// A year that matches nothing: empty, no error, one extra search at most.
	s, qs = search("The Thing 1999")
	if s.TMDbState != "ok" || len(s.TMDb) != 0 || len(qs) != 2 {
		t.Errorf("the thing 1999: %+v, searched %q", s, qs)
	}
	// Any four digits after a title: an announced film is found too, and digits nothing has give nothing.
	e.tm.answers["blade runner"] = append(e.tm.answers["blade runner"], tmdb.MultiResult{MediaType: "movie", ID: 9099,
		Title: "Blade Runner", ReleaseDate: "2099-01-01"})
	if s, qs = search("Blade Runner 2099"); len(s.TMDb) != 1 || s.TMDb[0].TMDbID != 9099 || len(qs) != 2 {
		t.Errorf("blade runner 2099: %+v, searched %q", s.TMDb, qs)
	}
	if s, qs = search("Blade Runner 1850"); len(s.TMDb) != 0 || len(qs) != 2 {
		t.Errorf("blade runner 1850: %+v, searched %q", s.TMDb, qs)
	}
}

func TestSearchKindAndPaging(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	now := store.Millis(addNow)
	for i := range 5 {
		e.exec(`INSERT INTO title (kind, name, sort_name, tmdb_id, anime, created_at, updated_at) VALUES ('movie', ?, ?,
			?, 0, ?, ?)`, fmt.Sprintf("Alpha %d", i), fmt.Sprintf("alpha %d", i), 1000+i, now, now)
	}
	e.exec(`INSERT INTO title (kind, name, sort_name, tvdb_id, anime, created_at, updated_at) VALUES ('series', 'Alpha Show',
		'alpha show', 4242, 0, ?, ?)`, now, now)
	var s SearchAnswer
	e.call("GET", APIBase+"/search?q=alpha&scope=library&kind=series&limit=2", nil, 200, &s)
	if s.LibraryTotal != 1 || len(s.Library) != 1 || s.Library[0].Name != "Alpha Show" {
		t.Errorf("series: %+v", s)
	}
	e.call("GET", APIBase+"/search?q=alpha&scope=library&kind=movie&limit=4", nil, 200, &s)
	if s.LibraryTotal != 6 || len(s.Library) != 4 {
		t.Errorf("movies page 1: total %d, %d items", s.LibraryTotal, len(s.Library))
	}
	seen := map[int64]bool{}
	for _, r := range s.Library {
		seen[r.ID] = true
	}
	n := e.tm.searches
	e.call("GET", APIBase+"/search?q=alpha&kind=movie&limit=4&offset=4", nil, 200, &s)
	if s.LibraryTotal != 6 || len(s.Library) != 2 || s.TMDbState != "skipped" || e.tm.searches != n {
		t.Errorf("movies page 2: %+v", s)
	}
	for _, r := range s.Library {
		if seen[r.ID] || r.Kind != "movie" {
			t.Errorf("page 2 repeats or mixes: %+v", r)
		}
	}
	e.call("GET", APIBase+"/search?q=alpha&offset=-1", nil, 400, nil)
}

func TestAddOptions(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, func(p *policy.Policy) {
		p.Router = &policy.Router{Enabled: true}
	})
	var o AddOptions
	e.call("GET", APIBase+"/add/options?kind=movie", nil, 200, &o)
	byKey := map[string]VersionOption{}
	for _, v := range o.Versions {
		byKey[v.Key] = v
	}
	if len(o.Versions) != 3 || !byKey["movie-web"].Default || !byKey["movie-web"].Searchable ||
		!byKey["movie-remux"].Default || !byKey["movie-remux"].Searchable || byKey["anime-movie"].Default || !o.CanSearch {
		t.Fatalf("movie options: %+v", o)
	}
	e.call("GET", APIBase+"/add/options?kind=movie&tmdb_id=101", nil, 200, &o)
	if o.TitleID == nil || *o.TitleID != 1 {
		t.Errorf("in-library movie: %+v", o)
	}
	for _, v := range o.Versions {
		if v.On != (v.Key == "movie-remux") || v.Held != v.On {
			t.Errorf("on: %+v", v)
		}
		// What each library fetches: its profile and the cutoff it upgrades to.
		if pr := e.web.pol.Profiles[e.web.pol.Versions[v.Key].Profile]; pr == nil || v.Profile == "" || v.Cutoff != pr.Cutoff {
			t.Errorf("profile of %s: %+v", v.Key, v)
		}
	}
	// A library the title is in but does not monitor is held, not on: the add monitors it again.
	e.exec(`UPDATE version SET monitored = 0 WHERE id = 1`)
	e.call("GET", APIBase+"/add/options?kind=movie&tmdb_id=101", nil, 200, &o)
	for _, v := range o.Versions {
		if v.On || v.Held != (v.Key == "movie-remux") {
			t.Errorf("held: %+v", v)
		}
	}
	e.exec(`UPDATE version SET monitored = 1 WHERE id = 1`)
	// A library whose own upgrades are off does not upgrade, whatever its profile says.
	e.web.pol.Versions["movie-remux"].Upgrades = "off"
	o = AddOptions{}
	e.call("GET", APIBase+"/add/options?kind=movie", nil, 200, &o)
	for _, v := range o.Versions {
		if v.Key == "movie-remux" && v.Upgrades {
			t.Errorf("upgrades off: %+v", v)
		}
	}
	e.web.pol.Versions["movie-remux"].Upgrades = ""
	e.call("GET", APIBase+"/add/options?title_id=10", nil, 200, &o)
	if o.Kind != "series" || !o.EpisodesKnown || len(o.Seasons) != 3 || *o.Seasons[0].Monitored || !*o.Seasons[1].Monitored ||
		!o.CanSearch {
		t.Errorf("series options: %+v", o)
	}
	// A new series without the series lookup (metadata off): no seasons, the sheet offers the presets only.
	o = AddOptions{}
	e.call("GET", APIBase+"/add/options?kind=series&tmdb_id=777&anime=1", nil, 200, &o)
	if o.EpisodesKnown || len(o.Seasons) != 0 || !o.Anime {
		t.Errorf("new series options without the lookup: %+v", o)
	}
	// With it: the series' own seasons (TVDB's numbering, by the TVDB id TMDb names), without episode counts.
	e.web.seriesLookup = &fakeSeriesLookup{seasons: []int{0, 1, 2}}
	o = AddOptions{}
	e.call("GET", APIBase+"/add/options?kind=series&tmdb_id=777&anime=1", nil, 200, &o)
	if o.EpisodesKnown || len(o.Seasons) != 3 || o.Seasons[0].Number != 0 || o.Seasons[2].Number != 2 ||
		o.Seasons[1].Episodes != 0 || o.Seasons[1].Monitored != nil {
		t.Errorf("new series options: %+v", o)
	}
	// No TVDB id on TMDb: no seasons, never an error.
	o = AddOptions{}
	e.call("GET", APIBase+"/add/options?kind=series&tmdb_id=778", nil, 200, &o)
	if len(o.Seasons) != 0 {
		t.Errorf("series without a TVDB id: %+v", o)
	}
	// Seasons the lookup took from TMDb (TVDB has no such series) are not the series' own: none.
	e.web.seriesLookup = &fakeSeriesLookup{seasons: []int{1, 2}, source: "tmdb"}
	o = AddOptions{}
	e.call("GET", APIBase+"/add/options?kind=series&tmdb_id=777", nil, 200, &o)
	if len(o.Seasons) != 0 {
		t.Errorf("TMDb fallback seasons offered: %+v", o)
	}
	e.call("GET", APIBase+"/add/options", nil, 400, nil)
	e.call("GET", APIBase+"/add/options?title_id=99", nil, 404, nil)
}

// TestReleasesStoredAnswer: without live=1 the releases panel answers from the journal.
func TestReleasesStoredAnswer(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	e.exec(`INSERT INTO version (title_id, policy_key, state, created_at, updated_at) VALUES (1, 'movie-web', 'wanted', 0, 0)`)
	var r Releases
	e.call("GET", APIBase+"/titles/1/releases?version=movie-remux", nil, 200, &r)
	if r.Source != "none" || len(r.Candidates) != 0 || r.Message != "" {
		t.Fatalf("nothing stored (the badge says it, no message): %+v", r)
	}
	var ep Releases
	e.call("GET", APIBase+"/titles/10/episodes/101/releases?version=tv-2160p", nil, 200, &ep)
	if ep.Source != "none" || ep.Message != "" {
		t.Fatalf("episode with nothing stored: %+v", ep)
	}
	// The film's own file in the library, whatever the candidates compared against: none yet, then its arr file.
	if r.Current == nil || r.Current.Held {
		t.Errorf("no file: current %+v", r.Current)
	}
	e.exec(`INSERT INTO link (id, path, target, title_id, quality, score, file_metadata, created_at, updated_at)
		VALUES (900, '/m/alpha.mkv', '/zurg/alpha.mkv', 1, 'Remux-2160p', 3100, '{"Score":3100}', 0, 0)`)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (900, 1)`)
	r = Releases{}
	e.call("GET", APIBase+"/titles/1/releases?version=movie-remux", nil, 200, &r)
	if c := r.Current; c == nil || !c.Held || c.Quality != "Remux-2160p" || c.Score == nil || *c.Score != 3100 {
		t.Errorf("held: current %+v", c)
	}
	// A journal decision answers.
	e.exec(`INSERT INTO journal_decision (source, title_id, version_key, target, results, accepted, candidates,
		rejections, created_at) VALUES ('rss', 1, 'movie-remux', 'rss', 7, 1,
		'[{"rank":1,"release":"Alpha.2020.REMUX-C","accepted":true,"score":3100}]', '{"size":6}', 3000)`)
	e.call("GET", APIBase+"/titles/1/releases?version=movie-remux", nil, 200, &r)
	if r.Source != "journal" || r.Results != 7 || r.Rejections["size"] != 6 || r.Candidates[0].Release != "Alpha.2020.REMUX-C" {
		t.Fatalf("journal: %+v", r)
	}
	// Without live the stored decision names what it came from, once.
	r = Releases{}
	e.call("GET", APIBase+"/titles/1/releases?version=movie-remux", nil, 200, &r)
	if r.Message != "From RSS" {
		t.Errorf("stored message %q", r.Message)
	}
	if _, i := e.acq.counts(); i != 0 {
		t.Fatalf("a stored answer searched indexers %d times", i)
	}
	// The active version: live=1 searches (the router switch does not matter for an answer that writes nothing).
	e.call("GET", APIBase+"/titles/1/releases?version=movie-web", nil, 200, &r)
	if !r.LiveAllowed || r.Source != "none" {
		t.Errorf("active, stored: %+v", r)
	}
	if _, i := e.acq.counts(); i != 0 {
		t.Fatal("searched without live=1")
	}
	e.call("GET", APIBase+"/titles/1/releases?version=movie-web&live=1", nil, 200, &r)
	if r.Current == nil || r.Current.Held {
		t.Errorf("live, movie-web holds nothing: current %+v", r.Current)
	}
	if r.Source != "live" || r.Results != 2 || r.Accepted != 1 || r.Candidates[0].Indexer != "dmm" ||
		r.Candidates[1].Rejections[0].Reason != "quality" || r.Rejections["quality"] != 1 {
		t.Errorf("live: %+v", r)
	}
	if _, i := e.acq.counts(); i != 1 {
		t.Errorf("live searches %d", i)
	}
	if s, _ := e.acq.counts(); s != 0 {
		t.Errorf("the panel started %d acquisitions", s)
	}
	e.call("GET", APIBase+"/titles/1/releases?version=tv-2160p", nil, 400, nil)
	e.call("GET", APIBase+"/titles/99/releases", nil, 404, nil)
}

// TestReleasesOneRowPerTorrent: a release list has one row per info hash: the best-ranked copy shown, every indexer
// listed, the most seeders, the union of cached-on; a release without a hash stays a row of its own; the counts and
// the cap count rows. A stored answer merges its kept sample and keeps its stored counts.
func TestReleasesOneRowPerTorrent(t *testing.T) {
	t.Parallel()
	h := strings.Repeat("a", 40)
	dec := func(name string, ok bool) *decide.Decision {
		d := &decide.Decision{Release: name, Accepted: ok}
		if !ok {
			d.Rejections = []decide.Rejection{{Reason: "quality"}}
		}
		return d
	}
	best, second, third := dec("Alpha.2020.2160p-A", true), dec("Alpha 2020 2160p A", true), dec("alpha.2020.2160p.a", false)
	other, bare := dec("Alpha.2020.720p-B", false), dec("Alpha.2020.1080p-NOHASH", false)
	sr := &router.Searched{Ranked: []*decide.Decision{best, second, third, other, bare}, Indexers: 3,
		Releases: map[*decide.Decision]indexer.Release{
			best:   {Indexer: "prowlarr-tpb", InfoHash: h, Seeders: 4, Peers: 5, Extras: map[string]string{"cached_on": "rd"}},
			second: {Indexer: "therarbg", InfoHash: strings.ToUpper(h), Seeders: 90, Peers: 100},
			third:  {Indexer: "dmm", InfoHash: h, Seeders: -1, Extras: map[string]string{"cached_on": "ad"}},
			other:  {Indexer: "dmm", InfoHash: strings.Repeat("b", 40), Seeders: -1},
			bare:   {Indexer: "therarbg", Seeders: -1},
		}}
	var out Releases
	out.Rejections = map[string]int{}
	rankedInto(&out, sr, time.Unix(0, 0))
	if out.Results != 3 || out.Accepted != 1 || out.Rejections["quality"] != 2 || len(out.Candidates) != 3 {
		t.Fatalf("results %d accepted %d rejections %v rows %d", out.Results, out.Accepted, out.Rejections, len(out.Candidates))
	}
	r := out.Candidates[0]
	if r.Release != best.Release || r.Rank != 1 || !r.Accepted || r.Indexer != "prowlarr-tpb" ||
		!slices.Equal(r.Indexers, []string{"prowlarr-tpb", "therarbg", "dmm"}) || r.Seeders != 90 || r.Leechers == nil ||
		*r.Leechers != 10 || !slices.Equal(r.CachedOn, []string{"rd", "ad"}) ||
		!slices.Equal(r.Copies, []ReleaseCopy{{"therarbg", second.Release}, {"dmm", third.Release}}) {
		t.Errorf("merged row %+v", r)
	}
	if r := out.Candidates[1]; r.Seeders != -1 || r.Leechers != nil || len(r.Copies) != 0 || !slices.Equal(r.Indexers, []string{"dmm"}) {
		t.Errorf("one copy, unknown peers: %+v", r)
	}
	if r := out.Candidates[2]; r.Release != bare.Release || r.InfoHash != "" {
		t.Errorf("hashless row %+v", r)
	}
	// An accepted and a rejected copy of one hash make an accepted row.
	ok, no := dec("Beta-A", true), dec("Beta-B", false)
	out = Releases{Rejections: map[string]int{}}
	rankedInto(&out, &router.Searched{Ranked: []*decide.Decision{ok, no}, Releases: map[*decide.Decision]indexer.Release{
		ok: {Indexer: "x", InfoHash: h}, no: {Indexer: "y", InfoHash: h}}}, time.Unix(0, 0))
	if out.Results != 1 || out.Accepted != 1 || len(out.Rejections) != 0 || len(out.Candidates) != 1 || !out.Candidates[0].Accepted {
		t.Errorf("accepted and rejected copy: %+v", out)
	}
	// The cap counts rows: 301 torrents of two copies each are 301 results, 300 shown.
	sr = &router.Searched{Releases: map[*decide.Decision]indexer.Release{}}
	for i := range maxLiveCandidates + 1 {
		for _, ix := range []string{"x", "y"} {
			d := dec(fmt.Sprintf("R%d-%s", i, ix), true)
			sr.Ranked = append(sr.Ranked, d)
			sr.Releases[d] = indexer.Release{Indexer: ix, InfoHash: fmt.Sprintf("%040x", i+1)}
		}
	}
	out = Releases{Rejections: map[string]int{}}
	rankedInto(&out, sr, time.Unix(0, 0))
	if out.Results != maxLiveCandidates+1 || out.Accepted != maxLiveCandidates+1 || len(out.Candidates) != maxLiveCandidates ||
		len(out.Candidates[maxLiveCandidates-1].Indexers) != 2 {
		t.Errorf("cap: results %d accepted %d rows %d", out.Results, out.Accepted, len(out.Candidates))
	}

	// Stored: the kept sample is merged, the stored counts stay.
	e := newAddEnv(t, nil)
	e.exec(`INSERT INTO journal_decision (source, title_id, version_key, target, results, accepted, candidates,
		rejections, created_at) VALUES ('rss', 1, 'movie-remux', 'rss', 9, 2, ?, '{"size":7}', 3000)`,
		`[{"rank":1,"release":"Alpha.2020.REMUX-C","indexer":"tpb","info_hash":"`+h+`","seeders":3,"accepted":true},
		  {"rank":2,"release":"Alpha 2020 REMUX C","indexer":"rarbg","info_hash":"`+h+`","seeders":8,"accepted":true},
		  {"rank":3,"release":"Alpha.2020.REMUX-D","indexer":"rarbg","accepted":false}]`)
	var st Releases
	e.call("GET", APIBase+"/titles/1/releases?version=movie-remux", nil, 200, &st)
	if st.Results != 9 || st.Accepted != 2 || len(st.Candidates) != 2 || st.Candidates[0].Seeders != 8 ||
		!slices.Equal(st.Candidates[0].Indexers, []string{"tpb", "rarbg"}) || !slices.Equal(st.Candidates[1].Indexers, []string{"rarbg"}) {
		t.Errorf("stored: %+v", st)
	}
}

func (f *fakeAcquirer) WantedList(context.Context, acquire.WantedQuery) (*acquire.WantedList, error) {
	return &acquire.WantedList{Items: []acquire.WantedRow{}, Counts: map[string]int{}}, nil
}

func (e *addEnv) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.st.Reader().QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return n
}

// TestAddMonitorPreset: POST /titles' monitor is kept as pending_monitor for a new series, which has no episodes
// until its first refresh; it is refused for a movie, with a value outside the list and for a title already in the
// library.
func TestAddMonitorPreset(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	for _, body := range []map[string]any{
		{"kind": "series", "tvdb_id": 301, "monitor": "sometimes"},
		{"kind": "series", "tvdb_id": 301, "monitor": "skip"},
		{"kind": "movie", "tmdb_id": 105, "monitor": "all"},
		{"title_id": 1, "monitor": "all"},
		{"kind": "series", "tvdb_id": 301, "series_type": "weekly"},
		{"kind": "movie", "tmdb_id": 105, "series_type": "daily"},
		{"kind": "movie", "tmdb_id": 105, "minimum_availability": "tba"},
		{"kind": "series", "tvdb_id": 301, "minimum_availability": "announced"},
	} {
		e.call("POST", APIBase+"/titles", body, 400, nil)
	}
	if n := e.count(`SELECT COUNT(*) FROM title`); n != 2 {
		t.Fatalf("a refused add wrote a title: %d titles", n)
	}
	// A title already in the library, by its id or found by its TVDB id (the drawer looks a title up by TMDb id, so
	// it can take such a series for a new one and send what it sends for one): the preset is refused, since it would
	// rewrite the monitoring of the copies the title has. Nothing is written, not even the new copy of the request,
	// and the answer names the title.
	held := func() string {
		var intent string
		if err := e.st.Reader().QueryRow(`SELECT COALESCE((SELECT monitor_new || ' ' || seasons || ' ' || pending_monitor
			FROM series_monitor WHERE version_id = 10), 'no row')`).Scan(&intent); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%s, %d monitored", intent,
			e.count(`SELECT COUNT(*) FROM episode_version_state WHERE version_id = 10 AND monitored = 1`))
	}
	before := held()
	for _, body := range []map[string]any{
		{"title_id": 10, "versions": []string{"tv-2160p"}, "monitor": "pilot"},
		{"title_id": 10, "versions": []string{"tv-2160p", "anime-tv"}, "monitor": "pilot"},
		{"kind": "series", "tvdb_id": 201, "versions": []string{"tv-2160p"}, "monitor": "none"},
		{"kind": "series", "tmdb_id": 987654, "tvdb_id": 201, "title": "The Show", "versions": []string{"tv-2160p", "anime-tv"},
			"monitor": "all", "monitor_new": "none"},
		// The type is the series' one value: an add to a series the library has never sets it, stored or not.
		{"title_id": 10, "versions": []string{"tv-2160p"}, "series_type": "daily"},
		{"kind": "series", "tvdb_id": 201, "versions": []string{"tv-2160p", "anime-tv"}, "series_type": "daily"},
	} {
		var refused struct {
			Error string `json:"error"`
		}
		e.call("POST", APIBase+"/titles", body, 400, &refused)
		if !strings.Contains(refused.Error, "already in the library (title 10)") {
			t.Errorf("%v: answer %q does not name the library title", body, refused.Error)
		}
	}
	if after := held(); after != before || !strings.HasSuffix(after, ", 3 monitored") {
		t.Errorf("a refused preset changed the held copy: %q, was %q", after, before)
	}
	if n := e.count(`SELECT COUNT(*) FROM version WHERE title_id = 10`); n != 1 {
		t.Errorf("a refused add wrote a version: %d versions", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM title WHERE id = 10 AND COALESCE(series_type, '') = ''`); n != 1 {
		t.Error("a refused series_type changed the title's type")
	}
	// The same request without the preset is today's add.
	e.call("POST", APIBase+"/titles", map[string]any{"kind": "series", "tvdb_id": 201, "versions": []string{"tv-2160p", "anime-tv"}}, 201, nil)
	// A series without episodes keeps each preset until its first refresh.
	for i, m := range addMonitors {
		var res AddResult
		e.call("POST", APIBase+"/titles", map[string]any{"kind": "series", "tvdb_id": 400 + i, "title": "New " + m,
			"versions": []string{"tv-2160p"}, "monitor": m}, 201, &res)
		var pending string
		if err := e.st.Reader().QueryRow(`SELECT m.pending_monitor FROM series_monitor m JOIN version v ON v.id = m.version_id
			WHERE v.title_id = ?`, res.Title.ID).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending != m {
			t.Errorf("monitor %s: pending_monitor %q", m, pending)
		}
	}
	// No preset: nothing pending, as before.
	var res AddResult
	e.call("POST", APIBase+"/titles", map[string]any{"kind": "series", "tvdb_id": 301, "title": "Kappa",
		"versions": []string{"tv-2160p"}}, 201, &res)
	if n := e.count(`SELECT COUNT(*) FROM series_monitor m JOIN version v ON v.id = m.version_id WHERE v.title_id = ?
		AND m.pending_monitor <> ''`, res.Title.ID); n != 0 {
		t.Error("an add without monitor left a pending preset")
	}
	// monitor_new none alone stores what it always did: nothing is held for the first refresh. The drawer's "Monitor
	// seasons that appear later" off therefore sends monitor all with it, so the seasons the first refresh finds
	// are monitored and none governs only the ones after (tvrefresh TestLaterSeasonsOffKeepsFirstSeasons runs the
	// refreshes from that stored state).
	intent := func(id int64) string {
		var mn, seasons, pending string
		if err := e.st.Reader().QueryRow(`SELECT m.monitor_new, m.seasons, m.pending_monitor FROM series_monitor m
			JOIN version v ON v.id = m.version_id WHERE v.title_id = ? ORDER BY v.id LIMIT 1`, id).Scan(&mn, &seasons, &pending); err != nil {
			t.Fatal(err)
		}
		return mn + " " + seasons + " " + pending
	}
	if got := intent(res.Title.ID); got != "all {} " {
		t.Errorf("a default add stores %q", got)
	}
	e.call("POST", APIBase+"/titles", map[string]any{"kind": "series", "tvdb_id": 310, "title": "Later off",
		"versions": []string{"tv-2160p"}, "monitor_new": "none"}, 201, &res)
	if got := intent(res.Title.ID); got != "none {} " {
		t.Errorf("monitor_new none alone: %q", got)
	}
	e.call("POST", APIBase+"/titles", map[string]any{"kind": "series", "tvdb_id": 313, "title": "Later off, as the drawer sends it",
		"versions": []string{"tv-2160p"}, "monitor_new": "none", "monitor": "all"}, 201, &res)
	if got := intent(res.Title.ID); got != "none {} all" {
		t.Errorf("later seasons off as the drawer sends it: %q", got)
	}
	e.call("POST", APIBase+"/titles", map[string]any{"kind": "series", "tvdb_id": 311, "title": "Later off, pilot",
		"versions": []string{"tv-2160p"}, "monitor_new": "none", "monitor": "pilot"}, 201, &res)
	if got := intent(res.Title.ID); got != "none {} pilot" {
		t.Errorf("later seasons off with a preset: %q", got)
	}
	e.call("POST", APIBase+"/titles", map[string]any{"kind": "series", "tvdb_id": 312, "title": "Later off, picks",
		"versions": []string{"tv-2160p"}, "monitor_new": "none",
		"seasons": []map[string]any{{"season": 1, "monitored": true}, {"season": 2, "monitored": false}}}, 201, &res)
	if got := intent(res.Title.ID); got != `none {"1":true,"2":false} ` {
		t.Errorf("later seasons off with season picks: %q", got)
	}
	// A title already in the library: its episodes are not rewritten.
	e.call("POST", APIBase+"/titles", map[string]any{"title_id": 10, "versions": []string{"tv-2160p"}, "monitor_new": "none"}, 201, nil)
	if got := intent(10); !strings.HasPrefix(got, "none ") || !strings.HasSuffix(got, " ") {
		t.Errorf("later seasons off on a held series: %q", got)
	}
	// A new series takes the type of the request; without one none is stored.
	seriesType := func(id int64) string {
		var s string
		if err := e.st.Reader().QueryRow(`SELECT COALESCE(series_type, '') FROM title WHERE id = ?`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	plain := seriesType(res.Title.ID)
	e.call("POST", APIBase+"/titles", map[string]any{"kind": "series", "tvdb_id": 302, "title": "Daily",
		"versions": []string{"tv-2160p"}, "series_type": "daily"}, 201, &res)
	if got := seriesType(res.Title.ID); got != "daily" || plain != "" {
		t.Errorf("series_type: %q after daily, %q without", got, plain)
	}
	var page TitleDetail
	e.call("GET", fmt.Sprintf("%s/titles/%d", APIBase, res.Title.ID), nil, 200, &page)
	if page.SeriesType != "daily" {
		t.Errorf("the title page reads series_type %q", page.SeriesType)
	}
}

// TestAddMinimumAvailability: the choice is one arrview_title row on the own view (v/<key>) of each library the add
// creates, which availability.Movie reads; an add without it writes no such row, and a later add leaves the rows of
// the libraries the movie already had.
func TestAddMinimumAvailability(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	ctx := context.Background()
	records := func(titleID int64) string {
		var out []string
		rows, err := e.st.Reader().Query(`SELECT view, minimum_availability, quality_profile_id, root_folder_path
			FROM arrview_title WHERE title_id = ? ORDER BY view`, titleID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var view, minimum, root string
			var qp int64
			if err := rows.Scan(&view, &minimum, &qp, &root); err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprintf("%s=%s/%d/%s", view, minimum, qp, root))
		}
		return strings.Join(out, " ")
	}
	available := func(titleID int64, key string) bool {
		var vid int64
		if err := e.st.Reader().QueryRow(`SELECT id FROM version WHERE title_id = ? AND policy_key = ?`, titleID, key).Scan(&vid); err != nil {
			t.Fatal(err)
		}
		ok, err := availability.Movie(ctx, e.st.Reader(), e.pol, titleID, vid, key, addNow)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	// Today's requests: no row, for a new movie, a series and a movie gaining a library.
	var plain, res AddResult
	e.call("POST", APIBase+"/titles", map[string]any{"kind": "movie", "tmdb_id": 105, "title": "Epsilon",
		"versions": []string{"movie-remux", "movie-web"}}, 201, &plain)
	e.call("POST", APIBase+"/titles", map[string]any{"kind": "series", "tvdb_id": 301, "title": "Kappa",
		"versions": []string{"tv-2160p"}}, 201, nil)
	e.call("POST", APIBase+"/titles", map[string]any{"title_id": 1, "versions": []string{"movie-remux", "movie-web"}}, 201, nil)
	if n := e.count(`SELECT COUNT(*) FROM arrview_title`); n != 0 {
		t.Fatalf("default adds wrote %d arrview_title rows", n)
	}
	// Epsilon has no release dates: not available as released (the default), available as announced.
	if available(plain.Title.ID, "movie-web") {
		t.Error("an unreleased movie added without a choice is available")
	}
	e.call("POST", APIBase+"/titles", map[string]any{"kind": "movie", "tmdb_id": 106, "title": "Zeta",
		"versions": []string{"movie-remux"}, "minimum_availability": "inCinemas"}, 201, &res)
	if got := records(res.Title.ID); got != "v/movie-remux=inCinemas/0/" {
		t.Errorf("first add: %s", got)
	}
	if available(res.Title.ID, "movie-remux") {
		t.Error("inCinemas without a cinema date is available")
	}
	// A second library with another choice: its own row; the first library's row stays.
	e.call("POST", APIBase+"/titles", map[string]any{"title_id": res.Title.ID, "versions": []string{"movie-remux", "movie-web"},
		"minimum_availability": "announced"}, 201, &res)
	if got := records(res.Title.ID); got != "v/movie-remux=inCinemas/0/ v/movie-web=announced/0/" {
		t.Errorf("second add: %s", got)
	}
	if !available(res.Title.ID, "movie-web") || available(res.Title.ID, "movie-remux") {
		t.Error("announced applies to movie-web only")
	}
	// Nothing new in the request: nothing written.
	e.call("POST", APIBase+"/titles", map[string]any{"title_id": res.Title.ID, "versions": []string{"movie-remux", "movie-web"},
		"minimum_availability": "released"}, 201, nil)
	if got := records(res.Title.ID); got != "v/movie-remux=inCinemas/0/ v/movie-web=announced/0/" {
		t.Errorf("an add of nothing new changed the rows: %s", got)
	}
	// One row per new library of one add.
	e.call("POST", APIBase+"/titles", map[string]any{"kind": "movie", "tmdb_id": 107, "title": "Eta",
		"versions": []string{"movie-remux", "movie-web"}, "minimum_availability": "announced"}, 201, &res)
	if got := records(res.Title.ID); got != "v/movie-remux=announced/0/ v/movie-web=announced/0/" {
		t.Errorf("two libraries: %s", got)
	}
	// A copy removed and added again is an added copy too. Its row may hold what an arr client wrote: the choice
	// alone changes, and the copy that stayed keeps its own.
	id := res.Title.ID
	remove := func(keys ...string) {
		t.Helper()
		if err := e.st.WriteTx(ctx, func(tx *sql.Tx) error {
			_, err := catalog.Remove(ctx, tx, id, keys, "ui", addNow)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	both := []string{"movie-remux", "movie-web"}
	e.exec(`UPDATE arrview_title SET quality_profile_id = 7, root_folder_path = '/x', added_at = 5 WHERE title_id = ?
		AND view = 'v/movie-web'`, id)
	remove("movie-web")
	// Without the field a restored copy keeps the choice it had.
	e.call("POST", APIBase+"/titles", map[string]any{"title_id": id, "versions": both}, 201, nil)
	if got := records(id); got != "v/movie-remux=announced/0/ v/movie-web=announced/7//x" {
		t.Errorf("restored without a choice: %s", got)
	}
	remove("movie-web")
	e.call("POST", APIBase+"/titles", map[string]any{"title_id": id, "versions": both, "minimum_availability": "inCinemas"}, 201, nil)
	if got := records(id); got != "v/movie-remux=announced/0/ v/movie-web=inCinemas/7//x" {
		t.Errorf("restored with a choice: %s", got)
	}
	if n := e.count(`SELECT COUNT(*) FROM arrview_title WHERE title_id = ? AND view = 'v/movie-web' AND added_at = 5`, id); n != 1 {
		t.Error("the restored copy's row changed in more than the choice")
	}
	if available(id, "movie-web") || !available(id, "movie-remux") {
		t.Error("inCinemas applies to the restored movie-web only")
	}
	// Every copy removed (the rows go with them), then added again, found by its TMDb id, with a choice.
	remove(both...)
	if got := records(id); got != "" {
		t.Fatalf("rows after removing every copy: %s", got)
	}
	e.call("POST", APIBase+"/titles", map[string]any{"kind": "movie", "tmdb_id": 107, "versions": both, "minimum_availability": "announced"}, 201, &res)
	if got := records(id); res.Title.ID != id || got != "v/movie-remux=announced/0/ v/movie-web=announced/0/" {
		t.Errorf("added again with a choice: title %d, %s", res.Title.ID, got)
	}
	if !available(id, "movie-web") || !available(id, "movie-remux") {
		t.Error("announced does not apply after adding the movie again")
	}
}

// TestAddMinimumAvailabilityLibraryDefault: the drawer's choice is deliberate, so it is stored for a library that has
// a minimum_availability of its own too; without a choice ("Library default") nothing is stored and the library's
// value applies.
func TestAddMinimumAvailabilityLibraryDefault(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, func(p *policy.Policy) { p.Versions["movie-web"].MinimumAvailability = "announced" })
	minimum := func(id int64) string {
		var d TitleDetail
		e.call("GET", fmt.Sprintf("%s/titles/%d", APIBase, id), nil, 200, &d)
		var out []string
		for _, v := range d.Versions {
			out = append(out, v.Key+"="+v.MinimumAvailability)
		}
		return strings.Join(out, " ")
	}
	var res AddResult
	e.call("POST", APIBase+"/titles", map[string]any{"kind": "movie", "tmdb_id": 105, "title": "Epsilon",
		"versions": []string{"movie-remux", "movie-web"}}, 201, &res)
	if n := e.count(`SELECT COUNT(*) FROM arrview_title`); n != 0 {
		t.Fatalf("an add at the library default wrote %d arrview_title rows", n)
	}
	if got := minimum(res.Title.ID); got != "movie-remux=released movie-web=announced" {
		t.Errorf("library default: %s", got)
	}
	e.call("POST", APIBase+"/titles", map[string]any{"kind": "movie", "tmdb_id": 106, "title": "Zeta",
		"versions": []string{"movie-remux", "movie-web"}, "minimum_availability": "released"}, 201, &res)
	if got := minimum(res.Title.ID); got != "movie-remux=released movie-web=released" {
		t.Errorf("a choice of released: %s", got)
	}
	if n := e.count(`SELECT COUNT(*) FROM arrview_title WHERE title_id = ? AND minimum_availability = 'released'`, res.Title.ID); n != 2 {
		t.Errorf("released is stored on %d rows, want 2", n)
	}
}

// TestAddMinimumAvailabilityArrView: a connected Radarr client reads the library's own view. The row the add writes
// there (empty root, profile 0, no added date) changes that view's movie in minimumAvailability and the two fields
// Radarr derives from it (isAvailable, releaseDate) and in nothing else, and no other view's movie at all.
func TestAddMinimumAvailabilityArrView(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body map[string]any
	}{
		{"new movie", map[string]any{"kind": "movie", "tmdb_id": 105, "title": "Epsilon", "year": 2024,
			"versions": []string{"movie-remux", "movie-web"}}},
		{"second library", map[string]any{"title_id": 1, "versions": []string{"movie-remux", "movie-web"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			movie := func(minimum, path string) map[string]any {
				e := newAddEnvWith(t, nil, true)
				body := maps.Clone(c.body)
				if minimum != "" {
					body["minimum_availability"] = minimum
				}
				var res AddResult
				e.call("POST", APIBase+"/titles", body, 201, &res)
				if n := e.count(`SELECT COUNT(*) FROM arrview_title WHERE view = 'v/movie-web'`); (n == 1) != (minimum != "") {
					t.Fatalf("minimum %q: %d rows on v/movie-web", minimum, n)
				}
				// Dates still ahead, so that both derived fields depend on the choice: released waits for the
				// digital date, announced is available now and reports the earliest date.
				e.exec(`DELETE FROM title_release_date WHERE title_id = ?`, res.Title.ID)
				e.exec(`INSERT INTO title_release_date (title_id, source, country, type, date, fetched_at) VALUES
					(?, 'tmdb', '', 'theatrical', '2090-01-01', 0), (?, 'tmdb', '', 'digital', '2090-06-01', 0)`,
					res.Title.ID, res.Title.ID)
				var m map[string]any
				e.call("GET", fmt.Sprintf(path+"/radarr/api/v3/movie/%d", arrview.IDBase+res.Title.ID), nil, 200, &m)
				return m
			}
			without, with := movie("", "/v/movie-web"), movie("announced", "/v/movie-web")
			if without["rootFolderPath"] == "" || without["rootFolderPath"] == nil || without["qualityProfileId"] == float64(0) {
				t.Fatalf("the view gives no root or profile: %v %v", without["rootFolderPath"], without["qualityProfileId"])
			}
			if without["minimumAvailability"] != "released" || with["minimumAvailability"] != "announced" {
				t.Errorf("minimumAvailability: %v without, %v with", without["minimumAvailability"], with["minimumAvailability"])
			}
			var differ []string
			for k := range with {
				if !reflect.DeepEqual(with[k], without[k]) {
					differ = append(differ, k)
				}
			}
			for k := range without {
				if _, ok := with[k]; !ok {
					differ = append(differ, k)
				}
			}
			slices.Sort(differ)
			if !slices.Equal(differ, []string{"isAvailable", "minimumAvailability", "releaseDate"}) {
				t.Errorf("the row changes the view's movie in %v", differ)
			}
			if without["isAvailable"] != false || with["isAvailable"] != true {
				t.Errorf("isAvailable: %v without, %v with", without["isAvailable"], with["isAvailable"])
			}
			if without["releaseDate"] != "2090-06-01T00:00:00Z" || with["releaseDate"] != "2090-01-01T00:00:00Z" {
				t.Errorf("releaseDate: %v without, %v with", without["releaseDate"], with["releaseDate"])
			}
			// The policy's radarr view shows both libraries: it reports the earliest of their values
			// (docs/design/arrview.md "Minimum availability").
			if a, b := movie("", ""), movie("announced", ""); a["minimumAvailability"] != "released" || b["minimumAvailability"] != "announced" {
				t.Errorf("the radarr view: %v without, %v with", a["minimumAvailability"], b["minimumAvailability"])
			}
		})
	}
}

// TestAddMinimumAvailabilityKeepsAddedDate: the row the choice is stored on carries no added date of its own, so the
// view shows the copy's own date, as after a default add.
func TestAddMinimumAvailabilityKeepsAddedDate(t *testing.T) {
	t.Parallel()
	for _, minimum := range []string{"", "announced"} {
		e := newAddEnvWith(t, nil, true)
		body := map[string]any{"kind": "movie", "tmdb_id": 105, "title": "Epsilon", "year": 2024, "versions": []string{"movie-web"}}
		if minimum != "" {
			body["minimum_availability"] = minimum
		}
		var res AddResult
		e.call("POST", APIBase+"/titles", body, 201, &res)
		if minimum != "" {
			if n := e.count(`SELECT COUNT(*) FROM arrview_title WHERE view = 'v/movie-web' AND title_id = ? AND added_at = 0`, res.Title.ID); n != 1 {
				t.Fatalf("minimum %q: the row's added date is set", minimum)
			}
		}
		added := func() any {
			var m map[string]any
			e.call("GET", fmt.Sprintf("/v/movie-web/radarr/api/v3/movie/%d", arrview.IDBase+res.Title.ID), nil, 200, &m)
			return m["added"]
		}
		if got := added(); got != addNow.UTC().Format(time.RFC3339) {
			t.Errorf("minimum %q: added %v, want %s", minimum, got, addNow.UTC().Format(time.RFC3339))
		}
	}
}

// TestAddSeriesTypeLibraries: an add that names no libraries takes the ones acquisition will search the series in,
// which reads a series as anime by its flag or its type; an add without series_type picks as before, and libraries
// the request names are left alone. The searches run through a real acquirer: its targets apply the rule. The title
// page marks a monitored copy the rule leaves out (anime_skip), also after a change of type.
func TestAddSeriesTypeLibraries(t *testing.T) {
	e := newAddEnv(t, func(p *policy.Policy) {
		p.Router = &policy.Router{Enabled: true}
	})
	q := jobs.NewQueue(e.st)
	rt, err := routertest.New(e.st, e.pol, q, nil, func() time.Time { return addNow })
	if err != nil {
		t.Fatal(err)
	}
	acq, err := acquire.New(acquire.Config{Store: e.st, Policy: e.pol, Queue: q, Router: rt,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return addNow },
		Search: func(context.Context, decide.Title, decide.Version, *decide.ExistingFile, *decide.SeriesSearch) (*router.Searched, error) {
			return &router.Searched{}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	e.web.add.Acquirer = acq
	plain, anime := catalog.DefaultVersions(e.pol, policy.KindSeries, false), catalog.DefaultVersions(e.pol, policy.KindSeries, true)
	if slices.Equal(plain, anime) || len(plain) == 0 || len(anime) == 0 {
		t.Fatalf("the policy's defaults: %v, anime %v", plain, anime)
	}
	// add adds a new series, gives it an aired episode and searches it: the libraries it got and the searches queued.
	add := func(body map[string]any) (int64, []string, int) {
		t.Helper()
		var res AddResult
		e.call("POST", APIBase+"/titles", body, 201, &res)
		now := store.Millis(addNow)
		e.exec(`INSERT INTO episode (title_id, season, number, name, air_date_utc_at, monitored, created_at, updated_at)
			VALUES (?, 1, 1, 'One', ?, 1, ?, ?)`, res.Title.ID, now-1000, now, now)
		var so SearchOutcome
		e.call("POST", fmt.Sprintf("%s/titles/%d/search", APIBase, res.Title.ID), nil, 200, &so)
		return res.Title.ID, res.Versions, so.Queued
	}
	skips := func(id int64) string {
		t.Helper()
		var d TitleDetail
		e.call("GET", fmt.Sprintf("%s/titles/%d", APIBase, id), nil, 200, &d)
		var out []string
		for _, v := range d.Versions {
			if v.AnimeSkip {
				out = append(out, v.Key)
			}
		}
		return strings.Join(out, " ")
	}
	// No series_type: the libraries main picks, by the flag alone.
	if _, got, queued := add(map[string]any{"kind": "series", "tvdb_id": 301, "title": "Plain"}); !slices.Equal(got, plain) || queued == 0 {
		t.Errorf("no series_type: libraries %v (want %v), %d searches", got, plain, queued)
	}
	if _, got, queued := add(map[string]any{"kind": "series", "tvdb_id": 302, "title": "Flagged", "anime": true}); !slices.Equal(got, anime) || queued == 0 {
		t.Errorf("anime flag: libraries %v (want %v), %d searches", got, anime, queued)
	}
	// Type anime with the flag off: the anime libraries, which are searched.
	id, got, queued := add(map[string]any{"kind": "series", "tvdb_id": 303, "title": "Typed", "series_type": "anime"})
	if !slices.Equal(got, anime) || queued == 0 || skips(id) != "" {
		t.Errorf("series_type anime: libraries %v (want %v), %d searches, skipped %q", got, anime, queued, skips(id))
	}
	if n := e.count(`SELECT COUNT(*) FROM title WHERE id = ? AND anime = 0`, id); n != 1 {
		t.Error("series_type anime set the anime flag")
	}
	// Named libraries are left alone: acquisition does not search that copy, and the title page says so.
	id, got, queued = add(map[string]any{"kind": "series", "tvdb_id": 304, "title": "Named", "series_type": "anime",
		"versions": []string{"tv-2160p"}})
	if !slices.Equal(got, []string{"tv-2160p"}) || queued != 0 || skips(id) != "tv-2160p" {
		t.Errorf("named library: libraries %v, %d searches, skipped %q", got, queued, skips(id))
	}
	// A library no search runs for (paused here) is not marked: the anime rule is not why it is not searched.
	paused := newAddEnv(t, nil)
	paused.call("POST", APIBase+"/titles/series-type", map[string]any{"title_ids": []int64{10}, "series_type": "anime"}, 200, nil)
	var d TitleDetail
	paused.call("GET", APIBase+"/titles/10", nil, 200, &d)
	if len(d.Versions) != 1 || d.Versions[0].AnimeSkip {
		t.Errorf("a paused library: %+v", d.Versions)
	}
	// The title page follows a change of type, both ways.
	if got := skips(10); got != "" {
		t.Fatalf("the seeded series: skipped %q", got)
	}
	for _, c := range []struct{ typ, want string }{{"anime", "tv-2160p"}, {"standard", ""}} {
		e.call("POST", APIBase+"/titles/series-type", map[string]any{"title_ids": []int64{10}, "series_type": c.typ}, 200, nil)
		if got := skips(10); got != c.want {
			t.Errorf("type %s: skipped %q, want %q", c.typ, got, c.want)
		}
	}
}

// TestSearchLibraryNames: the library's name search ignores punctuation, reads the title's other names and the year,
// ranks exact over prefix over word matches, and a TMDb hit marked In library is in the library group too.
func TestSearchLibraryNames(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	now := store.Millis(addNow)
	for i, n := range []struct {
		name string
		year int
	}{{"Spider-Man: Brand New Day", 2026}, {"The Alien Hunter", 2005}, {"Aliens", 1986}, {"Alien", 1979},
		{"Alien: Romulus", 2024}, {"Attack on Titan", 2013}} {
		e.exec(`INSERT INTO title (id, kind, name, sort_name, year, anime, created_at, updated_at) VALUES (?, 'movie', ?, ?, ?, 0,
			?, ?)`, 50+i, n.name, strings.ToLower(n.name), n.year, now, now)
	}
	e.exec(`INSERT INTO title_alias (title_id, source, kind, name) VALUES (55, 'tmdb-original', 'alternative', '進撃の巨人'),
		(55, 'tmdb', 'alternative', 'Shingeki no Kyojin')`)
	names := func(q string) []string {
		var s SearchAnswer
		e.call("GET", APIBase+"/search?scope=library&kind=movie&q="+url.QueryEscape(q), nil, 200, &s)
		out := []string{}
		for _, r := range s.Library {
			out = append(out, r.Name)
		}
		return out
	}
	for q, want := range map[string][]string{
		"spiderman":          {"Spider-Man: Brand New Day"},
		"spider man":         {"Spider-Man: Brand New Day"},
		"alien 1979":         {"Alien"},
		"alien":              {"Alien", "Alien: Romulus", "Aliens", "The Alien Hunter"},
		"shingeki no kyojin": {"Attack on Titan"},
		"進撃の巨人":              {"Attack on Titan"},
	} {
		if got := names(q); !slices.Equal(got, want) {
			t.Errorf("%q: %v, want %v", q, got, want)
		}
	}
	// The lists send sort=name without an order: ranked the same; a chosen order stays as chosen.
	list := func(q string) string {
		var p TitlePage
		e.call("GET", APIBase+"/titles?kind=movie&"+q, nil, 200, &p)
		var out []string
		for _, r := range p.Items {
			out = append(out, r.Name)
		}
		return strings.Join(out, ",")
	}
	if got := list("q=alien&sort=name"); got != "Alien,Alien: Romulus,Aliens,The Alien Hunter" {
		t.Errorf("list, default sort: %s", got)
	}
	if got := list("q=alien&sort=name&order=desc"); got != "The Alien Hunter,Aliens,Alien: Romulus,Alien" {
		t.Errorf("list, chosen order: %s", got)
	}
	// TMDb's "alpha" hit 0 is title 1. Found by name, the screens' tmdb request does not repeat it; renamed so the
	// name search misses it, the tmdb request answers it as its library part (the screens merge it into the group).
	var s SearchAnswer
	e.call("GET", APIBase+"/search?q=alpha&scope=tmdb", nil, 200, &s)
	if len(s.Library) != 0 || !s.TMDb[0].InLibrary {
		t.Errorf("a hit the name search finds is not repeated: %+v", s.Library)
	}
	// No year stored: "alpha 2020" does not find it by name (NULL is no match), so the tmdb request lifts it.
	e.exec(`UPDATE title SET year = NULL WHERE id = 1`)
	s = SearchAnswer{}
	e.call("GET", APIBase+"/search?q=alpha+2020&scope=tmdb", nil, 200, &s)
	if len(s.Library) != 1 || s.Library[0].ID != 1 {
		t.Errorf("no stored year: %+v", s.Library)
	}
	e.exec(`UPDATE title SET name = 'Zeta', sort_name = 'zeta' WHERE id = 1`)
	s = SearchAnswer{}
	e.call("GET", APIBase+"/search?q=alpha&scope=tmdb", nil, 200, &s)
	if s.LibraryTotal != 1 || len(s.Library) != 1 || s.Library[0].ID != 1 || !s.TMDb[0].InLibrary {
		t.Errorf("an In library hit must reach the library group: total %d, %+v", s.LibraryTotal, s.Library)
	}
}

// Libraries kept in sync ({movie-web, movie-hd}, {tv-2160p, tv-1080p}) through the native add, the title page's
// library switch and a compatible view of one library: the partner gets its copy when the named one is new, the
// add's result lists it, and a series' partner gets the add's seasons; monitoring a copy again brings nothing.
func TestSyncGroupsRoutes(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, func(p *policy.Policy) {
		hd := *p.Versions["movie-web"]
		hd.Library = "/media/movies-hd"
		hd.MinimumAvailability = "released"
		p.Versions["movie-hd"] = &hd
		tv := *p.Versions["tv-2160p"]
		tv.Library = "/media/tv-1080p"
		p.Versions["tv-1080p"] = &tv
		p.Library.SyncGroups = [][]string{{"movie-web", "movie-hd"}, {"tv-2160p", "tv-1080p"}}
	})
	var at AddedTitle
	e.call("PUT", APIBase+"/titles/1/versions/movie-web", map[string]any{"monitored": true}, 200, &at)
	if n := e.count(`SELECT COUNT(*) FROM version WHERE title_id = 1 AND policy_key = 'movie-hd' AND monitored = 1`); n != 1 {
		t.Errorf("library switch: movie-hd copies %d, want 1", n)
	}
	e.exec(`UPDATE version SET monitored = 0 WHERE title_id = 1 AND policy_key IN ('movie-web', 'movie-hd')`)
	e.call("PUT", APIBase+"/titles/1/versions/movie-web", map[string]any{"monitored": true}, 200, &at)
	if n := e.count(`SELECT COUNT(*) FROM version WHERE title_id = 1 AND policy_key = 'movie-hd' AND monitored = 0`); n != 1 {
		t.Error("monitoring movie-web again touched movie-hd")
	}
	var res AddResult
	e.call("POST", APIBase+"/titles", map[string]any{"kind": "movie", "tmdb_id": 105, "title": "Epsilon",
		"versions": []string{"movie-web"}}, 201, &res)
	if !slices.Equal(res.Synced, []string{"movie-hd"}) || !slices.Equal(res.New, []string{"movie-web", "movie-hd"}) ||
		!slices.Equal(res.Versions, []string{"movie-web", "movie-hd"}) {
		t.Errorf("native add: %+v", res)
	}
	// The add's minimum availability: movie-web takes it, movie-hd in sync keeps its own.
	e.call("POST", APIBase+"/titles", map[string]any{"kind": "movie", "tmdb_id": 106, "title": "Zeta",
		"versions": []string{"movie-web"}, "minimum_availability": "announced"}, 201, &res)
	if n := e.count(`SELECT COUNT(*) FROM arrview_title WHERE title_id = ? AND view = 'v/movie-web' AND minimum_availability = 'announced'`, res.Title.ID); n != 1 {
		t.Error("movie-web lacks the add's minimum availability")
	}
	if n := e.count(`SELECT COUNT(*) FROM arrview_title WHERE title_id = ? AND view = 'v/movie-hd' AND minimum_availability <> ''`, res.Title.ID); n != 0 {
		t.Error("movie-hd took the add's minimum availability over its own")
	}
	// A series the catalogue has with its episodes and no library yet, added through the view of tv-2160p alone.
	e.exec(`INSERT INTO title (id, kind, name, sort_name, tvdb_id, anime, created_at, updated_at) VALUES
		(20, 'series', 'Lambda', 'lambda', 401, 0, 0, 0)`)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, monitored, created_at, updated_at) VALUES
		(200, 20, 1, 1, 'One', 1, 0, 0), (201, 20, 2, 1, 'Two', 1, 0, 0)`)
	e.call("POST", "/v/tv-2160p/sonarr/api/v3/series", map[string]any{"title": "Lambda", "tvdbId": 401,
		"seasons": []map[string]any{{"seasonNumber": 1, "monitored": true}, {"seasonNumber": 2, "monitored": false}}}, 201, nil)
	for _, k := range []string{"tv-2160p", "tv-1080p"} {
		if n := e.count(`SELECT COUNT(*) FROM episode_version_state s JOIN version v ON v.id = s.version_id
			JOIN episode ep ON ep.id = s.episode_id WHERE v.title_id = 20 AND v.policy_key = ? AND s.monitored = 1
			AND ep.season = 1`, k); n != 1 {
			t.Errorf("compatible add: %s has %d monitored season 1 episodes, want 1", k, n)
		}
		if n := e.count(`SELECT COUNT(*) FROM episode_version_state s JOIN version v ON v.id = s.version_id
			JOIN episode ep ON ep.id = s.episode_id WHERE v.title_id = 20 AND v.policy_key = ? AND s.monitored = 1
			AND ep.season = 2`, k); n != 0 {
			t.Errorf("compatible add: %s monitors season 2", k)
		}
	}
	if s, i := e.acq.counts(); s+i != 0 {
		t.Errorf("searches: %d %d", s, i)
	}
}

// The Show's tv-2160p copy keeps only season 1; removed and turned on again by the title page's library switch (bulk
// add's route too) with tv-2160p and tv-1080p in sync: tv-1080p is created with season 1 only, as Bring in sync
// would; an existing partner is not touched.
func TestSyncPartnerTakesSeasonChoices(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, func(p *policy.Policy) {
		tv := *p.Versions["tv-2160p"]
		tv.Library = "/media/tv-1080p"
		p.Versions["tv-1080p"] = &tv
		p.Library.SyncGroups = [][]string{{"tv-2160p", "tv-1080p"}}
	})
	e.exec(`INSERT INTO series_monitor (version_id, monitor_new, seasons, updated_at) VALUES (10, 'all',
		'{"0":false,"1":true,"2":false}', 0)`)
	e.exec(`UPDATE episode_version_state SET monitored = 0 WHERE version_id = 10 AND episode_id = 103`)
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (10, 'ui', 0)`)
	var vs VersionSet
	e.call("PUT", APIBase+"/titles/10/versions/tv-2160p", map[string]any{"monitored": true}, 200, &vs)
	if !slices.Equal(vs.Synced, []string{"tv-1080p"}) {
		t.Fatalf("synced %v", vs.Synced)
	}
	monitored := func(k string) string {
		var out []string
		_ = scanAll(context.Background(), e.st.Reader(), `SELECT s.episode_id FROM episode_version_state s
			JOIN version v ON v.id = s.version_id WHERE v.title_id = 10 AND v.policy_key = ? AND s.monitored = 1
			ORDER BY s.episode_id`, []any{k}, func(r *sql.Rows) error {
			var s string
			out = append(out, s)
			return r.Scan(&out[len(out)-1])
		})
		return strings.Join(out, ",")
	}
	if got := monitored("tv-1080p"); got != "101,102" {
		t.Errorf("tv-1080p monitors %q, want 101,102", got)
	}
	if n := e.count(`SELECT COUNT(*) FROM series_monitor m JOIN version v ON v.id = m.version_id WHERE v.title_id = 10
		AND v.policy_key = 'tv-1080p' AND m.seasons = '{"0":false,"1":true,"2":false}'`); n != 1 {
		t.Error("tv-1080p lacks the season intent")
	}
	// The partner exists now: removing and restoring tv-2160p again copies nothing onto it.
	e.exec(`UPDATE episode_version_state SET monitored = 1 WHERE episode_id = 103 AND version_id =
		(SELECT id FROM version WHERE title_id = 10 AND policy_key = 'tv-1080p')`)
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (10, 'ui', 0)`)
	vs = VersionSet{}
	e.call("PUT", APIBase+"/titles/10/versions/tv-2160p", map[string]any{"monitored": true}, 200, &vs)
	if got := monitored("tv-1080p"); len(vs.Synced) != 0 || got != "101,102,103" {
		t.Errorf("existing partner: synced %v, monitors %q", vs.Synced, got)
	}
}

// A copy whose only stored choice is "monitor new seasons: none", restored by the library switch: its new partner
// takes that choice too.
func TestSyncPartnerTakesMonitorNewNone(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, func(p *policy.Policy) {
		tv := *p.Versions["tv-2160p"]
		tv.Library = "/media/tv-1080p"
		p.Versions["tv-1080p"] = &tv
		p.Library.SyncGroups = [][]string{{"tv-2160p", "tv-1080p"}}
	})
	e.exec(`DELETE FROM episode_version_state WHERE version_id = 10`)
	e.exec(`INSERT INTO series_monitor (version_id, monitor_new, seasons, updated_at) VALUES (10, 'none', '{}', 0)`)
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (10, 'ui', 0)`)
	var vs VersionSet
	e.call("PUT", APIBase+"/titles/10/versions/tv-2160p", map[string]any{"monitored": true}, 200, &vs)
	if n := e.count(`SELECT COUNT(*) FROM series_monitor m JOIN version v ON v.id = m.version_id WHERE v.title_id = 10
		AND v.policy_key = 'tv-1080p' AND m.monitor_new = 'none'`); !slices.Equal(vs.Synced, []string{"tv-1080p"}) || n != 1 {
		t.Errorf("synced %v; tv-1080p with monitor new none: %d", vs.Synced, n)
	}
}

// Each partner takes the season choices of the named library it is in sync with, never of another library the add
// also names: tv-2160p stands alone, tv-b and tv-c are kept in sync.
func TestSyncPartnerTakesItsOwnLibrarysChoices(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, func(p *policy.Policy) {
		for _, k := range []string{"tv-b", "tv-c"} {
			v := *p.Versions["tv-2160p"]
			v.Library = "/media/" + k
			p.Versions[k] = &v
		}
		p.Library.SyncGroups = [][]string{{"tv-b", "tv-c"}}
	})
	monitorNew := func(k string) string {
		var m string
		_ = e.st.Reader().QueryRow(`SELECT m.monitor_new FROM series_monitor m JOIN version v ON v.id = m.version_id
			WHERE v.title_id = 10 AND v.policy_key = ?`, k).Scan(&m)
		return m
	}
	// tv-2160p, already there, keeps no new seasons; tv-b is new, so tv-c follows the add, not tv-2160p.
	e.exec(`DELETE FROM episode_version_state WHERE version_id = 10`)
	e.exec(`INSERT INTO series_monitor (version_id, monitor_new, seasons, updated_at) VALUES (10, 'none', '{}', 0)`)
	var res AddResult
	e.call("POST", APIBase+"/titles", map[string]any{"title_id": 10, "versions": []string{"tv-2160p", "tv-b"}}, 201, &res)
	if !slices.Equal(res.Synced, []string{"tv-c"}) || monitorNew("tv-b") != "all" || monitorNew("tv-c") != "all" {
		t.Errorf("new tv-b: synced %v, tv-b %q, tv-c %q", res.Synced, monitorNew("tv-b"), monitorNew("tv-c"))
	}
	// tv-b and tv-c removed, tv-b keeping no new seasons, tv-2160p back to all: the restored tv-c follows tv-b.
	e.exec(`UPDATE series_monitor SET monitor_new = 'all' WHERE version_id = 10`)
	e.exec(`UPDATE series_monitor SET monitor_new = 'none' WHERE version_id = (SELECT id FROM version
		WHERE title_id = 10 AND policy_key = 'tv-b')`)
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) SELECT id, 'ui', 0 FROM version
		WHERE title_id = 10 AND policy_key IN ('tv-b', 'tv-c')`)
	res = AddResult{}
	e.call("POST", APIBase+"/titles", map[string]any{"title_id": 10, "versions": []string{"tv-2160p", "tv-b"}}, 201, &res)
	if !slices.Equal(res.Synced, []string{"tv-c"}) || monitorNew("tv-c") != "none" {
		t.Errorf("restored tv-b: synced %v, tv-c %q", res.Synced, monitorNew("tv-c"))
	}
}

// The Sonarr mass editor that makes a standard series anime and picks the anime profile creates the anime copy and
// its anime-only partner: the edited type is stored before the copies are turned on.
func TestSyncGroupsEditorSeriesType(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, func(p *policy.Policy) {
		a2 := *p.Versions["anime-tv"]
		a2.Library = "/media/anime-2"
		p.Versions["anime-tv-2"] = &a2
		p.Library.SyncGroups = [][]string{{"anime-tv", "anime-tv-2"}}
	})
	e.call("PUT", "/sonarr/api/v3/series/editor", map[string]any{"seriesIds": []int64{arrview.IDBase + 10},
		"qualityProfileId": e.profile("sonarr", "Anime"), "seriesType": "anime"}, 202, nil)
	for _, k := range []string{"anime-tv", "anime-tv-2"} {
		if n := e.count(`SELECT COUNT(*) FROM version WHERE title_id = 10 AND policy_key = ?`, k); n != 1 {
			t.Errorf("%s: %d copies, want 1", k, n)
		}
	}
}

// A series every copy of which was removed, added again as anime: its new type counts for the libraries in sync, so
// the anime-only partner of the named anime library gets its copy.
func TestSyncGroupsReaddedSeriesType(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, func(p *policy.Policy) {
		a2 := *p.Versions["anime-tv"]
		a2.Library = "/media/anime-2"
		p.Versions["anime-tv-2"] = &a2
		p.Library.SyncGroups = [][]string{{"anime-tv", "anime-tv-2"}}
	})
	e.exec(`UPDATE title SET series_type = 'standard' WHERE id = 10`)
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (10, 'ui', 0)`)
	e.call("POST", APIBase+"/titles", map[string]any{"title_id": 10, "versions": []string{"anime-tv"},
		"series_type": "anime"}, 201, nil)
	for _, k := range []string{"anime-tv", "anime-tv-2"} {
		if n := e.count(`SELECT COUNT(*) FROM version WHERE title_id = 10 AND policy_key = ?`, k); n != 1 {
			t.Errorf("%s: %d copies, want 1", k, n)
		}
	}
}

// A series with no stored type, every copy removed, added again through the Sonarr-compatible API as anime: the type
// counts for the libraries in sync, so the anime-only partner of the anime library gets its copy too.
func TestSyncGroupsCompatibleReaddSeriesType(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, func(p *policy.Policy) {
		a2 := *p.Versions["anime-tv"]
		a2.Library = "/media/anime-2"
		p.Versions["anime-tv-2"] = &a2
		p.Library.SyncGroups = [][]string{{"anime-tv", "anime-tv-2"}}
	})
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (10, 'ui', 0)`)
	e.call("POST", "/v/anime-tv/sonarr/api/v3/series", map[string]any{"title": "The Show", "tvdbId": 201,
		"seriesType": "anime", "monitored": true}, 201, nil)
	for _, k := range []string{"anime-tv", "anime-tv-2"} {
		if n := e.count(`SELECT COUNT(*) FROM version WHERE title_id = 10 AND policy_key = ?`, k); n != 1 {
			t.Errorf("%s: %d copies, want 1", k, n)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM title WHERE id = 10 AND series_type = 'anime'`); n != 1 {
		t.Error("the add's series type is not stored")
	}
}

// A person's add fills the whole group: The Show with both copies removed, added again to tv-2160p by the add sheet
// and by the compatible API, gets its tv-1080p copy back, monitored, as the add options showed.
func TestSyncGroupsRestoreRemovedPartner(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, func(p *policy.Policy) {
		tv := *p.Versions["tv-2160p"]
		tv.Library = "/media/tv-1080p"
		p.Versions["tv-1080p"] = &tv
		p.Library.SyncGroups = [][]string{{"tv-2160p", "tv-1080p"}}
	})
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
		(11, 10, 'tv-1080p', 1, 'wanted', 0, 0)`)
	removeAll := func() {
		e.exec(`UPDATE version SET monitored = 0 WHERE title_id = 10`)
		e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (10, 'ui', 0), (11, 'ui', 0)`)
	}
	live := func() int {
		return e.count(`SELECT COUNT(*) FROM version v WHERE v.title_id = 10 AND v.monitored = 1 AND NOT EXISTS
			(SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id)`)
	}
	removeAll()
	var o AddOptions
	e.call("GET", APIBase+"/add/options?title_id=10", nil, 200, &o)
	for _, v := range o.Versions {
		if v.Key == "tv-2160p" && !slices.Equal(v.Synced, []string{"tv-1080p"}) {
			t.Errorf("options: tv-2160p synced %v", v.Synced)
		}
	}
	var res AddResult
	e.call("POST", APIBase+"/titles", map[string]any{"title_id": 10, "versions": []string{"tv-2160p"}}, 201, &res)
	if !slices.Equal(res.Synced, []string{"tv-1080p"}) || live() != 2 {
		t.Errorf("add sheet: synced %v, %d live copies, want 2", res.Synced, live())
	}
	// The restored copy takes the named copy's episode choices (the add named no seasons).
	if n := e.count(`SELECT COUNT(*) FROM episode_version_state WHERE version_id = 11 AND monitored = 1`); n != 3 {
		t.Errorf("restored tv-1080p monitors %d episodes, want 3", n)
	}
	removeAll()
	e.call("POST", "/v/tv-2160p/sonarr/api/v3/series", map[string]any{"title": "The Show", "tvdbId": 201,
		"monitored": true}, 201, nil)
	if n := live(); n != 2 {
		t.Errorf("compatible add: %d live copies, want 2", n)
	}
}

// A movie partner restored in sync keeps its saved minimum availability: the add's choice is for the libraries it
// names, through the native add and the Radarr-compatible one.
func TestSyncGroupsRestoredPartnerKeepsMinimum(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, func(p *policy.Policy) {
		hd := *p.Versions["movie-web"]
		hd.Library = "/media/movies-hd"
		hd.MinimumAvailability = ""
		p.Versions["movie-hd"] = &hd
		p.Versions["movie-web"].MinimumAvailability = ""
		p.Library.SyncGroups = [][]string{{"movie-web", "movie-hd"}}
	})
	// Alpha (1): movie-remux, and a removed movie-hd copy saved as Released.
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
		(2, 1, 'movie-hd', 0, 'wanted', 0, 0)`)
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (2, 'ui', 0)`)
	e.exec(`INSERT INTO arrview_title (view, title_id, minimum_availability, added_at, updated_at) VALUES
		('v/movie-hd', 1, 'released', 0, 0)`)
	minimum := func(k string) string {
		var m string
		_ = e.st.Reader().QueryRow(`SELECT minimum_availability FROM arrview_title WHERE view = ? AND title_id = 1`,
			"v/"+k).Scan(&m)
		return m
	}
	var res AddResult
	e.call("POST", APIBase+"/titles", map[string]any{"title_id": 1, "versions": []string{"movie-web"},
		"minimum_availability": "announced"}, 201, &res)
	if !slices.Equal(res.Synced, []string{"movie-hd"}) || minimum("movie-web") != "announced" || minimum("movie-hd") != "released" {
		t.Errorf("native add: synced %v, movie-web %q, movie-hd %q", res.Synced, minimum("movie-web"), minimum("movie-hd"))
	}
	// Every copy removed again, then added through the Radarr view of movie-web.
	e.exec(`UPDATE version SET monitored = 0 WHERE title_id = 1`)
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) SELECT id, 'ui', 0 FROM version
		WHERE title_id = 1`)
	e.call("POST", "/v/movie-web/radarr/api/v3/movie", map[string]any{"title": "Alpha", "tmdbId": 101,
		"minimumAvailability": "announced", "monitored": true}, 201, nil)
	if n := e.count(`SELECT COUNT(*) FROM version v WHERE v.title_id = 1 AND v.policy_key = 'movie-hd' AND NOT EXISTS
		(SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id)`); n != 1 || minimum("movie-hd") != "released" {
		t.Errorf("compatible add: movie-hd live %d, minimum %q", n, minimum("movie-hd"))
	}
}

// One answer to "which libraries in sync would gain a copy" (catalog.SyncPartners) serves the add options, the title
// page's library switch and a request's stored libraries: a partner the title has a live copy of is never listed, a
// removed one is (a person's add restores it), and a request stores the partners next to the libraries it asks for.
func TestSyncGroupsAnswers(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, func(p *policy.Policy) {
		hd := *p.Versions["movie-web"]
		hd.Library = "/media/movies-hd"
		p.Versions["movie-hd"] = &hd
		p.Library.SyncGroups = [][]string{{"movie-web", "movie-hd"}}
	})
	synced := func(o AddOptions) map[string][]string {
		out := map[string][]string{}
		for _, v := range o.Versions {
			if len(v.Synced) > 0 {
				out[v.Key] = v.Synced
			}
		}
		return out
	}
	var o AddOptions
	e.call("GET", APIBase+"/add/options?kind=movie&tmdb_id=900", nil, 200, &o)
	if got := fmt.Sprint(synced(o)); got != "map[movie-hd:[movie-web] movie-web:[movie-hd]]" {
		t.Errorf("new title: %s", got)
	}
	// Alpha (title 1, movie-remux) with a removed movie-hd copy: ticking movie-web brings it back, shown as synced
	// and removed, and the switch restores exactly that.
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
		(2, 1, 'movie-hd', 0, 'wanted', 0, 0)`)
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (2, 'ui', 0)`)
	o = AddOptions{}
	e.call("GET", APIBase+"/add/options?title_id=1", nil, 200, &o)
	if got := fmt.Sprint(synced(o)); got != "map[movie-hd:[movie-web] movie-web:[movie-hd]]" {
		t.Errorf("removed partner: %s", got)
	}
	for _, v := range o.Versions {
		if v.Removed != (v.Key == "movie-hd") {
			t.Errorf("%s: removed %v", v.Key, v.Removed)
		}
	}
	var vs VersionSet
	e.call("PUT", APIBase+"/titles/1/versions/movie-web", map[string]any{"monitored": true}, 200, &vs)
	if vs.ID != 1 || !slices.Equal(vs.Synced, []string{"movie-hd"}) ||
		e.count(`SELECT COUNT(*) FROM version WHERE id = 2 AND monitored = 1 AND NOT EXISTS (SELECT 1 FROM
			arrview_removed_version r WHERE r.version_id = 2)`) != 1 {
		t.Errorf("switch with a removed partner: %+v", vs)
	}
	e.exec(`DELETE FROM version WHERE title_id = 1 AND policy_key IN ('movie-web', 'movie-hd')`)
	vs = VersionSet{}
	e.call("PUT", APIBase+"/titles/1/versions/movie-web", map[string]any{"monitored": true}, 200, &vs)
	if !slices.Equal(vs.Synced, []string{"movie-hd"}) || len(vs.Versions) != 3 {
		t.Errorf("switch: %+v", vs)
	}
	// A request for movie-web stores movie-hd with it; one for a title holding movie-hd unmonitored does not.
	annID, ann := e.user("ann", auth.RoleUser)
	web := []string{"movie-web"}
	if err := e.au.UpdateUser(context.Background(), annID, auth.UserPatch{RequestVersions: &web}); err != nil {
		t.Fatal(err)
	}
	var cr CreateResult
	e.callAs(ann, "POST", APIBase+"/requests", map[string]any{"kind": "movie", "tmdb_id": 106, "name": "Zeta"}, 201, &cr)
	if got := keysOf(cr.Request); !slices.Equal(got, []string{"movie-hd", "movie-web"}) {
		t.Errorf("request: %v", got)
	}
	zeta := cr.Request.ID
	e.exec(`INSERT INTO title (id, kind, name, sort_name, tmdb_id, anime, created_at, updated_at) VALUES
		(30, 'movie', 'Mu', 'mu', 130, 0, 0, 0)`)
	e.exec(`INSERT INTO version (title_id, policy_key, monitored, state, created_at, updated_at) VALUES
		(30, 'movie-hd', 0, 'wanted', 0, 0)`)
	e.callAs(ann, "POST", APIBase+"/requests", map[string]any{"title_id": 30}, 201, &cr)
	if got := keysOf(cr.Request); !slices.Equal(got, []string{"movie-web"}) {
		t.Errorf("request with an unmonitored partner: %v", got)
	}
	// An approval edited down to movie-web still adds movie-hd in sync, and the request keeps following it.
	var ap ApproveResult
	e.call("POST", fmt.Sprintf("%s/requests/%d/approve", APIBase, zeta), map[string]any{"versions": []string{"movie-web"}},
		200, &ap)
	if got := keysOf(ap.Request); !slices.Equal(got, []string{"movie-hd", "movie-web"}) {
		t.Errorf("edited approval: %v", got)
	}
}

// A request for a series the catalogue has by TVDB id only looks it up as its approval will: its removed copy in a
// library in sync is stored as a partner, and the approval restores it. A series whose stored type is anime
// offers the anime library's partner in the add options.
func TestSyncGroupsSeriesByTVDB(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, func(p *policy.Policy) {
		tv := *p.Versions["tv-2160p"]
		tv.Library = "/media/tv-1080p"
		p.Versions["tv-1080p"] = &tv
		a2 := *p.Versions["anime-tv"]
		a2.Library = "/media/anime-2"
		p.Versions["anime-tv-2"] = &a2
		p.Library.SyncGroups = [][]string{{"tv-2160p", "tv-1080p"}, {"anime-tv", "anime-tv-2"}}
	})
	e.exec(`INSERT INTO title (id, kind, name, sort_name, tvdb_id, anime, created_at, updated_at) VALUES
		(40, 'series', 'Omega', 'omega', 940, 0, 0, 0)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
		(40, 40, 'tv-2160p', 0, 'wanted', 0, 0)`)
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (40, 'ui', 0)`)
	annID, ann := e.user("ann", auth.RoleUser)
	hd := []string{"tv-1080p"}
	if err := e.au.UpdateUser(context.Background(), annID, auth.UserPatch{RequestVersions: &hd}); err != nil {
		t.Fatal(err)
	}
	var cr CreateResult
	e.callAs(ann, "POST", APIBase+"/requests", map[string]any{"kind": "series", "tvdb_id": 940, "name": "Omega"}, 201, &cr)
	// The removed tv-2160p copy is a partner of the request, and the approval restores it, monitored.
	if got := keysOf(cr.Request); !slices.Equal(got, []string{"tv-1080p", "tv-2160p"}) {
		t.Errorf("request: %v", got)
	}
	var ap ApproveResult
	e.call("POST", fmt.Sprintf("%s/requests/%d/approve", APIBase, cr.Request.ID), map[string]any{}, 200, &ap)
	if n := e.count(`SELECT COUNT(*) FROM version v WHERE v.id = 40 AND v.monitored = 1 AND NOT EXISTS (SELECT 1
		FROM arrview_removed_version r WHERE r.version_id = 40)`); n != 1 {
		t.Error("approval left the removed tv-2160p copy removed")
	}
	// The Show (10) with a stored anime type: ticking anime-tv brings anime-tv-2.
	e.exec(`UPDATE title SET series_type = 'anime' WHERE id = 10`)
	var o AddOptions
	e.call("GET", APIBase+"/add/options?title_id=10", nil, 200, &o)
	for _, v := range o.Versions {
		if v.Key == "anime-tv" && !slices.Equal(v.Synced, []string{"anime-tv-2"}) {
			t.Errorf("anime-tv of an anime-type series: synced %v", v.Synced)
		}
	}
}

// Bring in sync over a group {tv-2160p, tv-b, tv-c}: the preview counts per library what the action adds, the
// action gives each title its missing copies like its first monitored copy (a series' season and episode
// monitoring included), restores nothing, touches no existing copy, and searches only when asked, through the
// paced schedule.
func TestBringInSync(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, func(p *policy.Policy) {
		for _, k := range []string{"tv-b", "tv-c"} {
			v := *p.Versions["tv-2160p"]
			v.Library = "/media/" + k
			p.Versions[k] = &v
		}
		p.Library.SyncGroups = [][]string{{"tv-2160p", "tv-b", "tv-c"}}
	})
	// The Show (10) is in tv-2160p with seasons 1 and 2 monitored, specials not, and new seasons not.
	e.exec(`INSERT INTO series_monitor (version_id, monitor_new, seasons, updated_at) VALUES (10, 'none',
		'{"0":false,"1":true,"2":true}', 0)`)
	series := func(id int64, name string, copies ...string) {
		e.exec(`INSERT INTO title (id, kind, name, sort_name, tvdb_id, anime, created_at, updated_at) VALUES
			(?, 'series', ?, ?, ?, 0, 0, 0)`, id, name, strings.ToLower(name), 500+id)
		for _, c := range copies {
			k, mon := strings.TrimRight(c, "-!"), !strings.HasSuffix(c, "-") && !strings.HasSuffix(c, "!")
			e.exec(`INSERT INTO version (title_id, policy_key, monitored, state, created_at, updated_at) VALUES
				(?, ?, ?, 'wanted', 0, 0)`, id, k, mon)
			if strings.HasSuffix(c, "!") {
				e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) SELECT id, 'ui', 0
					FROM version WHERE title_id = ? AND policy_key = ?`, id, k)
			}
		}
	}
	series(40, "Only B", "tv-b")
	series(41, "Removed C", "tv-2160p", "tv-c!")
	series(42, "Unmonitored", "tv-2160p-", "tv-b-")
	series(43, "Complete", "tv-2160p", "tv-b-", "tv-c!")
	path := APIBase + "/settings/versions/tv-b/bring-in-sync"
	var b BringInSync
	e.call("GET", path, nil, 200, &b)
	counts := func(b BringInSync) string {
		var out []string
		for _, l := range b.Libraries {
			out = append(out, fmt.Sprintf("%s:%d%v", l.Key, l.Count, l.Names))
		}
		return fmt.Sprintf("%d %s", b.Titles, strings.Join(out, " "))
	}
	want := "4 tv-2160p:1[Only B] tv-b:2[Removed C The Show] tv-c:3[Only B The Show Unmonitored]"
	if got := counts(b); got != want || b.Source == "" {
		t.Errorf("preview %s, want %s", got, want)
	}
	all := func() string {
		var out []string
		_ = scanAll(context.Background(), e.st.Reader(), `SELECT v.title_id || ':' || v.policy_key || ':' || v.monitored ||
			CASE WHEN EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id) THEN ':removed' ELSE '' END
			FROM version v WHERE v.title_id >= 10 ORDER BY v.title_id, v.policy_key`, nil, func(r *sql.Rows) error {
			var s string
			out = append(out, s)
			return r.Scan(&out[len(out)-1])
		})
		return strings.Join(out, " ")
	}
	e.call("POST", path, map[string]any{"search": false}, 200, &b)
	if got := counts(b); got != want || b.Searched != 0 {
		t.Errorf("action %s, searched %d", got, b.Searched)
	}
	if got, want := all(), "10:tv-2160p:1 10:tv-b:1 10:tv-c:1 40:tv-2160p:1 40:tv-b:1 40:tv-c:1 "+
		"41:tv-2160p:1 41:tv-b:1 41:tv-c:0:removed 42:tv-2160p:0 42:tv-b:0 42:tv-c:0 "+
		"43:tv-2160p:1 43:tv-b:0 43:tv-c:0:removed"; got != want {
		t.Errorf("copies\n%s\nwant\n%s", got, want)
	}
	// The Show's new copies monitor what tv-2160p does, episode by episode, with its intent.
	for _, k := range []string{"tv-b", "tv-c"} {
		if n := e.count(`SELECT COUNT(*) FROM episode_version_state a JOIN version va ON va.id = a.version_id
			JOIN version vb ON vb.title_id = va.title_id AND vb.policy_key = ? JOIN episode_version_state b
			ON b.version_id = vb.id AND b.episode_id = a.episode_id AND b.monitored = a.monitored
			WHERE va.id = 10`, k); n != 4 {
			t.Errorf("%s: %d episodes monitored like tv-2160p, want 4", k, n)
		}
		if n := e.count(`SELECT COUNT(*) FROM series_monitor m JOIN version v ON v.id = m.version_id WHERE
			v.title_id = 10 AND v.policy_key = ? AND m.monitor_new = 'none' AND m.seasons =
			(SELECT seasons FROM series_monitor WHERE version_id = 10)`, k); n != 1 {
			t.Errorf("%s: intent not copied", k)
		}
	}
	if s, i := e.acq.counts(); s+i != 0 || len(e.acq.later) != 0 {
		t.Errorf("search off: submits %d, interactive %d, later %v", s, i, e.acq.later)
	}
	e.call("GET", path, nil, 200, &b)
	if b.Titles != 0 {
		t.Errorf("after the action: %s", counts(b))
	}
	// With search: the new copies of a monitored source go on the paced schedule, nothing searches at once.
	series(44, "Late", "tv-2160p")
	series(45, "Late unmonitored", "tv-2160p-")
	e.call("POST", path, map[string]any{"search": true}, 200, &b)
	if s, i := e.acq.counts(); s+i != 0 || !slices.Equal(e.acq.later, []string{"44:tv-b,tv-c"}) || b.Searched != 1 {
		t.Errorf("search on: submits %d, interactive %d, later %v, searched %d", s, i, e.acq.later, b.Searched)
	}
	// A library outside every group has no Bring in sync.
	e.call("GET", APIBase+"/settings/versions/movie-web/bring-in-sync", nil, 404, nil)
}

// A title every copy of which was removed is added like a new one: the sheet's anime choice, which the add writes
// first, decides the libraries in sync, so the add options show what the add creates, either way.
func TestSyncGroupsRemovedTitleAnimeChoice(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, func(p *policy.Policy) {
		hd := *p.Versions["movie-web"]
		hd.Library = "/media/movies-hd"
		p.Versions["movie-hd"] = &hd
		a2 := *p.Versions["anime-movie"]
		a2.Library = "/media/anime-movies-2"
		p.Versions["anime-movie-2"] = &a2
		p.Library.SyncGroups = [][]string{{"movie-web", "movie-hd"}, {"anime-movie", "anime-movie-2"}}
	})
	for _, c := range []struct {
		id          int64
		stored, now bool
		key, synced string
	}{{150, true, false, "movie-web", "movie-hd"}, {151, false, true, "anime-movie", "anime-movie-2"}} {
		e.exec(`INSERT INTO title (id, kind, name, sort_name, tmdb_id, anime, created_at, updated_at) VALUES
			(?, 'movie', 'Removed', 'removed', ?, ?, 0, 0)`, c.id, 100+c.id, c.stored)
		e.exec(`INSERT INTO version (title_id, policy_key, monitored, state, created_at, updated_at) VALUES
			(?, 'movie-remux', 0, 'wanted', 0, 0)`, c.id)
		e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) SELECT id, 'ui', 0 FROM version
			WHERE title_id = ?`, c.id)
		var o AddOptions
		e.call("GET", fmt.Sprintf("%s/add/options?title_id=%d&anime=%t", APIBase, c.id, c.now), nil, 200, &o)
		for _, v := range o.Versions {
			if v.Key == c.key && !slices.Equal(v.Synced, []string{c.synced}) {
				t.Errorf("title %d: %s shows synced %v, want %s", c.id, c.key, v.Synced, c.synced)
			}
		}
		var res AddResult
		e.call("POST", APIBase+"/titles", map[string]any{"title_id": c.id, "anime": c.now, "versions": []string{c.key}},
			201, &res)
		if !slices.Equal(res.Synced, []string{c.synced}) {
			t.Errorf("title %d: the add created %v in sync, want %s", c.id, res.Synced, c.synced)
		}
	}
}
