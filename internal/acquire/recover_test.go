package acquire

import (
	"context"
	"testing"
	"time"
)

// A search interrupted by shutdown hands its job back; the retry finds the run in state searching and must search
// again and route it, not acknowledge it and leave it stuck.
func TestInterruptedSearchResumes(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	if _, _, err := e.a.Submit(e.ctx, 1, Selection{Versions: []string{"movie"}, TitleIDs: []int64{1}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(e.ctx)
	e.onSearch = func(ctx context.Context) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}
	if ran, err := e.w.RunOne(ctx); err != nil || !ran {
		t.Fatalf("first run: %v %v", ran, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE state = 'searching'`); n != 1 {
		t.Fatalf("the interrupted run is not searching: %d", n)
	}
	e.onSearch = nil
	e.drain()
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE state = 'routed'`); n != 1 {
		t.Errorf("the interrupted run was not resumed (searches %d)", len(e.searches()))
	}
	if n := len(e.rt.Adders["rd"].Adds()); n != 1 {
		t.Errorf("adds %d, want 1", n)
	}
}

// A process that dies mid-search leaves its job running with a lease that expires; the job is leased again and the
// run is resumed.
func TestCrashedSearchResumes(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	if _, _, err := e.a.Submit(e.ctx, 1, Selection{Versions: []string{"movie"}, TitleIDs: []int64{1}}); err != nil {
		t.Fatal(err)
	}
	// The dead process had leased the job (first attempt) and claimed the run.
	if _, err := e.st.Writer().ExecContext(e.ctx, `UPDATE job SET state = 'running', attempts = 1, lease_owner = 'dead',
		lease_until = ? WHERE kind = ?`, t0.UnixMilli(), JobSearch); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer().ExecContext(e.ctx, `UPDATE acquire_run SET state = 'searching', claim = 'dead'`); err != nil {
		t.Fatal(err)
	}
	e.advance(time.Hour)
	e.drain()
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE state = 'routed'`); n != 1 {
		t.Errorf("the crashed run was not resumed")
	}
}

// A run is recorded together with its search job: if the job can't be enqueued, no run is left without one.
func TestRunNeverWithoutItsJob(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	if _, err := e.st.Writer().ExecContext(e.ctx, `CREATE TRIGGER no_jobs BEFORE INSERT ON job
		BEGIN SELECT RAISE(FAIL, 'enqueue refused'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.a.Submit(e.ctx, 1, Selection{Versions: []string{"movie"}, TitleIDs: []int64{1}}); err == nil {
		t.Fatal("submit succeeded without a job")
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run r WHERE NOT EXISTS (SELECT 1 FROM job j
		WHERE j.dedupe_key = 'acquire.search/' || r.id)`); n != 0 {
		t.Errorf("%d runs without a search job", n)
	}
}

// A run whose search finished routes exactly once: the route job and the routed state commit together, so a
// stale claim (a run leased again elsewhere) can't route it a second time.
func TestStaleClaimDoesNotRoute(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	if _, _, err := e.a.Submit(e.ctx, 1, Selection{Versions: []string{"movie"}, TitleIDs: []int64{1}}); err != nil {
		t.Fatal(err)
	}
	// Another lease took the run over while this search ran.
	e.onSearch = func(context.Context) error {
		_, err := e.st.Writer().ExecContext(e.ctx, `UPDATE acquire_run SET claim = 'other'`)
		return err
	}
	e.drain()
	if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = 'router.route'`); n != 0 {
		t.Errorf("a stale claim enqueued %d routes", n)
	}
}
