package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/retention"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
	"github.com/theatarr/theatarr/internal/tasks"
)

func TestNightlyNext(t *testing.T) {
	t.Parallel()
	n := &nightly{hour: 3}
	loc := time.UTC
	cases := []struct{ now, want time.Time }{
		{time.Date(2026, 10, 25, 1, 0, 0, 0, loc), time.Date(2026, 10, 25, 3, 0, 0, 0, loc)},
		{time.Date(2026, 10, 25, 3, 0, 0, 0, loc), time.Date(2026, 10, 26, 3, 0, 0, 0, loc)},
		{time.Date(2026, 10, 25, 23, 0, 0, 0, loc), time.Date(2026, 10, 26, 3, 0, 0, 0, loc)},
	}
	for _, c := range cases {
		if got := n.next(c.now); !got.Equal(c.want) {
			t.Errorf("next(%v) = %v, want %v", c.now, got, c.want)
		}
	}
}

func TestNightlyRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(dir, "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	backups := filepath.Join(dir, "backups")
	os.MkdirAll(backups, 0o755)
	for _, old := range []string{"theatarr-20260101-030000", "theatarr-20260102-030000", "theatarr-20260103-030000"} {
		for _, sfx := range []string{".db", ".policy.tar", ".manifest.json"} {
			os.WriteFile(filepath.Join(backups, old+sfx), nil, 0o644)
		}
	}
	pol, err := policy.Load(filepath.Join("..", "..", "examples", "policy"))
	if err != nil {
		t.Fatal(err)
	}
	q := jobs.NewQueue(st)
	n := &nightly{st: st, q: q, pol: pol, log: slog.New(slog.NewTextHandler(io.Discard, nil)), dir: backups, keep: 2,
		hour: 3}
	if err := n.run(ctx); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(backups, "theatarr-*.db"))
	if len(files) != 2 || filepath.Base(files[0]) != "theatarr-20260103-030000.db" {
		t.Errorf("backups after prune: %v", files)
	}
	// Each set is three files; pruning took the old sets' sidecars too.
	all, _ := filepath.Glob(filepath.Join(backups, "theatarr-*"))
	if len(all) != 6 {
		t.Errorf("files after prune: %v", all)
	}
	m, err := readManifest(strings.TrimSuffix(files[1], ".db"))
	if err != nil || m.Policy.Revision != pol.Revision || len(m.Policy.Files) == 0 {
		t.Fatalf("manifest %+v %v", m, err)
	}
	b, err := store.Open(ctx, files[1], store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if cur, target, err := b.SchemaVersion(ctx); err != nil || cur != target {
		t.Errorf("backup schema %d/%d %v", cur, target, err)
	}
}

// A nightly run that fails on every try records its error and the next night on its task row; the retention task
// still starts after the last try.
func TestNightlyFailureStillSchedulesNext(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"backup", "job prune"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(dir, "theatarr.db")), store.Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if _, err := st.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			tb := tasks.New(ctx, st, nil)
			tb.Backoff = func(int) time.Duration { return time.Millisecond }
			retained := make(chan struct{}, 4)
			n := &nightly{st: st, q: jobs.NewQueue(st), tasks: tb, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
				hour: 3, retain: func(context.Context) error { retained <- struct{}{}; return nil }}
			var wantError string
			if failure == "backup" {
				n.dir = filepath.Join(dir, "not-a-directory")
				if err := os.WriteFile(n.dir, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				wantError = "not a directory"
			} else {
				if _, err := st.Writer().ExecContext(ctx, `INSERT INTO job (kind, state, payload, run_at, created_at,
					updated_at, finished_at) VALUES ('old', 'done', '{}', 0, 0, 0, 0)`); err != nil {
					t.Fatal(err)
				}
				if _, err := st.Writer().ExecContext(ctx, `CREATE TRIGGER fail_job_prune BEFORE DELETE ON job
					BEGIN SELECT RAISE(ABORT, 'prune unavailable'); END`); err != nil {
					t.Fatal(err)
				}
				wantError = "prune unavailable"
			}
			tries := 0
			task := n.task()
			run := task.Run
			task.Run = func(ctx context.Context) error { tries++; return run(ctx) }
			defer tb.Register(ctx, task, time.Time{})()
			defer tb.Register(ctx, n.retentionTask(), time.Time{})()
			n.night(ctx)
			if tries != 3 {
				t.Fatalf("tries %d", tries)
			}
			got, err := tb.List(ctx)
			if err != nil || len(got) != 2 || got[0].Key != nightlyKind || !strings.Contains(got[0].LastError, wantError) ||
				got[0].NextRun == nil || !got[0].NextRun.Equal(n.next(time.Now())) {
				t.Fatalf("task %+v %v", got, err)
			}
			select {
			case <-retained:
			case <-time.After(5 * time.Second):
				t.Fatal("retention not started after the last try")
			}
		})
	}
}

