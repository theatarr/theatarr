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
	e.getKey(APIBase+"/calendar?from=2026-11-01&to=2026-11-30&unmonitored=true", key, &cal)
	if len(cal.Items) != 4 {
		t.Errorf("with unmonitored: %d items", len(cal.Items))
	}
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
	// A copy removed from its library does not make the date available: Alpha's only file is in movie-remux.
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (1, 'radarr_4k_debrid', ?)`, now)
	cal = CalendarPage{}
	e.getKey(APIBase+"/calendar?from=2026-11-01&to=2026-11-30&kind=movie", key, &cal)
	if len(cal.Items) != 2 || cal.Items[0].Title.Name != "Alpha" || cal.Items[0].Available || !cal.Items[1].Available {
		t.Errorf("removed copy counted: %+v", cal.Items)
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
		p.Blocked[0].Words != "Failed the library check (sample or runtime)" || p.Blocked[0].Researched != nil {
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
		"version=movie-web&limit=1": "Beta.2021.WEB"} {
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
// episodes for an admin and none of them in the user view; a "Linked into the library" row the release title and
// quality of the link's download, and no score; a queue row its formats for an admin only.
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
	var link, bare, replace ActivityEvent
	for _, x := range h.History {
		switch {
		case x.Kind == "link" && x.Words == "Replaced by an upgrade":
			replace = x
		case x.Kind == "link" && x.Release != "":
			link = x
		case x.Kind == "link":
			bare = x
		}
	}
	if replace.ID == "" || replace.Release != "" || replace.Quality != "" || replace.Score != nil {
		t.Errorf("replace row: %+v", replace)
	}
	if link.Words != "Linked into the library" || link.Release != "Alpha.2020.2160p.WEB" || link.Quality != "WEBDL-2160p" ||
		link.Score != nil {
		t.Errorf("link row: %+v", link)
	}
	if bare.ID == "" || bare.Words != "Linked into the library" || bare.Release != "" || bare.Quality != "" || bare.Score != nil {
		t.Errorf("link row without a download: %+v", bare)
	}

	var u ActivityPage
	e.getKey(APIBase+"/activity?state=history&as=user", key, &u)
	if len(u.History) != len(h.History) {
		t.Fatalf("user history: %+v", u.History)
	}
	for _, x := range u.History {
		if x.Quality != "" || x.Score != nil || x.Indexer != "" || x.Size != 0 || x.Formats != nil || x.Episodes != nil {
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

	words, tone := historyWords(&journal.Entry{Kind: journal.KindImport, Import: &journal.Import{State: "partial"}})
	if words != "Partly imported (retrying)" || tone != "warn" {
		t.Errorf("partial history: %q, %q", words, tone)
	}
}

func TestDryRunLinkHistoryWords(t *testing.T) {
	t.Parallel()
	words, tone := historyWords(&journal.Entry{Kind: journal.KindLink, Import: &journal.Import{
		State: "dry_run", Links: []journal.Link{{Kind: "link", Path: "/library/Alpha.mkv"}},
	}})
	if words != "Would be linked (the library is in dry-run mode)" || tone != "info" {
		t.Fatalf("dry-run link history: %q, %q", words, tone)
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

// A queued download ended because downloads were off reads as not started, not as a failure: in Activity (words, no
// failed step, the admin's plain message) and in History.
func TestActivityNotStartedDownload(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	now := store.Millis(testNow)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state, error,
		created_at, updated_at) VALUES (1, 1, 2, 'movie-web', ?, 'Alpha.Release', 'dmm', 'failed', ?, ?, ?)`,
		hashA, "route ended: "+router.ErrDisabled.Error(), now, now)
	key := e.apiKey()
	var p ActivityPage
	if code := e.getKey(APIBase+"/activity", key, &p); code != 200 || len(p.Now) != 1 {
		t.Fatalf("activity: %d %+v", code, p)
	}
	d := p.Now[0]
	if !d.NotStarted || d.Words != "Downloads were off when it was due to start" ||
		d.Error != "Nothing was added to a provider. Use Retry once downloads are on again." {
		t.Errorf("not started: %+v", d)
	}
	for _, s := range d.Steps {
		if s.State == "failed" {
			t.Errorf("failed step %+v", d.Steps)
		}
	}
	words, tone := historyWords(&journal.Entry{Kind: journal.KindFailure,
		Download: &journal.Download{State: "failed", Error: "route ended: " + router.ErrDisabled.Error()}})
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
	if code := e.getKey(APIBase+"/activity", key, &p); code != 200 || len(p.Now) != 2 {
		t.Fatalf("activity: %d %+v", code, p)
	}
	for _, d := range p.Now {
		if want := d.ID == 1; d.NotStarted != want || (d.Words == "Download failed") == want {
			t.Errorf("download %d: not started %v, words %q", d.ID, d.NotStarted, d.Words)
		}
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
	if words, _ := historyWords(&journal.Entry{Kind: journal.KindFailure, Attempts: tried,
		Download: &journal.Download{State: "failed", Error: "route ended: " + router.ErrDisabled.Error()}}); words != "Download failed" {
		t.Errorf("tried history: %q", words)
	}
	// The admin's History summary of a not-started row is the plain message; the journal keeps its own text.
	h, err := e.web.historyEvents(context.Background(), []*journal.Entry{{Kind: journal.KindFailure,
		Summary: "download failed: route ended: router: router.enabled is false", Title: journal.TitleRef{ID: 1},
		Download: &journal.Download{State: "failed", Error: "route ended: " + router.ErrDisabled.Error()}}}, true)
	if err != nil || len(h) != 1 || h[0].Summary != "Nothing was added to a provider. Use Retry once downloads are on again." {
		t.Errorf("history summary: %+v, %v", h, err)
	}
	if words, tone = historyWords(&journal.Entry{Kind: journal.KindFailure,
		Download: &journal.Download{State: "failed", Error: "download failed: x"}}); words != "Download failed" || tone != "bad" {
		t.Errorf("real failure history: %q, %q", words, tone)
	}
}
