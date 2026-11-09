// Package webui serves the single-page app built from web/ (docs/design/ui.md).
//
// The bundle is built by `make ui` (or the Dockerfile's node stage) into internal/webui/dist and embedded in the
// binary. The repository commits only dist/.keep, so a checkout without node still builds and tests: when
// dist/index.html is missing the handler serves a small built-in page that says so and links to the API.
//
// Routing: the handler is the catch-all. Service paths and the removed /classic/ and /static/ paths
// get a 404 when they fall through to here, never index.html. A path that names a file in the
// bundle serves that file; anything else serves index.html, so the SPA's router takes over.
//
// Caching: index.html is no-cache (it names the current hashed assets); files under assets/ carry a content hash in
// their names and are cached for a year, immutable; other files (the icon) for an hour.
package webui

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var dist embed.FS

// Bundle is the embedded build output (the dist directory's contents).
func Bundle() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // dist is always embedded (dist/.keep is committed)
	}
	return sub
}

// Built reports whether fsys holds a built SPA (an index.html).
func Built(fsys fs.FS) bool {
	st, err := fs.Stat(fsys, "index.html")
	return err == nil && !st.IsDir()
}

// reserved are the path prefixes that never serve the SPA: other handlers own them, so a request that reaches the
// catch-all with one of these is a 404.
var reserved = []string{"/api/", "/classic/", "/static/", "/img/", "/radarr/", "/sonarr/", "/v/"}

var reservedExact = map[string]bool{"/api": true, "/classic": true, "/healthz": true, "/readyz": true,
	"/metrics": true, "/radarr": true, "/sonarr": true}

// Reserved reports whether p belongs to another handler.
func Reserved(p string) bool {
	if reservedExact[p] {
		return true
	}
	for _, r := range reserved {
		if strings.HasPrefix(p, r) {
			return true
		}
	}
	return false
}

// Handler serves fsys as the SPA.
type Handler struct {
	fsys  fs.FS
	built bool
	mod   time.Time
}

// New returns a handler for fsys (normally Bundle()).
func New(fsys fs.FS) *Handler {
	return &Handler{fsys: fsys, built: Built(fsys), mod: time.Now()}
}

// Built reports whether the handler has a bundle to serve.
func (h *Handler) Built() bool { return h.built }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := r.URL.Path
	if Reserved(p) {
		if strings.HasPrefix(p, "/api/") {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"not found"}`+"\n")
			return
		}
		http.NotFound(w, r)
		return
	}
	if !h.built {
		h.notBuilt(w, r)
		return
	}
	name := strings.TrimPrefix(path.Clean(p), "/")
	if name != "" && name != "index.html" {
		if f, err := h.fsys.Open(name); err == nil {
			st, err := f.Stat()
			if err == nil && !st.IsDir() {
				defer f.Close()
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "public, max-age=3600")
				}
				if rs, ok := f.(io.ReadSeeker); ok {
					http.ServeContent(w, r, name, h.mod, rs)
					return
				}
				w.Header().Set("Content-Type", contentType(name))
				io.Copy(w, f)
				return
			}
			f.Close()
		}
		// A missing hashed asset is a 404, not the app: a stale index.html must not get HTML for its script.
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "no-store")
			http.NotFound(w, r)
			return
		}
	}
	h.index(w, r)
}

func (h *Handler) index(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(h.fsys, "index.html")
	if err != nil {
		h.notBuilt(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(b)
	}
}

func contentType(name string) string {
	switch path.Ext(name) {
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".json":
		return "application/json"
	}
	return "application/octet-stream"
}

// notBuiltPage is shown when the binary was built without the SPA. No inline script or style: the CSP forbids them.
const notBuiltPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>UI not built</title>
</head>
<body>
<main>
<h1>UI not built</h1>
<p>This binary was built without the web UI. Run <code>make ui</code> and rebuild, or use <code>make image</code>.</p>
<ul>
<li><a href="/api/theatarr/v1/openapi.json">API description</a> (<code>/api/theatarr/v1</code>)</li>
</ul>
</main>
</body>
</html>
`

func (h *Handler) notBuilt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		io.WriteString(w, notBuiltPage)
	}
}
