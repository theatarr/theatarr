package web

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/store"
)

// A title every copy of which was removed is not in the library anywhere a person looks: list rows and total,
// facets, Home's count and Upcoming, the calendar (with unmonitored too), the search's library group and its TMDb
// hit, the add sheet. Its page, its removal routes and its History keep answering; Search now, interactive search,
// rename and the episode list answer as for a title never added. The removal shows in History as Removed from
// library, with who and what became of its files.
func TestRemovedTitleIsGone(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	aid := o.account("admin-gone", auth.RoleAdmin, auth.StatusActive, false)
	ac, acsrf := o.session(aid)
	// Both movies have a theatrical date in the next days; Beta is the one removed.
	o.exec(`INSERT INTO title_release_date (title_id, source, country, type, date) VALUES
		(1, 'tmdb', '', 'theatrical', '2026-10-28'), (2, 'tmdb', '', 'theatrical', '2026-10-29')`)
	if code := o.jsonCall("POST", APIBase+"/titles/2/remove", RemoveRequest{}, o.mc, o.mcsrf, nil); code != 200 {
		t.Fatalf("remove: %d", code)
	}

	var page TitlePage
	o.jsonCall("GET", APIBase+"/titles?kind=movie", nil, o.mc, "", &page)
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Name != "Alpha" {
		t.Errorf("movies: total %d %+v", page.Total, page.Items)
	}
	var f TitleFacets
	o.jsonCall("GET", APIBase+"/titles/facets?kind=movie", nil, o.mc, "", &f)
	if f.Total != 1 {
		t.Errorf("facets total %d", f.Total)
	}
	var st Status
	o.jsonCall("GET", APIBase+"/status", nil, ac, "", &st)
	// Alpha, and Gamma, a series with no version yet (not removed).
	if st.Counts["title"] != 2 {
		t.Errorf("status title count %d, want 2", st.Counts["title"])
	}
	var up struct {
		Items []TitleRow `json:"items"`
	}
	o.jsonCall("GET", APIBase+"/discover/upcoming", nil, o.mc, "", &up)
	if len(up.Items) != 1 || up.Items[0].Name != "Alpha" {
		t.Errorf("upcoming %+v", up.Items)
	}
	var cal CalendarPage
	o.jsonCall("GET", APIBase+"/calendar?from=2026-10-26&to=2026-11-05&unmonitored=true", nil, o.mc, "", &cal)
	if len(cal.Items) != 1 || cal.Items[0].Title.Name != "Alpha" {
		t.Errorf("calendar with unmonitored %+v", cal.Items)
	}
	var found SearchAnswer
	o.jsonCall("GET", APIBase+"/search?q=beta&scope=library", nil, o.mc, "", &found)
	if found.LibraryTotal != 0 || len(found.Library) != 0 {
		t.Errorf("search library group %+v", found.Library)
	}
	hits := []SearchHit{{Kind: "movie", TMDbID: 102}, {Kind: "movie", TMDbID: 101}}
	if err := o.web.markInLibrary(context.Background(), hits); err != nil {
		t.Fatal(err)
	}
	if hits[0].InLibrary || hits[0].TitleID != nil || !hits[1].InLibrary {
		t.Errorf("TMDb hits: removed %+v, kept %+v", hits[0], hits[1])
	}
	var vl struct {
		Items []Version `json:"items"`
	}
	o.jsonCall("GET", APIBase+"/versions?key=movie-web", nil, o.mc, "", &vl)
	if len(vl.Items) != 1 || vl.Items[0].TitleID != 1 {
		t.Errorf("versions list: %+v", vl.Items)
	}
	var opts AddOptions
	o.jsonCall("GET", APIBase+"/add/options?title_id=2", nil, o.mc, "", &opts)
	if opts.TitleID == nil || *opts.TitleID != 2 || opts.InLibrary {
		t.Errorf("add options of the removed title: %+v", opts)
	}
	o.jsonCall("GET", APIBase+"/add/options?title_id=1", nil, o.mc, "", &opts)
	if !opts.InLibrary {
		t.Errorf("add options of a title in the library: %+v", opts)
	}

	// The page and its removal routes answer; the rest as for a title never added.
	if code := o.jsonCall("GET", APIBase+"/titles/2", nil, o.mc, "", nil); code != 200 {
		t.Errorf("title page: %d", code)
	}
	if code := o.jsonCall("GET", APIBase+"/titles/2/removal", nil, o.mc, "", nil); code != 200 {
		t.Errorf("removal scope: %d", code)
	}
	for _, c := range []struct{ method, path string }{{"GET", "/titles/2/episodes"}, {"GET", "/titles/2/rename"},
		{"POST", "/titles/2/rename"}, {"GET", "/titles/2/releases?version=movie-web"}, {"POST", "/titles/2/search"}} {
		if code := o.jsonCall(c.method, APIBase+c.path, nil, ac, acsrf, nil); code != 404 {
			t.Errorf("%s %s: %d, want 404", c.method, c.path, code)
		}
	}
	if code := o.jsonCall("GET", APIBase+"/titles/1/episodes", nil, o.mc, "", nil); code != 200 {
		t.Errorf("episodes of a title in the library: %d", code)
	}

	var hist ActivityPage
	o.jsonCall("GET", APIBase+"/activity?state=history&title_id=2", nil, ac, "", &hist)
	var got *ActivityEvent
	for i := range hist.History {
		if hist.History[i].Event == journal.EvTitleRemoved {
			got = &hist.History[i]
		}
	}
	if got == nil || got.Words != "Removed from library" || got.Version != "movie-web" || got.By != "mgr" ||
		got.Removal == nil || got.Removal.Files != journal.RemovalFilesKept || got.Reason != "By mgr. Files kept." ||
		got.Title.ID != 2 {
		t.Fatalf("history: %+v", hist.History)
	}
	// Under Deleted and removed, and as the removal's kind.
	hist = ActivityPage{}
	o.jsonCall("GET", APIBase+"/activity?state=history&events=title_removed&kinds=action", nil, ac, "", &hist)
	if len(hist.History) != 1 || hist.History[0].Title.ID != 2 {
		t.Errorf("filtered history: %+v", hist.History)
	}
}

