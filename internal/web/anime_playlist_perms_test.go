package web

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
)

// e2esec 7: the playlist plan of a franchise keeps its order for every browsing account, but file paths only for
// accounts with see_files.
func TestAnimePlaylistPlanHidesFilesWithoutSeeFiles(t *testing.T) {
	e := newEnv(t)
	e.seedAnime()
	e.exec(`INSERT INTO plex_playlist (franchise, order_kind, name, plan, created_at, updated_at)
		VALUES ('mushoku-tensei', 'watch', 'watch order', ?, 0, 0)`,
		`[{"row":1,"title":"Episode","path":"/library/Secret.Release-GRP/episode.mkv","from":"link","rating_key":"77"}]`)
	keys := map[string]string{}
	for _, role := range []string{auth.RoleUser, auth.RoleAdmin} {
		uid := e.account(role+"-acct", role, auth.StatusActive, true)
		key, _, err := e.auth.CreateKey(context.Background(), role+"-key", uid)
		if err != nil {
			t.Fatal(err)
		}
		keys[role] = key
	}
	get := func(role, path string) string {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set(auth.APIKeyHeader, keys[role])
		rec := e.do(req)
		if rec.Code != 200 {
			t.Fatalf("%s %s: %d %s", role, path, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	for _, path := range []string{APIBase + "/anime/playlists?franchise=mushoku-tensei",
		APIBase + "/anime/franchises/mushoku-tensei"} {
		body := get(auth.RoleUser, path)
		if strings.Contains(body, "Secret.Release-GRP") || strings.Contains(body, `"rating_key":"77"`) {
			t.Errorf("%s: file details shown without see_files: %s", path, body)
		}
		if !strings.Contains(body, `"title":"Episode"`) {
			t.Errorf("%s: order lost without see_files: %s", path, body)
		}
		if body := get(auth.RoleAdmin, path); !strings.Contains(body, "Secret.Release-GRP") {
			t.Errorf("%s: admin lost the file path: %s", path, body)
		}
	}

	// The playlist's own Plex rating key and its diagnostic detail need see_files; a franchise's build job (with its
	// error), refresh time and review counts are for admins. The watch order stays for everyone.
	e.exec(`UPDATE plex_playlist SET rating_key = '88', mode = 'failed', detail = 'plex said: secret-diagnostic'`)
	e.exec(`INSERT INTO job (kind, payload, state, run_at, last_error, created_at, updated_at)
		VALUES ('anime_build', '{"slug":"mushoku-tensei"}', 'failed', 0, 'build-error-text', 0, 0)`)
	for _, path := range []string{APIBase + "/anime/playlists?franchise=mushoku-tensei",
		APIBase + "/anime/franchises/mushoku-tensei", APIBase + "/anime/franchises"} {
		body := get(auth.RoleUser, path)
		for _, hidden := range []string{`"rating_key":"88"`, "secret-diagnostic", "build-error-text", `"job":{`} {
			if strings.Contains(body, hidden) {
				t.Errorf("%s: %s shown to a household account: %s", path, hidden, body)
			}
		}
		if strings.Contains(path, "playlists") && !strings.Contains(body, `"title":"Episode"`) {
			t.Errorf("%s: order lost: %s", path, body)
		}
		admin := get(auth.RoleAdmin, path)
		want := []string{"build-error-text"}
		if !strings.HasSuffix(path, "/anime/franchises") {
			want = append(want, `"rating_key":"88"`, "secret-diagnostic")
		}
		if strings.Contains(path, "playlists") {
			want = []string{`"rating_key":"88"`, "secret-diagnostic"}
		}
		for _, shown := range want {
			if !strings.Contains(admin, shown) {
				t.Errorf("%s: admin lost %s: %s", path, shown, admin)
			}
		}
	}
}
