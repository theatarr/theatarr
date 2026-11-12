package router

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
)

// Unfinished cached-only checks (cachedcheck.go): the poller finishes what settle could not.

// withWorker is withQueue with the worker returned, to run the route jobs the poller queues.
func withWorker(e *env) *jobs.Worker {
	q := jobs.NewQueue(e.st)
	q.SetClock(func() time.Time { return e.clock })
	w := jobs.NewWorker(q, jobs.WorkerOptions{})
	e.r.RegisterJobs(w, q)
	return w
}

func cachedCheck(e *env, dl int64) string {
	e.t.Helper()
	var s string
	if err := e.st.Reader().QueryRow(`SELECT COALESCE(cached_check, '') FROM download WHERE id = ?`, dl).
		Scan(&s); err != nil {
		e.t.Fatal(err)
	}
	return s
}

// hiccupAdd routes h("a") to Real-Debrid while the first status read after the add fails: no verdict, the add
// stays with the check pending.
func hiccupAdd(t *testing.T, e *env, f *fakeDebrid) int64 {
	t.Helper()
	f.rdInfoFails = 1
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.Outcome != OutcomeAdded || res.Provider != "rd" {
		t.Fatalf("%+v %v: %s", res, err, steps(res))
	}
	if got := cachedCheck(e, res.DownloadID); got != checkPending {
		t.Fatalf("cached_check %q after an unknown verdict", got)
	}
	return res.DownloadID
}

