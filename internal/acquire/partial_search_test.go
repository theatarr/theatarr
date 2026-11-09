package acquire

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
)

// TestPartialSearchCountsAsSearchedSooner: a partial answer (some indexer
// failed) records acquire_run.infra = runPartial, still routes what it accepted, and counts as searched for
// partialAgain: the wanted loop does not take the item at the next round, but does after partialAgain, well before
// again_after.
func TestPartialSearchCountsAsSearchedSooner(t *testing.T) {
	t.Parallel()
	for _, fanout := range []bool{false, true} {
		for _, accepted := range []bool{false, true} {
			t.Run(fmt.Sprintf("fanout=%v/accepted=%v", fanout, accepted), func(t *testing.T) {
				e := newEnv(t, true, &policy.Wanted{PerHour: 200, AgainAfter: policy.Duration(7 * 24 * time.Hour)})
				e.accept = accepted
				search := e.a.search
				e.a.search = func(ctx context.Context, title decide.Title, v decide.Version, f *decide.ExistingFile, ss *decide.SeriesSearch) (*router.Searched, error) {
					got, err := search(ctx, title, v, f, ss)
					if got != nil {
						got.Incomplete = true
					}
					return got, err
				}
				targets := []target{{titleID: 1, versionID: 101, key: "movie", name: "movie"}}
				want := 1
				if fanout {
					vp := *e.pol.Versions["movie"]
					e.pol.Versions["alternate"] = &vp
					if _, err := e.st.Writer().Exec(`INSERT INTO version (id,title_id,policy_key,state,created_at,updated_at) VALUES (121,1,'alternate','wanted',0,0)`); err != nil {
						t.Fatal(err)
					}
					targets = append(targets, target{titleID: 1, versionID: 121, key: "alternate", name: "movie"})
					want = 2
				}
				if err := e.a.startGroup(e.ctx, "wanted", 0, targets, ""); err != nil {
					t.Fatal(err)
				}
				e.drain()
				if n := e.count(fmt.Sprintf(`SELECT COUNT(*) FROM acquire_run WHERE title_id=1 AND infra=%d AND state <> 'failed'`, runPartial)); n != want {
					t.Fatalf("partial runs %d, want %d", n, want)
				}
				if accepted && e.count(`SELECT COUNT(*) FROM acquire_run WHERE title_id=1 AND state='routed'`) == 0 {
					t.Fatal("partial answer lost usable candidates")
				}
				if accepted {
					return // in flight now
				}
				before := e.count(`SELECT COUNT(*) FROM acquire_run WHERE title_id=1 AND search_run IS NULL`)
				e.advance(time.Hour)
				if _, err := e.a.Wanted(e.ctx); err != nil {
					t.Fatal(err)
				}
				if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE title_id=1 AND search_run IS NULL`); n != before {
					t.Fatalf("a partial search an hour ago was not counted as searched (%d runs, was %d)", n, before)
				}
				e.advance(partialAgain)
				if _, err := e.a.Wanted(e.ctx); err != nil {
					t.Fatal(err)
				}
				if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE title_id=1 AND search_run IS NULL`); n != before+1 {
					t.Fatalf("not due again after partialAgain (%d runs, was %d)", n, before)
				}
			})
		}
	}
}