// The Removed event's detail round-trips (journal.RemovalDetail), and reads the details recorded before it.
func TestRemovalDetail(t *testing.T) {
	t.Parallel()
	for detail, want := range map[string]journal.Removal{
		journal.RemovalDetail(journal.Removal{Files: journal.RemovalFilesKept}): {Files: journal.RemovalFilesKept},
		journal.RemovalDetail(journal.Removal{Files: journal.RemovalFilesDeleted, Cleanup: "deleted on RD; kept on AD: used by X"}): {
			Files: journal.RemovalFilesDeleted, Cleanup: "deleted on RD; kept on AD: used by X"},
		journal.RemovalDetail(journal.Removal{Files: journal.RemovalFilesPartial, Left: 2, Reason: "a hardlink: stays"}): {
			Files: journal.RemovalFilesPartial, Left: 2, Reason: "a hardlink: stays"},
		journal.RemovalDetail(journal.Removal{}): {},
		"files left linked":                      {Files: journal.RemovalFilesKept},
		// Recorded before outcomes were: whether the links went is not known.
		"its library links go to the trash next (one unlink_file entry each)": {},
	} {
		if got := journal.ParseRemovalDetail(detail); got != want {
			t.Errorf("%q: %+v, want %+v", detail, got, want)
		}
	}
	h := &journal.HistoryRow{Event: journal.EvTitleRemoved, By: "ann",
		Removal: &journal.Removal{Files: journal.RemovalFilesDeleted, Cleanup: "deleted on RD"}}
	if got := removalWords(h); got != "By ann. Files deleted. Debrid: deleted on RD." {
		t.Errorf("words %q", got)
	}
	h.Removal = &journal.Removal{Files: journal.RemovalFilesPartial, Left: 1, Reason: "no space left on device"}
	if got := removalWords(h); got != "By ann. Files partly deleted (1 left in place: no space left on device)." {
		t.Errorf("words %q", got)
	}
}

