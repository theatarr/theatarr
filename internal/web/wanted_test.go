package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/store"
)

type wantedAcquirer struct {
	fakeAcquirer
	queries []acquire.WantedQuery
	only    string
	items   []acquire.SearchNowItem
	all     []acquire.WantedQuery
	cleared int
	off     bool
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
	// Managers and admins; users are refused.
	for role, want := range map[string]int{auth.RoleUser: 403, auth.RoleManager: 200} {
		uid := e.account(role, role, auth.StatusActive, true)
		cookie, _ := e.session(uid)
		req := httptest.NewRequest("GET", APIBase+"/wanted?only=missing", nil)
		req.AddCookie(cookie)
		if rec := e.do(req); rec.Code != want {
			t.Errorf("%s %d, want %d", role, rec.Code, want)
		}
	}
	if len(acq.queries) != 2 {
		t.Fatalf("queries %d: only the manager's request reaches the acquirer", len(acq.queries))
	}
}

func (f *wantedAcquirer) RequestSearchNow(_ context.Context, only string, items []acquire.SearchNowItem) (*acquire.SearchNowResult, error) {
	if f.off {
		return nil, acquire.ErrDownloadsOff
	}
	f.only, f.items = only, items
	return &acquire.SearchNowResult{Queued: len(items), Started: 1, Pending: len(items) - 1, Skipped: []acquire.SearchNowSkip{}}, nil
}

func (f *wantedAcquirer) RequestSearchNowAll(_ context.Context, q acquire.WantedQuery) (*acquire.SearchNowResult, error) {
	if f.off {
		return nil, acquire.ErrDownloadsOff
	}
	f.all = append(f.all, q)
	return &acquire.SearchNowResult{Queued: 7, Skipped: []acquire.SearchNowSkip{{TitleID: 2, Version: "movie-web", Reason: "x"}}}, nil
}

func (f *wantedAcquirer) ClearSearchNow(context.Context) (int, error) {
	f.cleared++
	return 4, nil
}

func TestWantedSearchAPI(t *testing.T) {
	t.Parallel()
	acq := &wantedAcquirer{}
	e := newEnvWith(t, func(c *Config) { c.Add.Acquirer = acq })
	key := e.apiKey()
	post := func(body string) (int, map[string]any) {
		t.Helper()
		return e.postKeyJSON(APIBase+"/wanted/search", key, body)
	}
	many := `{"only":"missing","items":[` + strings.Repeat(`{"title_id":1,"version":"movie-web"},`, 200) + `{"title_id":1,"version":"movie-web"}]}`
	for name, body := range map[string]string{
		"bad only":      `{"only":"wrong","items":[{"title_id":1,"version":"movie-web"}]}`,
		"neither":       `{"only":"missing"}`,
		"both":          `{"only":"missing","items":[{"title_id":1,"version":"movie-web"}],"all":{}}`,
		"no items":      `{"only":"missing","items":[]}`,
		"too many":      many,
		"no version":    `{"only":"missing","items":[{"title_id":1}]}`,
		"bad season":    `{"only":"missing","items":[{"title_id":1,"version":"movie-web","season":-1}]}`,
		"bad waiting":   `{"only":"cutoff","all":{"waiting":"wrong"}}`,
		"unknown field": `{"only":"missing","all":{},"force":true}`,
	} {
		if code, _ := post(body); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if acq.items != nil || len(acq.all) != 0 {
		t.Fatal("an invalid request reached the acquirer")
	}
	code, out := post(`{"only":"cutoff","items":[{"title_id":1,"version":"movie-web"},{"title_id":3,"version":"series-web","season":2}]}`)
	if code != 200 || out["queued"] != float64(2) || out["started"] != float64(1) || out["pending"] != float64(1) || out["next_at"] != nil {
		t.Fatalf("items %d %+v", code, out)
	}
	if skipped, ok := out["skipped"].([]any); !ok || len(skipped) != 0 {
		t.Fatalf("skipped %+v", out["skipped"])
	}
	if acq.only != "cutoff" || len(acq.items) != 2 || acq.items[0].Season != nil || acq.items[1].Season == nil || *acq.items[1].Season != 2 {
		t.Fatalf("items %s %+v", acq.only, acq.items)
	}
	if code, out = post(`{"only":"missing","all":{"version":"movie-web","waiting":"due"}}`); code != 200 || out["queued"] != float64(7) {
		t.Fatalf("all %d %+v", code, out)
	}
	if len(acq.all) != 1 || acq.all[0] != (acquire.WantedQuery{Only: "missing", Version: "movie-web", Waiting: "due"}) {
		t.Fatalf("all query %+v", acq.all)
	}
	acq.off = true
	if code, out = post(`{"only":"missing","all":{}}`); code != 409 || out["error"] == "" {
		t.Fatalf("downloads off %d %+v", code, out)
	}
	del := func(c *http.Cookie, csrf string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("DELETE", APIBase+"/wanted/search", nil)
		if c != nil {
			req.AddCookie(c)
			req.Header.Set(auth.CSRFHeader, csrf)
		} else {
			req.Header.Set(auth.APIKeyHeader, key)
		}
		return e.do(req)
	}
	if rec := del(nil, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"cleared":4`) || acq.cleared != 1 {
		t.Fatalf("clear %d %s", rec.Code, rec.Body.String())
	}
	// Managers and admins; users are refused.
	acq.off = false
	for _, role := range []string{auth.RoleUser, auth.RoleManager} {
		want := 403
		if role == auth.RoleManager {
			want = 200
		}
		uid := e.account(role, role, auth.StatusActive, true)
		cookie, csrf := e.session(uid)
		if code := e.jsonCall("POST", APIBase+"/wanted/search", map[string]any{"only": "missing", "all": map[string]any{}}, cookie, csrf, nil); code != want {
			t.Errorf("%s search %d, want %d", role, code, want)
		}
		if rec := del(cookie, csrf); rec.Code != want {
			t.Errorf("%s clear %d, want %d", role, rec.Code, want)
		}
	}
	if len(acq.all) != 2 || acq.cleared != 2 {
		t.Fatalf("all %d cleared %d: only the manager's requests reach the acquirer", len(acq.all), acq.cleared)
	}
	// Without an acquirer that can queue searches: 503.
	bare := newEnv(t)
	if code, _ := bare.postKeyJSON(APIBase+"/wanted/search", bare.apiKey(), `{"only":"missing","all":{}}`); code != 503 {
		t.Errorf("no acquirer: %d", code)
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
	for i := 1; i <= 3; i++ {
		e.exec(`INSERT INTO download(id,title_id,version_id,version_key,info_hash,release_title,indexer,quality,state,episodes,created_at,updated_at,added_at) VALUES(?,1,1,'movie-remux',printf('%040x',?),'Release','dmm','Remux-2160p','added','[]',?,?,?)`, 900+i, i, now-int64(1000+i), now-int64(1000+i), now-int64(1000+i))
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
