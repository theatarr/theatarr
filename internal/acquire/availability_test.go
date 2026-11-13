package acquire

import (
	"slices"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
)

func TestMovieAvailabilityBeforeSearch(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 1})
	e.exec(`UPDATE version SET monitored = 0 WHERE title_id = 10`)
	e.exec(`UPDATE title_release_date SET date = '2099-01-01' WHERE title_id = 1`)
	if got, err := e.a.InteractiveMany(e.ctx, 1, []string{"movie", "movie-shadow"}, nil, nil); err != nil || len(got) != 2 || len(e.calls) != 2 {
		t.Fatalf("interactive: %v %v, calls %v", got, err, e.calls)
	}
	e.calls = nil
	if err := e.a.Research(e.ctx, router.ResearchRequest{TitleID: 1, VersionID: 101, Version: "movie",
		Reason: router.ReasonStalled}); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE title_id = 1`) + e.count(`SELECT COUNT(*) FROM search_request`); n != 0 {
		t.Fatalf("research of an unavailable movie: %d runs and rows", n)
	}
	sum, err := e.a.Round(e.ctx, nil)
	if err != nil || sum.Queued != 1 {
		t.Fatalf("wanted: %+v %v", sum, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE title_id = 1`); n != 0 {
		t.Fatalf("unavailable movie spent budget: %d", n)
	}
	e.drain()
	if len(e.calls) != 1 || e.calls[0].Title != "Two" {
		t.Fatalf("searches: %+v", e.calls)
	}
}

func TestQueuedSearchRechecksMovieAvailability(t *testing.T) {
	t.Parallel()
	for _, fanout := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "fanout"}[fanout], func(t *testing.T) {
			e := newEnv(t, true, nil)
			if fanout {
				e.pol.Versions["movie-shadow"].Mode = policy.ModeActive
			}
			if q, _, err := e.a.Submit(e.ctx, 0, Selection{Versions: []string{"movie"}, TitleIDs: []int64{1}}); err != nil || q != 1 {
				t.Fatalf("submit: %d %v", q, err)
			}
			e.exec(`UPDATE acquire_run SET source = 'wanted'`)
			e.exec(`UPDATE title_release_date SET date = '2099-01-01' WHERE title_id = 1`)
			e.drain()
			if len(e.calls) != 0 {
				t.Fatalf("searched unavailable movie: %v", e.calls)
			}
			want := 1
			if fanout {
				want = 2
			}
			if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE state = 'skipped' AND message LIKE '%availability%'`); n != want {
				t.Fatalf("skipped %d, want %d", n, want)
			}
		})
	}
}

func TestWantedUpdatesNewlyAiredSeries(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	e.exec(`UPDATE episode_version_state SET state = 'cutoff_met' WHERE version_id IN (201,211)`)
	e.exec(`UPDATE episode_version_state SET state = 'wanted' WHERE episode_id = 1005`)
	e.exec(`UPDATE version SET state = 'cutoff_met' WHERE title_id = 10`)
	if _, err := e.a.Round(e.ctx, nil); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM version WHERE title_id = 10 AND state = 'cutoff_met'`); n != 2 {
		t.Fatalf("future episode made series wanted: %d", n)
	}
	e.advance(7 * 24 * time.Hour)
	if _, err := e.a.Round(e.ctx, nil); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM version WHERE title_id = 10 AND state = 'wanted'`); n != 2 {
		t.Fatalf("aired episode did not make series wanted: %d", n)
	}
	e.exec(`UPDATE episode_version_state SET monitored = 0 WHERE episode_id = 1005`)
	e.exec(`UPDATE version SET state = 'cutoff_met' WHERE title_id = 10`)
	if _, err := e.a.Round(e.ctx, nil); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM version WHERE title_id = 10 AND state = 'cutoff_met'`); n != 2 {
		t.Fatalf("unmonitored episode made series wanted: %d", n)
	}
}

