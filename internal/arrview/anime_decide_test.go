package arrview

import (
	"strconv"
	"strings"
	"testing"
)

// Theatarr decides anime for a new title when the add says nothing about it (no anime series type, root folder or
// anime-only profile): the anime mapping data first, then TMDb's Japanese-language animation. So Seerr can use one
// Radarr server and one Sonarr server with "Rules default" and no anime override rules. What the client says still
// wins; the decision's source is recorded on the view's record of the title.
func TestAddDecidesAnime(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	now := ms(testNow)
	// Mapping data: a franchise whose entry maps to TMDb movie 150 and to TVDB series 300.
	e.exec(`INSERT INTO anime_franchise (slug, name, seed_anilist, created_at, updated_at) VALUES ('f', 'F', 1, ?, ?)`,
		now, now)
	e.exec(`INSERT INTO anime_entry (franchise, key, origin, anilist_id, tmdb_movie, body) VALUES
		('f', 'al:1', 'anilist', 1, '[150]', '{}')`)
	e.exec(`INSERT INTO anime_mapping (franchise, entry_key, status, tvdb_series, updated_at) VALUES
		('f', 'al:1', 'auto', 300, ?)`, now)
	ja := `"original_language": "ja", "genres": [{"id": 16, "name": "Animation"}]`
	en := `"original_language": "en", "genres": [{"id": 28, "name": "Action"}]`
	f := &fakeTMDb{movies: map[int]string{
		149: `{"id": 149, "title": "Akira", ` + ja + `}`,
		150: `{"id": 150, "title": "Mapped", ` + en + `}`,
		555: `{"id": 555, "title": "Alpha Two", ` + en + `}`,
		101: `{"id": 101, "title": "Alpha", ` + ja + `}`,
		556: `{"id": 556, "title": "Plain", ` + en + `}`,
		558: `{"id": 558, "title": "Anime by root", ` + ja + `}`,
		557: `{"id": 557, ` + ja + `}`,
	}, tvByTVDB: map[int]int{301: 901, 303: 903}, tv: map[int]string{
		901: `{"id": 901, "name": "Anime Show", ` + ja + `}`,
		903: `{"id": 903, "name": "Plain Show", ` + en + `}`,
	}}
	e.withTMDb(f)
	def := e.profileID("radarr", "Rules default")
	addMovie := func(tmdbID int, profile int64, root string) int64 {
		t.Helper()
		var m map[string]any
		e.call("POST", "/radarr/api/v3/movie", map[string]any{"title": "M" + strconv.Itoa(tmdbID), "tmdbId": tmdbID,
			"qualityProfileId": profile, "rootFolderPath": root, "monitored": true,
			"addOptions": map[string]any{"searchForMovie": false}}, 201, &m)
		return int64(m["id"].(float64)) - IDBase
	}
	check := func(what string, tid int64, anime bool, source string, versions ...string) {
		t.Helper()
		got := e.versionsOf(tid)
		if len(got) != len(versions) {
			t.Errorf("%s: versions %v, want %v", what, got, versions)
		}
		for _, k := range versions {
			if !got[k] {
				t.Errorf("%s: versions %v, want %v", what, got, versions)
			}
		}
		if a := e.one(`SELECT anime FROM title WHERE id = ?`, tid); (a == 1) != anime {
			t.Errorf("%s: title.anime %d", what, a)
		}
		var src string
		if err := e.st.Reader().QueryRow(`SELECT anime_source FROM arrview_title WHERE title_id = ?`, tid).Scan(&src); err != nil || src != source {
			t.Errorf("%s: anime_source %q %v, want %q", what, src, err, source)
		}
	}
	// Seerr's single Radarr server: Rules default and the non-anime root. TMDb: Japanese animation.
	check("TMDb anime", addMovie(149, def, "/media/movies"), true, "tmdb", "anime-movie")
	// The mapping data wins over TMDb, and TMDb is not asked.
	check("mapped", addMovie(150, def, "/media/movies"), true, "mapping", "anime-movie")
	for _, r := range f.requests() {
		if strings.Contains(r, "/movie/150") {
			t.Errorf("TMDb asked about a mapped movie: %v", r)
		}
	}
	// Not anime by TMDb: the profile's non-anime versions.
	check("TMDb not anime", addMovie(555, def, "/media/movies"), false, "tmdb", "movie-remux",
		"movie-web")
	// What the client says wins: the Anime profile.
	check("client anime", addMovie(556, e.profileID("radarr", "Anime"), ""), true, "request", "anime-movie")
	// No profile but a root folder: the root's versions, whatever Theatarr decides.
	check("root wins", addMovie(558, 0, "/media/movies"), true, "tmdb", "movie-remux")
	// A title we already hold keeps its flag; TMDb is not asked.
	before := len(f.requests())
	e.call("POST", "/radarr/api/v3/movie", map[string]any{"title": "Alpha", "tmdbId": 101, "qualityProfileId": def,
		"rootFolderPath": "/media/movies"}, 201, nil)
	if len(f.requests()) != before {
		t.Errorf("TMDb asked about a catalogued title: %v", f.requests()[before:])
	}
	if e.one(`SELECT anime FROM title WHERE id = 1`) != 0 {
		t.Error("Alpha became anime")
	}
	// TMDb down: not anime for now, recorded as unknown (asked again later: TestAnimeCheckRetriedAfterTMDbOutage).
	f.mu.Lock()
	f.fail = true
	f.mu.Unlock()
	check("TMDb down", addMovie(557, def, "/media/movies"), false, "unknown", "movie-remux", "movie-web")
	f.mu.Lock()
	f.fail = false
	f.mu.Unlock()

	// Sonarr, as Seerr's single server sends a non-anime series: Rules default, standard, the 4K root.
	sdef := e.profileID("sonarr", "Rules default")
	addSeries := func(tvdbID int, seriesType string) int64 {
		t.Helper()
		var m map[string]any
		e.call("POST", "/sonarr/api/v3/series", map[string]any{"title": "S" + strconv.Itoa(tvdbID), "tvdbId": tvdbID,
			"qualityProfileId": sdef, "rootFolderPath": "/media/tv", "seriesType": seriesType,
			"monitored": true, "seasons": []any{}, "addOptions": map[string]any{"searchForMissingEpisodes": false}}, 201, &m)
		return int64(m["id"].(float64)) - IDBase
	}
	check("series mapped", addSeries(300, "standard"), true, "mapping", "anime-tv")
	check("series TMDb anime", addSeries(301, "standard"), true, "tmdb", "anime-tv")
	check("series TMDb not anime", addSeries(303, "standard"), false, "tmdb", "tv-2160p")
	// Seerr's anime series type wins (TMDb knows nothing of 302).
	check("series client anime", addSeries(302, "anime"), true, "request", "anime-tv")
}

