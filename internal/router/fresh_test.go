package router

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
)

func freshMovie(e *env) {
	e.t.Helper()
	e.exec(`INSERT INTO arrview_title (view, title_id, minimum_availability, added_at, updated_at)
		VALUES ('v/movie', 1, 'announced', 0, 0)`)
	e.pol.Profiles = map[string]*policy.Profile{"fresh": {
		Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}, {Name: "WEBDL-1080p"}}, Cutoff: "WEBDL-2160p"}}
	e.pol.Versions["movie"].Profile = "fresh"
}

func freshReq(c Candidate, source string) Request {
	req := movieReq(c)
	req.Source = source
	return req
}

func TestDeferredAutomaticRouteCancelsAfterUnmonitor(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	freshMovie(e)
	q := jobs.NewQueue(e.st)
	q.SetClock(func() time.Time { return e.clock })
	e.r.RegisterJobs(jobs.NewWorker(q, jobs.WorkerOptions{}), q)
	for _, k := range []string{"rd", "ad", "tb"} {
		e.exec(`INSERT INTO router_pacing (provider, backoff_until, failures, reason, updated_at)
			VALUES (?, ?, 1, 'x', 0)`, k, e.clock.Add(time.Minute).UnixMilli())
	}
	req := freshReq(cand(h("a"), "Movie.2024.2160p.WEB-DL-GRP"), "rss")
	payload, _ := json.Marshal(routePayload{Request: req})
	if err := e.r.handleRoute(e.ctx, &jobs.Job{Kind: JobRoute, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := e.st.Reader().QueryRow(`SELECT payload FROM job WHERE kind = ? AND state = 'queued'`, JobRoute).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var resumed routePayload
	if err := json.Unmarshal([]byte(raw), &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.DownloadID == 0 || resumed.Source != "rss" {
		t.Fatalf("deferred payload %+v", resumed)
	}
	e.exec(`UPDATE title SET monitored = 0 WHERE id = 1`)
	e.clock = e.clock.Add(2 * time.Minute)
	res, err := e.r.Route(e.ctx, resumed.Request)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeCancelled || !strings.Contains(steps(res), "route_cancelled") || e.writes() != 0 {
		t.Fatalf("resume %+v, writes %d", res, e.writes())
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'failed'
		AND error LIKE 'route cancelled:%'`, resumed.DownloadID); n != 1 {
		t.Errorf("resumed download was not failed: %d", n)
	}
}

func TestAutomaticSeriesPackKeepsOnlyWantedEpisodes(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.exec(`INSERT INTO episode (id, title_id, season, number, created_at, updated_at)
		VALUES (101, 2, 1, 1, 0, 0), (102, 2, 1, 2, 0, 0)`)
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at)
		VALUES (20, 101, 0, 'wanted', 0)`)
	pack := cand(h("a"), "Show.S01.2160p.WEB-DL-GRP")
	pack.Episodes = []int64{101, 102}
	req := Request{TitleID: 2, VersionID: 20, Version: "series", Source: "rss", Candidates: []Candidate{pack}}
	res, err := e.r.Route(e.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || e.writes() != 1 {
		t.Fatalf("pack result %+v, adds %v", res, e.ad["rd"].adds)
	}
	var eps string
	if err := e.st.Reader().QueryRow(`SELECT episodes FROM download WHERE id = ?`, res.DownloadID).Scan(&eps); err != nil {
		t.Fatal(err)
	}
	if eps != "[102]" {
		t.Errorf("download episodes %s", eps)
	}

	e2 := newEnv(t, true, policy.ModeActive)
	e2.exec(`INSERT INTO episode (id, title_id, season, number, created_at, updated_at)
		VALUES (101, 2, 1, 1, 0, 0), (102, 2, 1, 2, 0, 0)`)
	e2.exec(`UPDATE episode SET monitored = 0 WHERE title_id = 2`)
	res, err = e2.r.Route(e2.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeCancelled || e2.writes() != 0 || !strings.Contains(steps(res), "route_cancelled") {
		t.Errorf("unwanted pack %+v, writes %d", res, e2.writes())
	}
}

func TestAutomaticRouteRechecksCurrentMovieFile(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, current string
		score         int
		want          Outcome
	}{
		{name: "equal", current: "WEBDL-2160p", score: 100, want: OutcomeCancelled},
		{name: "better score", current: "WEBDL-2160p", score: 200, want: OutcomeCancelled},
		{name: "worse quality", current: "WEBDL-1080p", score: 100, want: OutcomeAdded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			freshMovie(e)
			e.link(1, 10, 0, tc.current, tc.score, 1000)
			c := cand(h("a"), "Movie.2024.2160p.WEB-DL-GRP")
			c.Score = 100
			res, err := e.r.Route(e.ctx, freshReq(c, "wanted"))
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != tc.want {
				t.Fatalf("outcome %s, want %s: %s", res.Outcome, tc.want, steps(res))
			}
			if tc.want == OutcomeCancelled && e.writes() != 0 || tc.want == OutcomeAdded && e.writes() != 1 {
				t.Errorf("writes %d", e.writes())
			}
		})
	}
}

func TestDeferredAutomaticRouteCancelsWhenVersionBecomesShadow(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	freshMovie(e)
	for _, k := range []string{"rd", "ad", "tb"} {
		e.exec(`INSERT INTO router_pacing (provider, backoff_until, failures, reason, updated_at)
			VALUES (?, ?, 1, 'x', 0)`, k, e.clock.Add(time.Minute).UnixMilli())
	}
	req := freshReq(cand(h("a"), "Movie.2024.2160p.WEB-DL-GRP"), "research")
	first, err := e.r.Route(e.ctx, req)
	if err != nil || first.Outcome != OutcomeDeferred {
		t.Fatalf("first route %+v: %v", first, err)
	}
	e.pol.Versions["movie"].Mode = policy.ModeShadow
	e.clock = e.clock.Add(2 * time.Minute)
	req.DownloadID = first.DownloadID
	res, err := e.r.Route(e.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != ModeShadow || res.Outcome != OutcomeCancelled || e.writes() != 0 {
		t.Fatalf("resume %+v, writes %d", res, e.writes())
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'failed'`, first.DownloadID); n != 1 {
		t.Errorf("shadow route did not finish its queued download: %d", n)
	}
}

func TestManualGrabStillRoutesAfterUnmonitor(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.exec(`UPDATE title SET monitored = 0 WHERE id = 1`)
	e.exec(`UPDATE version SET monitored = 0 WHERE id = 10`)
	res, err := e.r.Route(e.ctx, freshReq(cand(h("a"), "Movie.2024.2160p.WEB-DL-GRP"), "ui:grab:test"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || e.writes() != 1 {
		t.Fatalf("manual grab %+v, writes %d", res, e.writes())
	}
}

// TestJoinedCommandRouteChecksFreshness: a command's first-pass route skips the freshness check only for
// a version the caller named (acquire_run.requested = 1); a version a title search widened to is checked, so an
// unmonitored copy is never grabbed by a search of another copy.
func TestJoinedCommandRouteChecksFreshness(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		requested int
		want      Outcome
	}{{1, OutcomeAdded}, {0, OutcomeCancelled}} {
		e := newEnv(t, true, policy.ModeActive)
		freshMovie(e)
		e.exec(`UPDATE version SET monitored = 0 WHERE id = 10`)
		e.exec(`INSERT INTO arrview_command (id, view, name, status, queued_at) VALUES (1, 'radarr', 'MoviesSearch', 'queued', 0)`)
		e.exec(`INSERT INTO acquire_run (id, source, command_id, title_id, version_id, version_key, target, state,
			created_at, updated_at, requested) VALUES (7, 'command', 1, 1, 10, 'movie', 'movie', 'routed', 0, 0, ?)`,
			tc.requested)
		res, err := e.r.Route(e.ctx, freshReq(cand(h("a"), "Movie.2024.2160p.WEB-DL-GRP"), "acquire:7"))
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != tc.want {
			t.Errorf("requested %d: outcome %s, want %s: %s", tc.requested, res.Outcome, tc.want, steps(res))
		}
	}
}

func TestFreshEpisodeSnapshotMatchesCurrentFiles(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.exec(`INSERT INTO episode (id,title_id,season,number,monitored,created_at,updated_at)
  VALUES (101,2,1,1,1,0,0),(102,2,1,2,1,0,0),(103,2,1,3,0,0,0),(104,2,1,4,1,0,0),(105,2,1,5,1,0,0)`)
	e.exec(`INSERT INTO episode_version_state (version_id,episode_id,monitored,state,updated_at)
  VALUES (20,102,0,'wanted',0),(20,103,1,'wanted',0)`)
	e.exec(`UPDATE episode SET removed_at=1 WHERE id=104`)
	e.link(2, 20, 101, "WEBDL-2160p", 120, 10)
	e.exec(`INSERT INTO version_file (id,version_id,title_id,instance,arr_file_id,path,quality,quality_revision,custom_format_score,added_at)
  VALUES (1,20,2,'arr',1,'/arr/a.mkv','WEBDL-1080p',1,100,100), (2,20,2,'other',2,'/other/a.mkv','WEBDL-1080p',1,200,200)`)
	e.exec(`INSERT INTO version_file_episode (version_file_id,episode_id) VALUES (1,101),(2,105)`)
	e.exec(`INSERT INTO version_source (version_id,instance,arr_id) VALUES (20,'arr',2)`)
	s := &routeState{req: Request{TitleID: 2, VersionID: 20, Version: "series"}, vp: e.pol.Versions["series"]}
	got, err := e.r.freshEpisodeFiles(e.ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("snapshot episodes: %+v", got)
	}
	for _, id := range []int64{101, 103, 105} {
		want, err := currentFiles(e.ctx, e.st.Writer(), 2, 20, []int64{id})
		if err != nil || !reflect.DeepEqual(got[id], want) {
			t.Fatalf("episode %d: %+v want %+v: %v", id, got[id], want, err)
		}
	}
	e.exec(`UPDATE episode SET monitored=0 WHERE id=101`)
	got, err = e.r.freshEpisodeFiles(e.ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[101]; ok {
		t.Fatal("next check reused old monitoring")
	}
}
