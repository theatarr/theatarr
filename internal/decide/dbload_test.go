package decide

import (
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
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
	for id, want := range map[int64]string{1: "anime", 2: "standard", 3: "standard"} {
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
	// One type per series: the title's standard, not the version's imported anime.
	if tt.SeriesType != "standard" || len(tt.SceneMappings) != 1 || len(tt.Episodes) != 3 {
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
	// A series' original title is not loaded or matched, even with a cached TVDB record naming it.
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO metadata_cache (source, key, status, body, fetched_at, expires_at)
		VALUES ('tvdb', 'GET https://api4.thetvdb.com/v4/series/2002/extended?meta=translations&short=true', 200, ?, 0, 0)`,
		gz(t, `{"status":"success","data":{"id":2002,"name":"Isekai Story","originalLanguage":"jpn","translations":
			{"nameTranslations":[{"language":"eng","name":"Isekai Story"},{"language":"jpn","name":"Isekai Monogatari Gaiden"}]}}}`)); err != nil {
		t.Fatal(err)
	}
	t2 := series[5]
	if err := LoadSeriesVersion(ctx, st.Reader(), &t2, 8); err != nil {
		t.Fatal(err)
	}
	if t2.OriginalTitle != "" {
		t.Errorf("original title %q", t2.OriginalTitle)
	}
	d4, _ := e.Decide(ctx, rel("[G] Isekai Monogatari Gaiden - 01-02 (BD 1080p)", "public", 3*gb), t2, v, nil)
	if reasons(d4) != "UnknownSeries" {
		t.Errorf("original title: %s", reasons(d4))
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

// languageFixture is the series policy with "Language: Not English" and "Not Original" at -10000, and four series:
// 1 native with title_meta en, 2 native with title_meta ja, 3 imported (title.original_language English), 4 native
// with no metadata language. Each has version 10+id and episode 100+id (S01E01).
func languageFixture(t *testing.T) (*Engine, func(id int64) Title) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/series")); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"not-english.json":  `{"name":"Language: Not English","specifications":[{"name":"Not English","implementation":"LanguageSpecification","negate":true,"required":false,"fields":{"value":1}}]}`,
		"not-original.json": `{"name":"Language: Not Original","specifications":[{"name":"Not Original","implementation":"LanguageSpecification","negate":true,"required":false,"fields":{"value":-2}}]}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, "formats", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	y := strings.Replace(string(raw), "      Season Pack: 10\n      HDR: 500\n",
		"      Season Pack: 10\n      HDR: 500\n      \"Language: Not English\": -10000\n      \"Language: Not Original\": -10000\n", 1)
	y = strings.Replace(y, "formats:\n  Season Pack:", "formats:\n  \"Language: Not English\": {file: formats/not-english.json, flavour: sonarr}\n  \"Language: Not Original\": {file: formats/not-original.json, flavour: sonarr}\n  Season Pack:", 1)
	if y == string(raw) {
		t.Fatal("policy not patched")
	}
	if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(pol, Options{Now: func() time.Time { return seriesClock }})
	if err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	w := st.Writer()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := w.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// Rows as catalog.create writes them: no original_language. monitored set as the add does later.
	for id, name := range map[int]string{1: "English Show", 2: "Nihon Show", 3: "Imported Show", 4: "Bare Show"} {
		exec(`INSERT INTO title (id, kind, name, sort_name, year, tmdb_id, tvdb_id, imdb_id, anime, monitored, created_at, updated_at)
			VALUES (?, 'series', ?, ?, 2020, ?, ?, NULL, 0, 1, 0, 0)`, id, name, strings.ToLower(name), 100+id, 1000+id)
		exec(`INSERT INTO version (id, title_id, policy_key, monitored, created_at, updated_at) VALUES (?, ?, 'tv', 1, 0, 0)`, 10+id, id)
		exec(`INSERT INTO episode (id, title_id, season, number, name, air_date, air_date_utc_at, runtime_minutes, monitored, created_at, updated_at)
			VALUES (?, ?, 1, 1, 'Pilot', '2020-04-01', 1585706400000, 45, 1, 0, 0)`, 100+id, id)
	}
	// What art.Service.write stores for a series: title_meta only.
	exec(`INSERT INTO title_meta (title_id, original_language, refreshed_at, attempted_at, refresh_after, error)
		VALUES (1, 'en', 1, 2, 3, ''), (2, 'ja', 1, 2, 3, '')`)
	// Control: what importarr stores for a Sonarr-imported series.
	exec(`UPDATE title SET original_language = 'English' WHERE id = 3`)
	load := func(id int64) Title {
		t.Helper()
		title, err := LoadTitle(ctx, st.Reader(), id)
		if err != nil {
			t.Fatal(err)
		}
		if err := LoadSeriesVersion(ctx, st.Reader(), &title, 10+id); err != nil {
			t.Fatal(err)
		}
		return title
	}
	return e, load
}

// A natively added series (title.original_language NULL, title_meta.original_language set by the art
// refresh) must decide an untagged release with the series' original language, not Unknown.
func TestSeriesOriginalLanguageFromMetadata(t *testing.T) {
	e, load := languageFixture(t)
	ctx := context.Background()

	for _, c := range []struct {
		id        int64
		name, rel string
		wantLang  string
	}{
		{3, "control: imported, title.original_language=English", "Imported.Show.S01E01.2160p.WEB-DL-GRP", "English"},
		{1, "native, title_meta en", "English.Show.S01E01.2160p.WEB-DL-GRP", "English"},
		{2, "native, title_meta ja", "Nihon.Show.S01E01.2160p.WEB-DL-GRP", "Japanese"},
		{4, "native, no metadata language (Sonarr: English)", "Bare.Show.S01E01.2160p.WEB-DL-GRP", "English"},
	} {
		t.Run(c.name, func(t *testing.T) {
			title := load(c.id)
			d, err := e.Decide(ctx, rel(c.rel, "public", 8*gb), title, Version{ID: 10 + c.id, Key: "tv", Policy: e.Policy.Versions["tv"]}, nil)
			if err != nil {
				t.Fatal(err)
			}
			var formats []string
			for _, f := range d.Formats {
				formats = append(formats, f.Name)
			}
			if len(d.Languages) != 1 || d.Languages[0] != c.wantLang {
				t.Errorf("languages %v, want %s", d.Languages, c.wantLang)
			}
			// Not Original never matches an untagged release; Not English only when the original is not English.
			for _, f := range formats {
				if f == "Language: Not Original" || (f == "Language: Not English" && c.wantLang == "English") {
					t.Errorf("negated language format matched: %s (score %d)", f, d.Score)
				}
			}
		})
	}
}

// A held file stored with no known language, against an untagged release of the same quality: on a series added
// without the arr's language it takes the series' language, as the release does, so the release is no upgrade. An
// imported series (title.original_language set) and files with a known language score as before.
func TestHeldUnknownLanguageFile(t *testing.T) {
	e, load := languageFixture(t)
	ctx := context.Background()
	for _, c := range []struct {
		id        int64
		name      string
		languages []string
		existing  int
		upgrade   bool
	}{
		{1, "native en, Unknown", []string{"Unknown"}, 0, false},
		{1, "native en, none", nil, 0, false},
		{2, "native ja, Unknown", []string{"Unknown"}, -10000, false},
		{4, "native bare, Unknown", []string{"Unknown"}, 0, false},
		{1, "native en, French", []string{"French"}, -20000, true},
		{1, "native en, English", []string{"English"}, 0, false},
		// Unchanged: imported series keep scoring an Unknown file as no language.
		{3, "imported, Unknown", []string{"Unknown"}, -20000, true},
		{3, "imported, French", []string{"French"}, -20000, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			title := load(c.id)
			ep := &title.Episodes[0]
			ep.File, ep.FileID = &ExistingFile{Quality: "WEBDL-2160p", Revision: 1, SceneName: "Show.S01E01.2160p.WEB-DL-OLD",
				ReleaseGroup: "OLD", Size: 8 * gb, ReleaseType: "singleEpisode", Languages: c.languages, EpisodeIDs: []int64{ep.ID}}, 1
			d, err := e.Decide(ctx, rel(strings.ReplaceAll(title.Name, " ", ".")+".S01E01.2160p.WEB-DL-GRP", "public", 8*gb), title,
				Version{ID: 10 + c.id, Key: "tv", Policy: e.Policy.Versions["tv"]}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if d.Existing == nil || d.Existing.Score != c.existing || d.Upgrade != c.upgrade {
				t.Errorf("existing %+v upgrade %v (%s), want score %d upgrade %v", d.Existing, d.Upgrade, d.UpgradeReason,
					c.existing, c.upgrade)
			}
		})
	}
}

func TestSeriesOriginalLanguageCodes(t *testing.T) {
	t.Parallel()
	for code, want := range map[string]string{
		"en": "English", "ja": "Japanese", "ko": "Korean", "de": "German", "no": "Norwegian", "fr": "French",
		"es": "Spanish", "sv": "Swedish", "zh": "Chinese", "lv": "Latvian", "cn": "Chinese", "zh-TW": "Chinese",
		"pt-BR": "Portuguese (Brazil)", "eng": "English", "jpn": "Japanese", "xx": "English", "": "English",
		"zh-HK": "Chinese", "zh-Hant": "Chinese", "zh-Hans": "Chinese", "fr-CA": "French", "de-AT": "German",
		"yue": "Chinese", "nn": "Norwegian", "nno": "Norwegian", "es-419": "Spanish (Latino)", "es-MX": "Spanish (Latino)",
		"iw": "Hebrew", "in": "Indonesian", "tl": "English", "ur": "English",
	} {
		if got := seriesOriginalLanguage(code); got != want {
			t.Errorf("%q: %s, want %s", code, got, want)
		}
	}
}
