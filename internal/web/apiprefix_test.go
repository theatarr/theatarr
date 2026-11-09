package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
)

// TestOnlyNativeAPIPrefix: every /api/ route of the web package is under APIBase, and the native API answers under
// APIBase alone: the same path under any other /api/<name>/v1 prefix, or another version, is no route, even with a
// valid API key or session. (The arr views, Prowlarr and Seerr paths are mounted by their own packages.)
func TestOnlyNativeAPIPrefix(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	c := e.login()
	n := 0
	for _, rt := range e.web.Routes() {
		_, p := concrete(rt.Pattern)
		if strings.HasPrefix(p, "/api/") && !strings.HasPrefix(p, APIBase+"/") {
			t.Errorf("route %s is under /api/ but outside %s", rt.Pattern, APIBase)
		}
		if !rt.API {
			continue
		}
		n++
		method, path := concrete(rt.Pattern)
		rest, ok := strings.CutPrefix(path, APIBase)
		if !ok {
			t.Fatalf("API route %s outside %s", rt.Pattern, APIBase)
		}
		for _, base := range []string{"/api/other/v1", "/api/v1", "/api/theatarr/v2"} {
			for _, withKey := range []bool{true, false} {
				r := httptest.NewRequest(method, base+rest, nil)
				if withKey {
					r.Header.Set(auth.APIKeyHeader, key)
				} else {
					r.AddCookie(c)
				}
				if rec := e.do(r); rec.Code != http.StatusNotFound {
					t.Errorf("%s %s: %d, want 404", method, base+rest, rec.Code)
				}
			}
		}
	}
	if n < 25 {
		t.Fatalf("only %d API routes checked", n)
	}
}
