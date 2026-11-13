package web

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// seedCopies adds to seedManage: Alpha's second copy movie-web (Theatarr-managed, wanted), The Show's S02E01 still
// to air, and downloads in hand: S01E02 downloading (37%) and S01E01 complete but waiting for the mount for
// tv-2160p, and a queued upgrade of Alpha's movie-remux.
func seedCopies(e *manageEnv) {
	now := store.Millis(addNow)
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
		(2, 1, 'movie-web', 1, 'wanted', ?, ?)`, now, now)
	e.exec(`UPDATE episode SET air_date = '2026-11-02' WHERE id = 103`)
	airAtMidnight(e.exec)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		episodes, progress, state, created_at, updated_at) VALUES
		(1, 10, 10, 'tv-2160p', 'aa', 'The.Show.S01E02.2160p', 'dmm', 'WEBDL-2160p', '[102]', 37, 'downloading', ?, ?),
		(2, 10, 10, 'tv-2160p', 'bb', 'The.Show.S01E01.2160p', 'dmm', 'WEBDL-2160p', '[101]', 100, 'ready', ?, ?),
		(3, 1, 1, 'movie-remux', 'cc', 'Alpha.2020.Remux', 'dmm', 'Remux-2160p', '[]', 0, 'queued', ?, ?),
		(4, 1, 1, 'movie-remux', 'dd', 'Alpha.Old', 'dmm', '', '[]', 0, 'imported', ?, ?)`,
		now-5000, now-5000, now-4000, now-3000, now-9000, now-9000, now-1, now-1)
	e.exec(`INSERT INTO library_import (download_id, state, waiting_since, created_at, updated_at) VALUES
		(2, 'waiting_mount', ?, ?, ?)`, now-3000, now-3000, now-3000)
}