// A failed status read in the settle leaves the check pending; the poller selects the files, sees the torrent still
// downloading past the grace, deletes our add, records the miss and routes the release again (to AllDebrid, cached
// there), with nothing blocklisted.
func TestCachedCheckHiccupResumed(t *testing.T) {
	t.Parallel()
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
	w := withWorker(e)
	f.cached["ad:"+h("a")] = true
	dl := hiccupAdd(t, e, f)
	// The first poll selects the files: RD waiting for a selection is no verdict.
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if f.deletes() != 0 || cachedCheck(e, dl) != checkPending ||
		f.count("rd.test POST /rest/1.0/torrents/selectFiles/RD1001") != 1 {
		t.Fatalf("after the selection: deletes %d, check %q, requests %v", f.deletes(), cachedCheck(e, dl), f.list())
	}
	// Within the grace after the selection: still no verdict.
	e.clock = e.clock.Add(time.Second)
	if _, err := e.r.Poll(e.ctx); err != nil || f.deletes() != 0 {
		t.Fatalf("deleted within the grace: %v %v", err, f.list())
	}
	e.clock = e.clock.Add(2 * time.Second)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if f.count("rd.test DELETE /rest/1.0/torrents/delete/RD1001") != 1 || len(f.rd) != 0 {
		t.Fatalf("our uncached add not deleted: %v", f.list())
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'queued' AND provider IS NULL
		AND cached_check IS NULL AND error LIKE 'uncached on rd;%'`, dl); n != 1 {
		t.Error("download not queued again")
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE info_hash = ? AND provider = 'rd'
		AND state = 'uncached'`, h("a")); n != 1 {
		t.Error("rd not recorded uncached")
	}
	if p, _ := loadPace(e.ctx, e.st.Reader(), "rd"); p.HourUncached != 1 {
		t.Errorf("rd uncached adds this hour %d", p.HourUncached)
	}
	if ran, err := w.RunOne(e.ctx); err != nil || !ran {
		t.Fatalf("route job: %v %v", ran, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'added' AND provider = 'ad'`, dl); n != 1 {
		t.Errorf("download not routed to ad: %s", attempts(e, dl))
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
		t.Error("blocklisted")
	}
}

// With Real-Debrid the only provider, the route job finds the release a miss everywhere: the download fails without
// a blocklist entry and the title is searched again, as an in-route miss on every provider.
func TestCachedCheckOnlyProviderSearchesAgain(t *testing.T) {
	t.Parallel()
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
	e.pol.Versions["movie"].Providers = []string{"rd"}
	w := withWorker(e)
	dl := hiccupAdd(t, e, f)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.clock = e.clock.Add(3 * time.Second)
	if _, err := e.r.Poll(e.ctx); err != nil || len(f.rd) != 0 {
		t.Fatalf("%v; left on rd %d", err, len(f.rd))
	}
	if ran, err := w.RunOne(e.ctx); err != nil || !ran {
		t.Fatalf("route job: %v %v", ran, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'failed'
		AND error = 'miss on every provider'`, dl); n != 1 {
		t.Errorf("download not failed as a miss: %s", attempts(e, dl))
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
		t.Error("blocklisted")
	}
	if got := researchJobs(e); len(got) != 1 || got[0].Reason != ReasonExhausted || got[0].TitleID != 1 {
		t.Errorf("research jobs %+v", got)
	}
	if f.count("rd.test POST /rest/1.0/torrents/addMagnet") != 1 {
		t.Errorf("added to rd again: %v", f.list())
	}
}

// The stop switch closes during the settle's grace: the uncached verdict stands, nothing is deleted and the delete is
// owed. Polls while the gate is closed delete nothing; once it reopens, the poller deletes and routes again.
func TestCachedCheckStopSwitch(t *testing.T) {
	t.Parallel()
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
	withQueue(e)
	e.r.sleep = func(context.Context, time.Duration) error {
		e.r.set.Enabled = false // Pause turned on mid-check
		return nil
	}
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.Provider != "rd" || !strings.Contains(steps(res), codeUncachedKeptShared) {
		t.Fatalf("%+v %v: %s", res, err, steps(res))
	}
	dl := res.DownloadID
	if f.deletes() != 0 || cachedCheck(e, dl) != checkUncached {
		t.Fatalf("deletes %d, check %q", f.deletes(), cachedCheck(e, dl))
	}
	for range 2 {
		e.clock = e.clock.Add(time.Minute)
		if _, err := e.r.Poll(e.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if f.deletes() != 0 || cachedCheck(e, dl) != checkUncached || len(f.rd) != 1 {
		t.Fatalf("gated poll: deletes %d, check %q", f.deletes(), cachedCheck(e, dl))
	}
	e.r.set.Enabled = true
	e.clock = e.clock.Add(time.Minute)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if f.count("rd.test DELETE /rest/1.0/torrents/delete/RD1001") != 1 || len(f.rd) != 0 {
		t.Fatalf("not deleted after the gate reopened: %v", f.list())
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'queued' AND cached_check IS NULL`,
		dl); n != 1 {
		t.Error("not routed again")
	}
	if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = ?`, JobRoute); n != 1 {
		t.Errorf("%d route jobs", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
		t.Error("blocklisted")
	}
}

// A pending check whose torrent finishes: the check is dropped, nothing deleted, and the download is ready.
func TestCachedCheckPendingDownloaded(t *testing.T) {
	t.Parallel()
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
	withQueue(e)
	dl := hiccupAdd(t, e, f)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.rd["RD1001"].status = "downloaded"
	f.mu.Unlock()
	e.clock = e.clock.Add(time.Minute)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if f.deletes() != 0 {
		t.Errorf("deleted: %v", f.list())
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'ready' AND cached_check IS NULL`,
		dl); n != 1 {
		t.Error("not ready with the check dropped")
	}
}

// insertChecked inserts a download on pk's torrent tid, downloading, with an unfinished check.
func insertChecked(e *env, id int64, pk, tid, hash, check string, reused int, ownAdd any) {
	e.t.Helper()
	at := e.clock.Add(-time.Hour).UnixMilli()
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, files_selected, selected_at, reused, own_add, cached_check, added_at, progress,
		progress_at, created_at, updated_at) VALUES (?, 1, 10, 'movie', ?, 'x', 'pub', ?, ?, 'downloading', 1, ?, ?, ?,
		?, ?, 3, ?, 0, 0)`, id, hash, pk, tid, at, reused, ownAdd, check, at, e.clock.UnixMilli())
}

// The poller's delete keeps the stalled delete's ownership guard: a reused torrent, an add not proven ours, an id
// another download knows, and anything on AllDebrid (even with alldebrid_delete_uncached on) are kept; the check is
// dropped with the reason on the row.
func TestCachedCheckOwnershipKept(t *testing.T) {
	t.Parallel()
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
	withQueue(e)
	e.r.set.AllDebridDeleteUncached = true
	f.rd["RDR"] = &fakeRD{hash: h("a"), status: "downloading", selected: true}
	insertChecked(e, 1, "rd", "RDR", h("a"), checkUncached, 1, nil)
	f.rd["RDZ"] = &fakeRD{hash: h("b"), status: "downloading", selected: true}
	insertChecked(e, 2, "rd", "RDZ", h("b"), checkPending, 0, 0)
	f.rd["RDS"] = &fakeRD{hash: h("c"), status: "downloading", selected: true}
	insertChecked(e, 3, "rd", "RDS", h("c"), checkUncached, 0, 1)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, created_at, updated_at) VALUES (4, 2, 20, 'series', ?, 'y', 'pub', 'rd', 'RDS',
		'imported', 0, 0)`, h("c"))
	f.ad[55] = &fakeAD{hash: h("d"), code: 1}
	insertChecked(e, 5, "ad", "55", h("d"), checkPending, 0, 1)
	f.ad[56] = &fakeAD{hash: h("e"), code: 1}
	insertChecked(e, 6, "ad", "56", h("e"), checkUncached, 0, 1)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if f.deletes() != 0 || len(f.rd) != 3 || len(f.ad) != 2 {
		t.Errorf("deleted: %v", f.list())
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id IN (1, 2, 3, 5, 6) AND state = 'downloading'
		AND cached_check IS NULL AND error LIKE 'uncached add kept: %; review it'`); n != 5 {
		t.Errorf("%d kept with the reason", n)
	}
	var why string
	_ = e.st.Reader().QueryRow(`SELECT error FROM download WHERE id = 6`).Scan(&why)
	if !strings.Contains(why, "only a Real-Debrid add of ours") {
		t.Errorf("ad reason %q", why)
	}
}

// cached_only turned off: the check is dropped and nothing is deleted.
func TestCachedCheckCachedOnlyOff(t *testing.T) {
	t.Parallel()
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
	withQueue(e)
	e.r.set.CachedOnly = false
	f.rd["RD1"] = &fakeRD{hash: h("a"), status: "downloading", selected: true}
	insertChecked(e, 1, "rd", "RD1", h("a"), checkUncached, 0, 1)
	f.rd["RD2"] = &fakeRD{hash: h("b"), status: "downloading", selected: true}
	insertChecked(e, 2, "rd", "RD2", h("b"), checkPending, 0, 1)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if f.deletes() != 0 || len(f.rd) != 2 {
		t.Errorf("deleted: %v", f.list())
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE state = 'downloading' AND cached_check IS NULL
		AND error IS NULL`); n != 2 {
		t.Errorf("%d checks dropped", n)
	}
}

// The router status counts the unfinished checks.
func TestCachedCheckStatus(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	insertChecked(e, 1, "rd", "RD1", h("a"), checkPending, 0, 1)
	insertChecked(e, 2, "rd", "RD2", h("b"), checkUncached, 0, 1)
	e.exec(`UPDATE download SET state = 'failed' WHERE id = 2`)
	s, err := ReadStatus(e.ctx, e.st, e.pol, nil, e.clock, 5)
	if err != nil || s.UnfinishedChecks != 1 {
		t.Errorf("%v: %d unfinished", err, s.UnfinishedChecks)
	}
}

// Our selection failed on our side but reached Real-Debrid: the read showing it downloading records the selection,
// and the grace counts from then, not from the add.
func TestCachedCheckInferredSelection(t *testing.T) {
	t.Parallel()
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
	withQueue(e)
	f.rd["RD1"] = &fakeRD{hash: h("a"), status: "downloading", selected: true}
	insertChecked(e, 1, "rd", "RD1", h("a"), checkPending, 0, 1)
	e.exec(`UPDATE download SET state = 'added', files_selected = 0, selected_at = NULL, progress_at = NULL,
		error = 'selecting files: timeout' WHERE id = 1`)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if f.deletes() != 0 || cachedCheck(e, 1) != checkPending {
		t.Fatalf("decided before the inferred selection's grace: deletes %d, check %q", f.deletes(), cachedCheck(e, 1))
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = 1 AND files_selected = 1 AND selected_at = ?`,
		e.clock.UnixMilli()); n != 1 {
		t.Fatal("selection not inferred")
	}
	e.clock = e.clock.Add(3 * time.Second)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if f.count("rd.test DELETE /rest/1.0/torrents/delete/RD1") != 1 {
		t.Errorf("not deleted after the grace: %v", f.list())
	}
}

// A resumed check's uncached verdict uses the route's content-aware TTL: 1 h for a new movie, miss_ttl for an old one.
func TestCachedCheckResumedFreshTTL(t *testing.T) {
	t.Parallel()
	for _, q := range []struct {
		name string
		date time.Duration // the movie's digital date before now; 0: none
		want time.Duration
	}{{"new", 24 * time.Hour, time.Hour}, {"old", 0, 6 * time.Hour}} {
		t.Run(q.name, func(t *testing.T) {
			e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
			if q.date != 0 {
				e.exec(`INSERT INTO title_release_date (title_id, source, type, date) VALUES (1, 'fixture', 'digital', ?)`,
					e.clock.Add(-q.date).UTC().Format("2006-01-02"))
			}
			dl := hiccupAdd(t, e, f)
			for range 2 {
				if _, err := e.r.Poll(e.ctx); err != nil {
					t.Fatal(err)
				}
				e.clock = e.clock.Add(3 * time.Second)
			}
			if _, err := e.r.Poll(e.ctx); err != nil {
				t.Fatal(err)
			}
			var exp, checked int64
			if err := e.st.Reader().QueryRow(`SELECT expires_at, checked_at FROM provider_cache WHERE info_hash = ?
				AND provider = 'rd' AND state = 'uncached'`, h("a")).Scan(&exp, &checked); err != nil {
				t.Fatalf("download %d: no rd verdict: %v", dl, err)
			}
			if got := time.Duration(exp-checked) * time.Millisecond; got != q.want {
				t.Errorf("rd uncached for %v, want %v", got, q.want)
			}
		})
	}
}
