package web

import (
	"context"
	"database/sql"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/activity"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/notify"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// user makes an active account with a role and returns its id and an API key of its own.
func (e *addEnv) user(name, role string) (int64, string) {
	e.t.Helper()
	now := store.Millis(addNow)
	res, err := e.st.Writer().Exec(`INSERT INTO web_user (username, password_hash, created_at, updated_at,
		password_changed_at, role) VALUES (?, '', ?, ?, ?, ?)`, name, now, now, now, role)
	if err != nil {
		e.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	key, _, err := e.au.CreateKey(context.Background(), "k", id)
	if err != nil {
		e.t.Fatal(err)
	}
	return id, key
}

func (e *addEnv) callAs(key, method, path string, body any, want int, out any) {
	e.t.Helper()
	saved := e.key
	e.key = key
	defer func() { e.key = saved }()
	e.call(method, path, body, want, out)
}

func keysOf(r Request) []string {
	var out []string
	for _, v := range r.Versions {
		out = append(out, v.Key)
	}
	return out
}

func (e *addEnv) requestStatus(id int64) string {
	e.t.Helper()
	var s string
	if err := e.st.Reader().QueryRow(`SELECT status FROM request WHERE id = ?`, id).Scan(&s); err != nil {
		e.t.Fatal(err)
	}
	return s
}

// TestRequestFlow: a user requests, a second user's request merges, a user cannot approve or see others', a manager
// approves, the title appears with the requested versions wanted (through catalog.Add, so as POST /titles would),
// nothing searches in shadow, and the request follows its versions to available.
func TestRequestFlow(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	_, ann := e.user("ann", auth.RoleUser)
	_, bob := e.user("bob", auth.RoleUser)
	_, cat := e.user("cat", auth.RoleUser)
	mgrID, mgr := e.user("mgr", auth.RoleManager)

	var cr CreateResult
	e.callAs(ann, "POST", APIBase+"/requests", map[string]any{"kind": "movie", "tmdb_id": 105, "name": "Epsilon",
		"year": 2025, "poster_path": "/abcdefghij12.jpg", "note": "please"}, 201, &cr)
	r := cr.Request
	if r.Status != ReqPending || cr.Merged || cr.Approved || !slices.Equal(keysOf(r), []string{"movie-remux", "movie-web"}) ||
		r.Poster != "/img/tmdb/w342/abcdefghij12.jpg" || !r.Mine || r.TitleID != nil {
		t.Fatalf("created: %+v", cr)
	}
	// A users' requests can't approve; they see only their own.
	e.callAs(ann, "POST", fmt.Sprintf("%s/requests/%d/approve", APIBase, r.ID), nil, 403, nil)
	e.callAs(cat, "GET", fmt.Sprintf("%s/requests/%d", APIBase, r.ID), nil, 404, nil)
	var page RequestPage
	e.callAs(cat, "GET", APIBase+"/requests?mine=0", nil, 200, &page)
	if page.Total != 0 {
		t.Errorf("cat sees %d requests", page.Total)
	}
	// Bob asks for the same title: merged.
	e.callAs(bob, "POST", APIBase+"/requests", map[string]any{"kind": "movie", "tmdb_id": 105}, 200, &cr)
	if !cr.Merged || cr.Request.ID != r.ID || !cr.Request.Mine {
		t.Fatalf("merge: %+v", cr)
	}
	e.callAs(mgr, "GET", fmt.Sprintf("%s/requests/%d", APIBase, r.ID), nil, 200, &r)
	if r.Others != 1 || len(r.OtherUser) != 1 || r.OtherUser[0].Username != "bob" || r.User.Username != "ann" {
		t.Errorf("merged request: %+v", r)
	}
	var sum RequestSummary
	e.callAs(mgr, "GET", APIBase+"/requests/summary", nil, 200, &sum)
	if sum.Pending != 1 || sum.Badge != 1 {
		t.Errorf("manager summary %+v", sum)
	}
	e.callAs(bob, "GET", APIBase+"/requests/summary", nil, 200, &sum)
	if sum.Pending != 0 || sum.MineOpen != 1 || sum.Badge != 1 {
		t.Errorf("user summary %+v", sum)
	}

	// The manager approves: the title appears with both versions wanted; nothing searched (shadow).
	var ap ApproveResult
	e.callAs(mgr, "POST", fmt.Sprintf("%s/requests/%d/approve", APIBase, r.ID), map[string]any{}, 200, &ap)
	if ap.Add == nil || !ap.Add.Created || ap.Request.Status != ReqApproved || ap.Request.TitleID == nil ||
		ap.Request.DecidedBy == nil || ap.Request.DecidedBy.ID != mgrID {
		t.Fatalf("approve: %+v", ap)
	}
	if ap.Add.Search == nil || ap.Add.Search.Started || len(searchCalls(e)) != 0 {
		t.Fatalf("approval searched in shadow: %+v", ap.Add.Search)
	}
	tid := *ap.Request.TitleID
	// Paused: the first search is kept for when Pause is off, as the request sheet promises.
	if want := fmt.Sprintf("%d:movie-remux,movie-web:[]", tid); !slices.Equal(e.acq.resume, []string{want}) ||
		!strings.Contains(ap.Add.Search.Message, "when Pause is turned off") {
		t.Fatalf("paused approval: kept %v, %q", e.acq.resume, ap.Add.Search.Message)
	}
	var states []string
	scanAll(context.Background(), e.st.Reader(), `SELECT policy_key || ':' || state || ':' || monitored FROM version
		WHERE title_id = ? ORDER BY policy_key`, []any{tid}, func(rows *sql.Rows) error {
		var s string
		rows.Scan(&s)
		states = append(states, s)
		return nil
	})
	if !slices.Equal(states, []string{"movie-remux:wanted:1", "movie-web:wanted:1"}) {
		t.Fatalf("versions after approval: %v", states)
	}
	e.callAs(mgr, "POST", fmt.Sprintf("%s/requests/%d/approve", APIBase, r.ID), nil, 409, nil)

	// A download for movie-web: processing. Both filled: available.
	now := store.Millis(addNow)
	var vweb int64
	e.st.Reader().QueryRow(`SELECT id FROM version WHERE title_id = ? AND policy_key = 'movie-web'`, tid).Scan(&vweb)
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at) VALUES (?, ?, 'movie-web', 'h1', 'Epsilon.2025.2160p.WEB', 'x', 'downloading', ?, ?)`,
		tid, vweb, now, now)
	if err := e.web.syncRequests(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := e.requestStatus(r.ID); s != ReqProcessing {
		t.Errorf("with a download: %s", s)
	}
	// Ended before any provider add because downloads were off: not a failure, the request stays approved.
	e.exec(`UPDATE download SET state = 'failed', error = ?, updated_at = ?`,
		"route ended: "+router.ErrDisabled.Error(), now+1)
	e.web.syncRequests(context.Background())
	if s := e.requestStatus(r.ID); s != ReqApproved {
		t.Errorf("download not started: %s", s)
	}
	// The same text on a download a provider already refused (its retry was due when downloads went off): failed.
	e.exec(`INSERT INTO download_attempt (download_id, provider, outcome, code, attempted_at)
		SELECT id, 'rd', 'refused', 'too_many_active', ? FROM download`, now)
	e.web.syncRequests(context.Background())
	if s := e.requestStatus(r.ID); s != ReqFailed {
		t.Errorf("tried download: %s", s)
	}
	e.exec(`UPDATE download SET error = NULL`)
	e.exec(`UPDATE download SET state = 'failed', updated_at = ?`, now+1)
	e.web.syncRequests(context.Background())
	if s := e.requestStatus(r.ID); s != ReqFailed {
		t.Errorf("download failed: %s", s)
	}
	// A candidate every provider missed before any took it is not a failed download: the request stays approved.
	e.exec(`UPDATE download SET error = ?`, router.MissOnEveryProvider)
	e.web.syncRequests(context.Background())
	if s := e.requestStatus(r.ID); s != ReqApproved {
		t.Errorf("a miss on every provider: %s", s)
	}
	e.exec(`UPDATE version SET state = 'filled' WHERE title_id = ?`, tid)
	e.web.syncRequests(context.Background())
	if s := e.requestStatus(r.ID); s != ReqAvailable {
		t.Errorf("both filled: %s", s)
	}
	// The user sees availability, not who else asked.
	var mine Request
	e.callAs(ann, "GET", fmt.Sprintf("%s/requests/%d", APIBase, r.ID), nil, 200, &mine)
	if mine.Status != ReqAvailable || !mine.Versions[0].Available || !mine.Versions[1].Available || mine.OtherUser != nil {
		t.Errorf("user view: %+v", mine)
	}
	// A request for something the library now has with those versions: refused.
	e.callAs(cat, "POST", APIBase+"/requests", map[string]any{"kind": "movie", "tmdb_id": 105}, 409, nil)
}

// TestRequestVersionsAndAutoApprove: an admin's per-user request set, a kind the set does not cover, a title the
// library has (only the versions it lacks), auto-approve, decline and cancel.
func TestRequestVersionsAndAutoApprove(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	ctx := context.Background()
	annID, ann := e.user("ann", auth.RoleUser)
	_, mgr := e.user("mgr", auth.RoleManager)
	web := []string{"movie-web"}
	if err := e.au.UpdateUser(ctx, annID, auth.UserPatch{RequestVersions: &web}); err != nil {
		t.Fatal(err)
	}
	var cr CreateResult
	e.callAs(ann, "POST", APIBase+"/requests", map[string]any{"kind": "movie", "tmdb_id": 106, "name": "Zeta"}, 201, &cr)
	if !slices.Equal(keysOf(cr.Request), web) {
		t.Errorf("per-user set: %v", keysOf(cr.Request))
	}
	// Users cannot pick versions; the set decides.
	e.callAs(ann, "POST", APIBase+"/requests", map[string]any{"kind": "movie", "tmdb_id": 107, "name": "Eta",
		"versions": []string{"movie-remux"}}, 201, &cr)
	if !slices.Equal(keysOf(cr.Request), web) {
		t.Errorf("user-picked versions honoured: %v", keysOf(cr.Request))
	}
	// A series: the set has no series version, so every one that fits.
	e.callAs(ann, "POST", APIBase+"/requests", map[string]any{"kind": "series", "tmdb_id": 777, "tvdb_id": 7770,
		"name": "Alpha Anime", "anime": false, "seasons": []int{2, 1, 1}}, 201, &cr)
	if !slices.Equal(keysOf(cr.Request), []string{"tv-2160p"}) || !slices.Equal(cr.Request.Seasons, []int{1, 2}) {
		t.Errorf("series request: %v %v", keysOf(cr.Request), cr.Request.Seasons)
	}
	series := cr.Request.ID
	// Alpha (title 1) has movie-remux: a request asks for movie-web only, with the title's own name and ids.
	if err := e.au.UpdateUser(ctx, annID, auth.UserPatch{RequestVersions: new([]string)}); err != nil {
		t.Fatal(err)
	}
	e.callAs(ann, "POST", APIBase+"/requests", map[string]any{"title_id": 1}, 201, &cr)
	if !slices.Equal(keysOf(cr.Request), []string{"movie-web"}) || cr.Request.Name != "Alpha" ||
		cr.Request.TitleID == nil || *cr.Request.TitleID != 1 {
		t.Errorf("library title: %+v", cr.Request)
	}
	alpha := cr.Request.ID
	// Decline with a reason; the user sees it.
	var r Request
	e.callAs(mgr, "POST", fmt.Sprintf("%s/requests/%d/decline", APIBase, alpha), map[string]any{"reason": "no 4K web yet"},
		200, &r)
	e.callAs(ann, "GET", fmt.Sprintf("%s/requests/%d", APIBase, alpha), nil, 200, &r)
	if r.Status != ReqDeclined || r.Reason != "no 4K web yet" {
		t.Errorf("declined: %+v", r)
	}
	// Approve the series with an edit: seasons 1 only.
	var ap ApproveResult
	e.callAs(mgr, "POST", fmt.Sprintf("%s/requests/%d/approve", APIBase, series), map[string]any{"seasons": []int{1}},
		200, &ap)
	if !slices.Equal(ap.Request.Seasons, []int{1}) || ap.Add == nil {
		t.Errorf("series approval: %+v", ap.Request)
	}
	// Cancel own pending (Zeta); a declined one can't be canceled.
	var page RequestPage
	e.callAs(ann, "GET", APIBase+"/requests?status=pending", nil, 200, &page)
	if page.Total != 2 {
		t.Fatalf("pending: %+v", page)
	}
	e.callAs(ann, "DELETE", fmt.Sprintf("%s/requests/%d", APIBase, page.Items[0].ID), nil, 204, nil)
	e.callAs(ann, "DELETE", fmt.Sprintf("%s/requests/%d", APIBase, alpha), nil, 409, nil)
	// Auto-approve: applied at once.
	if err := e.au.UpdateUser(ctx, annID, auth.UserPatch{AutoApprove: new(bool)}); err != nil {
		t.Fatal(err)
	}
	yes := true
	e.au.UpdateUser(ctx, annID, auth.UserPatch{AutoApprove: &yes})
	e.callAs(ann, "POST", APIBase+"/requests", map[string]any{"kind": "movie", "tmdb_id": 108, "name": "Theta"}, 201, &cr)
	if !cr.Approved || cr.Request.Status != ReqApproved || cr.Request.TitleID == nil {
		t.Errorf("auto-approve: %+v", cr)
	} else if want := fmt.Sprintf("%d:movie-remux,movie-web:[]", *cr.Request.TitleID); !slices.Contains(e.acq.resume, want) {
		t.Errorf("paused auto-approval kept no first search: %v", e.acq.resume)
	}
	// A manager's own request is approved at once (managers auto-approve) and may name versions.
	e.callAs(mgr, "POST", APIBase+"/requests", map[string]any{"kind": "movie", "tmdb_id": 109, "name": "Iota",
		"versions": []string{"movie-remux"}}, 201, &cr)
	if !cr.Approved || !slices.Equal(keysOf(cr.Request), []string{"movie-remux"}) {
		t.Errorf("manager request: %+v", cr)
	}
}

// TestAutoApproveJoinsPendingRequest: an auto-approve account whose ask joins a pending request approves it, as the
// decision maker, when the request asks for nothing beyond its ask; a smaller ask leaves it pending
// and says why; a plain user's join and a join onto an approved request change nothing. The first requester (a
// member) is not shown the joiner's name as decider; a manager is.
func TestAutoApproveJoinsPendingRequest(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	ctx := context.Background()
	annID, ann := e.user("ann", auth.RoleUser)
	_, bob := e.user("bob", auth.RoleUser)
	catID, cat := e.user("cat", auth.RoleUser)
	danID, dan := e.user("dan", auth.RoleUser)
	eveID, eve := e.user("eve", auth.RoleUser)
	mgrID, mgr := e.user("mgr", auth.RoleManager)
	yes, web := true, []string{"movie-web"}
	for _, id := range []int64{catID, danID, eveID} {
		if err := e.au.UpdateUser(ctx, id, auth.UserPatch{AutoApprove: &yes}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.au.UpdateUser(ctx, catID, auth.UserPatch{RequestVersions: &web}); err != nil {
		t.Fatal(err)
	}
	// ask decodes into a fresh result each time: not_approved is omitted when empty.
	ask := func(key string, body map[string]any, want int) CreateResult {
		t.Helper()
		var cr CreateResult
		e.callAs(key, "POST", APIBase+"/requests", body, want, &cr)
		return cr
	}
	cr := ask(ann, map[string]any{"kind": "movie", "tmdb_id": 105, "name": "Epsilon"}, 201)
	id := cr.Request.ID
	if cr.Approved || !slices.Equal(keysOf(cr.Request), []string{"movie-remux", "movie-web"}) {
		t.Fatalf("created: %+v", cr)
	}
	// A plain user joining: merged, pending, no reason.
	cr = ask(bob, map[string]any{"kind": "movie", "tmdb_id": 105}, 200)
	if !cr.Merged || cr.Approved || cr.NotApproved != "" || e.requestStatus(id) != ReqPending {
		t.Fatalf("plain join: %+v", cr)
	}
	// Cat (auto-approve) asks for movie-web only: the request asks for more, so it stays pending, with the reason.
	cr = ask(cat, map[string]any{"kind": "movie", "tmdb_id": 105}, 200)
	if !cr.Merged || cr.Approved || cr.NotApproved != NotApprovedAsksMore || e.requestStatus(id) != ReqPending ||
		!slices.Equal(keysOf(cr.Request), []string{"movie-remux", "movie-web"}) {
		t.Fatalf("smaller auto-approve join: %+v", cr)
	}
	// Dan (auto-approve) asks for the same: approved, with Dan as the decision maker.
	cr = ask(dan, map[string]any{"kind": "movie", "tmdb_id": 105}, 200)
	if !cr.Merged || !cr.Approved || cr.NotApproved != "" || e.requestStatus(id) != ReqApproved ||
		cr.Request.DecidedBy == nil || cr.Request.DecidedBy.ID != danID {
		t.Fatalf("same-ask auto-approve join: %+v", cr)
	}
	var r Request
	e.callAs(mgr, "GET", fmt.Sprintf("%s/requests/%d", APIBase, id), nil, 200, &r)
	if r.DecidedBy == nil || r.DecidedBy.ID != danID {
		t.Errorf("manager's view of the decider: %+v", r.DecidedBy)
	}
	// The first requester and another joiner see no name for Dan's approval, in the detail and the list.
	for _, k := range []string{ann, bob} {
		var d RequestDetail
		e.callAs(k, "GET", fmt.Sprintf("%s/requests/%d", APIBase, id), nil, 200, &d)
		var page RequestPage
		e.callAs(k, "GET", APIBase+"/requests", nil, 200, &page)
		if d.DecidedBy != nil || len(page.Items) != 1 || page.Items[0].DecidedBy != nil || d.Status != ReqApproved {
			t.Errorf("a member sees the joiner who approved: detail %+v, list %+v", d.DecidedBy, page.Items)
		}
	}
	// Joining an approved request (its title not in the library yet) has nothing to approve.
	e.exec(`INSERT INTO request (id, user_id, kind, tmdb_id, name, versions, status, decided_by, decided_at, created_at,
		updated_at) VALUES (50, ?, 'movie', 110, 'Kappa', '["movie-remux","movie-web"]', 'approved', ?, ?, ?, ?)`,
		annID, mgrID, store.Millis(addNow), store.Millis(addNow), store.Millis(addNow))
	cr = ask(eve, map[string]any{"kind": "movie", "tmdb_id": 110}, 200)
	if !cr.Merged || cr.Approved || cr.NotApproved != "" || cr.Request.ID != 50 {
		t.Errorf("join onto an approved request: %+v", cr)
	}
	e.callAs(mgr, "GET", APIBase+"/requests/50", nil, 200, &r)
	if r.DecidedBy == nil || r.DecidedBy.ID != mgrID || r.Status != ReqApproved {
		t.Errorf("decider after a later join: %+v", r)
	}

	// A join that grows a pending request to exactly the joiner's ask approves it.
	if err := e.au.UpdateUser(ctx, annID, auth.UserPatch{RequestVersions: &web}); err != nil {
		t.Fatal(err)
	}
	cr = ask(ann, map[string]any{"kind": "movie", "tmdb_id": 106, "name": "Zeta"}, 201)
	cr = ask(dan, map[string]any{"kind": "movie", "tmdb_id": 106}, 200)
	if !cr.Merged || !cr.Approved || !slices.Equal(keysOf(cr.Request), []string{"movie-remux", "movie-web"}) {
		t.Errorf("grown join: %+v", cr)
	}

	// What refuses a new auto-approved request refuses a join the same way: Alpha has movie-remux already.
	remux := []string{"movie-remux"}
	if err := e.au.UpdateUser(ctx, danID, auth.UserPatch{RequestVersions: &remux}); err != nil {
		t.Fatal(err)
	}
	cr = ask(ann, map[string]any{"title_id": 1}, 201)
	e.callAs(dan, "POST", APIBase+"/requests", map[string]any{"title_id": 1}, 409, nil)
	if s := e.requestStatus(cr.Request.ID); s != ReqPending {
		t.Errorf("refused join changed the request: %s", s)
	}
}

// TestAutoApproveJoinSeasons: for a series, an auto-approve join approves the pending request only when its seasons
// are within the joiner's: the same seasons approve, fewer stay pending (asks_more), and a pending request for every
// season (no seasons named) is covered only by an ask for every season.
func TestAutoApproveJoinSeasons(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	ctx := context.Background()
	_, ann := e.user("ann", auth.RoleUser)
	danID, dan := e.user("dan", auth.RoleUser)
	yes := true
	if err := e.au.UpdateUser(ctx, danID, auth.UserPatch{AutoApprove: &yes}); err != nil {
		t.Fatal(err)
	}
	ask := func(key string, body map[string]any, want int) CreateResult {
		t.Helper()
		var cr CreateResult
		e.callAs(key, "POST", APIBase+"/requests", body, want, &cr)
		return cr
	}
	series := func(tmdb int, seasons []int) map[string]any {
		b := map[string]any{"kind": "series", "tmdb_id": tmdb, "tvdb_id": tmdb * 10, "name": fmt.Sprintf("Show %d", tmdb),
			"anime": false}
		if seasons != nil {
			b["seasons"] = seasons
		}
		return b
	}
	// Fewer seasons than the pending request: merged, still pending, with the reason.
	first := ask(ann, series(801, []int{1, 2}), 201)
	cr := ask(dan, series(801, []int{1}), 200)
	if !cr.Merged || cr.Approved || cr.NotApproved != NotApprovedAsksMore || cr.Request.ID != first.Request.ID ||
		!slices.Equal(cr.Request.Seasons, []int{1, 2}) || e.requestStatus(first.Request.ID) != ReqPending {
		t.Errorf("fewer seasons: %+v", cr)
	}
	// A pending request for every season: an ask naming seasons does not cover it.
	every := ask(ann, series(802, nil), 201)
	cr = ask(dan, series(802, []int{1, 2}), 200)
	if !cr.Merged || cr.Approved || cr.NotApproved != NotApprovedAsksMore || len(cr.Request.Seasons) != 0 ||
		e.requestStatus(every.Request.ID) != ReqPending {
		t.Errorf("seasons against every season: %+v", cr)
	}
	// The same seasons: approved, with Dan as the decision maker.
	same := ask(ann, series(777, []int{1, 2}), 201)
	cr = ask(dan, series(777, []int{2, 1}), 200)
	if !cr.Merged || !cr.Approved || cr.NotApproved != "" || e.requestStatus(same.Request.ID) != ReqApproved ||
		cr.Request.DecidedBy == nil || cr.Request.DecidedBy.ID != danID {
		t.Errorf("same seasons: %+v", cr)
	}
	// Every season against every season: approved.
	all := ask(ann, series(803, nil), 201)
	cr = ask(dan, series(803, nil), 200)
	if !cr.Merged || !cr.Approved || e.requestStatus(all.Request.ID) != ReqApproved {
		t.Errorf("every season both ways: %+v", cr)
	}
	// A join that grows the pending request's seasons to the joiner's ask: approved, with Dan as the decision maker.
	grow := ask(ann, series(804, []int{1}), 201)
	cr = ask(dan, series(804, []int{1, 2}), 200)
	if !cr.Merged || !cr.Approved || cr.Request.ID != grow.Request.ID || !slices.Equal(cr.Request.Seasons, []int{1, 2}) ||
		e.requestStatus(grow.Request.ID) != ReqApproved || cr.Request.DecidedBy == nil || cr.Request.DecidedBy.ID != danID {
		t.Errorf("grown to [1 2]: %+v", cr)
	}
	// A join asking every season grows the pending request to every season: approved.
	widen := ask(ann, series(805, []int{1}), 201)
	cr = ask(dan, series(805, nil), 200)
	if !cr.Merged || !cr.Approved || cr.Request.ID != widen.Request.ID || e.requestStatus(widen.Request.ID) != ReqApproved ||
		cr.Request.DecidedBy == nil || cr.Request.DecidedBy.ID != danID {
		t.Errorf("grown to every season: %+v", cr)
	}
	var seasons string
	if err := e.st.Reader().QueryRow(`SELECT seasons FROM request WHERE id = ?`, widen.Request.ID).Scan(&seasons); err != nil {
		t.Fatal(err)
	}
	if seasons != "[]" {
		t.Errorf("widened request stored seasons %s, want [] (every season)", seasons)
	}
}

// TestRequestEventsReachTheRightPeople: a request event goes to its requesters and to managers, not other users.
func TestRequestEventsReachTheRightPeople(t *testing.T) {
	t.Parallel()
	ev := event{name: "request", need: auth.PermBrowse, toUsers: true, users: []int64{5}, orPerm: auth.PermManageRequests}
	user := PermsOf(auth.RolePerms(auth.RoleUser))
	mgr := PermsOf(auth.RolePerms(auth.RoleManager))
	if !ev.For(&auth.Identity{UserID: 5}, user) || ev.For(&auth.Identity{UserID: 6}, user) || !ev.For(&auth.Identity{UserID: 6}, mgr) {
		t.Error("request event audience")
	}
	jobs := event{name: "jobs", need: auth.PermAdmin}
	if jobs.For(&auth.Identity{UserID: 6}, mgr) || !jobs.For(&auth.Identity{UserID: 1}, PermsOf(auth.PermAll)) {
		t.Error("jobs event audience")
	}
}

// TestActivityScopedToRequests: a user's activity lists the titles of their requests only, without release names.
func TestActivityScopedToRequests(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	_, ann := e.user("ann", auth.RoleUser)
	now := store.Millis(addNow)
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at) VALUES (1, 1, 'movie-remux', 'h1', 'Alpha.2020.2160p.REMUX-GRP', 'x', 'downloading', ?, ?)`,
		now, now)
	var page ActivityPage
	e.callAs(ann, "GET", APIBase+"/activity?state=now", nil, 200, &page)
	if len(page.Now) != 0 {
		t.Fatalf("user sees others' downloads: %+v", page.Now)
	}
	e.callAs(e.key, "GET", APIBase+"/activity?state=now", nil, 200, &page)
	if len(page.Now) != 1 || page.Now[0].Release == "" {
		t.Fatalf("admin view: %+v", page.Now)
	}
	// Ann requests Alpha's other version: the remux copy's download is still not hers, but a
	// download of the version she asked for is, in plain words only.
	var cr CreateResult
	e.callAs(ann, "POST", APIBase+"/requests", map[string]any{"title_id": 1}, 201, &cr)
	var up ActivityPage
	e.callAs(ann, "GET", APIBase+"/activity?state=now", nil, 200, &up)
	if len(up.Now) != 0 {
		t.Fatalf("user sees the other copy's download: %+v", up.Now)
	}
	if len(cr.Request.Versions) != 1 || cr.Request.Versions[0].Key != "movie-web" {
		t.Fatalf("Ann's request: %+v", cr.Request.Versions)
	}
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at)
		VALUES (2, 1, 'movie-web', 1, 'wanted', ?, ?)`, now, now)
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at) VALUES (1, 2, 'movie-web', 'h2', 'Alpha.2020.2160p.WEB-DL-GRP', 'x', 'downloading', ?, ?)`,
		now, now)
	e.callAs(ann, "GET", APIBase+"/activity?state=now", nil, 200, &up)
	if len(up.Now) != 1 || up.Now[0].Version != "movie-web" || up.Now[0].Release != "" || up.Now[0].Hash != "" ||
		up.Admin {
		t.Fatalf("user view: %+v", up.Now)
	}
	r := httptest.NewRequest("GET", APIBase+"/activity?state=now", nil)
	r.Header.Set(auth.APIKeyHeader, ann)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, r)
	if strings.Contains(rec.Body.String(), "REMUX-GRP") {
		t.Error("release name in a user's activity")
	}
}

