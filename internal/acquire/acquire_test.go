package acquire

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/router/routertest"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

var t0 = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func h(c string) string { return strings.Repeat(c, 40) }

type search struct {
	Title, Version string
	Season, Ep     int
	Episodes       int
	// AnyMonitored is a series search that takes unmonitored episodes too (MonitoredOnly false).
	AnyMonitored bool
}

type env struct {
	t     *testing.T
	ctx   context.Context
	st    *store.Store
	pol   *policy.Policy
	q     *jobs.Queue
	w     *jobs.Worker
	rt    *routertest.Fake
	a     *Acquirer
	mu    sync.Mutex
	now   time.Time
	calls []search
	// accept: whether the fake search returns an accepted release.
	accept bool
	// onSearch, when set, runs at the start of each fake search; a non-nil error is the search's.
	onSearch func(ctx context.Context) error
	// noAnswer makes every fake search one no indexer answered (router.Searched.NoAnswer).
	noAnswer bool
}

func (e *env) clock() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *env) advance(d time.Duration) {
	e.mu.Lock()
	e.now = e.now.Add(d)
	e.mu.Unlock()
}

// testPolicy pins router.wanted.retry_new off unless wanted sets it, so only the retry tests see follow-up chains.
func testPolicy(enabled bool, wanted *policy.Wanted) *policy.Policy {
	if wanted == nil {
		wanted = &policy.Wanted{RetryNew: new(false)}
	} else if wanted.RetryNew == nil {
		wanted.RetryNew = new(false)
	}
	v := func(mode policy.Mode, kind policy.Kind) *policy.VersionPolicy {
		return &policy.VersionPolicy{Mode: mode, Grab: true, Kind: kind, Storage: policy.StorageDebrid, Indexers: []string{"pub"},
			Providers: []string{"rd"}}
	}
	return &policy.Policy{
		Indexers: map[string]*policy.Indexer{"pub": {Kind: "torznab", URL: "http://pub.test/api",
			Privacy: policy.PrivacyPublic}},
		Providers: map[string]*policy.Provider{"rd": {Kind: provider.KindRealDebrid, Order: 1,
			URL: "http://rd.test/rest/1.0", APIKeyEnv: "THEATARR_RD_KEY", Pacing: policy.Pacing{AddsPerMinute: 100,
				MinGap: new(policy.Duration)}}}, // adds back to back on a frozen clock: no min_gap (tested in internal/router)
		Versions: map[string]*policy.VersionPolicy{
			"movie":         v(policy.ModeActive, policy.KindMovie),
			"movie-shadow":  v(policy.ModeShadow, policy.KindMovie),
			"series":        v(policy.ModeActive, policy.KindSeries),
			"series-shadow": v(policy.ModeShadow, policy.KindSeries),
		},
		Router: &policy.Router{Enabled: enabled, Wanted: wanted},
	}
}