func rowOf(t *testing.T, p TitlePage, id int64) TitleRow {
	t.Helper()
	for _, r := range p.Items {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no title %d", id)
	return TitleRow{}
}

func copyOf(t *testing.T, r TitleRow, key string) VersionBrief {
	t.Helper()
	for _, v := range r.Versions {
		if v.Key == key {
			return v
		}
	}
	t.Fatalf("%s has no %s", r.Name, key)
	return VersionBrief{}
}

// TestCopyProjections: list rows carry each copy's controls, a series copy's coverage and one acquisition summary
// per copy; the episode grid and the title page carry it per episode and per version; users get none of it.
func TestCopyProjections(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	seedCopies(e)
	var page TitlePage
	e.call("GET", APIBase+"/titles", nil, 200, &page)
	show, alpha := rowOf(t, page, 10), rowOf(t, page, 1)

	tv := copyOf(t, show, "tv-2160p")
	if tv.Control == nil || !tv.Control.CanEdit {
		t.Errorf("a copy an arr imports is editable: %+v", tv.Control)
	}
	if c := copyOf(t, alpha, "movie-web").Control; c == nil || !c.CanEdit {
		t.Errorf("Theatarr's copy: %+v", c)
	}
	// S01E01 and S01E02 held, S02E01 not aired yet, the special left out.
	if c := tv.Coverage; c == nil || *c != (CopyCoverage{Have: 2, Total: 2, Aired: 2, Upcoming: 1, Missing: 0,
		Monitored: 3, InFlight: 2}) {
		t.Errorf("coverage: %+v", c)
	}
	if copyOf(t, alpha, "movie-remux").Coverage != nil {
		t.Error("a movie copy has no episode coverage")
	}
	// Waiting for the mount is further along than downloading; both downloads replace held episodes.
	if a := tv.Acquisition; a == nil || a.Stage != "waiting_mount" || a.DownloadID != 2 || a.Downloads != 2 ||
		a.Episodes != 2 || !a.Upgrade || a.Stages["downloading"] != 1 || a.Progress != nil || a.WaitingSince == nil ||
		a.Words != "Waiting for the mount" {
		t.Errorf("series acquisition: %+v", a)
	}
	if a := copyOf(t, alpha, "movie-remux").Acquisition; a == nil || a.Stage != "queued" || a.DownloadID != 3 ||
		a.Downloads != 1 || !a.Upgrade || a.Quality != "Remux-2160p" {
		t.Errorf("movie acquisition (the imported download is not in hand): %+v", a)
	}
	if copyOf(t, alpha, "movie-web").Acquisition != nil {
		t.Error("nothing is in hand for movie-web")
	}
	if n := show.Next; n == nil || n.Date != "2026-11-02" || *n.Season != 2 || *n.Number != 1 || n.Name != "Third" {
		t.Errorf("next episode: %+v", n)
	}
	if alpha.Next != nil {
		t.Errorf("Alpha has no coming release: %+v", alpha.Next)
	}

	var eps EpisodeList
	e.call("GET", APIBase+"/titles/10/episodes", nil, 200, &eps)
	for _, ep := range eps.Episodes {
		a := ep.Versions["tv-2160p"].Acquisition
		switch ep.ID {
		case 102:
			if a == nil || a.Stage != "downloading" || a.Progress == nil || *a.Progress != 37 || !a.Upgrade {
				t.Errorf("S01E02: %+v", a)
			}
		case 101:
			if a == nil || a.Stage != "waiting_mount" {
				t.Errorf("S01E01: %+v", a)
			}
		default:
			if a != nil {
				t.Errorf("episode %d: %+v", ep.ID, a)
			}
		}
	}
	for _, s := range eps.Seasons {
		sc := s.Versions["tv-2160p"]
		if s.Season == 1 && (sc.InFlight != 2 || sc.Stages["downloading"] != 1 || sc.Stages["waiting_mount"] != 1) {
			t.Errorf("season 1 in flight: %+v", sc)
		}
		if s.Season != 1 && sc.InFlight != 0 {
			t.Errorf("season %d in flight: %+v", s.Season, sc)
		}
	}
	// One season's grid still counts its own season only.
	e.call("GET", APIBase+"/titles/10/episodes?season=2", nil, 200, &eps)
	if len(eps.Episodes) != 1 || eps.Episodes[0].Versions["tv-2160p"].Acquisition != nil {
		t.Errorf("season 2: %+v", eps.Episodes)
	}

	var d TitleDetail
	e.call("GET", APIBase+"/titles/1", nil, 200, &d)
	for _, v := range d.Versions {
		if (v.Key == "movie-remux") != (v.Acquisition != nil) {
			t.Errorf("title page %s: %+v", v.Key, v.Acquisition)
		}
	}

	// Users: no ownership or downloads; coverage as watchability only.
	mux := e.userMux()
	for _, p := range []string{"/titles", "/titles/10/episodes", "/titles/1"} {
		r := httptest.NewRequest("GET", APIBase+p, nil)
		r.Header.Set(auth.APIKeyHeader, e.key)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		body := rec.Body.String()
		for _, s := range []string{`"acquisition"`, `"control"`, `"in_flight"`, `"stages"`, "sonarr_4k_debrid", `"missing":`} {
			if p != "/titles" && (s == `"missing":` || s == `"control"`) {
				continue // the season counts keep their missing count; controls there are redacted, not left out
			}
			if strings.Contains(body, s) {
				t.Errorf("%s shows %s to a user", p, s)
			}
		}
	}
}

// TestCopySorts: completion, quality, activity and next order titles by their copies; sort_version narrows them
// to one copy; the file sorts need see files.
func TestCopySorts(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	seedCopies(e)
	order := func(q string) string {
		t.Helper()
		var p TitlePage
		e.call("GET", APIBase+"/titles?"+q, nil, 200, &p)
		var names []string
		for _, r := range p.Items {
			names = append(names, r.Name)
		}
		return strings.Join(names, ",")
	}
	e.exec(`UPDATE episode SET air_date = '2020-01-01' WHERE id = 103`)
	airAtMidnight(e.exec)
	// Alpha's movie-remux has its file (1.0); The Show holds two of three aired episodes.
	if got := order("sort=completion"); got != "Alpha,The Show" {
		t.Errorf("completion: %s", got)
	}
	if got := order("sort=completion&order=asc"); got != "The Show,Alpha" {
		t.Errorf("completion asc: %s", got)
	}
	// The same with air dates alone (no instants): the dated episode not held still counts as aired.
	e.exec(`UPDATE episode SET air_date_utc_at = NULL`)
	if got := order("sort=completion&order=asc"); got != "The Show,Alpha" {
		t.Errorf("completion asc, dates only: %s", got)
	}
	airAtMidnight(e.exec)
	// movie-web has no file (0.0); The Show has no movie-web copy, so it comes last either way.
	if got := order("sort=completion&sort_version=movie-web&order=asc"); got != "Alpha,The Show" {
		t.Errorf("completion of movie-web: %s", got)
	}
	// WEBDL-2160p over WEBDL-1080p.
	if got := order("sort=quality"); got != "Alpha,The Show" {
		t.Errorf("quality: %s", got)
	}
	if got := order("sort=quality&order=asc"); got != "The Show,Alpha" {
		t.Errorf("quality asc: %s", got)
	}
	if got := order("sort=quality&sort_version=tv-2160p&order=desc"); got != "The Show,Alpha" {
		t.Errorf("quality of tv-2160p (Alpha has none): %s", got)
	}
	// The Show's waiting download changed last; the imported Alpha download is not in hand.
	if got := order("sort=activity"); got != "The Show,Alpha" {
		t.Errorf("activity: %s", got)
	}
	if got := order("sort=activity&sort_version=movie-remux"); got != "Alpha,The Show" {
		t.Errorf("activity of movie-remux: %s", got)
	}
	e.exec(`UPDATE episode SET air_date = '2026-11-02' WHERE id = 103`)
	airAtMidnight(e.exec)
	e.exec(`INSERT INTO title_release_date (title_id, source, type, date) VALUES (1, 'tmdb', 'digital', '2026-12-01')`)
	if got := order("sort=next"); got != "The Show,Alpha" {
		t.Errorf("next: %s", got)
	}
	var p TitlePage
	e.call("GET", APIBase+"/titles", nil, 200, &p)
	if n := rowOf(t, p, 1).Next; n == nil || n.Date != "2026-12-01" || n.Type != "digital" {
		t.Errorf("Alpha's coming release: %+v", n)
	}
	e.call("GET", APIBase+"/titles?sort=quality&sort_version=Bad%20Key", nil, 400, nil)
	mux := e.userMux()
	for _, q := range []string{"sort=quality", "sort=activity", "sort=completion", "sort=next"} {
		r := httptest.NewRequest("GET", APIBase+"/titles?"+q, nil)
		r.Header.Set(auth.APIKeyHeader, e.key)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		want := 200
		if q == "sort=quality" || q == "sort=activity" {
			want = 403
		}
		if rec.Code != want {
			t.Errorf("user %s: %d, want %d", q, rec.Code, want)
		}
	}
}

// TestBulkMonitor: the copy-targeted bulk monitor previews and applies per item, a copy an arr imports included; a
// copy the title lacks is never added, items changed since the preview are stale, and nothing searches.
func TestBulkMonitor(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	seedCopies(e)
	f := false
	before := e.state()
	var pre BulkMonitorResult
	e.call("POST", APIBase+"/titles/monitor", BulkMonitorRequest{TitleIDs: []int64{10, 1, 999}, Monitored: &f,
		DryRun: true}, 200, &pre)
	if e.state() != before {
		t.Fatal("a preview wrote")
	}
	got := map[string]string{}
	for _, it := range pre.Items {
		got[it.Version+"@"+it.Title] = it.Outcome
	}
	want := map[string]string{"tv-2160p@The Show": "changed", "movie-remux@Alpha": "changed",
		"movie-web@Alpha": "changed", "@": "missing"}
	if len(got) != len(want) {
		t.Fatalf("preview items: %+v", pre.Items)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("preview %s: %q, want %q", k, got[k], v)
		}
	}
	if pre.Items[0].TitleID != 10 || pre.Changed != 3 || pre.Missing != 1 ||
		!strings.HasPrefix(pre.Message, "Would unmonitor 3 libraries") {
		t.Errorf("preview: %+v", pre)
	}

	// movie-web changes after the preview: the apply refuses it as stale and changes movie-remux and tv-2160p.
	e.exec(`UPDATE version SET monitored = 0 WHERE id = 2`)
	var expect []BulkExpect
	for _, it := range pre.Items {
		if it.From != nil {
			expect = append(expect, BulkExpect{TitleID: it.TitleID, Version: it.Version, Monitored: *it.From})
		}
	}
	var out BulkMonitorResult
	e.call("POST", APIBase+"/titles/monitor", BulkMonitorRequest{TitleIDs: []int64{10, 1}, Monitored: &f,
		Expect: expect}, 200, &out)
	if out.DryRun || out.Changed != 2 || out.Stale != 1 {
		t.Errorf("apply: %+v", out)
	}
	var remux, web, show, alpha int
	e.st.Reader().QueryRow(`SELECT (SELECT monitored FROM version WHERE id = 1), (SELECT monitored FROM version WHERE id = 2),
		(SELECT monitored FROM version WHERE id = 10), (SELECT monitored FROM title WHERE id = 1)`).Scan(&remux, &web, &show, &alpha)
	if remux != 0 || web != 0 || show != 0 || alpha != 0 {
		t.Errorf("after apply: remux %d web %d show %d title %d", remux, web, show, alpha)
	}

	// A copy the title lacks is reported, never added.
	tr := true
	e.call("POST", APIBase+"/titles/monitor", BulkMonitorRequest{TitleIDs: []int64{10}, Versions: []string{"anime-tv"},
		Monitored: &tr}, 200, &out)
	var n int
	e.st.Reader().QueryRow(`SELECT COUNT(*) FROM version WHERE title_id = 10`).Scan(&n)
	if out.Missing != 1 || n != 1 {
		t.Errorf("missing copy: %+v (versions %d)", out, n)
	}
	e.call("POST", APIBase+"/titles/monitor", BulkMonitorRequest{TitleIDs: []int64{1}, Versions: []string{"movie-remux"},
		Monitored: &tr}, 200, &out)
	if out.Changed != 1 || out.Message != "Monitored 1 library" {
		t.Errorf("monitor one: %+v", out)
	}

	for _, body := range []any{
		map[string]any{"title_ids": []int64{1}},
		map[string]any{"title_ids": []int64{}, "monitored": true},
		map[string]any{"title_ids": []int64{1}, "versions": []string{}, "monitored": true},
		map[string]any{"title_ids": []int64{1}, "versions": []string{"nope"}, "monitored": true},
	} {
		e.call("POST", APIBase+"/titles/monitor", body, 400, nil)
	}
	if s, i := e.acq.counts(); s != 0 || i != 0 {
		t.Errorf("a bulk monitor searched: %d %d", s, i)
	}

	// Without manage: 403, nothing written.
	before = e.state()
	b, _ := json.Marshal(BulkMonitorRequest{TitleIDs: []int64{1}, Monitored: &f})
	r := httptest.NewRequest("POST", APIBase+"/titles/monitor", bytes.NewReader(b))
	r.Header.Set(auth.APIKeyHeader, e.key)
	rec := httptest.NewRecorder()
	e.userMux().ServeHTTP(rec, r)
	if rec.Code != 403 || e.state() != before {
		body, _ := io.ReadAll(rec.Body)
		t.Errorf("user bulk monitor: %d %s", rec.Code, body)
	}
}

