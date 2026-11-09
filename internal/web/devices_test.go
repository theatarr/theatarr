package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/arrview/nativeapi"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/settings"
)

type fakeDevices struct {
	imports     []nativeapi.ImportRequest
	generations []int64
}

func (f *fakeDevices) Decide(ctx context.Context, req nativeapi.DecideRequest) (*decide.Decision, error) {
	f.generations = append(f.generations, settings.ContextGeneration(ctx))
	if req.TitleID == 0 {
		return nil, fmt.Errorf("%w: title_id is required", nativeapi.ErrBadRequest)
	}
	return &decide.Decision{Release: req.Release, Version: req.Version, Accepted: true, Score: 42}, nil
}

func (f *fakeDevices) Import(ctx context.Context, req nativeapi.ImportRequest) (*nativeapi.ImportResult, error) {
	f.imports = append(f.imports, req)
	f.generations = append(f.generations, settings.ContextGeneration(ctx))
	return &nativeapi.ImportResult{Plan: &library.Plan{DryRun: true, Refused: "library.enabled is false"},
		Refused: "library.enabled is false"}, nil
}

func TestDecideAndImportAPI(t *testing.T) {
	t.Parallel()
	e := newEnvWith(t, func(c *Config) {
		c.CaptureRuntime = func(ctx context.Context) context.Context { return settings.WithGeneration(ctx, 23) }
	})
	key := e.apiKey()
	post := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", APIBase+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Api-Key", key)
		return e.do(r)
	}
	// Not wired: 503.
	if rec := post("/decide", `{"title_id":1,"version":"movie-web","release":"x"}`); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unwired %d", rec.Code)
	}
	f := &fakeDevices{}
	e.web.devices = f
	rec := post("/decide", `{"title_id":1,"version":"movie-web","release":"Alpha.2020.2160p.WEB-DL-GRP"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"score": 42`) && !strings.Contains(rec.Body.String(), `"score":42`) {
		t.Errorf("decide %d %s", rec.Code, rec.Body.String())
	}
	if len(f.generations) != 1 || f.generations[0] != 23 {
		t.Fatalf("decide did not keep its captured runtime: %v", f.generations)
	}
	if rec := post("/decide", `{"version":"movie-web","release":"x"}`); rec.Code != 400 {
		t.Errorf("bad decide %d", rec.Code)
	}
	if rec := post("/decide", `{"nope":1}`); rec.Code != 400 {
		t.Errorf("unknown field %d", rec.Code)
	}
	rec = post("/import", `{"title_id":1,"version":"movie-web","info_hash":"`+strings.Repeat("a", 40)+`"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "library.enabled is false") || len(f.imports) != 1 {
		t.Errorf("import %d %s", rec.Code, rec.Body.String())
	}
	if len(f.generations) != 3 || f.generations[2] != 23 {
		t.Fatalf("import did not keep its captured runtime: %v", f.generations)
	}
	// No key: 401.
	r := httptest.NewRequest("POST", APIBase+"/import", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	if rec := e.do(r); rec.Code != 401 {
		t.Errorf("no key %d", rec.Code)
	}
}

type manualDevices struct {
	fakeDevices
	calls       []string
	generations []int64
}

func (f *manualDevices) ServeManualImport(w http.ResponseWriter, r *http.Request) {
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.generations = append(f.generations, settings.ContextGeneration(r.Context()))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func TestManualImportNativePermissions(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	f := &manualDevices{}
	o.web.devices = f
	aid := o.account("admin-import", auth.RoleAdmin, auth.StatusActive, false)
	admin, csrf := o.session(aid)
	for _, route := range [][2]string{{"GET", "/manualimport?version=movie-web&folder=/mount"}, {"POST", "/manualimport?version=movie-web"}, {"GET", "/manualimport/options?version=movie-web"}, {"POST", "/manualimport/execute?version=movie-web"}} {
		path := APIBase + route[1]
		for _, c := range []*http.Cookie{o.mc, o.uc} {
			if code := o.jsonCall(route[0], path, []any{}, c, csrf, nil); code != http.StatusForbidden {
				t.Fatalf("non-admin %s: %d", path, code)
			}
		}
		if code := o.jsonCall(route[0], path, []any{}, nil, "", nil); code != http.StatusUnauthorized {
			t.Fatalf("anonymous %s: %d", path, code)
		}
		if route[0] == "POST" {
			if code := o.jsonCall(route[0], path, []any{}, admin, "", nil); code != http.StatusForbidden {
				t.Fatalf("without CSRF %s: %d", path, code)
			}
		}
		if len(f.calls) != 0 {
			t.Fatal("refused request reached import service")
		}
		if code := o.jsonCall(route[0], path, []any{}, admin, csrf, nil); code != http.StatusOK {
			t.Fatalf("admin %s: %d", path, code)
		}
		if len(f.calls) != 1 {
			t.Fatalf("calls %v", f.calls)
		}
		f.calls = nil
	}
}
