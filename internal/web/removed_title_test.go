package web

import (
	"context"
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
		"files left linked": {Files: journal.RemovalFilesKept},
		"its library links go to the trash next (one unlink_file entry each)": {Files: journal.RemovalFilesDeleted},
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
	if code := o.jsonCall("POST", APIBase+"/titles/1/remove", RemoveRequest{Versions: []string{"movie-web"}}, o.mc,
		o.mcsrf, nil); code != 200 {
		t.Fatalf("remove: %d", code)
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
	for _, r := range rs.Items {
		for _, v := range r.Versions {
			if v.Key == "movie-web" && (v.State != "" || v.Available) {
				t.Errorf("request %d, removed library: %+v", r.ID, v)
			}
		}
	}
}
