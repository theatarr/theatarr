package arrview

import (
	"testing"

	"github.com/theatarr/theatarr/internal/jobs"
)

// A TMDb outage at the add leaves the anime question open ("unknown"); the next add asks again and turns
// on the anime version once TMDb answers.
func TestAnimeCheckRetriedAfterTMDbOutage(t *testing.T) {
	e := newEnv(t)
	f := &fakeTMDb{fail: true, movies: map[int]string{
		557: `{"id":557,"title":"Anime","original_language":"ja","genres":[{"id":16,"name":"Animation"}]}`,
	}}
	e.withTMDb(f)
	body := map[string]any{"title": "Anime", "tmdbId": 557, "qualityProfileId": e.profileID("radarr", "Rules default"),
		"rootFolderPath": "/media/movies", "monitored": true}
	var got map[string]any
	e.call("POST", "/radarr/api/v3/movie", body, 201, &got)
	tid := int64(got["id"].(float64)) - IDBase
	if src := e.str(`SELECT anime_source FROM arrview_title WHERE view = 'radarr' AND title_id = ?`, tid); src != "unknown" {
		t.Fatalf("anime_source after the failed check = %q, want unknown", src)
	}
	if e.versionsOf(tid)["anime-movie"] {
		t.Fatal("fixture: anime version created without an answer")
	}
	f.mu.Lock()
	f.fail = false
	f.mu.Unlock()
	e.call("POST", "/radarr/api/v3/movie", body, 201, &got)
	if !e.versionsOf(tid)["anime-movie"] {
		t.Fatalf("anime-movie missing after TMDb recovered; versions=%v", e.versionsOf(tid))
	}
	if src := e.str(`SELECT anime_source FROM arrview_title WHERE view = 'radarr' AND title_id = ?`, tid); src != "tmdb" {
		t.Fatalf("anime_source = %q, want tmdb", src)
	}
	if e.one(`SELECT anime FROM title WHERE id = ?`, tid) != 1 {
		t.Fatal("title not flagged anime")
	}
}

// A compatible Refresh whose anime check now answers anime turns on the view's anime library and fills its group,
// but, like lists and collections, brings back no copy that was removed there (restore false): a person's add or
// edit still does.
func TestAnimeRefreshKeepsRemovedPartner(t *testing.T) {
	e := newEnv(t)
	hd := *e.pol.Versions["anime-movie"]
	hd.Library = "/media/anime-movies-hd"
	e.pol.Versions["anime-movie-hd"] = &hd
	e.pol.Library.SyncGroups = [][]string{{"anime-movie", "anime-movie-hd"}}
	f := &fakeTMDb{fail: true, movies: map[int]string{
		557: `{"id":557,"title":"Anime","original_language":"ja","genres":[{"id":16,"name":"Animation"}]}`,
	}}
	e.withTMDb(f)
	q := jobs.NewQueue(e.st)
	e.svc.SetMaintenance(q, &commandMetadata{q: q})
	body := map[string]any{"title": "Anime", "tmdbId": 557, "qualityProfileId": e.profileID("radarr", "Rules default"),
		"rootFolderPath": "/media/movies", "monitored": true}
	var got map[string]any
	e.call("POST", "/radarr/api/v3/movie", body, 201, &got)
	tid := int64(got["id"].(float64)) - IDBase
	// The title once had a copy in anime-movie-hd, removed since.
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at)
		VALUES (90, ?, 'anime-movie-hd', 0, 'wanted', 0, 0)`, tid)
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (90, 'radarr', 0)`)
	f.mu.Lock()
	f.fail = false
	f.mu.Unlock()
	e.call("POST", "/radarr/api/v3/command", map[string]any{"name": "RefreshMovie", "movieIds": []int64{tid + IDBase}},
		201, nil)
	if !e.versionsOf(tid)["anime-movie"] {
		t.Fatalf("the refresh did not turn on anime-movie: %v", e.versionsOf(tid))
	}
	if n := e.one(`SELECT COUNT(*) FROM arrview_removed_version WHERE version_id = 90`); n != 1 {
		t.Error("the refresh brought back the removed anime-movie-hd copy")
	}
	if n := e.one(`SELECT monitored FROM version WHERE id = 90`); n != 0 {
		t.Error("the refresh monitored the removed anime-movie-hd copy")
	}
}
