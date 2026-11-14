package web

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/activity"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func TestCalendar(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	now := store.Millis(testNow)
	// Alpha: the arr's digital date in the window, TMDb's US one ignored (the arr's wins); Beta: only TMDb US dates.
	e.exec(`INSERT INTO title_release_date (title_id, source, country, type, date) VALUES
		(1, 'radarr_4k_debrid', '', 'digital', '2026-11-03'), (1, 'tmdb', 'US', 'digital', '2026-11-01'),
		(1, 'radarr_4k_debrid', '', 'theatrical', '2026-01-01'),
		(2, 'tmdb', 'US', 'physical', '2026-11-10'), (2, 'tmdb', 'FR', 'physical', '2026-11-02')`)
	// Gamma (series): two episodes in the window, one unmonitored.
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, air_date, air_date_utc_at, monitored, created_at,
		updated_at) VALUES (1, 3, 1, 1, 'One', '2026-11-04', ?, 1, ?, ?), (2, 3, 1, 2, 'Two', '2026-11-11', NULL, 0, ?, ?)`,
		store.Millis(time.Date(2026, 11, 5, 2, 0, 0, 0, time.UTC)), now, now, now, now)
	e.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES
		(4, 3, 'tv-2160p', 'wanted', ?, ?)`, now, now)
	e.exec(`INSERT INTO series_meta (title_id, source, air_time, time_zone) VALUES (3, 'tvdb', '21:00', 'America/New_York')`)

	var cal CalendarPage
	if code := e.getKey(APIBase+"/calendar?from=2026-11-01&to=2026-11-30", key, &cal); code != 200 {
		t.Fatalf("calendar: %d", code)
	}
	var got []string
	for _, it := range cal.Items {
		got = append(got, it.Date+" "+it.Type+" "+it.Title.Name)
	}
	want := []string{"2026-11-03 digital Alpha", "2026-11-04 episode Gamma", "2026-11-10 physical Beta"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("calendar:\n got %v\nwant %v", got, want)
	}
	if ep := cal.Items[1]; ep.Season == nil || *ep.Season != 1 || ep.Episode != "One" || ep.At == nil ||
		!strings.HasPrefix(ep.Title.Poster, "/img/t/3/poster/") || ep.Title.Kind != "series" {
		t.Errorf("episode %+v", ep)
	}
	if !cal.Items[0].Available || cal.Items[2].Available != true {
		t.Errorf("available: %+v", cal.Items)
	}
	// Alpha's movie-web copy has no file yet: partly available, as the lists say it; Beta's only copy has its file.
	if !cal.Items[0].Partial || cal.Items[2].Partial {
		t.Errorf("partly available: %+v", cal.Items)
	}
	// A household account gets watchability only: no partly available.
	ur := httptest.NewRequest("GET", APIBase+"/calendar?from=2026-11-01&to=2026-11-30", nil)
	ur.Header.Set(auth.APIKeyHeader, key)
	urec := httptest.NewRecorder()
	e.userMux().ServeHTTP(urec, ur)
	if urec.Code != 200 || strings.Contains(urec.Body.String(), `"partial"`) || !strings.Contains(urec.Body.String(), `"available":true`) {
		t.Errorf("household calendar: %d %s", urec.Code, urec.Body.String())
	}
	e.getKey(APIBase+"/calendar?from=2026-11-01&to=2026-11-30&unmonitored=true", key, &cal)
	if len(cal.Items) != 4 {
		t.Errorf("with unmonitored: %d items", len(cal.Items))
	}
	// A movie is monitored when a library monitors it, as the monitored-only list counts it: Beta's one library does not.
	e.exec(`UPDATE version SET monitored = 0 WHERE id = 3`)
	cal = CalendarPage{}
	e.getKey(APIBase+"/calendar?from=2026-11-01&to=2026-11-30&unmonitored=true", key, &cal)
	movieMonitored := func() map[string]bool {
		cal = CalendarPage{}
		e.getKey(APIBase+"/calendar?from=2026-11-01&to=2026-11-30&unmonitored=true", key, &cal)
		got := map[string]bool{}
		for _, it := range cal.Items {
			if it.Kind == "movie" {
				got[it.Title.Name] = it.Monitored
			}
		}
		return got
	}
	if got := movieMonitored(); !maps.Equal(got, map[string]bool{"Alpha": true, "Beta": false}) {
		t.Errorf("monitored movies: %v", got)
	}
	e.exec(`UPDATE version SET monitored = 1 WHERE id = 3`)
	// A removed library's monitoring does not count; a title removed from every library is not listed, even with
	// unmonitored. Alpha keeps its unmonitored movie-web copy; Beta's only copy is removed.
	e.exec(`UPDATE version SET monitored = 0 WHERE id = 2`)
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (1, 'radarr', 0), (3, 'radarr', 0)`)
	if got := movieMonitored(); !maps.Equal(got, map[string]bool{"Alpha": false}) {
		t.Errorf("monitored movies after removal: %v", got)
	}
	// The monitored-only calendar follows the same rule: Alpha's only monitored copy was removed.
	cal = CalendarPage{}
	e.getKey(APIBase+"/calendar?from=2026-11-01&to=2026-11-30", key, &cal)
	for _, it := range cal.Items {
		if it.Kind == "movie" {
			t.Errorf("monitored-only calendar lists %s", it.Title.Name)
		}
	}
	e.exec(`DELETE FROM arrview_removed_version`)
	e.exec(`UPDATE version SET monitored = 1 WHERE id = 2`)
	// An imported series: no air time in series_meta (or no row at all), yet its episode has a real evening instant.
	// The entry keeps the moment.
	evening := time.Date(2026, 11, 5, 2, 0, 0, 0, time.UTC)
	for _, q := range []string{`UPDATE series_meta SET air_time = '' WHERE title_id = 3`, `DELETE FROM series_meta WHERE title_id = 3`} {
		e.exec(q)
		cal = CalendarPage{}
		e.getKey(APIBase+"/calendar?from=2026-11-01&to=2026-11-30", key, &cal)
		if len(cal.Items) != 3 || cal.Items[1].Date != "2026-11-04" || cal.Items[1].At == nil || !cal.Items[1].At.Equal(evening) {
			t.Errorf("imported air time (%s): %+v", q, cal.Items)
		}
	}
	// An instant at midnight of the air date in the series' zone is what the refresh writes for an unknown air time
	// (here New York, then UTC for a series without a zone): the entry carries the day only. So does a second episode
	// of that day, which the refresh puts a runtime after the first.
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, air_date, air_date_utc_at, monitored, created_at,
		updated_at) VALUES (3, 3, 1, 3, 'Three', '2026-11-04', ?, 1, ?, ?)`,
		store.Millis(time.Date(2026, 11, 4, 5, 45, 0, 0, time.UTC)), now, now)
	for _, c := range []struct {
		zone string
		at   time.Time
	}{{"America/New_York", time.Date(2026, 11, 4, 5, 0, 0, 0, time.UTC)}, {"", time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)}} {
		e.exec(`DELETE FROM series_meta WHERE title_id = 3`)
		if c.zone != "" {
			e.exec(`INSERT INTO series_meta (title_id, source, time_zone) VALUES (3, 'tmdb', ?)`, c.zone)
		}
		e.exec(`UPDATE episode SET air_date_utc_at = ? WHERE id = 1`, store.Millis(c.at))
		e.exec(`UPDATE episode SET air_date_utc_at = ? WHERE id = 3`, store.Millis(c.at.Add(45*time.Minute)))
		cal = CalendarPage{}
		e.getKey(APIBase+"/calendar?from=2026-11-01&to=2026-11-30", key, &cal)
		if len(cal.Items) != 4 || cal.Items[1].Date != "2026-11-04" || cal.Items[1].At != nil || cal.Items[2].At != nil {
			t.Errorf("unknown air time (zone %q): %+v", c.zone, cal.Items)
		}
	}
	e.exec(`DELETE FROM episode WHERE id = 3`)
	e.getKey(APIBase+"/calendar?from=2026-11-01&to=2026-11-30&kind=movie", key, &cal)
	if len(cal.Items) != 2 {
		t.Errorf("movies only: %d items", len(cal.Items))
	}
	// Downloads in hand: Beta's movie, and Gamma's episode 1 (not episode 2). One finished download does not count.
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		state, episodes, created_at, updated_at) VALUES
		(90, 2, 3, 'movie-web', ?, 'Beta.2021.2160p.WEB-DL', 'dmm', 'WEBDL-2160p', 'downloading', '[]', ?, ?),
		(91, 3, 4, 'tv-2160p', ?, 'Gamma.S01E01.2160p.WEB-DL', 'dmm', 'WEBDL-2160p', 'queued', '[1]', ?, ?),
		(92, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB-DL', 'dmm', 'WEBDL-2160p', 'imported', '[]', ?, ?)`,
		strings.Repeat("9", 40), now, now, strings.Repeat("a", 40), now, now, strings.Repeat("b", 40), now, now)
	cal = CalendarPage{}
	e.getKey(APIBase+"/calendar?from=2026-11-01&to=2026-11-30&unmonitored=true", key, &cal)
	acq := map[string]bool{}
	for _, it := range cal.Items {
		acq[fmt.Sprintf("%s %d", it.Title.Name, it.EpisodeID)] = it.Acquiring
	}
	if want := map[string]bool{"Alpha 0": false, "Beta 0": true, "Gamma 1": true, "Gamma 2": false}; !maps.Equal(acq, want) {
		t.Errorf("acquiring: got %v, want %v", acq, want)
	}
	// Each entry in hand names its stage (the calendar says it, not "Downloading" for everything).
	stage := map[string]string{}
	for _, it := range cal.Items {
		stage[fmt.Sprintf("%s %d", it.Title.Name, it.EpisodeID)] = it.Stage
	}
	if want := map[string]string{"Alpha 0": "", "Beta 0": "downloading", "Gamma 1": "queued", "Gamma 2": ""}; !maps.Equal(stage, want) {
		t.Errorf("stages: got %v, want %v", stage, want)
	}
	// A copy removed from its library does not make the date available: Alpha's only file is in movie-remux.
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (1, 'radarr_4k_debrid', ?)`, now)
	cal = CalendarPage{}
	e.getKey(APIBase+"/calendar?from=2026-11-01&to=2026-11-30&kind=movie", key, &cal)
	if len(cal.Items) != 2 || cal.Items[0].Title.Name != "Alpha" || cal.Items[0].Available || !cal.Items[1].Available {
		t.Errorf("removed copy counted: %+v", cal.Items)
	}
	// Gamma's episode 1 is monitored and held only by a removed copy; the remaining copy does not monitor it: the
	// entry is not monitored (not missing). Once the remaining copy monitors it, it is.
	e.exec(`INSERT INTO version (id, title_id, policy_key, state, monitored, created_at, updated_at) VALUES
		(5, 3, 'tv-web', 'filled', 1, ?, ?)`, now, now)
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (5, 'sonarr_web', ?)`, now)
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at) VALUES
		(5, 1, 1, 'filled', ?), (4, 1, 0, 'wanted', ?)`, now, now)
	gamma := func() CalendarEntry {
		cal = CalendarPage{}
		e.getKey(APIBase+"/calendar?from=2026-11-01&to=2026-11-30&kind=series", key, &cal)
		if len(cal.Items) != 1 {
			t.Fatalf("series: %+v", cal.Items)
		}
		return cal.Items[0]
	}
	if g := gamma(); g.Monitored || g.Available {
		t.Errorf("removed copy's monitoring counted: %+v", g)
	}
	e.exec(`UPDATE episode_version_state SET monitored = 1 WHERE version_id = 4`)
	if g := gamma(); !g.Monitored || g.Available {
		t.Errorf("remaining copy's monitoring: %+v", g)
	}
	for _, bad := range []string{"from=2026-13-01", "from=2026-11-01&to=2026-10-01", "from=2026-01-01&to=2026-12-31",
		"kind=music"} {
		if code := e.getKey(APIBase+"/calendar?"+bad, key, nil); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
}

func TestActivityBlockedAndParams(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	now := store.Millis(testNow)
	e.exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, release_title, reason, source, created_at)
		VALUES ('movie-web', ?, 1, 'Alpha.Sample.Release', 'sample guard', 'library', ?)`, hashA, now)
	var p ActivityPage
	if code := e.getKey(APIBase+"/activity?state=blocked", key, &p); code != 200 {
		t.Fatalf("blocked: %d", code)
	}
	if p.Total != 1 || len(p.Blocked) != 1 || p.Blocked[0].Title.Name != "Alpha" ||
		p.Blocked[0].Words != "Failed the library check" || p.Blocked[0].Researched != nil {
		t.Errorf("blocked %+v", p)
	}
	var h ActivityPage
	if code := e.getKey(APIBase+"/activity?state=history&kinds=blocklist", key, &h); code != 200 {
		t.Fatalf("history: %d", code)
	}
	if len(h.History) != 1 || h.History[0].Kind != "blocklist" || h.History[0].Tone != "warn" {
		t.Errorf("history %+v", h.History)
	}
	// History filters: title (name part), copy, since (a date or a time).
	day := testNow.UTC().Format(time.DateOnly)
	later := testNow.Add(time.Hour).UTC().Format(time.RFC3339)
	for q, want := range map[string]int{"title=alp": 1, "title=beta": 0, "version=movie-web": 1,
		"version=movie-remux": 0, "since=" + day: 1, "since=" + later: 0, "title=alp&version=movie-web&since=" + day: 1} {
		var f ActivityPage
		if code := e.getKey(APIBase+"/activity?state=history&kinds=blocklist&"+q, key, &f); code != 200 {
			t.Fatalf("%s: %d", q, code)
		}
		if len(f.History) != want {
			t.Errorf("%s: %d entries, want %d", q, len(f.History), want)
		}
	}
	// Blocklist filters: title (name part) and copy narrow the rows and the total; a row whose download is known
	// carries its quality and indexer.
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		episodes, state, created_at, updated_at) VALUES (5, 2, 3, 'movie-web', 'b5', 'Beta.2021.WEB', 'torrentio',
		'WEBDL-2160p', '[]', 'failed', ?, ?)`, now, now)
	e.exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, release_title, reason, source, download_id,
		created_at) VALUES ('movie-web', 'b5', 2, 'Beta.2021.WEB', 'stalled', 'stalled', 5, ?),
		('movie-remux', 'a6', 1, 'Alpha.Remux', 'wrong', 'queue', NULL, ?)`, now+1, now+2)
	for q, want := range map[string]string{"": "Alpha.Remux,Beta.2021.WEB,Alpha.Sample.Release",
		"title=alp": "Alpha.Remux,Alpha.Sample.Release", "version=movie-web": "Beta.2021.WEB,Alpha.Sample.Release",
		"title=BET&version=movie-web": "Beta.2021.WEB", "title=beta&version=movie-remux": "",
		"version=movie-web&limit=1": "Beta.2021.WEB",
		// Every word, in the title's name or the blocked release's name.
		"title=remux+alpha": "Alpha.Remux", "title=sample+alp": "Alpha.Sample.Release", "title=2021+web": "Beta.2021.WEB"} {
		var f ActivityPage
		if code := e.getKey(APIBase+"/activity?state=blocked&"+q, key, &f); code != 200 {
			t.Fatalf("blocked %s: %d", q, code)
		}
		var got []string
		for _, b := range f.Blocked {
			got = append(got, b.Release)
		}
		n := len(strings.Split(want, ","))
		if want == "" {
			n = 0
		}
		if q == "version=movie-web&limit=1" {
			n = 2
		}
		if strings.Join(got, ",") != want || f.Total != n {
			t.Errorf("blocked %s: %v (total %d), want %s (total %d)", q, got, f.Total, want, n)
		}
	}
	var bf ActivityPage
	e.getKey(APIBase+"/activity?state=blocked&title=beta", key, &bf)
	if b := bf.Blocked[0]; b.Quality != "WEBDL-2160p" || b.Indexer != "torrentio" {
		t.Errorf("blocked Beta: %+v", b)
	}
	if b := p.Blocked[0]; b.Quality != "" || b.Indexer != "" {
		t.Errorf("blocked Alpha without a download: %+v", b)
	}
	for _, bad := range []string{"state=later", "kind=music", "title_id=x", "state=history&kinds=shadow",
		"state=history&cursor=bm8", "state=history&since=yesterday"} {
		if code := e.getKey(APIBase+"/activity?"+bad, key, nil); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
}

