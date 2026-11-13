package arrview

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/golden")

func init() { auth.SetFastHashing() }

var testNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

const (
	radarrViewKey = "radarr-view-key"
	sonarrViewKey = "sonarr-view-key"
)

type env struct {
	t   *testing.T
	st  *store.Store
	au  *auth.Service
	pol *policy.Policy
	svc *Service
	mux *http.ServeMux
	key string // an internal/auth API key
	now time.Time
	// searcher is handed to the service (search_test.go).
	searcher Searcher
	// tmdb is handed to the service (lookup_tmdb_test.go); nil: no TMDb.
	tmdb TMDb
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := &env{t: t, st: st, now: testNow}
	now := func() time.Time { return e.now }
	e.au, _, err = auth.New(ctx, st, auth.Options{Logger: log, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	// The first start creates no account; the admin (id 1) is made here.
	if err := auth.ResetPassword(ctx, st, auth.AdminUsername, "test admin password"); err != nil {
		t.Fatal(err)
	}
	e.pol, err = policy.Load(filepath.Join("..", "..", "examples", "policy"))
	if err != nil {
		t.Fatal(err)
	}
	e.seed()
	e.start()
	if e.key, _, err = e.au.CreateKey(ctx, "seerr", 1); err != nil {
		t.Fatal(err)
	}
	return e
}

// pushClient makes a new API key named name the test's key, as autobrr's and an upgrade script's keys are made.
func (e *env) pushClient(name string) {
	e.t.Helper()
	key, _, err := e.au.CreateKey(context.Background(), name, 1)
	if err != nil {
		e.t.Fatal(err)
	}
	e.key = key
}

// start (re)builds the service over the same database, as a restart would.
func (e *env) start() {
	e.t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := map[string]string{"THEATARR_VIEW_RADARR_KEY": radarrViewKey, "THEATARR_VIEW_SONARR_KEY": sonarrViewKey}
	svc, err := New(context.Background(), Config{Store: e.st, Auth: e.au, Policy: e.pol, Logger: log,
		Now: func() time.Time { return e.now }, Getenv: func(k string) string { return env[k] },
		Started: time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC), Searcher: e.searcher, TMDb: e.tmdb})
	if err != nil {
		e.t.Fatal(err)
	}
	e.svc = svc
	e.mux = http.NewServeMux()
	svc.Mount(e.mux)
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.st.Writer().Exec(q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *env) one(q string, args ...any) int64 {
	e.t.Helper()
	var n int64
	if err := e.st.Reader().QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func ms(t time.Time) int64 { return store.Millis(t) }

// seed:
//
//	title 1 "Alpha" (2020, TMDb 101): movie-remux with a file (imported from radarr_4k_debrid, arr id 7, file 501),
//	  movie-web wanted. Released 2020.
//	title 2 "Beta" (2021, TMDb 102): movie-web with a file, unmonitored (radarr_4k_web, arr id 7 too).
//	title 3 "Gamma" (2027, TMDb 103): anime-movie, wanted, not released yet.
//	title 4 "Delta" (TMDb 104): no versions (known to us, in no view).
//	title 10 "The Show" (2019, TVDB 201): tv-2160p; S01E01 aired with a file, S01E02 aired and wanted, S01E03 airs
//	  next year; S00E01 a special. sonarr_4k_debrid id 3.
func (e *env) seed() {
	now := ms(testNow)
	e.exec(`INSERT INTO title (id, kind, name, sort_name, year, tmdb_id, tvdb_id, imdb_id, anime, created_at,
		updated_at) VALUES
		(1, 'movie', 'Alpha', 'alpha', 2020, 101, NULL, 'tt0000101', 0, ?, ?),
		(2, 'movie', 'Beta', 'beta', 2021, 102, NULL, NULL, 0, ?, ?),
		(3, 'movie', 'Gamma', 'gamma', 2027, 103, NULL, NULL, 1, ?, ?),
		(4, 'movie', 'Delta', 'delta', 2022, 104, NULL, NULL, 0, ?, ?),
		(10, 'series', 'The Show', 'show', 2019, NULL, 201, 'tt0000201', 0, ?, ?)`,
		now, now, now, now, now, now, now, now, now, now)
	created := ms(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
		(1, 1, 'movie-remux', 1, 'cutoff_met', ?, ?), (2, 1, 'movie-web', 1, 'wanted', ?, ?),
		(3, 2, 'movie-web', 0, 'filled', ?, ?), (4, 3, 'anime-movie', 1, 'wanted', ?, ?),
		(10, 10, 'tv-2160p', 1, 'wanted', ?, ?)`,
		created, created, created, created, created, created, created, created, created, created)
	e.exec(`INSERT INTO arr_instance (name, flavour, policy_key, created_at, updated_at) VALUES
		('radarr_4k_debrid', 'radarr', 'movie-remux', ?, ?), ('radarr_4k_web', 'radarr', 'movie-web', ?, ?),
		('sonarr_4k_debrid', 'sonarr', 'tv-2160p', ?, ?)`, now, now, now, now, now, now)
	added := ms(time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC))
	e.exec(`INSERT INTO arr_title (instance, arr_id, kind, title_id, version_id, monitored, quality_profile_id,
		minimum_availability, status, path, root_folder_path, tags, arr_file_id, added_at) VALUES
		('radarr_4k_debrid', 7, 'movie', 1, 1, 1, 9, 'released', 'released',
		 '/media/movies/Alpha (2020)', '/media/movies', '[]', 501, ?),
		('radarr_4k_web', 7, 'movie', 2, 3, 0, 4, 'released', 'released',
		 '/media/movies-web/Beta (2021)', '/media/movies-web', '[]', 502, ?),
		('sonarr_4k_debrid', 3, 'series', 10, 10, 1, 2, '', 'continuing',
		 '/media/tv/The Show', '/media/tv', '[]', 0, ?)`, added, added, added)
	e.exec(`INSERT INTO version_source (version_id, instance, arr_id) VALUES (1, 'radarr_4k_debrid', 7),
		(3, 'radarr_4k_web', 7), (10, 'sonarr_4k_debrid', 3)`)
	fileAdded := ms(time.Date(2025, 3, 5, 0, 0, 0, 0, time.UTC))
	e.exec(`INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path, relative_path, size_bytes,
		quality, quality_revision, release_group, scene_name, languages, media_info, custom_format_score,
		indexer_flags, added_at) VALUES
		(1, 1, 1, 'radarr_4k_debrid', 501, '/media/movies/Alpha (2020)/Alpha (2020) Remux-2160p.mkv',
		 'Alpha (2020) Remux-2160p.mkv', 60000000000, 'Remux-2160p', 1, 'GRP', 'Alpha.2020.2160p.BluRay.REMUX-GRP',
		 '["English"]', '{"resolution":"3840x2160","videoDynamicRangeType":"DV HDR10"}', 3200, 0, ?),
		(2, 3, 2, 'radarr_4k_web', 502, '/media/movies-web/Beta (2021)/Beta (2021) WEBDL-2160p.mkv',
		 'Beta (2021) WEBDL-2160p.mkv', 9000000000, 'WEBDL-2160p', 2, 'WEB', '', '[]', '{}', NULL, 0, ?)`,
		fileAdded, fileAdded)
	e.exec(`INSERT INTO title_release_date (title_id, source, country, type, date) VALUES
		(1, 'radarr_4k_debrid', '', 'theatrical', '2020-05-01'), (1, 'radarr_4k_debrid', '', 'digital', '2020-08-01'),
		(3, 'radarr_anime', '', 'theatrical', '2027-02-01')`)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, air_date, air_date_utc_at, runtime_minutes,
		monitored, created_at, updated_at) VALUES
		(101, 10, 1, 1, 'Pilot', '2019-01-07', ?, 45, 1, ?, ?),
		(102, 10, 1, 2, 'Second', '2019-01-14', ?, 45, 1, ?, ?),
		(103, 10, 1, 3, 'Third', '2027-01-21', ?, 45, 1, ?, ?),
		(100, 10, 0, 1, 'Special', '2019-12-24', ?, 30, 0, ?, ?)`,
		ms(time.Date(2019, 1, 8, 2, 0, 0, 0, time.UTC)), now, now,
		ms(time.Date(2019, 1, 15, 2, 0, 0, 0, time.UTC)), now, now,
		ms(time.Date(2027, 1, 22, 2, 0, 0, 0, time.UTC)), now, now,
		ms(time.Date(2019, 12, 25, 2, 0, 0, 0, time.UTC)), now, now)
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at) VALUES
		(10, 101, 1, 'cutoff_met', ?), (10, 102, 1, 'wanted', ?), (10, 103, 1, 'wanted', ?), (10, 100, 0, 'wanted', ?)`,
		now, now, now, now)
	e.exec(`INSERT INTO episode_number (episode_id, title_id, scheme, season, number) VALUES
		(101, 10, 'absolute_tvdb', NULL, 1), (102, 10, 'absolute_tvdb', NULL, 2)`)
}

// do sends a request to the views with the given key (as X-Api-Key; "" sends none).
func (e *env) do(method, path, key string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, rd)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		r.Header.Set("X-Api-Key", key)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, r)
	return rec
}

// call sends a request with the env's key, wants the status, and decodes the body into out (when not nil).
func (e *env) call(method, path string, body any, want int, out any) *httptest.ResponseRecorder {
	e.t.Helper()
	rec := e.do(method, path, e.key, body)
	if rec.Code != want {
		e.t.Fatalf("%s %s: status %d, want %d\n%s", method, path, rec.Code, want, rec.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			e.t.Fatalf("%s %s: %v\n%s", method, path, err, rec.Body.String())
		}
	}
	return rec
}

// golden compares a response body with testdata/golden/<name>.json (rewritten with -update).
func (e *env) golden(name string, body []byte) {
	e.t.Helper()
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		e.t.Fatalf("%s: %v", name, err)
	}
	got, _ := json.MarshalIndent(v, "", "  ")
	got = append(got, '\n')
	p := filepath.Join("testdata", "golden", name+".json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			e.t.Fatal(err)
		}
		if err := os.WriteFile(p, got, 0o644); err != nil {
			e.t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		e.t.Fatalf("%s: %v (run go test -update)", name, err)
	}
	if !bytes.Equal(got, want) {
		e.t.Errorf("%s differs from %s (go test -update rewrites it):\n%s", name, p, got)
	}
}

// keys returns the sorted keys of a JSON object.
func keys(t *testing.T, obj any) []string {
	t.Helper()
	m, ok := obj.(map[string]any)
	if !ok {
		t.Fatalf("not an object: %T", obj)
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// wantKeys checks that obj has every key in want (fields the arr writes, or the callers read) and no key outside
// want+optional.
func wantKeys(t *testing.T, what string, obj any, want []string, optional ...string) {
	t.Helper()
	have := map[string]bool{}
	for _, k := range keys(t, obj) {
		have[k] = true
	}
	allowed := map[string]bool{}
	for _, k := range want {
		allowed[k] = true
		if !have[k] {
			t.Errorf("%s: missing %q", what, k)
		}
	}
	for _, k := range optional {
		allowed[k] = true
	}
	for k := range have {
		if !allowed[k] {
			t.Errorf("%s: unexpected field %q", what, k)
		}
	}
}

func decode(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	return v
}

func fields(s string) []string { return strings.Fields(s) }

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
