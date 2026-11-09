package autobuild

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/offlinecache"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func count(t *testing.T, st *store.Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := st.Reader().QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func service(t *testing.T, st *store.Store, c *clock, pol *policy.Policy, sources func(context.Context) (*anime.Sources, error)) (*Service, *jobs.Queue) {
	t.Helper()
	q := jobs.NewQueue(st)
	q.SetClock(c.now)
	if pol == nil {
		pol = &policy.Policy{}
	}
	if sources == nil {
		sources = func(context.Context) (*anime.Sources, error) { t.Fatal("no sources in this test"); return nil, nil }
	}
	return New(Config{Policy: pol, Store: st, Queue: q, Logger: quiet, Now: c.now, Sources: sources}), q
}

func finishJobs(t *testing.T, st *store.Store) {
	exec(t, st, `UPDATE job SET state = 'done' WHERE kind = ?`, JobKind)
}

func TestLoopDiscoversOncePerUncoveredTitle(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	c := &clock{t: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}
	s, _ := service(t, st, c, nil, nil)
	exec(t, st, `INSERT INTO title (id, kind, name, tvdb_id, anime, created_at, updated_at) VALUES
		(1, 'series', 'Uncovered', 500, 1, 0, 0), (2, 'series', 'Covered', 600, 1, 0, 0),
		(3, 'series', 'Not anime', 900, 0, 0, 0), (4, 'series', 'No TVDB', NULL, 1, 0, 0),
		(5, 'movie', 'Anime movie', NULL, 1, 0, 0)`)
	exec(t, st, `INSERT INTO anime_franchise (slug, name, seed_anilist, built_at, created_at, updated_at) VALUES ('big', 'Big', 1, ?, 0, 0)`,
		store.Millis(c.now()))
	exec(t, st, `INSERT INTO anime_mapping (franchise, entry_key, status, tvdb_series, updated_at) VALUES ('big', 'al:1', 'auto', 600, 0)`)
	var next time.Time
	pass := func() {
		t.Helper()
		if err := s.Pass(ctx, &next); err != nil {
			t.Fatal(err)
		}
	}
	jobsFor := func(key string) int {
		return count(t, st, `SELECT count(*) FROM job WHERE dedupe_key = ?`, key)
	}
	pass()
	if jobsFor(TVDBKey(500)) != 1 || count(t, st, `SELECT count(*) FROM job`) != 1 {
		t.Fatal("no discovery job for the uncovered series, or more than one job")
	}
	if n := count(t, st, `SELECT count(*) FROM anime_discovery WHERE title_id = 1 AND outcome = 'queued'`); n != 1 {
		t.Fatal("outcome not queued")
	}
	pass() // the job is still queued: nothing new
	if count(t, st, `SELECT count(*) FROM job`) != 1 {
		t.Fatal("enqueued while a build is queued")
	}
	// the build found no AniList mapping: not tried again before the refresh interval
	finishJobs(t, st)
	if err := s.record(ctx, 1, 500, OutcomeNoMapping, "", ""); err != nil {
		t.Fatal(err)
	}
	c.add(time.Hour)
	pass()
	if jobsFor(TVDBKey(500)) != 1 {
		t.Fatal("retried before the refresh interval")
	}
	c.add(policy.DefaultAnimeRefreshInterval)
	pass()
	if jobsFor(TVDBKey(500)) != 2 {
		t.Fatal("not retried after the refresh interval")
	}
	if jobsFor(TVDBKey(600)) != 0 || jobsFor(TVDBKey(900)) != 0 {
		t.Fatal("covered or non-anime series queued")
	}
}