// TestActivityReleaseFacts: a grab's history row carries the release's quality, score, indexer, size, formats and
// episodes for an admin and none of them in the user view; the import's link actions are no rows of their own; an
// upgrade's replace is "Deleted (upgrade)" naming what replaced it, its old release unknown when the link row was
// taken over before the facts were recorded; a queue row its formats for an admin only.
func TestActivityReleaseFacts(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	now := store.Millis(testNow)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		score, size_bytes, formats, episodes, progress, state, created_at, updated_at) VALUES
		(1, 1, 2, 'movie-web', 'f1', 'Alpha.2020.2160p.WEB', 'dmm', 'WEBDL-2160p', 1500, 18400000000,
		'[{"name":"DV HDR10","score":1500}]', '[]', 40, 'downloading', ?, ?)`, now, now)
	// An old link action with no download of its own, on a link an upgrade (download 1) has since taken over, and
	// that upgrade's replace action (its path names the old file): neither carries the upgrade's release.
	e.exec(`INSERT INTO link (id, path, target, title_id, download_id, state, created_at, updated_at) VALUES
		(50, '/media/movies-web/Alpha (2020)/Alpha.srt', '/zurg/a.srt', 1, 1, 'active', ?, ?)`, now, now)
	e.exec(`INSERT INTO library_action (at, kind, mode, version_key, download_id, link_id, path, target, detail) VALUES
		(?, 'link', 'applied', 'movie-web', 1, NULL, '/media/movies-web/Alpha (2020)/Alpha.mkv', '/zurg/a.mkv', ''),
		(?, 'link', 'applied', 'movie-web', NULL, 50, '/media/movies-web/Alpha (2020)/Alpha.srt', '/zurg/a.srt', ''),
		(?, 'replace', 'applied', 'movie-web', 1, 50, '/media/movies-web/Alpha (2020)/Alpha.old.mkv', '/zurg/o.mkv', '')`,
		now+1, now+2, now+3)

	var h ActivityPage
	if code := e.getKey(APIBase+"/activity?state=history", key, &h); code != 200 {
		t.Fatalf("history: %d", code)
	}
	byID := map[string]ActivityEvent{}
	for _, x := range h.History {
		byID[x.ID] = x
	}
	g := byID["dl:1"]
	if g.Kind != "grab" || g.Quality != "WEBDL-2160p" || g.Score == nil || *g.Score != 1500 || g.Indexer != "dmm" ||
		g.Size != 18400000000 || len(g.Formats) != 1 || g.Formats[0].Name != "DV HDR10" || g.Formats[0].Score != 1500 {
		t.Errorf("grab row: %+v", g)
	}
	var replace ActivityEvent
	links := 0
	for _, x := range h.History {
		if x.Kind == "link" {
			links++
			replace = x
		}
	}
	if links != 1 || replace.Event != "deleted" || replace.Words != "Deleted (upgrade)" || replace.Release != "" ||
		replace.Quality != "" || replace.Score != nil || replace.ReplacedBy == nil ||
		replace.ReplacedBy.Release != "Alpha.2020.2160p.WEB" {
		t.Errorf("replace row (%d link rows): %+v", links, replace)
	}

	var u ActivityPage
	e.getKey(APIBase+"/activity?state=history&as=user", key, &u)
	if len(u.History) != len(h.History) {
		t.Fatalf("user history: %+v", u.History)
	}
	for _, x := range u.History {
		if x.Quality != "" || x.Score != nil || x.Indexer != "" || x.Size != 0 || x.Formats != nil || x.Episodes != nil ||
			x.ReplacedBy != nil || x.Release != "" {
			t.Errorf("user history row has release facts: %+v", x)
		}
	}

	var q, uq ActivityPage
	e.getKey(APIBase+"/activity?state=now", key, &q)
	e.getKey(APIBase+"/activity?state=now&as=user", key, &uq)
	if len(q.Now) != 1 || len(q.Now[0].Formats) != 1 || q.Now[0].Formats[0].Name != "DV HDR10" {
		t.Errorf("queue: %+v", q.Now)
	}
	if len(uq.Now) != 1 || uq.Now[0].Formats != nil {
		t.Errorf("user queue: %+v", uq.Now)
	}
}

func TestPartialImportActivity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "partial.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := store.Millis(testNow)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'Alpha', ?, ?)`, []any{now, now}},
		{`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'movie-web', ?, ?)`, []any{now, now}},
		{`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state, created_at,
			updated_at) VALUES (1, 1, 1, 'movie-web', ?, 'Alpha.Release', 'dmm', 'importing', ?, ?)`, []any{hashA, now, now}},
		{`INSERT INTO library_import (download_id, state, created_at, updated_at) VALUES (1, 'partial', ?, ?)`, []any{now, now}},
	} {
		if _, err := st.Writer().Exec(q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	ds, err := activity.Downloads(ctx, st.Reader(), activity.Filter{States: activity.InHand})
	if err != nil {
		t.Fatal(err)
	}
	w := &Web{st: st, now: func() time.Time { return testNow }}
	views, err := w.downloadViews(ctx, ds, false)
	if err != nil || len(views) != 1 {
		t.Fatalf("download views: %+v, %v", views, err)
	}
	d := views[0]
	if d.State != "importing" || d.Step != "ready" || d.Import != "partial" || d.Words != "Partly imported (retrying)" ||
		len(d.Steps) <= 3 || d.Steps[3].State != "current" {
		t.Errorf("partial activity: %+v", d)
	}
	body, err := json.Marshal(d)
	if err != nil || strings.Contains(string(body), hashA) {
		t.Errorf("user partial activity exposed file details: %s, %v", body, err)
	}

	// The partial import's retry comes from the library's back-off after its last update.
	if d.NextRetry == nil || !d.NextRetry.Equal(testNow.Add(10*time.Minute)) {
		t.Errorf("partial next retry: %v", d.NextRetry)
	}

	words, tone := historyWords(&journal.HistoryRow{Kind: journal.KindImport, Event: journal.EvImported, Import: "partial"})
	if words != "Imported" || tone != "warn" {
		t.Errorf("partial history: %q, %q", words, tone)
	}
}

// Dry runs stay in the journal: a dry-run link or replace is no History row.
func TestDryRunLinkNotInHistory(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	now := store.Millis(testNow)
	e.exec(`INSERT INTO library_action (at, kind, mode, version_key, path, detail) VALUES
		(?, 'link', 'dry_run', 'movie-web', '/library/Alpha.mkv', 'adopted from radarr /arr/a.mkv'),
		(?, 'replace', 'dry_run', 'movie-web', '/library/Alpha.old.mkv', 'unlink (x): dry run')`, now, now)
	var h ActivityPage
	if code := e.getKey(APIBase+"/activity?state=history", e.apiKey(), &h); code != 200 || len(h.History) != 0 {
		t.Fatalf("history: %d %+v", code, h.History)
	}
}

// A download's state change reaches a listener as a download event without a reload; a version change as a title
// event.
func TestActivityEvents(t *testing.T) {
	e := newEnv(t)
	key := e.apiKey()
	now := store.Millis(testNow)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		progress, state, created_at, updated_at) VALUES (1, 1, 2, 'movie-web', ?, 'Alpha.Secret.Release', 'dmm', 'rd',
		10, 'downloading', ?, ?)`, hashA, now, now)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.web.Run(ctx)
	ts := httptest.NewServer(e.mux)
	defer ts.Close()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+APIBase+"/events", nil)
	req.Header.Set(auth.APIKeyHeader, key)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	events := make(chan [2]string, 64)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var name string
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
				name = v
			} else if v, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				events <- [2]string{name, v}
			}
		}
		close(events)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for e.web.activityPolls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the activity poller took no baseline")
		}
		time.Sleep(2 * time.Millisecond)
	}
	next := func(want string, parts ...string) string {
		t.Helper()
		timeout := time.After(5 * time.Second)
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					t.Fatalf("stream ended waiting for %s", want)
				}
				if ev[0] != want {
					continue
				}
				all := true
				for _, p := range parts {
					all = all && strings.Contains(ev[1], p)
				}
				if all {
					return ev[1]
				}
			case <-timeout:
				t.Fatalf("no %s event with %v", want, parts)
			}
		}
	}
	e.exec(`UPDATE download SET state = 'ready', progress = 100, ready_at = ?, updated_at = ? WHERE id = 1`, now+5, now+5)
	data := next("download", `"state":"ready"`, `"previous":"downloading"`, `"words":"Download complete, waiting to be linked"`)
	if strings.Contains(data, "Secret") || strings.Contains(data, "Debrid") || strings.Contains(data, `"rd"`) {
		t.Errorf("download event carries admin fields: %s", data)
	}
	e.exec(`INSERT INTO library_import (download_id, state, created_at, updated_at) VALUES (1, 'waiting_mount', ?, ?)`,
		now+6, now+6)
	next("download", `"words":"Waiting for the mount"`)
	e.exec(`UPDATE version SET state = 'filled', updated_at = ? WHERE id = 2`, now+7)
	next("title", `"id":1`, `"reason":"versions"`)
	cancel()
}

// Waiting downloads carry when the wait began and the next scheduled try: a queued download its latest deferred
// attempt's retry_at, a finished one waiting for the mount the library's waiting_since. Active ones carry neither.
func TestActivityWaitTimes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "wait.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := store.Millis(testNow)
	hourAgo, twoHoursAgo := store.Millis(testNow.Add(-time.Hour)), store.Millis(testNow.Add(-2*time.Hour))
	retry, oldRetry := store.Millis(testNow.Add(15*time.Minute)), store.Millis(testNow.Add(-time.Minute))
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'Alpha', ?, ?)`, []any{now, now}},
		{`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'movie-web', ?, ?)`, []any{now, now}},
		{`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state, created_at,
			updated_at) VALUES (1, 1, 1, 'movie-web', ?, 'Queued.Release', 'dmm', 'queued', ?, ?)`, []any{hashA, twoHoursAgo, now}},
		{`INSERT INTO download_attempt (download_id, provider, outcome, retry_at, attempted_at) VALUES (1, 'rd', 'deferred', ?, ?)`,
			[]any{oldRetry, twoHoursAgo}},
		{`INSERT INTO download_attempt (download_id, provider, outcome, retry_at, attempted_at) VALUES (1, 'rd', 'deferred', ?, ?)`,
			[]any{retry, hourAgo}},
		{`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state, created_at,
			updated_at, ready_at) VALUES (2, 1, 1, 'movie-web', ?, 'Ready.Release', 'dmm', 'ready', ?, ?, ?)`,
			[]any{strings.Repeat("b", 40), twoHoursAgo, now, twoHoursAgo}},
		{`INSERT INTO library_import (download_id, state, waiting_since, created_at, updated_at) VALUES (2, 'waiting_mount', ?, ?, ?)`,
			[]any{hourAgo, hourAgo, now}},
		{`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state, created_at,
			updated_at, progress) VALUES (3, 1, 1, 'movie-web', ?, 'Busy.Release', 'dmm', 'downloading', ?, ?, 40)`,
			[]any{strings.Repeat("c", 40), twoHoursAgo, now}},
	} {
		if _, err := st.Writer().Exec(q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	ds, err := activity.Downloads(ctx, st.Reader(), activity.Filter{States: activity.InHand})
	if err != nil {
		t.Fatal(err)
	}
	w := &Web{st: st, now: func() time.Time { return testNow }}
	views, err := w.downloadViews(ctx, ds, false)
	if err != nil || len(views) != 3 {
		t.Fatalf("download views: %+v, %v", views, err)
	}
	at := func(p *time.Time) string {
		if p == nil {
			return "-"
		}
		return p.UTC().Format(time.RFC3339)
	}
	want := map[int64][2]string{
		1: {at(ptrTime(testNow.Add(-2 * time.Hour))), at(ptrTime(testNow.Add(15 * time.Minute)))},
		2: {at(ptrTime(testNow.Add(-time.Hour))), "-"},
		3: {"-", "-"},
	}
	for _, d := range views {
		if got := [2]string{at(d.WaitingSince), at(d.NextRetry)}; got != want[d.ID] {
			t.Errorf("download %d (%s): waiting since, next retry = %v, want %v", d.ID, d.State, got, want[d.ID])
		}
	}
	if views[1].Words != "Waiting for the mount" || views[1].Steps[3].Label != "Download complete" {
		t.Errorf("ready download words: %q, steps %+v", views[1].Words, views[1].Steps)
	}
	// A retry time already past is not a next retry.
	if _, r := waitTimes(&activity.Download{State: "queued", RetryAt: testNow.Add(-time.Second)}, testNow); r != nil {
		t.Errorf("past retry shown: %v", r)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

// A user's history pages over their own titles (and any kind filter over that kind): a page is never empty while
// more of theirs follow, so Load more always shows something or ends.
func TestActivityHistoryPagesOverOwnTitles(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	now := store.Millis(testNow)
	// Newest first: four entries for Beta (not the user's), then three for Alpha (theirs), then two for Gamma.
	at := now
	add := func(title int64, n int) {
		for i := 0; i < n; i++ {
			at -= 1000
			e.exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, release_title, reason, source, created_at)
				VALUES ('movie-web', ?, ?, 'R', 'bad', 'router', ?)`, fmt.Sprintf("%040x", at), title, at)
		}
	}
	add(2, 4)
	add(1, 3)
	add(3, 2)
	uid := e.account("usr", auth.RoleUser, auth.StatusActive, true)
	e.exec(`INSERT INTO request (user_id, kind, tmdb_id, title_id, name, versions, status, created_at, updated_at)
		VALUES (?, 'movie', 101, 1, 'Alpha', '["movie-web"]', 'approved', ?, ?)`, uid, now, now)
	ukey, _, err := e.auth.CreateKey(ctx, "k-usr", uid)
	if err != nil {
		t.Fatal(err)
	}
	walk := func(key, q string) (sizes []int, names []string) {
		cursor := ""
		for range 10 {
			var p ActivityPage
			u := APIBase + "/activity?state=history&limit=2" + q
			if cursor != "" {
				u += "&cursor=" + cursor
			}
			if code := e.getKey(u, key, &p); code != 200 {
				t.Fatalf("%s: %d", u, code)
			}
			sizes = append(sizes, len(p.History))
			for _, h := range p.History {
				names = append(names, h.Title.Name)
			}
			if cursor = p.NextCursor; cursor == "" {
				return sizes, names
			}
		}
		t.Fatalf("history did not end")
		return nil, nil
	}
	sizes, names := walk(ukey, "")
	if sizes[0] != 2 || strings.Join(names, ",") != "Alpha,Alpha,Alpha" || slices.Contains(sizes[:len(sizes)-1], 0) {
		t.Errorf("user pages %v, titles %v: want full pages of Alpha only", sizes, names)
	}
	// The kind filter pages over that kind too.
	sizes, names = walk(e.apiKey(), "&kind=series")
	if sizes[0] != 2 || strings.Join(names, ",") != "Gamma,Gamma" {
		t.Errorf("series pages %v, titles %v", sizes, names)
	}
}

// activity.NotStarted mirrors the router's gate errors (activity does not import router); this pins the texts.
func TestNotStartedMirrorsRouterGate(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"route ended: ", "route stopped: "} {
		// The downloads switch's text is kept for rows written before it was removed.
		for err, want := range map[error]string{router.ErrDisabled: "off", router.ErrShadow: "watch_only",
			errors.New("router: the version's grab switch is off; nothing is written for it"): "grab_off"} {
			if got := activity.NotStarted(prefix + err.Error()); got != want {
				t.Errorf("%s%v: %q, want %q", prefix, err, got, want)
			}
		}
	}
}

// A queued download ended because downloads were off reads as not started, not as a failure: History only (not the
// queue), and where a view shows it (words, no failed step, the admin's plain message).
func TestActivityNotStartedDownload(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	now := store.Millis(testNow)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state, error,
		created_at, updated_at) VALUES (1, 1, 2, 'movie-web', ?, 'Alpha.Release', 'dmm', 'failed', ?, ?, ?)`,
		hashA, "route ended: "+router.ErrDisabled.Error(), now, now)
	key := e.apiKey()
	var p ActivityPage
	if code := e.getKey(APIBase+"/activity", key, &p); code != 200 || len(p.Now) != 0 {
		t.Fatalf("activity: %d %+v", code, p)
	}
	ds, err := activity.Downloads(context.Background(), e.st.Reader(), activity.Filter{IDs: []int64{1}})
	if err != nil {
		t.Fatal(err)
	}
	views, err := e.web.downloadViews(context.Background(), ds, true)
	if err != nil || len(views) != 1 {
		t.Fatalf("views: %+v, %v", views, err)
	}
	d := views[0]
	if !d.NotStarted || d.Words != "Downloads were off when it was due to start" ||
		d.Error != "Nothing was added to a provider. Use Retry once downloads are on again." {
		t.Errorf("not started: %+v", d)
	}
	for _, s := range d.Steps {
		if s.State == "failed" {
			t.Errorf("failed step %+v", d.Steps)
		}
	}
	words, tone := historyWords(&journal.HistoryRow{Kind: journal.KindFailure, Event: journal.EvFailed,
		Reason: "route ended: " + router.ErrDisabled.Error()})
	if words != "Not started: downloads were off" || tone != "info" {
		t.Errorf("history: %q, %q", words, tone)
	}
	// A download tried at a provider before the gate closed (a refused add, waiting for its retry) is a failure.
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state, error,
		created_at, updated_at) VALUES (2, 1, 2, 'movie-web', ?, 'Alpha.Other', 'dmm', 'failed', ?, ?, ?)`,
		strings.Repeat("b", 40), "route ended: "+router.ErrDisabled.Error(), now, now)
	e.exec(`INSERT INTO download_attempt (download_id, provider, outcome, code, retry_at, attempted_at)
		VALUES (2, 'rd', 'refused', 'too_many_active', ?, ?), (1, 'rd', 'deferred', 'paced', ?, ?)`, now, now, now, now)
	p = ActivityPage{}
	if code := e.getKey(APIBase+"/activity", key, &p); code != 200 || len(p.Now) != 1 || p.Now[0].ID != 2 ||
		p.Now[0].NotStarted || p.Now[0].Words != "Download failed" {
		t.Fatalf("activity: %d %+v", code, p)
	}
	// The requests' SQL selects exactly the rows the Go check reads as not started.
	for _, text := range []string{"route ended: " + router.ErrShadow.Error(), "route stopped: router: the version's grab switch is off; nothing is written for it",
		"route ended: router: unknown version", "Route ended: router: x", "route ended: its release was not placed",
		"download failed: route ended: router: x", "stalled: not ready", ""} {
		e.exec(`UPDATE download SET error = ?`, text)
		ds, err := activity.Downloads(context.Background(), e.st.Reader(), activity.Filter{})
		if err != nil {
			t.Fatal(err)
		}
		var goIDs, sqlIDs []int64
		for _, d := range ds {
			if d.NotStarted() != "" {
				goIDs = append(goIDs, d.ID)
			}
		}
		rows, err := e.st.Reader().Query(`SELECT id FROM download WHERE state = 'failed' AND ` + activity.NotStartedSQL + ` ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			sqlIDs = append(sqlIDs, id)
		}
		rows.Close()
		if !slices.Equal(goIDs, sqlIDs) {
			t.Errorf("%q: Go %v, SQL %v", text, goIDs, sqlIDs)
		}
	}
	tried := []journal.Attempt{{Provider: "rd", Outcome: "refused", Code: "too_many_active"}}
	if words, _ := historyWords(&journal.HistoryRow{Kind: journal.KindFailure, Event: journal.EvFailed, Attempts: tried,
		Reason: "route ended: " + router.ErrDisabled.Error()}); words != "Failed" {
		t.Errorf("tried history: %q", words)
	}
	// The admin's History summary of a not-started row is the plain message; the journal keeps its own text.
	h, err := e.web.historyEvents(context.Background(), []*journal.HistoryRow{{Kind: journal.KindFailure,
		Event: journal.EvFailed, Title: journal.TitleRef{ID: 1}, Reason: "route ended: " + router.ErrDisabled.Error()}}, true)
	if err != nil || len(h) != 1 || h[0].Summary != "Nothing was added to a provider. Use Retry once downloads are on again." {
		t.Errorf("history summary: %+v, %v", h, err)
	}
	if words, tone = historyWords(&journal.HistoryRow{Kind: journal.KindFailure, Event: journal.EvFailed,
		Reason: "download failed: x"}); words != "Failed" || tone != "bad" {
		t.Errorf("real failure history: %q, %q", words, tone)
	}
}

