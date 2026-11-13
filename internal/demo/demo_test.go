package demo_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/theatarr/theatarr/internal/activity"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/demo"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/metadata/art"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
	"github.com/theatarr/theatarr/internal/web"
	"go.yaml.in/yaml/v3"
)

func init() { auth.SetFastHashing() }

var anchor = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

// noNet fails every outbound request and counts them: the image cache's fetcher and http.DefaultTransport.
type noNet struct {
	mu   sync.Mutex
	urls []string
}

func (n *noNet) record(u string) {
	n.mu.Lock()
	n.urls = append(n.urls, u)
	n.mu.Unlock()
}

func (n *noNet) Do(r *http.Request) (*http.Response, error) {
	n.record(r.URL.String())
	return nil, errors.New("demo test: no network")
}

func (n *noNet) RoundTrip(r *http.Request) (*http.Response, error) { return n.Do(r) }

func (n *noNet) calls() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.urls...)
}

// loadPolicy loads the policy the demo runs with: examples/policy with the demo overlay (examples/demo) copied over
// it, as scripts/devserver.sh mounts it.
func loadPolicy(t *testing.T) *policy.Policy {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("..", "..", "examples", "policy"))); err != nil {
		t.Fatal(err)
	}
	overlay, err := filepath.Glob(filepath.Join("..", "..", "examples", "demo", "*.yaml"))
	if err != nil || len(overlay) == 0 {
		t.Fatalf("demo overlay: %v %v", overlay, err)
	}
	for _, f := range overlay {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(f)), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pol, err := policy.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return pol
}

// examplePolicy is examples/policy alone: one copy per series.
func examplePolicy(t *testing.T) *policy.Policy {
	t.Helper()
	pol, err := policy.Load(filepath.Join("..", "..", "examples", "policy"))
	if err != nil {
		t.Fatal(err)
	}
	return pol
}

func seed(t *testing.T, pol *policy.Policy, s uint64, images string) *store.Store {
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
	if _, err := demo.Seed(ctx, st, demo.Options{Policy: pol, Seed: s, Now: anchor, ImagesDir: images,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}); err != nil {
		t.Fatal(err)
	}
	return st
}

// seeded is one database seeded from the demo policy with seed 1 and no images, built once per test binary: most
// tests read that same data, and seeding is the slowest part of each. TestDemoDeterministic shows a seed always
// gives the same data.
var seeded struct {
	once sync.Once
	dir  string
	b    []byte
}