// TestListRowFacts: a movie row carries its home release (availability.HomeRelease over the canonical dates, the
// TMDb refresh over imports) and acquiring, a download in hand that is neither blocked nor an upgrade; a user's row
// keeps acquiring without any acquisition detail.
func TestListRowFacts(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	seedCopies(e)
	now := store.Millis(addNow)
	e.exec(`INSERT INTO title (id, kind, name, year, tmdb_id, created_at, updated_at) VALUES
		(30, 'movie', 'Cinema', 2026, 530, ?, ?), (31, 'movie', 'Disc', 2026, 531, ?, ?),
		(32, 'movie', 'Recent', 2026, 532, ?, ?), (33, 'movie', 'Old', 2026, 533, ?, ?),
		(34, 'movie', 'Undated', 2026, 534, ?, ?)`, now, now, now, now, now, now, now, now, now, now)
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
		(30, 30, 'movie-web', 1, 'wanted', ?, ?), (31, 31, 'movie-web', 1, 'wanted', ?, ?),
		(32, 32, 'movie-web', 1, 'wanted', ?, ?), (33, 33, 'movie-web', 1, 'wanted', ?, ?),
		(34, 34, 'movie-web', 1, 'wanted', ?, ?)`, now, now, now, now, now, now, now, now, now, now)
	// Cinema has a TMDb refresh, so its imported digital date is not canonical; Recent opened in cinemas 30 days
	// ago and Old 100 days ago, neither with a home date.
	e.exec(`INSERT INTO title_meta (title_id, movie_dates_at, attempted_at, refresh_after) VALUES (30, 1, 1, 1)`)
	e.exec(`INSERT INTO title_release_date (title_id, source, type, date) VALUES
		(30, 'tmdb-art', 'theatrical', '2026-10-01'), (30, 'tmdb-art', 'digital', '2026-11-20'),
		(30, 'radarr_4k_debrid', 'digital', '2026-10-05'),
		(31, 'tmdb', 'theatrical', '2026-08-01'), (31, 'tmdb', 'digital', '2026-10-01'), (31, 'tmdb', 'physical', '2026-12-01'),
		(32, 'tmdb', 'theatrical', '2026-09-26'), (33, 'tmdb', 'theatrical', '2026-07-18')`)
	// Cinema downloading; Disc's download is blocked at import.
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		episodes, progress, state, created_at, updated_at) VALUES
		(30, 30, 30, 'movie-web', 'e0', 'Cinema.2026.WEB', 'dmm', 'WEBDL-1080p', '[]', 12, 'downloading', ?, ?),
		(31, 31, 31, 'movie-web', 'e1', 'Disc.2026.WEB', 'dmm', 'WEBDL-1080p', '[]', 100, 'ready', ?, ?)`, now, now, now, now)
	e.exec(`INSERT INTO library_import (download_id, state, created_at, updated_at) VALUES (31, 'failed', ?, ?)`, now, now)
	// Facts: Cinema is in cinemas, in English; Recent has a link in the library; Disc has an explicit availability choice
	// on its library's own view; The Show is a returning series.
	e.exec(`UPDATE title_meta SET status = 'inCinemas', original_language = 'en' WHERE title_id = 30`)
	e.exec(`INSERT INTO title_meta (title_id, status, attempted_at, refresh_after) VALUES (10, 'Returning Series', 1, 1)`)
	e.exec(`INSERT INTO arrview_title (view, title_id, minimum_availability, added_at, updated_at) VALUES
		('v/movie-web', 31, 'announced', 1, 1)`)
	e.exec(`INSERT INTO link (id, path, target, title_id, quality, state, created_at, updated_at) VALUES
		(30, '/media/movies-web/Recent (2026)/Recent.2026.mkv', '/zurg/__all__/Recent/r.mkv', 32, 'WEBDL-1080p', 'active', ?, ?)`,
		now, now)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (30, 32)`)

	var page TitlePage
	e.call("GET", APIBase+"/titles?limit=100", nil, 200, &page)
	cin, disc, show := rowOf(t, page, 30), rowOf(t, page, 31), rowOf(t, page, 10)
	if cin.TitleAddedAt == nil || !cin.TitleAddedAt.Equal(store.FromMillis(now)) || cin.Status != "inCinemas" ||
		cin.OriginalLanguage != "en" || cin.InCinemas != "2026-10-01" || cin.DigitalRelease != "2026-11-20" ||
		cin.PhysicalRelease != "" || cin.SeriesType != "" || cin.SeasonCount != 0 {
		t.Errorf("Cinema facts: %+v", cin)
	}
	if disc.InCinemas != "2026-08-01" || disc.DigitalRelease != "2026-10-01" || disc.PhysicalRelease != "2026-12-01" {
		t.Errorf("Disc dates: %+v", disc)
	}
	// movie-web has no minimum_availability in the example settings: released; Disc's own choice wins.
	if v := copyOf(t, cin, "movie-web"); v.Profile != "web-2160p" || v.MinimumAvailability != "released" || v.Folder != "" {
		t.Errorf("Cinema copy: %+v", v)
	}
	if v := copyOf(t, rowOf(t, page, 32), "movie-web"); v.Folder != "/media/movies-web/Recent (2026)" {
		t.Errorf("Recent copy: %+v", v)
	}
	if v := copyOf(t, disc, "movie-web"); v.MinimumAvailability != "announced" || v.Folder != "" {
		t.Errorf("Disc copy: %+v", v)
	}
	// Seasons 1 and 2 (the special left out); S02E01 is still to come.
	if show.SeriesType != "standard" || show.SeasonCount != 2 || show.PreviousAiring != "2020-01-01" ||
		show.Status != "Returning Series" || show.InCinemas != "" {
		t.Errorf("The Show facts: %+v", show)
	}
	if v := copyOf(t, show, "tv-2160p"); v.Profile != "tv-2160p" || v.MinimumAvailability != "" {
		t.Errorf("The Show copy: %+v", v)
	}
	for id, want := range map[int64]string{30: "2026-11-20", 31: "2026-10-01", 32: "2026-12-25", 33: "2026-10-16", 34: "", 10: ""} {
		r := rowOf(t, page, id)
		if r.HomeRelease != want {
			t.Errorf("%s home_release %q, want %q", r.Name, r.HomeRelease, want)
		}
		// HomeRelease agrees with Available's released minimum.
		dates, err := availability.Dates(context.Background(), e.st.Reader(), id)
		if err != nil {
			t.Fatal(err)
		}
		out := r.HomeRelease != "" && r.HomeRelease <= addNow.Format(time.DateOnly)
		if r.Kind == "movie" && availability.Available("released", dates, addNow, 0) != out {
			t.Errorf("%s: home_release %q disagrees with Available", r.Name, r.HomeRelease)
		}
	}
	// Alpha's and The Show's downloads are upgrades; Disc's is blocked.
	for id, want := range map[int64]bool{30: true, 31: false, 1: false, 10: false, 34: false} {
		if r := rowOf(t, page, id); r.Acquiring != want {
			t.Errorf("%s acquiring %v, want %v", r.Name, r.Acquiring, want)
		}
	}

	r := httptest.NewRequest("GET", APIBase+"/titles?limit=100", nil)
	r.Header.Set(auth.APIKeyHeader, e.key)
	rec := httptest.NewRecorder()
	e.userMux().ServeHTTP(rec, r)
	var up TitlePage
	if err := json.Unmarshal(rec.Body.Bytes(), &up); err != nil {
		t.Fatal(err)
	}
	cinema := rowOf(t, up, 30)
	if !cinema.Acquiring || cinema.HomeRelease != "2026-11-20" || cinema.SizeBytes != nil {
		t.Errorf("user's Cinema row: %+v", cinema)
	}
	if cinema.Status != "inCinemas" || cinema.TitleAddedAt == nil || cinema.InCinemas != "2026-10-01" {
		t.Errorf("user's Cinema row lost its title facts: %+v", cinema)
	}
	for _, v := range append(cinema.Versions, rowOf(t, up, 32).Versions...) {
		if v.Acquisition != nil || v.SizeBytes != nil || v.Providers != nil || v.Profile != "" ||
			v.MinimumAvailability != "" || v.Folder != "" {
			t.Errorf("user's Cinema copy %s: %+v", v.Key, v)
		}
	}
	if rowOf(t, up, 31).Acquiring {
		t.Error("user's Disc row: a blocked download is not acquiring")
	}

	// The title page says the same, to file viewers and users alike; Undated's only download
	// failed, and a failed download is not in hand.
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		episodes, state, error, created_at, updated_at) VALUES
		(34, 34, 34, 'movie-web', 'e4', 'Undated.2026.WEB', 'dmm', 'WEBDL-1080p', '[]', 'failed', 'stalled', ?, ?)`, now, now)
	for id, want := range map[int64]bool{30: true, 31: false, 1: false, 10: false, 34: false} {
		var d TitleDetail
		e.call("GET", fmt.Sprintf("%s/titles/%d", APIBase, id), nil, 200, &d)
		r := httptest.NewRequest("GET", fmt.Sprintf("%s/titles/%d", APIBase, id), nil)
		r.Header.Set(auth.APIKeyHeader, e.key)
		rec := httptest.NewRecorder()
		e.userMux().ServeHTTP(rec, r)
		var u TitleDetail
		if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
			t.Fatal(err)
		}
		if d.Acquiring != want || u.Acquiring != want {
			t.Errorf("%s title page acquiring %v (user %v), want %v", d.Name, d.Acquiring, u.Acquiring, want)
		}
		for _, v := range u.Versions {
			if v.Acquisition != nil {
				t.Errorf("user's %s title page copy %s has acquisition detail", u.Name, v.Key)
			}
		}
	}
}