// TestActivityScopedToRequestedCopies: a household user's activity (now, history, the continue row and download
// events) covers the versions and seasons of the requests they are on, not every copy of the title:
// another user's movie-web download of a title they asked for in movie-remux is hidden, as is a season
// they did not ask for (history is scoped by version only). Those who see files are unchanged.
func TestActivityScopedToRequestedCopies(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	ctx := context.Background()
	annID, ann := e.user("ann", auth.RoleUser)
	bobID, bob := e.user("bob", auth.RoleUser)
	_, mgr := e.user("mgr", auth.RoleManager)
	now := store.Millis(addNow)
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at)
		VALUES (2, 1, 'movie-web', 1, 'wanted', ?, ?)`, now, now)
	e.exec(`INSERT INTO request (user_id, kind, tmdb_id, title_id, name, versions, seasons, status, created_at,
		updated_at) VALUES (?, 'movie', 101, 1, 'Alpha', '["movie-remux"]', '[]', 'approved', ?, ?),
		(?, 'movie', 101, 1, 'Alpha', '["movie-web"]', '[]', 'approved', ?, ?),
		(?, 'series', NULL, 10, 'The Show', '["tv-2160p"]', '[2]', 'approved', ?, ?)`,
		annID, now, now, bobID, now, now, annID, now, now)
	dl := func(title, version int64, key, hash, episodes string) int64 {
		res, err := e.st.Writer().Exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title,
			indexer, episodes, state, created_at, updated_at) VALUES (?, ?, ?, ?, 'R', 'x', ?, 'downloading', ?, ?)`,
			title, version, key, hash, episodes, now, now)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		e.sqlSources = true // a fixture download: History is stored from it before it is read
		return id
	}
	remux := dl(1, 1, "movie-remux", "h1", "[]")
	web := dl(1, 2, "movie-web", "h2", "[]")
	s1 := dl(10, 10, "tv-2160p", "h3", "[101]")
	s2 := dl(10, 10, "tv-2160p", "h4", "[103]")
	ids := func(key string) []int64 {
		var p ActivityPage
		e.callAs(key, "GET", APIBase+"/activity?state=now", nil, 200, &p)
		var out []int64
		for _, d := range p.Now {
			out = append(out, d.ID)
		}
		slices.Sort(out)
		return out
	}
	if got := ids(ann); !slices.Equal(got, []int64{remux, s2}) {
		t.Errorf("ann's queue %v, want her remux copy and season 2 (%d, %d)", got, remux, s2)
	}
	if got := ids(bob); !slices.Equal(got, []int64{web}) {
		t.Errorf("bob's queue %v, want his web copy %d", got, web)
	}
	for _, k := range []string{mgr, e.key} {
		if got := ids(k); !slices.Equal(got, []int64{remux, web, s1, s2}) {
			t.Errorf("file viewer's queue %v, want every download", got)
		}
	}

	var row ContinueRow
	e.callAs(ann, "GET", APIBase+"/activity/continue", nil, 200, &row)
	got := map[int64]ContinueItem{}
	for _, it := range row.Items {
		got[it.ID] = it
	}
	if len(row.Items) != 2 || got[1].Download != remux || got[1].Downloads != 1 || got[10].Download != s2 ||
		got[10].Downloads != 1 {
		t.Errorf("ann's continue row: %+v", row.Items)
	}
	e.callAs(mgr, "GET", APIBase+"/activity/continue", nil, 200, &row)
	if len(row.Items) != 2 || row.Items[0].Downloads+row.Items[1].Downloads != 4 {
		t.Errorf("manager's continue row: %+v", row.Items)
	}
	// Each card names its library by label, not by key (Home's "Getting ready" subtitle).
	labels := map[string]string{}
	for _, it := range row.Items {
		labels[it.Version] = it.Label
	}
	if len(labels) != 2 || labels["tv-2160p"] != "4K" || labels["movie-remux"] != "" && labels["movie-remux"] != "4K Remux" ||
		labels["movie-web"] != "" && labels["movie-web"] == "movie-web" {
		t.Errorf("manager's continue row labels %v", labels)
	}

	// History: the grab of each download; by version, so both seasons of The Show.
	versions := func(key string) []string {
		var p ActivityPage
		e.callAs(key, "GET", APIBase+"/activity?state=history&kinds=grab", nil, 200, &p)
		var out []string
		for _, h := range p.History {
			out = append(out, h.Version)
		}
		slices.Sort(out)
		return out
	}
	// A household account's History has no Grabbed rows (journal.Filter.Household).
	if got := versions(ann); len(got) != 0 {
		t.Errorf("ann's history versions %v, want no Grabbed rows", got)
	}
	if got := versions(mgr); !slices.Equal(got, []string{"movie-remux", "movie-web", "tv-2160p", "tv-2160p"}) {
		t.Errorf("manager's history versions %v", got)
	}

	ds, err := activity.Downloads(ctx, e.st.Reader(), activity.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	user := PermsOf(auth.RolePerms(auth.RoleUser))
	files := PermsOf(auth.RolePerms(auth.RoleManager))
	want := map[int64][2]bool{remux: {true, false}, web: {false, true}, s1: {false, false}, s2: {true, false}}
	for i := range ds {
		d := &ds[i]
		ev, err := e.web.downloadEvent(ctx, d, []byte("{}"))
		if err != nil {
			t.Fatal(err)
		}
		w := want[d.ID]
		if ev.For(&auth.Identity{UserID: annID}, user) != w[0] || ev.For(&auth.Identity{UserID: bobID}, user) != w[1] ||
			!ev.For(&auth.Identity{UserID: annID}, files) {
			t.Errorf("download %d (%s) event audience: %+v", d.ID, d.Version, ev)
		}
	}
}

