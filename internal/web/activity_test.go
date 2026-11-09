package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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
	e.getKey(APIBase+"/calendar?from=2026-11-01&to=2026-11-30&kind=movie", key, &cal)
	if len(cal.Items) != 2 {
		t.Errorf("movies only: %d items", len(cal.Items))
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
	for _, bad := range []string{"state=later", "kind=music", "title_id=x", "state=history&kinds=shadow",
		"state=history&cursor=bm8", "state=history&since=yesterday"} {
		if code := e.getKey(APIBase+"/activity?"+bad, key, nil); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
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
