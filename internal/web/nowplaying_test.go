package web

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/nowplaying"
)

func TestNowPlayingAPI(t *testing.T) {
	t.Parallel()
	var snap *nowplaying.Snapshot
	e := newEnvWith(t, func(c *Config) { c.NowPlaying = func() *nowplaying.Snapshot { return snap } })
	ctx := context.Background()
	src := func() *nowplaying.Source {
		return &nowplaying.Source{File: "/media/tv/Show/S01E02.mkv", Target: "/mnt/zurg/__all__/Secret.Release/S01E02.mkv",
			LinkID: 10, TitleID: 3, Title: "Gamma", Versions: []string{"tv-hd"}, Release: "Secret.Release-GRP",
			Hash: hashA, Providers: []string{"realdebrid"}, SingleCopy: true, ServedBy: "rd-main", Matched: "link"}
	}
	snap = &nowplaying.Snapshot{Enabled: true, Error: "plex: HTTP 500 from 10.1.2.3", Sessions: []nowplaying.SessionView{
		{Key: "1", UserID: "4242", User: "Usr-Plex", State: "buffering", Type: "episode", Title: "Two",
			Health: nowplaying.Health{Verdict: "buffering", Cause: "provider", Detail: "the copy on rd-main turned broken"},
			Source: src(), Fetch: &nowplaying.Fetch{Speed: 1}, Prewarm: &nowplaying.PrewarmState{Episode: "S01E03", State: "ok"}},
		{Key: "2", UserID: "9", User: "someone-else", State: "playing", Type: "movie", Title: "Alpha", Source: src()},
	}}
	uid := e.account("usr", auth.RoleUser, auth.StatusActive, true)
	e.exec(`UPDATE web_user SET plex_id = 4242, plex_username = 'usr-plex' WHERE id = ?`, uid)
	ukey, _, err := e.auth.CreateKey(ctx, "k-usr", uid)
	if err != nil {
		t.Fatal(err)
	}
	get := func(key string) string {
		r := httptest.NewRequest("GET", APIBase+"/nowplaying", nil)
		r.Header.Set(auth.APIKeyHeader, key)
		rec := e.do(r)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	akey := e.apiKey()
	admin := get(akey)
	for _, m := range []string{"someone-else", hashA, "Secret.Release", "rd-main", "S01E03", "10.1.2.3", "realdebrid"} {
		if !strings.Contains(admin, m) {
			t.Errorf("admin does not see %q: %s", m, admin)
		}
	}
	user := get(ukey)
	if !strings.Contains(user, `"title_id":3`) || !strings.Contains(user, `"verdict":"buffering"`) ||
		!strings.Contains(user, `"cause":"provider"`) || !strings.Contains(user, "Plex does not answer") {
		t.Errorf("user view: %s", user)
	}
	for _, m := range []string{"someone-else", "Alpha", hashA, "Secret.Release", "rd-main", "S01E03", "10.1.2.3",
		"realdebrid", "/media/", "/mnt/", "fetch", "prewarm"} {
		if strings.Contains(user, m) {
			t.Errorf("user sees %q: %s", m, user)
		}
	}
	// Another user with no Plex account sees nothing; off answers enabled false.
	other := e.account("other", auth.RoleUser, auth.StatusActive, false)
	okey, _, _ := e.auth.CreateKey(ctx, "k-other", other)
	if body := get(okey); !strings.Contains(body, `"sessions":[]`) {
		t.Errorf("other: %s", body)
	}
	snap = nil
	if body := get(akey); !strings.Contains(body, `"enabled":false`) || !strings.Contains(body, `"sessions":[]`) {
		t.Errorf("off: %s", body)
	}
}