// TestAcquiringTitleEvent: a change of state of another copy's download, which a household requester's scoped
// download events leave out, still reaches them as a title event (reason acquiring, id and name only) so their
// posters and title page refetch the title-wide acquiring; progress alone sends none, and non-requesters get none.
func TestAcquiringTitleEvent(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	ctx := context.Background()
	annID, _ := e.user("ann", auth.RoleUser)
	bobID, _ := e.user("bob", auth.RoleUser)
	now := store.Millis(addNow)
	e.exec(`INSERT INTO request (user_id, kind, tmdb_id, title_id, name, versions, status, created_at, updated_at)
		VALUES (?, 'movie', 101, 1, 'Alpha', '["movie-web"]', 'pending', ?, ?)`, annID, now, now)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
		progress, created_at, updated_at) VALUES (1, 1, 1, 'movie-remux', 'h1', 'R', 'x', 'downloading', 10, ?, ?)`,
		now, now)
	ch := e.web.hub.subscribe()
	defer e.web.hub.unsubscribe(ch)
	p := &activityPoller{sent: map[int64]string{}, state: map[int64]string{}}
	user := PermsOf(auth.RolePerms(auth.RoleUser))
	poll := func() (download, acquiring []event) {
		t.Helper()
		if err := e.web.pollActivity(ctx, p); err != nil {
			t.Fatal(err)
		}
		for {
			select {
			case ev := <-ch:
				switch {
				case ev.name == "download":
					download = append(download, ev)
				case ev.name == "title" && strings.Contains(string(ev.data), `"reason":"acquiring"`):
					acquiring = append(acquiring, ev)
				}
			default:
				return download, acquiring
			}
		}
	}
	check := func(when string, acquiring []event) {
		t.Helper()
		if len(acquiring) != 1 || !acquiring[0].For(&auth.Identity{UserID: annID}, user) ||
			acquiring[0].For(&auth.Identity{UserID: bobID}, user) || string(acquiring[0].data) !=
			`{"id":1,"name":"Alpha","reason":"acquiring"}` {
			t.Errorf("%s: acquiring events %+v", when, acquiring)
		}
	}
	dl, acq := poll()
	if len(dl) != 1 || dl[0].For(&auth.Identity{UserID: annID}, user) {
		t.Errorf("the remux download event reaches ann: %+v", dl)
	}
	check("new download", acq)
	e.exec(`UPDATE download SET progress = 40, updated_at = ? WHERE id = 1`, now+1)
	if _, acq := poll(); len(acq) != 0 {
		t.Errorf("progress alone sent %+v", acq)
	}
	e.exec(`UPDATE download SET state = 'failed', updated_at = ? WHERE id = 1`, now+2)
	_, acq = poll()
	check("failed", acq)
}

// TestUserKeyRefusedByArrViews: the arr views take admin keys (and view keys) only.
func TestUserKeyRefusedByArrViews(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	_, ann := e.user("ann", auth.RoleUser)
	_, mgr := e.user("mgr", auth.RoleManager)
	for _, c := range []struct {
		key  string
		want int
	}{{e.key, 200}, {ann, 401}, {mgr, 401}} {
		r := httptest.NewRequest("GET", "/radarr/api/v3/system/status", nil)
		r.Header.Set(auth.APIKeyHeader, c.key)
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, r)
		if rec.Code != c.want {
			t.Errorf("arr view with %q's key: %d, want %d", c.key[:6], rec.Code, c.want)
		}
	}
}

// TestRequestMoreSeasons: a series the library has with a version is still requestable for the seasons that version
// does not fully monitor; approval monitors just those seasons and leaves the others as they are, and the request is
// available when the requested seasons are, not when the version was filled before.
func TestRequestMoreSeasons(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	ctx := context.Background()
	_, ann := e.user("ann", auth.RoleUser)
	_, bob := e.user("bob", auth.RoleUser)
	_, cat := e.user("cat", auth.RoleUser)
	_, mgr := e.user("mgr", auth.RoleManager)
	now := store.Millis(addNow)
	// The Show (title 10, tv-2160p): season 3 known but unmonitored, one season 1 episode unmonitored by hand.
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, monitored, air_date, created_at, updated_at) VALUES
		(104, 10, 3, 1, 'Fourth', 0, '2026-01-01', ?, ?), (105, 10, 3, 2, 'Fifth', 0, '2026-01-08', ?, ?)`,
		now, now, now, now)
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at) VALUES
		(10, 104, 0, 'wanted', ?), (10, 105, 0, 'wanted', ?)`, now, now)
	e.exec(`UPDATE episode_version_state SET monitored = 0 WHERE episode_id = 102`)
	e.exec(`UPDATE version SET state = 'cutoff_met' WHERE id = 10`)

	var opts AddOptions
	e.callAs(ann, "GET", APIBase+"/add/options?title_id=10", nil, 200, &opts)
	var v *VersionOption
	for i := range opts.Versions {
		if opts.Versions[i].Key == "tv-2160p" {
			v = &opts.Versions[i]
		}
	}
	if v == nil || !v.On || !slices.Equal(v.OpenSeasons, []int{1, 3}) {
		t.Fatalf("add options: %+v", v)
	}

	// Season 2 is fully monitored: nothing to ask for.
	e.callAs(ann, "POST", APIBase+"/requests", map[string]any{"title_id": 10, "seasons": []int{2}}, 409, nil)
	var cr CreateResult
	e.callAs(ann, "POST", APIBase+"/requests", map[string]any{"title_id": 10, "seasons": []int{3}}, 201, &cr)
	if !slices.Equal(keysOf(cr.Request), []string{"tv-2160p"}) || !slices.Equal(cr.Request.Seasons, []int{3}) {
		t.Fatalf("more seasons: %+v", cr.Request)
	}
	id := cr.Request.ID
	e.callAs(bob, "POST", APIBase+"/requests", map[string]any{"title_id": 10, "seasons": []int{3}}, 200, &cr)
	if !cr.Merged || cr.Request.ID != id {
		t.Fatalf("same season merges: %+v", cr)
	}

	var ap ApproveResult
	e.callAs(mgr, "POST", fmt.Sprintf("%s/requests/%d/approve", APIBase, id), map[string]any{}, 200, &ap)
	if ap.Request.Status != ReqApproved {
		t.Fatalf("approve: %+v", ap.Request)
	}
	mon := map[int64]int{}
	scanAll(ctx, e.st.Reader(), `SELECT episode_id, monitored FROM episode_version_state WHERE version_id = 10`, nil,
		func(rows *sql.Rows) error {
			var id int64
			var m int
			rows.Scan(&id, &m)
			mon[id] = m
			return nil
		})
	if mon[104] != 1 || mon[105] != 1 || mon[102] != 0 || mon[100] != 0 || mon[101] != 1 || mon[103] != 1 {
		t.Fatalf("monitoring after approval: %v", mon)
	}
	var epMon int
	e.st.Reader().QueryRow(`SELECT monitored FROM episode WHERE id = 104`).Scan(&epMon)
	if epMon != 1 {
		t.Error("episode 104 not monitored")
	}

	// An approved request for season 3 does not cover season 1: a new request.
	e.callAs(cat, "POST", APIBase+"/requests", map[string]any{"title_id": 10, "seasons": []int{1}}, 201, &cr)
	if cr.Merged || cr.Request.ID == id || !slices.Equal(cr.Request.Seasons, []int{1}) {
		t.Fatalf("season 1: %+v", cr)
	}

	// The version was cutoff_met before the request: the season decides.
	if err := e.web.syncRequests(ctx, id); err != nil {
		t.Fatal(err)
	}
	if s := e.requestStatus(id); s != ReqApproved {
		t.Errorf("season 3 missing: %s", s)
	}
	e.exec(`UPDATE episode_version_state SET state = 'filled' WHERE episode_id IN (104, 105)`)
	e.web.syncRequests(ctx, id)
	if s := e.requestStatus(id); s != ReqAvailable {
		t.Errorf("season 3 filled: %s", s)
	}
}

// TestRequestMoreSeasonsSearchesThem: approving more seasons of a series a library has searches the missing episodes
// of those seasons only, not upgrades of the seasons already there ("the ones already there stay as they are").
func TestRequestMoreSeasonsSearchesThem(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, func(p *policy.Policy) {
		p.Router = &policy.Router{Enabled: true}
	})
	_, ann := e.user("ann", auth.RoleUser)
	_, mgr := e.user("mgr", auth.RoleManager)
	now := store.Millis(addNow)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, monitored, air_date, created_at, updated_at) VALUES
		(104, 10, 3, 1, 'Fourth', 0, '2026-01-01', ?, ?)`, now, now)
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at) VALUES
		(10, 104, 0, 'wanted', ?)`, now)
	var cr CreateResult
	e.callAs(ann, "POST", APIBase+"/requests", map[string]any{"title_id": 10, "seasons": []int{3}}, 201, &cr)
	var ap ApproveResult
	e.callAs(mgr, "POST", fmt.Sprintf("%s/requests/%d/approve", APIBase, cr.Request.ID), map[string]any{}, 200, &ap)
	calls := searchCalls(e)
	// NoJoin: the approved libraries only, not another library of the series that a title search would join.
	if len(calls) != 1 || calls[0].Only != acquire.OnlyMissing || !slices.Equal(calls[0].Seasons, []int{3}) ||
		!slices.Equal(calls[0].TitleIDs, []int64{10}) || !calls[0].NoJoin {
		t.Fatalf("approval searched: %+v", calls)
	}
}

// TestRequestMoreSeasonsEveryOpenSeason: a request naming no seasons asks for every season (specials aside) a
// version lacks, and approval names them on the request.
func TestRequestMoreSeasonsEveryOpenSeason(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	_, ann := e.user("ann", auth.RoleUser)
	_, mgr := e.user("mgr", auth.RoleManager)
	// Only the specials are unmonitored: nothing to ask for.
	e.callAs(ann, "POST", APIBase+"/requests", map[string]any{"title_id": 10}, 409, nil)
	e.exec(`UPDATE episode_version_state SET monitored = 0 WHERE episode_id = 103`)
	var cr CreateResult
	e.callAs(ann, "POST", APIBase+"/requests", map[string]any{"title_id": 10}, 201, &cr)
	var ap ApproveResult
	e.callAs(mgr, "POST", fmt.Sprintf("%s/requests/%d/approve", APIBase, cr.Request.ID), map[string]any{}, 200, &ap)
	if !slices.Equal(ap.Request.Seasons, []int{1, 2}) {
		t.Errorf("seasons on the approved request: %v", ap.Request.Seasons)
	}
	var m100, m103 int
	e.st.Reader().QueryRow(`SELECT monitored FROM episode_version_state WHERE episode_id = 100`).Scan(&m100)
	e.st.Reader().QueryRow(`SELECT monitored FROM episode_version_state WHERE episode_id = 103`).Scan(&m103)
	if m100 != 0 || m103 != 1 {
		t.Errorf("specials %d, season 2 %d", m100, m103)
	}
}

// TestRequestJoinIsContainment: a new ask joins an open request only when that request holds all of it, grows a
// pending request it holds all of, and is otherwise a request of its own for exactly what was asked.
func TestRequestJoinIsContainment(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	ctx := context.Background()
	annID, _ := e.user("ann", auth.RoleUser)
	bobID, _ := e.user("bob", auth.RoleUser)
	type ask struct {
		keys    []string
		seasons []int
	}
	every := []int{}
	for i, c := range []struct {
		name     string
		first    ask
		approved bool
		next     ask
		joined   bool
		stored   ask // the first request after the second ask
	}{
		{"same ask twice", ask{[]string{"A"}, []int{1}}, false, ask{[]string{"A"}, []int{1}}, true, ask{[]string{"A"}, []int{1}}},
		{"grown by a library", ask{[]string{"A"}, every}, false, ask{[]string{"A", "B"}, every}, true, ask{[]string{"A", "B"}, every}},
		{"grown by a season", ask{[]string{"A"}, []int{1}}, false, ask{[]string{"A"}, []int{1, 2}}, true, ask{[]string{"A"}, []int{1, 2}}},
		{"other library, other season", ask{[]string{"A"}, []int{1}}, false, ask{[]string{"B"}, []int{2}}, false, ask{[]string{"A"}, []int{1}}},
		{"fewer libraries, other season", ask{[]string{"A", "B"}, []int{1}}, false, ask{[]string{"A"}, []int{2}}, false, ask{[]string{"A", "B"}, []int{1}}},
		{"every season holds one", ask{[]string{"A"}, every}, false, ask{[]string{"A"}, []int{2}}, true, ask{[]string{"A"}, every}},
		{"approved covers the ask", ask{[]string{"A", "B"}, every}, true, ask{[]string{"A"}, []int{1}}, true, ask{[]string{"A", "B"}, every}},
		{"approved does not cover the ask", ask{[]string{"A"}, []int{1}}, true, ask{[]string{"A"}, []int{1, 2}}, false, ask{[]string{"A"}, []int{1}}},
	} {
		n := newRequest{kind: "series", tmdbID: int64(900 + i), tvdbID: int64(9000 + i), name: c.name}
		n.keys, n.seasons = c.first.keys, c.first.seasons
		first, _, err := e.web.createRequest(ctx, annID, n)
		if err != nil {
			t.Fatal(err)
		}
		if c.approved {
			e.exec(`UPDATE request SET status = 'approved' WHERE id = ?`, first.Request.ID)
		}
		n.keys, n.seasons = c.next.keys, c.next.seasons
		next, _, err := e.web.createRequest(ctx, bobID, n)
		if err != nil {
			t.Fatal(err)
		}
		if next.Merged != c.joined || (next.Request.ID == first.Request.ID) != c.joined {
			t.Errorf("%s: merged %v into %d, first %d", c.name, next.Merged, next.Request.ID, first.Request.ID)
		}
		got := func(id int64) string {
			var vs, ss string
			if err := e.st.Reader().QueryRow(`SELECT versions, seasons FROM request WHERE id = ?`, id).Scan(&vs, &ss); err != nil {
				t.Fatal(err)
			}
			return vs + " " + ss
		}
		if want := jsonText(c.stored.keys) + " " + jsonText(c.stored.seasons); got(first.Request.ID) != want {
			t.Errorf("%s: first request %s, want %s", c.name, got(first.Request.ID), want)
		}
		if !c.joined {
			if want := jsonText(c.next.keys) + " " + jsonText(c.next.seasons); got(next.Request.ID) != want {
				t.Errorf("%s: new request %s, want %s", c.name, got(next.Request.ID), want)
			}
		}
	}
}

// TestOverlappingRequestsApproveApart: {A: S1} and {B: S2} are two requests; approved one after the other they end
// with exactly season 1 monitored in A and season 2 in B. While both are open, a failed season 1 download fails the
// season 1 request only, and an S1 download in progress moves only that request along.
func TestOverlappingRequestsApproveApart(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, func(p *policy.Policy) {
		for _, k := range []string{"tv-a", "tv-b"} {
			v := *p.Versions["tv-2160p"]
			p.Versions[k] = &v
		}
	})
	ctx := context.Background()
	annID, ann := e.user("ann", auth.RoleUser)
	bobID, bob := e.user("bob", auth.RoleUser)
	_, mgr := e.user("mgr", auth.RoleManager)
	for id, k := range map[int64]string{annID: "tv-a", bobID: "tv-b"} {
		keys := []string{k}
		if err := e.au.UpdateUser(ctx, id, auth.UserPatch{RequestVersions: &keys}); err != nil {
			t.Fatal(err)
		}
	}
	var s1, s2 CreateResult
	e.callAs(ann, "POST", APIBase+"/requests", map[string]any{"title_id": 10, "seasons": []int{1}}, 201, &s1)
	e.callAs(bob, "POST", APIBase+"/requests", map[string]any{"title_id": 10, "seasons": []int{2}}, 201, &s2)
	if s2.Merged || s2.Request.ID == s1.Request.ID || !slices.Equal(keysOf(s2.Request), []string{"tv-b"}) ||
		!slices.Equal(s2.Request.Seasons, []int{2}) {
		t.Fatalf("second request: %+v", s2)
	}
	for _, id := range []int64{s1.Request.ID, s2.Request.ID} {
		e.callAs(mgr, "POST", fmt.Sprintf("%s/requests/%d/approve", APIBase, id), map[string]any{}, 200, nil)
	}
	var mon []string
	scanAll(ctx, e.st.Reader(), `SELECT v.policy_key || ':' || e.season FROM episode_version_state s JOIN version v ON
		v.id = s.version_id JOIN episode e ON e.id = s.episode_id WHERE v.title_id = 10 AND v.policy_key IN ('tv-a', 'tv-b')
		AND s.monitored = 1 GROUP BY v.policy_key, e.season ORDER BY 1`, nil, func(rows *sql.Rows) error {
		var s string
		rows.Scan(&s)
		mon = append(mon, s)
		return nil
	})
	if !slices.Equal(mon, []string{"tv-a:1", "tv-b:2"}) {
		t.Fatalf("monitored after both approvals: %v", mon)
	}

	// Both requests on one library (tv-a), seasons 1 and 2, approved: downloads count for their own seasons only.
	// (The episodes have aired: a failure counts only for an aired gap.)
	e.exec(`UPDATE episode SET air_date = '2020-01-01' WHERE title_id = 10`)
	e.exec(`UPDATE request SET versions = '["tv-a"]' WHERE id = ?`, s2.Request.ID)
	var va int64
	e.st.Reader().QueryRow(`SELECT id FROM version WHERE title_id = 10 AND policy_key = 'tv-a'`).Scan(&va)
	now := store.Millis(addNow)
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state, episodes,
		created_at, updated_at) VALUES (10, ?, 'tv-a', 'h1', 'The.Show.S01E01.2160p', 'x', 'downloading', '[101]', ?, ?)`,
		va, now, now+1)
	if err := e.web.syncRequests(ctx); err != nil {
		t.Fatal(err)
	}
	if a, b := e.requestStatus(s1.Request.ID), e.requestStatus(s2.Request.ID); a != ReqProcessing || b != ReqApproved {
		t.Errorf("season 1 downloading: %s, %s", a, b)
	}
	e.exec(`UPDATE download SET state = 'failed', updated_at = ?`, now+1)
	e.web.syncRequests(ctx)
	if a, b := e.requestStatus(s1.Request.ID), e.requestStatus(s2.Request.ID); a != ReqFailed || b != ReqApproved {
		t.Errorf("season 1 failed: %s, %s", a, b)
	}
	// A download without episodes is the whole series: it counts for both.
	e.exec(`UPDATE download SET episodes = '[]', state = 'downloading'`)
	e.web.syncRequests(ctx)
	if a, b := e.requestStatus(s1.Request.ID), e.requestStatus(s2.Request.ID); a != ReqProcessing || b != ReqProcessing {
		t.Errorf("whole series downloading: %s, %s", a, b)
	}
}

