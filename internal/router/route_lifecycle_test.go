package router

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/store"
)

// Router and download correctness.

// A queue item removed (Dismiss) while its first route's add is out. The add's record finds the download ended:
// the route stops there (no job retry), so the release is not added again under a new download row.
func TestDismissDuringAddIsNotAddedAgainByTheRetry(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	dismissed := false
	e.ad["rd"].observe = func(context.Context) {
		if dismissed {
			return
		}
		var id int64
		if err := e.st.Reader().QueryRow(`SELECT id FROM download`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := Dismiss(e.ctx, e.st, id, "removed in Activity", false, "", store.Millis(e.clock)); err != nil {
			t.Fatal(err)
		}
		dismissed = true
	}
	p, _ := json.Marshal(routePayload{Request: movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"))})
	if err := e.r.handleRoute(e.ctx, &jobs.Job{Kind: JobRoute, Payload: p}); err != nil {
		// The job worker would retry a failed handler with the same payload.
		e.clock = e.clock.Add(10 * time.Second)
		err2 := e.r.handleRoute(e.ctx, &jobs.Job{Kind: JobRoute, Payload: p})
		t.Errorf("the route failed (%v), so the job retries it (retry: %v)", err, err2)
	}
	if n := len(e.ad["rd"].adds); n != 1 {
		t.Errorf("the removed release was added %d times", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM download`); n != 1 {
		t.Errorf("%d download rows: the retry opened a new download for the removed release", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE state = 'failed'`); n != 1 {
		t.Errorf("the removed download did not stay failed")
	}
}

// A route job that carries a download id that left queued (here: imported) and is cancelled by freshness
// leaves that download alone.
func TestCancelledRouteDoesNotFailAnImportedDownload(t *testing.T) {
	t.Parallel()
	for _, state := range []string{StateImported, StateReady, StateAdded} {
		t.Run(state, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			withQueue(e)
			e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
				score, episodes, state, source, created_at, updated_at, imported_at) VALUES (7, 1, 10, 'movie', ?,
				'Movie.2024.2160p.WEB.H265-GRP', 'pub', 'WEBDL-2160p', 100, '[]', ?, 'rss', 0, 0, 0)`, h("a"), state)
			e.exec(`UPDATE title SET monitored = 0 WHERE id = 1`)
			req := movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"))
			req.Source, req.DownloadID = "rss", 7
			p, _ := json.Marshal(routePayload{Request: req})
			if err := e.r.handleRoute(e.ctx, &jobs.Job{Kind: JobRoute, Payload: p}); err != nil {
				t.Fatal(err)
			}
			var got string
			if err := e.st.Reader().QueryRow(`SELECT state FROM download WHERE id = 7`).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != state {
				t.Errorf("a %s download was moved to %s by a cancelled route", state, got)
			}
		})
	}
}

// A queued download of a version whose grab switch was turned off ends at its next route, as a switch to shadow
// ends it: it is not resumed every ten minutes and grabbed when grab comes back on.
func TestGrabOffQueuedDownloadIsEnded(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	paceAll(e, 3*time.Minute)
	p, _ := json.Marshal(routePayload{Request: movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"))})
	if err := e.r.handleRoute(e.ctx, &jobs.Job{Kind: JobRoute, Payload: p}); err != nil {
		t.Fatal(err)
	}
	next := routeJobs(e, 0)
	if len(next) != 1 {
		t.Fatalf("continuation %+v", next)
	}
	dl := next[0].DownloadID
	e.pol.Versions["movie"].Grab = false
	resumed := 0
	for range 6 {
		e.exec(`UPDATE job SET state = 'done' WHERE kind = ? AND state = 'queued'`, JobRoute)
		e.clock = e.clock.Add(11 * time.Minute)
		sum, err := e.r.Poll(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		resumed += sum.Resumed
		for _, j := range routeJobs(e, dl) {
			b, _ := json.Marshal(j)
			if err := e.r.handleRoute(e.ctx, &jobs.Job{Kind: JobRoute, Payload: b}); err != nil {
				t.Fatal(err)
			}
		}
	}
	state := ""
	_ = e.st.Reader().QueryRow(`SELECT state FROM download WHERE id = ?`, dl).Scan(&state)
	if state != StateFailed || resumed > 1 {
		t.Errorf("after an hour with grab off: download %s, resumed %d times", state, resumed)
	}
	if e.writes() != 0 {
		t.Errorf("%d provider writes with grab off", e.writes())
	}
}

// The freshness check judges a native profile with its format thresholds (cutoff score, minimum upgrade step):
// a same-quality file scoring 1000 under a 10000 cutoff is upgraded by a release scoring 2000, as the decision
// engine says, and not by one only 50 better when the profile needs 100.
func TestFreshnessUsesProfileFormatThresholds(t *testing.T) {
	t.Parallel()
	step := 100
	for _, tc := range []struct {
		name  string
		score int
		want  Outcome
	}{
		{"below cutoff score", 2000, OutcomeAdded},
		{"under the upgrade step", 1050, OutcomeCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			freshMovie(e)
			pr := e.pol.Profiles["fresh"]
			pr.CutoffScore, pr.MinUpgradeStep = 10000, &step
			e.link(1, 10, 0, "WEBDL-2160p", 1000, 1000)
			c := cand(h("a"), "Movie.2024.2160p.WEB-DL-GRP")
			c.Score = tc.score
			res, err := e.r.Route(e.ctx, freshReq(c, "wanted"))
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != tc.want {
				t.Errorf("outcome %s, want %s: %s", res.Outcome, tc.want, steps(res))
			}
		})
	}
}

// A resumed queued pack whose freshness check dropped an episode records the narrowed candidate on its download
// before the add, so the library imports only the episodes still wanted.
func TestResumedPackPersistsNarrowedEpisodes(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	e.exec(`INSERT INTO episode (id, title_id, season, number, created_at, updated_at)
		VALUES (101, 2, 1, 1, 0, 0), (102, 2, 1, 2, 0, 0)`)
	paceAll(e, 3*time.Minute)
	pack := cand(h("a"), "Show.S01.2160p.WEB-DL-GRP")
	pack.Episodes = []int64{101, 102}
	req := Request{TitleID: 2, VersionID: 20, Version: "series", Source: "rss", Candidates: []Candidate{pack}}
	p, _ := json.Marshal(routePayload{Request: req})
	if err := e.r.handleRoute(e.ctx, &jobs.Job{Kind: JobRoute, Payload: p}); err != nil {
		t.Fatal(err)
	}
	next := routeJobs(e, 0)
	if len(next) != 1 || next[0].DownloadID == 0 {
		t.Fatalf("continuation %+v", next)
	}
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at)
		VALUES (20, 101, 0, 'wanted', 0)`)
	e.clock = e.clock.Add(4 * time.Minute)
	res, err := e.r.Route(e.ctx, next[0].Request)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || res.DownloadID != next[0].DownloadID {
		t.Fatalf("resume %+v: %s", res, steps(res))
	}
	var eps string
	if err := e.st.Reader().QueryRow(`SELECT episodes FROM download WHERE id = ?`, res.DownloadID).Scan(&eps); err != nil {
		t.Fatal(err)
	}
	if eps != "[102]" {
		t.Errorf("the resumed download still records episodes %s", eps)
	}
}

// A route decided again under a new policy revision tries its candidates in the order the current decisions
// rank them, not the order they were queued in.
func TestRedecisionRanksCandidatesAgain(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	freshMovie(e)
	e.exec(`UPDATE title SET year = 2024 WHERE id = 1`)
	e.pol.Revision = "rev2"
	eng, err := decide.New(&policy.Policy{Profiles: e.pol.Profiles, Indexers: e.pol.Indexers,
		Versions: map[string]*policy.VersionPolicy{"movie": e.pol.Versions["movie"]}}, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.r.SetDecider(eng.Decide, eng.DecideSearch)
	a := cand(h("a"), "Movie.2024.1080p.WEB-DL.DDP5.1.H.264-GRP")
	a.Quality, a.Release.Seeders = "WEBDL-1080p", 50
	b := cand(h("b"), "Movie.2024.2160p.WEB-DL.DDP5.1.H.265-GRP")
	b.Quality, b.Release.Seeders = "WEBDL-2160p", 50
	// Queued under rev1 when A ranked first.
	req := freshReq(a, "rss")
	req.Candidates = append(req.Candidates, b)
	req.DecisionRev = "rev1"
	res, err := e.r.Route(e.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || res.Hash != h("b") {
		t.Errorf("route %+v: %s; want the 2160p release first", res, steps(res))
	}
}

// The ready deadline counts only time the mount was healthy: a download that became ready during a long outage
// is not failed the moment the mount comes back.
func TestReadyDeadlineSkipsMountOutage(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	start := e.clock
	down := errors.New("mount down")
	e.r.SetMountCheck(func(context.Context) error { return down })
	if _, err := e.r.Poll(e.ctx); err != nil { // the outage is seen
		t.Fatal(err)
	}
	e.clock = start.Add(time.Hour)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
		state, ready_at, created_at, updated_at) VALUES (1, 1, 10, 'movie', ?, 'Movie', 'pub', 'ready', ?, 0, 0)`,
		h("a"), store.Millis(e.clock))
	for i := range 8 {
		e.clock = start.Add(time.Hour + time.Duration(i+1)*time.Hour)
		if sum, err := e.r.Poll(e.ctx); err != nil || sum.Failed != 0 {
			t.Fatalf("poll with the mount down: %+v, %v", sum, err)
		}
	}
	down = nil
	e.clock = e.clock.Add(time.Minute)
	if sum, err := e.r.Poll(e.ctx); err != nil || sum.Failed != 0 {
		t.Fatalf("the download failed right after the mount came back: %+v, %v", sum, err)
	}
	e.clock = e.clock.Add(e.r.set.ReadyTimeout)
	if sum, err := e.r.Poll(e.ctx); err != nil || sum.Failed != 1 {
		t.Fatalf("a ready_timeout of healthy mount later: %+v, %v", sum, err)
	}
}

// Grab turned off in the published runtime stops a route of an older runtime before its add.
func TestGrabOffStopsRunningRoute(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	pol := testPolicy(true, policy.ModeActive)
	pol.Versions["movie"].Grab = false
	cur, err := New(Config{Store: e.st, Policy: pol, Previous: e.r,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return e.clock }})
	if err != nil {
		t.Fatal(err)
	}
	e.r.SetCurrentRuntime(func() *Router { return cur })
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if e.writes() != 0 {
		t.Fatalf("added after grab was turned off: %+v %v", res, err)
	}
	if err != nil || res.Outcome != OutcomeCancelled {
		t.Errorf("route %+v, %v", res, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE state = 'queued'`); n != 0 {
		t.Errorf("%d downloads left queued", n)
	}
}

// A continuation that finds its add still unsettled (recUnknown) queues the next one, so the route resumes the
// download once the account can tell, instead of the poller failing it.
func TestUnsettledContinuationQueuesAnother(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	e.ad["rd"].add = func(m adder.Magnet, _ adder.AddOptions) (*adder.Added, error) {
		return nil, &adder.Error{Provider: "rd", Code: adder.CodeUnavailable, Status: 502}
	}
	p, _ := json.Marshal(routePayload{Request: movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"))})
	if err := e.r.handleRoute(e.ctx, &jobs.Job{Kind: JobRoute, Payload: p}); err != nil {
		t.Fatal(err)
	}
	next := routeJobs(e, 0)
	if len(next) != 1 || next[0].DownloadID == 0 {
		t.Fatalf("continuation %+v", next)
	}
	dl := next[0].DownloadID
	// The continuation runs (its job is running, then done) while the account cannot be read.
	e.exec(`UPDATE job SET state = 'done' WHERE kind = ?`, JobRoute)
	e.pr["rd"].listErr = errors.New("rd is down")
	e.clock = e.clock.Add(addSettle + time.Second)
	b, _ := json.Marshal(next[0])
	if err := e.r.handleRoute(e.ctx, &jobs.Job{Kind: JobRoute, Payload: b}); err != nil {
		t.Fatal(err)
	}
	if got := routeJobs(e, dl); len(got) != 1 {
		t.Fatalf("no route job resumes the unsettled download: %+v", got)
	}
	// The poller leaves it to that route; once the account answers, the route resumes the download.
	e.clock = e.clock.Add(10 * time.Minute)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if state, _, _, _, _ := e.download(dl); state != StateAdding {
		t.Fatalf("download %s, want adding until its route settles it", state)
	}
	e.pr["rd"].listErr = nil
	e.ad["rd"].add = nil
	res, err := e.r.Route(e.ctx, routeJobs(e, dl)[0].Request)
	if err != nil || res.Outcome != OutcomeAdded || res.DownloadID != dl {
		t.Errorf("resume %+v %v: %s", res, err, steps(res))
	}
}

// A poll pass reads providers without the router lock, so a route is not blocked behind slow status reads, and
// a pass ends at its deadline, leaving the rest for the next one.
func TestPollReleasesLockAndIsBounded(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	for i, id := range []string{"t1", "t2"} {
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
			provider_torrent_id, state, added_at, created_at, updated_at) VALUES (?, 1, 10, 'movie', ?, 'Movie', 'pub', 'rd',
			?, 'added', ?, 0, 0)`, i+1, h(string(rune('a'+i))), id, store.Millis(e.clock))
		e.pr["rd"].set(id, provider.Torrent{Hash: h(string(rune('a' + i))), State: provider.StateDownloading,
			Status: "downloading"})
	}
	free, reads := false, 0
	old := pollPassLimit
	pollPassLimit = 100 * time.Millisecond
	t.Cleanup(func() { pollPassLimit = old })
	e.pr["rd"].read = func(ctx context.Context, _ string) {
		reads++
		if e.r.mu.TryLock() {
			free = true
			e.r.mu.Unlock()
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	started := time.Now()
	sum, err := e.r.Poll(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !free {
		t.Error("the poll held the router lock across a provider read")
	}
	if took := time.Since(started); took > time.Second || reads != 1 {
		t.Errorf("the pass took %s and read %d torrents: no pass deadline (%+v)", took, reads, sum)
	}
}

// With the decision engine's compiled profiles (SetProfiles), the freshness check judges with the engine's
// profile, not one rebuilt from the policy.
func TestFreshnessUsesEngineProfile(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	freshMovie(e)
	engPol := &policy.Policy{Profiles: map[string]*policy.Profile{"fresh": {
		Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}, {Name: "WEBDL-1080p"}}, Cutoff: "WEBDL-2160p",
		CutoffScore: 10000}}, Indexers: e.pol.Indexers,
		Versions: map[string]*policy.VersionPolicy{"movie": e.pol.Versions["movie"]}}
	eng, err := decide.New(engPol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.r.SetProfiles(eng.Profile)
	e.link(1, 10, 0, "WEBDL-2160p", 1000, 1000)
	c := cand(h("a"), "Movie.2024.2160p.WEB-DL-GRP")
	c.Score = 2000
	res, err := e.r.Route(e.ctx, freshReq(c, "wanted"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded {
		t.Errorf("outcome %s: %s", res.Outcome, steps(res))
	}
}

// A resumed route whose re-ranked candidates place another release first ends the queued download it resumed,
// so the stray sweep does not resume it every ten minutes.
func TestRedecisionEndsSupersededQueuedDownload(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	freshMovie(e)
	e.exec(`UPDATE title SET year = 2024 WHERE id = 1`)
	e.pol.Revision = "rev1"
	paceAll(e, 3*time.Minute)
	a := cand(h("a"), "Movie.2024.1080p.WEB-DL.DDP5.1.H.264-GRP")
	a.Quality, a.Release.Seeders = "WEBDL-1080p", 50
	b := cand(h("b"), "Movie.2024.2160p.WEB-DL.DDP5.1.H.265-GRP")
	b.Quality, b.Release.Seeders = "WEBDL-2160p", 50
	req := freshReq(a, "rss")
	req.Candidates = append(req.Candidates, b)
	p, _ := json.Marshal(routePayload{Request: req})
	if err := e.r.handleRoute(e.ctx, &jobs.Job{Kind: JobRoute, Payload: p}); err != nil {
		t.Fatal(err)
	}
	next := routeJobs(e, 0)
	if len(next) != 1 || next[0].DownloadID == 0 || len(next[0].Candidates) != 2 {
		t.Fatalf("continuation %+v", next)
	}
	e.pol.Revision = "rev2"
	eng, err := decide.New(&policy.Policy{Profiles: e.pol.Profiles, Indexers: e.pol.Indexers,
		Versions: map[string]*policy.VersionPolicy{"movie": e.pol.Versions["movie"]}}, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.r.SetDecider(eng.Decide, eng.DecideSearch)
	e.clock = e.clock.Add(4 * time.Minute)
	res, err := e.r.Route(e.ctx, next[0].Request)
	if err != nil || res.Outcome != OutcomeAdded || res.Hash != h("b") {
		t.Fatalf("resume %+v %v: %s", res, err, steps(res))
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'failed' AND error LIKE 'route ended:%'`,
		next[0].DownloadID); n != 1 {
		t.Errorf("the superseded queued download was not ended")
	}
}

// TestPollFailedReadsRotateAndStall: a failed status read counts as an attempt, so the next pass
// reads the downloads behind it first; the failing one is backed off; and failed reads spanning stalled_after fail
// it as stalled instead of keeping it out of the stall timeout forever. Nothing is deleted: it was never seen.
func TestPollFailedReadsRotateAndStall(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	for i, id := range []string{"t1", "t2", "t3"} {
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
			provider_torrent_id, state, own_add, added_at, created_at, updated_at) VALUES (?, 1, 10, 'movie', ?, 'Movie',
			'pub', 'rd', ?, 'added', 1, ?, 0, 0)`, i+1, h(string(rune('a'+i))), id, store.Millis(e.clock))
		e.pr["rd"].set(id, provider.Torrent{Hash: h(string(rune('a' + i))), State: provider.StateDownloading,
			Status: "downloading"})
	}
	e.pr["rd"].readErr = map[string]error{"t1": errors.New("timeout")}
	var order []string
	e.pr["rd"].read = func(_ context.Context, id string) { order = append(order, id) }
	pass := func() string {
		t.Helper()
		order = nil
		if _, err := e.r.Poll(e.ctx); err != nil {
			t.Fatal(err)
		}
		return strings.Join(order, " ")
	}
	if got := pass(); got != "t1 t2 t3" {
		t.Fatalf("first pass read %s", got)
	}
	// Right after: the failing read is backed off; the others are read.
	e.clock = e.clock.Add(time.Second)
	if got := pass(); got != "t2 t3" {
		t.Fatalf("second pass read %s, want the failing read backed off", got)
	}
	// Past its back-off it is read again, first: it was tried in the first pass, t2 and t3 in the second.
	e.clock = e.clock.Add(2 * time.Minute)
	if got := pass(); got != "t1 t2 t3" {
		t.Fatalf("third pass read %s", got)
	}
	var tried int64
	_ = e.st.Reader().QueryRow(`SELECT poll_errors FROM download WHERE id = 1`).Scan(&tried)
	if tried != 2 {
		t.Fatalf("failed reads %d, want 2", tried)
	}
	// Failed reads in a row past stalled_after: stalled, blocklisted, nothing deleted.
	for range 10 {
		e.clock = e.clock.Add(e.r.stallAfter("rd"))
		pass()
	}
	var state, why string
	_ = e.st.Reader().QueryRow(`SELECT state, COALESCE(error, '') FROM download WHERE id = 1`).Scan(&state, &why)
	if state != StateFailed || !strings.Contains(why, "status reads failed") {
		t.Fatalf("download 1 %s %q, want failed as stalled", state, why)
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist WHERE info_hash = ?`, h("a")); n != 1 {
		t.Fatalf("blocklisted %d", n)
	}
	if e.deletes() != 0 {
		t.Fatalf("%d deletes", e.deletes())
	}
}
