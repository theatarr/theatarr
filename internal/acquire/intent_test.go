package acquire

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/importarr"
	"github.com/theatarr/theatarr/internal/policy"
)

// addTimeOnly is the deployment's loop: searches on add, then RSS only (no missing or cutoff-unmet selection).
func addTimeOnly(perHour int) *policy.Wanted {
	off := false
	return &policy.Wanted{PerHour: perHour, Missing: &off, CutoffUnmet: policy.CutoffOff}
}

// Add-time searches over the hourly cap are kept and run when the budget allows, oldest first, although the wanted
// loop selects nothing itself.
func TestAddTimeSearchesSurviveTheHourlyCap(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, addTimeOnly(1))
	if err := e.a.EnqueueFollowSearch(e.ctx, []importarr.FollowSearchTarget{{TitleID: 1, VersionKey: "movie", All: true},
		{TitleID: 2, VersionKey: "movie", All: true}}); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if got := e.searches(); len(got) != 1 || got[0].Title != "One" {
		t.Fatalf("first hour searches %+v", got)
	}
	e.advance(61 * time.Minute)
	if _, err := e.a.Wanted(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if got := e.searches(); len(got) != 2 || got[1].Title != "Two" {
		t.Fatalf("second hour searches %+v", got)
	}
	// Each add-time search ran once: later rounds search nothing.
	e.advance(61 * time.Minute)
	if _, err := e.a.Wanted(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if got := e.searches(); len(got) != 2 {
		t.Fatalf("searched again %+v", got)
	}
}

// An add-time search that fails is not repeated (the arrs search on add once, then rely on RSS); an intent whose
// title is no longer monitored is dropped without a search.
func TestAddTimeSearchBounded(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, addTimeOnly(1))
	e.onSearch = func(context.Context) error { return errors.New("indexer down") }
	if err := e.a.EnqueueFollowSearch(e.ctx, []importarr.FollowSearchTarget{{TitleID: 1, VersionKey: "movie", All: true},
		{TitleID: 2, VersionKey: "movie", All: true}}); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if _, err := e.st.Writer().ExecContext(e.ctx, `UPDATE version SET monitored = 0 WHERE id = 102`); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		e.advance(8 * 24 * time.Hour)
		if _, err := e.a.Wanted(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.drain()
	}
	if got := e.searches(); len(got) != 1 || got[0].Title != "One" {
		t.Fatalf("searches %+v", got)
	}
}

// TestInfraFailedSearchesNotCountedAsSearched: a search the VPN guard refused, or one no indexer
// answered, is failed but not counted as searched. The wanted loop finds the item due again at its next round
// instead of after again_after, and an add-time intent is kept and started again, until a search gets an answer.
func TestInfraFailedSearchesNotCountedAsSearched(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 200})
	e.onSearch = func(context.Context) error { return fmt.Errorf("search: %w", gateway.ErrVPNRequired) }
	sum, err := e.a.Wanted(e.ctx)
	if err != nil || sum.Queued == 0 {
		t.Fatalf("round 1: %+v %v", sum, err)
	}
	e.drain()
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE state = 'failed' AND infra = 1`); n == 0 {
		t.Fatal("no infrastructure failure recorded")
	}
	e.onSearch = nil
	e.advance(61 * time.Minute)
	sum2, err := e.a.Wanted(e.ctx)
	if err != nil || sum2.Due != sum.Due {
		t.Fatalf("round 2 due %d, want %d again (%v)", sum2.Due, sum.Due, err)
	}
}

func TestInfraFailedAddTimeSearchIsKept(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, addTimeOnly(10))
	e.noAnswer = true
	if err := e.a.EnqueueFollowSearch(e.ctx, []importarr.FollowSearchTarget{{TitleID: 1, VersionKey: "movie",
		All: true}}); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE infra = 1 AND message LIKE '%no indexer answered%'`); n != 1 {
		t.Fatalf("infra runs %d", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM follow_search_intent`); n != 1 {
		t.Fatal("the intent was dropped after a search no indexer answered")
	}
	e.noAnswer = false
	e.advance(time.Minute)
	if _, err := e.a.DrainFollowSearches(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if got := e.searches(); len(got) != 1 {
		t.Fatalf("searches %+v, want the add-time search held for infraBackoff", got)
	}
	e.advance(infraRetry)
	if _, err := e.a.DrainFollowSearches(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if got := e.searches(); len(got) != 2 {
		t.Fatalf("searches %+v, want the add-time search again", got)
	}
	if _, err := e.a.DrainFollowSearches(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM follow_search_intent`); n != 0 {
		t.Fatal("the intent was kept after an answered search")
	}
}

// TestNoAnswerAddTimeSearchGivesUp: an add-time search that never gets an answer is started again
// after 15 m, 1 h and 4 h, then given up, instead of at every drain for ever.
func TestNoAnswerAddTimeSearchGivesUp(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, addTimeOnly(100))
	e.noAnswer = true
	if err := e.a.EnqueueFollowSearch(e.ctx, []importarr.FollowSearchTarget{{TitleID: 1, VersionKey: "movie",
		All: true}}); err != nil {
		t.Fatal(err)
	}
	e.drain()
	for range 48 { // twelve hours of drains every 15 minutes
		e.advance(15*time.Minute + time.Second)
		if _, err := e.a.DrainFollowSearches(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.drain()
	}
	if got := e.searches(); len(got) != 1+len(infraBackoff) {
		t.Fatalf("%d searches of an unanswered add-time search in 12 h, want %d", len(got), 1+len(infraBackoff))
	}
	if n := e.count(`SELECT COUNT(*) FROM follow_search_intent`); n != 0 {
		t.Fatal("the intent was kept after its retries were spent")
	}
}