func TestBlockWords(t *testing.T) {
	for source, want := range map[string]string{
		"library":            "Failed the library check",
		"provider":           "Failed at the provider",
		"ready_timeout":      "Timed out before import",
		"arr:radarr_4k_zurg": "Imported from Radarr's blocklist",
		"arr:sonarr_4k_zurg": "Imported from Sonarr's blocklist",
		"arr:lidarr":         "Imported from a Sonarr/Radarr blocklist",
		"something_new":      "Blocked",
	} {
		if got := blockWords(source); got != want {
			t.Errorf("blockWords(%q) = %q, want %q", source, got, want)
		}
	}
}

// TestQueueScoreEvidence: a queue row shows a score only with scoring evidence: stored formats ('[]' for a calculated
// 0) or a score; a download with neither shows none.
func TestQueueScoreEvidence(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	now := store.Millis(testNow)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		score, formats, episodes, progress, state, created_at, updated_at) VALUES
		(1, 1, 2, 'movie-web', 'f1', 'A.2160p.WEB', 'dmm', 'WEBDL-2160p', 0, NULL, '[]', 40, 'downloading', ?, ?),
		(2, 2, 3, 'movie-web', 'f2', 'B.2160p.WEB', 'dmm', 'WEBDL-2160p', 0, '[]', '[]', 40, 'downloading', ?, ?),
		(3, 1, 1, 'movie-remux', 'f3', 'A.2160p.REMUX', 'dmm', 'Remux-2160p', 900, NULL, '[]', 40, 'downloading', ?, ?)`,
		now, now, now, now, now, now)
	var q ActivityPage
	e.getKey(APIBase+"/activity?state=now", key, &q)
	got := map[int64]string{}
	for _, x := range q.Now {
		got[x.ID] = "none"
		if x.Score != nil {
			got[x.ID] = itoa(int64(*x.Score))
		}
	}
	if want := map[int64]string{1: "none", 2: "0", 3: "900"}; !maps.Equal(got, want) {
		t.Errorf("queue scores %v, want %v", got, want)
	}
}

// Activity › History and the title page's History tab read one builder (GET /activity?state=history, the tab with
// title_id): the title's rows are the same in both. Copies to other providers are rows for those who see files, with
// words naming the provider, and none in the user view.
func TestHistorySameForTitle(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	now := store.Millis(testNow)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		score, episodes, state, created_at, updated_at, imported_at) VALUES
		(1, 1, 2, 'movie-web', 'f1', 'Alpha.2020.2160p.WEB', 'dmm', 'WEBDL-2160p', 1500, '[]', 'imported', ?, ?, ?),
		(2, 2, 3, 'movie-web', 'f2', 'Beta.2021.2160p.WEB', 'dmm', 'WEBDL-2160p', 900, '[]', 'imported', ?, ?, ?)`,
		now, now, now+5, now+1, now+1, now+6)
	e.exec(`INSERT INTO library_import (download_id, state, mount_folder, plan, created_at, updated_at) VALUES
		(1, 'linked', '/m/a', '{}', ?, ?), (2, 'linked', '/m/b', '{}', ?, ?)`, now+5, now+5, now+6, now+6)
	e.exec(`INSERT INTO provider_copy_event (at, info_hash, name, title_id, provider, state, message, quality, score)
		VALUES (?, 'f1', 'Alpha.2020.2160p.WEB', 1, 'rd', 'not_cached', 'Not cached on RD', 'WEBDL-2160p', 1500)`, now+9)

	var all, one ActivityPage
	if code := e.getKey(APIBase+"/activity?state=history", key, &all); code != 200 {
		t.Fatalf("history: %d", code)
	}
	if code := e.getKey(APIBase+"/activity?state=history&title_id=1", key, &one); code != 200 {
		t.Fatalf("title history: %d", code)
	}
	var want []ActivityEvent
	for _, x := range all.History {
		if x.Title.ID == 1 {
			want = append(want, x)
		}
	}
	if !reflect.DeepEqual(one.History, want) || len(want) != 3 {
		t.Fatalf("title history:\n%+v\nwant\n%+v", one.History, want)
	}
	var words []string
	for _, x := range one.History {
		words = append(words, x.Words)
	}
	if !slices.Equal(words, []string{"Not cached on RD", "Imported", "Grabbed"}) {
		t.Errorf("words: %q", words)
	}
	if c := one.History[0]; c.Event != "copy_not_cached" || c.Release != "Alpha.2020.2160p.WEB" || c.Reason != "" ||
		c.Quality != "WEBDL-2160p" || c.Score == nil || *c.Score != 1500 {
		t.Errorf("copy row: %+v", c)
	}
	var u ActivityPage
	e.getKey(APIBase+"/activity?state=history&title_id=1&as=user", key, &u)
	if len(u.History) != 2 || u.History[0].Event != "imported" {
		t.Errorf("user history: %+v", u.History)
	}
	if code := e.getKey(APIBase+"/activity?state=history&events=copy_added&as=user", key, nil); code != 400 {
		t.Errorf("user copy filter: %d", code)
	}
}

