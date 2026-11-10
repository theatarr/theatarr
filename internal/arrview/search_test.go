package arrview

import (
	"context"
	"io"
	"log/slog"
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
	e.pol.Versions["movie-web"].Mode, e.pol.Versions["movie-web"].Grab = policy.ModeActive, true
	e.pol.Versions["tv-2160p"].Mode, e.pol.Versions["tv-2160p"].Grab = policy.ModeActive, true
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
// router (queued, then completed with what was added). The shadow version of the same title is not searched.
func TestSearchCommandActiveVersion(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	e.svc.captureRuntime = func(ctx context.Context) context.Context { return settings.WithGeneration(ctx, 23) }
	var c map[string]any
	e.call("POST", "/radarr/api/v3/command", map[string]any{"name": "MoviesSearch", "movieIds": []int{IDBase + 1}},
		201, &c)
	if c["status"] != "queued" || c["message"] != "Searching 1 items for movie-web; Watch only, nothing searched for movie-remux" {
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
	e.call("POST", "/sonarr/api/v3/command", map[string]any{"name": "SeasonSearch", "seriesId": 3, "seasonNumber": 1},
		201, &c)
	e.call("POST", "/sonarr/api/v3/command", map[string]any{"name": "EpisodeSearch", "episodeIds": []int{102}}, 201, &c)
	e.drain()
	e.call("POST", "/sonarr/api/v3/command", map[string]any{"name": "MissingEpisodeSearch", "seriesId": 3}, 201, &c)
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
	// No download for a shadow version, and every add is for one of ours.
	if n := e.one(`SELECT COUNT(*) FROM download WHERE version_key NOT IN ('movie-web', 'tv-2160p')`); n != 0 {
		t.Errorf("%d downloads for shadow versions", n)
	}
	// Alpha, the season, the episode (searched before the season's route ran) and Iota.
	// Three adds: the episode search finds S01E02 covered by the season search's pack, which is in flight.
	if n := int64(len(e.rt.Adders["rd"].Adds())); n != e.one(`SELECT COUNT(*) FROM download`) || n != 3 {
		t.Errorf("adds %d", n)
	}
}

// Shadow versions through every command and the add option: no search, no route, no write. The message stays the
// shadow one.
func TestSearchCommandsShadowNeverWrite(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	var c map[string]any
	for _, tc := range []struct {
		path string
		body map[string]any
	}{
		{"/v/movie-remux/radarr/api/v3/command", map[string]any{"name": "MoviesSearch", "movieIds": []int{IDBase + 1}}},
		{"/v/movie-remux/radarr/api/v3/command", map[string]any{"name": "MissingMoviesSearch"}},
		{"/v/movie-remux/radarr/api/v3/command", map[string]any{"name": "CutOffUnmetMoviesSearch"}},
		{"/v/anime-tv/sonarr/api/v3/command", map[string]any{"name": "MissingEpisodeSearch"}},
		{"/v/anime-movie/radarr/api/v3/command", map[string]any{"name": "MoviesSearch", "movieIds": []int{IDBase + 3}}},
	} {
		e.call("POST", tc.path, tc.body, 201, &c)
		if c["status"] != "completed" || !strings.HasPrefix(c["message"].(string), "Watch only: nothing searched") {
			t.Errorf("%s %v: %v %v", tc.path, tc.body["name"], c["status"], c["message"])
		}
	}
	e.call("POST", "/v/movie-remux/radarr/api/v3/movie", map[string]any{"title": "Kappa", "year": 2024, "tmdbId": 110,
		"addOptions": map[string]any{"searchForMovie": true}}, 201, nil)
	e.drain()
	if got := e.searches(); len(got) != 0 {
		t.Errorf("searches %v", got)
	}
	if n := e.one(`SELECT COUNT(*) FROM acquire_run`); n != 0 {
		t.Errorf("%d runs", n)
	}
	if e.rt.Writes() != 0 {
		t.Errorf("%d provider writes for shadow versions", e.rt.Writes())
	}
}
