package decide

import (
	"bytes"
	"compress/gzip"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.Bytes()
}

func TestLoadAnimeWithoutSeriesType(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO title (id, kind, name, anime, series_type, created_at, updated_at)
		VALUES (1, 'series', 'Isekai Story', 1, NULL, 0, 0),
		(2, 'series', 'Explicit Standard', 1, 'standard', 0, 0),
		(3, 'series', 'Unspecified Standard', 0, NULL, 0, 0)`); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]string{1: "anime", 2: "standard", 3: ""} {
		title, err := LoadTitle(ctx, st.Reader(), id)
		if err != nil {
			t.Fatal(err)
		}
		if title.SeriesType != want {
			t.Errorf("title %d: series type %q, want %q", id, title.SeriesType, want)
		}
	}
	series, err := LoadSeries(ctx, st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	if series[1].SeriesType != "anime" {
		t.Errorf("bulk series loader lost anime type: %+v", series[1])
	}
}

// Titles from the database carry the secondary year, the alternative titles and translations of every source, and
// the cached TMDb details under either request key; mapping then uses them as the arr does.
func TestLoadTitlesWithNames(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	w := st.Writer()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO title (id, kind, name, year, secondary_year, tmdb_id, imdb_id, created_at, updated_at)
			VALUES (1, 'movie', 'How to Train Your Dragon', 2010, NULL, 10191, 'tt0892769', 0, 0),
			       (2, 'movie', 'Late Night with the Devil', 2024, 2023, 938614, 'tt14966898', 0, 0),
			       (3, 'movie', 'Akira', 1988, NULL, 149, 'tt0094625', 0, 0),
			       (4, 'movie', 'No Language', 2020, NULL, 4, NULL, 0, 0)`, nil},
		{`UPDATE title SET original_language = 'Japanese' WHERE id = 3`, nil},
		{`INSERT INTO title_alias (title_id, source, kind, name, language, country) VALUES
			(1, 'radarr_a', 'alternative', 'Dragons', '', ''),
			(1, 'tmdb', 'alternative', 'Dragons', '', 'DE'),
			(1, 'tmdb', 'translation', 'Dragon Trainer', 'it', 'IT')`, nil},
		{`INSERT INTO metadata_cache (source, key, status, body, fetched_at, expires_at) VALUES
			('tmdb', '/movie/10191?append_to_response=external_ids%2Crelease_dates', 200, ?, 0, 0),
			('tmdb', '/movie/10191?append_to_response=external_ids%2Crelease_dates%2Calternative_titles%2Ctranslations', 200, ?, 0, 0)`,
			[]any{gz(t, `{"original_title": "old", "runtime": 1}`),
				gz(t, `{"original_title": "How to Train Your Dragon", "original_language": "en", "runtime": 98}`)}},
		{`INSERT INTO metadata_cache (source, key, status, body, fetched_at, expires_at) VALUES
			('tmdb', '/movie/4?append_to_response=external_ids%2Crelease_dates%2Calternative_titles%2Ctranslations', 200, ?, 0, 0)`,
			[]any{gz(t, `{"original_title": "No Language", "original_language": "xx", "runtime": 90}`)}},
	} {
		if _, err := w.ExecContext(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	titles, err := LoadMovies(ctx, st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	htt := titles[1]
	if htt.Runtime != 98 || htt.OriginalLanguage != "English" || len(htt.Aliases) != 2 || htt.Aliases[0] != "Dragons" ||
		htt.Aliases[1] != "Dragon Trainer" {
		t.Errorf("title 1: %+v", htt)
	}
	if titles[2].SecondaryYear != 2023 || len(titles[2].Aliases) != 0 {
		t.Errorf("title 2: %+v", titles[2])
	}
	// Original language: TMDb's code as Radarr maps it (IsoLanguages.Find, else English); without a cached TMDb
	// answer, the arr's name import-arr stored.
	if titles[3].OriginalLanguage != "Japanese" || titles[4].OriginalLanguage != "English" || titles[2].OriginalLanguage != "" {
		t.Errorf("original languages %q %q %q", titles[3].OriginalLanguage, titles[4].OriginalLanguage, titles[2].OriginalLanguage)
	}
	one, err := LoadTitle(ctx, st.Reader(), 1)
	if err != nil || len(one.Aliases) != 2 || one.Runtime != 98 {
		t.Errorf("LoadTitle: %+v %v", one, err)
	}
	// Normalized metadata takes precedence over a stale import response. A failed refresh still leaves the
	// last good values readable, while an error-only row must not hide the imported fallback.
	for _, q := range []string{
		`INSERT INTO title_meta (title_id, runtime_minutes, original_language, refreshed_at, attempted_at, refresh_after, error)
			VALUES (1, 110, 'ja', 1, 2, 3, 'last refresh failed'), (4, NULL, '', NULL, 2, 3, 'not found')`,
		`INSERT INTO title_alias (title_id, source, kind, name) VALUES (1, 'tmdb-original', 'alternative', 'Original Dragon')`,
		`UPDATE title SET runtime_minutes = 124 WHERE id = 3`,
		`INSERT INTO metadata_cache (source, key, status, body, fetched_at, expires_at) VALUES
			('tmdb', '/movie/149?append_to_response=external_ids%2Crelease_dates', 200, X'', 0, 0)`,
	} {
		if _, err := w.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	// A TMDb response without runtime must not hide the known arr runtime.
	if _, err := w.ExecContext(ctx, `UPDATE metadata_cache SET body=? WHERE key LIKE '/movie/149?%'`,
		gz(t, `{"original_title":"Akira","runtime":0}`)); err != nil {
		t.Fatal(err)
	}
	normalized, err := LoadMovies(ctx, st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	if normalized[1].Runtime != 110 || normalized[1].OriginalLanguage != "Japanese" || normalized[1].OriginalTitle != "Original Dragon" ||
		normalized[4].Runtime != 90 || normalized[4].OriginalLanguage != "English" || normalized[3].Runtime != 124 {
		t.Errorf("normalized movie metadata: %+v", normalized)
	}

	e := testEngine(t)
	for _, c := range []struct {
		title Title
		rel   string
	}{
		{htt, "Dragon.Trainer.2010.2160p.WEB-DL-FLUX"},
		{titles[2], "Late.Night.with.the.Devil.2023.2160p.WEB-DL-FLUX"},
	} {
		d, err := e.Decide(ctx, rel(c.rel, "public", 20*gb), c.title, Version{Key: "movie-remux"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := reasons(d); got != "" {
			t.Errorf("%s: %s", c.rel, got)
		}
	}
}

// A series loads with its version's series type (the owner instance's), scene mappings, episodes with their
// numbers, and the version's files with the episodes each holds; the decision then maps and judges with them.
func TestLoadSeriesVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, year, tvdb_id, series_type, use_scene_numbering, runtime_minutes,
			original_language, created_at, updated_at) VALUES (5, 'series', 'Isekai Story', 2021, 2002, 'standard', 1, 24,
			'Japanese', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (8, 5, 'anime-tv', 0, 0)`,
		`INSERT INTO arr_instance (name, flavour, policy_key, created_at, updated_at) VALUES ('sonarr_b', 'sonarr', 'anime-tv', 0, 0)`,
		`INSERT INTO arr_title (instance, arr_id, kind, title_id, version_id, monitored, series_type)
			VALUES ('sonarr_b', 1, 'series', 5, 8, 1, 'anime')`,
		`INSERT INTO version_source (version_id, instance, arr_id) VALUES (8, 'sonarr_b', 1)`,
		`INSERT INTO title_scene_mapping (title_id, source, name, scene_season_number) VALUES
			(5, 'sonarr_b', 'Isekai Story S2', 2), (5, 'sonarr_c', 'Isekai Story S2', 2)`,
		`INSERT INTO episode (id, title_id, season, number, name, air_date, air_date_utc_at, runtime_minutes, monitored,
			created_at, updated_at) VALUES
			(31, 5, 1, 1, 'One', '2021-01-08', 1610118000000, 24, 1, 0, 0),
			(32, 5, 1, 2, 'Two', '2021-01-15', 1610722800000, 24, 1, 0, 0),
			(41, 5, 2, 1, 'Three', '2021-07-08', 1625756400000, 24, 1, 0, 0)`,
		`INSERT INTO episode_number (episode_id, title_id, scheme, season, number, source) VALUES
			(31, 5, 'absolute_tvdb', NULL, 1, 'sonarr_b'), (32, 5, 'absolute_tvdb', NULL, 2, 'sonarr_b'),
			(41, 5, 'absolute_tvdb', NULL, 3, 'sonarr_b'), (41, 5, 'scene', 2, 1, 'sonarr_b'),
			(41, 5, 'scene_absolute', NULL, 3, 'sonarr_b')`,
		`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at) VALUES
			(8, 31, 1, 'filled', 0), (8, 32, 1, 'filled', 0), (8, 41, 0, 'wanted', 0)`,
		`INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path, quality, release_type, scene_name,
			size_bytes) VALUES (70, 8, 5, 'sonarr_b', 900, '/x.mkv', 'WEBDL-1080p', 'multiEpisode', '[G] Isekai Story - 01-02 (WEB 1080p)', 1000)`,
		`INSERT INTO version_file_episode (version_file_id, episode_id) VALUES (70, 31), (70, 32)`,
	} {
		if _, err := st.Writer().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	series, err := LoadSeries(ctx, st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	tt := series[5]
	if tt.TVDB != 2002 || !tt.UseSceneNumbering || tt.Runtime != 24 || tt.OriginalLanguage != "Japanese" || tt.SeriesType != "standard" {
		t.Errorf("series: %+v", tt)
	}
	if err := LoadSeriesVersion(ctx, st.Reader(), &tt, 8); err != nil {
		t.Fatal(err)
	}
	if tt.OriginalTitle != "" {
		t.Errorf("original title %q without a TVDB record", tt.OriginalTitle)
	}
	if tt.SeriesType != "anime" || len(tt.SceneMappings) != 1 || len(tt.Episodes) != 3 {
		t.Fatalf("version: type %s, %d mappings, %d episodes", tt.SeriesType, len(tt.SceneMappings), len(tt.Episodes))
	}
	e3 := tt.Episodes[2]
	if e3.Absolute != 3 || e3.SceneSeason == nil || *e3.SceneSeason != 2 || *e3.SceneNumber != 1 || *e3.SceneAbsolute != 3 ||
		e3.Monitored || e3.File != nil {
		t.Errorf("S02E01: %+v", e3)
	}
	e1 := tt.Episodes[0]
	if e1.File == nil || e1.FileID != 70 || e1.File.ReleaseType != "multiEpisode" || len(e1.File.EpisodeIDs) != 2 ||
		tt.Episodes[1].File != e1.File || e1.AirDateUTC == nil {
		t.Errorf("S01E01: %+v", e1)
	}

	e := seriesEngine(t)
	v := Version{ID: 8, Key: "anime-tv", Policy: e.Policy.Versions["anime-tv"]}
	// The file holds E01 and E02: a release of E01 alone would leave E02 behind.
	d, err := e.Decide(ctx, rel("[G] Isekai Story - 01 (BD 1080p)", "public", 2*gb), tt, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reasons(d) != "ExistingFileHasMoreEpisodes" {
		t.Errorf("E01 alone: %s", reasons(d))
	}
	d, _ = e.Decide(ctx, rel("[G] Isekai Story - 01-02 (BD 1080p)", "public", 3*gb), tt, v, nil)
	if !d.Accepted || !d.Upgrade || d.Existing == nil || d.Existing.Quality != "WEBDL-1080p" {
		t.Errorf("E01-02: %s %v %s", reasons(d), d.Upgrade, d.UpgradeReason)
	}
	// The original title (Sonarr v5's CleanOriginalTitle) comes from the cached TVDB extended record: the name
	// translation in the original language, not an alias.
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO metadata_cache (source, key, status, body, fetched_at, expires_at)
		VALUES ('tvdb', 'GET https://api4.thetvdb.com/v4/series/2002/extended?meta=translations&short=true', 200, ?, 0, 0)`,
		gz(t, `{"status":"success","data":{"id":2002,"name":"Isekai Story","originalLanguage":"jpn","translations":
			{"nameTranslations":[{"language":"jpn","name":"Isekai Alias","isAlias":true},
			{"language":"eng","name":"Isekai Story"},{"language":"jpn","name":"Isekai Monogatari Gaiden"}]}}}`)); err != nil {
		t.Fatal(err)
	}
	t2 := series[5]
	if err := LoadSeriesVersion(ctx, st.Reader(), &t2, 8); err != nil {
		t.Fatal(err)
	}
	if t2.OriginalTitle != "Isekai Monogatari Gaiden" {
		t.Errorf("original title %q", t2.OriginalTitle)
	}
	v5 := v
	p5 := *v.Policy
	p5.Sonarr = "v5"
	v5.Policy = &p5
	d4, _ := e.Decide(ctx, rel("[G] Isekai Monogatari Gaiden - 01-02 (BD 1080p)", "public", 3*gb), t2, v, nil)
	d5, _ := e.Decide(ctx, rel("[G] Isekai Monogatari Gaiden - 01-02 (BD 1080p)", "public", 3*gb), t2, v5, nil)
	if reasons(d4) != "UnknownSeries" || !d5.Accepted || d5.MatchType != MatchTitle {
		t.Errorf("original title: v4 %s, v5 %s %s", reasons(d4), reasons(d5), d5.MatchType)
	}
	// S2 by scene mapping, but the episode is not monitored in this version (RSS path).
	d, _ = e.Decide(ctx, rel("[G] Isekai Story S2 - 01 (BD 1080p)", "public", 2*gb), tt, v, nil)
	if reasons(d) != "EpisodeNotMonitored" || strings.Join(d.Episodes, ",") != "S02E01" {
		t.Errorf("S2 01: %s %v", reasons(d), d.Episodes)
	}
	// Retained removed metadata must not compete with its live replacement's absolute number.
	if _, err := st.Writer().Exec(`UPDATE episode SET removed_at = 1 WHERE id = 41;
		INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at)
		VALUES (42, 5, 2, 2, 'Replacement', 0, 0);
		INSERT INTO episode_number (episode_id, title_id, scheme, number, source)
		VALUES (42, 5, 'absolute_tvdb', 3, 'tvdb')`); err != nil {
		t.Fatal(err)
	}
	if err := LoadSeriesVersion(ctx, st.Reader(), &tt, 8); err != nil {
		t.Fatal(err)
	}
	if len(tt.Episodes) != 3 || tt.Episodes[2].ID != 42 {
		t.Errorf("matching episodes include removed rows: %+v", tt.Episodes)
	}
	d, err = e.Decide(ctx, rel("[G] Isekai Story - 03 (BD 1080p)", "public", 2*gb), tt, v, nil)
	if err != nil || !d.Accepted {
		t.Errorf("replacement absolute match: %+v, %v", d, err)
	}
}
