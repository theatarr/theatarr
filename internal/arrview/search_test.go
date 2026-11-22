package arrview

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/router/routertest"
	"github.com/theatarr/theatarr/internal/settings"
)

// searchEnv is the views with movie-web and tv-2160p active, the router switched on over fake adders, and a fake
// indexer search that accepts one public release per call.
type searchEnv struct {
	*env
	q                 *jobs.Queue
	w                 *jobs.Worker
	rt                *routertest.Fake
	mu                sync.Mutex
	calls             []string // version title s<season>e<episode>
	generations       []int64
	currentGeneration int64
	n                 int
}

func newSearchEnv(t *testing.T) *searchEnv {
	e := &searchEnv{env: newEnv(t)}
	e.onlyLibraries("movie-web", "tv-2160p")
	e.pol.Router = &policy.Router{Enabled: true}
	// These tests drain jobs on a frozen clock: RD's 20s min_gap (router gap_test.go) would hold every add after the
	// first.
	e.pol.Providers["rd"].Pacing.MinGap = new(policy.Duration)
	now := func() time.Time { return e.now }
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.q = jobs.NewQueue(e.st)
	e.q.SetClock(now)
	e.w = jobs.NewWorker(e.q, jobs.WorkerOptions{Logger: log})
	var err error
	if e.rt, err = routertest.New(e.st, e.pol, e.q, e.w, now); err != nil {
		t.Fatal(err)
	}
	a, err := acquire.New(acquire.Config{Store: e.st, Policy: e.pol, Queue: e.q, Router: e.rt, Search: e.search,
		Logger: log, Now: now, CaptureRuntime: func(ctx context.Context) context.Context {
			return settings.WithGeneration(ctx, e.currentGeneration)
		}})
	if err != nil {
		t.Fatal(err)
	}
	a.RegisterJobs(e.w)
	e.searcher = a
	e.start()
	return e
}

func (e *searchEnv) search(ctx context.Context, t decide.Title, v decide.Version, _ *decide.ExistingFile,
	ss *decide.SeriesSearch) (*router.Searched, error) {
	e.mu.Lock()
	e.generations = append(e.generations, settings.ContextGeneration(ctx))
	call := v.Key + " " + t.Name
	if ss != nil {
		call += " s" + strconv.Itoa(ss.Season) + "e" + strconv.Itoa(ss.Episode)
	}
	e.calls = append(e.calls, call)
	e.n++
	n := e.n
	e.mu.Unlock()
	out := &router.Searched{Ranked: []*decide.Decision{{}}}
	out.Request = router.Request{TitleID: t.ID, VersionID: v.ID, Version: v.Key, Candidates: []router.Candidate{{
		Release: indexer.Release{Indexer: "prowlarr-public", Kind: "torznab", Privacy: policy.PrivacyPublic,
			Title:    strings.ReplaceAll(t.Name, " ", ".") + ".2160p.UHD.BluRay.REMUX.HEVC-GRP" + strconv.Itoa(n),
			InfoHash: strings.Repeat(strconv.Itoa(n%10), 40), Size: 10 << 30}, Quality: "Remux-2160p", Score: 10}}}
	return out, nil
}

func (e *searchEnv) drain() {
	e.t.Helper()
	for range 100 {
		ran, err := e.w.RunOne(context.Background())
		if err != nil {
			e.t.Fatal(err)
		}
		if !ran {
			return
		}
	}
	e.t.Fatal("jobs never drained")
}

func (e *searchEnv) searches() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := append([]string(nil), e.calls...)
	e.calls = nil
	return out
}

func (e *searchEnv) command(id any) map[string]any {
	var c map[string]any
	e.call("GET", "/radarr/api/v3/command/"+strconv.Itoa(int(id.(float64))), nil, 200, &c)
	return c
}