func TestRefreshOnlyStaleFranchisesSpread(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	c := &clock{t: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}
	s, _ := service(t, st, c, nil, nil)
	old := store.Millis(c.now().Add(-8 * 24 * time.Hour))
	exec(t, st, `INSERT INTO anime_franchise (slug, name, seed_anilist, built_at, pending_result, created_at, updated_at) VALUES
		('old', 'Old', 1, ?, NULL, 0, 0), ('older-held', 'Held', 2, ?, x'00', 0, 0), ('fresh', 'Fresh', 3, ?, NULL, 0, 0),
		('older', 'Older', 4, ?, NULL, 0, 0)`, old, old-1000, store.Millis(c.now()), old-10)
	var next time.Time
	if err := s.Pass(ctx, &next); err != nil {
		t.Fatal(err)
	}
	if n := count(t, st, `SELECT count(*) FROM job WHERE dedupe_key = ?`, SlugKey("older")); n != 1 {
		t.Fatal("the stalest franchise without a held refresh was not queued")
	}
	if !next.Equal(c.now().Add(policy.DefaultAnimeRefreshInterval / 4)) {
		t.Fatalf("next refresh %v", next)
	}
	finishJobs(t, st)
	exec(t, st, `UPDATE anime_franchise SET built_at = ? WHERE slug = 'older'`, store.Millis(c.now()))
	if err := s.Pass(ctx, &next); err != nil {
		t.Fatal(err)
	}
	if n := count(t, st, `SELECT count(*) FROM job WHERE kind = ? AND state = 'queued'`, JobKind); n != 0 {
		t.Fatal("rebuild queued before its spread slot")
	}
	c.add(policy.DefaultAnimeRefreshInterval / 4)
	if err := s.Pass(ctx, &next); err != nil {
		t.Fatal(err)
	}
	if n := count(t, st, `SELECT count(*) FROM job WHERE dedupe_key = ? AND state = 'queued'`, SlugKey("old")); n != 1 {
		t.Fatal("the next stale franchise was not queued")
	}
	if n := count(t, st, `SELECT count(*) FROM job WHERE dedupe_key IN (?, ?)`, SlugKey("fresh"), SlugKey("older-held")); n != 0 {
		t.Fatal("fresh or held franchise queued")
	}
}

func TestAutoBuildOffOrUnconfiguredRunsNothing(t *testing.T) {
	st := openStore(t)
	off := false
	s := New(Config{Policy: &policy.Policy{Metadata: &policy.Metadata{Anime: &policy.AnimeBuild{AutoBuild: &off}}},
		Store: st, Queue: jobs.NewQueue(st), Logger: quiet, Sources: func(context.Context) (*anime.Sources, error) { return nil, nil }})
	s.Run(context.Background()) // returns at once
	u := New(Config{Policy: &policy.Policy{}, Store: st, Queue: jobs.NewQueue(st), Logger: quiet, Off: "missing anilist"})
	u.Run(context.Background())
	if u.Ready() || u.Status().Off != "missing anilist" {
		t.Fatalf("%+v", u.Status())
	}
	if _, err := u.EnqueueRebuild(context.Background(), "x"); err != ErrNotConfigured {
		t.Fatal(err)
	}
	if missing := Missing(&policy.Policy{}); len(missing) != len(Required) {
		t.Fatal(missing)
	}
}

const offline = "../testdata/offline"

