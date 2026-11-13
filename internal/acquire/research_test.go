package acquire

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

func (e *env) research(p router.ResearchRequest) {
	e.t.Helper()
	b, _ := json.Marshal(p)
	if err := e.a.handleResearch(e.ctx, &jobs.Job{Kind: router.JobResearch, Payload: b}); err != nil {
		e.t.Fatal(err)
	}
	if err := e.a.Research(context.Background(), router.ResearchRequest{Version: "nope"}); err != nil {
		e.t.Fatalf("unknown version: %v", err)
	}
}

// A re-search after a failure makes the item due at once and searches it as a loop search: active versions only, with
// the router on, within per_hour, once per item within again_after.
func TestResearchAfterFailure(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 3, AgainAfter: policy.Duration(6 * time.Hour)})
	movie := router.ResearchRequest{TitleID: 1, VersionID: 101, Version: "movie", Reason: router.ReasonStalled,
		DownloadID: 9}
	e.research(movie)
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE source = 'wanted' AND research = 'stalled'
		AND version_id = 101 AND target = 'movie' AND state = 'queued'`); n != 1 {
		t.Fatalf("%d research runs", n)
	}
	e.drain()
	if got := e.searches(); len(got) != 1 || got[0].Title != "One" || got[0].Version != "movie" {
		t.Errorf("searches %+v", got)
	}
	// The same failure asked again within again_after is skipped, with the download in flight or not.
	e.research(movie)
	e.exec(`UPDATE download SET state = 'failed'`)
	e.research(movie)
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research IS NOT NULL`); n != 1 {
		t.Errorf("%d research runs, want 1", n)
	}
	e.advance(7 * time.Hour)
	e.research(movie)
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research IS NOT NULL`); n != 2 {
		t.Errorf("%d research runs after again_after, want 2", n)
	}
	// Series: the failed download's episodes, as one season search per season, or an episode search.
	e.research(router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Episodes: []int64{1001, 1003,
		1004}, Reason: router.ReasonExhausted})
	var targets []string
	rows, _ := e.st.Reader().Query(`SELECT target || ':' || episodes FROM acquire_run WHERE version_id = 201 ORDER BY id`)
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		targets = append(targets, s)
	}
	rows.Close()
	if strings.Join(targets, " ") != "s01:[1001,1003] s02e01:[1004]" {
		t.Errorf("series targets %v", targets)
	}
	// per_hour (3) counts every wanted-source run of the hour: the movie and the two series runs use it up.
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE created_at > ?`, e.clock().Add(-time.Hour).UnixMilli()); n > 3 {
		t.Errorf("%d searches in the hour, per_hour 3", n)
	}
	e.research(router.ResearchRequest{TitleID: 2, VersionID: 102, Version: "movie", Reason: router.ReasonLibrary})
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE version_id = 102`); n != 0 {
		t.Error("searched past per_hour")
	}
	// Shadow versions and the kill switch: nothing.
	e.advance(2 * time.Hour)
	e.research(router.ResearchRequest{TitleID: 1, VersionID: 111, Version: "movie-shadow", Reason: "stalled"})
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE version_id = 111`); n != 0 {
		t.Error("a shadow version was searched")
	}
	off := newEnv(t, false, nil)
	off.research(movie)
	if n := off.count(`SELECT COUNT(*) FROM acquire_run`); n != 0 || len(off.searches()) != 0 {
		t.Error("searched with router.enabled false")
	}
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.st.Writer().ExecContext(e.ctx, q, args...); err != nil {
		e.t.Fatal(err)
	}
}

// TestResearchInfraFailureAskedAgain: a re-search the VPN guard refused is not counted against the
// one-per-again_after limit, and its row is due again an hour later; it runs then.
func TestResearchInfraFailureAskedAgain(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 3, AgainAfter: policy.Duration(6 * time.Hour)})
	e.onSearch = func(context.Context) error { return gateway.ErrVPNRequired }
	e.research(router.ResearchRequest{TitleID: 1, VersionID: 101, Version: "movie", Reason: router.ReasonStalled})
	e.drain()
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = 'stalled' AND infra = 1`); n != 1 {
		t.Fatalf("%d infra-failed re-searches", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM search_request WHERE why = 'stalled' AND tries = 1 AND next_at = ?`,
		store.Millis(t0.Add(time.Hour))); n != 1 {
		t.Fatalf("row not due again in an hour")
	}
	e.onSearch = nil
	e.advance(time.Hour)
	if _, err := e.a.Round(e.ctx, nil); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = 'stalled' AND infra = 0`); n != 1 {
		t.Fatalf("%d re-searches after the retry", n)
	}
}

// TestNoAnswerResearchGivesUp: a re-search that never gets an answer is tried again an hour later three times; then
// the ordinary rule applies (here: the switches are off, so the row goes).
func TestNoAnswerResearchGivesUp(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 100, AgainAfter: policy.Duration(24 * time.Hour),
		Missing: new(false), CutoffUnmet: policy.CutoffOff})
	e.onSearch = func(context.Context) error { return gateway.ErrVPNRequired }
	e.research(router.ResearchRequest{TitleID: 1, VersionID: 101, Version: "movie", Reason: router.ReasonStalled})
	e.drain()
	for range 48 { // twelve hours
		e.advance(15 * time.Minute)
		if _, err := e.a.Round(e.ctx, nil); err != nil {
			t.Fatal(err)
		}
		e.drain()
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = 'stalled'`); n != 1+noAnswerTries {
		t.Fatalf("%d re-search runs without an answer in 12 h, want %d", n, 1+noAnswerTries)
	}
	if n := e.count(`SELECT COUNT(*) FROM search_request`); n != 0 {
		t.Fatalf("%d rows left", n)
	}
}
