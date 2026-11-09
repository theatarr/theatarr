package acquire

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
)

// One indexer of several failing marks every search Incomplete. Such a search counts as searched (for
// partialAgain), so the wanted loop moves on through the backlog instead of searching the same items every round.
func TestPartialSearchDoesNotStarveBacklog(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 2})
	e.accept = false
	for i := 20; i < 30; i++ {
		for _, q := range []string{
			fmt.Sprintf(`INSERT INTO title (id, kind, name, year, created_at, updated_at) VALUES (%d, 'movie', 'M%d', 2024, 0, 0)`, i, i),
			fmt.Sprintf(`INSERT INTO title_release_date (title_id, source, type, date) VALUES (%d, 'fixture', 'digital', '2024-01-01')`, i),
			fmt.Sprintf(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (%d, %d, 'movie', 'wanted', %d, 0)`, 1000+i, i, i),
		} {
			if _, err := e.st.Writer().Exec(q); err != nil {
				t.Fatal(err)
			}
		}
	}
	search := e.a.search
	e.a.search = func(ctx context.Context, title decide.Title, v decide.Version, f *decide.ExistingFile, ss *decide.SeriesSearch) (*router.Searched, error) {
		got, err := search(ctx, title, v, f, ss)
		if got != nil {
			got.Incomplete = true // e.g. DMM page_cap 200 reached on a popular title
		}
		return got, err
	}
	for h := 0; h < 8; h++ {
		if _, err := e.a.Wanted(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.drain()
		e.advance(61 * time.Minute)
	}
	per := map[string]int{}
	for _, s := range e.searches() {
		per[s.Title]++
	}
	t.Logf("searches per title over 8 hourly rounds: %v", per)
	if len(per) < 6 {
		t.Fatalf("backlog starved: only %d distinct titles searched in 8 rounds of 2/hour (%v)", len(per), per)
	}
}

// A re-search whose searches are always partial counts as searched; it is not asked for again every
// infraRetry with no end.
func TestPartialResearchDoesNotRepeat(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 100, AgainAfter: policy.Duration(24 * time.Hour)})
	e.accept = false
	search := e.a.search
	e.a.search = func(ctx context.Context, title decide.Title, v decide.Version, f *decide.ExistingFile, ss *decide.SeriesSearch) (*router.Searched, error) {
		got, err := search(ctx, title, v, f, ss)
		if got != nil {
			got.Incomplete = true
		}
		return got, err
	}
	e.research(router.ResearchRequest{TitleID: 1, VersionID: 101, Version: "movie", Reason: router.ReasonStalled})
	e.drain()
	for i := 0; i < 24; i++ { // six hours
		e.advance(infraRetry + time.Second)
		e.drain()
	}
	n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = 'stalled'`)
	t.Logf("re-search runs of one stalled download in 6h: %d", n)
	if n > 3 {
		t.Fatalf("one re-search became %d searches in 6 hours", n)
	}
}

// Searches that get no answer (the VPN guard refusing them) are not counted as searched, but the wanted
// loop orders by the last attempt of any kind, so the same newest items are not retried at every round ahead of the
// rest.
func TestNoAnswerSearchDoesNotStarveBacklog(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 2})
	e.accept = false
	for i := 20; i < 30; i++ {
		for _, q := range []string{
			fmt.Sprintf(`INSERT INTO title (id, kind, name, year, created_at, updated_at) VALUES (%d, 'movie', 'M%d', 2024, 0, 0)`, i, i),
			fmt.Sprintf(`INSERT INTO title_release_date (title_id, source, type, date) VALUES (%d, 'fixture', 'digital', '2024-01-01')`, i),
			fmt.Sprintf(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (%d, %d, 'movie', 'wanted', %d, 0)`, 1000+i, i, i),
		} {
			if _, err := e.st.Writer().Exec(q); err != nil {
				t.Fatal(err)
			}
		}
	}
	e.onSearch = func(context.Context) error { return fmt.Errorf("search: %w", gateway.ErrVPNRequired) }
	for h := 0; h < 8; h++ {
		if _, err := e.a.Wanted(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.drain()
		e.advance(61 * time.Minute)
	}
	per := map[int64]int{}
	rows, err := e.st.Reader().Query(`SELECT title_id FROM acquire_run WHERE search_run IS NULL`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		per[id]++
	}
	rows.Close()
	if len(per) < 10 {
		t.Fatalf("backlog starved: only %d distinct titles attempted in 8 rounds of 2/hour (%v)", len(per), per)
	}
}
