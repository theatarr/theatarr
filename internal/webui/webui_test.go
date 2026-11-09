package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func serve(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestNotBuilt(t *testing.T) {
	h := New(fstest.MapFS{".keep": {}})
	if h.Built() {
		t.Fatal("an empty bundle counts as built")
	}
	rec := serve(h, "GET", "/admin/system")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "UI not built") ||
		strings.Contains(rec.Body.String(), `/classic/`) || strings.Contains(rec.Body.String(), `/static/`) {
		t.Errorf("not built: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "<script") || strings.Contains(rec.Body.String(), "style=") {
		t.Error("the not-built page has inline script or style")
	}
}

// The committed bundle is only dist/.keep; this keeps it that way, so go test never depends on a node build.
func TestEmbeddedBundleOpens(t *testing.T) {
	b := Bundle()
	if _, err := b.Open(".keep"); err != nil && !Built(b) {
		t.Errorf("dist/.keep missing: %v", err)
	}
}

func TestServe(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":              {Data: []byte(`<!doctype html><script type="module" src="/assets/index-abc123.js"></script>`)},
		"assets/index-abc123.js":  {Data: []byte(`console.log(1)`)},
		"assets/index-abc123.css": {Data: []byte(`body{}`)},
		"icon.svg":                {Data: []byte(`<svg/>`)},
	}
	h := New(fsys)
	for _, c := range []struct {
		method, path string
		code         int
		cache, body  string
	}{
		{"GET", "/", 200, "no-cache", "index-abc123.js"},
		{"GET", "/admin/system", 200, "no-cache", "index-abc123.js"},
		{"GET", "/title/12", 200, "no-cache", "<!doctype html>"},
		{"HEAD", "/movies", 200, "no-cache", ""},
		{"GET", "/index.html", 200, "no-cache", "<!doctype html>"},
		{"GET", "/assets/index-abc123.js", 200, "public, max-age=31536000, immutable", "console.log"},
		{"GET", "/assets/index-abc123.css", 200, "public, max-age=31536000, immutable", "body{}"},
		{"GET", "/icon.svg", 200, "public, max-age=3600", "<svg/>"},
		{"GET", "/assets/index-old.js", 404, "no-store", ""},
		{"GET", "/api/theatarr/v1/nope", 404, "no-store", `"error"`},
		{"GET", "/classic/nope", 404, "", ""},
		{"GET", "/radarr/api/v3/nope", 404, "", ""},
		{"GET", "/healthz", 404, "", ""},
		{"POST", "/", 405, "", ""},
	} {
		rec := serve(h, c.method, c.path)
		if rec.Code != c.code {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, rec.Code, c.code)
			continue
		}
		if c.cache != "" && rec.Header().Get("Cache-Control") != c.cache {
			t.Errorf("%s %s: Cache-Control %q, want %q", c.method, c.path, rec.Header().Get("Cache-Control"), c.cache)
		}
		if c.body != "" && !strings.Contains(rec.Body.String(), c.body) {
			t.Errorf("%s %s: body %q lacks %q", c.method, c.path, rec.Body.String(), c.body)
		}
	}
	if ct := serve(h, "GET", "/assets/index-abc123.js").Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("js content type %q", ct)
	}
}
