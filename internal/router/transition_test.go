package router

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/store"
)

// Download transitions.

func paceAll(e *env, d time.Duration) {
	for _, k := range []string{"rd", "ad", "tb"} {
		e.exec(`INSERT INTO router_pacing (provider, backoff_until, failures, reason, updated_at) VALUES (?, ?, 1, 'x', 0)
			ON CONFLICT (provider) DO UPDATE SET backoff_until = excluded.backoff_until`, k, e.clock.Add(d).UnixMilli())
	}
}

func routeJobs(e *env, dl int64) []routePayload {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT payload FROM job WHERE kind = ? AND state IN ('queued', 'running')
		ORDER BY id`, JobRoute)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []routePayload
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		var p routePayload
		_ = json.Unmarshal([]byte(s), &p)
		if dl == 0 || p.DownloadID == dl {
			out = append(out, p)
		}
	}
	return out
}

// A queue item removed while its route waits for a provider stays removed. Dismiss cancels the route's
// continuation, and a continuation already leased finds the download ended: nothing is added and no new download
// is made.
func TestDismissCancelsDeferredRoute(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	paceAll(e, 3*time.Minute)
	req := movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"))
	p, _ := json.Marshal(routePayload{Request: req})
	if err := e.r.handleRoute(e.ctx, &jobs.Job{Kind: JobRoute, Payload: p}); err != nil {
		t.Fatal(err)
	}
	next := routeJobs(e, 0)
	if len(next) != 1 || next[0].DownloadID == 0 {
		t.Fatalf("continuation %+v", next)
	}
	dl := next[0].DownloadID
	d, err := Dismiss(e.ctx, e.st, dl, "removed in Activity", false, "", store.Millis(e.clock))
	if err != nil || !d.Ended {
		t.Fatalf("dismiss %+v %v", d, err)
	}
	if left := routeJobs(e, dl); len(left) != 0 {
		t.Errorf("the dismissed download's route is still queued: %+v", left)
	}
	// The continuation was leased before the dismissal: it runs anyway.
	e.clock = e.clock.Add(5 * time.Minute)
	np, _ := json.Marshal(next[0])
	if err := e.r.handleRoute(e.ctx, &jobs.Job{Kind: JobRoute, Payload: np}); err != nil {
		t.Fatal(err)
	}
	if e.writes() != 0 {
		t.Errorf("a dismissed download's route added: %v", e.ad["rd"].adds)
	}
	if n := e.count(`SELECT COUNT(*) FROM download`); n != 1 {
		t.Errorf("%d download rows after the dismissed route ran", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'failed'`, dl); n != 1 {
		t.Error("the dismissed download is no longer failed")
	}
}