// seedDemo is seedDemo(t): a store holding a copy of the seeded database.
func seedDemo(t *testing.T) *store.Store {
	t.Helper()
	seeded.once.Do(func() {
		dir, err := os.MkdirTemp("", "theatarr-demo-")
		if err != nil {
			t.Fatal(err)
		}
		seeded.dir = dir
		path := filepath.Join(dir, "theatarr.db")
		ctx := context.Background()
		st, err := store.Open(ctx, storetest.Seed(t, path), store.Options{})
		if err != nil {
			t.Fatal(err)
		}
		seedPolicy := loadPolicy(t)
		// The cached database outlives the first test's temporary policy directory.
		if seedPolicy.Metadata != nil {
			seedPolicy.Metadata.OverridesDir, err = filepath.Abs("../../examples/policy/overrides")
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err := demo.Seed(ctx, st, demo.Options{Policy: seedPolicy, Seed: 1, Now: anchor,
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}); err != nil {
			st.Close()
			t.Fatal(err)
		}
		if err := st.Close(); err != nil { // checkpoints the WAL into the file
			t.Fatal(err)
		}
		if seeded.b, err = os.ReadFile(path); err != nil {
			t.Fatal(err)
		}
	})
	if seeded.b == nil {
		t.Fatal("seeding the shared demo database failed (see the first test that asked for it)")
	}
	path := filepath.Join(t.TempDir(), "theatarr.db")
	if err := os.WriteFile(path, seeded.b, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := store.Open(ctx, path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}

// TestDemoScreens seeds a database and walks every API the SPA's screens call: each answers 200 with data, the art
// comes from the seeded cache, and nothing goes out on the network.
func TestDemoScreens(t *testing.T) {
	net := &noNet{}
	saved := http.DefaultTransport
	http.DefaultTransport = net
	defer func() { http.DefaultTransport = saved }()

	ctx := context.Background()
	pol := loadPolicy(t)
	images := t.TempDir()
	st := seed(t, pol, 1, images)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	now := func() time.Time { return anchor.Add(time.Hour) }
	au, pw, err := auth.New(ctx, st, auth.Options{Logger: log, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if pw != "" {
		t.Fatal("auth created a second admin: the demo admin is missing")
	}
	session, err := au.Login(ctx, demo.Username, demo.Password, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("demo password: %v", err)
	}
	gw, err := gateway.New(ctx, pol, gateway.Options{Store: st, Logger: log, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	set := policy.ImageSettings{Dir: images, MaxBytes: policy.DefaultImageMaxBytes}
	im, err := art.NewImages(art.ImagesConfig{Settings: set, BaseURL: "https://image.tmdb.org/t/p", Doer: net, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	ar, err := art.New(art.Config{Store: st, Images: im, Settings: pol.ArtSettings(), Logger: log, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	w, err := web.New(web.Config{Store: st, Auth: au, Policy: pol, Egress: gw, Logger: log, Version: "demo", Now: now,
		Art: ar, UI: fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>spa</title>")}}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	w.Mount(mux)
	get := func(path string) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body.String())
			return rec, nil
		}
		var v map[string]any
		if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
				t.Errorf("%s: %v", path, err)
			}
		}
		return rec, v
	}
	var seriesID, animeID, movieID int64
	_ = st.Reader().QueryRow(`SELECT id FROM title WHERE kind = 'series' AND anime = 0 ORDER BY id LIMIT 1`).Scan(&seriesID)
	_ = st.Reader().QueryRow(`SELECT id FROM title WHERE name = 'Aokaze Requiem'`).Scan(&animeID)
	_ = st.Reader().QueryRow(`SELECT id FROM title WHERE kind = 'movie' ORDER BY id LIMIT 1`).Scan(&movieID)
	var hash string
	_ = st.Reader().QueryRow(`SELECT info_hash FROM content WHERE state = 'present' ORDER BY id LIMIT 1`).Scan(&hash)
	var journalID string
	{
		_, v := get(web.APIBase + "/journal?suspect=1")
		if items, _ := v["items"].([]any); len(items) > 0 {
			journalID, _ = items[0].(map[string]any)["id"].(string)
		}
	}
	// Each API with the fields that must not be empty: arrays with items, objects with keys, numbers above 0. The
	// reads run at once, as a browser's do.
	var wg sync.WaitGroup
	for path, fields := range map[string][]string{
		"/status":                                    {"counts"},
		"/auth/me":                                   {"username"},
		"/titles":                                    {"items"},
		"/titles?kind=movie&anime=0":                 {"items"},
		"/titles?kind=series&anime=0":                {"items"},
		"/titles?anime=1":                            {"items"},
		"/titles?availability=partial":               {"items"},
		"/titles?availability=upgrade":               {"items"},
		"/titles?availability=missing":               {"items"},
		"/titles/facets?kind=movie":                  {"versions", "qualities", "genres", "certifications", "providers"},
		"/titles/facets?kind=series":                 {"versions", "qualities", "genres"},
		"/discover":                                  {"recent.items", "upcoming.items", "missing.items", "upgrades.items"},
		fmt.Sprintf("/titles/%d", movieID):           {"versions", "meta", "genres"},
		fmt.Sprintf("/titles/%d", seriesID):          {"versions", "meta"},
		fmt.Sprintf("/titles/%d", animeID):           {"versions", "franchises"},
		fmt.Sprintf("/titles/%d/episodes", seriesID): {"seasons", "episodes", "versions"},
		fmt.Sprintf("/titles/%d/episodes", animeID):  {"seasons", "episodes"},
		"/activity?state=now":                        {"now", "counts"},
		"/activity?state=history":                    {"history"},
		"/activity?state=blocked":                    {"blocked"},
		"/activity/continue":                         {"items"},
		"/calendar":                                  {"items"},
		"/journal":                                   {"items"},
		"/journal?suspect=1":                         {"items"},
		"/journal/" + journalID:                      {"flags"},
		"/jobs":                                      {"items", "counts"},
		"/jobs?attention=1":                          {"items"},
		"/registry":                                  {"items"},
		"/registry/" + hash:                          {"versions"},
		"/anime/franchises":                          {"items"},
		"/anime/franchises/" + demo.FranchiseSlug:    {"orders", "mappings", "numbering"},
		"/anime/review":                              {"items"},
		"/anime/playlists":                           {"items"},
		"/gateway":                                   {"budgets", "backoffs"},
		"/sources":                                   {"indexers", "providers"},
		"/policy":                                    {"versions"},
		"/keys":                                      {"items"},
		"/versions":                                  {"items"},
		"/search?q=lantern":                          {"library"},
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, v := get(web.APIBase + path)
			if v == nil {
				return
			}
			for _, f := range fields {
				if !nonEmpty(v, f) {
					t.Errorf("%s: %s is empty", path, f)
				}
			}
		}()
	}
	wg.Wait()
	// Activity has every router state in hand.
	_, now0 := get(web.APIBase + "/activity?state=now")
	counts, _ := now0["counts"].(map[string]any)
	for _, s := range []string{"queued", "adding", "added", "downloading", "ready", "importing"} {
		if n, _ := counts[s].(float64); n == 0 {
			t.Errorf("activity: no download %s (%v)", s, counts)
		}
	}
	// Art: posters and backdrops come from the seeded cache as PNG, with no fetch.
	for _, p := range []string{"/img/t/1/poster/w342", "/img/t/1/poster/w92", "/img/t/1/backdrop/w1280",
		fmt.Sprintf("/img/t/%d/poster/w342", seriesID)} {
		rec, _ := get(p)
		if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
			t.Errorf("%s: %s, want a seeded PNG", p, ct)
		}
	}
	if calls := net.calls(); len(calls) > 0 {
		t.Errorf("network calls: %v", calls)
	}
}

func nonEmpty(v map[string]any, field string) bool {
	var cur any = v
	for part := range strings.SplitSeq(field, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		cur = m[part]
	}
	switch x := cur.(type) {
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	case float64:
		return x > 0
	case string:
		return x != ""
	}
	return false
}

// TestDemoCoverage checks the dataset has every state the screens show.
func TestDemoCoverage(t *testing.T) {
	t.Parallel()
	st := seedDemo(t)
	db := st.Reader()
	distinct := func(q string) []string {
		t.Helper()
		rows, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}
	want := func(what string, got []string, need ...string) {
		t.Helper()
		for _, n := range need {
			if !slices.Contains(got, n) {
				t.Errorf("%s: no %q in %v", what, n, got)
			}
		}
	}
	want("download states", distinct(`SELECT DISTINCT state FROM download`), "queued", "adding", "added",
		"downloading", "ready", "importing", "imported", "failed")
	want("stalled", distinct(`SELECT DISTINCT source FROM router_blocklist`), "stalled", "library", "journal")
	want("version states", distinct(`SELECT DISTINCT policy_key || ':' || state FROM version`),
		"movie-remux:wanted", "movie-remux:filled", "movie-remux:cutoff_met", "movie-web:wanted", "movie-web:cutoff_met",
		"anime-movie:wanted", "anime-movie:cutoff_met", "tv-2160p:wanted", "tv-2160p:filled", "tv-2160p:cutoff_met",
		"anime-tv:wanted", "anime-tv:filled", "anime-tv:cutoff_met")
	want("episode states", distinct(`SELECT DISTINCT state FROM episode_version_state`), "wanted", "filled", "cutoff_met")
	want("link states", distinct(`SELECT DISTINCT state FROM link`), "active", "dead")
	want("library imports", distinct(`SELECT DISTINCT state FROM library_import`), "waiting_mount", "linked", "rejected", "dry_run")
	want("job states", distinct(`SELECT DISTINCT state FROM job`), "queued", "done", "failed")
	want("job marks", distinct(`SELECT CASE WHEN dismissed_at IS NOT NULL THEN 'dismissed' WHEN retry_of IS NOT NULL
		THEN 'retry' ELSE 'plain' END FROM job`), "dismissed", "retry", "plain")
	want("verdicts", distinct(`SELECT DISTINCT verdict FROM journal_verdict`), "ok", "wrong")
	want("attempt outcomes", distinct(`SELECT DISTINCT outcome FROM download_attempt`), "accepted", "refused",
		"skipped", "error", "deferred")
	want("cache evidence", distinct(`SELECT DISTINCT state FROM provider_cache`), "cached", "uncached")
	want("content states", distinct(`SELECT DISTINCT state FROM content`), "present", "unreferenced", "absent", "gone")
	want("anime mappings", distinct(`SELECT DISTINCT status FROM anime_mapping`), "auto", "arbitrated", "heuristic",
		"conflict")
	want("request statuses", distinct(`SELECT DISTINCT status FROM request`), "pending", "approved", "declined",
		"canceled", "processing", "available", "failed")
	var future int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM title_release_date WHERE date > '2026-10-26') +
		(SELECT COUNT(*) FROM episode WHERE air_date > '2026-10-26')`).Scan(&future); err != nil || future < 10 {
		t.Errorf("upcoming dates: %d %v", future, err)
	}
	// Ids stay in the demo ranges.
	var bad int
	if err := db.QueryRow(`SELECT COUNT(*) FROM title WHERE tmdb_id < ? OR (tvdb_id IS NOT NULL AND tvdb_id < ?)
		OR imdb_id NOT LIKE 'tt99%'`, demo.TMDbMovieBase, demo.TVDBBase).Scan(&bad); err != nil || bad > 0 {
		t.Errorf("titles outside the demo id ranges: %d %v", bad, err)
	}
}

func TestDemoUsersRequests(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pol := loadPolicy(t)
	st := seedDemo(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	now := func() time.Time { return anchor.Add(time.Hour) }
	au, pw, err := auth.New(ctx, st, auth.Options{Logger: log, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if pw != "" {
		t.Fatal("auth created a second admin: the demo admin is missing")
	}
	gw, err := gateway.New(ctx, pol, gateway.Options{Store: st, Logger: log, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	w, err := web.New(web.Config{Store: st, Auth: au, Policy: pol, Egress: gw, Logger: log, Version: "demo", Now: now,
		UI: fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>spa</title>")}}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	w.Mount(mux)
	accounts := []struct {
		username, role string
		autoApprove    bool
	}{
		{demo.Username, auth.RoleAdmin, false},
		{demo.ManagerUsername, auth.RoleManager, false},
		{demo.AutoApproveUsername, auth.RoleUser, true},
		{demo.LimitedUsername, auth.RoleUser, false},
	}
	sessions := make(map[string]string, len(accounts))
	userIDs := make(map[string]int64, len(accounts))
	for _, account := range accounts {
		token, err := au.Login(ctx, account.username, demo.Password, "127.0.0.1", "demo test")
		if err != nil {
			t.Fatalf("%s sign in: %v", account.username, err)
		}
		sessions[account.username] = token
		var userID int64
		if err := st.Reader().QueryRowContext(ctx, `SELECT id FROM web_user WHERE username = ?`, account.username).
			Scan(&userID); err != nil {
			t.Fatalf("%s user id: %v", account.username, err)
		}
		userIDs[account.username] = userID
		user, err := au.GetUser(ctx, userID)
		if err != nil {
			t.Fatalf("%s user: %v", account.username, err)
		}
		if user.Role != account.role || user.Status != auth.StatusActive || !user.Local || user.AutoApprove != account.autoApprove {
			t.Errorf("%s account settings: %+v", account.username, user)
		}
		if !slices.Equal(user.Permissions, auth.Effective(account.role, 0, account.autoApprove).Names()) {
			t.Errorf("%s permissions: %v", account.username, user.Permissions)
		}
		if account.username == demo.LimitedUsername && len(user.RequestVersions) != 2 {
			t.Errorf("%s request versions: %v, want a restricted set", account.username, user.RequestVersions)
		}
	}
	get := func(username, path string) (*httptest.ResponseRecorder, string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessions[username]})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec, rec.Body.String()
	}
	managerRec, managerBody := get(demo.ManagerUsername, web.APIBase+"/requests")
	if managerRec.Code != http.StatusOK {
		t.Fatalf("manager GET /requests: %d %s", managerRec.Code, managerBody)
	}
	var all web.RequestPage
	if err := json.Unmarshal([]byte(managerBody), &all); err != nil {
		t.Fatal(err)
	}
	if len(all.Items) < 7 {
		t.Fatalf("manager sees %d requests, want every seeded request", len(all.Items))
	}
	statuses := map[string]bool{}
	merged := false
	for _, request := range all.Items {
		statuses[request.Status] = true
		merged = merged || request.Others > 0
	}
	for _, status := range []string{"pending", "approved", "declined", "canceled", "processing", "available", "failed"} {
		if !statuses[status] {
			t.Errorf("manager requests lack %q status", status)
		}
	}
	if !merged {
		t.Error("manager requests contain no merged request")
	}
	for _, username := range []string{demo.AutoApproveUsername, demo.LimitedUsername} {
		rec, body := get(username, web.APIBase+"/requests")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s GET /requests: %d %s", username, rec.Code, body)
		}
		var page web.RequestPage
		if err := json.Unmarshal([]byte(body), &page); err != nil {
			t.Fatal(err)
		}
		rows, err := st.Reader().QueryContext(ctx, `SELECT id FROM request WHERE user_id = ? UNION
			SELECT request_id FROM request_user WHERE user_id = ? ORDER BY 1`, userIDs[username], userIDs[username])
		if err != nil {
			t.Fatal(err)
		}
		want := map[int64]bool{}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			want[id] = true
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if page.Total != len(want) || len(page.Items) != len(want) {
			t.Errorf("%s sees %d requests (total %d), want %d", username, len(page.Items), page.Total, len(want))
		}
		for _, request := range page.Items {
			if !want[request.ID] {
				t.Errorf("%s sees request %d belonging to another user", username, request.ID)
			}
			delete(want, request.ID)
		}
		for id := range want {
			t.Errorf("%s cannot see own request %d", username, id)
		}
	}
	for _, tmdbID := range []int{demo.TMDbMovieBase + 900, demo.TMDbTVBase + 900} {
		var requestCount, titleCount int
		if err := st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM request WHERE tmdb_id = ? AND title_id IS NULL`,
			tmdbID).Scan(&requestCount); err != nil {
			t.Fatal(err)
		}
		if err := st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM title WHERE tmdb_id = ?`, tmdbID).
			Scan(&titleCount); err != nil {
			t.Fatal(err)
		}
		if requestCount != 1 || titleCount != 0 {
			t.Errorf("TMDb id %d: %d external requests, %d library titles", tmdbID, requestCount, titleCount)
		}
	}
	var titleID int64
	if err := st.Reader().QueryRowContext(ctx, `SELECT id FROM title WHERE name = 'The Brassmoor Accord'`).Scan(&titleID); err != nil {
		t.Fatal(err)
	}
	adminRec, adminBody := get(demo.ManagerUsername, fmt.Sprintf("%s/titles/%d", web.APIBase, titleID))
	if adminRec.Code != http.StatusOK || !strings.Contains(adminBody, `"release_title"`) {
		t.Fatalf("manager title details: %d %s", adminRec.Code, adminBody)
	}
	userRec, userBody := get(demo.AutoApproveUsername, fmt.Sprintf("%s/titles/%d", web.APIBase, titleID))
	if userRec.Code != http.StatusOK {
		t.Fatalf("user title: %d %s", userRec.Code, userBody)
	}
	for _, field := range []string{`"size_bytes"`, `"providers"`, `"score"`, `"path"`, `"info_hash"`,
		`"release_title"`, `"release_group"`, `"instance"`} {
		if strings.Contains(userBody, field) {
			t.Errorf("user title contains file detail %s", field)
		}
	}
}

// TestDemoDeterministic seeds twice with the same seed and once with another: the same seed gives the same rows.
func TestDemoDeterministic(t *testing.T) {
	t.Parallel()
	pol := loadPolicy(t)
	// Three separate databases, seeded at once: the group returns when all three are done.
	var a, b, c string
	t.Run("seed", func(t *testing.T) {
		for i, run := range []struct {
			digest *string
			seed   uint64
		}{{&a, 7}, {&b, 7}, {&c, 8}} {
			t.Run(fmt.Sprint(i), func(t *testing.T) {
				t.Parallel()
				*run.digest = digest(t, seed(t, pol, run.seed, ""))
			})
		}
	})
	if t.Failed() {
		return
	}
	if a != b {
		t.Error("the same seed gave different data")
	}
	if a == c {
		t.Error("another seed gave the same data")
	}
}

// digest hashes the demo tables and stable user fields, excluding password hashes and the signing secret.
func digest(t *testing.T, st *store.Store) string {
	t.Helper()
	db := st.Reader()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		AND name NOT IN ('web_setting', 'web_session', 'goose_db_version') ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	h := sha256.New()
	for _, tb := range tables {
		query := `SELECT * FROM "` + tb + `" ORDER BY 1, 2`
		if tb == "web_user" {
			query = `SELECT id, username, created_at, updated_at, password_changed_at, role, permissions,
				status, disabled_at, display_name, email, avatar_url, plex_id, plex_username, request_versions,
				auto_approve, last_login_at FROM web_user ORDER BY id`
		}
		r, err := db.Query(query)
		if err != nil && tb != "web_user" {
			r, err = db.Query(`SELECT * FROM "` + tb + `" ORDER BY 1`)
		}
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := r.Columns()
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for r.Next() {
			if err := r.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(h, "%s%v\n", tb, vals)
		}
		r.Close()
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestDemoCeilings(t *testing.T) {
	t.Parallel()
	st := seedDemo(t)
	for _, state := range []string{"best_available", "best_source_seen", "higher_source_exists", "below_cutoff"} {
		var n int
		if err := st.Reader().QueryRow(`SELECT COUNT(*) FROM version_ceiling WHERE upgrade_state=?`, state).Scan(&n); err != nil || n < 2 {
			t.Errorf("%s examples: %d (%v)", state, n, err)
		}
	}
	for _, status := range []string{"known", "likely", "unknown"} {
		var n int
		if err := st.Reader().QueryRow(`SELECT COUNT(*) FROM version_ceiling WHERE json_extract(ceiling,'$.status')=?`, status).Scan(&n); err != nil || n < 2 {
			t.Errorf("%s evidence: %d (%v)", status, n, err)
		}
	}
}

// The demo database starts on database settings: the first-start import is done with invented credentials and a
// few admin edits are in the audit, anchored at the seed's now, so theatarr serve starts on it.
func TestDemoSettings(t *testing.T) {
	t.Parallel()
	pol := loadPolicy(t)
	st := seedDemo(t)
	ss, err := settings.New(st)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	snap, err := ss.Load(ctx, pol)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Generation != 6 {
		t.Errorf("generation %d, want 6 (four imports and two edits)", snap.Generation)
	}
	if !reflect.DeepEqual(snap.Policy.Versions, pol.Versions) {
		t.Fatal("demo source edits changed version settings")
	}
	// Views keep their paths, flavour and bindings; only the credential reference becomes the stored key's.
	views := func(in map[string]*policy.View) map[string]policy.View {
		out := map[string]policy.View{}
		for k, v := range in {
			c := *v
			c.APIKeyEnv = ""
			out[k] = c
		}
		return out
	}
	if got, want := views(snap.Policy.Views), views(pol.Views); len(want) == 0 || !reflect.DeepEqual(got, want) {
		t.Fatalf("demo views = %+v, want the policy's %+v", got, want)
	}
	if len(snap.Records.Indexers) != len(pol.Indexers) || len(snap.Records.Providers) != len(pol.Providers) || len(snap.Records.Budgets) != len(pol.Budgets) {
		t.Errorf("records %d/%d/%d, want the policy's %d/%d/%d", len(snap.Records.Indexers), len(snap.Records.Providers),
			len(snap.Records.Budgets), len(pol.Indexers), len(pol.Providers), len(pol.Budgets))
	}
	for _, ix := range snap.Records.Indexers {
		if ix.Key == "tracker-example" && (ix.Enabled || ix.SecretSet) {
			t.Errorf("tracker-example: enabled %v, secret set %v; want disabled with no credential", ix.Enabled, ix.SecretSet)
		}
		if ix.Key == "zilean" && ix.Priority != 10 {
			t.Errorf("zilean priority %d, want 10", ix.Priority)
		}
	}
	for _, p := range snap.Records.Providers {
		if !p.Enabled || !p.SecretSet {
			t.Errorf("provider %s: enabled %v, secret set %v; want both", p.Key, p.Enabled, p.SecretSet)
		}
	}
	audit, err := ss.Audit(ctx, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	ops := map[string]int{}
	for _, a := range audit {
		ops[a.Operation]++
		if !a.CreatedAt.Before(anchor) {
			t.Errorf("audit %d at %v, want before the anchor %v", a.ID, a.CreatedAt, anchor)
		}
		if strings.Contains(string(a.Before)+string(a.After), "demo-only-") {
			t.Errorf("audit %d holds a credential value", a.ID)
		}
	}
	if ops["bootstrap-import"] == 0 || ops["test"] != 2 || ops["update"] == 0 {
		t.Errorf("audit operations %v, want bootstrap-import, updates and two tests", ops)
	}
}

// The demo overlay's library.yaml is the example's with Theatarr's copies' roots added, and nothing else.
func TestDemoOverlayLibrary(t *testing.T) {
	t.Parallel()
	read := func(dir string) map[string]any {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", "..", "examples", dir, "library.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := yaml.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	example, overlay := read("policy"), read("demo")
	pol := loadPolicy(t)
	versions := overlay["library"].(map[string]any)["versions"].(map[string]any)
	var copies []string
	for k := range pol.Versions {
		base, ok := strings.CutSuffix(k, "-theatarr")
		if !ok || pol.Versions[base] == nil {
			continue
		}
		copies = append(copies, k)
		if versions[k] == nil {
			t.Errorf("overlay library.yaml: no roots for Theatarr's copy %s", k)
		}
		delete(versions, k)
	}
	if len(copies) == 0 {
		t.Fatal("the demo policy has no Theatarr copy")
	}
	if !reflect.DeepEqual(example, overlay) {
		t.Error("examples/demo/library.yaml differs from examples/policy/library.yaml beyond the copies' roots")
	}
}

// Seeding with examples/policy alone still works: one copy per series.
func TestDemoExamplePolicy(t *testing.T) {
	t.Parallel()
	st := seed(t, examplePolicy(t), 1, "")
	var two int
	if err := st.Reader().QueryRow(`SELECT COUNT(*) FROM title t WHERE kind = 'series'
		AND (SELECT COUNT(*) FROM version v WHERE v.title_id = t.id) <> 1`).Scan(&two); err != nil || two != 0 {
		t.Errorf("series without exactly one copy: %d %v", two, err)
	}
}

// TestDemoTwoCopies: every series has an arr-fed copy and Theatarr's own (<key>-theatarr), and the copies differ
// the way they do live: seasons on one copy only, Theatarr's own downloads in every state, an upgrade on a file the copy
// has, different monitoring, and anime absolute numbers on both.
func TestDemoTwoCopies(t *testing.T) {
	t.Parallel()
	st := seedDemo(t)
	db := st.Reader()
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
		return n
	}
	// Two copies per series: one fed by its arr, and Theatarr's (<key>-theatarr) with no arr records of its own.
	if n := count(`SELECT COUNT(*) FROM title t WHERE kind = 'series' AND NOT (
		(SELECT COUNT(*) FROM version v WHERE v.title_id = t.id) = 2 AND
		(SELECT COUNT(*) FROM version v JOIN version_source s ON s.version_id = v.id WHERE v.title_id = t.id) = 1 AND
		(SELECT COUNT(*) FROM version v JOIN (SELECT c.id AS version_id, o.id AS follows_id FROM version c JOIN version o ON o.title_id = c.title_id AND c.policy_key = o.policy_key || '-theatarr') f ON f.version_id = v.id JOIN version o ON o.id = f.follows_id
			AND o.title_id = t.id JOIN version_source s ON s.version_id = o.id WHERE v.title_id = t.id) = 1)`); n != 0 {
		t.Errorf("%d series without an arr-fed copy and Theatarr's copy of it", n)
	}
	if n := count(`SELECT COUNT(*) FROM version_file vf JOIN (SELECT c.id AS version_id, o.id AS follows_id FROM version c JOIN version o ON o.title_id = c.title_id AND c.policy_key = o.policy_key || '-theatarr') f ON f.version_id = vf.version_id`); n != 0 {
		t.Errorf("Theatarr's copies hold %d arr records", n)
	}
	// Theatarr's copies start from the arr's files (adopted links to the same mount targets).
	if n := count(`SELECT COUNT(*) FROM library_action a JOIN link_version lv ON lv.link_id = a.link_id
		JOIN (SELECT c.id AS version_id, o.id AS follows_id FROM version c JOIN version o ON o.title_id = c.title_id AND c.policy_key = o.policy_key || '-theatarr') f ON f.version_id = lv.version_id WHERE a.detail LIKE 'adopted from %'`); n < 100 {
		t.Errorf("%d adopted links", n)
	}
	if n := count(`SELECT COUNT(*) FROM link l JOIN link_version lv ON lv.link_id = l.id
		JOIN (SELECT c.id AS version_id, o.id AS follows_id FROM version c JOIN version o ON o.title_id = c.title_id AND c.policy_key = o.policy_key || '-theatarr') f ON f.version_id = lv.version_id JOIN version v ON v.id = lv.version_id
		WHERE l.path NOT LIKE '/media/library/%'`); n != 0 {
		t.Errorf("%d links of Theatarr's copies outside their roots", n)
	}
	// Every episode Theatarr's copy has, it has a link for (adopted or imported), and no link for what it lacks.
	if n := count(`SELECT COUNT(*) FROM (SELECT c.id AS version_id, o.id AS follows_id FROM version c JOIN version o ON o.title_id = c.title_id AND c.policy_key = o.policy_key || '-theatarr') f JOIN episode_version_state es ON es.version_id = f.version_id
		JOIN episode e ON e.id = es.episode_id WHERE e.season > 0 AND (es.state <> 'wanted') <> EXISTS (SELECT 1
		FROM link_episode le JOIN link l ON l.id = le.link_id AND l.state = 'active'
		JOIN link_version lv ON lv.link_id = l.id AND lv.version_id = es.version_id WHERE le.episode_id = e.id)`); n != 0 {
		t.Errorf("%d episodes of Theatarr's copies whose state and links disagree", n)
	}
	// Per-copy differences: a season on one copy only, both ways, and a series complete on one copy, partial on the
	// other, both ways.
	pair := `FROM (SELECT c.id AS version_id, o.id AS follows_id FROM version c JOIN version o ON o.title_id = c.title_id AND c.policy_key = o.policy_key || '-theatarr') f JOIN version theatarr ON theatarr.id = f.version_id JOIN version arr ON arr.id = f.follows_id`
	if n := count(`SELECT COUNT(*) ` + pair + ` WHERE arr.state = 'cutoff_met' AND theatarr.state = 'wanted'`); n < 2 {
		t.Errorf("%d series complete on the arr's copy and partial on Theatarr's", n)
	}
	if n := count(`SELECT COUNT(*) ` + pair + ` WHERE arr.state = 'wanted' AND theatarr.state = 'cutoff_met'`); n < 1 {
		t.Errorf("%d series partial on the arr's copy and complete on Theatarr's", n)
	}
	if n := count(`SELECT COUNT(*) FROM (SELECT DISTINCT e.title_id, e.season ` + pair + `
		JOIN episode e ON e.title_id = theatarr.title_id
		JOIN episode_version_state a ON a.version_id = arr.id AND a.episode_id = e.id
		JOIN episode_version_state m ON m.version_id = theatarr.id AND m.episode_id = e.id
		WHERE e.season > 0 GROUP BY e.title_id, e.season
		HAVING SUM(a.state <> 'wanted') = COUNT(*) AND SUM(m.state <> 'wanted') = 0)`); n < 2 {
		t.Errorf("%d seasons the arr's copy has and Theatarr's lacks", n)
	}
	// Theatarr's own downloads for its copies, in every state.
	var states []string
	rows, err := db.Query(`SELECT DISTINCT d.state FROM download d JOIN (SELECT c.id AS version_id, o.id AS follows_id FROM version c JOIN version o ON o.title_id = c.title_id AND c.policy_key = o.policy_key || '-theatarr') f ON f.version_id = d.version_id
		ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		states = append(states, s)
	}
	rows.Close()
	for _, s := range []string{"queued", "added", "downloading", "importing", "failed", "imported"} {
		if !slices.Contains(states, s) {
			t.Errorf("Theatarr's copies: no download %s (%v)", s, states)
		}
	}
	if n := count(`SELECT COUNT(*) FROM download d JOIN (SELECT c.id AS version_id, o.id AS follows_id FROM version c JOIN version o ON o.title_id = c.title_id AND c.policy_key = o.policy_key || '-theatarr') f ON f.version_id = d.version_id
		JOIN library_import i ON i.download_id = d.id WHERE d.state = 'importing' AND i.state = 'waiting_mount'`); n == 0 {
		t.Error("Theatarr's copies: no import waiting for the mount")
	}
	// An upgrade in flight for an episode the copy already has a file for.
	if n := count(`SELECT COUNT(*) FROM download d JOIN (SELECT c.id AS version_id, o.id AS follows_id FROM version c JOIN version o ON o.title_id = c.title_id AND c.policy_key = o.policy_key || '-theatarr') f ON f.version_id = d.version_id
		JOIN json_each(d.episodes) je JOIN link_episode le ON le.episode_id = je.value
		JOIN link l ON l.id = le.link_id AND l.state = 'active'
		JOIN link_version lv ON lv.link_id = l.id AND lv.version_id = d.version_id
		JOIN episode_version_state es ON es.version_id = d.version_id AND es.episode_id = je.value AND es.state = 'filled'
		JOIN acquire_run r ON 'acquire:' || r.id = d.source AND r.reason = 'cutoff'
		WHERE d.state IN ('queued', 'adding', 'added', 'downloading', 'ready', 'importing')`); n == 0 {
		t.Error("no upgrade in flight on an episode Theatarr's copy has a file for")
	}
	// Monitoring that differs between the copies of one series.
	if n := count(`SELECT COUNT(DISTINCT theatarr.title_id) ` + pair + `
		JOIN episode_version_state a ON a.version_id = arr.id
		JOIN episode_version_state m ON m.version_id = theatarr.id AND m.episode_id = a.episode_id
		WHERE a.monitored <> m.monitored`); n != 1 {
		t.Errorf("%d series whose copies' monitoring differs, want 1", n)
	}
	// Anime: absolute numbers, and files on both copies.
	if n := count(`SELECT COUNT(DISTINCT v.policy_key) FROM title t JOIN version v ON v.title_id = t.id
		JOIN episode_version_state es ON es.version_id = v.id AND es.state <> 'wanted'
		JOIN episode_number n ON n.episode_id = es.episode_id AND n.scheme = 'absolute'
		WHERE t.anime = 1 AND t.kind = 'series'`); n != 2 {
		t.Errorf("anime copies with absolute-numbered files: %d, want 2", n)
	}
}

// The title page of a two-copy series shows both copies, episode by episode, absolute numbers for anime.
func TestDemoTwoCopiesScreens(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pol := loadPolicy(t)
	st := seedDemo(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	now := func() time.Time { return anchor.Add(time.Hour) }
	au, _, err := auth.New(ctx, st, auth.Options{Logger: log, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	session, err := au.Login(ctx, demo.Username, demo.Password, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	gw, err := gateway.New(ctx, pol, gateway.Options{Store: st, Logger: log, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	w, err := web.New(web.Config{Store: st, Auth: au, Policy: pol, Egress: gw, Logger: log, Version: "demo", Now: now,
		UI: fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>spa</title>")}}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	w.Mount(mux)
	for _, name := range []string{"The Harrowgate Files", "Aokaze Requiem"} {
		var id int64
		if err := st.Reader().QueryRow(`SELECT id FROM title WHERE name = ?`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("%s/titles/%d/episodes", web.APIBase, id), nil)
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		var list web.EpisodeList
		if err := json.Unmarshal(rec.Body.Bytes(), &list); rec.Code != http.StatusOK || err != nil {
			t.Fatalf("%s episodes: %d %v", name, rec.Code, err)
		}
		if len(list.Versions) != 2 {
			t.Fatalf("%s: %d copies, want 2", name, len(list.Versions))
		}
		have := map[string]int{}
		for _, e := range list.Episodes {
			if e.Season > 0 && strings.HasPrefix(name, "Aokaze") && e.Absolute == nil {
				t.Errorf("%s S%02dE%02d: no absolute number", name, e.Season, e.Number)
			}
			for k, c := range e.Versions {
				if c.HasFile {
					have[k]++
				}
			}
		}
		for _, v := range list.Versions {
			if have[v.Key] == 0 {
				t.Errorf("%s: copy %s has no file", name, v.Key)
			}
		}
	}
}

// TestDemoLibraryViews: the library views' per-copy reads have something to show: editable
// copies, series coverage, downloads in hand at each stage, next episodes and releases, episodes in flight, and a
// missing movie in cinemas with its home release ahead (Upcoming with that date), none of them in Home's Missing row.
func TestDemoLibraryViews(t *testing.T) {
	ctx := context.Background()
	pol := loadPolicy(t)
	st := seedDemo(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	now := func() time.Time { return anchor.Add(time.Hour) }
	au, _, err := auth.New(ctx, st, auth.Options{Logger: log, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	session, err := au.Login(ctx, demo.Username, demo.Password, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	gw, err := gateway.New(ctx, pol, gateway.Options{Store: st, Logger: log, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	w, err := web.New(web.Config{Store: st, Auth: au, Policy: pol, Egress: gw, Logger: log, Version: "demo", Now: now,
		UI: fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>spa</title>")}}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	w.Mount(mux)
	get := func(path string, out any) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, web.APIBase+path, nil)
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	stages := map[string]int{}
	var editable, coverage, next, movieNext, movieTwo, inCinemas int
	today := now().UTC().Format(time.DateOnly)
	for _, kind := range []string{"movie", "series"} {
		var page web.TitlePage
		get("/titles?limit=500&kind="+kind, &page)
		for _, r := range page.Items {
			if r.Next != nil {
				next++
				if kind == "movie" {
					movieNext++
				}
			}
			if kind == "movie" && len(r.Versions) > 1 {
				movieTwo++
			}
			if r.Availability == "missing" && r.ReleaseDate != "" && r.ReleaseDate <= today && r.HomeRelease > today {
				inCinemas++
			}
			for _, v := range r.Versions {
				switch {
				case v.Control == nil:
					t.Errorf("%s %s: no control", r.Name, v.Key)
				case !v.Control.CanEdit:
					t.Errorf("%s %s: not editable", r.Name, v.Key)
				default:
					editable++
				}
				if v.Coverage != nil && v.Coverage.Total > 0 {
					coverage++
				}
				if v.Acquisition != nil {
					stages[v.Acquisition.Stage]++
				}
			}
		}
	}
	if editable == 0 || coverage == 0 || movieNext == 0 || next <= movieNext || movieTwo == 0 {
		t.Errorf("editable %d, coverage %d, next %d (movies %d), two-copy movies %d", editable, coverage, next,
			movieNext, movieTwo)
	}
	if inCinemas == 0 {
		t.Error("no missing movie is in cinemas with its home release ahead")
	}
	var missing web.DiscoverRow
	get("/discover/missing", &missing)
	for _, it := range missing.Items {
		if it.HomeRelease > today {
			t.Errorf("%s is in the missing row with home_release %s", it.Name, it.HomeRelease)
		}
	}
	for _, s := range activity.Stages {
		if stages[s] == 0 {
			t.Errorf("no copy at stage %s: %v", s, stages)
		}
	}
	var id int64
	if err := st.Reader().QueryRow(`SELECT id FROM title WHERE name = 'Kiraboshi Mechanics'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	var list web.EpisodeList
	get(fmt.Sprintf("/titles/%d/episodes", id), &list)
	inFlight := 0
	for _, e := range list.Episodes {
		for _, c := range e.Versions {
			if c.Acquisition != nil {
				inFlight++
			}
		}
	}
	if inFlight == 0 {
		t.Error("Kiraboshi Mechanics: no episode in flight")
	}
	t.Logf("stages %v; editable %d next %d movies with two copies %d", stages, editable, next, movieTwo)
}

// TestDemoJournalCounts checks that a download's journal entry counts the results its run and its notes report.
func TestDemoJournalCounts(t *testing.T) {
	t.Parallel()
	st := seedDemo(t)
	rows, err := st.Reader().Query(`SELECT j.results, j.notes, r.results FROM journal_decision j
		JOIN acquire_run r ON j.source = 'acquire:' || r.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var results, run int
		var notes string
		if err := rows.Scan(&results, &notes, &run); err != nil {
			t.Fatal(err)
		}
		n++
		if results != run || !strings.Contains(notes, fmt.Sprintf("%d results from", results)) {
			t.Errorf("journal entry counts %d results, its run %d, its notes %q", results, run, notes)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no download journal entries")
	}
}