// Requests follow a removal at once: a request whose every library was removed is canceled (even with another library
// of the title left), one that asked for a library that remains stands by it, and a pending one waits.
func TestRemovalCancelsRequests(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	now := store.Millis(testNow)
	// Alpha: remux has its file (cutoff_met), web is wanted. 201 asks for web alone, 202 for both, 203 is pending.
	o.exec(`INSERT INTO request (id, user_id, kind, title_id, name, versions, status, decided_at, created_at, updated_at)
		VALUES (201, 1, 'movie', 1, 'Alpha', '["movie-web"]', 'approved', ?, ?, ?),
		(202, 1, 'movie', 1, 'Alpha', '["movie-remux","movie-web"]', 'approved', ?, ?, ?),
		(203, 1, 'movie', 1, 'Alpha', '["movie-web"]', 'pending', NULL, ?, ?)`, now, now, now, now, now, now, now, now)
	var events []RequestEvent
	o.web.onRequest = func(ev RequestEvent) { events = append(events, ev) }
	if code := o.jsonCall("POST", APIBase+"/titles/1/remove", RemoveRequest{Versions: []string{"movie-web"}}, o.mc,
		o.mcsrf, nil); code != 200 {
		t.Fatalf("remove: %d", code)
	}
	// Both moves are the removal's: no file arrived, so nothing says Now available (no toast, no webhook).
	for _, ev := range events {
		if ev.Change != "removed" || ev.Notification != nil {
			t.Errorf("event %+v", ev)
		}
	}
	if len(events) != 2 {
		t.Errorf("events %+v", events)
	}
	var by sql.NullInt64
	var at int64
	if err := o.st.Reader().QueryRow(`SELECT decided_by, decided_at FROM request WHERE id = 201`).Scan(&by, &at); err != nil ||
		by.Valid || at != store.Millis(testNow) {
		t.Errorf("cancel attributed to %v at %d (%v), want the removal", by, at, err)
	}
	status := func(id int) (s, reason string) {
		if err := o.st.Reader().QueryRow(`SELECT status, reason FROM request WHERE id = ?`, id).Scan(&s, &reason); err != nil {
			t.Fatal(err)
		}
		return s, reason
	}
	if s, r := status(201); s != ReqCanceled || r != RequestRemovedReason {
		t.Errorf("request for the removed library: %s %q", s, r)
	}
	// The remux library remains with its file: what is left of the request is available.
	if s, _ := status(202); s != ReqAvailable {
		t.Errorf("request with a library left: %s", s)
	}
	if s, _ := status(203); s != ReqPending {
		t.Errorf("pending request: %s", s)
	}
	// The removed library shows no state on the request that remains.
	var rs RequestPage
	o.jsonCall("GET", APIBase+"/requests?status=available", nil, o.mc, "", &rs)
	if len(rs.Items) != 1 || rs.Items[0].ID != 202 {
		t.Fatalf("available requests: %+v", rs.Items)
	}
	// The removed library is no longer part of the request that remains.
	if vs := rs.Items[0].Versions; len(vs) != 1 || vs[0].Key != "movie-remux" || !vs[0].Available {
		t.Errorf("request 202 libraries: %+v", vs)
	}
	// The scope is stored: 202 asks for remux alone now.
	var vs string
	if err := o.st.Reader().QueryRow(`SELECT versions FROM request WHERE id = 202`).Scan(&vs); err != nil ||
		vs != `["movie-remux"]` {
		t.Errorf("202 versions %s %v", vs, err)
	}
	// 202 still waiting for remux (an approved request that would have taken web in): asking for the removed library
	// again is a new request, not one absorbed by 202.
	o.exec(`UPDATE version SET state = 'wanted' WHERE id = 1`)
	o.exec(`UPDATE request SET status = 'approved' WHERE id = 202`)
	o.exec(`DELETE FROM request WHERE id = 203`)
	var cr CreateResult
	if code := o.jsonCall("POST", APIBase+"/requests", CreateRequest{Kind: "movie", TitleID: 1,
		Versions: []string{"movie-web"}}, o.mc, o.mcsrf, &cr); code != 200 && code != 201 {
		t.Fatalf("request again: %d", code)
	}
	if cr.Request.ID == 202 || cr.Request.ID == 201 {
		t.Errorf("merged into %d", cr.Request.ID)
	}
	// Adding web back does not bring it into 202 again.
	if code := o.jsonCall("PUT", APIBase+"/titles/1/versions/movie-web", map[string]bool{"monitored": true}, o.mc,
		o.mcsrf, nil); code != 200 {
		t.Fatalf("add web again: %d", code)
	}
	if err := o.st.Reader().QueryRow(`SELECT versions FROM request WHERE id = 202`).Scan(&vs); err != nil ||
		vs != `["movie-remux"]` {
		t.Errorf("202 versions after the add %s %v", vs, err)
	}
}

