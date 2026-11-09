package router

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

func TestDeferredRouteReEnqueues(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	q := jobs.NewQueue(e.st)
	q.SetClock(func() time.Time { return e.clock })
	e.r.q = q
	w := jobs.NewWorker(q, jobs.WorkerOptions{})
	e.r.RegisterJobs(w, q)
	for _, k := range []string{"rd", "ad", "tb"} {
		e.exec(`INSERT INTO router_pacing (provider, backoff_until, failures, reason, updated_at) VALUES (?, ?, 1, 'x', 0)`,
			k, e.clock.Add(3*time.Minute).UnixMilli())
	}
	req := movieReq(cand(h("a"), "A"), cand(h("b"), "B"))
	p, _ := json.Marshal(routePayload{Request: req})
	if err := e.r.handleRoute(e.ctx, &jobs.Job{Kind: JobRoute, Payload: p}); err != nil {
		t.Fatal(err)
	}
	var id int64
	var payload string
	var runAt int64
	if err := e.st.Reader().QueryRow(`SELECT id, payload, run_at FROM job WHERE kind = ?`, JobRoute).
		Scan(&id, &payload, &runAt); err != nil {
		t.Fatal(err)
	}
	var next routePayload
	if err := json.Unmarshal([]byte(payload), &next); err != nil {
		t.Fatal(err)
	}
	// A wait for back-off that sent nothing is no deferral toward maxDeferrals; the wait is dated.
	if next.Deferrals != 0 || next.WaitingSince != store.Millis(e.clock) || next.DownloadID == 0 ||
		len(next.Candidates) != 2 || store.FromMillis(runAt) != e.clock.Add(3*time.Minute) {
		t.Errorf("next %+v at %v", next, store.FromMillis(runAt))
	}
	if e.writes() != 0 {
		t.Error("a paced route wrote")
	}
	// A shadow version's job records a plan and enqueues nothing.
	e.pol.Versions["movie"].Mode = policy.ModeShadow
	if err := e.r.handleRoute(e.ctx, &jobs.Job{Kind: JobRoute, Payload: p}); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = ?`, JobRoute); n != 1 {
		t.Errorf("%d route jobs", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM router_plan WHERE mode = 'shadow' AND outcome = 'deferred'`); n != 1 {
		t.Errorf("shadow plan rows: %d", n)
	}
}

func TestRuntimeRouteJobUsesCurrentRuntimeWhenItStarts(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	q := jobs.NewQueue(e.st)
	q.SetClock(func() time.Time { return e.clock })
	e.r.q = q
	w := jobs.NewWorker(q, jobs.WorkerOptions{})
	currentGeneration := int64(1)
	var observed int64
	e.ad["rd"].observe = func(ctx context.Context) { observed = settings.ContextGeneration(ctx) }
	RegisterRuntimeJobs(w, q, func(ctx context.Context) (context.Context, *Router) {
		return settings.WithGeneration(ctx, currentGeneration), e.r
	})
	req := movieReq(cand(h("a"), "A"))
	if _, _, err := q.Enqueue(e.ctx, JobRoute, routePayload{Request: req}, jobs.EnqueueOptions{}); err != nil {
		t.Fatal(err)
	}
	currentGeneration = 2
	if ok, err := w.RunOne(e.ctx); err != nil || !ok {
		t.Fatalf("route job: ran %v, %v", ok, err)
	}
	if e.writes() == 0 || e.count(`SELECT COUNT(*) FROM router_plan WHERE mode = 'disabled'`) != 0 || observed != 2 {
		t.Fatalf("route did not use the current runtime: writes=%d generation=%d", e.writes(), observed)
	}
}