// A download removed and blocklisted while the cached-only settle waits is not reset to queued and added on
// the next provider.
func TestDismissDuringSettleStaysEnded(t *testing.T) {
	t.Parallel()
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
	dismissed := false
	e.r.sleep = func(context.Context, time.Duration) error {
		if dismissed {
			return nil
		}
		var id int64
		if err := e.st.Reader().QueryRow(`SELECT id FROM download`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := Dismiss(e.ctx, e.st, id, "removed in Activity", true, "queue", store.Millis(e.clock)); err != nil {
			t.Fatal(err)
		}
		dismissed = true
		return nil
	}
	res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	t.Logf("%+v %v: %s", res, err, steps(res))
	if !dismissed {
		t.Fatal("no settle wait")
	}
	if len(f.ad) != 0 || len(e.ad["tb"].adds) != 0 {
		t.Errorf("the dismissed download went on to the next provider: ad %d, tb %v", len(f.ad), e.ad["tb"].adds)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE state = 'failed'`); n != 1 {
		t.Error("the dismissed download is no longer failed")
	}
	if n := e.count(`SELECT COUNT(*) FROM download`); n != 1 {
		t.Errorf("%d download rows", n)
	}
}

// A deferred route records its queued download and the continuation together, whoever called Route.
func TestDeferredRouteWritesItsContinuation(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	paceAll(e, 3*time.Minute)
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "A"), cand(h("b"), "B")))
	if err != nil || res.Outcome != OutcomeDeferred {
		t.Fatalf("%+v %v", res, err)
	}
	next := routeJobs(e, res.DownloadID)
	if len(next) != 1 || len(next[0].Candidates) != 2 || next[0].Deferrals != 0 || next[0].WaitingSince == 0 {
		t.Errorf("continuation %+v", next)
	}
}

// A queued download with no route job to resume it (a route that died between the two writes, before they
// were written together) is picked up by the poller. One whose route job failed is ended instead of retried
// forever.
func TestStrayQueuedDownloadIsResumed(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	old := store.Millis(e.clock.Add(-time.Hour))
	insert := func(id int64, hash string) {
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
			score, episodes, state, source, created_at, updated_at) VALUES (?, 1, 10, 'movie', ?, 'Movie.2024.2160p.WEB',
			'pub', 'WEBDL-2160p', 100, '[]', 'queued', 'rss', ?, ?)`, id, hash, old, old)
	}
	insert(7, h("a"))
	insert(8, h("b"))
	e.exec(`INSERT INTO job (kind, payload, dedupe_key, state, run_at, attempts, max_attempts, last_error, created_at,
		updated_at, finished_at) VALUES (?, '{}', ?, 'failed', ?, 3, 3, 'boom', ?, ?, ?)`, JobRoute,
		routeJobKey(8)+"1", old, old, old+1000, old+1000)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	got := routeJobs(e, 7)
	if len(got) != 1 || len(got[0].Candidates) != 1 || got[0].Candidates[0].Release.InfoHash != h("a") {
		t.Errorf("stray queued download not resumed: %+v", got)
	}
	if len(routeJobs(e, 8)) != 0 {
		t.Error("a download whose route failed was routed again")
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = 8 AND state = 'failed'`); n != 1 {
		t.Error("a download whose route failed stays queued")
	}
}

// AllDebrid's queued state is not downloading: a magnet still queued on AD past stalled_after stalls, even when
// a poll read it before then.
func TestADQueuedStillStalls(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, own_add, added_at, created_at, updated_at) VALUES (1, 1, 10, 'movie', ?, 'x', 'pub',
		'ad', '55', 'added', 0, ?, 0, 0)`, h("a"), store.Millis(e.clock))
	e.pr["ad"].set("55", provider.Torrent{Hash: h("a"), Status: "In Queue", State: provider.StateQueued})
	e.clock = e.clock.Add(time.Minute)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = 1 AND state = 'added'`); n != 1 {
		t.Error("a queued AllDebrid magnet left added")
	}
	// AllDebrid's own timeout (25m by default, not the router's 10m: per-provider stalled_after).
	e.clock = e.clock.Add(e.r.set.StalledAfter)
	if sum, err := e.r.Poll(e.ctx); err != nil || sum.Stalled != 0 {
		t.Errorf("queued AllDebrid magnet stalled at the router's 10m: %+v %v", sum, err)
	}
	e.clock = e.clock.Add(e.r.stallAfter("ad"))
	sum, err := e.r.Poll(e.ctx)
	if err != nil || sum.Stalled != 1 {
		t.Errorf("queued AllDebrid magnet past stalled_after: %+v %v", sum, err)
	}
}

// A download in downloading whose progress stays flat for router.downloading_timeout fails and is blocklisted;
// one that moves does not.
func TestDownloadingWithoutProgressTimesOut(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	add := func(id int64, hash, tid string) {
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
			provider_torrent_id, state, reused, progress, added_at, created_at, updated_at) VALUES (?, 1, 10, 'movie', ?,
			'x', 'pub', 'tb', ?, 'downloading', 1, 40, ?, 0, 0)`, id, hash, tid, store.Millis(e.clock))
	}
	add(1, h("a"), "t1")
	add(2, h("b"), "t2")
	e.pr["tb"].set("t1", provider.Torrent{Hash: h("a"), Status: "downloading", State: provider.StateDownloading,
		Progress: 40})
	for i := range 26 {
		e.pr["tb"].set("t2", provider.Torrent{Hash: h("b"), Status: "downloading", State: provider.StateDownloading,
			Progress: 41 + float64(i)})
		if _, err := e.r.Poll(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.clock = e.clock.Add(time.Hour)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = 1 AND state = 'failed'`); n != 1 {
		t.Error("a download with no progress for a day is still in flight")
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist WHERE download_id = 1`); n != 1 {
		t.Error("the stuck download was not blocklisted")
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = 2 AND state = 'downloading'`); n != 1 {
		t.Error("a download that progresses was failed")
	}
}

// The settle after a delete is recorded even when the route's context ends right after the delete: the download
// is queued again, not left added with the deleted torrent.
func TestUncachedDeleteRecordedAfterCancel(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.r.set.CachedOnly = true
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	e.ad["rd"].onDelete = cancel
	e.ad["rd"].add = func(m adder.Magnet, o adder.AddOptions) (*adder.Added, error) {
		return &adder.Added{ID: "rd-new", Hash: m.Hash()}, nil
	}
	e.pr["rd"].set("rd-new", provider.Torrent{Hash: h("a"), Status: "downloading", State: provider.StateDownloading})
	_, err := e.r.Route(ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	t.Logf("route: %v", err)
	if len(e.ad["rd"].deletes) != 1 {
		t.Fatalf("deletes %v", e.ad["rd"].deletes)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE state = 'queued' AND provider IS NULL
		AND provider_torrent_id IS NULL`); n != 1 {
		t.Error("the deleted add is still recorded on the download")
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE provider = 'rd' AND state = 'uncached'`); n != 1 {
		t.Error("the uncached add was not recorded")
	}
}

// stalledFake is a fake Real-Debrid adder that can delete a stalled add of ours.
type stalledFake struct{ *fakeAdder }

func (f stalledFake) DeleteOwnStalledAdd(_ context.Context, id string, proof adder.OwnAdd) error {
	if !proof.NotOnAccountBefore || !proof.NewEntry {
		return adder.ErrNotOwnAdd
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, id)
	return nil
}

// Our own Real-Debrid add that fails before any file is selected (a dangerous file, no video) is deleted with
// the stalled-delete proof; a reused torrent never is.
func TestRDOwnAddFailedBeforeSelectionDeleted(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		files []provider.File
	}{
		{"dangerous", []provider.File{{ID: "1", Path: "Movie.mkv", Bytes: 1 << 30}, {ID: "2", Path: "setup.exe"}}},
		{"no video", []provider.File{{ID: "1", Path: "Movie.nfo"}, {ID: "2", Path: "readme.txt"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			fa := stalledFake{e.ad["rd"]}
			e.r.adders["rd"] = fa
			fa.add = func(m adder.Magnet, o adder.AddOptions) (*adder.Added, error) {
				return &adder.Added{ID: "rd-new", Hash: m.Hash()}, nil
			}
			e.pr["rd"].set("rd-new", provider.Torrent{Hash: h("a"), Status: rdWaitingFiles,
				State: provider.StateQueued, Files: tc.files})
			res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
			if err != nil {
				t.Fatal(err)
			}
			if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'failed'`, res.DownloadID); n != 1 {
				t.Fatalf("not failed: %s", steps(res))
			}
			if len(fa.deletes) != 1 || fa.deletes[0] != "rd-new" {
				t.Errorf("our own failed RD add stayed: deletes %v", fa.deletes)
			}
		})
	}
}