// TestSeasonRequestAvailableDespiteOtherSeasonGap: a request naming seasons is available when its seasons are, even
// while the copy is wanted for a gap in another season; the request for that other season is not.
func TestSeasonRequestAvailableDespiteOtherSeasonGap(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	ctx := context.Background()
	annID, _ := e.user("ann", auth.RoleUser)
	_, mgr := e.user("mgr", auth.RoleManager)
	now := store.Millis(addNow)
	var available []int64
	e.web.onRequest = func(ev RequestEvent) {
		if ev.Notification != nil && ev.Notification.Kind == notify.RequestAvailable {
			available = append(available, ev.ID)
		}
	}
	e.exec(`INSERT INTO request (id, user_id, kind, title_id, name, versions, seasons, status, created_at, updated_at)
		VALUES (501, ?, 'series', 10, 'The Show', '["tv-2160p"]', '[1]', 'approved', ?, ?),
		(502, ?, 'series', 10, 'The Show', '["tv-2160p"]', '[2]', 'approved', ?, ?)`, annID, now, now, annID, now, now)
	// Season 1 complete with one episode not aired yet, season 2's aired episode missing: the copy stays wanted.
	e.exec(`UPDATE episode SET air_date = '2020-01-01' WHERE id IN (101, 102, 103)`)
	e.exec(`UPDATE episode_version_state SET state = 'filled' WHERE episode_id IN (101, 102)`)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, monitored, air_date, created_at, updated_at)
		VALUES (106, 10, 1, 3, 'Later', 1, '2099-01-01', ?, ?)`, now, now)
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at)
		VALUES (10, 106, 1, 'wanted', ?)`, now)
	for range 2 {
		if err := e.web.syncRequests(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if s := e.requestStatus(501); s != ReqAvailable {
		t.Errorf("season 1 request: %s", s)
	}
	if s := e.requestStatus(502); s == ReqAvailable {
		t.Errorf("season 2 request: %s", s)
	}
	if !slices.Equal(available, []int64{501}) {
		t.Errorf("available notifications: %v", available)
	}
	var page RequestPage
	e.callAs(mgr, "GET", APIBase+"/requests?status=all", nil, 200, &page)
	badge := map[int64]bool{}
	for _, r := range page.Items {
		badge[r.ID] = r.Versions[0].Available
	}
	if !badge[501] || badge[502] || len(badge) != 2 {
		t.Errorf("library badges: %v", badge)
	}
	// The detail gives the same badge as the list.
	for _, id := range []int64{501, 502} {
		var d RequestDetail
		e.callAs(mgr, "GET", fmt.Sprintf("%s/requests/%d", APIBase, id), nil, 200, &d)
		if d.Versions[0].Available != badge[id] {
			t.Errorf("request %d: detail badge %v, list %v", id, d.Versions[0].Available, badge[id])
		}
	}
}

// TestSeasonRequestWithFutureSeason: a season none of whose episodes has aired does not hold back a request for the
// show (seasons 1 to 3, the third in the future, 1 and 2 held: available), but a request of that future season alone
// is not available and its detail says when the season starts; a season partly aired and not all held is not.
func TestSeasonRequestWithFutureSeason(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	ctx := context.Background()
	annID, _ := e.user("ann", auth.RoleUser)
	_, mgr := e.user("mgr", auth.RoleManager)
	now := store.Millis(addNow)
	e.exec(`INSERT INTO request (id, user_id, kind, title_id, name, versions, seasons, status, created_at, updated_at)
		VALUES (503, ?, 'series', 10, 'The Show', '["tv-2160p"]', '[1,2,3]', 'approved', ?, ?),
		(504, ?, 'series', 10, 'The Show', '["tv-2160p"]', '[3]', 'approved', ?, ?),
		(505, ?, 'series', 10, 'The Show', '["tv-2160p"]', '[4]', 'approved', ?, ?)`,
		annID, now, now, annID, now, now, annID, now, now)
	// Seasons 1 (101, 102) and 2 (103) aired and held; 3 entirely in the future; 4 one aired episode missing, one to come.
	e.exec(`UPDATE episode SET air_date = '2020-01-01' WHERE id IN (101, 102, 103)`)
	e.exec(`UPDATE episode_version_state SET state = 'filled' WHERE episode_id IN (101, 102, 103)`)
	e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at) VALUES
		(80, '/tv/S/S01E01.mkv', '/t1', 10, 'active', ?, ?), (81, '/tv/S/S01E02.mkv', '/t2', 10, 'active', ?, ?),
		(82, '/tv/S/S02E01.mkv', '/t3', 10, 'active', ?, ?)`, now, now, now, now, now, now)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (80, 10), (81, 10), (82, 10)`)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (80, 101), (81, 102), (82, 103)`)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, monitored, air_date, created_at, updated_at) VALUES
		(107, 10, 3, 1, 'Return', 1, '2099-03-21', ?, ?), (108, 10, 3, 2, 'After', 1, '2099-03-28', ?, ?),
		(109, 10, 4, 1, 'Aired', 1, '2020-02-01', ?, ?), (110, 10, 4, 2, 'Later', 1, '2099-04-01', ?, ?)`,
		now, now, now, now, now, now, now, now)
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at) VALUES
		(10, 107, 1, 'wanted', ?), (10, 108, 1, 'wanted', ?), (10, 109, 1, 'wanted', ?), (10, 110, 1, 'wanted', ?)`,
		now, now, now, now)
	for range 2 {
		if err := e.web.syncRequests(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var page RequestPage
	e.callAs(mgr, "GET", APIBase+"/requests?status=all", nil, 200, &page)
	badge := map[int64]bool{}
	for _, r := range page.Items {
		badge[r.ID] = r.Versions[0].Available
	}
	for id, want := range map[int64]bool{503: true, 504: false, 505: false} {
		if s := e.requestStatus(id); (s == ReqAvailable) != want || badge[id] != want {
			t.Errorf("request %d: %s, badge %v; want available %v", id, s, badge[id], want)
		}
	}
	var d RequestDetail
	e.callAs(mgr, "GET", fmt.Sprintf("%s/requests/%d", APIBase, 504), nil, 200, &d)
	if len(d.Progress) != 1 || d.Progress[0].Words != "Season 3 starts 21 March 2099" {
		t.Errorf("request 504: progress %+v", d.Progress)
	}
}

// TestRequestNewSeriesSeasonChoices: a request for season 1 of a series not in the library stores the series' other
// seasons off, by the series' own seasons (the add sheet's source), so a season TVDB has and TMDb lacks (3 here; the
// TMDb fake names none) is not monitored by default when the first refresh brings it.
func TestRequestNewSeriesSeasonChoices(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	e.web.seriesLookup = &fakeSeriesLookup{seasons: []int{0, 1, 2, 3}}
	_, ann := e.user("ann", auth.RoleUser)
	_, mgr := e.user("mgr", auth.RoleManager)
	var opts AddOptions
	e.callAs(ann, "GET", APIBase+"/add/options?kind=series&tmdb_id=777", nil, 200, &opts)
	if opts.EpisodesKnown || len(opts.Seasons) != 4 {
		t.Fatalf("request sheet seasons: %+v", opts)
	}
	var cr CreateResult
	e.callAs(ann, "POST", APIBase+"/requests", map[string]any{"kind": "series", "tmdb_id": 777, "name": "Alpha Anime",
		"seasons": []int{1}}, 201, &cr)
	var ap ApproveResult
	e.callAs(mgr, "POST", fmt.Sprintf("%s/requests/%d/approve", APIBase, cr.Request.ID), map[string]any{}, 200, &ap)
	if ap.Add == nil {
		t.Fatalf("approve: %+v", ap)
	}
	var seasons, pending string
	if err := e.st.Reader().QueryRow(`SELECT m.seasons, m.pending_monitor FROM series_monitor m JOIN version v
		ON v.id = m.version_id WHERE v.title_id = ?`, ap.Add.Title.ID).Scan(&seasons, &pending); err != nil {
		t.Fatal(err)
	}
	if seasons != `{"0":false,"1":true,"2":false,"3":false}` || pending != "" {
		t.Errorf("stored season choice %s, pending %q", seasons, pending)
	}
}

// TestSeasonRequestImportedMissingFailed: an imported download is not in hand, so a season request with an imported
// episode and a missing one is not "processing"; a failure counts only against an episode still missing in the
// requested library (an old failure of an episode filled later does not fail the request), and an episode airing
// later today is not yet aired.
func TestSeasonRequestImportedMissingFailed(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	ctx := context.Background()
	annID, _ := e.user("ann", auth.RoleUser)
	_, mgr := e.user("mgr", auth.RoleManager)
	now := store.Millis(addNow)
	// The detail names a failure exactly when the request's status counts one.
	detailFailed := func() bool {
		t.Helper()
		var d RequestDetail
		e.callAs(mgr, "GET", APIBase+"/requests/601", nil, 200, &d)
		for _, p := range d.Progress {
			if p.Words == "Download failed" {
				return true
			}
		}
		return false
	}
	e.exec(`INSERT INTO request (id, user_id, kind, title_id, name, versions, seasons, status, decided_at, created_at,
		updated_at) VALUES (601, ?, 'series', 10, 'The Show', '["tv-2160p"]', '[1]', 'approved', ?, ?, ?)`,
		annID, now-10, now-10, now-10)
	e.exec(`UPDATE episode SET air_date = '2020-01-01' WHERE id IN (101, 102)`)
	e.exec(`UPDATE episode_version_state SET state = 'filled' WHERE episode_id = 101`)
	status := func() string {
		t.Helper()
		if err := e.web.syncRequests(ctx); err != nil {
			t.Fatal(err)
		}
		return e.requestStatus(601)
	}
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state, episodes,
		created_at, updated_at) VALUES (71, 10, 10, 'tv-2160p', 'h71', 'The.Show.S01E01', 'x', 'imported', '[101]', ?, ?)`,
		now, now)
	if s := status(); s != ReqApproved {
		t.Errorf("imported S01E01, S01E02 missing: %s", s)
	}
	// S01E01 failed once before its import: not a failure of the request.
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state, episodes,
		created_at, updated_at) VALUES (72, 10, 10, 'tv-2160p', 'h72', 'The.Show.S01E01.Other', 'x', 'failed', '[101]', ?, ?)`,
		now, now)
	if s := status(); s != ReqApproved || detailFailed() {
		t.Errorf("old failure of a filled episode: %s, detail failed %v", s, detailFailed())
	}
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state, episodes,
		created_at, updated_at) VALUES (73, 10, 10, 'tv-2160p', 'h73', 'The.Show.S01E02', 'x', 'failed', '[102]', ?, ?)`,
		now, now)
	if s := status(); s != ReqFailed || !detailFailed() {
		t.Errorf("missing episode failed: %s, detail failed %v", s, detailFailed())
	}
	// The failed episode is removed from the series while another aired episode is still missing: its failure no
	// longer counts, in the status and in the detail.
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, monitored, air_date, created_at, updated_at)
		VALUES (105, 10, 1, 3, 'Third of one', 1, '2020-01-01', ?, ?)`, now, now)
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at)
		VALUES (10, 105, 1, 'wanted', ?)`, now)
	e.exec(`UPDATE episode SET removed_at = ? WHERE id = 102`, now)
	if s := status(); s != ReqApproved || detailFailed() {
		t.Errorf("failure of a removed episode: %s, detail failed %v", s, detailFailed())
	}
	e.exec(`UPDATE episode SET removed_at = NULL WHERE id = 102`)
	e.exec(`DELETE FROM episode_version_state WHERE episode_id = 105`)
	e.exec(`DELETE FROM episode WHERE id = 105`)
	// S01E02 airs later today: the season holds every aired episode.
	e.exec(`UPDATE episode SET air_date = '2026-10-26', air_date_utc_at = ? WHERE id = 102`,
		store.Millis(addNow.Add(2*time.Hour)))
	if s := status(); s != ReqAvailable {
		t.Errorf("episode airing later today: %s", s)
	}
}