// A search command for an active version searches it and routes the result; its status follows the search and the
// router (queued, then completed with what was added). A copy in a library gone from the policy is not searched.
func TestSearchCommandActiveVersion(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	e.svc.captureRuntime = func(ctx context.Context) context.Context { return settings.WithGeneration(ctx, 23) }
	var c map[string]any
	e.call("POST", "/radarr/api/v3/command", map[string]any{"name": "MoviesSearch", "movieIds": []int{IDBase + 1}},
		201, &c)
	if c["status"] != "queued" || c["message"] != "Searching 1 items for movie-web" {
		t.Fatalf("posted %v: %v", c["status"], c["message"])
	}
	e.currentGeneration = 24
	e.drain()
	if got := e.searches(); strings.Join(got, "|") != "movie-web Alpha" {
		t.Fatalf("searches %v", got)
	}
	e.mu.Lock()
	generations := append([]int64(nil), e.generations...)
	e.mu.Unlock()
	if len(generations) != 1 || generations[0] != 24 {
		t.Fatalf("queued arr-view search did not capture the current runtime when it started: %v", generations)
	}
	c = e.command(c["id"])
	if c["status"] != "completed" || c["result"] != "successful" || c["ended"] == nil ||
		!strings.Contains(c["message"].(string), "added Alpha.2160p.UHD.BluRay.REMUX.HEVC-GRP1 on rd") {
		t.Fatalf("after the route: %v %v %v", c["status"], c["result"], c["message"])
	}
	if n := e.one(`SELECT COUNT(*) FROM download WHERE version_key = 'movie-web' AND title_id = 1 AND state = 'added'`); n != 1 {
		t.Errorf("downloads %d", n)
	}

	// Sonarr: a season, an episode, the missing episodes of a series (aired only), each its own search.
	e.call("POST", "/sonarr/api/v3/command", map[string]any{"name": "SeasonSearch", "seriesId": IDBase + 10, "seasonNumber": 1},
		201, &c)
	e.call("POST", "/sonarr/api/v3/command", map[string]any{"name": "EpisodeSearch", "episodeIds": []int{102}}, 201, &c)
	e.drain()
	e.call("POST", "/sonarr/api/v3/command", map[string]any{"name": "MissingEpisodeSearch", "seriesId": IDBase + 10}, 201, &c)
	// S01E02 is in flight now (the episode search's download): the missing search skips it.
	e.drain()
	if got := e.searches(); strings.Join(got, "|") != "tv-2160p The Show s1e0|tv-2160p The Show s1e2" {
		t.Errorf("series searches %v", got)
	}
	c = e.command(c["id"])
	if c["status"] != "completed" || !strings.Contains(c["message"].(string), "in flight") {
		t.Errorf("missing search %v %v", c["status"], c["message"])
	}

	// An add with its search option, on the active version's own view.
	e.call("POST", "/v/movie-web/radarr/api/v3/movie", map[string]any{"title": "Iota", "year": 2024, "tmdbId": 109,
		"minimumAvailability": "announced",
		"addOptions":          map[string]any{"searchForMovie": true}}, 201, nil)
	e.drain()
	if got := e.searches(); strings.Join(got, "|") != "movie-web Iota" {
		t.Errorf("add search %v", got)
	}
	// No download for a library gone from the policy, and every add is for one of ours.
	if n := e.one(`SELECT COUNT(*) FROM download WHERE version_key NOT IN ('movie-web', 'tv-2160p')`); n != 0 {
		t.Errorf("%d downloads for other libraries", n)
	}
	// Alpha, the season, the episode (searched before the season's route ran) and Iota.
	// Three adds: the episode search finds S01E02 covered by the season search's pack, which is in flight.
	if n := int64(len(e.rt.Adders["rd"].Adds())); n != e.one(`SELECT COUNT(*) FROM download`) || n != 3 {
		t.Errorf("adds %d", n)
	}
}

// A compatible add through the view of one library of a sync group, with its search option, searches the library in
// sync it created too: movies (searchForMovie) and series (searchForMissingEpisodes).
func TestSearchOnAddCoversSyncedLibrary(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	hd := *e.pol.Versions["movie-web"]
	hd.Library = "/media/movies-hd"
	e.pol.Versions["movie-hd"] = &hd
	tv := *e.pol.Versions["tv-2160p"]
	tv.Library = "/media/tv-1080p"
	e.pol.Versions["tv-1080p"] = &tv
	e.pol.Library.SyncGroups = [][]string{{"movie-web", "movie-hd"}, {"tv-2160p", "tv-1080p"}}
	e.call("POST", "/v/movie-web/radarr/api/v3/movie", map[string]any{"title": "Iota", "year": 2024, "tmdbId": 109,
		"minimumAvailability": "announced", "addOptions": map[string]any{"searchForMovie": true}}, 201, nil)
	e.drain()
	got := e.searches()
	slices.Sort(got)
	if strings.Join(got, "|") != "movie-hd Iota|movie-web Iota" {
		t.Errorf("movie add search %v", got)
	}
	e.exec(`INSERT INTO title (id, kind, name, sort_name, tvdb_id, anime, created_at, updated_at) VALUES
		(40, 'series', 'Mu', 'mu', 401, 0, 0, 0)`)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, air_date, air_date_utc_at, runtime_minutes,
		monitored, created_at, updated_at) VALUES (400, 40, 1, 1, 'One', '2019-01-07', ?, 45, 1, 0, 0)`,
		ms(time.Date(2019, 1, 8, 2, 0, 0, 0, time.UTC)))
	e.call("POST", "/v/tv-2160p/sonarr/api/v3/series", map[string]any{"title": "Mu", "tvdbId": 401,
		"seasons":    []map[string]any{{"seasonNumber": 1, "monitored": true}},
		"addOptions": map[string]any{"searchForMissingEpisodes": true}}, 201, nil)
	e.drain()
	got = e.searches()
	slices.Sort(got)
	if strings.Join(got, "|") != "tv-1080p Mu s1e1|tv-2160p Mu s1e1" {
		t.Errorf("series add search %v", got)
	}
}

// A compatible add with its search option while Pause is on adds the title but searches no indexer, as the native
// add (catalog.Searchable).
func TestSearchOnAddPaused(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	e.pol.Router.Enabled = false
	e.call("POST", "/v/movie-web/radarr/api/v3/movie", map[string]any{"title": "Iota", "year": 2024, "tmdbId": 109,
		"minimumAvailability": "announced", "addOptions": map[string]any{"searchForMovie": true}}, 201, nil)
	e.drain()
	if got := e.searches(); len(got) != 0 {
		t.Errorf("searched while paused: %v", got)
	}
	if n := e.one(`SELECT COUNT(*) FROM title WHERE tmdb_id = 109`); n != 1 {
		t.Errorf("the add did not add the title: %d", n)
	}
	// The search is kept for when Pause is off: a row of the view's library on the search schedule.
	if n := e.one(`SELECT COUNT(*) FROM search_request r JOIN title t ON t.id = r.title_id WHERE t.tmdb_id = 109
		AND r.version_key = 'movie-web' AND r.why = 'added'`); n != 1 {
		t.Errorf("paused add kept %d searches", n)
	}
}