// A series' History rows keep their episodes and file counts for those who see files; a household account (and the
// user view) gets the event words and title only.
func TestHistorySeriesRedaction(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	now := store.Millis(testNow)
	e.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (9, 3, 'tv-2160p', 'wanted', ?, ?)`,
		now, now)
	e.exec(`INSERT INTO episode (id, title_id, season, number, created_at, updated_at) VALUES (31, 3, 1, 1, ?, ?),
		(32, 3, 1, 2, ?, ?)`, now, now, now, now)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		score, episodes, state, created_at, updated_at, imported_at) VALUES
		(5, 3, 9, 'tv-2160p', 'f5', 'Gamma.S01.2160p.WEB', 'dmm', 'WEBDL-2160p', 700, '[31,32]', 'imported', ?, ?, ?)`,
		now, now+5, now+5)
	e.exec(`INSERT INTO library_import (download_id, state, mount_folder, plan, created_at, updated_at) VALUES
		(5, 'linked', '/m/g', '{}', ?, ?)`, now+5, now+5)
	e.exec(`INSERT INTO link (id, path, target, title_id, download_id, state, created_at, updated_at) VALUES
		(80, '/tv/G/S01E01.mkv', '/t1', 3, 5, 'active', ?, ?), (81, '/tv/G/S01E02.mkv', '/t2', 3, 5, 'active', ?, ?)`,
		now, now, now, now)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (80, 31), (81, 32)`)
	e.exec(`INSERT INTO library_action (at, kind, mode, version_key, download_id, link_id, path) VALUES
		(?, 'link', 'applied', 'tv-2160p', 5, 80, '/tv/G/S01E01.mkv'), (?, 'link', 'applied', 'tv-2160p', 5, 81, '/tv/G/S01E02.mkv')`,
		now+5, now+5)
	var a, u ActivityPage
	e.getKey(APIBase+"/activity?state=history&title_id=3", key, &a)
	if len(a.History) != 2 || !slices.Equal(a.History[0].Episodes, []string{"S01E01", "S01E02"}) {
		t.Fatalf("admin series history: %+v", a.History)
	}
	e.getKey(APIBase+"/activity?state=history&title_id=3&as=user", key, &u)
	if len(u.History) != 2 {
		t.Fatalf("user series history: %+v", u.History)
	}
	// Household rows carry the episode labels (S01E01), not file details.
	for _, x := range u.History {
		if !slices.Equal(x.Episodes, []string{"S01E01", "S01E02"}) || x.Files != 0 || x.Release != "" || x.Quality != "" || x.Provider != "" {
			t.Errorf("user row: %+v", x)
		}
	}
}

// A copy that found the torrent there already reads "Already on", not "Added to".
func TestHistoryCopyWords(t *testing.T) {
	t.Parallel()
	for ev, want := range map[string]string{journal.EvCopyAdded: "Added to AD", journal.EvCopyAlready: "Already on AD",
		journal.EvCopyNotCached: "Not cached on AD"} {
		if got, _ := historyWords(&journal.HistoryRow{Kind: journal.KindCopy, Event: ev, Provider: "ad"}); got != want {
			t.Errorf("%s: %q, want %q", ev, got, want)
		}
	}
}

// TestReleaseDateChoice: Home's Upcoming row, the calendar, the lists' Next release (and its sort) and the title page
// give a movie the same dates (activity.FirstReleaseSQL): the refreshed TMDb date over the arr's and TMDb's US ones,
// and only the first date of a type (a later US re-release or a limited run is not the next release).
func TestReleaseDateChoice(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	e.exec(`INSERT INTO title_meta (title_id, release_date, movie_dates_at, attempted_at, refresh_after) VALUES
		(1, '1968-04-02', NULL, 1, 1), (2, '2026-11-20', 1, 1, 1)`)
	e.exec(`INSERT INTO title_release_date (title_id, source, country, type, date) VALUES
		(1, 'radarr_4k_debrid', '', 'theatrical', '1968-04-02'), (1, 'tmdb', 'US', 'theatrical_limited', '2026-11-16'),
		(1, 'tmdb', 'US', 'theatrical', '2026-11-21'), (1, 'radarr_4k_debrid', '', 'digital', '2026-11-22'),
		(2, 'radarr_4k_debrid', '', 'theatrical', '2026-11-18'), (2, 'tmdb-art', '', 'theatrical', '2026-11-20'),
		(2, 'tmdb', 'US', 'theatrical', '2026-11-19'), (2, 'tmdb-art', '', 'digital', '2027-02-01')`)
	want := "Beta theatrical 2026-11-20|Alpha digital 2026-11-22"

	var d Discover
	e.getKey(APIBase+"/discover", key, &d)
	var home []string
	for _, it := range d.Upcoming.Items {
		home = append(home, it.Name+" "+it.Note+" "+it.Date)
	}
	if got := strings.Join(home, "|"); got != "Beta In cinemas 2026-11-20|Alpha Digital release 2026-11-22" {
		t.Errorf("Home upcoming: %s", got)
	}
	var cal CalendarPage
	e.getKey(APIBase+"/calendar?from=2026-11-01&to=2026-11-30&kind=movie&unmonitored=true", key, &cal)
	var days []string
	for _, it := range cal.Items {
		days = append(days, it.Title.Name+" "+it.Type+" "+it.Date)
	}
	if got := strings.Join(days, "|"); got != want {
		t.Errorf("calendar: %s, want %s", got, want)
	}
	var p TitlePage
	e.getKey(APIBase+"/titles?kind=movie&sort=next", key, &p)
	var next []string
	for _, r := range p.Items {
		if r.Next != nil {
			next = append(next, r.Name+" "+r.Next.Type+" "+r.Next.Date)
		}
	}
	if got := strings.Join(next, "|"); got != want {
		t.Errorf("Next sort and cells: %s, want %s", got, want)
	}
	if b := rowOf(t, p, 2); b.InCinemas != "2026-11-20" || b.DigitalRelease != "2027-02-01" {
		t.Errorf("Beta's list dates: %s, %s", b.InCinemas, b.DigitalRelease)
	}
	var td TitleDetail
	e.getKey(APIBase+"/titles/1", key, &td)
	if td.InCinemas != "1968-04-02" || td.DigitalRelease != "2026-11-22" {
		t.Errorf("Alpha's title page dates: %s, %s", td.InCinemas, td.DigitalRelease)
	}
	// Alpha's chosen dates have all passed and TMDb's release date moved to a re-release: no Next (Home and the
	// calendar have none either), and the Release date sort orders by the first cinema date, 1968.
	e.exec(`DELETE FROM title_release_date WHERE title_id = 1 AND type = 'digital'`)
	e.exec(`UPDATE title_meta SET release_date = '2026-12-25' WHERE title_id = 1`)
	p = TitlePage{}
	e.getKey(APIBase+"/titles?kind=movie&sort=next", key, &p)
	if a := rowOf(t, p, 1); a.Next != nil || p.Items[0].ID != 2 {
		t.Errorf("Alpha's next after its dates passed: %+v, first %s", a.Next, p.Items[0].Name)
	}
	p = TitlePage{}
	e.getKey(APIBase+"/titles?kind=movie&sort=release&order=asc", key, &p)
	if len(p.Items) < 2 || p.Items[0].ID != 1 || p.Items[1].ID != 2 {
		t.Errorf("release sort by the first cinema date: %+v", p.Items)
	}
}

// The queue (Activity › Queue, System › Status) is a reader rule over failed downloads (activity.NativeQueueFailureSQL):
// a miss on every provider and a download never started go to History only; any other failure stays until a newer
// imported download of the same title and library covers all of its episodes, or for 7 days. Household accounts and
// "View as a user" see downloads in progress only. Reading it changes no row, and History keeps every failure.
func TestActivityQueueFailureRule(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	now := store.Millis(testNow)
	old := store.Millis(testNow.Add(-8 * 24 * time.Hour))
	e.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (9, 3, 'tv-2160p', 'wanted', ?, ?)`,
		now, now)
	e.exec(`INSERT INTO episode (id, title_id, season, number, created_at, updated_at) VALUES (31, 3, 1, 1, ?1, ?1),
		(32, 3, 1, 2, ?1, ?1), (33, 3, 1, 3, ?1, ?1), (34, 3, 1, 4, ?1, ?1), (35, 3, 1, 5, ?1, ?1), (36, 3, 1, 6, ?1, ?1),
		(37, 3, 1, 7, ?1, ?1), (38, 3, 1, 8, ?1, ?1)`, now)
	type row struct {
		id, title, version int
		key, state, eps    string
		err                any
		updated            int64
	}
	for _, r := range []row{
		{1, 1, 2, "movie-web", "failed", "[]", "miss on every provider", now},
		{2, 1, 2, "movie-web", "failed", "[]", "route ended: " + router.ErrDisabled.Error(), now},
		{3, 1, 2, "movie-web", "failed", "[]", "stalled: not ready 10m0s after the add", now},
		{4, 1, 2, "movie-web", "failed", "[]", "ready timeout: not imported after 6h0m0s", now},
		{5, 1, 2, "movie-web", "failed", "[]", "provider status error: 503", now},
		{6, 1, 1, "movie-remux", "failed", "[]", "stalled: not ready", now},
		{7, 1, 1, "movie-remux", "imported", "[]", nil, now},
		{8, 1, 1, "movie-remux", "failed", "[]", "stalled: not ready", now},
		{9, 2, 3, "movie-web", "failed", "[]", "stalled: not ready", old},
		{11, 3, 9, "tv-2160p", "failed", "[31,32]", "stalled: not ready", now},
		// 12 asked for E01-E02 but linked E01 only (E02 refused): 11 stays. 14 asked for nothing listed but linked
		// E03, which 16 then upgraded in place (the link is 16's now): 13 stays covered by a newer import's link.
		// Coverage is what newer imports hold linked, not what they asked for.
		{12, 3, 9, "tv-2160p", "imported", "[31,32]", nil, now},
		{13, 3, 9, "tv-2160p", "failed", "[33]", "stalled: not ready", now},
		{14, 3, 9, "tv-2160p", "imported", "[]", nil, now},
		{15, 2, 3, "movie-web", "downloading", "[]", nil, now},
		{16, 3, 9, "tv-2160p", "imported", "[33]", nil, now},
		// Coverage split over two newer imports (E04 by 19, E05 by 20) hides 17; 18 stays, its E06 never linked.
		{17, 3, 9, "tv-2160p", "failed", "[34,35]", "stalled: not ready", now},
		{18, 3, 9, "tv-2160p", "failed", "[34,36]", "stalled: not ready", now},
		{19, 3, 9, "tv-2160p", "imported", "[34]", nil, now},
		{20, 3, 9, "tv-2160p", "imported", "[35]", nil, now},
		// A manual import (episodes on download_file: E07) is not covered by a newer import of E08; a series download
		// with no known episodes is never covered.
		{21, 3, 9, "tv-2160p", "failed", "[]", "library: no file matched", now},
		{22, 3, 9, "tv-2160p", "failed", "[]", "stalled: not ready", now},
		{23, 3, 9, "tv-2160p", "imported", "[38]", nil, now},
	} {
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, episodes,
			state, error, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 'dmm', ?, ?, ?, ?, ?)`,
			r.id, r.title, r.version, r.key, fmt.Sprintf("%040x", r.id), fmt.Sprintf("Release.%d", r.id), r.eps,
			r.state, r.err, r.updated, r.updated)
	}
	e.exec(`INSERT INTO download_attempt (download_id, provider, outcome, code, attempted_at) VALUES
		(1, 'rd', 'skipped', 'uncached', ?), (3, 'rd', 'accepted', '', ?)`, now, now)
	e.exec(`INSERT INTO link (id, path, target, title_id, download_id, state, created_at, updated_at) VALUES
		(90, '/tv/G/S01E01.mkv', '/t1', 3, 12, 'active', ?1, ?1), (92, '/tv/G/S01E03.mkv', '/t3', 3, 16, 'active', ?1, ?1),
		(93, '/movies/A/A.mkv', '/t4', 1, 7, 'active', ?1, ?1), (94, '/tv/G/S01E04.mkv', '/t5', 3, 19, 'active', ?1, ?1),
		(95, '/tv/G/S01E05.mkv', '/t6', 3, 20, 'active', ?1, ?1), (96, '/tv/G/S01E08.mkv', '/t7', 3, 23, 'active', ?1, ?1)`, now)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (90, 31), (92, 33), (94, 34), (95, 35), (96, 38)`)
	e.exec(`INSERT INTO download_file (download_id, rel, episodes, source, updated_at) VALUES (21, 'G.S01E07.mkv', '[37]', 'manual', ?)`, now)
	snapshot := func() string {
		var b strings.Builder
		for _, table := range []string{"download", "download_attempt", "library_import", "router_blocklist", "link", "link_episode", "download_file"} {
			rows, err := e.st.Reader().Query(`SELECT * FROM ` + table + ` ORDER BY rowid`)
			if err != nil {
				t.Fatal(err)
			}
			cols, _ := rows.Columns()
			for rows.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				if err := rows.Scan(ptrs...); err != nil {
					t.Fatal(err)
				}
				fmt.Fprintf(&b, "%s %v\n", table, vals)
			}
			rows.Close()
		}
		return b.String()
	}
	before := snapshot()
	ids := func(p ActivityPage) []int64 {
		var out []int64
		for _, d := range p.Now {
			out = append(out, d.ID)
		}
		return out
	}
	var a, u ActivityPage
	e.getKey(APIBase+"/activity?state=now", key, &a)
	if got, want := ids(a), []int64{22, 21, 18, 15, 11, 8, 5, 4, 3}; !slices.Equal(got, want) {
		t.Errorf("admin queue %v, want %v", got, want)
	}
	for _, d := range a.Now {
		if d.ID == 11 && (!slices.Equal(d.EpisodeIDs, []int64{31, 32}) || !slices.Equal(d.Episodes, []string{"S01E01", "S01E02"})) {
			t.Errorf("download 11 episodes: %v %v", d.EpisodeIDs, d.Episodes)
		}
	}
	e.getKey(APIBase+"/activity?state=now&as=user", key, &u)
	if got := ids(u); !slices.Equal(got, []int64{15}) {
		t.Errorf("user view %v, want only the download in progress", got)
	}
	// A household account (no "see files") sees in-progress downloads only, whatever its requests cover.
	uid := e.account("ann", auth.RoleUser, auth.StatusActive, true)
	e.exec(`INSERT INTO request (user_id, kind, tmdb_id, title_id, name, versions, status, created_at, updated_at)
		VALUES (?, 'movie', 101, 1, 'Alpha', '["movie-web"]', 'approved', ?, ?), (?, 'movie', 102, 2, 'Beta', '["movie-web"]',
		'approved', ?, ?)`, uid, now, now, uid, now, now)
	ukey, _, err := e.auth.CreateKey(context.Background(), "k-ann", uid)
	if err != nil {
		t.Fatal(err)
	}
	var h ActivityPage
	e.getKey(APIBase+"/activity?state=now", ukey, &h)
	if got := ids(h); !slices.Equal(got, []int64{15}) {
		t.Errorf("household queue %v, want only the download in progress", got)
	}
	if after := snapshot(); after != before {
		t.Errorf("reading the queue changed rows:\nbefore %s\nafter %s", before, after)
	}
	// History still lists the failures the queue leaves out.
	var hist ActivityPage
	e.getKey(APIBase+"/activity?state=history&title_id=1&limit=200", key, &hist)
	seen := map[string]bool{}
	for _, x := range hist.History {
		seen[x.ID] = true
	}
	for _, id := range []string{"dl:1:fail", "dl:2:fail", "dl:6:fail"} {
		if !seen[id] {
			t.Errorf("history lacks %s: %v", id, seen)
		}
	}
}