func TestProviderEpochChangeRequeuesRouteBeforeAdding(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["movie"].Providers = []string{"ad"}
	e.r.settingsManaged = true
	e.exec(`INSERT INTO settings_runtime_state (id, last_published_generation, provider_epoch) VALUES (1, 1, 0)`)
	e.exec(`INSERT INTO setting_provider (key, kind, enabled, order_rank, pacing, config, created_at, updated_at)
		VALUES ('ad', 'alldebrid', 1, 1, '{}', '{}', 0, 0)`)
	beforeList, resume := make(chan struct{}), make(chan struct{})
	e.pr["ad"].beforeList = func() {
		close(beforeList)
		<-resume
	}
	q := jobs.NewQueue(e.st)
	e.r.q = q
	w := jobs.NewWorker(q, jobs.WorkerOptions{})
	e.r.RegisterJobs(w, q)
	req := movieReq(cand(h("a"), "A"))
	p, err := json.Marshal(routePayload{Request: req})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- e.r.handleRoute(e.ctx, &jobs.Job{Kind: JobRoute, Payload: p}) }()
	<-beforeList
	if n := e.count(`SELECT COUNT(*) FROM download WHERE state = 'queued'`); n != 1 {
		t.Fatalf("blocked route state count %d, want queued before add", n)
	}
	// This transaction models a provider removal save: the provider row and its epoch fence commit together.
	if err := e.st.WriteTx(e.ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(e.ctx, "DELETE FROM setting_provider WHERE key = 'ad'"); err != nil {
			return err
		}
		return settings.IncrementProviderEpoch(e.ctx, tx)
	}); err != nil {
		t.Fatal(err)
	}
	close(resume)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if e.writes() != 0 {
		t.Fatalf("old route wrote to provider %d times", e.writes())
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE state = 'queued' AND provider IS NULL AND add_attempt IS NULL`); n != 1 {
		t.Fatalf("download state count %d, want queued route untouched by old runtime", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = ? AND state = 'queued'`, JobRoute); n != 1 {
		t.Fatalf("requeued jobs %d, want 1", n)
	}
}

func TestStartupFallbackProviderEpochAllowsRoute(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.exec(`INSERT INTO settings_generation (generation, revision, created_at) VALUES (5, 'head', 0)`)
	e.exec(`INSERT INTO settings_runtime_state (id, last_published_generation, provider_epoch) VALUES (1, 4, 7)`)
	e.r.generation = 4
	e.r.settingsManaged = true
	e.r.providerEpoch = 7
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "A")))
	if err != nil || res.Outcome != OutcomeAdded || res.Provider != "rd" {
		t.Fatalf("fallback route: %+v %v", res, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE state = 'added' AND provider = 'rd'`); n != 1 {
		t.Fatalf("fallback route added rows=%d", n)
	}
}

// A router built from YAML (--settings-source yaml rollback) ignores the DB provider epoch left by earlier
// DB-managed provider changes.
func TestYAMLRouterIgnoresSettingsProviderEpoch(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.exec(`INSERT INTO settings_runtime_state (id, last_published_generation, provider_epoch) VALUES (1, 3, 1)`)
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "A")))
	if err != nil || res.Outcome != OutcomeAdded || res.Provider != "rd" {
		t.Fatalf("yaml route after DB provider change: %+v %v", res, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE state = 'added' AND provider = 'rd'`); n != 1 {
		t.Fatalf("yaml route added rows=%d", n)
	}
}

