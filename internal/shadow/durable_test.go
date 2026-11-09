package shadow

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

// A grab whose job can't be enqueued keeps the history cursor before it, so the next import hands it off again.
func TestGrabEnqueueErrorKeepsCursor(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	cursor := one[int](t, e.st, `SELECT history_id FROM shadow_cursor WHERE instance = 'radarr_zurg'`)
	e.zurg.add(grabEvent(2, dune, t0.Add(-time.Hour), duneRemux, hDuneRemux, "Remux-2160p", 31, 1950, "Rss"))
	allow := e.refuseJobs(t, KindGrab)
	if _, err := e.r.Import(e.ctx); err == nil {
		t.Error("an enqueue error was swallowed")
	}
	if c := one[int](t, e.st, `SELECT history_id FROM shadow_cursor WHERE instance = 'radarr_zurg'`); c != cursor {
		t.Errorf("cursor moved %d -> %d past a grab with no job", cursor, c)
	}
	allow()
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM job WHERE dedupe_key = 'shadow.grab:radarr_zurg:2'`); n != 1 {
		t.Errorf("grab jobs %d", n)
	}
}

// A deferred grab whose next job can't be enqueued fails its run (so the job retries) instead of leaving a pending
// row with no job; a pending row left with no job by an older run is found and enqueued again by the next import.
func TestDeferredGrabNeverStranded(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.r.set.SearchesPerHour = 0
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.zurg.add(grabEvent(2, dune, t0.Add(-time.Hour), duneRemux, hDuneRemux, "Remux-2160p", 31, 1950, "Rss"))
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	allow := e.refuseJobs(t, KindGrab)
	if _, err := e.r.grab(e.ctx, grabPayload{Instance: "radarr_zurg", HistoryID: 2}, false); err == nil {
		t.Error("the deferral's enqueue error was swallowed")
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM shadow_grab WHERE history_id = 2 AND state = 'pending'`); n != 0 {
		t.Error("a pending row was stored without its job")
	}
	allow()
	// An older run's stranded pending row (its job lost): the next import enqueues it again.
	if _, err := e.st.Writer().Exec(`INSERT INTO shadow_grab (instance, history_id, title_id, version_key, grabbed_at,
		release_title, release_source, arr_indexer, arr_quality, state, created_at, updated_at)
		SELECT 'radarr_zurg', 2, title_id, 'movie-remux', 0, 'x', 'Rss', '', '', 'pending', 0, 0 FROM version
		WHERE policy_key = 'movie-remux' LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer().Exec(`DELETE FROM job WHERE kind = ?`, KindGrab); err != nil {
		t.Fatal(err)
	}
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM job WHERE kind = ? AND state = 'queued'
		AND json_extract(payload, '$.history_id') = 2`, KindGrab); n != 1 {
		t.Errorf("stranded pending grab not re-enqueued: %d jobs", n)
	}
}

// RSS items are marked seen only once their decisions are durable: when routing the active version's picks fails,
// the next read decides and routes the same items again.
func TestRSSSeenOnlyAfterRouting(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	_, _, _ = e.withRouter(t, "movie-web")
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
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
	if rs, err = e.r.RSS(e.ctx); err != nil || rs.New != 1 || rs.Routed != 1 || rs.Picks != 0 {
		t.Errorf("third read %+v %v", rs, err)
	}
}
