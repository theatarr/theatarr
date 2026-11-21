package router

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
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
			e := newEnv(t, true)
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

// TestKeptByRuleEpisodesLeftOut checks AA2.3 and AA2.4: Candidates and DecideOther leave episodes kept by rule out of
// Candidate.Episodes (so download.episodes omits them), and a re-decided route narrows or skips a candidate whose
// queued episodes are now kept by rule.
func TestKeptByRuleEpisodesLeftOut(t *testing.T) {
	t.Parallel()

	setupSeries := func(t *testing.T, preferSeaDex bool) (*env, *decide.Engine) {
		t.Helper()
		e := newEnv(t, true)
		e.pol.Profiles = map[string]*policy.Profile{
			"tv": {
				Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}},
				Cutoff:    "WEBDL-2160p",
				Propers:   policy.PropersPreferAndUpgrade,
			},
		}
		e.pol.Versions["series"].Profile = "tv"
		e.pol.Versions["series"].PreferSeaDex = preferSeaDex
		e.exec(`UPDATE title SET runtime_minutes = 45 WHERE id = 2`)
		e.exec(`INSERT INTO episode (id, title_id, season, number, name, air_date, air_date_utc_at, created_at, updated_at) VALUES
			(101, 2, 1, 1, 'Pilot', '2024-01-01', 1000, 0, 0),
			(102, 2, 1, 2, 'Second', '2024-01-02', 1000, 0, 0),
			(103, 2, 1, 3, 'Third', '2024-01-03', 1000, 0, 0)`)
		eng, err := decide.New(e.pol, decide.Options{Now: func() time.Time { return e.clock }})
		if err != nil {
			t.Fatal(err)
		}
		e.r.SetProfiles(eng.Profile)
		e.r.SetDecider(eng.Decide, eng.DecideSearch)
		return e, eng
	}

	addHeldE01 := func(e *env) {
		id := e.link(2, 20, 101, "WEBDL-2160p", 0, e.clock.Add(-time.Hour).UnixMilli())
		e.exec(`UPDATE link SET release_title = 'Show.S01E01.2160p.WEB-DL-OLD',
			file_metadata = '{"Revision":1,"ReleaseGroup":"OLD","SceneName":"Show.S01E01.2160p.WEB-DL-OLD","Languages":["English"],"Size":8000000000,"ReleaseType":"singleEpisode"}'
			WHERE id = ?`, id)
	}

	t.Run("candidates_and_route", func(t *testing.T) {
		t.Parallel()
		e, eng := setupSeries(t, false)
		addHeldE01(e)
		title, err := decide.LoadTitle(e.ctx, e.st.Reader(), 2)
		if err != nil {
			t.Fatal(err)
		}
		v, err := decide.LoadVersion(e.ctx, e.st.Reader(), e.pol, 2, "series")
		if err != nil {
			t.Fatal(err)
		}
		if err := decide.LoadSeriesVersion(e.ctx, e.st.Reader(), &title, v.ID); err != nil {
			t.Fatal(err)
		}
		rel := indexer.Release{
			Indexer: "pub", Title: "Show.S01.REPACK.2160p.WEB-DL-NEW",
			InfoHash: h("a"), Size: 30 << 30, Seeders: 10, Peers: 5,
		}
		d, err := eng.Decide(e.ctx, decide.Release{Release: rel}, title, v, nil)
		if err != nil || !d.Accepted {
			t.Fatalf("decide: %+v %v", d, err)
		}
		cs := Candidates([]*decide.Decision{d}, map[*decide.Decision]indexer.Release{d: rel})
		if len(cs) != 1 || !slices.Equal(cs[0].Episodes, []int64{102, 103}) {
			t.Fatalf("Candidates episodes = %+v, want [102 103]", cs)
		}
		res, err := e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Source: "rss", Candidates: cs})
		if err != nil || res.Outcome != OutcomeAdded {
			t.Fatalf("route: %+v %v steps=%s", res, err, steps(res))
		}
		var eps string
		if err := e.st.Reader().QueryRow(`SELECT episodes FROM download WHERE id = ?`, res.DownloadID).Scan(&eps); err != nil {
			t.Fatal(err)
		}
		if eps != "[102,103]" {
			t.Errorf("download.episodes = %s, want [102,103]", eps)
		}
	})

	t.Run("decide_other", func(t *testing.T) {
		t.Parallel()
		e, eng := setupSeries(t, false)
		addHeldE01(e)
		title, err := decide.LoadTitle(e.ctx, e.st.Reader(), 2)
		if err != nil {
			t.Fatal(err)
		}
		v, err := decide.LoadVersion(e.ctx, e.st.Reader(), e.pol, 2, "series")
		if err != nil {
			t.Fatal(err)
		}
		if err := decide.LoadSeriesVersion(e.ctx, e.st.Reader(), &title, v.ID); err != nil {
			t.Fatal(err)
		}
		c, why, err := DecideOther(e.ctx, e.pol, eng, OtherRelease{
			Indexer:  "pub",
			Title:    "Show.S01.REPACK.2160p.WEB-DL-NEW",
			Hash:     h("b"),
			Size:     30 << 30,
			Episodes: []int64{101, 102, 103},
		}, title, v, nil, e.clock)
		if err != nil || c == nil {
			t.Fatalf("DecideOther: c=%+v why=%q err=%v", c, why, err)
		}
		if !slices.Equal(c.Episodes, []int64{102, 103}) {
			t.Errorf("DecideOther episodes = %v, want [102 103]", c.Episodes)
		}
	})

	t.Run("redecide_drops_kept_episode", func(t *testing.T) {
		t.Parallel()
		e, _ := setupSeries(t, true)
		addHeldE01(e)
		c := cand(h("c"), "Show.S01.REPACK.2160p.WEB-DL-NEW")
		c.Release.Size = 30 << 30
		c.Release.Seeders = 10
		c.Episodes = []int64{101, 102, 103}
		res, err := e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Source: "rss",
			Candidates: []Candidate{c}})
		if err != nil || res.Outcome != OutcomeAdded {
			t.Fatalf("redecide route: %+v %v steps=%s", res, err, steps(res))
		}
		var eps string
		if err := e.st.Reader().QueryRow(`SELECT episodes FROM download WHERE id = ?`, res.DownloadID).Scan(&eps); err != nil {
			t.Fatal(err)
		}
		if eps != "[102,103]" {
			t.Errorf("redecided download.episodes = %s, want [102,103]", eps)
		}
	})

	t.Run("redecide_skips_when_all_queued_kept", func(t *testing.T) {
		t.Parallel()
		e, _ := setupSeries(t, true)
		addHeldE01(e)
		c := cand(h("d"), "Show.S01.REPACK.2160p.WEB-DL-NEW")
		c.Release.Size = 30 << 30
		c.Release.Seeders = 10
		c.Episodes = []int64{101}
		res, err := e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Source: "rss",
			Candidates: []Candidate{c}})
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != OutcomeCancelled || !strings.Contains(steps(res), "no_longer_upgrade") || e.writes() != 0 {
			t.Fatalf("all queued kept: outcome=%s writes=%d steps=%s", res.Outcome, e.writes(), steps(res))
		}
	})

	t.Run("remap_resumed_drops_kept_episode", func(t *testing.T) {
		t.Parallel()
		e, _ := setupSeries(t, false)
		for _, k := range []string{"rd", "ad", "tb"} {
			e.exec(`INSERT INTO router_pacing (provider, backoff_until, failures, reason, updated_at)
				VALUES (?, ?, 1, 'x', 0)`, k, e.clock.Add(time.Minute).UnixMilli())
		}
		c := cand(h("e"), "Show.S01.REPACK.2160p.WEB-DL-NEW")
		c.Release.Size = 30 << 30
		c.Release.Seeders = 10
		c.Episodes = []int64{101, 102, 103}
		res, err := e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Source: "rss",
			Candidates: []Candidate{c}})
		if err != nil || res.Outcome != OutcomeDeferred {
			t.Fatalf("defer: %+v %v steps=%s", res, err, steps(res))
		}
		addHeldE01(e)
		req, err := DownloadRequest(e.ctx, e.st.Reader(), e.pol, res.DownloadID)
		if err != nil {
			t.Fatal(err)
		}
		e.clock = e.clock.Add(2 * time.Minute)
		res, err = e.r.Route(e.ctx, *req)
		if err != nil || res.Outcome != OutcomeAdded {
			t.Fatalf("resume: %+v %v steps=%s", res, err, steps(res))
		}
		var eps string
		if err := e.st.Reader().QueryRow(`SELECT episodes FROM download WHERE id = ?`, res.DownloadID).Scan(&eps); err != nil {
			t.Fatal(err)
		}
		if eps != "[102,103]" {
			t.Errorf("remapped download.episodes = %s, want [102,103]", eps)
		}
	})
}
