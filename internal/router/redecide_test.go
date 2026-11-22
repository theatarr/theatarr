package router

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/profile"
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
				if last := res.Steps[len(res.Steps)-1]; last.Message != "no release is accepted under the current policy" {
					t.Errorf("cancel message = %q, want %q", last.Message, "no release is accepted under the current policy")
				}
				if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'failed' AND error = ?`,
					resumed.DownloadID, "route cancelled: no release is accepted under the current policy"); n != 1 {
					t.Errorf("queued download not failed with policy message")
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
		res, err := e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Source: "other:source-lib",
			Candidates: []Candidate{*c}})
		if err != nil || res.Outcome != OutcomeAdded {
			t.Fatalf("DecideOther route: %+v %v steps=%s", res, err, steps(res))
		}
		var eps string
		if err := e.st.Reader().QueryRow(`SELECT episodes FROM download WHERE id = ?`, res.DownloadID).Scan(&eps); err != nil {
			t.Fatal(err)
		}
		if eps != "[102,103]" {
			t.Errorf("DecideOther download.episodes = %s, want [102,103]", eps)
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
		if last := res.Steps[len(res.Steps)-1]; last.Message != "no release is still wanted by this version" {
			t.Errorf("cancel message = %q, want %q", last.Message, "no release is still wanted by this version")
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

	// C2: a resumed automatic route decides its candidates again with what they were found by. An RSS pack whose
	// episodes are all held now is rejected (Repack: another group) and dropped; a search pack is decided with its
	// stored criteria and loses the held episode; all held, it is dropped too.
	holdAll := func(e *env) {
		for _, ep := range []int64{101, 102, 103} {
			id := e.link(2, 20, ep, "WEBDL-2160p", 0, e.clock.Add(-time.Hour).UnixMilli())
			e.exec(`UPDATE link SET release_title = 'Show.S01.2160p.WEB-DL-OLD',
				file_metadata = '{"Revision":1,"ReleaseGroup":"OLD","SceneName":"Show.S01.2160p.WEB-DL-OLD","Languages":["English"],"Size":8000000000,"ReleaseType":"seasonPack"}'
				WHERE id = ?`, id)
		}
	}
	// mode: "rss", "search" (stored criteria), "manual" (a grab by hand) or "legacy" (a search stored without its
	// criteria).
	resume := func(t *testing.T, mode string, hold func(*env)) (*env, *Result) {
		t.Helper()
		e, _ := setupSeries(t, false)
		for _, k := range []string{"rd", "ad", "tb"} {
			e.exec(`INSERT INTO router_pacing (provider, backoff_until, failures, reason, updated_at)
				VALUES (?, ?, 1, 'x', 0)`, k, e.clock.Add(time.Minute).UnixMilli())
		}
		c := cand(h("f"), "Show.S01.REPACK.2160p.WEB-DL-NEW")
		c.Release.Size = 30 << 30
		c.Release.Seeders = 10
		c.Episodes = []int64{101, 102, 103}
		src := "rss"
		switch mode {
		case "search":
			c.Search, c.Origin, src = true, OriginSearch, "wanted"
			c.Criteria = []*decide.SeriesSearch{{Kind: profile.SearchSeason, Season: 1, Episodes: []int64{101, 102, 103}}}
		case "episode":
			// An episode search's criteria: they reject a season pack (Full season pack), RSS rules accept it.
			c.Search, c.Origin, src = true, OriginSearch, "wanted"
			c.Criteria = []*decide.SeriesSearch{{Kind: profile.SearchSingleEpisode, Season: 1, Episode: 2, Episodes: []int64{102}}}
		case "legacy":
			c.Search, c.Origin, src = true, OriginSearch, "wanted"
		case "manual":
			src = "ui:grab:test"
		}
		res, err := e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Source: src,
			Candidates: []Candidate{c}})
		if err != nil || res.Outcome != OutcomeDeferred {
			t.Fatalf("defer: %+v %v steps=%s", res, err, steps(res))
		}
		hold(e)
		req, err := DownloadRequest(e.ctx, e.st.Reader(), e.pol, res.DownloadID)
		if err != nil {
			t.Fatal(err)
		}
		if (mode == "search" || mode == "episode") && (req.Candidates[0].Origin != OriginSearch || len(req.Candidates[0].Criteria) != 1) {
			t.Fatalf("resumed search candidate lost its criteria: %+v", req.Candidates[0])
		}
		e.clock = e.clock.Add(2 * time.Minute)
		res, err = e.r.Route(e.ctx, *req)
		if err != nil {
			t.Fatal(err)
		}
		return e, res
	}

	t.Run("remap_resumed_drops_all_held_rss", func(t *testing.T) {
		t.Parallel()
		e, res := resume(t, "rss", holdAll)
		if res.Outcome != OutcomeCancelled || !strings.Contains(steps(res), "skipped/rejected_now") || e.writes() != 0 {
			t.Fatalf("all held RSS: outcome=%s writes=%d steps=%s", res.Outcome, e.writes(), steps(res))
		}
	})

	t.Run("remap_resumed_search_with_criteria", func(t *testing.T) {
		t.Parallel()
		e, res := resume(t, "search", addHeldE01)
		if res.Outcome != OutcomeAdded {
			t.Fatalf("resume: %+v steps=%s", res, steps(res))
		}
		var eps string
		if err := e.st.Reader().QueryRow(`SELECT episodes FROM download WHERE id = ?`, res.DownloadID).Scan(&eps); err != nil {
			t.Fatal(err)
		}
		if eps != "[102,103]" {
			t.Errorf("re-decided search download.episodes = %s, want [102,103]", eps)
		}
	})

	t.Run("remap_resumed_drops_all_held_search", func(t *testing.T) {
		t.Parallel()
		e, res := resume(t, "search", holdAll)
		if res.Outcome != OutcomeCancelled || !strings.Contains(steps(res), "skipped/rejected_now") || e.writes() != 0 {
			t.Fatalf("all held search: outcome=%s writes=%d steps=%s", res.Outcome, e.writes(), steps(res))
		}
	})

	// The stored criteria decide, not RSS rules: nothing is held, RSS rules would accept the pack.
	t.Run("remap_resumed_decides_with_stored_criteria", func(t *testing.T) {
		t.Parallel()
		e, res := resume(t, "episode", func(*env) {})
		if res.Outcome != OutcomeCancelled || !strings.Contains(steps(res), "skipped/rejected_now") || e.writes() != 0 {
			t.Fatalf("episode criteria: outcome=%s writes=%d steps=%s", res.Outcome, e.writes(), steps(res))
		}
	})

	// A grab by hand and a search stored without its criteria are not decided again: all held, they are still added
	// with the episodes they were queued with.
	for _, mode := range []string{"manual", "legacy"} {
		t.Run("remap_resumed_keeps_"+mode, func(t *testing.T) {
			t.Parallel()
			e, res := resume(t, mode, holdAll)
			if res.Outcome != OutcomeAdded || strings.Contains(steps(res), "rejected_now") {
				t.Fatalf("%s: %+v steps=%s", mode, res, steps(res))
			}
			var eps string
			if err := e.st.Reader().QueryRow(`SELECT episodes FROM download WHERE id = ?`, res.DownloadID).Scan(&eps); err != nil {
				t.Fatal(err)
			}
			if eps != "[101,102,103]" {
				t.Errorf("%s download.episodes = %s, want [101,102,103]", mode, eps)
			}
		})
	}

	// One release rejected now and another accepted with no wanted episode left: the route is cancelled as one whose
	// releases are no longer wanted, not as one none of whose releases is accepted; each skip says its own reason.
	t.Run("remap_resumed_rejected_and_all_kept", func(t *testing.T) {
		t.Parallel()
		e, _ := setupSeries(t, false)
		addHeldE01(e)
		one := cand(h("c"), "Show.S01E01.REPACK.2160p.WEB-DL-NEW")
		one.Episodes = []int64{101}
		pack := cand(h("d"), "Show.S01.REPACK.2160p.WEB-DL-NEW")
		pack.Release.Size, pack.Release.Seeders = 30<<30, 10
		pack.Episodes = []int64{101}
		for _, c := range []*Candidate{&one, &pack} {
			c.Identity, c.Origin = RouteIdentityVersion, OriginRSS
		}
		req := Request{TitleID: 2, VersionID: 20, Version: "series", Source: "rss", Candidates: []Candidate{one, pack},
			DecisionRev: e.pol.Revision}
		req.waitingSince = e.clock.Add(-time.Minute)
		res, err := e.r.routeStored(e.ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != OutcomeCancelled || !strings.Contains(steps(res), "cccc//skipped/rejected_now") ||
			!strings.Contains(steps(res), "dddd//skipped/no_longer_upgrade") || e.writes() != 0 {
			t.Fatalf("outcome=%s writes=%d steps=%s", res.Outcome, e.writes(), steps(res))
		}
		if last := res.Steps[len(res.Steps)-1]; last.Message != "no release is still wanted by this version" {
			t.Errorf("cancel message = %q", last.Message)
		}
	})

	// A route deferred behind provider pacing before a download was opened (no download id, a waiting time) is a
	// resumed route too.
	t.Run("paced_before_any_download_counts_as_resumed", func(t *testing.T) {
		t.Parallel()
		e, _ := setupSeries(t, false)
		holdAll(e)
		c := cand(h("g"), "Show.S01.REPACK.2160p.WEB-DL-NEW")
		c.Release.Size = 30 << 30
		c.Release.Seeders = 10
		c.Episodes = []int64{101, 102, 103}
		c.Identity, c.Origin = RouteIdentityVersion, OriginRSS
		req := Request{TitleID: 2, VersionID: 20, Version: "series", Source: "rss", Candidates: []Candidate{c},
			DecisionRev: e.pol.Revision}
		req.waitingSince = e.clock.Add(-time.Minute)
		res, err := e.r.routeStored(e.ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != OutcomeCancelled || !strings.Contains(steps(res), "skipped/rejected_now") || e.writes() != 0 {
			t.Fatalf("paced resume: outcome=%s writes=%d steps=%s", res.Outcome, e.writes(), steps(res))
		}
	})
}

// AF D3: a TV capture of an episode that airs in five days is passed over by an automatic pick (the route's second
// decision drops it from RSS in a library that prefers SeaDex), but a person's grab of it from Interactive search
// passes the route, also when it was deferred and resumes, in an ordinary library and in one that prefers SeaDex.
func TestHandGrabOfUnairedTVCapturePassesRoute(t *testing.T) {
	t.Parallel()
	var engines sync.Map // *env -> *decide.Engine
	setup := func(t *testing.T, preferSeaDex bool) *env {
		t.Helper()
		e := newEnv(t, true)
		e.pol.Profiles = map[string]*policy.Profile{"tv": {Qualities: []policy.QualityItem{{Name: "HDTV-720p"}, {Name: "WEBDL-720p"}},
			Cutoff: "HDTV-720p", Propers: policy.PropersPreferAndUpgrade}}
		e.pol.Versions["series"].Profile = "tv"
		e.pol.Versions["series"].PreferSeaDex = preferSeaDex
		e.exec(`UPDATE title SET runtime_minutes = 45 WHERE id = 2`)
		e.exec(`INSERT INTO episode (id, title_id, season, number, name, air_date, air_date_utc_at, created_at, updated_at)
			VALUES (101, 2, 1, 1, 'Pilot', '2026-10-31', ?, 0, 0)`, e.clock.Add(5*24*time.Hour).UnixMilli())
		eng, err := decide.New(e.pol, decide.Options{Now: func() time.Time { return e.clock }})
		if err != nil {
			t.Fatal(err)
		}
		e.r.SetProfiles(eng.Profile)
		e.r.SetDecider(eng.Decide, eng.DecideSearch)
		engines.Store(e, eng)
		return e
	}
	hdtv := func(c string) Candidate {
		x := cand(h(c), "Show.S01E01.720p.HDTV.x264-GRP")
		x.Release.Size, x.Release.Seeders = 1<<30, 10
		x.Quality, x.Episodes = "HDTV-720p", []int64{101}
		return x
	}
	for _, prefer := range []bool{false, true} {
		name := "ordinary"
		if prefer {
			name = "prefer_seadex"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := setup(t, prefer)
			for _, k := range []string{"rd", "ad", "tb"} {
				e.exec(`INSERT INTO router_pacing (provider, backoff_until, failures, reason, updated_at)
					VALUES (?, ?, 1, 'x', 0)`, k, e.clock.Add(time.Minute).UnixMilli())
			}
			res, err := e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Source: "ui:grab:me",
				Candidates: []Candidate{hdtv("a")}})
			if err != nil || res.Outcome != OutcomeDeferred {
				t.Fatalf("hand grab: %+v %v steps=%s", res, err, steps(res))
			}
			req, err := DownloadRequest(e.ctx, e.st.Reader(), e.pol, res.DownloadID)
			if err != nil {
				t.Fatal(err)
			}
			e.clock = e.clock.Add(2 * time.Minute)
			res, err = e.r.Route(e.ctx, *req)
			if err != nil || res.Outcome != OutcomeAdded {
				t.Fatalf("resumed hand grab: %+v %v steps=%s", res, err, steps(res))
			}
		})
	}
	t.Run("rss_prefer_seadex", func(t *testing.T) {
		t.Parallel()
		e := setup(t, true)
		e.exec(`INSERT INTO series_meta (title_id, air_time, time_zone) VALUES (2, '', 'America/New_York')`)
		e.exec(`UPDATE episode SET air_date_utc_at = ? WHERE id = 101`, // the refresh's midnight of the day in the zone
			time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC).Add(4*time.Hour).UnixMilli())
		res, err := e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Source: "rss",
			Candidates: []Candidate{hdtv("b")}})
		if err != nil || res.Outcome != OutcomeCancelled || !strings.Contains(steps(res), "rejected_now") {
			t.Fatalf("rss route: %+v %v steps=%s", res, err, steps(res))
		}
		if m := res.Steps[0].Message; !strings.Contains(m, "[NotAiredYet] Not aired yet: S01E01 airs Sat 31 Oct; a TV recording") {
			t.Errorf("rejection = %q", m)
		}
	})

	// Only a television source the name states is held, and never a file already on disk (Manual import, reuse).
	t.Run("named_source_and_on_disk", func(t *testing.T) {
		t.Parallel()
		e := setup(t, false)
		v, _ := engines.Load(e)
		eng := v.(*decide.Engine)
		title, err := decide.LoadTitle(e.ctx, e.st.Reader(), 2)
		if err != nil {
			t.Fatal(err)
		}
		ver, err := decide.LoadVersion(e.ctx, e.st.Reader(), e.pol, 2, "series")
		if err != nil {
			t.Fatal(err)
		}
		if err := decide.LoadSeriesVersion(e.ctx, e.st.Reader(), &title, ver.ID); err != nil {
			t.Fatal(err)
		}
		held := func(ctx context.Context, name string) bool {
			d, err := eng.Decide(ctx, decide.Release{Release: indexer.Release{Indexer: "pub", Title: name, InfoHash: h("f"),
				Size: 1 << 30, Seeders: 10, Peers: 5}}, title, ver, nil)
			if err != nil {
				t.Fatal(err)
			}
			return slices.ContainsFunc(d.Rejections, func(r decide.Rejection) bool { return r.Reason == "NotAiredYet" })
		}
		for _, name := range []string{"Show.S01E01.2160p.x265-GRP", "Show.S01E01.1080p.x264-GRP"} {
			if held(e.ctx, name) {
				t.Errorf("%s (no source in the name) held", name)
			}
		}
		if !held(e.ctx, "Show.S01E01.720p.HDTV.x264-GRP") {
			t.Error("named HDTV not held")
		}
		if held(decide.WithOnDisk(e.ctx), "Show.S01E01.720p.HDTV.x264-GRP") {
			t.Error("a file on disk held")
		}
	})

	// The same, with a WEB fallback the route adds: the held HDTV's queued row closes quietly too.
	t.Run("postponed_with_fallback", func(t *testing.T) {
		t.Parallel()
		e := setup(t, true)
		e.exec(`UPDATE episode SET air_date_utc_at = ? WHERE id = 101`, e.clock.Add(time.Hour).UnixMilli())
		for _, k := range []string{"rd", "ad", "tb"} {
			e.exec(`INSERT INTO router_pacing (provider, backoff_until, failures, reason, updated_at)
				VALUES (?, ?, 1, 'x', 0)`, k, e.clock.Add(time.Minute).UnixMilli())
		}
		q := jobs.NewQueue(e.st)
		q.SetClock(func() time.Time { return e.clock })
		e.r.RegisterJobs(jobs.NewWorker(q, jobs.WorkerOptions{}), q)
		tv := hdtv("d")
		tv.Origin = OriginRSS
		web := cand(h("e"), "Show.S01E01.720p.WEB-DL.x264-GRP")
		web.Release.Size, web.Release.Seeders = 1<<30, 10
		web.Quality, web.Episodes, web.Origin = "WEBDL-720p", []int64{101}, OriginRSS
		if res, err := e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Source: "rss",
			Candidates: []Candidate{tv, web}}); err != nil || res.Outcome != OutcomeDeferred {
			t.Fatalf("defer: %+v %v steps=%s", res, err, steps(res))
		}
		var raw string
		if err := e.st.Reader().QueryRow(`SELECT payload FROM job WHERE kind = ? AND state = 'queued'`, JobRoute).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var resumed routePayload
		if err := json.Unmarshal([]byte(raw), &resumed); err != nil {
			t.Fatal(err)
		}
		if len(resumed.Candidates) != 2 || e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND info_hash = ?`,
			resumed.DownloadID, h("d")) != 1 {
			t.Fatalf("deferred payload %+v", resumed)
		}
		e.exec(`UPDATE episode SET air_date_utc_at = ? WHERE id = 101`, e.clock.Add(5*24*time.Hour).UnixMilli())
		e.clock = e.clock.Add(2 * time.Minute)
		res, err := e.r.Route(e.ctx, resumed.Request)
		if err != nil || res.Outcome != OutcomeAdded || res.Hash != h("e") {
			t.Fatalf("resume: %+v %v steps=%s", res, err, steps(res))
		}
		if n := e.count(`SELECT COUNT(*) FROM download WHERE state = 'failed' OR info_hash = ?`, h("d")); n != 0 {
			t.Errorf("%d failed or held download rows left", n)
		}
		if n := e.count(`SELECT COUNT(*) FROM history WHERE ref LIKE '%:fail'`); n != 0 {
			t.Errorf("%d failure History rows", n)
		}
	})

	// "Date only" as Calendar reads it: a midnight of the air day in the series' zone (UTC without a series_meta row)
	// prints the day; a real time prints with its zone even where series_meta.air_time is blank.
	t.Run("date_only_as_calendar", func(t *testing.T) {
		t.Parallel()
		words := func(t *testing.T, meta bool, at time.Time) string {
			e := setup(t, false)
			if meta {
				e.exec(`INSERT INTO series_meta (title_id, air_time, time_zone) VALUES (2, '', 'UTC')`)
			}
			e.exec(`UPDATE episode SET air_date = '2026-11-26', air_date_utc_at = ? WHERE id = 101`, at.UnixMilli())
			v, _ := engines.Load(e)
			title, err := decide.LoadTitle(e.ctx, e.st.Reader(), 2)
			if err != nil {
				t.Fatal(err)
			}
			ver, err := decide.LoadVersion(e.ctx, e.st.Reader(), e.pol, 2, "series")
			if err != nil {
				t.Fatal(err)
			}
			if err := decide.LoadSeriesVersion(e.ctx, e.st.Reader(), &title, ver.ID); err != nil {
				t.Fatal(err)
			}
			d, err := v.(*decide.Engine).Decide(e.ctx, decide.Release{Release: indexer.Release{Indexer: "pub",
				Title: "Show.S01E01.720p.HDTV.x264-GRP", InfoHash: h("g"), Size: 1 << 30, Seeders: 10, Peers: 5}}, title, ver, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range d.Rejections {
				if r.Reason == "NotAiredYet" {
					return r.Message
				}
			}
			t.Fatalf("not held: %+v", d.Rejections)
			return ""
		}
		if m := words(t, false, time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)); !strings.Contains(m, "airs Thu 26 Nov; ") {
			t.Errorf("UTC midnight without series_meta: %q", m)
		}
		if m := words(t, true, time.Date(2026, 11, 26, 21, 0, 0, 0, time.UTC)); !strings.Contains(m, "airs Thu 26 Nov, 9 pm UTC; ") {
			t.Errorf("real time with a blank air_time: %q", m)
		}
	})

	// A queued automatic route whose episode moved beyond 36 hours while it waited for a provider closes without a
	// failure: no failed download, History or blocklist row (an ordinary library re-maps it, a prefer-SeaDex one
	// decides it again).
	for _, prefer := range []bool{false, true} {
		t.Run(fmt.Sprintf("postponed_prefer_%v", prefer), func(t *testing.T) {
			t.Parallel()
			e := setup(t, prefer)
			e.exec(`UPDATE episode SET air_date_utc_at = ? WHERE id = 101`, e.clock.Add(time.Hour).UnixMilli())
			for _, k := range []string{"rd", "ad", "tb"} {
				e.exec(`INSERT INTO router_pacing (provider, backoff_until, failures, reason, updated_at)
					VALUES (?, ?, 1, 'x', 0)`, k, e.clock.Add(time.Minute).UnixMilli())
			}
			c := hdtv("c")
			c.Origin = OriginRSS
			res, err := e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Source: "rss",
				Candidates: []Candidate{c}})
			if err != nil || res.Outcome != OutcomeDeferred {
				t.Fatalf("defer: %+v %v steps=%s", res, err, steps(res))
			}
			req, err := DownloadRequest(e.ctx, e.st.Reader(), e.pol, res.DownloadID)
			if err != nil {
				t.Fatal(err)
			}
			e.exec(`UPDATE episode SET air_date_utc_at = ? WHERE id = 101`, e.clock.Add(5*24*time.Hour).UnixMilli())
			e.clock = e.clock.Add(2 * time.Minute)
			res, err = e.r.Route(e.ctx, *req)
			if err != nil || res.Outcome != OutcomeCancelled || !strings.Contains(steps(res), "not_aired") {
				t.Fatalf("resume: %+v %v steps=%s", res, err, steps(res))
			}
			if n := e.count(`SELECT COUNT(*) FROM download`); n != 0 {
				t.Errorf("%d download rows left", n)
			}
			if n := e.count(`SELECT COUNT(*) FROM history`); n != 0 {
				t.Errorf("%d History rows", n)
			}
			if n := e.count(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
				t.Errorf("%d blocklist rows", n)
			}
		})
	}
}
