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
}
