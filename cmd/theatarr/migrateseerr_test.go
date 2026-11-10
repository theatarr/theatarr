package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/arr"
	"github.com/theatarr/theatarr/internal/arrview"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/migration"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

const cliSeerrKey = "cli-seerr-key-0123456789"

// seerrIDsViews serves the example policy's real arr views over a small catalog and returns the URL and an admin
// API key:
//
//	title 1 Alpha (TMDb 101): movie-remux; radarr_4k_debrid movie 7.
//	title 2 Beta (TMDb 102): movie-web; radarr_4k_web movie 9.
//	title 3 Delta (TMDb 104): no version, so in no view; radarr_4k_debrid movie 8.
//	title 10 Andor (TVDB 393189): tv-2160p; sonarr_4k_debrid series 35.
func seerrIDsViews(t *testing.T) (string, string) {
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
	now := store.Millis(time.Now())
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, sort_name, year, tmdb_id, tvdb_id, anime, created_at, updated_at) VALUES
		 (1, 'movie', 'Alpha', 'alpha', 2020, 101, NULL, 0, :now, :now), (2, 'movie', 'Beta', 'beta', 2021, 102, NULL, 0, :now, :now),
		 (3, 'movie', 'Delta', 'delta', 2022, 104, NULL, 0, :now, :now), (10, 'series', 'Andor', 'andor', 2022, NULL, 393189, 0, :now, :now)`,
		`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
		 (1, 1, 'movie-remux', 1, 'wanted', :now, :now), (2, 2, 'movie-web', 1, 'wanted', :now, :now),
		 (10, 10, 'tv-2160p', 1, 'wanted', :now, :now)`,
		`INSERT INTO arr_alias (instance, kind, arr_id, target_id, created_at) VALUES
		 ('radarr_4k_debrid', 'movie', 7, 1, :now), ('radarr_4k_web', 'movie', 9, 2, :now),
		 ('radarr_4k_debrid', 'movie', 8, 3, :now), ('sonarr_4k_debrid', 'series', 35, 10, :now)`,
	} {
		if _, err := st.Writer().Exec(strings.ReplaceAll(q, ":now", strconv.FormatInt(now, 10))); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	au, _, err := auth.New(ctx, st, auth.Options{Logger: log, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// The first start creates no account; the admin (id 1) is made here.
	if err := auth.ResetPassword(ctx, st, auth.AdminUsername, "test admin password"); err != nil {
		t.Fatal(err)
	}
	key, _, err := au.CreateKey(ctx, "cli", 1)
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(filepath.Join("..", "..", "examples", "policy"))
	if err != nil {
		t.Fatal(err)
	}
	av, err := arrview.New(ctx, arrview.Config{Store: st, Auth: au, Policy: pol, Logger: log,
		Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	av.Mount(mux)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("theatarr got %s %s", r.Method, r.URL.Path)
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, key
}

// Sonarr id 35 is sonarr_4k_debrid's Andor in the sonarr view (an id that resolves to another title) and fails the run;
// Sonarr id 35 is sonarr_4k_debrid's Andor in the sonarr view, not Reacher (an id that resolves to another title), and fails the run;
// an alias of a title the view does not hold is unresolved, as the view answers 404.
func TestMigrateSeerrIDs(t *testing.T) {
	theatarr, adminKey := seerrIDsViews(t)
	dial := migrationDialer
	migrationDialer = migration.NewDialer(arr.Options{Timeout: 5 * time.Second, MinInterval: time.Millisecond, Retries: 1,
		Backoff: time.Millisecond})
	t.Cleanup(func() { migrationDialer = dial })
	media := []map[string]any{
		{"id": 1, "mediaType": "movie", "tmdbId": 101, "tvdbId": nil, "serviceId": 0, "externalServiceId": 7,
			"serviceId4k": nil, "externalServiceId4k": nil},
		{"id": 2, "mediaType": "movie", "tmdbId": 102, "tvdbId": nil, "serviceId": 0, "externalServiceId": 1000002,
			"serviceId4k": 1, "externalServiceId4k": 7},
		{"id": 3, "mediaType": "movie", "tmdbId": 104, "tvdbId": nil, "serviceId": 0, "externalServiceId": 8},
		{"id": 4, "mediaType": "tv", "tmdbId": 108978, "tvdbId": 366924, "serviceId": 0, "externalServiceId": 35},
		{"id": 5, "mediaType": "tv", "tmdbId": 83867, "tvdbId": 393189, "serviceId": 0, "externalServiceId": 1000010},
	}
	seerr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("X-Api-Key") != cliSeerrKey {
			t.Errorf("seerr got %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/settings/radarr":
			w.Write([]byte(`[{"id":0,"name":"Radarr 4K","apiKey":"arr-key-kept-by-seerr","is4k":false,"isDefault":true},
				{"id":1,"name":"Radarr UHD","apiKey":"arr-key-kept-by-seerr","is4k":true,"isDefault":true}]`))
		case "/api/v1/settings/sonarr":
			w.Write([]byte(`[{"id":0,"name":"Sonarr 4K","apiKey":"arr-key-kept-by-seerr","is4k":false,"isDefault":true}]`))
		case "/api/v1/media":
			json.NewEncoder(w).Encode(map[string]any{"pageInfo": map[string]any{"pages": 1, "pageSize": 100,
				"results": len(media), "page": 1}, "results": media})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(seerr.Close)
	t.Setenv("CLI_SEERR_KEY", cliSeerrKey)
	t.Setenv("CLI_THEATARR_KEY", adminKey)
	args := []string{"seerr-ids", "-seerr", seerr.URL + ",CLI_SEERR_KEY", "-service", theatarr, "-api-key-env", "CLI_THEATARR_KEY"}

	out, err := runMigrationCLI(t, args...)
	if err == nil || !strings.Contains(err.Error(), "2 stored Seerr ids name a different title") {
		t.Fatalf("err %v\n%s", err, out)
	}
	for _, want := range []string{
		"Seerr media 5, stored ids 6 (radarr view /radarr, sonarr view /sonarr)",
		"ok 3, wrong title 2, unresolved 1, unverifiable 0",
		`tv tmdb 108978, tvdb 366924 (Seerr media 4): externalServiceId 35 on "Sonarr 4K": /sonarr answers "Andor" (tvdb 393189)`,
		`movie tmdb 102 (Seerr media 2): externalServiceId4k 7 on "Radarr UHD": /radarr answers "Alpha" (tmdb 101)`,
		`unresolved:` + "\n" + `  movie tmdb 104 (Seerr media 3): externalServiceId 8 on "Radarr 4K": /radarr answers 404`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, cliSeerrKey) || strings.Contains(out, adminKey) || strings.Contains(out, "arr-key-kept-by-seerr") {
		t.Errorf("a key in the output:\n%s", out)
	}

	out, err = runMigrationCLI(t, append(args, "-json")...)
	var rep migration.SeerrIDReport
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil || err == nil {
		t.Fatalf("json %v %v\n%s", jerr, err, out)
	}
	if rep.Summary != (migration.SeerrIDSummary{Media: 5, Checked: 6, OK: 3, WrongTitle: 2, Unresolved: 1}) ||
		rep.Items[0].Result != migration.SeerrIDOK || rep.Items[0].ViewTitle.Title != "Alpha" {
		t.Errorf("report %+v", rep)
	}

	// Once Seerr's scans rewrote the ids to the views' own, the run passes.
	media = media[:2]
	media[1]["externalServiceId4k"] = 1000002
	if out, err := runMigrationCLI(t, args...); err != nil || !strings.Contains(out, "ok 3, wrong title 0, unresolved 0") {
		t.Fatalf("after the scan: %v\n%s", err, out)
	}

	if _, err := runMigrationCLI(t, "seerr-ids", "-service", theatarr); err == nil || !strings.Contains(err.Error(), "-seerr") {
		t.Errorf("no -seerr: %v", err)
	}
	if _, err := runMigrationCLI(t, append(args, "-radarr-view", "radarr")...); err == nil {
		t.Error("a view path without a slash was taken")
	}
}
