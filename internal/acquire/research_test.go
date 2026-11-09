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
)

func (e *env) research(p router.ResearchRequest) *ResearchSummary {
	e.t.Helper()
	b, _ := json.Marshal(p)
	if err := e.a.handleResearch(e.ctx, &jobs.Job{Kind: router.JobResearch, Payload: b}); err != nil {
		e.t.Fatal(err)
	}
	sum, err := e.a.Research(context.Background(), router.ResearchRequest{Version: "nope"})
	if err != nil || sum.Off == "" {
		e.t.Fatalf("unknown version: %+v %v", sum, err)
	}
	return sum
}

// A re-search after a failure searches the title again at once, as a wanted search: active versions only, with the
// router on, within per_hour, once per target within again_after, never with a download in flight.
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
	// Its route added a download: in flight, so the same failure asked again is skipped; and once that ends, the
	// target was searched again within again_after: skipped too.
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
// one-per-again_after limit, and is asked for again after infraRetry; it runs then.
func TestResearchInfraFailureAskedAgain(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 3, AgainAfter: policy.Duration(6 * time.Hour)})
	e.onSearch = func(context.Context) error { return gateway.ErrVPNRequired }
	e.research(router.ResearchRequest{TitleID: 1, VersionID: 101, Version: "movie", Reason: router.ReasonStalled})
	e.drain()
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = 'stalled' AND infra = 1`); n != 1 {
		t.Fatalf("%d infra-failed re-searches", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = ? AND state = 'queued'`, router.JobResearch); n != 1 {
		t.Fatalf("%d re-search jobs queued again", n)
	}
	e.onSearch = nil
	e.advance(infraRetry + time.Second)
	e.drain()
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = 'stalled' AND infra = 0`); n != 1 {
		t.Fatalf("%d re-searches after the retry", n)
	}
}

// TestNoAnswerResearchGivesUp: a re-search that never gets an answer is asked for again after 15 m,
// 1 h and 4 h, then not again.
func TestNoAnswerResearchGivesUp(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 100, AgainAfter: policy.Duration(24 * time.Hour)})
	e.onSearch = func(context.Context) error { return gateway.ErrVPNRequired }
	e.research(router.ResearchRequest{TitleID: 1, VersionID: 101, Version: "movie", Reason: router.ReasonStalled})
	e.drain()
	for range 48 { // twelve hours
		e.advance(15*time.Minute + time.Second)
		e.drain()
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = 'stalled'`); n != 1+len(infraBackoff) {
		t.Fatalf("%d re-search runs without an answer in 12 h, want %d", n, 1+len(infraBackoff))
	}
	if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = ? AND state = 'queued'`, router.JobResearch); n != 0 {
		t.Fatalf("%d re-search jobs still queued", n)
	}
}