func TestBudgetSaveDoesNotAbortRouteInFlight(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.exec(`INSERT INTO settings_generation (generation, revision, created_at) VALUES (1, 'initial', 0)`)
	e.exec(`INSERT INTO settings_runtime_state (id, last_published_generation, provider_epoch) VALUES (1, 1, 0)`)
	e.r.generation = 1
	e.r.settingsManaged = true
	started, release := make(chan struct{}), make(chan struct{})
	e.ad["rd"].observe = func(context.Context) {
		close(started)
		<-release
	}
	result := make(chan error, 1)
	go func() {
		res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "A")))
		if err == nil && (res == nil || res.Outcome != OutcomeAdded) {
			err = fmt.Errorf("route outcome %v", res)
		}
		result <- err
	}()
	<-started
	e.exec(`INSERT INTO settings_generation (generation, revision, created_at) VALUES (2, 'budget', 0)`)
	e.exec(`UPDATE settings_runtime_state SET last_published_generation = 2 WHERE id = 1`)
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE state = 'added' AND provider = 'rd'`); n != 1 {
		t.Fatalf("budget reload aborted route, added rows=%d", n)
	}
}

// Migration 00014 replaces the unused download tables and goes down and up cleanly.
func TestMigration14(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "theatarr.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sub, err := fs.Sub(os.DirFS("../store"), "migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(database.DialectSQLite3, st.Writer(), sub, goose.WithDisableGlobalRegistry(true),
		goose.WithTableName(store.MigrationTable), goose.WithAllowOutofOrder(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 13); err != nil {
		t.Fatal(err)
	}
	w := st.Writer()
	if _, err := w.ExecContext(ctx, `INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'M', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ExecContext(ctx, `INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'v', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := p.UpTo(ctx, 14); err != nil {
			t.Fatal(err)
		}
		if _, err := w.ExecContext(ctx, `INSERT INTO download (title_id, version_id, version_key, info_hash,
			release_title, indexer, state, created_at, updated_at) VALUES (1, 1, 'v', 'h', 'r', 'i', 'ready', 0, 0)`); err != nil {
			t.Fatal(err)
		}
		if _, err := w.ExecContext(ctx, `UPDATE download SET state = 'bogus'`); err == nil {
			t.Error("state CHECK missing")
		}
		var fk bool
		w.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk)
		var n int
		w.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&n)
		if !fk || n != 0 {
			t.Errorf("foreign keys on %v, %d problems", fk, n)
		}
		if _, err := p.DownTo(ctx, 13); err != nil {
			t.Fatal(err)
		}
		if _, err := w.ExecContext(ctx, `SELECT release_id, client FROM download`); err != nil {
			t.Errorf("down: the old download table is back: %v", err)
		}
	}
}

// A route job that dies after its download row is queued but before its follow-up job is recorded is retried with the
// original payload (DownloadID 0). The retry must resume the stranded queued row, not report it in flight and stop.
func TestRetriedRouteResumesStrandedQueuedRow(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	q := jobs.NewQueue(e.st)
	q.SetClock(func() time.Time { return e.clock })
	w := jobs.NewWorker(q, jobs.WorkerOptions{})
	e.r.RegisterJobs(w, q)
	for _, k := range []string{"rd", "ad", "tb"} {
		e.exec(`INSERT INTO router_pacing (provider, backoff_until, failures, reason, updated_at) VALUES (?, ?, 1, 'x', 0)`,
			k, e.clock.Add(3*time.Minute).UnixMilli())
	}
	req := movieReq(cand(h("a"), "A"))
	// The first run: Route queues the row with its follow-up. A row left without one (a build before the two were
	// written together, a follow-up that ran out of attempts) is stranded: its job is dropped here.
	res, err := e.r.Route(e.ctx, req)
	if err != nil || res.Outcome != OutcomeDeferred || res.DownloadID == 0 {
		t.Fatalf("first run: %+v %v", res, err)
	}
	e.exec(`DELETE FROM job WHERE kind = ?`, JobRoute)
	e.clock = e.clock.Add(5 * time.Minute)
	p, _ := json.Marshal(routePayload{Request: req})
	if err := e.r.handleRoute(e.ctx, &jobs.Job{Kind: JobRoute, Payload: p, Attempt: 2}); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'added'`, res.DownloadID); n != 1 {
		t.Errorf("the queued row was not resumed: adds %v", e.ad["rd"].adds)
	}
	if n := e.count(`SELECT COUNT(*) FROM download`); n != 1 {
		t.Errorf("%d download rows", n)
	}

	// A queued row whose follow-up job is still pending belongs to that job: another route stays in flight.
	e2 := newEnv(t, true, policy.ModeActive)
	q2 := jobs.NewQueue(e2.st)
	q2.SetClock(func() time.Time { return e2.clock })
	e2.r.RegisterJobs(jobs.NewWorker(q2, jobs.WorkerOptions{}), q2)
	for _, k := range []string{"rd", "ad", "tb"} {
		e2.exec(`INSERT INTO router_pacing (provider, backoff_until, failures, reason, updated_at) VALUES (?, ?, 1, 'x', 0)`,
			k, e2.clock.Add(3*time.Minute).UnixMilli())
	}
	if err := e2.r.handleRoute(e2.ctx, &jobs.Job{Kind: JobRoute, Payload: p}); err != nil {
		t.Fatal(err)
	}
	res, err = e2.r.Route(e2.ctx, req)
	if err != nil || res.Outcome != OutcomeInFlight || e2.writes() != 0 {
		t.Errorf("a row with a pending follow-up was taken over: %+v %v", res, err)
	}
}