// newEnv: movies 1-3 (1 and 2 wanted, 3 filled below cutoff), each with an active and a shadow version; series 10
// with seasons 1 (three aired episodes, one filled) and 2 (one aired, one not yet), and a special.
func newEnv(t *testing.T, enabled bool, wanted *policy.Wanted) *env {
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
	e := &env{t: t, ctx: ctx, st: st, pol: testPolicy(enabled, wanted), now: t0, accept: true}
	aired := store.Millis(t0.Add(-24 * time.Hour))
	future := store.Millis(t0.Add(7 * 24 * time.Hour))
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, year, created_at, updated_at) VALUES (1, 'movie', 'One', 2024, 0, 0)`,
		`INSERT INTO title (id, kind, name, year, created_at, updated_at) VALUES (2, 'movie', 'Two', 2024, 0, 0)`,
		`INSERT INTO title (id, kind, name, year, created_at, updated_at) VALUES (3, 'movie', 'Three', 2024, 0, 0)`,
		`INSERT INTO title (id, kind, name, year, tvdb_id, created_at, updated_at) VALUES (10, 'series', 'Show', 2020, 99, 0, 0)`,
		`INSERT INTO title_release_date (title_id, source, type, date) VALUES
			(1, 'fixture', 'digital', '2024-01-01'), (2, 'fixture', 'digital', '2024-01-01'),
			(3, 'fixture', 'digital', '2024-01-01')`,
		`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (101, 1, 'movie', 'wanted', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (102, 2, 'movie', 'wanted', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (103, 3, 'movie', 'filled', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (111, 1, 'movie-shadow', 'wanted', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (112, 2, 'movie-shadow', 'wanted', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (113, 3, 'movie-shadow', 'filled', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (201, 10, 'series', 'wanted', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (211, 10, 'series-shadow', 'wanted', 0, 0)`,
		`INSERT INTO arrview_command (id, view, name, status, queued_at) VALUES (1, 'radarr', 'MoviesSearch', 'queued', 0)`,
	} {
		if _, err := st.Writer().ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	for _, ep := range []struct {
		id             int64
		season, number int
		air            int64
		state          string
	}{{1001, 1, 1, aired, "wanted"}, {1002, 1, 2, aired, "filled"}, {1003, 1, 3, aired, "wanted"},
		{1004, 2, 1, aired, "wanted"}, {1005, 2, 2, future, "wanted"}, {1000, 0, 1, aired, "wanted"}} {
		if _, err := st.Writer().ExecContext(ctx, `INSERT INTO episode (id, title_id, season, number, air_date_utc_at,
			created_at, updated_at) VALUES (?, 10, ?, ?, ?, 0, 0)`, ep.id, ep.season, ep.number, ep.air); err != nil {
			t.Fatal(err)
		}
		for _, v := range []int64{201, 211} {
			if _, err := st.Writer().ExecContext(ctx, `INSERT INTO episode_version_state (version_id, episode_id, state,
				updated_at) VALUES (?, ?, ?, 0)`, v, ep.id, ep.state); err != nil {
				t.Fatal(err)
			}
		}
	}
	e.q = jobs.NewQueue(st)
	e.q.SetClock(e.clock)
	e.w = jobs.NewWorker(e.q, jobs.WorkerOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if e.rt, err = routertest.New(st, e.pol, e.q, e.w, e.clock); err != nil {
		t.Fatal(err)
	}
	e.a, err = New(Config{Store: st, Policy: e.pol, Queue: e.q, Router: e.rt, Search: e.search,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	e.a.RegisterJobs(e.w)
	return e
}

// search is the fake indexer search: one public release per call, accepted when e.accept.
func (e *env) search(ctx context.Context, t decide.Title, v decide.Version, _ *decide.ExistingFile,
	ss *decide.SeriesSearch) (*router.Searched, error) {
	e.mu.Lock()
	n := len(e.calls)
	s := search{Title: t.Name, Version: v.Key}
	if ss != nil {
		s.Season, s.Ep, s.Episodes, s.AnyMonitored = ss.Season, ss.Episode, len(ss.Episodes), !ss.MonitoredOnly
	}
	e.calls = append(e.calls, s)
	accept, hook, noAnswer := e.accept, e.onSearch, e.noAnswer
	e.mu.Unlock()
	if hook != nil {
		if err := hook(ctx); err != nil {
			return nil, err
		}
	}
	out := &router.Searched{Ranked: []*decide.Decision{{}}}
	out.Request = router.Request{TitleID: t.ID, VersionID: v.ID, Version: v.Key}
	if noAnswer {
		return &router.Searched{NoAnswer: true, Notes: []string{"pub: connection refused"},
			Request: out.Request}, nil
	}
	if accept {
		out.Request.Candidates = []router.Candidate{{Release: indexer.Release{Indexer: "pub", Kind: "torznab",
			Title: t.Name + ".2024.2160p.UHD.BluRay.REMUX.HEVC-GRP", InfoHash: h(string(rune('a' + n%6))), Privacy: policy.PrivacyPublic,
			Size: 10 << 30}, Quality: "WEBDL-2160p", Score: 100}}
	}
	return out, nil
}

// drain runs every ready job.
func (e *env) drain() {
	e.t.Helper()
	for range 200 {
		ran, err := e.w.RunOne(e.ctx)
		if err != nil {
			e.t.Fatal(err)
		}
		if !ran {
			return
		}
	}
	e.t.Fatal("jobs never drained")
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.st.Reader().QueryRowContext(e.ctx, q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) searches() []search {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]search(nil), e.calls...)
}

func TestWantedNeverSearchedNewestFirst(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 1})
	if _, err := e.st.Writer().ExecContext(e.ctx, `UPDATE version SET created_at = CASE id WHEN 101 THEN ? ELSE 0 END WHERE id IN (101, 102)`,
		store.Millis(t0)); err != nil {
		t.Fatal(err)
	}
	sum, err := e.a.Wanted(e.ctx)
	if err != nil || sum.Queued != 1 {
		t.Fatalf("wanted: %+v %v", sum, err)
	}
	var title int64
	if err := e.st.Reader().QueryRow(`SELECT title_id FROM acquire_run WHERE source = 'wanted' ORDER BY id DESC LIMIT 1`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != 1 {
		t.Fatalf("newest title %d, want 1", title)
	}
}

// A search command over an active and a shadow version searches the active one only, and its route adds; the
// command's status follows the run and the router's download.
func TestCommandSearchesActiveVersionsOnly(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	queued, found, err := e.a.Submit(e.ctx, 1, Selection{Versions: []string{"movie", "movie-shadow"}, TitleIDs: []int64{1}})
	if err != nil || queued != 1 || found != 1 {
		t.Fatalf("submit: %d of %d, %v", queued, found, err)
	}
	st, err := LoadCommandStatus(e.ctx, e.st.Reader(), 1)
	if err != nil || st.Status != "queued" {
		t.Fatalf("before the jobs: %+v %v", st, err)
	}
	e.drain()
	if got := e.searches(); len(got) != 1 || got[0].Version != "movie" {
		t.Fatalf("searches %+v", got)
	}
	if n := len(e.rt.Adders["rd"].Adds()); n != 1 {
		t.Fatalf("adds %d, want 1", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE source = ? AND version_key = 'movie' AND state = 'added'`,
		Source(1)); n != 1 {
		t.Fatalf("downloads %d", n)
	}
	st, err = LoadCommandStatus(e.ctx, e.st.Reader(), 1)
	if err != nil || st.Status != "completed" || !strings.Contains(st.Message, "added One.2024.2160p.UHD.BluRay.REMUX.HEVC-GRP on rd") ||
		st.Ended == nil {
		t.Fatalf("after: %+v %v", st, err)
	}
}

func TestSearchJobCapturesRuntimeWhenItStarts(t *testing.T) {
	e := newEnv(t, true, nil)
	generation := int64(7)
	e.a.runtimeCapture = func(ctx context.Context) context.Context { return settings.WithGeneration(ctx, generation) }
	original := e.a.search
	entered, release, observed := make(chan struct{}), make(chan struct{}), make(chan int64, 1)
	e.a.search = func(ctx context.Context, title decide.Title, version decide.Version, existing *decide.ExistingFile,
		series *decide.SeriesSearch) (*router.Searched, error) {
		close(entered)
		<-release
		observed <- RuntimeGeneration(ctx)
		return original(ctx, title, version, existing, series)
	}
	if _, _, err := e.a.Submit(e.ctx, 1, Selection{Versions: []string{"movie"}, TitleIDs: []int64{1}}); err != nil {
		t.Fatal(err)
	}
	var payload string
	if err := e.st.Reader().QueryRowContext(e.ctx, `SELECT payload FROM job WHERE kind = ?`, JobSearch).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, `"generation"`) {
		t.Fatalf("search job persisted a generation: %s", payload)
	}
	generation = 8
	done := make(chan error, 1)
	go func() {
		_, err := e.w.RunOne(e.ctx)
		done <- err
	}()
	<-entered
	generation = 9
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := <-observed; got != 8 {
		t.Fatalf("in-flight search used generation %d after swap, want start generation 8", got)
	}
	var routePayload string
	if err := e.st.Reader().QueryRowContext(e.ctx, `SELECT payload FROM job WHERE kind = ?`, router.JobRoute).Scan(&routePayload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(routePayload, `"settings_generation"`) {
		t.Fatalf("route job persisted a settings generation: %s", routePayload)
	}
}

func TestCommandFanoutSearchesTitleOnceAndRecordsServedVersions(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	e.pol.Versions["movie-copy"] = &policy.VersionPolicy{Mode: policy.ModeActive, Grab: true, Kind: policy.KindMovie,
		Indexers: []string{"pub"}, Providers: []string{"rd"}}
	if _, err := e.st.Writer().ExecContext(e.ctx, `INSERT INTO version (id, title_id, policy_key, state,
		created_at, updated_at) VALUES (114, 1, 'movie-copy', 'wanted', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	calls := 0
	e.a.searchBatch = func(_ context.Context, _ decide.Title, targets []router.SearchTarget) (map[string]*router.Searched, error) {
		calls++
		out := map[string]*router.Searched{}
		for _, target := range targets {
			out[target.ID] = &router.Searched{Request: router.Request{TitleID: 1, VersionID: target.Version.ID,
				Version: target.Version.Key}}
		}
		return out, nil
	}
	queued, found, err := e.a.Submit(e.ctx, 1, Selection{Versions: []string{"movie"}, TitleIDs: []int64{1}})
	if err != nil || queued != 1 || found != 1 {
		t.Fatalf("submit: queued=%d found=%d err=%v", queued, found, err)
	}
	e.drain()
	if calls != 1 {
		t.Fatalf("shared search calls=%d, want 1", calls)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE command_id=1 AND title_id=1`); n != 2 {
		t.Fatalf("served version rows=%d, want 2", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE command_id=1 AND title_id=1 AND search_run IS NULL`); n != 1 {
		t.Fatalf("search roots=%d, want 1", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE command_id=1 AND title_id=1 AND search_run IS NOT NULL
		AND state='nothing'`); n != 1 {
		t.Fatalf("shared target outcome rows=%d, want 1", n)
	}
}

// A command that names no titles (MissingMoviesSearch, CutoffUnmetEpisodeSearch) searches only the versions it
// asked for, as that arr would: never another active version of the same kind, nor the other kind's. One that names
// titles still serves every active version of them (above).
func TestBulkCommandSearchesOnlyItsVersions(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	e.pol.Versions["movie-copy"] = &policy.VersionPolicy{Mode: policy.ModeActive, Grab: true, Kind: policy.KindMovie,
		Indexers: []string{"pub"}, Providers: []string{"rd"}}
	if _, err := e.st.Writer().ExecContext(e.ctx, `INSERT INTO version (id, title_id, policy_key, state,
		created_at, updated_at) VALUES (114, 1, 'movie-copy', 'wanted', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	runs := func(cmd int64) string {
		t.Helper()
		rows, err := e.st.Reader().QueryContext(e.ctx, `SELECT version_key || ':' || target FROM acquire_run
			WHERE command_id = ? ORDER BY 1`, cmd)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return strings.Join(out, " ")
	}
	for i, tc := range []struct {
		sel  Selection
		want string
	}{
		{Selection{Versions: []string{"movie", "movie-shadow"}, Only: OnlyMissing}, "movie:movie movie:movie"},
		{Selection{Versions: []string{"series", "series-shadow"}, Only: OnlyMissing}, "series:s00e01 series:s01 series:s02e01"},
		{Selection{Versions: []string{"movie"}, Only: OnlyCutoff}, "movie:movie"},
		{Selection{Versions: []string{"series"}, Only: OnlyCutoff}, "series:s01e02"},
		{Selection{Versions: []string{"movie"}, TitleIDs: []int64{1}}, "movie-copy:movie movie:movie"},
	} {
		cmd := int64(i + 10)
		if _, err := e.st.Writer().ExecContext(e.ctx, `INSERT INTO arrview_command (id, view, name, status, queued_at)
			VALUES (?, 'radarr', 'MissingMoviesSearch', 'queued', 0)`, cmd); err != nil {
			t.Fatal(err)
		}
		if _, _, err := e.a.Submit(e.ctx, cmd, tc.sel); err != nil {
			t.Fatal(err)
		}
		if got := runs(cmd); got != tc.want {
			t.Errorf("%+v: runs %s, want %s", tc.sel, got, tc.want)
		}
	}
}

func TestInteractiveFanoutUsesOneBatch(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	e.pol.Versions["movie-copy"] = &policy.VersionPolicy{Mode: policy.ModeActive, Grab: true, Kind: policy.KindMovie,
		Indexers: []string{"pub"}, Providers: []string{"rd"}}
	if _, err := e.st.Writer().ExecContext(e.ctx, `INSERT INTO version (id, title_id, policy_key, state,
		created_at, updated_at) VALUES (114, 1, 'movie-copy', 'wanted', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	calls := 0
	e.a.searchBatch = func(_ context.Context, _ decide.Title, targets []router.SearchTarget) (map[string]*router.Searched, error) {
		calls++
		out := map[string]*router.Searched{}
		for _, target := range targets {
			out[target.ID] = &router.Searched{Request: router.Request{TitleID: 1, VersionID: target.Version.ID,
				Version: target.Version.Key}}
		}
		return out, nil
	}
	got, err := e.a.InteractiveMany(e.ctx, 1, []string{"movie", "movie-copy"}, nil, nil)
	if err != nil || calls != 1 || len(got) != 2 {
		t.Fatalf("interactive batch=%v calls=%d err=%v", got, calls, err)
	}
}

// An interactive (or Wanted row) search of a season names no episodes: it requests every episode of that season,
// Specials (season 0) included, so a release for one of them is not rejected as unrequested.
func TestInteractiveSeasonRequestsItsEpisodes(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	var got *decide.SeriesSearch
	e.a.searchBatch = func(_ context.Context, _ decide.Title, targets []router.SearchTarget) (map[string]*router.Searched, error) {
		got = targets[0].Series
		return map[string]*router.Searched{targets[0].ID: {}}, nil
	}
	for _, c := range []struct {
		season int
		want   []int64
	}{{0, []int64{1000}}, {1, []int64{1001, 1002, 1003}}} {
		season := c.season
		if _, err := e.a.InteractiveMany(e.ctx, 10, []string{"series"}, &season, nil); err != nil {
			t.Fatal(err)
		}
		if got == nil || got.Season != c.season || got.Episode != 0 || !slices.Equal(got.Episodes, c.want) {
			t.Errorf("season %d: search %+v, want season %d with episodes %v", c.season, got, c.season, c.want)
		}
	}
}

// Shadow versions are never searched and never written for: not by Submit, not by a run left from when the
// version was active, not by the wanted loop.
func TestShadowVersionsNeverSearchedOrWritten(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 200})
	queued, found, err := e.a.Submit(e.ctx, 1, Selection{Versions: []string{"movie-shadow", "series-shadow"},
		TitleIDs: []int64{1, 2, 3, 10}})
	if err != nil || queued != 0 || found != 0 {
		t.Fatalf("submit: %d of %d, %v", queued, found, err)
	}
	if _, _, err := e.a.Submit(e.ctx, 1, Selection{Versions: []string{"movie-shadow"}, Only: OnlyMissing}); err != nil {
		t.Fatal(err)
	}
	// A run recorded for a version that is in shadow mode now.
	if _, err := e.st.Writer().ExecContext(e.ctx, `INSERT INTO acquire_run (id, source, title_id, version_id,
		version_key, target, state, created_at, updated_at) VALUES (50, 'command', 1, 111, 'movie-shadow', 'movie',
		'queued', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if err := e.a.Run1(e.ctx, 50); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE id = 50 AND state = 'skipped'`); n != 1 {
		t.Error("the shadow run was not skipped")
	}
	sum, err := e.a.Wanted(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	e.drain()
	for _, s := range e.searches() {
		if strings.HasSuffix(s.Version, "-shadow") {
			t.Errorf("searched a shadow version: %+v", s)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE version_key LIKE '%-shadow' AND id != 50`); n != 0 {
		t.Errorf("%d runs for shadow versions", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE version_key LIKE '%-shadow'`); n != 0 {
		t.Errorf("%d downloads for shadow versions", n)
	}
	for _, a := range e.rt.Adders {
		for _, hash := range a.Adds() {
			var key string
			_ = e.st.Reader().QueryRow(`SELECT version_key FROM download WHERE info_hash = ?`, hash).Scan(&key)
			if strings.HasSuffix(key, "-shadow") || key == "" {
				t.Errorf("add %s for %q", hash, key)
			}
		}
	}
	if sum.Queued == 0 {
		t.Errorf("the loop should still search the active versions: %+v", sum)
	}
}

// The kill switch: an active version's search routes, but the router only records a dry-run plan; the command says
// what it would have added.
func TestKillSwitchDryRun(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false, nil)
	if _, _, err := e.a.Submit(e.ctx, 1, Selection{Versions: []string{"movie"}, TitleIDs: []int64{2}}); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if e.rt.Writes() != 0 {
		t.Fatalf("writes %d with router.enabled false", e.rt.Writes())
	}
	if n := e.count(`SELECT COUNT(*) FROM router_plan WHERE mode = 'disabled' AND source = ?`, Source(1)); n != 1 {
		t.Fatalf("plans %d", n)
	}
	st, err := LoadCommandStatus(e.ctx, e.st.Reader(), 1)
	if err != nil || st.Status != "completed" || !strings.Contains(st.Message, "dry run (disabled): would add Two") {
		t.Fatalf("status %+v %v", st, err)
	}
	// The wanted loop does not search at all while the switch is off.
	sum, err := e.a.Wanted(e.ctx)
	if err != nil || sum.Off != "router.enabled is false" || sum.Queued != 0 {
		t.Fatalf("wanted %+v %v", sum, err)
	}
}

func TestSeriesTargets(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	one := 1
	for _, tc := range []struct {
		name string
		sel  Selection
		want string
	}{
		{"SeriesSearch", Selection{TitleIDs: []int64{10}}, "s00:1 s01:3 s02:2"},
		{"SeasonSearch", Selection{TitleIDs: []int64{10}, Season: &one}, "s01:3"},
		{"EpisodeSearch", Selection{EpisodeIDs: []int64{1002, 1000}}, "s00e01:1 s01e02:1"},
		{"MissingEpisodeSearch", Selection{TitleIDs: []int64{10}, Only: OnlyMissing}, "s00e01:1 s01:2 s02e01:1"},
		{"CutoffUnmetEpisodeSearch", Selection{Only: OnlyCutoff}, "s01e02:1"},
	} {
		tc.sel.Versions = []string{"series", "series-shadow"}
		ts, err := e.a.targets(e.ctx, tc.sel)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, x := range ts {
			if x.key != "series" {
				t.Errorf("%s: target for %s", tc.name, x.key)
			}
			got = append(got, x.name+":"+string(rune('0'+len(x.episodes))))
		}
		if strings.Join(got, " ") != tc.want {
			t.Errorf("%s: %v, want %s", tc.name, got, tc.want)
		}
	}
	// A season search asks the indexers for the season, an episode search for the episode.
	if _, _, err := e.a.Submit(e.ctx, 1, Selection{Versions: []string{"series"}, TitleIDs: []int64{10}, Only: OnlyMissing}); err != nil {
		t.Fatal(err)
	}
	e.drain()
	got := e.searches()
	if len(got) != 3 || got[0].Season != 0 || got[0].Ep != 1 || got[0].Episodes != 1 || got[1].Season != 1 ||
		got[1].Ep != 0 || got[1].Episodes != 2 || got[2].Season != 2 || got[2].Ep != 1 || got[2].Episodes != 1 {
		t.Errorf("searches %+v", got)
	}
}

// The wanted loop: missing and cutoff-unmet items of the active versions, at most per_hour in any hour, again only
// after again_after, never an item with a download in flight.
func TestWantedLoopPacing(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 3, AgainAfter: policy.Duration(6 * time.Hour)})
	e.accept = false
	// Movie 2 has a download in flight.
	if _, err := e.st.Writer().ExecContext(e.ctx, `INSERT INTO download (title_id, version_id, version_key, info_hash,
		release_title, indexer, state, created_at, updated_at) VALUES (2, 102, 'movie', ?, 'x', 'pub', 'downloading', 0, 0)`,
		h("f")); err != nil {
		t.Fatal(err)
	}
	// Due: movie 1 and 2 (missing), movie 3 (cutoff), series specials, s01 and s02 (missing), s01e02 (cutoff).
	sum, err := e.a.Wanted(e.ctx)
	if err != nil || sum.Due != 6 || sum.Queued != 3 || sum.InFlight != 1 {
		t.Fatalf("first round %+v %v", sum, err)
	}
	e.drain()
	e.advance(20 * time.Minute)
	if sum, _ = e.a.Wanted(e.ctx); sum.Queued != 0 || sum.SearchedLastHour != 3 {
		t.Fatalf("same hour %+v", sum)
	}
	e.advance(time.Hour)
	if sum, _ = e.a.Wanted(e.ctx); sum.Queued != 2 || sum.Due != 3 || sum.InFlight != 1 {
		t.Fatalf("next hour %+v", sum)
	}
	e.drain()
	e.advance(time.Hour)
	if sum, _ = e.a.Wanted(e.ctx); sum.Queued != 0 || sum.Due != 1 {
		t.Fatalf("all searched within again_after %+v", sum)
	}
	e.advance(6 * time.Hour)
	if sum, _ = e.a.Wanted(e.ctx); sum.Queued != 3 {
		t.Fatalf("after again_after %+v", sum)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE version_id = 102`); n != 0 {
		t.Errorf("searched a movie with a download in flight %d times", n)
	}
	if e.rt.Writes() != 0 {
		t.Errorf("writes %d with nothing accepted", e.rt.Writes())
	}
	off := newEnv(t, true, &policy.Wanted{Enabled: new(bool)})
	if sum, _ := off.a.Wanted(off.ctx); sum.Off != "router.wanted is off" {
		t.Errorf("off: %+v", sum)
	}
}

// Sonarr v5 (b94cf9b3b, SeriesSearchServiceFixture.should_only_search_missing_if_profile_does_not_allow_upgrades,
// ReleaseSearchServiceFixture.scene_seasonsearch_should_skip_search_if_no_episodes_after_filtering): with upgrades
// off, a whole-series search takes only the episodes without a file and skips a season whose episodes all have one.
// Other searches are unchanged.
func TestSeriesSearchSkipsFilledSeasonsWithoutUpgrades(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	no := false
	e.pol.Profiles = map[string]*policy.Profile{"noup": {Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}},
		Cutoff: "WEBDL-2160p", Upgrades: &no}}
	targets := func(sel Selection) string {
		t.Helper()
		sel.Versions = []string{"series"}
		ts, err := e.a.targets(e.ctx, sel)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, x := range ts {
			got = append(got, x.name+":"+strconv.Itoa(len(x.episodes)))
		}
		return strings.Join(got, " ")
	}
	one := 1
	series := Selection{TitleIDs: []int64{10}}
	// Upgrades on (no profile named: the default): every monitored episode.
	if got := targets(series); got != "s00:1 s01:3 s02:2" {
		t.Errorf("upgrades on: %s", got)
	}
	e.pol.Versions["series"].Profile = "noup"
	// s01 has one file: searched for its two missing episodes.
	if got := targets(series); got != "s00:1 s01:2 s02:2" {
		t.Errorf("upgrades off: %s", got)
	}
	// Every s01 episode has a file: s01 is skipped; the unaired s02e02 is still missing.
	if _, err := e.st.Writer().ExecContext(e.ctx, `UPDATE episode_version_state SET state = 'cutoff_met' WHERE version_id = 201 AND episode_id IN (1001, 1003)`); err != nil {
		t.Fatal(err)
	}
	if got := targets(series); got != "s00:1 s02:2" {
		t.Errorf("upgrades off, s01 filled: %s", got)
	}
	// A season search (SeasonSearchCommand) is not changed by v5.
	if got := targets(Selection{TitleIDs: []int64{10}, Season: &one}); got != "s01:3" {
		t.Errorf("season search: %s", got)
	}
	// The engine's profile decides when there is one (from_arr profiles have no policy upgrades).
	eng, err := decide.New(e.pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.a.eng = eng
	if e.a.upgradesAllowed("series") {
		t.Error("engine profile: upgrades should be off")
	}
	yes := true
	e.pol.Profiles["noup"].Upgrades = &yes
	if eng, err = decide.New(e.pol, decide.Options{}); err != nil {
		t.Fatal(err)
	}
	e.a.eng = eng
	if got := targets(series); got != "s00:1 s01:3 s02:2" {
		t.Errorf("engine profile with upgrades: %s", got)
	}
}

// versions[*].upgrades: off never replaces a file, so cutoff-unmet searches (the wanted loop's and the commands')
// skip the version; missing searches are unchanged.
func TestUpgradesOffSkipsCutoffSearch(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	e.pol.Versions["movie"].Upgrades = policy.UpgradesOff
	e.pol.Versions["series"].Upgrades = policy.UpgradesOff
	for _, only := range []string{OnlyCutoff, OnlyMissing} {
		ts, err := e.a.targets(e.ctx, Selection{Versions: []string{"movie", "series"}, Only: only})
		if err != nil {
			t.Fatal(err)
		}
		if only == OnlyCutoff && len(ts) != 0 || only == OnlyMissing && len(ts) == 0 {
			t.Errorf("%s: %d targets", only, len(ts))
		}
	}
}

func TestSeasonPartiallyInFlightStillSearches(t *testing.T) {
	e := newEnv(t, true, nil)
	if _, err := e.st.Writer().Exec(`INSERT INTO download (title_id, version_id, version_key, info_hash,
		release_title, indexer, episodes, state, created_at, updated_at)
		VALUES (10, 201, 'series', ?, 'Show.S01E01', 'pub', '[1001]', 'added', 0, 0)`, h("a")); err != nil {
		t.Fatal(err)
	}
	if busy, err := e.a.inFlight(e.ctx, target{titleID: 10, versionID: 201, episodes: []int64{1001, 1003}}); err != nil || busy {
		t.Fatalf("partial season busy=%v err=%v", busy, err)
	}
	if busy, err := e.a.inFlight(e.ctx, target{titleID: 10, versionID: 201, episodes: []int64{1001}}); err != nil || !busy {
		t.Fatalf("covered episode busy=%v err=%v", busy, err)
	}
	queued, _, err := e.a.Submit(e.ctx, 1, Selection{Versions: []string{"series"}, TitleIDs: []int64{10}, Only: OnlyMissing})
	if err != nil || queued != 3 {
		t.Fatalf("submit queued=%d err=%v", queued, err)
	}
	e.drain()
	got := e.searches()
	if len(got) != 3 || got[0].Season != 0 || got[1].Season != 1 || got[1].Ep != 0 || got[1].Episodes != 2 {
		t.Fatalf("searches %+v", got)
	}
}

func TestSeasonSearchRoutesDisjointEpisodes(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	e.a.search = func(ctx context.Context, title decide.Title, version decide.Version, existing *decide.ExistingFile,
		ss *decide.SeriesSearch) (*router.Searched, error) {
		out, err := e.search(ctx, title, version, existing, ss)
		if err != nil {
			return nil, err
		}
		base := out.Request.Candidates[0]
		out.Request.Candidates = nil
		for i, id := range []int64{1001, 1002, 1003} {
			c := base
			c.Release.InfoHash = h(string(rune('a' + i)))
			c.Episodes = []int64{id}
			out.Request.Candidates = append(out.Request.Candidates, c)
		}
		return out, nil
	}
	season := 1
	queued, _, err := e.a.Submit(e.ctx, 1, Selection{Versions: []string{"series"}, TitleIDs: []int64{10}, Season: &season})
	if err != nil || queued != 1 {
		t.Fatalf("submit queued=%d err=%v", queued, err)
	}
	e.drain()
	if got := e.searches(); len(got) != 1 || got[0].Season != 1 || got[0].Ep != 0 {
		t.Fatalf("searches %+v", got)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE version_key='series' AND state='added'`); n != 3 {
		t.Fatalf("season placements=%d, want 3", n)
	}
}

// TestTitleSearchFanoutSkipsUnmonitoredAndRemovedVersions: a search that names titles also serves the
// titles' other active versions, but only where the title (or episode) is monitored there and the version was not
// removed. The named version keeps the user-invoked semantics (unmonitored too); a version that joined is recorded
// with requested = 0, so its route keeps the router's freshness check.
func TestTitleSearchFanoutSkipsUnmonitoredAndRemovedVersions(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	for _, k := range []string{"movie-web", "movie-gone", "movie-copy"} {
		e.pol.Versions[k] = &policy.VersionPolicy{Mode: policy.ModeActive, Grab: true, Kind: policy.KindMovie,
			Indexers: []string{"pub"}, Providers: []string{"rd"}}
	}
	e.pol.Versions["series-shadow"].Mode, e.pol.Versions["series-shadow"].Grab = policy.ModeActive, true
	e.exec(`UPDATE version SET monitored = 0 WHERE id = 101`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, state, monitored, created_at, updated_at) VALUES
		(121, 1, 'movie-web', 'wanted', 0, 0, 0), (122, 1, 'movie-gone', 'wanted', 1, 0, 0),
		(123, 1, 'movie-copy', 'wanted', 1, 0, 0)`)
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (122, 'radarr', 0)`)
	e.exec(`UPDATE episode_version_state SET monitored = 0 WHERE version_id = 211 AND episode_id = 1001`)
	if _, _, err := e.a.Submit(e.ctx, 1, Selection{Versions: []string{"movie"}, TitleIDs: []int64{1}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.a.Submit(e.ctx, 1, Selection{Versions: []string{"series"}, TitleIDs: []int64{10},
		EpisodeIDs: []int64{1001}}); err != nil {
		t.Fatal(err)
	}
	rows, err := e.st.Reader().QueryContext(e.ctx, `SELECT version_key || ':' || target || ':' || requested
		FROM acquire_run ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		got = append(got, s)
	}
	if want := "movie-copy:movie:0 movie:movie:1 series:s01e01:1"; strings.Join(got, " ") != want {
		t.Fatalf("runs %s, want %s", strings.Join(got, " "), want)
	}
}