func TestJobBuildsThroughSaveAndHoldsShrinkingRebuild(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	c := &clock{t: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}
	pol := &policy.Policy{Metadata: &policy.Metadata{OverridesDir: t.TempDir()}}
	src, _, err := offlinecache.Sources(filepath.Join(offline, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	s, q := service(t, st, c, pol, func(context.Context) (*anime.Sources, error) { return src, nil })
	shrink := false
	s.cfg.Build = func(ctx context.Context, src *anime.Sources, f anime.Franchise, opt anime.Options) (*anime.Result, error) {
		opt.Today = "2026-11-09"
		res, err := anime.Build(ctx, src, f, opt)
		if err == nil && shrink {
			for _, e := range res.Entries {
				if e.Key == "al:21355" {
					e.TVDB, e.Mapping = nil, nil
				}
			}
		}
		return res, err
	}
	w := jobs.NewWorker(q, jobs.WorkerOptions{Logger: quiet})
	RegisterRuntimeJobs(w, func(ctx context.Context) (context.Context, *Service) { return ctx, s })
	exec(t, st, `INSERT INTO title (id, kind, name, tvdb_id, anime, created_at, updated_at) VALUES
		(7, 'series', 'Re:ZERO -Starting Life in Another World-', 305089, 1, 0, 0)`)
	if _, err := s.EnqueueDiscover(ctx, 305089); err != nil {
		t.Fatal(err)
	}
	if ran, err := w.RunOne(ctx); err != nil || !ran {
		t.Fatal(ran, err)
	}
	var slug, cfg string
	var seed int
	if err := st.Reader().QueryRow(`SELECT slug, seed_anilist, config FROM anime_franchise`).Scan(&slug, &seed, &cfg); err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	_ = json.Unmarshal([]byte(cfg), &config)
	if slug != "re-zero-starting-life-in-another-world" || seed != 21355 || config["origin"] != OriginAuto ||
		config["tvdb_id"] != float64(305089) {
		t.Fatalf("franchise %s %d %s", slug, seed, cfg)
	}
	if n := count(t, st, `SELECT count(*) FROM anime_mapping WHERE franchise = ? AND tvdb_series = 305089`, slug); n == 0 {
		t.Fatal("no mappings saved")
	}
	if n := count(t, st, `SELECT count(*) FROM anime_discovery WHERE title_id = 7 AND outcome = 'built' AND franchise = ?`, slug); n != 1 {
		t.Fatal("outcome not built")
	}
	// a rebuild that loses season 1 is held for review, keeping the applied build
	shrink = true
	if _, err := s.EnqueueRebuild(ctx, slug); err != nil {
		t.Fatal(err)
	}
	if ran, err := w.RunOne(ctx); err != nil || !ran {
		t.Fatal(ran, err)
	}
	if n := count(t, st, `SELECT count(*) FROM anime_franchise WHERE slug = ? AND pending_result IS NOT NULL`, slug); n != 1 {
		t.Fatal("shrinking rebuild not held")
	}
	if n := count(t, st, `SELECT count(*) FROM anime_review WHERE franchise = ? AND kind = 'refresh_shrink' AND state = 'open'`, slug); n != 1 {
		t.Fatal("no refresh_shrink review item")
	}
	if n := count(t, st, `SELECT count(*) FROM anime_mapping WHERE franchise = ? AND entry_key = 'al:21355'`, slug); n != 1 {
		t.Fatal("held rebuild was applied")
	}
	if n := count(t, st, `SELECT count(*) FROM job WHERE kind = ? AND state <> 'done'`, JobKind); n != 0 {
		t.Fatal("job not done")
	}
}

func TestBuildsRunOneAtATime(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	c := &clock{t: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}
	s, q := service(t, st, c, nil, nil)
	w := jobs.NewWorker(q, jobs.WorkerOptions{Logger: quiet})
	RegisterRuntimeJobs(w, func(ctx context.Context) (context.Context, *Service) { return ctx, s })
	exec(t, st, `INSERT INTO anime_franchise (slug, name, seed_anilist, created_at, updated_at) VALUES ('a', 'A', 1, 0, 0)`)
	id, err := s.EnqueueRebuild(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	building.Lock() // another build is running
	ran, err := w.RunOne(ctx)
	building.Unlock()
	if err != nil || !ran {
		t.Fatal(ran, err)
	}
	in, err := q.Get(ctx, id)
	if err != nil || in.State != jobs.StateQueued || in.Attempts != 0 || !in.RunAt.After(c.now()) {
		t.Fatalf("waiting build: %+v %v", in, err)
	}
}

func TestEnqueueSeedValidates(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	s, _ := service(t, st, &clock{t: time.Now()}, nil, func(context.Context) (*anime.Sources, error) { return nil, nil })
	exec(t, st, `INSERT INTO anime_franchise (slug, name, seed_anilist, created_at, updated_at) VALUES ('one-piece', 'One Piece', 21, 0, 0)`)
	if _, _, err := s.EnqueueSeed(ctx, 22, "one-piece", ""); err != ErrSlugTaken {
		t.Fatal(err)
	}
	if _, slug, err := s.EnqueueSeed(ctx, 21, "", "One Piece"); err != nil || slug != "one-piece" {
		t.Fatal(slug, err)
	}
	if _, _, err := s.EnqueueSeed(ctx, 30, "Bad Slug", ""); err == nil {
		t.Fatal("bad slug accepted")
	}
	if _, _, err := s.EnqueueSeed(ctx, 0, "", "x"); err == nil {
		t.Fatal("no seed accepted")
	}
	if _, slug, err := s.EnqueueSeed(ctx, 31, "", ""); err != nil || slug != "anilist-31" {
		t.Fatal(slug, err)
	}
}
