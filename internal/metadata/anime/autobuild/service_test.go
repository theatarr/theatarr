package autobuild

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// seedService is a builder whose sources are the invented lists and whose shipped list is franchises.yaml.
func seedService(t *testing.T, st *store.Store, shippedYAML string) (*Service, *jobs.Queue, *jobs.Worker) {
	t.Helper()
	dir := t.TempDir()
	if shippedYAML != "" {
		if err := os.WriteFile(filepath.Join(dir, "franchises.yaml"), []byte(shippedYAML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pol := &policy.Policy{Metadata: &policy.Metadata{OverridesDir: dir}}
	lists := testLists(t)
	s, q := service(t, st, &clock{t: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}, pol,
		func(context.Context) (*anime.Sources, error) {
			return &anime.Sources{Lists: lists, AniList: &fakeAniList{}}, nil
		})
	w := jobs.NewWorker(q, jobs.WorkerOptions{Logger: quiet})
	RegisterRuntimeJobs(w, func(ctx context.Context) (context.Context, *Service) { return ctx, s })
	return s, q, w
}

// fakeBuild crawls a franchise to its seed plus the ids in more[seed].
func fakeBuild(more map[int][]int) func(context.Context, *anime.Sources, anime.Franchise, anime.Options) (*anime.Result, error) {
	return func(_ context.Context, _ *anime.Sources, f anime.Franchise, _ anime.Options) (*anime.Result, error) {
		res := &anime.Result{Franchise: f.Name, Slug: f.Slug, SeedAniList: f.SeedAniList, StoryOrder: &anime.StoryOrder{}}
		for _, id := range append([]int{f.SeedAniList}, more[f.SeedAniList]...) {
			id := id
			res.Entries = append(res.Entries, &anime.Entry{Key: "al:" + strconv.Itoa(id), Origin: "anilist", AniListID: &id})
		}
		return res, nil
	}
}

func franchises(t *testing.T, st *store.Store) string {
	t.Helper()
	var out string
	if err := st.Reader().QueryRow(`SELECT COALESCE(group_concat(slug || ':' || seed_anilist || ':' ||
		COALESCE(json_extract(config, '$.origin'), ''), ' '), '') FROM (SELECT * FROM anime_franchise ORDER BY slug)`).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// An ad-hoc discovery whose finished crawl reaches a shipped franchise's seed builds that franchise instead.
func TestDiscoveryCrawlReachingShippedSeedBuildsIt(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	s, _, w := seedService(t, st, "- slug: big-show\n  name: Big Show\n  seed_anilist: 1000\n")
	s.cfg.Build = fakeBuild(map[int][]int{2000: {2002, 1000}, 1000: {2000, 2002}})
	exec(t, st, `INSERT INTO title (id, kind, name, tvdb_id, anime, created_at, updated_at) VALUES (2, 'series', 'Show Two', 600, 1, 0, 0)`)
	if _, err := s.EnqueueDiscover(ctx, 600); err != nil {
		t.Fatal(err)
	}
	if ran, err := w.RunOne(ctx); err != nil || !ran {
		t.Fatal(ran, err)
	}
	if got := franchises(t, st); got != "big-show:1000:list" {
		var jerr string
		_ = st.Reader().QueryRow(`SELECT COALESCE(last_error, '') || ' ' || state FROM job`).Scan(&jerr)
		t.Fatalf("franchises %q (job %s)", got, jerr)
	}
	if n := count(t, st, `SELECT count(*) FROM anime_discovery WHERE title_id = 2 AND outcome = 'built' AND franchise = 'big-show'`); n != 1 {
		t.Fatal("outcome not built by the shipped franchise")
	}
	// once the shipped franchise is stored, a series whose crawl reaches it is covered by it, not rebuilt
	finishJobs(t, st)
	exec(t, st, `INSERT INTO title (id, kind, name, tvdb_id, anime, created_at, updated_at) VALUES (3, 'series', 'Show Three', 700, 1, 0, 0)`)
	s.cfg.Build = fakeBuild(map[int][]int{3000: {1000}})
	exec(t, st, `UPDATE anime_franchise SET built_at = 5 WHERE slug = 'big-show'`)
	if _, err := s.EnqueueDiscover(ctx, 700); err != nil {
		t.Fatal(err)
	}
	if ran, err := w.RunOne(ctx); err != nil || !ran {
		t.Fatal(ran, err)
	}
	if n := count(t, st, `SELECT count(*) FROM anime_discovery WHERE title_id = 3 AND outcome = 'covered' AND franchise = 'big-show'`); n != 1 ||
		franchises(t, st) != "big-show:1000:list" || count(t, st, `SELECT count(*) FROM anime_franchise WHERE built_at = 5`) != 1 {
		t.Fatal("stored shipped franchise rebuilt or not covering")
	}
}

// A manual seed resolves by the seed first and never builds one seed under two slugs or two seeds under one slug.
func TestEnqueueSeedOwnership(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	s, _, w := seedService(t, st, "- slug: frieren\n  name: Frieren\n  seed_anilist: 154587\n")
	s.cfg.Build = fakeBuild(nil)
	exec(t, st, `INSERT INTO anime_franchise (slug, name, seed_anilist, created_at, updated_at) VALUES ('one-piece', 'One Piece', 21, 0, 0)`)
	exec(t, st, `INSERT INTO anime_entry (franchise, key, origin, anilist_id, body) VALUES ('one-piece', 'al:22', 'anilist', 22, '{}')`)
	payload := func(id int64) string {
		var out string
		if err := st.Reader().QueryRow(`SELECT dedupe_key || ' ' || payload FROM job WHERE id = ?`, id).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	for _, c := range []struct {
		seed       int
		slug, want string
		key        string
	}{
		{21, "other", "one-piece", SlugKey("one-piece")}, // the stored seed: a rebuild of its franchise
		{22, "", "one-piece", SlugKey("one-piece")},      // an entry of a stored franchise
		{154587, "mine", "frieren", SeedKey(154587)},     // a shipped seed, under its own slug
	} {
		id, slug, err := s.EnqueueSeed(ctx, c.seed, c.slug, "")
		if err != nil || slug != c.want || !strings.HasPrefix(payload(id), c.key+" ") {
			t.Fatalf("seed %d: %s %v (%s)", c.seed, slug, err, payload(id))
		}
	}
	if _, _, err := s.EnqueueSeed(ctx, 40, "frieren", ""); err != ErrSlugTaken {
		t.Fatalf("shipped slug: %v", err)
	}
	first, slug, err := s.EnqueueSeed(ctx, 50, "shared", "")
	if err != nil || slug != "shared" {
		t.Fatal(slug, err)
	}
	if _, _, err := s.EnqueueSeed(ctx, 51, "shared", ""); err != ErrSlugTaken {
		t.Fatalf("queued slug: %v", err)
	}
	if id, slug, err := s.EnqueueSeed(ctx, 50, "elsewhere", ""); err != nil || id != first || slug != "shared" {
		t.Fatalf("queued seed: %d %s %v", id, slug, err)
	}

	// The handler checks again before the save: the slug another seed took, or the seed another franchise took.
	finishJobs(t, st)
	if _, _, err := s.EnqueueSeed(ctx, 60, "late", ""); err != nil {
		t.Fatal(err)
	}
	exec(t, st, `INSERT INTO anime_franchise (slug, name, seed_anilist, created_at, updated_at) VALUES ('late', 'Late', 61, 0, 0)`)
	if ran, err := w.RunOne(ctx); err != nil || !ran {
		t.Fatal(ran, err)
	}
	if _, _, err := s.EnqueueSeed(ctx, 70, "a7", ""); err != nil {
		t.Fatal(err)
	}
	exec(t, st, `INSERT INTO anime_franchise (slug, name, seed_anilist, created_at, updated_at) VALUES ('b7', 'B7', 70, 0, 0)`)
	if ran, err := w.RunOne(ctx); err != nil || !ran {
		t.Fatal(ran, err)
	}
	if got := franchises(t, st); got != "b7:70: late:61: one-piece:21:" {
		t.Fatalf("franchises %q", got)
	}
}

// A rebuild records its attempt in the franchise's config; the loop takes the franchise attempted longest ago and
// skips one whose rebuild failed within a day, so a franchise that keeps failing does not take every refresh slot.
func TestRefreshSkipsRecentlyFailedFranchise(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	s, _, w := seedService(t, st, "")
	c := &clock{t: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}
	s.cfg.Now = c.now
	ok := fakeBuild(nil)
	s.cfg.Build = func(ctx context.Context, src *anime.Sources, f anime.Franchise, opt anime.Options) (*anime.Result, error) {
		if f.Slug == "broken" {
			return nil, jobs.Permanent(errors.New("bad overrides"))
		}
		return ok(ctx, src, f, opt)
	}
	old := store.Millis(c.now().Add(-30 * 24 * time.Hour))
	exec(t, st, `INSERT INTO anime_franchise (slug, name, seed_anilist, built_at, created_at, updated_at) VALUES
		('broken', 'Broken', 1, ?, 0, 0), ('fine', 'Fine', 2, ?, 0, 0)`, old-10, old)
	var next time.Time
	pass := func(want string) {
		t.Helper()
		next = time.Time{}
		if err := s.Pass(ctx, &next); err != nil {
			t.Fatal(err)
		}
		var got string
		_ = st.Reader().QueryRow(`SELECT json_extract(payload, '$.slug') FROM job WHERE state = 'queued'`).Scan(&got)
		if got != want {
			t.Fatalf("queued %q, want %q", got, want)
		}
		if want != "" {
			if ran, err := w.RunOne(ctx); !ran || err != nil {
				t.Fatal(ran, err)
			}
		}
	}
	pass("broken")
	if n := count(t, st, `SELECT count(*) FROM anime_franchise WHERE slug = 'broken' AND json_extract(config, '$.last_outcome') = 'failed'
		AND json_extract(config, '$.last_attempt') = ? AND json_extract(config, '$.last_error') LIKE '%bad overrides%'`, store.Millis(c.now())); n != 1 {
		t.Fatal("failed attempt not recorded")
	}
	pass("fine") // the failed one waits
	if n := count(t, st, `SELECT count(*) FROM anime_franchise WHERE slug = 'fine' AND json_extract(config, '$.last_outcome') = 'ok'`); n != 1 {
		t.Fatal("attempt not recorded")
	}
	pass("") // fine is built now; broken failed within the day
	c.add(failedRetry + time.Minute)
	pass("broken")
}