// Two route jobs can resume one queued row (a stranded row resumed while its own follow-up was being recorded). The
// one that runs second finds the row moved on: in flight, not a new download for the same hash.
func TestResumeOfMovedOnRowIsInFlight(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	for _, k := range []string{"rd", "ad", "tb"} {
		e.exec(`INSERT INTO router_pacing (provider, backoff_until, failures, reason, updated_at) VALUES (?, ?, 1, 'x', 0)`,
			k, e.clock.Add(3*time.Minute).UnixMilli())
	}
	req := movieReq(cand(h("a"), "A"))
	res, err := e.r.Route(e.ctx, req)
	if err != nil || res.Outcome != OutcomeDeferred {
		t.Fatalf("first: %+v %v", res, err)
	}
	e.clock = e.clock.Add(5 * time.Minute)
	if res2, err := e.r.Route(e.ctx, req); err != nil || res2.Outcome != OutcomeAdded || res2.DownloadID != res.DownloadID {
		t.Fatalf("resume: %+v %v", res2, err)
	}
	late := req
	late.DownloadID = res.DownloadID
	res3, err := e.r.Route(e.ctx, late)
	if err != nil || res3.Outcome != OutcomeInFlight {
		t.Errorf("late follow-up: %+v %v", res3, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download`); n != 1 {
		t.Errorf("%d download rows", n)
	}
}

// TestDeferralsCountAddsAndStagger: routes waiting on a paced provider are staggered one add spacing apart
// instead of all waking when it frees up; a wait that sent nothing does not count toward maxDeferrals, one after an
// add does; and a route gives up after maxDeferWait however it waited.
func TestDeferralsCountAddsAndStagger(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	e.pol.Versions["movie"].Providers = []string{"rd"}
	g := policy.Duration(20 * time.Second)
	e.pol.Providers["rd"].Pacing = policy.Pacing{MinGap: &g}
	gap := g.D()
	e.exec(`INSERT INTO router_pacing (provider, last_add_at, updated_at) VALUES ('rd', ?, 0)`, store.Millis(e.clock))
	var at []time.Time
	for i, hash := range []string{h("a"), h("b"), h("c")} {
		res, err := e.r.Route(e.ctx, grabReq(cand(hash, fmt.Sprintf("Movie%d.2024.2160p.WEB.H265-GRP", i))))
		if err != nil || res.Outcome != OutcomeDeferred {
			t.Fatalf("%+v %v %s", res, err, steps(res))
		}
		at = append(at, res.RetryAt)
	}
	if !at[0].Equal(e.clock.Add(gap)) || !at[1].Equal(at[0].Add(gap)) || !at[2].Equal(at[1].Add(gap)) {
		t.Fatalf("retry times %v, want %s apart from %v", at, gap, e.clock.Add(gap))
	}
	for _, p := range routeJobs(e, 0) {
		if p.Deferrals != 0 {
			t.Fatalf("a paced wait counted as a deferral: %+v", p)
		}
	}
	// A run that sent an add (refused, provider backed off) counts.
	e.exec(`DELETE FROM router_pacing`)
	e.exec(`DELETE FROM job`)
	e.ad["rd"].add = refuse(adder.CodeRateLimited)
	res, err := e.r.Route(e.ctx, grabReq(cand(h("d"), "Movie.2024.1080p.WEB.H265-GRP")))
	if err != nil || res.Outcome != OutcomeDeferred {
		t.Fatalf("%+v %v %s", res, err, steps(res))
	}
	if next := routeJobs(e, res.DownloadID); len(next) != 1 || next[0].Deferrals != 1 {
		t.Fatalf("continuation %+v, want one deferral counted", next)
	}
	// Past maxDeferWait the route gives up and ends its download.
	next := routeJobs(e, res.DownloadID)[0]
	e.clock = e.clock.Add(maxDeferWait + time.Minute)
	e.exec(`UPDATE router_pacing SET backoff_until = ?`, store.Millis(e.clock.Add(time.Minute)))
	p, _ := json.Marshal(next)
	if err := e.r.handleRoute(e.ctx, &jobs.Job{Kind: JobRoute, Payload: p}); err != nil {
		t.Fatal(err)
	}
	var state, why string
	_ = e.st.Reader().QueryRow(`SELECT state, COALESCE(error, '') FROM download WHERE id = ?`, res.DownloadID).
		Scan(&state, &why)
	if state != StateFailed || !strings.Contains(why, "giving up") {
		t.Fatalf("download %s %q, want ended after maxDeferWait", state, why)
	}
}
