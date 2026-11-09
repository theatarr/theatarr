package router

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
)

// deferredRSS routes an RSS candidate while every provider backs off and returns the queued follow-up payload.
func deferredRSS(t *testing.T, e *env, source string) routePayload {
	t.Helper()
	q := jobs.NewQueue(e.st)
	q.SetClock(func() time.Time { return e.clock })
	e.r.RegisterJobs(jobs.NewWorker(q, jobs.WorkerOptions{}), q)
	for _, k := range []string{"rd", "ad", "tb"} {
		e.exec(`INSERT INTO router_pacing (provider, backoff_until, failures, reason, updated_at)
			VALUES (?, ?, 1, 'x', 0)`, k, e.clock.Add(time.Minute).UnixMilli())
	}
	c := cand(h("a"), "Movie.2024.2160p.WEB-DL-GRP")
	c.Quality, c.Score = "WEBDL-2160p", 100
	payload, _ := json.Marshal(routePayload{Request: freshReq(c, source)})
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
	if resumed.DownloadID == 0 || resumed.DecisionRev != "rev1" {
		t.Fatalf("deferred payload %+v", resumed)
	}
	e.clock = e.clock.Add(2 * time.Minute)
	return resumed
}

// A deferred automatic grab queued under one policy revision is decided again when it resumes under
// another: a release the changed profile rejects is not grabbed; one it accepts takes the current score. A manual
// grab is never decided again.
func TestDeferredAutomaticRouteRedecidedAfterPolicyChange(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, source string
		accept       bool
	}{{"rejected", "rss", false}, {"accepted", "rss", true}, {"manual", "manual", false}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			freshMovie(e)
			e.pol.Revision = "rev1"
			asked := 0
			e.r.SetDecider(func(_ context.Context, rel decide.Release, _ decide.Title, v decide.Version,
				_ *decide.ExistingFile) (*decide.Decision, error) {
				asked++
				if rel.Title != "Movie.2024.2160p.WEB-DL-GRP" || v.Key != "movie" {
					t.Errorf("decided %s for %s", rel.Title, v.Key)
				}
				if !tc.accept {
					return &decide.Decision{Rejections: []decide.Rejection{{Reason: "score",
						Message: "score 100 is below the minimum 1000"}}}, nil
				}
				return &decide.Decision{Accepted: true, Quality: "WEBDL-2160p", Score: 1500}, nil
			}, nil)
			resumed := deferredRSS(t, e, tc.source)
			if asked != 0 {
				t.Fatalf("decided again under the same revision: %d", asked)
			}
			e.pol.Revision = "rev2"
			res, err := e.r.Route(e.ctx, resumed.Request)
			if err != nil {
				t.Fatal(err)
			}
			switch tc.name {
			case "rejected":
				if res.Outcome != OutcomeCancelled || !strings.Contains(steps(res), "rejected_now") || e.writes() != 0 {
					t.Fatalf("resume %+v, writes %d", res, e.writes())
				}
				if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'failed'`, resumed.DownloadID); n != 1 {
					t.Errorf("queued download not failed")
				}
			case "accepted":
				if res.Outcome != OutcomeAdded || asked != 1 {
					t.Fatalf("resume %+v, asked %d", res, asked)
				}
				if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND score = 1500`, res.DownloadID); n != 1 {
					t.Errorf("the download kept the old decision's score")
				}
			case "manual":
				if res.Outcome != OutcomeAdded || asked != 0 {
					t.Fatalf("manual resume %+v, asked %d", res, asked)
				}
			}
		})
	}
}