// asUser sends a request as a caller without manage.
func (e *manageEnv) asUser(method, path string, body any) int {
	e.t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.Header.Set(auth.APIKeyHeader, e.key)
	rec := httptest.NewRecorder()
	e.userMux().ServeHTTP(rec, r)
	return rec.Code
}

// TestMinimumAvailabilityEndpoint: POST /titles/minimum-availability writes an explicit choice on the own view of
// each named library the movie is in, which the title page then reads as the value in force; a series, a library
// the movie lacks, a value outside the list and a caller without manage change nothing.
func TestMinimumAvailabilityEndpoint(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, func(p *policy.Policy) { p.Versions["movie-web"].MinimumAvailability = "inCinemas" })
	seedCopies(e)
	const path = APIBase + "/titles/minimum-availability"
	shown := func() string {
		var d TitleDetail
		e.call("GET", APIBase+"/titles/1", nil, 200, &d)
		var out []string
		for _, v := range d.Versions {
			out = append(out, v.Key+"="+v.MinimumAvailability)
		}
		return strings.Join(out, " ")
	}
	stored := func() string {
		var out []string
		if err := queryEach(context.Background(), e.st.Reader(), `SELECT view || '/' || title_id || '=' || minimum_availability
			FROM arrview_title ORDER BY view, title_id`, nil, func(r *sql.Rows) error {
			out = append(out, "")
			return r.Scan(&out[len(out)-1])
		}); err != nil {
			t.Fatal(err)
		}
		return strings.Join(out, " ")
	}
	// Nothing chosen: released, or the library's own value.
	if got := shown(); got != "movie-remux=released movie-web=inCinemas" || stored() != "" {
		t.Fatalf("before: %s (%s)", got, stored())
	}
	var series TitleDetail
	e.call("GET", APIBase+"/titles/10", nil, 200, &series)
	if series.SeriesType != "standard" || series.Versions[0].MinimumAvailability != "" {
		t.Errorf("a series: type %q, minimum availability %q", series.SeriesType, series.Versions[0].MinimumAvailability)
	}

	// Refused, nothing written: a series (alone or among movies), an unknown title, a bad value, bad libraries, no
	// or too many titles.
	many := make([]int64, maxBulkTitles+1)
	for i := range many {
		many[i] = int64(i + 1)
	}
	for _, c := range []struct {
		body map[string]any
		want int
	}{
		{map[string]any{"title_ids": []int64{10}, "minimum_availability": "announced"}, 400},
		{map[string]any{"title_ids": []int64{1, 10}, "minimum_availability": "announced"}, 400},
		{map[string]any{"title_ids": []int64{1, 999}, "minimum_availability": "announced"}, 404},
		{map[string]any{"title_ids": []int64{1}, "minimum_availability": "tba"}, 400},
		{map[string]any{"title_ids": []int64{1}}, 400},
		{map[string]any{"title_ids": []int64{1}, "versions": []string{}, "minimum_availability": "announced"}, 400},
		{map[string]any{"title_ids": []int64{1}, "versions": []string{"nope"}, "minimum_availability": "announced"}, 400},
		{map[string]any{"title_ids": []int64{1}, "versions": []string{"tv-2160p"}, "minimum_availability": "announced"}, 400},
		{map[string]any{"title_ids": []int64{}, "minimum_availability": "announced"}, 400},
		{map[string]any{"title_ids": many, "minimum_availability": "announced"}, 400},
	} {
		e.call("POST", path, c.body, c.want, nil)
	}
	if code := e.asUser("POST", path, map[string]any{"title_ids": []int64{1}, "minimum_availability": "announced"}); code != 403 {
		t.Errorf("a caller without manage: %d", code)
	}
	if got := stored(); got != "" {
		t.Fatalf("a refused request wrote: %s", got)
	}

	// One library: its own row only. A library the movie is not in gets none.
	var out TitleChoiceResult
	e.call("POST", path, map[string]any{"title_ids": []int64{1}, "versions": []string{"movie-web", "anime-movie"},
		"minimum_availability": "announced"}, 200, &out)
	if got := stored(); out.Changed != 1 || got != "v/movie-web/1=announced" {
		t.Errorf("one library: changed %d, %s", out.Changed, got)
	}
	if got := shown(); got != "movie-remux=released movie-web=announced" {
		t.Errorf("after one library: %s", got)
	}
	// No libraries named: every library the movie is in. Released is a choice like the others.
	e.call("POST", path, map[string]any{"title_ids": []int64{1}, "minimum_availability": "released"}, 200, &out)
	if got := stored(); out.Changed != 2 || got != "v/movie-remux/1=released v/movie-web/1=released" {
		t.Errorf("every library: changed %d, %s", out.Changed, got)
	}
	if got := shown(); got != "movie-remux=released movie-web=released" {
		t.Errorf("after every library: %s", got)
	}
	// A copy removed from the library takes no choice.
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (2, 'ui', 0)`)
	e.call("POST", path, map[string]any{"title_ids": []int64{1}, "minimum_availability": "inCinemas"}, 200, &out)
	if got := stored(); out.Changed != 1 || got != "v/movie-remux/1=inCinemas v/movie-web/1=released" {
		t.Errorf("a removed copy: changed %d, %s", out.Changed, got)
	}
}

// TestSeriesTypeEndpoint: POST /titles/series-type edits the series' one type and leaves the anime flag; a movie, a
// type outside the list and a caller without manage change nothing.
func TestSeriesTypeEndpoint(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	const path = APIBase + "/titles/series-type"
	typ := func() string {
		var s string
		if err := e.st.Reader().QueryRow(`SELECT COALESCE(series_type, '') || '/' || anime FROM title WHERE id = 10`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := typ()
	many := make([]int64, maxBulkTitles+1)
	for i := range many {
		many[i] = int64(i + 1)
	}
	for _, c := range []struct {
		body map[string]any
		want int
	}{
		{map[string]any{"title_ids": []int64{1}, "series_type": "daily"}, 400},
		{map[string]any{"title_ids": []int64{10, 1}, "series_type": "daily"}, 400},
		{map[string]any{"title_ids": []int64{10, 999}, "series_type": "daily"}, 404},
		{map[string]any{"title_ids": []int64{10}, "series_type": "weekly"}, 400},
		{map[string]any{"title_ids": []int64{10}}, 400},
		{map[string]any{"title_ids": []int64{}, "series_type": "daily"}, 400},
		{map[string]any{"title_ids": many, "series_type": "daily"}, 400},
	} {
		e.call("POST", path, c.body, c.want, nil)
	}
	if code := e.asUser("POST", path, map[string]any{"title_ids": []int64{10}, "series_type": "daily"}); code != 403 {
		t.Errorf("a caller without manage: %d", code)
	}
	if got := typ(); got != before {
		t.Fatalf("a refused request changed the type: %s, was %s", got, before)
	}
	var out TitleChoiceResult
	e.call("POST", path, map[string]any{"title_ids": []int64{10}, "series_type": "daily"}, 200, &out)
	if got := typ(); out.Changed != 1 || got != "daily/0" {
		t.Errorf("daily: changed %d, %s", out.Changed, got)
	}
	var d TitleDetail
	e.call("GET", APIBase+"/titles/10", nil, 200, &d)
	if d.SeriesType != "daily" {
		t.Errorf("the title page reads %q", d.SeriesType)
	}
	var movie TitleDetail
	e.call("GET", APIBase+"/titles/1", nil, 200, &movie)
	if movie.SeriesType != "" {
		t.Errorf("a movie has series type %q", movie.SeriesType)
	}
}
