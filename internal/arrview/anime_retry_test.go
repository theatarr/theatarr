package arrview

import "testing"

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