// A night whose run came due while the service was stopped runs at start.
func TestNightlyMissedRunsAtStart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tb := tasks.New(ctx, st, nil)
	n := &nightly{st: st, q: jobs.NewQueue(st), tasks: tb, log: slog.New(slog.NewTextHandler(io.Discard, nil)), hour: 3}
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO task (key, next_run) VALUES (?, ?)`,
		nightlyKind, store.Millis(time.Now().Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	lctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { n.loop(lctx); close(done) }()
	waitFor(t, "missed night run", func() bool {
		row, _ := tb.Read(ctx, nightlyKind)
		return !row.LastStarted.IsZero()
	})
	cancel()
	<-done
	if row, _ := tb.Read(ctx, nightlyKind); !row.NextRun.Equal(n.next(time.Now())) {
		t.Errorf("next run %v, want %v", row.NextRun, n.next(time.Now()))
	}
}

func TestProviderSyncSchedule(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	pol := &policy.Policy{Providers: map[string]*policy.Provider{
		"rd": {Kind: "realdebrid", Order: 1, URL: "https://rd.test/rest/1.0", APIKeyEnv: "THEATARR_TEST_SYNC_RD"},
		"ad": {Kind: "alldebrid", Order: 2, URL: "https://ad.test/v4", APIKeyEnv: "THEATARR_TEST_SYNC_AD"},
		"tb": {Kind: "torbox", Order: 3, URL: "https://tb.test/v1", APIKeyEnv: "THEATARR_TEST_SYNC_TB"},
	}}
	t.Setenv("THEATARR_TEST_SYNC_RD", "x")
	t.Setenv("THEATARR_TEST_SYNC_AD", "")
	t.Setenv("THEATARR_TEST_SYNC_TB", "y")
	var logs bytes.Buffer
	q := jobs.NewQueue(st)
	ps, err := newProviderSync(pol, st, q, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ps.keys, ",") != "rd,tb" || strings.Join(ps.syncer.Configured, ",") != "rd,ad,tb" ||
		len(ps.syncer.Providers) != 2 || ps.interval != policy.DefaultProviderSyncInterval {
		t.Errorf("sync: keys %v, configured %v, interval %v", ps.keys, ps.syncer.Configured, ps.interval)
	}
	if n := strings.Count(logs.String(), "skips providers"); n != 1 || !strings.Contains(logs.String(), "ad (THEATARR_TEST_SYNC_AD)") {
		t.Errorf("skip log: %s", logs.String())
	}

	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	at := func() time.Time {
		t.Helper()
		next, err := ps.first(ctx, now)
		if err != nil {
			t.Fatal(err)
		}
		return next
	}
	// Never synced: soon after start.
	if got := at(); !got.Equal(now.Add(ps.delay)) {
		t.Errorf("first, no runs: %v", got)
	}
	run := func(k string, ago time.Duration) {
		ms := store.Millis(now.Add(-ago))
		if _, err := st.Writer().Exec(`INSERT INTO provider_sync_run (provider, started_at, finished_at, ok)
			VALUES (?, ?, ?, 1)`, k, ms, ms); err != nil {
			t.Fatal(err)
		}
	}
	// rd ran, tb never did: still soon.
	run("rd", time.Hour)
	if got := at(); !got.Equal(now.Add(ps.delay)) {
		t.Errorf("first, tb never ran: %v", got)
	}
	// Both ran, but neither has a torrent read yet (just upgraded): still soon.
	run("tb", 2*time.Hour)
	if got := at(); !got.Equal(now.Add(ps.delay)) {
		t.Errorf("first, no torrent read: %v", got)
	}
	for _, k := range []string{"rd", "tb"} {
		if _, err := st.Writer().Exec(`INSERT INTO provider_torrent_read (provider, read_at) VALUES (?, 1)`, k); err != nil {
			t.Fatal(err)
		}
	}
	// Both ran and read: an interval after the older run.
	if got := at(); !got.Equal(now.Add(4 * time.Hour)) {
		t.Errorf("first, both ran: %v", got)
	}
	// Overdue: soon.
	run("ad", 0) // not synced here; ignored
	ps.interval = time.Hour
	if got := at(); !got.Equal(now.Add(ps.delay)) {
		t.Errorf("first, overdue: %v", got)
	}

	// The schedule runs the sync as the provider.sync task: its row records the run and the next one an interval
	// on; no job row is written. After a restore (soon) the first sync is a delay after start.
	ps.delay, ps.soon, ps.interval = 10*time.Millisecond, true, time.Hour
	ps.tasks = tasks.New(ctx, st, nil)
	var syncs atomic.Int32
	ps.sync = func(context.Context) error { syncs.Add(1); return nil }
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { ps.run(rctx); close(done) }()
	waitFor(t, "scheduled sync recorded", func() bool {
		got, _ := ps.tasks.List(ctx)
		return len(got) == 1 && got[0].LastFinished != nil && got[0].NextRun != nil &&
			got[0].NextRun.Sub(*got[0].LastStarted).Round(time.Second) == time.Hour
	})
	cancel()
	<-done
	if n := syncs.Load(); n != 1 {
		t.Errorf("syncs %d", n)
	}
	if counts, _ := q.Counts(ctx); counts[jobs.StateQueued]+counts[jobs.StateDone] != 0 {
		t.Errorf("job rows %v", counts)
	}
}

func TestNightlyStartsRetentionAfterSuccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tb := tasks.New(ctx, st, nil)
	retained := make(chan struct{}, 1)
	n := &nightly{st: st, q: jobs.NewQueue(st), tasks: tb, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		hour: time.Now().Hour(), retain: func(context.Context) error { retained <- struct{}{}; return nil }}
	defer tb.Register(ctx, n.task(), time.Time{})()
	defer tb.Register(ctx, n.retentionTask(), time.Time{})()
	n.night(ctx)
	if row, _ := tb.Read(ctx, nightlyKind); row.LastOK.IsZero() {
		t.Fatal("the night did not work")
	}
	select {
	case <-retained:
	case <-time.After(5 * time.Second):
		t.Fatal("retention not started")
	}
	waitFor(t, "retention recorded", func() bool {
		row, _ := tb.Read(ctx, retention.JobKind)
		return !row.LastStarted.IsZero()
	})
}
