package web

import (
	"context"
	"database/sql/driver"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"modernc.org/sqlite"
)

var requestBatchFunctionID atomic.Int64

// A repeated title on a page must read its version states once. Count actual SQLite state evaluations so this
// catches N+1 reads without a timing threshold or a dependency on the query's spelling.
func TestRequestPageReadsEachTitleStateOnce(t *testing.T) {
	var reads atomic.Int64
	name := fmt.Sprintf("request_state_reads_%d", requestBatchFunctionID.Add(1))
	if err := sqlite.RegisterScalarFunction(name, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		reads.Add(1)
		return args[0], nil
	}); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	e.exec(`ALTER TABLE version RENAME TO request_state_source`)
	e.exec(`CREATE VIEW version AS SELECT title_id, policy_key, ` + name + `(state) AS state FROM request_state_source`)
	titleID := int64(1)
	items := make([]Request, 200)
	for i := range items {
		items[i] = Request{ID: int64(i + 1), TitleID: &titleID, keys: []string{"movie-remux", "movie-web"}, Status: ReqApproved}
	}
	if err := e.web.fillRequests(context.Background(), items, nil, true); err != nil {
		t.Fatal(err)
	}
	if n := reads.Load(); n != 2 {
		t.Fatalf("read %d states for 200 requests of one title; want its 2 states once", n)
	}
	for _, r := range items {
		if len(r.Versions) != 2 || r.Versions[0].State != "cutoff_met" || !r.Versions[0].Available || r.Versions[1].State != "wanted" {
			t.Fatalf("states: %+v", r)
		}
	}
}

func TestRequestBatchMembershipOrderingAndRedaction(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	bob := e.account("bob", auth.RoleUser, auth.StatusActive, false)
	cat := e.account("cat", auth.RoleUser, auth.StatusActive, false)
	e.exec(`INSERT INTO request (id,user_id,kind,title_id,name,status,created_at,updated_at) VALUES
		(101,1,'movie',1,'Alpha','approved',1,1),(102,1,'movie',1,'Alpha','pending',1,1),
		(103,1,'movie',1,'Alpha','canceled',1,1)`)
	e.exec(`INSERT INTO request_user (request_id,user_id,created_at) VALUES (101,?,2),(101,?,1)`, bob, cat)
	titleID := int64(1)
	for _, manager := range []bool{true, false} {
		items := []Request{
			{ID: 101, TitleID: &titleID, userID: 1, User: &RequestUser{ID: 1}, keys: []string{"movie-remux"}, Status: ReqApproved, Others: 2},
			{ID: 102, TitleID: &titleID, userID: 1, User: &RequestUser{ID: 1}, keys: []string{"movie-remux"}, Status: ReqPending},
			{ID: 103, TitleID: &titleID, userID: 1, User: &RequestUser{ID: 1}, keys: []string{"movie-remux"}, Status: ReqCanceled},
			{ID: 104, keys: []string{"movie-web"}, Status: ReqPending, posterPath: "/abcdefghij12.jpg"},
		}
		if err := e.web.fillRequests(context.Background(), items, &auth.Identity{UserID: bob}, manager); err != nil {
			t.Fatal(err)
		}
		if !items[0].Mine || !items[0].Versions[0].Available || items[1].Mine || items[1].Versions[0].State != "" ||
			items[2].Versions[0].State != "" || items[3].Poster != "/img/tmdb/w342/abcdefghij12.jpg" {
			t.Fatalf("manager=%v: %+v", manager, items)
		}
		if manager {
			if got := items[0].OtherUser; len(got) != 2 || got[0].ID != cat || got[1].ID != bob {
				t.Fatalf("requester order: %+v", got)
			}
		} else if items[0].Others != 0 || len(items[0].OtherUser) != 0 || items[1].User != nil || items[0].Versions[0].State != "filled" {
			t.Fatalf("redaction: %+v", items)
		}
	}
}