func TestMinimumAvailabilityViewSettings(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	e.exec(`DELETE FROM title_release_date WHERE title_id = 1`)
	e.pol.Views = map[string]*policy.View{"movies": {Versions: []string{"movie"}}, "other": {Versions: []string{"movie-shadow"}}}
	e.exec(`INSERT INTO arrview_title (view,title_id,minimum_availability,added_at,updated_at)
  VALUES ('other',1,'announced',0,1)`)
	selection := Selection{Versions: []string{"movie"}, TitleIDs: []int64{1}}
	if got, err := e.a.targets(e.ctx, selection); err != nil || len(got) != 0 {
		t.Fatalf("unrelated view: %v %v", got, err)
	}
	e.exec(`INSERT INTO arrview_title (view,title_id,minimum_availability,added_at,updated_at)
  VALUES ('movies',1,'announced',0,2)`)
	if got, err := e.a.targets(e.ctx, selection); err != nil || len(got) != 1 {
		t.Fatalf("announced: %v %v", got, err)
	}
	e.exec(`INSERT INTO arrview_title (view,title_id,minimum_availability,added_at,updated_at)
  VALUES ('v/movie',1,'released',0,3)`)
	if got, err := e.a.targets(e.ctx, selection); err != nil || len(got) != 0 {
		t.Fatalf("newer per-version choice: %v %v", got, err)
	}
	e.exec(`UPDATE arrview_title SET minimum_availability = 'inCinemas', updated_at = 4 WHERE view = 'movies'`)
	e.exec(`INSERT INTO title_release_date (title_id,source,type,date) VALUES (1,'fixture','theatrical','2026-10-01')`)
	if got, err := e.a.targets(e.ctx, selection); err != nil || len(got) != 1 {
		t.Fatalf("in cinemas: %v %v", got, err)
	}
}

func TestPushedMovieDecisionChecksAvailability(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	e.pol.Profiles = map[string]*policy.Profile{"movie": {Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p"}}
	e.pol.Versions["movie"].Profile = "movie"
	var err error
	e.a.eng, err = decide.New(e.pol, decide.Options{Now: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	release := decide.Release{Release: indexer.Release{Title: "One.2024.2160p.WEB-DL-GRP", Indexer: "pub", Size: 10 << 30, Privacy: policy.PrivacyPublic, Seeders: -1}}
	e.exec(`UPDATE title_release_date SET date = '2099-01-01' WHERE title_id = 1`)
	d, err := e.a.Decide(e.ctx, release, 1, "movie")
	if err != nil {
		t.Fatal(err)
	}
	if d.Accepted || !slices.ContainsFunc(d.Rejections, func(r decide.Rejection) bool { return r.Reason == "Availability" && r.Temporary }) {
		t.Fatalf("unavailable decision: %+v", d)
	}
	e.exec(`UPDATE title_release_date SET date = '2024-01-01' WHERE title_id = 1`)
	d, err = e.a.Decide(e.ctx, release, 1, "movie")
	if err != nil || !d.Accepted {
		t.Fatalf("available decision: %+v %v", d, err)
	}
}

func TestFanoutKeepsAvailableVersion(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	e.pol.Versions["movie-shadow"].Mode = policy.ModeActive
	if q, _, err := e.a.Submit(e.ctx, 0, Selection{Versions: []string{"movie"}, TitleIDs: []int64{1}}); err != nil || q != 1 {
		t.Fatalf("submit: %d %v", q, err)
	}
	e.exec(`UPDATE acquire_run SET source = 'wanted'`)
	e.exec(`DELETE FROM title_release_date WHERE title_id = 1`)
	e.exec(`INSERT INTO arrview_title (view,title_id,minimum_availability,added_at,updated_at)
  VALUES ('v/movie-shadow',1,'announced',0,1)`)
	e.drain()
	if len(e.calls) != 1 || e.calls[0].Version != "movie-shadow" {
		t.Fatalf("fanout searches: %+v", e.calls)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE version_key = 'movie' AND state = 'skipped'`); n != 1 {
		t.Fatalf("unavailable version not skipped: %d", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE version_key = 'movie-shadow' AND state = 'routed'`); n != 1 {
		t.Fatalf("available version not routed: %d", n)
	}
}

func TestUserInvokedMovieAvailability(t *testing.T) {
	t.Parallel()
	for _, fanout := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "fanout"}[fanout], func(t *testing.T) {
			e := newEnv(t, true, nil)
			if fanout {
				e.pol.Versions["movie-shadow"].Mode = policy.ModeActive
			}
			e.exec(`UPDATE title_release_date SET date = '2099-01-01' WHERE title_id = 1`)
			if q, n, err := e.a.Submit(e.ctx, 0, Selection{Versions: []string{"movie"}, TitleIDs: []int64{1}}); err != nil || q != 1 || n != 1 {
				t.Fatalf("submit: %d %d %v", q, n, err)
			}
			e.drain()
			// Only the named version skips availability: the version a title search widened to joins only when
			// available, as an automatic search would take it.
			want := 1
			if len(e.calls) != want {
				t.Fatalf("manual searches: %+v, want %d", e.calls, want)
			}
			if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE state = 'routed'`); n != want {
				t.Fatalf("manual routes %d, want %d", n, want)
			}
		})
	}
}
