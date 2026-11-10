package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/retention"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
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
	// Scheduling twice for the same night makes one job.
	n.schedule(ctx, time.Now())
	n.schedule(ctx, time.Now())
	if counts, _ := q.Counts(ctx); counts[jobs.StateQueued] != 1 {
		t.Errorf("queued %v, want 1", counts)
	}
	if err := n.run(ctx, &jobs.Job{}); err != nil {
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
			q := jobs.NewQueue(st)
			clock := time.Now()
			q.SetClock(func() time.Time { return clock })
			n := &nightly{st: st, q: q, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
				hour: clock.Hour()}
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
			id, _, err := q.Enqueue(ctx, nightlyKind, nil, jobs.EnqueueOptions{MaxAttempts: 3,
				DedupeKey: nightlyKind + ":" + clock.Format("2006-01-02")})
			if err != nil {
				t.Fatal(err)
			}
			w := jobs.NewWorker(q, jobs.WorkerOptions{Logger: n.log})
			w.Handle(nightlyKind, n.run)
			for attempt := 1; attempt <= 3; attempt++ {
				if ran, err := w.RunOne(ctx); err != nil || !ran {
					t.Fatalf("attempt %d: ran %v, %v", attempt, ran, err)
				}
				in, err := q.Get(ctx, id)
				wantState := jobs.StateQueued
				if attempt == 3 {
					wantState = jobs.StateFailed
				}
				if err != nil || in.State != wantState || in.Attempts != attempt || !strings.Contains(in.LastError, wantError) {
					t.Fatalf("attempt %d: %+v, %v", attempt, in, err)
				}
				clock = in.RunAt
			}
			var nextID, runAt int64
			var maxAttempts int
			var key string
			if err := st.Reader().QueryRowContext(ctx, `SELECT id, run_at, max_attempts, dedupe_key FROM job
				WHERE kind = ? AND state = 'queued'`, nightlyKind).Scan(&nextID, &runAt, &maxAttempts, &key); err != nil {
				t.Fatal(err)
			}
			next := n.next(time.Now())
			if nextID == id || runAt != store.Millis(next) || maxAttempts != 3 || key != nightlyKind+":"+next.Format("2006-01-02") {
				t.Fatalf("next nightly: id %d, run_at %d, attempts %d, key %q", nextID, runAt, maxAttempts, key)
			}
			var queued int
			if err := st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM job WHERE kind = ? AND state = 'queued'`,
				nightlyKind).Scan(&queued); err != nil || queued != 1 {
				t.Fatalf("next nightly duplicated: %d, %v", queued, err)
			}
		})
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
	// Both ran: an interval after the older run.
	run("tb", 2*time.Hour)
	if got := at(); !got.Equal(now.Add(4 * time.Hour)) {
		t.Errorf("first, both ran: %v", got)
	}
	// Overdue: soon.
	run("ad", 0) // not synced here; ignored
	ps.interval = time.Hour
	if got := at(); !got.Equal(now.Add(ps.delay)) {
		t.Errorf("first, overdue: %v", got)
	}

	// A sync still queued (or running) is not enqueued again.
	ps.enqueue(ctx)
	ps.enqueue(ctx)
	if counts, _ := q.Counts(ctx); counts[jobs.StateQueued] != 1 {
		t.Errorf("queued %v, want 1", counts)
	}
	if !strings.Contains(logs.String(), "not enqueued again") {
		t.Errorf("dedupe log: %s", logs.String())
	}
}

func TestNightlyEnqueuesRetentionAfterSuccess(t *testing.T) {
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
	q := jobs.NewQueue(st)
	n := &nightly{st: st, q: q, log: slog.New(slog.NewTextHandler(io.Discard, nil)), hour: time.Now().Hour()}
	if err := n.run(ctx, &jobs.Job{}); err != nil {
		t.Fatal(err)
	}
	var priority int
	var key string
	if err := st.Reader().QueryRowContext(ctx, `SELECT priority, dedupe_key FROM job WHERE kind = ? AND state = 'queued'`,
		retention.JobKind).Scan(&priority, &key); err != nil {
		t.Fatal(err)
	}
	if priority != retention.Priority || key != retention.JobKind+":"+time.Now().Format("2006-01-02") {
		t.Fatalf("retention job: priority %d, key %q", priority, key)
	}
}

func TestListenAddressHint(t *testing.T) {
	for listen, want := range map[string]string{":7700": "http://<this server>:7700",
		"0.0.0.0:8080": "http://<this server>:8080", "127.0.0.1:7700": "http://127.0.0.1:7700"} {
		if got := listenAddressHint(listen); got != want {
			t.Errorf("%s: %q, want %q", listen, got, want)
		}
	}
}
