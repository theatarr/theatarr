package web

import (
	"net/http"
	"net/url"

	"github.com/theatarr/theatarr/internal/webui"
)

// pageRedirects keeps bookmarks for pages now served by React.
var pageRedirects = []struct{ from, to string }{
	{"/library", "/movies"}, // see libraryRedirect
	{"/titles/{id}", "/title/{id}"},
	{"/registry", "/admin/torrents/inventory"},
	{"/registry/{hash}", "/admin/torrents/inventory/{hash}"},
	{"/shadow", "/admin/advanced/arr/comparison"},
	{"/shadow/{id}", "/admin/advanced/arr/comparison/{id}"},
	{"/settings", "/admin/settings"},
	{"/anime/{slug}", "/admin/reviews/anime/{slug}"},
}

// libraryRedirect preserves the grid filters of an old library bookmark.
func libraryRedirect(r *http.Request) string {
	q := r.URL.Query()
	to := "/movies"
	if q.Get("kind") == "series" {
		to = "/series"
	}
	out := url.Values{}
	for _, k := range []string{"q", "with", "without", "state"} {
		if v := q.Get(k); v != "" {
			out.Set(k, v)
		}
	}
	if len(out) > 0 {
		to += "?" + out.Encode()
	}
	return to
}

// mountPageRedirects registers public redirects; their SPA targets require a session.
func (w *Web) mountPageRedirects(mux *http.ServeMux) {
	for _, rd := range pageRedirects {
		w.handle(mux, "GET "+rd.from, Public, 0, func(rw http.ResponseWriter, r *http.Request) {
			if rd.from == "/library" {
				http.Redirect(rw, r, libraryRedirect(r), http.StatusFound)
				return
			}
			to := rd.to
			for _, name := range []string{"id", "hash", "slug"} {
				if v := r.PathValue(name); v != "" {
					to = replaceWildcard(to, name, url.PathEscape(v))
				}
			}
			if r.URL.RawQuery != "" {
				to += "?" + r.URL.RawQuery
			}
			http.Redirect(rw, r, to, http.StatusFound)
		})
	}
}

func replaceWildcard(pattern, name, value string) string {
	w := "{" + name + "}"
	for i := 0; i+len(w) <= len(pattern); i++ {
		if pattern[i:i+len(w)] == w {
			return pattern[:i] + value + pattern[i+len(w):]
		}
	}
	return pattern
}

// mountSPA registers the single-page app as the catch-all. The pattern has no method so it does not clash with the
// arr views' method-less prefixes; the handler answers GET and HEAD only, and 404 for paths other handlers own.
func (w *Web) mountSPA(mux *http.ServeMux) {
	// Unknown API paths have no action to authorise. Answer before the SPA's
	// session/CSRF guard so every method gets a JSON 404.
	missingAPI := func(rw http.ResponseWriter, r *http.Request) {
		securityHeaders(rw, true)
		notFound(rw)
	}
	mux.HandleFunc("/api/", missingAPI)
	mux.HandleFunc("/api", missingAPI)
	h := webui.New(w.ui)
	w.handle(mux, "/", Public, 0, h.ServeHTTP)
}
