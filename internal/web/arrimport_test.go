package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/arrimport"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/store"
)

// fakeArrImport blocks a run until release is closed.
type fakeArrImport struct {
	release chan struct{}
}

func (f *fakeArrImport) Check(_ context.Context, flavour, _, key string) (*arrimport.CheckResult, error) {
	if key != "sekrit-key" {
		return nil, &arrimport.CheckError{Msg: "Radarr did not answer: 401"}
	}
	return &arrimport.CheckResult{App: "Radarr", Version: "5.14", Titles: 3, WithFiles: 1,
		Profiles: []arrimport.CheckProf{{ID: 7, Name: "HD-1080p", Titles: 3}}}, nil
}

func (f *fakeArrImport) Validate(_ context.Context, req arrimport.Request) error {
	if req.Library != "movie-remux" {
		return &arrimport.Refusal{Msg: "Import needs the library to write links: it is Watch only."}
	}
	return nil
}

func (f *fakeArrImport) Run(_ context.Context, req arrimport.Request, progress func(arrimport.Progress)) (
	*arrimport.Report, error) {
	progress(arrimport.Progress{Phase: "importing", Done: 1, Total: 3})
	<-f.release
	return &arrimport.Report{DryRun: req.DryRun, App: "Radarr", Library: req.Library, AddedTitle: 2}, nil
}

func TestArrImportRoutes(t *testing.T) {
	t.Parallel()
	fake := &fakeArrImport{release: make(chan struct{})}
	e := newEnvWith(t, func(c *Config) { c.ArrImport = fake })
	admin := e.apiKey()
	res, err := e.st.Writer().Exec(`INSERT INTO web_user (username, password_hash, created_at, updated_at,
		password_changed_at, role) VALUES ('mgr', '', ?, ?, ?, ?)`, store.Millis(testNow), store.Millis(testNow),
		store.Millis(testNow), auth.RoleManager)
	if err != nil {
		t.Fatal(err)
	}
	mgrID, _ := res.LastInsertId()
	manager, _, err := e.auth.CreateKey(context.Background(), "k", mgrID)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, key, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, APIBase+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(auth.APIKeyHeader, key)
		return e.do(r)
	}
	start := `{"flavour":"radarr","url":"http://radarr:7878","api_key":"sekrit-key","library":"movie-remux",` +
		`"profiles":[7],"dry_run":true}`

	for _, c := range []struct{ method, path, body string }{{"GET", "/arr-import", ""},
		{"POST", "/arr-import", start}, {"POST", "/arr-import/check", `{}`}} {
		if rec := call(c.method, c.path, manager, c.body); rec.Code != http.StatusForbidden {
			t.Errorf("manager %s %s: %d", c.method, c.path, rec.Code)
		}
	}
	if rec := call("GET", "/arr-import", admin, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"run":null`) {
		t.Fatalf("no run yet: %d %s", rec.Code, rec.Body.String())
	}
	rec := call("POST", "/arr-import/check", admin, `{"flavour":"radarr","url":"http://radarr:7878","api_key":"sekrit-key"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"HD-1080p"`) {
		t.Fatalf("check: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call("POST", "/arr-import/check", admin, `{"flavour":"radarr","url":"http://radarr:7878","api_key":"x"}`); rec.Code != http.StatusBadGateway {
		t.Fatalf("check, no answer: %d", rec.Code)
	}
	refused := strings.Replace(start, "movie-remux", "watch", 1)
	if rec := call("POST", "/arr-import", admin, refused); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), "Watch only") {
		t.Fatalf("refusal: %d %s", rec.Code, rec.Body.String())
	}

	rec = call("POST", "/arr-import", admin, start)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"state":"running"`) {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call("POST", "/arr-import", admin, start); rec.Code != http.StatusConflict {
		t.Fatalf("second start while running: %d", rec.Code)
	}
	if rec := call("GET", "/arr-import", admin, ""); strings.Contains(rec.Body.String(), "sekrit") {
		t.Fatalf("GET shows the key: %s", rec.Body.String())
	}
	e.web.arrImportRuns.mu.Lock()
	done := e.web.arrImportRuns.done
	e.web.arrImportRuns.mu.Unlock()
	close(fake.release)
	<-done
	rec = call("GET", "/arr-import", admin, "")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"state":"done"`) || !strings.Contains(body, `"added_title":2`) ||
		strings.Contains(body, "sekrit") {
		t.Fatalf("finished run: %d %s", rec.Code, body)
	}
}
