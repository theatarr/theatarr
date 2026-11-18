package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/notify"
)

// TestProblemReports: a household member reports a movie with a file; a second report of it updates the open one;
// there is nothing to report without a file. The reporter sees their own report without file detail or names; a
// manager sees every open one with its file, the badge counts it, and Mark fixed reaches the reporter with the reply.
// Only a new report is notified.
func TestProblemReports(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var sent []notify.Event
	e := newEnvWith(t, func(c *Config) {
		c.OnProblem = func(ev notify.Event) {
			mu.Lock()
			defer mu.Unlock()
			sent = append(sent, ev)
		}
	})
	uid := e.account("usr", auth.RoleUser, auth.StatusActive, true)
	mid := e.account("mgr", auth.RoleManager, auth.StatusActive, false)
	uc, ucsrf := e.session(uid)
	mc, mcsrf := e.session(mid)
	b := APIBase + "/reports"

	var p ProblemReport
	if code := e.jsonCall("POST", b, CreateReport{TitleID: 1, Kind: "picture", Note: "green blocks"}, uc, ucsrf,
		&p); code != http.StatusCreated || p.Status != ReportOpen || !p.Mine || p.User != nil {
		t.Fatalf("report: %d %+v", code, p)
	}
	var again ProblemReport
	if code := e.jsonCall("POST", b, CreateReport{TitleID: 1, Kind: "sound"}, uc, ucsrf, &again); code != http.StatusOK ||
		again.ID != p.ID || again.Kind != "sound" || again.Note != "" {
		t.Fatalf("second report: %d %+v", code, again)
	}
	for _, tc := range []struct {
		body CreateReport
		code int
	}{
		{CreateReport{TitleID: 3, EpisodeID: 1, Kind: "picture"}, http.StatusConflict}, // Gamma has no file
		{CreateReport{TitleID: 1, EpisodeID: 5, Kind: "picture"}, http.StatusBadRequest},
		{CreateReport{TitleID: 1, Kind: "smell"}, http.StatusBadRequest},
		{CreateReport{TitleID: 1, Kind: "other", Note: strings.Repeat("é", 501)}, http.StatusBadRequest},
		{CreateReport{TitleID: 99, Kind: "other"}, http.StatusNotFound},
	} {
		if code := e.jsonCall("POST", b, tc.body, uc, ucsrf, nil); code != tc.code {
			t.Errorf("%+v: %d, want %d", tc.body, code, tc.code)
		}
	}
	mu.Lock()
	if len(sent) != 1 || sent[0].Kind != notify.ProblemReported || sent[0].Problem.Name != "Alpha (2020)" ||
		sent[0].Problem.Reporter.Username != "usr" || !slices.Equal(sent[0].Problem.Versions, []string{"movie-remux"}) {
		t.Errorf("notifications %+v", sent)
	}
	mu.Unlock()

	// The reporter's list: no file, release or names.
	rec := e.get(b, uc)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "/movies/") ||
		strings.Contains(rec.Body.String(), "REMUX") || strings.Contains(rec.Body.String(), `"user"`) {
		t.Fatalf("user list: %d %s", rec.Code, rec.Body.String())
	}
	var mine ReportPage
	if code := e.jsonCall("GET", b, nil, mc, "", &mine); code != http.StatusOK || len(mine.Items) != 1 ||
		mine.Items[0].User == nil || len(mine.Items[0].Files) != 1 {
		t.Fatalf("manager list: %d %+v", code, mine)
	}
	f := mine.Items[0].Files[0]
	if f.LinkID != 901 || !slices.Equal(f.Versions, []string{"movie-remux"}) || len(f.Libraries) != 1 || f.Replace ||
		f.Why == "" {
		t.Errorf("file %+v", f)
	}
	var sum RequestSummary
	if code := e.jsonCall("GET", APIBase+"/requests/summary", nil, mc, "", &sum); code != 200 || sum.Problems != 1 ||
		sum.Badge != 1 {
		t.Errorf("manager summary %+v", sum)
	}
	if code := e.jsonCall("POST", b+"/1/fix", FixReport{Reply: "x"}, uc, ucsrf, nil); code != http.StatusForbidden {
		t.Errorf("user fixes: %d", code)
	}
	if code := e.jsonCall("POST", b+"/1/fix", FixReport{Reply: "Swapped the file"}, mc, mcsrf, &p); code != 200 ||
		p.Status != ReportFixed || p.ResolvedBy == nil {
		t.Fatalf("fix: %d %+v", code, p)
	}
	if code := e.jsonCall("POST", b+"/1/fix", FixReport{}, mc, mcsrf, nil); code != http.StatusConflict {
		t.Errorf("fix again: %d", code)
	}
	mine = ReportPage{}
	if code := e.jsonCall("GET", b, nil, uc, "", &mine); code != 200 || len(mine.Items) != 1 ||
		mine.Items[0].Status != ReportFixed || mine.Items[0].Reply != "Swapped the file" {
		t.Errorf("reporter after the fix: %+v", mine)
	}
	sum = RequestSummary{}
	if e.jsonCall("GET", APIBase+"/requests/summary", nil, mc, "", &sum); sum.Problems != 0 {
		t.Errorf("summary after the fix %+v", sum)
	}
	// After the fix a new report of the same movie is a new one.
	if code := e.jsonCall("POST", b, CreateReport{TitleID: 1, Kind: "picture"}, uc, ucsrf, &again); code != http.StatusCreated ||
		again.ID == p.ID {
		t.Errorf("report after the fix: %d %+v", code, again)
	}
}