// The view's default versions stand in for a profile's only under "Rules default" (let Theatarr's rules decide) or with no
// profile: an anime movie added with "Rules default" gets anime-movie, one added with "WEB only" (picked on purpose)
// gets movie-web, as the profile says.
func TestAnimeFallbackOnlyForRulesDefault(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ja := `"original_language": "ja", "genres": [{"id": 16, "name": "Animation"}]`
	e.withTMDb(&fakeTMDb{movies: map[int]string{
		160: `{"id": 160, "title": "Anime A", ` + ja + `}`,
		161: `{"id": 161, "title": "Anime B", ` + ja + `}`,
	}})
	for _, tc := range []struct {
		tmdb    int
		profile string
		want    string
	}{{160, "Rules default", "anime-movie"}, {161, "WEB only", "movie-web"}} {
		var m map[string]any
		e.call("POST", "/radarr/api/v3/movie", map[string]any{"title": "M", "tmdbId": tc.tmdb,
			"qualityProfileId": e.profileID("radarr", tc.profile), "rootFolderPath": "/media/movies",
			"monitored": true, "addOptions": map[string]any{"searchForMovie": false}}, 201, &m)
		tid := int64(m["id"].(float64)) - IDBase
		if got := e.versionsOf(tid); len(got) != 1 || !got[tc.want] {
			t.Errorf("%s + anime movie: versions %v, want %s", tc.profile, got, tc.want)
		}
		if e.one(`SELECT anime FROM title WHERE id = ?`, tid) != 1 {
			t.Errorf("%s: not decided anime", tc.profile)
		}
	}
}
