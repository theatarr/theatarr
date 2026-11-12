package rss

import (
	"testing"
	"time"
)

// refuseJobs makes every enqueue of kind fail until the returned func is called.
func (e *env) refuseJobs(t *testing.T, kind string) func() {
	t.Helper()
	if _, err := e.st.Writer().Exec(`CREATE TRIGGER refuse_jobs BEFORE INSERT ON job WHEN NEW.kind = '` + kind + `'
		BEGIN SELECT RAISE(FAIL, 'enqueue refused'); END`); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := e.st.Writer().Exec(`DROP TRIGGER refuse_jobs`); err != nil {
			t.Fatal(err)
		}
	}
}

// RSS items are marked seen only once their decisions are durable: when routing the Live version's picks fails,
// the next read decides and routes the same items again.
func TestRSSSeenOnlyAfterRouting(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	_, _, _ = e.withRouter(t, "movie-web")
	e.seed(t)
	e.prow.setRSS(item{title: alienWebRel, hash: hAlienWeb, size: 20e9, pub: t0.Add(-10 * time.Minute)})
	allow := e.refuseJobs(t, "router.route")
	if _, err := e.r.RSS(e.ctx); err == nil {
		t.Fatal("routing error swallowed")
	}
	allow()
	rs, err := e.r.RSS(e.ctx)
	if err != nil || rs.New != 1 || rs.Routed != 1 {
		t.Errorf("second read %+v %v", rs, err)
	}
	// An enqueued route is not yet a grab; until the worker runs it, the item remains eligible.
	if rs, err = e.r.RSS(e.ctx); err != nil || rs.New != 1 || rs.Routed != 1 {
		t.Errorf("third read %+v %v", rs, err)
	}
}