// TestReplaceReportedFile: Replace file unlinks the file, blocklists its release in every library that loses it and
// searches those libraries for every episode the file held; the file list names those libraries and episodes. It
// never runs without the search after it: paused, a library or an episode not monitored, or a release Theatarr does
// not know refuse before anything changes. Nothing else of the provider's is touched (no grab, no retry).
func TestReplaceReportedFile(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	o.seedLibrary()
	uid := o.account("reporter", auth.RoleUser, auth.StatusActive, true)
	uc, ucsrf := o.session(uid)
	// Link 90 holds S01E01 and S01E03 of Delta, from a known release.
	o.exec(`UPDATE link SET release_title = 'Delta.S01.1080p.BluRay-GRP' WHERE id = 90`)
	o.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (90, 43)`)
	var p ProblemReport
	if code := o.jsonCall("POST", APIBase+"/reports", CreateReport{TitleID: 4, EpisodeID: 41, Kind: "wont_play"}, uc,
		ucsrf, &p); code != http.StatusCreated || p.Episode != "S01E01" {
		t.Fatalf("report: %d %+v", code, p)
	}
	files := func() []ReportFile {
		var page ReportPage
		if code := o.jsonCall("GET", APIBase+"/reports", nil, o.mc, "", &page); code != 200 || len(page.Items) != 1 {
			t.Fatalf("list: %d %+v", code, page)
		}
		return page.Items[0].Files
	}
	replace := func() int {
		return o.jsonCall("POST", APIBase+"/reports/1/replace", ReplaceReported{LinkID: 90}, o.mc, o.mcsrf, nil)
	}
	blocked := func() int { return o.count(`SELECT COUNT(*) FROM router_blocklist WHERE title_id = 4`) }

	// Paused: offered nowhere, refused.
	if fs := files(); len(fs) != 1 || fs[0].Replace || !slices.Equal(fs[0].Episodes, []string{"S01E01", "S01E03"}) {
		t.Fatalf("paused files %+v", fs)
	}
	if code := replace(); code != http.StatusConflict || len(o.ops.unlinks) != 0 || blocked() != 0 {
		t.Fatalf("replace while paused: %d unlinks %v", code, o.ops.unlinks)
	}
	o.activate("anime-tv")
	// An episode the file holds is not monitored: no search would follow for it.
	o.exec(`UPDATE episode_version_state SET monitored = 0 WHERE episode_id = 43`)
	if fs := files(); fs[0].Replace || !strings.Contains(fs[0].Why, "S01E03") {
		t.Fatalf("unmonitored episode %+v", fs[0])
	}
	if code := replace(); code != http.StatusConflict || len(o.ops.unlinks) != 0 {
		t.Fatalf("replace with an unmonitored episode: %d", code)
	}
	o.exec(`UPDATE episode_version_state SET monitored = 1 WHERE episode_id = 43`)
	// The library is not monitored.
	o.exec(`UPDATE version SET monitored = 0 WHERE id = 4`)
	if fs := files(); fs[0].Replace {
		t.Fatalf("unmonitored library %+v", fs[0])
	}
	o.exec(`UPDATE version SET monitored = 1 WHERE id = 4`)
	// A release Theatarr does not know: blocklisting could not keep it out.
	o.exec(`UPDATE link SET release_title = NULL WHERE id = 90`)
	if fs := files(); fs[0].Replace {
		t.Fatalf("unknown release %+v", fs[0])
	}
	o.exec(`UPDATE link SET release_title = 'Delta.S01.1080p.BluRay-GRP' WHERE id = 90`)
	// The title is no longer anime: the anime library's search skips it, so it would never search again.
	o.exec(`UPDATE title SET anime = 0 WHERE id = 4`)
	if fs := files(); fs[0].Replace || !strings.Contains(fs[0].Why, " does not search this title (it is for anime only), "+
		"so no new file would be searched") {
		t.Fatalf("anime library, title not anime %+v", fs[0])
	}
	if code := replace(); code != http.StatusConflict || len(o.ops.unlinks) != 0 {
		t.Fatalf("replace in a library that skips the title: %d", code)
	}
	o.exec(`UPDATE title SET anime = 1 WHERE id = 4`)
	// A file two libraries share: refused when either one would not search the title.
	o.exec(`INSERT INTO version (id, title_id, policy_key, state, monitored, created_at, updated_at)
		VALUES (99, 4, 'tv-2160p', 'filled', 1, 0, 0)`)
	o.exec(`INSERT INTO link_version (link_id, version_id) VALUES (90, 99)`)
	if fs := files(); fs[0].Replace || len(fs[0].Versions) != 2 ||
		!strings.Contains(fs[0].Why, " does not search this title (it is not for anime), ") {
		t.Fatalf("shared with a library not for anime %+v", fs[0])
	}
	o.exec(`DELETE FROM link_version WHERE version_id = 99`)
	o.exec(`DELETE FROM version WHERE id = 99`)

	fs := files()
	if !fs[0].Replace || fs[0].Why != "" || !slices.Equal(fs[0].Versions, []string{"anime-tv"}) || len(fs[0].Libraries) != 1 {
		t.Fatalf("replaceable file %+v", fs[0])
	}
	var got ReplaceResult
	if code := o.jsonCall("POST", APIBase+"/reports/1/replace", ReplaceReported{LinkID: 90}, o.mc, o.mcsrf,
		&got); code != 200 || got.Search == nil || !got.Search.Started {
		t.Fatalf("replace: %d %+v", code, got)
	}
	if !slices.Equal(o.ops.unlinks, []int64{90}) || len(o.ops.grabs) != 0 || len(o.ops.retries) != 0 {
		t.Errorf("ops: unlinks %v grabs %v retries %v", o.ops.unlinks, o.ops.grabs, o.ops.retries)
	}
	if n := o.count(`SELECT COUNT(*) FROM router_blocklist WHERE title_id = 4 AND version_key = 'anime-tv'
		AND source = 'ui' AND release_title = 'Delta.S01.1080p.BluRay-GRP'`); n != 1 {
		t.Errorf("blocklist rows %d", n)
	}
	o.acq.mu.Lock()
	subs := slices.Clone(o.acq.submits)
	o.acq.mu.Unlock()
	if len(subs) != 1 || !slices.Equal(subs[0].Versions, []string{"anime-tv"}) || !slices.Equal(subs[0].TitleIDs, []int64{4}) ||
		!slices.Equal(subs[0].EpisodeIDs, []int64{41, 43}) {
		t.Errorf("searches %+v", subs)
	}
	// The tab closes right after the file is removed: the blocklist and the search still happen.
	o.exec(`DELETE FROM router_blocklist`)
	o.acq.mu.Lock()
	o.acq.submits = nil
	o.acq.mu.Unlock()
	o.ops.unlinks = nil
	ctx, cancel := context.WithCancel(t.Context())
	o.ops.onUnlink = cancel
	b, _ := json.Marshal(ReplaceReported{LinkID: 90})
	r := httptest.NewRequestWithContext(ctx, "POST", APIBase+"/reports/1/replace", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(o.mc)
	r.Header.Set(auth.CSRFHeader, o.mcsrf)
	o.do(r)
	o.ops.onUnlink = nil
	if !slices.Equal(o.ops.unlinks, []int64{90}) || blocked() != 1 {
		t.Errorf("cancelled after the unlink: unlinks %v, blocklist rows %d", o.ops.unlinks, blocked())
	}
	o.acq.mu.Lock()
	if len(o.acq.submits) != 1 {
		t.Errorf("cancelled after the unlink: searches %+v", o.acq.submits)
	}
	o.acq.mu.Unlock()

	// The report stays open; a household account may not replace.
	var after ProblemReport
	if p2, _ := o.web.getReport(t.Context(), 1); p2 == nil || p2.Status != ReportOpen {
		t.Errorf("report after replace %+v", p2)
	}
	if code := o.jsonCall("POST", APIBase+"/reports/1/replace", ReplaceReported{LinkID: 90}, uc, ucsrf, &after); code != http.StatusForbidden {
		t.Errorf("user replace: %d", code)
	}
}

// TestReplaceFileFromTitlePage: a movie's file rows on the title page and the episode window's file carry Replace
// file for managers who see files (household accounts get nothing), with the Problems page's refusal; POST
// links/{id}/replace runs the same operation without a report, and the journal says a replacement search was
// requested.
func TestReplaceFileFromTitlePage(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	o.seedLibrary()
	uid := o.account("household", auth.RoleUser, auth.StatusActive, true)
	uc, ucsrf := o.session(uid)
	scopeOf := func(c *http.Cookie) *ReplaceScope {
		t.Helper()
		var d TitleDetail
		if code := o.jsonCall("GET", APIBase+"/titles/1", nil, c, "", &d); code != 200 {
			t.Fatalf("title: %d", code)
		}
		for _, v := range d.Versions {
			for _, l := range v.Links {
				if l.ID == 901 {
					return l.ReplaceScope
				}
			}
		}
		return nil
	}
	// Paused: the row says why, as the Problems page does, and the action is refused before anything changes.
	s := scopeOf(o.mc)
	if s == nil || s.Replace || s.Why == "" || len(s.Libraries) != 1 {
		t.Fatalf("paused scope %+v", s)
	}
	// The reason names the library, not its key.
	if strings.Contains(s.Why, "(movie-remux)") || !strings.Contains(s.Why, "("+o.web.versionLabel("movie-remux")+")") {
		t.Errorf("paused reason names the key: %q", s.Why)
	}
	var refused apiError
	if code := o.jsonCall("POST", APIBase+"/links/901/replace", nil, o.mc, o.mcsrf, &refused); code != http.StatusConflict ||
		refused.Error != s.Why || len(o.ops.unlinks) != 0 {
		t.Fatalf("replace while paused: %d %+v unlinks %v", code, refused, o.ops.unlinks)
	}
	if s := scopeOf(uc); s != nil {
		t.Errorf("household scope %+v", s)
	}
	o.activate("movie-remux")
	if s := scopeOf(o.mc); s == nil || !s.Replace || s.Why != "" {
		t.Fatalf("scope %+v", s)
	}
	// An anime title: the library is not for anime, its search skips the title, so the file is not replaced.
	o.exec(`UPDATE title SET anime = 1 WHERE id = 1`)
	if s := scopeOf(o.mc); s == nil || s.Replace || !strings.HasSuffix(s.Why,
		" does not search this title (it is not for anime), so no new file would be searched") {
		t.Fatalf("anime title scope %+v", s)
	}
	refused = apiError{}
	if code := o.jsonCall("POST", APIBase+"/links/901/replace", nil, o.mc, o.mcsrf, &refused); code != http.StatusConflict ||
		!strings.Contains(refused.Error, "does not search this title") || len(o.ops.unlinks) != 0 {
		t.Fatalf("replace of an anime title's file: %d %+v", code, refused)
	}
	o.exec(`UPDATE title SET anime = 0 WHERE id = 1`)
	// A file that is not one of Theatarr's links: the reason says what to do, the same on the title page and Problems.
	o.exec(`UPDATE link SET kind = 'copy' WHERE id = 901`)
	if s := scopeOf(o.mc); s == nil || s.Replace ||
		s.Why != "This file is not one of Theatarr's links. Use Edit file facts" {
		t.Fatalf("copied file scope %+v", s)
	}
	refused = apiError{}
	if code := o.jsonCall("POST", APIBase+"/links/901/replace", nil, o.mc, o.mcsrf, &refused); code != http.StatusConflict ||
		refused.Error != "This file is not one of Theatarr's links. Use Edit file facts" {
		t.Fatalf("replace of a copied file: %d %+v", code, refused)
	}
	o.exec(`UPDATE link SET kind = 'symlink' WHERE id = 901`)
	// The episode window: the file's scope names its episodes.
	o.exec(`UPDATE link SET release_title = 'Delta.S01E01.1080p.BluRay-GRP' WHERE id = 90`)
	var ep EpisodeDetail
	if code := o.jsonCall("GET", APIBase+"/titles/4/episodes/41", nil, o.mc, "", &ep); code != 200 || len(ep.Versions) != 1 ||
		ep.Versions[0].File == nil || ep.Versions[0].File.ReplaceScope == nil || !ep.Versions[0].File.ReplaceScope.Replace ||
		!slices.Equal(ep.Versions[0].File.ReplaceScope.Episodes, []string{"S01E01"}) {
		t.Fatalf("episode: %d %+v", code, ep.Versions)
	}
	ep = EpisodeDetail{}
	if code := o.jsonCall("GET", APIBase+"/titles/4/episodes/41", nil, uc, "", &ep); code != 200 || len(ep.Versions) != 1 ||
		ep.Versions[0].File != nil {
		t.Fatalf("household episode: %d %+v", code, ep.Versions)
	}
	if code := o.jsonCall("POST", APIBase+"/links/901/replace", nil, uc, ucsrf, nil); code != http.StatusForbidden {
		t.Errorf("household replace: %d", code)
	}
	var got ReplaceResult
	if code := o.jsonCall("POST", APIBase+"/links/901/replace", nil, o.mc, o.mcsrf, &got); code != 200 || got.Search == nil ||
		!got.Search.Started || !strings.HasPrefix(got.Message, "Removed Alpha.mkv; the release is blocklisted in ") {
		t.Fatalf("replace: %d %+v", code, got)
	}
	if !slices.Equal(o.ops.unlinks, []int64{901}) {
		t.Errorf("unlinks %v", o.ops.unlinks)
	}
	if n := o.count(`SELECT COUNT(*) FROM router_blocklist WHERE title_id = 1 AND version_key = 'movie-remux'
		AND source = 'ui' AND release_title = 'Alpha.2020.2160p.BluRay.REMUX-GRP'`); n != 1 {
		t.Errorf("blocklist rows %d", n)
	}
	if n := o.count(`SELECT COUNT(*) FROM journal_action WHERE action = 'unlink_file' AND title_id = 1
		AND detail LIKE 'replacement search requested; moved to %' AND by <> ''`); n != 1 {
		t.Errorf("journal rows %d", n)
	}
	if n := o.count(`SELECT COUNT(*) FROM problem_report`); n != 0 {
		t.Errorf("reports %d", n)
	}
}
