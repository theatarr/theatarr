package health

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
	"github.com/theatarr/theatarr/internal/uipages"
)

var t0 = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func openStore(t *testing.T) *store.Store {
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
	return st
}

func exec(t *testing.T, st *store.Store, q string, args ...any) {
	t.Helper()
	if _, err := st.Writer().Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func TestCheckerCachesAndBounds(t *testing.T) {
	now := t0
	c := New(Options{TTL: time.Minute, Timeout: 50 * time.Millisecond, Now: func() time.Time { return now }})
	var runs atomic.Int32
	c.Add("CountCheck", "Count", func(ctx context.Context) Result {
		runs.Add(1)
		return Result{Message: "fine"}
	})
	c.Add("SlowCheck", "Slow", func(ctx context.Context) Result {
		<-ctx.Done()
		time.Sleep(10 * time.Millisecond)
		return Result{Type: OK}
	})
	c.Add("PanicCheck", "Panic", func(ctx context.Context) Result { panic("boom") })
	c.Add("WarnCheck", "Warn", func(ctx context.Context) Result { return Result{Type: Warning, Message: "w"} })
	rs := c.Results(context.Background())
	if len(rs) != 4 || rs[0].Type != OK || rs[0].Source != "CountCheck" || rs[0].Name != "Count" {
		t.Fatalf("results %+v", rs)
	}
	if rs[1].Type != Error || !strings.Contains(rs[1].Message, "did not finish") {
		t.Errorf("slow: %+v", rs[1])
	}
	if rs[2].Type != Error || strings.Contains(rs[2].Message, "boom") {
		t.Errorf("panic: %+v", rs[2])
	}
	c.Results(context.Background())
	if runs.Load() != 1 {
		t.Errorf("ran %d times within the TTL", runs.Load())
	}
	now = now.Add(2 * time.Minute)
	c.Results(context.Background())
	if runs.Load() != 2 {
		t.Errorf("ran %d times after the TTL", runs.Load())
	}
	is := c.Issues(context.Background())
	if len(is) != 3 || is[0].Type != Error || is[2].Source != "WarnCheck" || Worst(is) != Error {
		t.Errorf("issues %+v", is)
	}
}

func TestMount(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if r := Mount(dir, time.Second)(ctx); r.Type != Error || !strings.Contains(r.Message, "empty") {
		t.Errorf("empty: %+v", r)
	}
	os.WriteFile(filepath.Join(dir, "a"), nil, 0o644)
	if r := Mount(dir, time.Second)(ctx); r.Type != OK {
		t.Errorf("ok: %+v", r)
	}
	if r := Mount(filepath.Join(dir, "gone"), time.Second)(ctx); r.Type != Error {
		t.Errorf("missing: %+v", r)
	}
	// A hung probe: the first check times out, the second does not start another probe.
	release := make(chan struct{})
	var probes atomic.Int32
	probe = func(string) (int, error) { probes.Add(1); <-release; return 1, nil }
	t.Cleanup(func() { probe = probeDir })
	f := Mount(dir, 20*time.Millisecond)
	if r := f(ctx); r.Type != Error || !strings.Contains(r.Message, "did not answer") {
		t.Errorf("hung: %+v", r)
	}
	if r := f(ctx); r.Type != Error || !strings.Contains(r.Message, "still blocked") || probes.Load() != 1 {
		t.Errorf("still hung: %+v (%d probes)", r, probes.Load())
	}
	close(release)
	for range 100 {
		if r := f(ctx); r.Type == OK {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("the mount check did not recover after the probe returned")
}

func TestMetadata(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	old := store.Millis(t0.Add(-48 * time.Hour))
	exec(t, st, `INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'A', ?, ?),
		(2, 'movie', 'B', ?, ?), (3, 'movie', 'C', ?, ?)`, old, old, old, old, store.Millis(t0), store.Millis(t0))
	now := func() time.Time { return t0 }
	// Title 2 has no row a day after it was added; title 3 is new.
	exec(t, st, `INSERT INTO title_meta (title_id, refreshed_at, attempted_at, refresh_after) VALUES (1, ?, ?, ?)`,
		store.Millis(t0.Add(-time.Hour)), store.Millis(t0.Add(-time.Hour)), store.Millis(t0.Add(72*time.Hour)))
	r := Metadata(st, MetadataOptions{TMDb: true, Now: now})(ctx)
	if r.Type != Warning || !strings.Contains(r.Message, "1 title(s) have no metadata") {
		t.Errorf("missing: %+v", r)
	}
	exec(t, st, `INSERT INTO title_meta (title_id, attempted_at, refresh_after) VALUES (2, ?, ?)`,
		store.Millis(t0.Add(-time.Hour)), store.Millis(t0.Add(time.Hour)))
	if r := Metadata(st, MetadataOptions{TMDb: true, Now: now})(ctx); r.Type != OK {
		t.Errorf("current: %+v", r)
	}
	// Refreshes failing, and the TVDB refresh not run for a day.
	exec(t, st, `UPDATE title_meta SET error = 'tmdb: 503', attempted_at = ?`, store.Millis(t0.Add(-time.Minute)))
	exec(t, st, `INSERT INTO title_meta (title_id, attempted_at, refresh_after, error) VALUES (3, ?, ?, 'tmdb: 503')`,
		store.Millis(t0.Add(-time.Minute)), store.Millis(t0.Add(time.Hour)))
	exec(t, st, `INSERT INTO tvdb_sync (id, since_at, run_at) VALUES (1, 0, ?)`, store.Millis(t0.Add(-24*time.Hour)))
	r = Metadata(st, MetadataOptions{TMDb: true, TVDB: true, Now: now})(ctx)
	if r.Type != Warning || !strings.Contains(r.Message, "3 of 3 TMDb refreshes") || !strings.Contains(r.Message, "TVDB") {
		t.Errorf("failing: %+v", r)
	}
	// Overdue rows and no good fetch for days.
	exec(t, st, `UPDATE title_meta SET error = '', refresh_after = ?, refreshed_at = ?, attempted_at = ?`,
		store.Millis(t0.Add(-30*time.Hour)), old, old)
	r = Metadata(st, MetadataOptions{TMDb: true, Now: now})(ctx)
	if r.Type != Warning || !strings.Contains(r.Message, "no TMDb refresh succeeded in 2d") ||
		!strings.Contains(r.Message, "3 title(s) are more than a day overdue") {
		t.Errorf("stale: %+v", r)
	}
	if r := Metadata(st, MetadataOptions{Now: now})(ctx); r.Type != Warning || !strings.Contains(r.Message, "TMDb refresh is off") {
		t.Errorf("off: %+v", r)
	}
}

func TestBacklogAndFailed(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	q := jobs.NewQueue(st)
	q.SetClock(func() time.Time { return t0 })
	now := func() time.Time { return t0 }
	f := Backlog(q, BacklogOptions{Urgent: []string{"router.route"}, Now: now})
	if r := f(ctx); r.Type != OK {
		t.Errorf("empty: %+v", r)
	}
	q.Enqueue(ctx, "meta.art", nil, jobs.EnqueueOptions{RunAt: t0.Add(-2 * time.Hour)})
	q.Enqueue(ctx, "router.route", nil, jobs.EnqueueOptions{RunAt: t0.Add(-20 * time.Minute)})
	r := f(ctx)
	if r.Type != Warning || !strings.Contains(r.Message, "router.route (1 waiting, oldest 20m)") ||
		strings.Contains(r.Message, "meta.art") {
		t.Errorf("urgent late: %+v", r)
	}
	q.Enqueue(ctx, "router.route", nil, jobs.EnqueueOptions{RunAt: t0.Add(-2 * time.Hour)})
	q.Enqueue(ctx, "meta.art", nil, jobs.EnqueueOptions{RunAt: t0.Add(-7 * time.Hour)})
	r = f(ctx)
	if r.Type != Error || !strings.Contains(r.Message, "meta.art (2 waiting, oldest 7h)") {
		t.Errorf("late: %+v", r)
	}
	if r := FailedJobs(q)(ctx); r.Type != OK {
		t.Errorf("failed: %+v", r)
	}
	exec(t, st, `INSERT INTO job (kind, payload, state, run_at, max_attempts, created_at, updated_at, finished_at,
		last_error) VALUES ('provider.sync', '{}', 'failed', 0, 1, 0, 0, 0, 'x')`)
	if r := FailedJobs(q)(ctx); r.Type != Warning || !strings.Contains(r.Message, "1 failed job(s)") || !strings.Contains(r.Message, "(System › Tasks)") {
		t.Errorf("failed: %+v", r)
	}
}

func TestBackup(t *testing.T) {
	ctx := context.Background()
	if r := Backup(BackupOptions{})(ctx); r.Type != Warning || !strings.Contains(r.Message, "off") {
		t.Errorf("off: %+v", r)
	}
	dir := t.TempDir()
	now := func() time.Time { return t0 }
	if r := Backup(BackupOptions{Dir: dir, Started: t0.Add(-time.Hour), Now: now})(ctx); r.Type != OK {
		t.Errorf("first day: %+v", r)
	}
	if r := Backup(BackupOptions{Dir: dir, Started: t0.Add(-48 * time.Hour), Now: now})(ctx); r.Type != Error {
		t.Errorf("none: %+v", r)
	}
	db := filepath.Join(dir, "theatarr-20261026-030000.db")
	os.WriteFile(db, []byte("x"), 0o600)
	os.Chtimes(db, t0.Add(-9*time.Hour), t0.Add(-9*time.Hour))
	if r := Backup(BackupOptions{Dir: dir, Now: now})(ctx); r.Type != Warning || !strings.Contains(r.Message, "manifest") {
		t.Errorf("no sidecars: %+v", r)
	}
	os.WriteFile(filepath.Join(dir, "theatarr-20261026-030000.policy.tar"), nil, 0o600)
	os.WriteFile(filepath.Join(dir, "theatarr-20261026-030000.manifest.json"), nil, 0o600)
	if r := Backup(BackupOptions{Dir: dir, Now: now})(ctx); r.Type != OK || !strings.Contains(r.Message, "9h ago") {
		t.Errorf("fresh: %+v", r)
	}
	os.Chtimes(db, t0.Add(-40*time.Hour), t0.Add(-40*time.Hour))
	if r := Backup(BackupOptions{Dir: dir, Now: now})(ctx); r.Type != Warning {
		t.Errorf("old: %+v", r)
	}
	os.Chtimes(db, t0.Add(-80*time.Hour), t0.Add(-80*time.Hour))
	if r := Backup(BackupOptions{Dir: dir, Now: now})(ctx); r.Type != Error {
		t.Errorf("very old: %+v", r)
	}
}

// A database-only set (no policy directory) with its manifest is complete; a set missing the archive of a policy
// directory that exists is not.
func TestBackupDatabaseOnlySet(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	now := func() time.Time { return t0 }
	base := filepath.Join(dir, "theatarr-20261026-030000")
	os.WriteFile(base+".db", []byte("x"), 0o600)
	os.Chtimes(base+".db", t0.Add(-time.Hour), t0.Add(-time.Hour))
	policyDir := filepath.Join(dir, "policy")
	manifest := `{"policy":{"state":"absent","dir":"` + policyDir + `"}}`
	os.WriteFile(base+".manifest.json", []byte(manifest), 0o600)
	if r := Backup(BackupOptions{Dir: dir, Now: now})(ctx); r.Type != OK {
		t.Errorf("database-only set: %+v", r)
	}
	if err := os.Mkdir(policyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if r := Backup(BackupOptions{Dir: dir, Now: now})(ctx); r.Type != Warning || !strings.Contains(r.Message, "policy archive") {
		t.Errorf("policy directory now exists, set has no archive: %+v", r)
	}
	os.WriteFile(base+".manifest.json", []byte(`{"policy":{"state":"files","dir":"`+policyDir+`"}}`), 0o600)
	if r := Backup(BackupOptions{Dir: dir, Now: now})(ctx); r.Type != Warning {
		t.Errorf("archive missing from a set with policy files: %+v", r)
	}
}

func TestProviders(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	now := func() time.Time { return t0 }
	f := Providers(st, ProviderOptions{Providers: []string{"rd", "tb"}, Interval: time.Hour, Now: now})
	if r := f(ctx); r.Type != OK {
		t.Errorf("never synced: %+v", r)
	}
	at := store.Millis(t0.Add(-10 * time.Minute))
	exec(t, st, `INSERT INTO provider_sync_run (provider, started_at, finished_at, ok, error) VALUES
		('rd', ?, ?, 1, NULL), ('tb', ?, ?, 0, 'tb: 401 unauthorized')`, at, at, at, at)
	if r := f(ctx); r.Type != Warning || !strings.Contains(r.Message, "tb: 401") || strings.Contains(r.Message, "tb: tb:") {
		t.Errorf("one failing: %+v", r)
	}
	old := store.Millis(t0.Add(-5 * time.Hour))
	exec(t, st, `UPDATE provider_sync_run SET finished_at = ? WHERE provider = 'rd'`, old)
	if r := f(ctx); r.Type != Error || !strings.Contains(r.Message, "no complete sync in 5h") {
		t.Errorf("all failing: %+v", r)
	}
}

func TestUnsettledAdds(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	f := UnsettledAdds(st, 0, func() time.Time { return t0 })
	exec(t, st, `INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'M', 0, 0)`)
	exec(t, st, `INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'm', 0, 0)`)
	exec(t, st, `INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		provider, error, created_at, updated_at) VALUES (1, 1, 'm', 'a', 'R', 'pub', 'adding', 'rd', ?, 0, ?)`,
		"add not settled: reading rd's torrents failed: 503", store.Millis(t0.Add(-time.Minute)))
	if r := f(ctx); r.Type != OK {
		t.Errorf("a fresh add: %+v", r)
	}
	exec(t, st, `UPDATE download SET updated_at = ?`, store.Millis(t0.Add(-3*time.Hour)))
	if r := f(ctx); r.Type != Warning || !strings.Contains(r.Message, "1 download(s) in adding for up to 3h") ||
		!strings.Contains(r.Detail.(map[string]any)["error"].(string), "503") {
		t.Errorf("unsettled: %+v", r)
	}
}

func TestUnfinishedCacheChecks(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	f := UnfinishedCacheChecks(st, 0, func() time.Time { return t0 })
	exec(t, st, `INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'M', 0, 0)`)
	exec(t, st, `INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'm', 0, 0)`)
	exec(t, st, `INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		provider, provider_torrent_id, cached_check, added_at, created_at, updated_at) VALUES (1, 1, 'm', 'a', 'R',
		'pub', 'downloading', 'rd', 'RD1', 'uncached', ?, 0, 0)`, store.Millis(t0.Add(-10*time.Minute)))
	if r := f(ctx); r.Type != OK {
		t.Errorf("a fresh check: %+v", r)
	}
	exec(t, st, `UPDATE download SET added_at = ?`, store.Millis(t0.Add(-2*time.Hour)))
	if r := f(ctx); r.Type != Warning || !strings.Contains(r.Message, "1 download(s) added up to 2h ago") {
		t.Errorf("unfinished: %+v", r)
	}
	exec(t, st, `UPDATE download SET state = 'failed'`)
	if r := f(ctx); r.Type != OK {
		t.Errorf("a failed download counts: %+v", r)
	}
}

func TestIndexersEgressDisk(t *testing.T) {
	ctx := context.Background()
	s := gateway.State{
		Backoffs: []gateway.BackoffState{{Subject: "indexer:dmm", Active: true, Reason: "5 failures"},
			{Subject: "indexer:zilean", Active: false}},
		Budgets: []gateway.BudgetState{{Key: "prowlarr", DayCapped: true, UsedBy: []string{"indexer:tpb"}}},
	}
	f := Indexers(func() gateway.State { return s }, []string{"dmm", "tpb", "zilean"})
	r := f(ctx)
	if r.Type != Warning || r.Message != "2 of 3 indexer(s) unavailable (backed off or budget spent): see Settings › Indexers for which" ||
		fmt.Sprint(r.Detail) != "map[unavailable:map[dmm:backed off: 5 failures tpb:budget prowlarr spent]]" {
		t.Errorf("indexers: %+v", r)
	}
	s.Backoffs[1].Active = true
	if r := f(ctx); r.Type != Error {
		t.Errorf("all down: %+v", r)
	}
	if r := Egress(nil, false)(ctx); r.Type != Warning || r.Message != "VPN guard off" {
		t.Errorf("no egress: %+v", r)
	}
	sum := map[string]any{"vpn_ok": false, "paths": []map[string]any{{"path": "direct", "error": "exit 1.2.3.4 is not a VPN address"}}}
	if r := Egress(func() map[string]any { return sum }, true)(ctx); r.Type != Error || !strings.Contains(r.Message, "direct: exit") {
		t.Errorf("egress: %+v", r)
	}
	sum["vpn_ok"] = true
	if r := Egress(func() map[string]any { return sum }, true)(ctx); r.Type != OK {
		t.Errorf("egress ok: %+v", r)
	}
	sum["mode"] = "monitor-only"
	if r := Egress(func() map[string]any { return sum }, true)(ctx); r.Type != OK || !strings.Contains(r.Message, "monitor-only") {
		t.Errorf("monitor ok: %+v", r)
	}
	sum["vpn_ok"] = false
	if r := Egress(func() map[string]any { return sum }, true)(ctx); r.Type != Warning || !strings.Contains(r.Message, "no requests require VPN") {
		t.Errorf("monitor down: %+v", r)
	}
	sum["mode"] = "off"
	if r := Egress(func() map[string]any { return sum }, true)(ctx); r.Type != Warning || r.Message != "VPN guard off" {
		t.Errorf("runtime off: %+v", r)
	}
	dir := t.TempDir()
	if r := Disk(dir, DiskOptions{WarnBytes: 1, ErrorBytes: 1, WarnPercent: 0.0001})(ctx); r.Type != OK {
		t.Errorf("disk: %+v", r)
	}
	if r := Disk(dir, DiskOptions{WarnBytes: 1 << 62, ErrorBytes: 1, WarnPercent: 0.0001})(ctx); r.Type != Warning {
		t.Errorf("disk low: %+v", r)
	}
	if r := Disk(dir, DiskOptions{WarnBytes: 1 << 62, ErrorBytes: 1 << 62})(ctx); r.Type != Error {
		t.Errorf("disk very low: %+v", r)
	}
	if r := Disk(filepath.Join(dir, "gone"), DiskOptions{})(ctx); r.Type != Warning {
		t.Errorf("disk missing: %+v", r)
	}
}

// The serve mount check through the library's guard reports what it answers; the helper check warns from 3 kills
// in 15 minutes.
func TestMountAnsweringAndHelper(t *testing.T) {
	ctx := context.Background()
	answer := func(n int, err error) func(context.Context) (int, error) {
		return func(context.Context) (int, error) { return n, err }
	}
	if r := MountAnswering("/m", answer(1, nil))(ctx); r.Type != OK {
		t.Errorf("ok: %+v", r)
	}
	if r := MountAnswering("/m", answer(0, nil))(ctx); r.Type != Error || !strings.Contains(r.Message, "empty") {
		t.Errorf("empty: %+v", r)
	}
	if r := MountAnswering("/m", answer(0, errors.New("library: mount unhealthy: no answer")))(ctx); r.Type != Error ||
		!strings.Contains(r.Message, "not answering") {
		t.Errorf("down: %+v", r)
	}
	state := func(s *MountHelperState) func() *MountHelperState { return func() *MountHelperState { return s } }
	if r := MountHelper(state(nil))(ctx); r.Type != OK {
		t.Errorf("no helpers: %+v", r)
	}
	if r := MountHelper(state(&MountHelperState{RecentKills: 2}))(ctx); r.Type != OK {
		t.Errorf("2 kills: %+v", r)
	}
	r := MountHelper(state(&MountHelperState{RecentKills: 3, Lingering: 1, LastError: "stat /m/x: no answer in time"}))(ctx)
	if r.Type != Warning || !strings.Contains(r.Message, "killed 3 times") || !strings.Contains(r.Message, "1 killed helpers") {
		t.Errorf("3 kills: %+v", r)
	}
}

// A caller that gives up must not leave its cut-short errors in the cache for everyone else.
func TestCheckerDoesNotCacheCancelledRun(t *testing.T) {
	c := New(Options{TTL: time.Minute, Timeout: time.Second, Now: func() time.Time { return t0 }})
	c.Add("WaitCheck", "Wait", func(ctx context.Context) Result {
		select {
		case <-ctx.Done():
			return Result{Type: Error, Message: ctx.Err().Error()}
		case <-time.After(20 * time.Millisecond):
			return Result{Type: OK}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Results(ctx)
	if rs := c.Results(context.Background()); len(rs) != 1 || rs[0].Type != OK {
		t.Fatalf("a cancelled run was cached: %+v", rs)
	}
}

// TestTorrentLimits: the warning from 90% of an account's limit, counted from its latest complete sync; accounts
// without a limit or without a sync are not warned about.
func TestTorrentLimits(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	lim := map[string]int{"ad": 5000, "rd": 0}
	f := TorrentLimits(st, func(context.Context) map[string]int { return lim }, 90)
	if r := f(ctx); r.Type != OK || !strings.Contains(r.Message, "no complete provider sync") {
		t.Errorf("never synced: %+v", r)
	}
	at := store.Millis(t0.Add(-10 * time.Minute))
	exec(t, st, `INSERT INTO provider_sync_run (provider, started_at, finished_at, ok, summary) VALUES
		('ad', ?, ?, 1, '{"torrents": 4400}'), ('rd', ?, ?, 1, '{"torrents": 99999}')`, at, at, at, at)
	if r := f(ctx); r.Type != OK || r.Message != "Torrent limits: ad 4400 of 5000 (88%)" {
		t.Errorf("under 90%%: %+v", r)
	}
	later := store.Millis(t0)
	exec(t, st, `INSERT INTO provider_sync_run (provider, started_at, finished_at, ok, summary) VALUES
		('ad', ?, ?, 1, '{"torrents": 4500}'), ('ad', ?, ?, 0, '{}')`, later, later, later, later)
	if r := f(ctx); r.Type != Warning || !strings.Contains(r.Message, "ad 4500 of 5000 (90%)") {
		t.Errorf("at 90%%: %+v", r)
	}
	lim = map[string]int{"rd": 0}
	if r := f(ctx); r.Type != OK || !strings.Contains(r.Message, "No debrid account with a torrent limit") {
		t.Errorf("no limits: %+v", r)
	}
}

type fakeAccount struct {
	account *provider.Account
	err     error
	block   bool
	reads   atomic.Int32
}

func (f *fakeAccount) Account(ctx context.Context) (*provider.Account, error) {
	f.reads.Add(1)
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.account, f.err
}

// TestAccounts: each account state and its wording; one provider not answering is a warning naming it, a refused
// key an error.
func TestAccounts(t *testing.T) {
	far := &fakeAccount{account: &provider.Account{Premium: true, Expires: t0.Add(90 * 24 * time.Hour)}}
	soon := &fakeAccount{account: &provider.Account{Premium: true, Expires: t0.Add(6*24*time.Hour + time.Hour)}}
	expired := &fakeAccount{account: &provider.Account{Premium: false, Expires: t0.Add(-24 * time.Hour)}}
	free := &fakeAccount{account: &provider.Account{Plan: "free"}}
	lifetime := &fakeAccount{account: &provider.Account{Premium: true}}
	refused := &fakeAccount{err: fmt.Errorf("realdebrid: %w", provider.ErrAuth)}
	down := &fakeAccount{err: errors.New("dial tcp: connection refused")}
	hung := &fakeAccount{block: true}
	o := AccountOptions{Timeout: 20 * time.Millisecond, Now: func() time.Time { return t0 }}
	run := func(m map[string]AccountReader) Result {
		return Accounts(func(context.Context) map[string]AccountSource { return sources(m, nil) }, o)(context.Background())
	}
	for _, tc := range []struct {
		name string
		m    map[string]AccountReader
		typ  string
		msg  string
	}{
		{"none", nil, OK, "No debrid provider keys set; no account to read"},
		{"far", map[string]AccountReader{"rd": far, "tb": lifetime}, OK, "Debrid accounts: rd: premium until Jan 24, 2027; tb: premium"},
		{"soon", map[string]AccountReader{"rd": far, "tb": soon}, Warning,
			"Debrid account needs attention: tb: premium ends Nov 1 (in 6d)"},
		{"expired", map[string]AccountReader{"ad": expired}, Error, "Debrid account needs attention: ad: premium ended Oct 25"},
		{"free", map[string]AccountReader{"ad": free}, Error, "Debrid account needs attention: ad: not premium"},
		{"refused", map[string]AccountReader{"rd": refused, "tb": far}, Error,
			"Debrid account needs attention: rd: key refused (realdebrid: provider: authentication refused). Check the key in " + uipages.Providers},
		{"down", map[string]AccountReader{"rd": far, "tb": down}, Warning,
			"Debrid account needs attention: tb: account not readable (dial tcp: connection refused)"},
		{"hung", map[string]AccountReader{"tb": hung}, Warning,
			"Debrid account needs attention: tb: account not readable (context deadline exceeded)"},
		{"worst", map[string]AccountReader{"ad": free, "tb": soon}, Error,
			"Debrid account needs attention: ad: not premium; tb: premium ends Nov 1 (in 6d)"},
	} {
		r := run(tc.m)
		if r.Type != tc.typ || r.Message != tc.msg {
			t.Errorf("%s: %s %q, want %s %q", tc.name, r.Type, r.Message, tc.typ, tc.msg)
		}
	}
}

// sources gives each client its credential's fingerprint (keys[k]).
func sources(m map[string]AccountReader, keys map[string]string) map[string]AccountSource {
	out := map[string]AccountSource{}
	for k, c := range m {
		out[k] = AccountSource{Reader: c, Fingerprint: Fingerprint(keys[k])}
	}
	return out
}

// TestAccountsCache: health polls reuse a good read for the interval and a failed one for the retry time, the
// expiry is judged against the clock at each run, and new clients (a settings save) reuse the read while their key
// is unchanged; a changed key is read at once.
func TestAccountsCache(t *testing.T) {
	now := t0
	acct := &fakeAccount{account: &provider.Account{Premium: true, Expires: t0.Add(7*24*time.Hour + 3*time.Hour)}}
	down := &fakeAccount{err: errors.New("503")}
	clients := map[string]AccountReader{"rd": acct, "tb": down}
	keys := map[string]string{"rd": "rd-key-1", "tb": "tb-key-1"}
	f := Accounts(func(context.Context) map[string]AccountSource { return sources(clients, keys) },
		AccountOptions{Interval: 6 * time.Hour, Retry: 30 * time.Minute, Now: func() time.Time { return now }})
	ctx := context.Background()
	if r := f(ctx); r.Type != Warning || !strings.Contains(r.Message, "tb: account not readable") {
		t.Fatalf("first: %+v", r)
	}
	for range 5 {
		f(ctx)
	}
	if acct.reads.Load() != 1 || down.reads.Load() != 1 {
		t.Fatalf("polls read again: %d %d", acct.reads.Load(), down.reads.Load())
	}
	now = t0.Add(31 * time.Minute)
	down.err = nil
	down.account = &provider.Account{Premium: true}
	if r := f(ctx); r.Type != OK || acct.reads.Load() != 1 || down.reads.Load() != 2 {
		t.Fatalf("retry: %+v, reads %d %d", r, acct.reads.Load(), down.reads.Load())
	}
	// Four hours on, the cached expiry is inside the window: a warning without a new read.
	now = t0.Add(4 * time.Hour)
	if r := f(ctx); r.Type != Warning || !strings.Contains(r.Message, "rd: premium ends Nov 2 (in 6d)") || acct.reads.Load() != 1 {
		t.Fatalf("window: %+v, reads %d", r, acct.reads.Load())
	}
	// After the interval the account is read again.
	now = t0.Add(6*time.Hour + time.Minute)
	acct.account = &provider.Account{Premium: true, Expires: t0.Add(400 * 24 * time.Hour)}
	if r := f(ctx); r.Type != OK || acct.reads.Load() != 2 {
		t.Fatalf("interval: %+v, reads %d", r, acct.reads.Load())
	}
	// A settings save builds new clients: with the same key they reuse the read; a changed key is read at once.
	fresh := &fakeAccount{account: &provider.Account{Premium: true, Expires: t0.Add(400 * 24 * time.Hour)}}
	clients = map[string]AccountReader{"rd": fresh}
	if r := f(ctx); r.Type != OK || fresh.reads.Load() != 0 {
		t.Fatalf("new client, same key: %+v, reads %d", r, fresh.reads.Load())
	}
	keys["rd"] = "rd-key-2"
	if r := f(ctx); r.Type != OK || fresh.reads.Load() != 1 {
		t.Fatalf("changed key: %+v, reads %d", r, fresh.reads.Load())
	}
	// A run cut short by the caller is not cached.
	cut, cancel := context.WithCancel(ctx)
	cancel()
	blocked := &fakeAccount{block: true}
	clients = map[string]AccountReader{"rd": blocked}
	keys["rd"] = "rd-key-3"
	f(cut)
	f(cut)
	if blocked.reads.Load() != 2 {
		t.Fatalf("cancelled run cached: %d", blocked.reads.Load())
	}
}

// The dangling-link check counts links under the libraries' roots only, not other folders the report also walks.
func TestDanglingCountsLibraryRootsOnly(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	at := store.Millis(t0)
	exec(t, st, `INSERT INTO library_dangling_scan (at, roots, links, dangling, took_ms, no_video, no_video_torrents,
		repairing) VALUES (?, '[]', 100, 3, 1, 2, 5, 0)`, at)
	exec(t, st, `INSERT INTO library_dangling (path, target, root, version_key, folder, cause, first_seen_at,
		last_seen_at) VALUES ('/media/mm/tv/A/a.mkv', '/mnt/z/A/a.mkv', '/media/mm/tv', 'tv-2160p', 'A', 'no_video', ?, ?),
		('/media/shows4k-zurg/B/b.mkv', '/mnt/z/B/b.mkv', '/media', NULL, 'B', 'no_video', ?, ?),
		('/media/shows4k-zurg/C/c.mkv', '/mnt/z/C/c.mkv', '/media', NULL, 'C', NULL, ?, ?)`, at, at, at, at, at, at)
	r := Dangling(st)(ctx)
	if r.Type != Notice || r.Message != "1 dangling link in the libraries; 1 of them: torrent has no video upstream" ||
		r.Detail.(map[string]any)["dangling"] != 1 {
		t.Errorf("one library link: %+v", r)
	}
	exec(t, st, `DELETE FROM library_dangling WHERE version_key IS NOT NULL`)
	if r := Dangling(st)(ctx); r.Type != OK || r.Message != "No dangling links in the libraries" {
		t.Errorf("only other folders: %+v", r)
	}
}
