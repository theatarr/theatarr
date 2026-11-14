package arrview

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/web"
)

// The native activity (internal/web/activity.go) and the arr views' queue read the same downloads through
// internal/activity: with a download in every state, the activity's "now" holds exactly the queue's items (Radarr:
// one per download; Sonarr: one per episode), every state reads in plain words, and the user view carries no
// release names or providers (docs/design/ui.md acceptance 3b).
func TestActivityMatchesQueue(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	created := ms(testNow)
	states := []struct {
		id      int
		title   int
		version int
		key     string
		state   string
		prov    any
		eps     string
		err     any
	}{
		{1, 1, 2, "movie-web", "queued", nil, "[]", "waiting for a provider until 2026-10-26T12:05:00Z"},
		{2, 1, 1, "movie-remux", "adding", "rd", "[]", nil},
		{3, 3, 4, "anime-movie", "added", "ad", "[]", nil},
		{4, 1, 2, "movie-web", "downloading", "rd", "[]", nil},
		{5, 3, 4, "anime-movie", "ready", "tb", "[]", nil},
		{6, 1, 1, "movie-remux", "importing", "rd", "[]", nil},
		{7, 1, 2, "movie-web", "imported", "rd", "[]", nil},
		{8, 1, 1, "movie-remux", "failed", "rd", "[]", "stalled: not ready 10m0s after the add"},
		{9, 10, 10, "tv-2160p", "downloading", "ad", "[101, 102]", nil},
		{10, 10, 10, "tv-2160p", "failed", nil, "[103]", "miss on every provider"},
	}
	for _, s := range states {
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
			score, size_bytes, episodes, provider, provider_torrent_id, progress, state, error, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 'dmm', 'WEBDL-2160p', 100, 1000, ?, ?, 'T', 40, ?, ?, ?, ?)`,
			s.id, s.title, s.version, s.key, strings.Repeat(string(rune('a'+s.id)), 40), "Secret.Release.Name."+s.state,
			s.eps, s.prov, s.state, s.err, created+int64(s.id), created+int64(s.id))
	}
	e.exec(`INSERT INTO library_import (download_id, state, created_at, updated_at) VALUES (5, 'waiting_mount', ?, ?)`,
		created, created)

	w, err := web.New(web.Config{Store: e.st, Auth: e.au, Policy: e.pol,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	w.Mount(mux)
	e.svc.Mount(mux)

	var radarr, sonarr struct {
		TotalRecords int
		Records      []queueResource
	}
	getJSON(t, mux, "/radarr/api/v3/queue?pageSize=100", e.key, &radarr)
	getJSON(t, mux, "/sonarr/api/v3/queue?pageSize=100", e.key, &sonarr)
	var now web.ActivityPage
	getJSON(t, mux, web.APIBase+"/activity", e.key, &now)
	movies, episodes := 0, 0
	for _, d := range now.Now {
		if d.Title.Kind == "series" {
			episodes += len(d.Episodes)
		} else {
			movies++
		}
	}
	if radarr.TotalRecords != 7 || movies != 7 {
		t.Errorf("activity now has %d movie downloads (want 7 including failed), the Radarr queue %d (want 7 including stalled)", movies, radarr.TotalRecords)
	}
	var pending int
	for _, item := range radarr.Records {
		if item.ID == 8 && item.TrackedDownloadState == "failedPending" && item.TrackedDownloadStatus == "error" {
			pending++
		}
	}
	if pending != 1 {
		t.Errorf("stalled download missing its failedPending state: %+v", radarr.Records)
	}
	if sonarr.TotalRecords != 2 || episodes != 3 {
		t.Errorf("activity now has %d episodes, the Sonarr queue %d (want 2)", episodes, sonarr.TotalRecords)
	}
	if now.Counts["total"] != 9 || now.Total != 9 {
		t.Errorf("counts %v total %d", now.Counts, now.Total)
	}
	for _, s := range []string{"queued", "adding", "added", "downloading", "ready", "importing"} {
		if now.Counts[s] == 0 {
			t.Errorf("no %s in counts %v", s, now.Counts)
		}
	}
	// Every state reads in words (now for those in hand, history for imported and failed).
	want := map[int64]string{1: "Waiting for a free slot", 2: "Adding to Real-Debrid", 3: "Added to AllDebrid",
		4: "Downloading on Real-Debrid (40%)", 5: "Waiting for the mount", 6: "Linking into the library",
		9: "Downloading on AllDebrid (40%)"}
	for _, d := range now.Now {
		if w := want[d.ID]; w != "" && !strings.HasPrefix(d.Detail, w) {
			t.Errorf("download %d (%s): detail %q, want %q", d.ID, d.State, d.Detail, w)
		}
		if d.Release == "" || d.Provider == "" && d.State != "queued" && d.State != "failed" || d.Journal == "" || len(d.Steps) != 5 {
			t.Errorf("admin fields missing on %d: %+v", d.ID, d)
		}
		if strings.Contains(d.Words, "Debrid") {
			t.Errorf("plain words name the provider: %q", d.Words)
		}
	}
	var hist web.ActivityPage
	getJSON(t, mux, web.APIBase+"/activity?state=history", e.key, &hist)
	kinds := map[string]bool{}
	for _, h := range hist.History {
		kinds[h.Kind+":"+h.Words] = true
	}
	for _, k := range []string{"stall:Failed", "failure:Failed", "grab:Grabbed"} {
		if !kinds[k] {
			t.Errorf("history lacks %s: %v", k, kinds)
		}
	}

	// The user view: plain words, no release names, providers, hashes or attempts; blocked is refused.
	r := httptest.NewRequest("GET", web.APIBase+"/activity?as=user", nil)
	r.Header.Set("X-Api-Key", e.key)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	body := rec.Body.String()
	for _, leak := range []string{"Secret.Release", "Debrid", `"provider"`, `"info_hash"`, `"attempts"`, `"release"`} {
		if strings.Contains(body, leak) {
			t.Errorf("user view carries %s: %s", leak, body)
		}
	}
	for _, path := range []string{"/activity?state=history&as=user"} {
		r := httptest.NewRequest("GET", web.APIBase+path, nil)
		r.Header.Set("X-Api-Key", e.key)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		if strings.Contains(rec.Body.String(), "Secret.Release") || strings.Contains(rec.Body.String(), `"summary"`) {
			t.Errorf("%s leaks: %s", path, rec.Body.String())
		}
	}
	r = httptest.NewRequest("GET", web.APIBase+"/activity?state=blocked&as=user", nil)
	r.Header.Set("X-Api-Key", e.key)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Errorf("blocked as user: %d", rec.Code)
	}

	// The Continue row: one item per title, its most advanced download.
	var row web.ContinueRow
	getJSON(t, mux, web.APIBase+"/activity/continue", e.key, &row)
	if row.Total != 3 || len(row.Items) != 3 || row.More != "/activity" {
		t.Fatalf("continue %+v", row)
	}
	for _, it := range row.Items {
		if it.ID == 1 && it.Download != 6 || it.ID == 3 && it.Download != 5 || it.ID == 10 && it.Download != 9 {
			t.Errorf("continue item %+v", it)
		}
	}
}

func getJSON(t *testing.T, mux http.Handler, path, key string, v any) {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("X-Api-Key", key)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}
