package web

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/store"
)

type wantedAcquirer struct {
	fakeAcquirer
	queries []acquire.WantedQuery
}

func (f *wantedAcquirer) WantedList(_ context.Context, q acquire.WantedQuery) (*acquire.WantedList, error) {
	f.queries = append(f.queries, q)
	return &acquire.WantedList{Items: []acquire.WantedRow{{TitleID: 1, Version: "movie-web", Targets: []string{"movie"}, Reason: q.Only, Waiting: "due", Position: 2}}, Total: 3, Counts: map[string]int{"due": 3}, Limit: q.Limit, Offset: q.Offset}, nil
}

func TestWantedAPI(t *testing.T) {
	t.Parallel()
	acq := &wantedAcquirer{}
	e := newEnvWith(t, func(c *Config) { c.Add.Acquirer = acq })
	key := e.apiKey()
	for _, only := range []string{"", "wrong"} {
		if code := e.getKey(APIBase+"/wanted?only="+only, key, nil); code != 400 {
			t.Errorf("bad only: %d", code)
		}
	}
	if code := e.getKey(APIBase+"/wanted?only=missing&waiting=wrong", key, nil); code != 400 {
		t.Errorf("bad waiting: %d", code)
	}
	var out WantedPage
	if code := e.getKey(APIBase+"/wanted?only=missing&version=movie-web&waiting=due&limit=1&offset=1", key, &out); code != 200 {
		t.Fatalf("wanted %d", code)
	}
	if len(acq.queries) != 1 || acq.queries[0] != (acquire.WantedQuery{Only: "missing", Version: "movie-web", Waiting: "due", Limit: 1, Offset: 1}) {
		t.Fatalf("query %+v", acq.queries)
	}
	if out.Total != 3 || out.Limit != 1 || out.Offset != 1 || len(out.Items) != 1 || out.Titles[1] != "Alpha" || out.Counts["due"] != 3 {
		t.Fatalf("page %+v", out)
	}
	if rec := e.do(httptest.NewRequest("GET", APIBase+"/wanted?only=missing", nil)); rec.Code != 401 {
		t.Errorf("anonymous %d", rec.Code)
	}
	for _, role := range []string{auth.RoleUser, auth.RoleManager} {
		uid := e.account(role, role, auth.StatusActive, true)
		cookie, _ := e.session(uid)
		req := httptest.NewRequest("GET", APIBase+"/wanted?only=missing", nil)
		req.AddCookie(cookie)
		if rec := e.do(req); rec.Code != 403 {
			t.Errorf("%s %d", role, rec.Code)
		}
	}
	if len(acq.queries) != 1 {
		t.Fatal("unauthorized request reached acquirer")
	}
}

func TestTitleSearchDecisionHandoff(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	now := store.Millis(testNow)
	e.exec(`INSERT INTO acquire_run(id,source,title_id,version_id,version_key,target,state,results,created_at,updated_at)
 VALUES(21,'wanted',1,2,'movie-web','movie','nothing',3,?,?)`, now, now)
	e.exec(`INSERT INTO journal_decision(id,source,run_id,title_id,version_key,target,created_at)
 VALUES(71,'acquire:21',21,1,'movie-web','movie',?)`, now)
	key := e.apiKey()
	var out TitleDetail
	if code := e.getKey(APIBase+"/titles/1", key, &out); code != 200 {
		t.Fatalf("title %d", code)
	}
	found := false
	for _, v := range out.Versions {
		for _, h := range v.History {
			if h.Event == "searched" {
				found = true
				if h.RunID == nil || *h.RunID != 21 || h.DecisionID == nil || *h.DecisionID != 71 {
					t.Errorf("history %+v", h)
				}
			}
		}
	}
	if !found {
		t.Fatal("search absent from title history")
	}
	var entry journal.Entry
	if code := e.getKey(APIBase+"/journal/71", key, &entry); code != 200 || entry.ID != "run:21" {
		t.Fatalf("handoff %d %+v", code, entry)
	}
	e.exec(`UPDATE acquire_run SET state = 'routed', accepted = 1 WHERE id = 21`)
	if code := e.getKey(APIBase+"/journal/71", key, &entry); code != 200 || entry.ID != "run:21" {
		t.Fatalf("routed handoff %d %+v", code, entry)
	}
	if code := e.getKey(APIBase+"/journal/999", key, nil); code != 404 {
		t.Errorf("missing decision %d", code)
	}
}

func TestTitleSearchHistoryKeepsReleaseEvents(t *testing.T) {
	e := newEnv(t)
	now := store.Millis(testNow)
	e.exec(`DELETE FROM arr_history WHERE title_id=1`)
	for i := 1; i <= 3; i++ {
		e.exec(`INSERT INTO arr_history(instance,arr_id,event_type,date_at,title_id,version_id,source_title,quality) VALUES('radarr_4k_debrid',?,'grabbed',?,1,1,'Release','Remux-2160p')`, i, now-int64(1000+i))
	}
	for i := 1; i <= 51; i++ {
		e.exec(`INSERT INTO acquire_run(id,source,title_id,version_id,version_key,target,state,created_at,updated_at) VALUES(?,'wanted',1,1,'movie-remux','movie','nothing',?,0)`, i, now+int64(i))
	}
	var out TitleDetail
	if code := e.getKey(APIBase+"/titles/1", e.apiKey(), &out); code != 200 {
		t.Fatalf("title %d", code)
	}
	searches, releases := 0, 0
	for _, v := range out.Versions {
		for _, h := range v.History {
			if h.Event == "searched" {
				searches++
				if h.RunID == nil || *h.RunID < 42 {
					t.Fatalf("old search %+v", h)
				}
			} else {
				releases++
			}
		}
	}
	if searches != 10 || releases != 3 {
		t.Fatalf("searches %d releases %d", searches, releases)
	}
}
