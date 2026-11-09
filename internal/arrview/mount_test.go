package arrview

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/theatarr/theatarr/internal/web"
)

// The views mount on the same mux as the web layer (cmd/theatarr serve) without a pattern conflict, and their paths reach
// the views, not the UI.
func TestMountsBesideWeb(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	w, err := web.New(web.Config{Store: e.st, Auth: e.au, Policy: e.pol,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	w.Mount(mux)
	e.svc.Mount(mux)
	for _, p := range []string{"/radarr/api/v3/system/status", "/sonarr/api/v3/system/status",
		"/v/movie-web/radarr/api/v3/system/status", "/v/tv-2160p/sonarr/api/v3/system/status"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", p+"?apikey="+e.key, nil))
		if rec.Code != 200 || rec.Header().Get("X-Application-Version") == "" {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	// The native API still refuses the query parameter.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", web.APIBase+"/status?apikey="+e.key, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("native API with apikey query: %d", rec.Code)
	}
}
