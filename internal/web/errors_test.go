package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/errinbox"
)

// seedErrors writes two inbox rows through the inbox (a log error and a panic with a stack) and returns their ids.
func seedErrors(t *testing.T, e *env) (logID, panicID int64) {
	t.Helper()
	in := errinbox.New(e.st, errinbox.Options{Version: "test", Now: func() time.Time { return testNow }})
	in.Report(errinbox.Event{Source: errinbox.SourceLog, Component: "router", Level: "ERROR",
		Message: "route failed for title 12: disk full", Context: map[string]string{"title": "12"}, Time: testNow})
	in.Report(errinbox.Event{Source: errinbox.SourcePanic, Component: "jobs", Level: "ERROR", Message: "boom",
		Stack: "goroutine 1 [running]:\npanic({0x0, 0x0})\ngithub.com/theatarr/theatarr/internal/jobs.run()\n", Time: testNow})
	if err := in.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, err := errinbox.List(context.Background(), e.st, errinbox.Filter{})
	if err != nil || len(p.Items) != 2 {
		t.Fatalf("seed: %v %+v", err, p)
	}
	for _, r := range p.Items {
		if r.Source == errinbox.SourceLog {
			logID = r.ID
		} else {
			panicID = r.ID
		}
	}
	return logID, panicID
}

func TestErrorsAPI(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	logID, panicID := seedErrors(t, e)
	key := e.apiKey()

	var page errinbox.Page
	if code := e.getKey(APIBase+"/errors?status=new", key, &page); code != 200 || page.Total != 2 ||
		page.Counts["new"] != 2 || len(page.Items) != 2 {
		t.Fatalf("list: %d %+v", code, page)
	}
	for _, r := range page.Items {
		if r.Stack != "" {
			t.Errorf("list carries a stack: %+v", r)
		}
	}
	if code := e.getKey(APIBase+"/errors?source=panic&since=24h", key, &page); code != 200 || len(page.Items) != 1 ||
		page.Items[0].ID != panicID {
		t.Fatalf("panic filter: %d %+v", code, page)
	}
	for _, q := range []string{"status=open&limit=x", "status=bogus", "source=disk", "since=yesterday"} {
		if code := e.getKey(APIBase+"/errors?"+q, key, nil); code != 400 {
			t.Errorf("%s: %d", q, code)
		}
	}
	var row errinbox.Row
	if code := e.getKey(APIBase+"/errors/"+itoa(panicID), key, &row); code != 200 ||
		!strings.Contains(row.Stack, "internal/jobs.run") || row.Count != 1 {
		t.Fatalf("get: %d %+v", code, row)
	}
	if code := e.getKey(APIBase+"/errors/999", key, nil); code != 404 {
		t.Errorf("missing: %d", code)
	}

	c := e.login()
	csrf := e.csrfFor(c)
	note := "fixed in 1.2.3"
	if code := e.jsonCall("POST", APIBase+"/errors/"+itoa(logID), ErrorUpdate{Status: "resolved", Note: &note}, c, csrf,
		&row); code != 200 || row.Status != "resolved" || row.ResolvedAt == nil || row.Note != note {
		t.Fatalf("resolve: %d %+v", code, row)
	}
	if code := e.jsonCall("POST", APIBase+"/errors/"+itoa(logID), ErrorUpdate{Status: "gone"}, c, csrf, nil); code != 400 {
		t.Errorf("bad status: %d", code)
	}
	if code := e.jsonCall("POST", APIBase+"/errors/"+itoa(logID), ErrorUpdate{}, c, csrf, nil); code != 400 {
		t.Errorf("empty update: %d", code)
	}
	if code := e.jsonCall("POST", APIBase+"/errors/"+itoa(logID), ErrorUpdate{Status: "seen"}, c, "", nil); code != 403 {
		t.Errorf("no CSRF: %d", code)
	}

	var bulk map[string]int64
	if code := e.jsonCall("POST", APIBase+"/errors/bulk", map[string]any{"status": "resolved"}, c, csrf, nil); code != 400 {
		t.Errorf("bulk without ids or where: %d", code)
	}
	if code := e.jsonCall("POST", APIBase+"/errors/bulk", map[string]any{"status": "resolved", "where": map[string]any{}},
		c, csrf, nil); code != 400 {
		t.Errorf("bulk with an empty filter: %d", code)
	}
	if code := e.jsonCall("POST", APIBase+"/errors/bulk", map[string]any{"status": "resolved",
		"where": map[string]any{"status": "open"}}, c, csrf, &bulk); code != 200 || bulk["changed"] != 1 {
		t.Fatalf("bulk resolve open: %d %v", code, bulk)
	}
	if code := e.jsonCall("POST", APIBase+"/errors/bulk", map[string]any{"status": "ignored", "ids": []int64{logID, panicID}},
		c, csrf, &bulk); code != 200 || bulk["changed"] != 2 {
		t.Fatalf("bulk ignore ids: %d %v", code, bulk)
	}
	if code := e.getKey(APIBase+"/errors", key, &page); code != 200 || page.Counts["ignored"] != 2 {
		t.Fatalf("after bulk: %d %+v", code, page)
	}

	// Admins only: a user's session and key get 403.
	uid := e.account("viewer", "user", "active", false)
	uc, ucsrf := e.session(uid)
	if code := e.get(APIBase+"/errors", uc).Code; code != 403 {
		t.Errorf("user list: %d", code)
	}
	if code := e.jsonCall("POST", APIBase+"/errors/"+itoa(logID), ErrorUpdate{Status: "new"}, uc, ucsrf, nil); code != 403 {
		t.Errorf("user update: %d", code)
	}
}

func TestErrorsMCP(t *testing.T) {
	t.Parallel()
	se := seededSectionsEnv(t)
	logID, _ := seedErrors(t, se.env)
	data := se.mcpTool("errors_list", map[string]any{"status": "open"}, 200).(map[string]any)
	if data["total"].(float64) != 2 {
		t.Fatalf("errors_list: %v", data)
	}
	got := se.mcpTool("errors_get", map[string]any{"id": logID}, 200).(map[string]any)
	if got["component"] != "router" {
		t.Fatalf("errors_get: %v", got)
	}
	res := se.mcpTool("errors_resolve", map[string]any{"id": logID, "note": "fixed"}, 200).(map[string]any)
	if res["status"] != "resolved" || res["note"] != "fixed" {
		t.Fatalf("errors_resolve: %v", res)
	}
	se.mcpTool("errors_get", map[string]any{"id": 999}, 404)
}