// A title every copy of which was removed is added like a new one: the new title's choices (series type, monitoring,
// anime) are taken, not refused, and the add restores it by its title id. A title in the library still refuses them.
func TestReAddRemovedTitleTakesNewTitleChoices(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	aid := o.account("admin-readd", auth.RoleAdmin, auth.StatusActive, false)
	ac, acsrf := o.session(aid)
	o.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
		(9, 3, 'tv-2160p', 0, 'wanted', 0, 0)`)
	o.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (9, 'ui', 0)`)
	body := map[string]any{"title_id": 3, "kind": "series", "versions": []string{"tv-2160p"}, "series_type": "daily",
		"monitor": "all", "anime": false}
	if code := o.jsonCall("POST", APIBase+"/titles", body, ac, acsrf, nil); code != 200 && code != 201 {
		t.Fatalf("re-add: %d", code)
	}
	var typ string
	if err := o.st.Reader().QueryRow(`SELECT COALESCE(series_type, '') FROM title WHERE id = 3`).Scan(&typ); err != nil ||
		typ != "daily" {
		t.Errorf("series type %q %v", typ, err)
	}
	if n := o.count(`SELECT COUNT(*) FROM arrview_removed_version WHERE version_id = 9`); n != 0 {
		t.Errorf("still removed")
	}
	// A removed film once anime, added back as not anime: the options and the add go by that choice.
	o.exec(`UPDATE title SET anime = 1 WHERE id = 2`)
	o.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (3, 'ui', 0)`)
	var opts AddOptions
	o.jsonCall("GET", APIBase+"/add/options?title_id=2&anime=0", nil, ac, "", &opts)
	for _, v := range opts.Versions {
		if v.Default && v.Anime != nil && *v.Anime {
			t.Errorf("anime library offered: %+v", v)
		}
	}
	if code := o.jsonCall("POST", APIBase+"/titles", map[string]any{"title_id": 2, "kind": "movie", "anime": false},
		ac, acsrf, nil); code != 200 && code != 201 {
		t.Fatalf("re-add film: %d", code)
	}
	if n := o.count(`SELECT COUNT(*) FROM version WHERE title_id = 2 AND policy_key = 'anime-movie'`); n != 0 {
		t.Errorf("an anime library was chosen")
	}
	if n := o.count(`SELECT COUNT(*) FROM title WHERE id = 2 AND anime = 0`); n != 1 {
		t.Errorf("anime flag kept")
	}
	// Now in the library: a monitor preset would rewrite its copies' monitoring.
	if code := o.jsonCall("POST", APIBase+"/titles", body, ac, acsrf, nil); code != 400 {
		t.Errorf("add to a title in the library with monitor: %d", code)
	}
}

// A copy removed from a title that stays in another library is not offered, searched, grabbed or added to: the
// releases picker lists the current copies, and naming the removed one is refused.
func TestRemovedCopyIsNotActionable(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	aid := o.account("admin-copy", auth.RoleAdmin, auth.StatusActive, false)
	ac, acsrf := o.session(aid)
	o.activate("movie-web")
	if code := o.jsonCall("POST", APIBase+"/titles/1/remove", RemoveRequest{Versions: []string{"movie-web"}}, o.mc,
		o.mcsrf, nil); code != 200 {
		t.Fatalf("remove copy: %d", code)
	}
	var rel Releases
	if code := o.jsonCall("GET", APIBase+"/titles/1/releases", nil, o.mc, "", &rel); code != 200 ||
		strings.Join(rel.Versions, ",") != "movie-remux" {
		t.Errorf("picker: %d %v", code, rel.Versions)
	}
	hash := strings.Repeat("a", 40)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/titles/1/releases?version=movie-web&live=1", nil},
		{"POST", "/titles/1/releases/grab", GrabRequest{Version: "movie-web", InfoHash: hash}},
		{"POST", "/titles/1/releases/add", AddReleaseRequest{Version: "movie-web", InfoHash: hash, Name: "Alpha.2020.WEB"}},
		{"POST", "/titles/1/search", map[string]any{"versions": []string{"movie-web"}}},
	} {
		if code := o.jsonCall(c.method, APIBase+c.path, c.body, ac, acsrf, nil); code != 400 {
			t.Errorf("%s %s: %d, want 400", c.method, c.path, code)
		}
	}
	if len(o.ops.grabs) != 0 {
		t.Errorf("grabbed %+v", o.ops.grabs)
	}
}

// A broken symlink of a removed copy is deleted with the others (J4b): the removal that deletes the files leaves none.
func TestRemovalDeletesBrokenLinks(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	now := store.Millis(testNow)
	o.exec(`INSERT INTO link (id, path, target, kind, title_id, state, created_at, updated_at) VALUES
		(5, '/lib/Beta/a.mkv', '/mnt/zurg/a.mkv', 'symlink', 2, 'active', ?, ?),
		(7, '/lib/Beta/b.mkv', '/mnt/zurg/b.mkv', 'symlink', 2, 'dead', ?, ?)`, now, now, now, now)
	o.exec(`INSERT INTO link_version (link_id, version_id) VALUES (5, 3), (7, 3)`)
	o.exec(`DELETE FROM link WHERE id = 902`) // the seed's file: this test's links are the files
	if code := o.jsonCall("POST", APIBase+"/titles/2/remove", RemoveRequest{Unlink: true}, o.mc, o.mcsrf, nil); code != 200 {
		t.Fatalf("remove: %d", code)
	}
	o.web.cleanups.Wait()
	acts := o.actions()
	if len(acts) == 0 || acts[0].Action != journal.ActRemoveTitle {
		t.Fatalf("actions %+v", acts)
	}
	if rm := journal.ParseRemovalDetail(acts[0].Detail); rm.Files != journal.RemovalFilesDeleted || rm.Left != 0 {
		t.Errorf("outcome %q", acts[0].Detail)
	}
	if !slices.Equal(o.ops.unlinks, []int64{5, 7}) {
		t.Errorf("unlinked %v", o.ops.unlinks)
	}
}

// The arr views' DELETE tells TitleRemoved: the title's requests follow as after the web UI's removal.
func TestTitleRemovedFollowsArrDelete(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	now := store.Millis(testNow)
	o.exec(`INSERT INTO request (id, user_id, kind, title_id, name, versions, status, decided_at, created_at, updated_at)
		VALUES (301, 1, 'movie', 2, 'Beta', '["movie-web"]', 'available', ?, ?, ?)`, now, now, now)
	o.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (3, 'radarr', 0)`)
	o.web.TitleRemoved(context.Background(), 2)
	var s, reason string
	if err := o.st.Reader().QueryRow(`SELECT status, reason FROM request WHERE id = 301`).Scan(&s, &reason); err != nil ||
		s != ReqCanceled || reason != RequestRemovedReason {
		t.Errorf("request after an arr delete: %s %q %v", s, reason, err)
	}
}
