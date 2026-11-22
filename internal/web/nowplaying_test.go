package web

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/nowplaying"
	"github.com/theatarr/theatarr/internal/outcome"
	"github.com/theatarr/theatarr/internal/store"
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
	}, Incidents: []nowplaying.Incident{
		{ID: 5, TitleID: 3, Title: "Gamma (2020)", Version: "tv-secret-lib", Files: 3, Episodes: []string{"S01E02"}, Viewer: "usr-plex", Player: "Den TV",
			State: "replaced", Status: "Replaced with another release", OldRelease: "Old.Broken-GRP", NewRelease: "New.Fine-GRP",
			Message: nowplaying.StoppedNote + "This file stopped working."},
		{ID: 6, TitleID: 4, Title: "Delta (2019)", Viewer: "usr-plex", State: "unavailable", Status: "No other copy yet",
			Message: "Plex refused to stop the player: HTTP 401 from 10.1.2.3"},
		{ID: 8, TitleID: 9, Title: "Epsilon (2018)", Viewer: "usr-plex", State: "unavailable", Status: "No other copy yet",
			Message: nowplaying.NotStoppedNote + "the playback was paused"},
		{ID: 7, TitleID: 8, Title: "Omega (2021)", Viewer: "someone-else", Player: "Kitchen", State: "looking",
			Status: "Looking for another copy"},
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
	for _, m := range []string{"someone-else", hashA, "Secret.Release", "rd-main", "S01E03", "10.1.2.3", "realdebrid",
		"Old.Broken-GRP", "New.Fine-GRP", "Omega", "Kitchen", "HTTP 401", `"version":"tv-secret-lib"`, `"files":3`,
		"was paused"} {
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
		"realdebrid", "/media/", "/mnt/", "fetch", "prewarm", "Old.Broken-GRP", "New.Fine-GRP", "Omega", "Kitchen",
		"HTTP 401", "tv-secret-lib", `"files"`, `"version"`} {
		if strings.Contains(user, m) {
			t.Errorf("user sees %q: %s", m, user)
		}
	}
	// Their own incidents, with what came of them, their player and the message it showed.
	for _, m := range []string{`"title":"Gamma (2020)"`, `"status":"Replaced with another release"`, "Den TV",
		"This file stopped working.", `"title":"Delta (2019)"`, `"status":"No other copy yet"`,
		"the player was not stopped: the playback was paused"} {
		if !strings.Contains(user, m) {
			t.Errorf("user does not see %q: %s", m, user)
		}
	}
	// Another user with no Plex account sees nothing; off answers enabled false.
	other := e.account("other", auth.RoleUser, auth.StatusActive, false)
	okey, _, _ := e.auth.CreateKey(ctx, "k-other", other)
	if body := get(okey); !strings.Contains(body, `"sessions":[]`) || strings.Contains(body, "incidents") {
		t.Errorf("other: %s", body)
	}
	snap = nil
	if body := get(akey); !strings.Contains(body, `"enabled":false`) || !strings.Contains(body, `"sessions":[]`) {
		t.Errorf("off: %s", body)
	}
}

// The title's History has what came of each file that stopped working while playing, the cases where the link never
// went dead (readable again) included, with the broken copy's release for those who see files.
func TestPlayingHistoryRows(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	e.exec(`INSERT INTO title (id, kind, name, year, tmdb_id, created_at, updated_at) VALUES (9, 'movie', 'Alpha', 2020,
		909, 1, 1)`)
	kinds := []string{outcome.Replaced, outcome.SameRelease, outcome.Unavailable, outcome.Restored}
	for i, kind := range kinds {
		id := i + 1
		e.exec(`INSERT INTO playing_break (id, incident, watched, link_id, title_id, version_key, path, old_target,
			release_title, quality, episodes, broken_at, updated_at)
			VALUES (?, ?, 1, ?, 9, 'movie-web', '/media/Alpha.mkv', '/mnt/old/Alpha.mkv', 'Alpha.2020.1080p-OLD',
			'WEBDL-1080p', '[]', 1, 1)`, id, id, 10+id)
		// A file of the release found broken with it: the row is about the watched file, with no count.
		e.exec(`INSERT INTO playing_break (id, incident, watched, link_id, title_id, version_key, path, old_target, episodes,
			broken_at, updated_at) VALUES (?, ?, 0, ?, 9, 'movie-web', '/media/x', '/mnt/x', '[]', 1, 1)`, 100+id, id, 20+id)
		store.HistoryPlaying(ctx, e.st.Writer(), int64(id), kind, int64(1000*(id+1)))
	}
	key := e.apiKey()
	var hist ActivityPage
	if code := e.getKey(APIBase+"/activity?state=history&title_id=9&events=playing", key, &hist); code != 200 {
		t.Fatalf("history: %d", code)
	}
	words := map[string]string{}
	for _, h := range hist.History {
		if h.Event != journal.EvPlaying || h.Release != "Alpha.2020.1080p-OLD" || h.Quality != "WEBDL-1080p" ||
			h.Reason == "" || h.Entry != "" || h.Files != 0 {
			t.Errorf("row %+v", h)
		}
		words[h.Words] = h.Tone
	}
	want := map[string]string{"Replaced after it stopped working": "ok", "Readable again after it stopped working": "ok",
		"Stopped working, no other copy yet": "bad", "Earlier copy put back": "ok"}
	if fmt.Sprint(words) != fmt.Sprint(want) {
		t.Errorf("words %v", words)
	}
	// Only for those who see files: a household view gets neither the rows nor the filter.
	var user ActivityPage
	if code := e.getKey(APIBase+"/activity?state=history&title_id=9&as=user", key, &user); code != 200 {
		t.Fatalf("household history: %d", code)
	}
	for _, h := range user.History {
		if h.Event == journal.EvPlaying {
			t.Errorf("household sees %+v", h)
		}
	}
	if code := e.getKey(APIBase+"/activity?state=history&events=playing&as=user", key, nil); code != 400 {
		t.Errorf("household filter by playing: %d", code)
	}
}
