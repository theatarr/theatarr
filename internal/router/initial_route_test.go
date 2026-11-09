package router

import (
	"context"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
)

// A running initial legacy route keeps its queued download: its payload has no download_id yet
// when it opens the queued row before the account read, so reconcile leaves every queued download of a title version
// with a running route job.
func TestRunningInitialRouteKeepsItsQueuedDownload(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["movie"].Providers = []string{"ad"}
	e.exec(`INSERT INTO title_release_date (title_id, source, type, date) VALUES (1, 'fixture', 'digital', '2024-01-01')`)
	entered, resume := make(chan struct{}), make(chan struct{})
	e.pr["ad"].onList = func(ctx context.Context) {
		close(entered)
		select {
		case <-resume:
		case <-ctx.Done():
		}
	}
	q := jobs.NewQueue(e.st)
	q.SetClock(func() time.Time { return e.clock })
	w := jobs.NewWorker(q, jobs.WorkerOptions{})
	e.r.RegisterJobs(w, q)
	req := movieReq(cand(h("a"), "Movie.2024.2160p.WEB-DL-GRP"))
	req.Source = "wanted"
	job, _, err := enqueueRoute(e.ctx, q, nil, routePayload{Request: req}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := w.RunOne(e.ctx); done <- err }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("route ended before provider read: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("route did not start")
	}
	var dl int64
	var before string
	if err := e.st.Reader().QueryRow(`SELECT id, state FROM download WHERE info_hash=?`, h("a")).Scan(&dl, &before); err != nil {
		close(resume)
		t.Fatal(err)
	}
	if before != StateQueued {
		close(resume)
		t.Fatalf("fixture: download state %s", before)
	}
	rep, err := ReconcileLegacyQueue(e.ctx, e.st, q, true, e.clock)
	var after string
	readErr := e.st.Reader().QueryRow(`SELECT state FROM download WHERE id=?`, dl).Scan(&after)
	close(resume)
	if err != nil {
		t.Fatal(err)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	t.Logf("running job %d, download %d before=%s after=%s, report=%+v, adds=%d", job, dl, before, after, rep, e.writes())
	if after != StateQueued {
		t.Fatalf("running job's queued download was changed to %s", after)
	}
}
