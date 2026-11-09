package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/activity"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/metadata/art"
	"github.com/theatarr/theatarr/internal/notify"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Requests (docs/design/ui.md "Requests", docs/design/users.md). A user asks for a title; the versions come
// from the user's request set (an admin's choice per user; by default every version that fits the title). A
// manager or admin approves (optionally changing versions or seasons) or declines with a reason; an account with
// auto-approve has its requests approved at once. Approval runs addTitle, the same path as POST /titles and so
// catalog.Add, the arr views' add path, and asks for a search, which runs only for active versions while the router
// is on (catalog.Searchable): nothing searches while a version is in shadow. Afterwards the request follows its
// versions: approved, processing (a download is in hand), available (every requested version has its file) or failed
// (the last downloads failed and none is in hand).
//
// Duplicates merge: a request for a title and versions that an open request already covers adds the requester to
// it (request_user); a pending request grows by the versions it lacks; versions beyond an approved request make a
// new request for those versions only.
//
// Seerr stays a separate front end (through the arr views) and is not touched.

// Request states.
const (
	ReqPending    = "pending"
	ReqApproved   = "approved"
	ReqDeclined   = "declined"
	ReqCanceled   = "canceled"
	ReqProcessing = "processing"
	ReqAvailable  = "available"
	ReqFailed     = "failed"
)

var requestStates = map[string]bool{ReqPending: true, ReqApproved: true, ReqDeclined: true, ReqCanceled: true,
	ReqProcessing: true, ReqAvailable: true, ReqFailed: true}

// openStates are a request still in play (it merges new requesters).
var openStates = []string{ReqPending, ReqApproved, ReqProcessing}

func (w *Web) mountRequestsAPI(mux *http.ServeMux) {
	b := APIBase + "/requests"
	w.handle(mux, "GET "+b, SignedIn, auth.PermBrowse, w.apiRequests)
	w.handle(mux, "GET "+b+"/summary", SignedIn, auth.PermBrowse, w.apiRequestSummary)
	w.handle(mux, "GET "+b+"/{id}", SignedIn, auth.PermBrowse, w.apiRequest)
	w.handle(mux, "POST "+b, SignedIn, auth.PermRequest, w.apiCreateRequest)
	w.handle(mux, "POST "+b+"/{id}/approve", SignedIn, auth.PermManageRequests, w.apiApproveRequest)
	w.handle(mux, "POST "+b+"/{id}/decline", SignedIn, auth.PermManageRequests, w.apiDeclineRequest)
	w.handle(mux, "DELETE "+b+"/{id}", SignedIn, auth.PermRequest, w.apiCancelRequest)
}

// RequestUser names an account on a request.
type RequestUser struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

// RequestVersion is a requested version and how far it is.
type RequestVersion struct {
	GrabState VersionGrabState `json:"grab_state"`
	Key       string           `json:"key"`
	Label     string           `json:"label"`
	// State: the version's state in the library (wanted, filled, cutoff_met), "" before approval.
	State     string `json:"state,omitempty"`
	Available bool   `json:"available"`
}

// Request is one request as the API returns it.
type Request struct {
	ID       int64            `json:"id"`
	Kind     string           `json:"kind"`
	Anime    bool             `json:"anime"`
	TMDbID   *int64           `json:"tmdb_id,omitempty"`
	TVDbID   *int64           `json:"tvdb_id,omitempty"`
	TitleID  *int64           `json:"title_id,omitempty"`
	Name     string           `json:"name"`
	Year     *int64           `json:"year,omitempty"`
	Poster   string           `json:"poster,omitempty"`
	Versions []RequestVersion `json:"versions"`
	// Seasons asked for (series); empty is every season.
	Seasons []int        `json:"seasons"`
	Note    string       `json:"note,omitempty"`
	Status  string       `json:"status"`
	Reason  string       `json:"reason,omitempty"`
	User    *RequestUser `json:"user,omitempty"`
	Mine    bool         `json:"mine"`
	// Others is how many other accounts joined the request (managers only).
	Others    int           `json:"others,omitempty"`
	OtherUser []RequestUser `json:"other_users,omitempty"`
	DecidedBy *RequestUser  `json:"decided_by,omitempty"`
	DecidedAt *time.Time    `json:"decided_at,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`

	posterPath string
	userID     int64
	decidedBy  int64
	// deciderManages: the decision maker holds manage_requests (a manager's decision, not a member's auto-approval).
	deciderManages bool
	keys           []string
	decidedAt      int64
}

// RequestPage is GET requests.
type RequestPage struct {
	Items []Request `json:"items"`
	Total int       `json:"total"`
	// Counts per status over the caller's scope (mine, or all), whatever the status filter.
	Counts map[string]int `json:"counts"`
}

// RequestEvent is the SSE "request" event and the OnRequest hook's argument.
type RequestEvent struct {
	ID       int64  `json:"id"`
	Status   string `json:"status"`
	Previous string `json:"previous,omitempty"`
	Name     string `json:"name"`
	TitleID  *int64 `json:"title_id,omitempty"`
	// Users are the requesters' account ids: they address the SSE event and reach the OnRequest hook, never a
	// listener. Mine is set in the copy of the event sent to a requester.
	Users []int64 `json:"-"`
	Mine  bool    `json:"mine,omitempty"`
	// Change: created, merged, approved, declined, canceled, status.
	Change string `json:"change"`
	// Notification is the native webhook payload; it never reaches SSE listeners.
	Notification *notify.Event `json:"-"`
}

const requestSelect = `SELECT r.id, r.kind, r.anime, r.tmdb_id, r.tvdb_id, r.title_id, r.name, r.year, r.poster_path,
	r.versions, r.seasons, r.note, r.status, r.reason, r.user_id, COALESCE(u.username, ''),
	COALESCE(NULLIF(u.display_name, ''), u.username, ''), r.decided_by, COALESCE(d.username, ''),
	COALESCE(NULLIF(d.display_name, ''), d.username, ''), r.decided_at, r.created_at, r.updated_at,
	(SELECT COUNT(*) FROM request_user x WHERE x.request_id = r.id), COALESCE(d.role, ''), COALESCE(d.permissions, 0),
	COALESCE(d.auto_approve, 0)
	FROM request r LEFT JOIN web_user u ON u.id = r.user_id LEFT JOIN web_user d ON d.id = r.decided_by`

func scanRequest(sc interface{ Scan(...any) error }) (Request, error) {
	var q Request
	var tmdb, tvdb, title, year, uid, did, decided sql.NullInt64
	var versions, seasons, uname, udisp, dname, ddisp, drole string
	var created, updated, dperms int64
	var dauto bool
	err := sc.Scan(&q.ID, &q.Kind, &q.Anime, &tmdb, &tvdb, &title, &q.Name, &year, &q.posterPath, &versions, &seasons,
		&q.Note, &q.Status, &q.Reason, &uid, &uname, &udisp, &did, &dname, &ddisp, &decided, &created, &updated,
		&q.Others, &drole, &dperms, &dauto)
	if err != nil {
		return q, err
	}
	q.deciderManages = auth.Effective(drole, auth.Perm(dperms), dauto).Has(auth.PermManageRequests)
	q.TMDbID, q.TVDbID, q.TitleID, q.Year = nullInt(tmdb), nullInt(tvdb), nullInt(title), nullInt(year)
	if uid.Valid {
		q.userID = uid.Int64
		q.User = &RequestUser{ID: uid.Int64, Username: uname, DisplayName: udisp}
	}
	if did.Valid {
		q.decidedBy = did.Int64
		q.DecidedBy = &RequestUser{ID: did.Int64, Username: dname, DisplayName: ddisp}
	}
	if decided.Valid {
		t := store.FromMillis(decided.Int64)
		q.DecidedAt, q.decidedAt = &t, decided.Int64
	}
	q.CreatedAt, q.UpdatedAt = store.FromMillis(created), store.FromMillis(updated)
	if err := json.Unmarshal([]byte(versions), &q.keys); err != nil {
		return q, fmt.Errorf("request %d versions: %w", q.ID, err)
	}
	if err := json.Unmarshal([]byte(seasons), &q.Seasons); err != nil {
		return q, fmt.Errorf("request %d seasons: %w", q.ID, err)
	}
	if q.Seasons == nil {
		q.Seasons = []int{}
	}
	return q, nil
}

// requestScope is the SQL that keeps a user's own requests: theirs, or ones they joined.
const requestMine = `(r.user_id = ? OR EXISTS (SELECT 1 FROM request_user x WHERE x.request_id = r.id AND x.user_id = ?))`

// RequestFilter selects requests.
type RequestFilter struct {
	Statuses []string
	Mine     int64 // the caller's id: only their requests; 0 = all
	TitleID  int64
	TMDbID   int64
	Kind     string
	Limit    int
	Offset   int
}

func (f RequestFilter) where(withStatus bool) (string, []any) {
	var conds []string
	var args []any
	if f.Mine > 0 {
		conds, args = append(conds, requestMine), append(args, f.Mine, f.Mine)
	}
	if f.TitleID > 0 {
		conds, args = append(conds, "r.title_id = ?"), append(args, f.TitleID)
	}
	if f.TMDbID > 0 {
		conds, args = append(conds, "r.tmdb_id = ?"), append(args, f.TMDbID)
	}
	if f.Kind != "" {
		conds, args = append(conds, "r.kind = ?"), append(args, f.Kind)
	}
	if withStatus && len(f.Statuses) > 0 {
		conds = append(conds, "r.status IN ("+store.Placeholders(len(f.Statuses))+")")
		for _, s := range f.Statuses {
			args = append(args, s)
		}
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// listRequests reads requests (newest first) with their version states and posters.
func (w *Web) listRequests(ctx context.Context, f RequestFilter, viewer *auth.Identity, manager bool) (*RequestPage,
	error) {
	db := w.st.Reader()
	page := &RequestPage{Items: []Request{}, Counts: map[string]int{}}
	wh, args := f.where(true)
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM request r`+wh, args...).Scan(&page.Total); err != nil {
		return nil, err
	}
	cw, cargs := f.where(false)
	err := scanAll(ctx, db, `SELECT r.status, COUNT(*) FROM request r`+cw+` GROUP BY r.status`, cargs,
		func(rows *sql.Rows) error {
			var s string
			var n int
			if err := rows.Scan(&s, &n); err != nil {
				return err
			}
			page.Counts[s] = n
			return nil
		})
	if err != nil {
		return nil, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := requestSelect + wh + ` ORDER BY r.status != 'pending', r.updated_at DESC, r.id DESC LIMIT ? OFFSET ?`
	err = scanAll(ctx, db, q, append(args, limit, f.Offset), func(rows *sql.Rows) error {
		r, err := scanRequest(rows)
		if err != nil {
			return err
		}
		page.Items = append(page.Items, r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := w.fillRequests(ctx, page.Items, viewer, manager); err != nil {
		return nil, err
	}
	return page, nil
}

// getRequest reads one request (nil when none).
func (w *Web) getRequest(ctx context.Context, id int64) (*Request, error) {
	r, err := scanRequest(w.st.Reader().QueryRowContext(ctx, requestSelect+` WHERE r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// requesters are the user ids on a request: its owner and those who joined it.
func (w *Web) requesters(ctx context.Context, r *Request) ([]int64, error) {
	var out []int64
	if r.userID > 0 {
		out = append(out, r.userID)
	}
	err := scanAll(ctx, w.st.Reader(), `SELECT user_id FROM request_user WHERE request_id = ? ORDER BY created_at`,
		[]any{r.ID}, func(rows *sql.Rows) error {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
			return nil
		})
	return out, err
}

// fillRequests adds posters, version labels and states, and for managers the other requesters.
func (w *Web) fillRequests(ctx context.Context, items []Request, viewer *auth.Identity, manager bool) error {
	if len(items) == 0 {
		return nil
	}
	var ids []int64
	var sharedIDs []int64
	for _, r := range items {
		if r.TitleID != nil {
			ids = append(ids, *r.TitleID)
		}
		if r.Others > 0 {
			sharedIDs = append(sharedIDs, r.ID)
		}
	}
	posters, err := w.posters(ctx, ids)
	if err != nil {
		return err
	}
	db := w.st.Reader()
	states := map[int64]map[string]string{}
	if len(ids) > 0 {
		b, _ := json.Marshal(ids)
		err := scanAll(ctx, db, `SELECT title_id, policy_key, state FROM version
			WHERE title_id IN (SELECT value FROM json_each(?))`, []any{string(b)}, func(rows *sql.Rows) error {
			var id int64
			var key, state string
			if err := rows.Scan(&id, &key, &state); err != nil {
				return err
			}
			if states[id] == nil {
				states[id] = map[string]string{}
			}
			states[id][key] = state
			return nil
		})
		if err != nil {
			return err
		}
	}
	joined := map[int64]bool{}
	otherUsers := map[int64][]RequestUser{}
	if len(sharedIDs) > 0 {
		b, _ := json.Marshal(sharedIDs)
		if viewer != nil {
			err := scanAll(ctx, db, `SELECT request_id FROM request_user WHERE user_id = ?
				AND request_id IN (SELECT value FROM json_each(?))`, []any{viewer.UserID, string(b)}, func(rows *sql.Rows) error {
				var id int64
				if err := rows.Scan(&id); err != nil {
					return err
				}
				joined[id] = true
				return nil
			})
			if err != nil {
				return err
			}
		}
		if manager {
			err := scanAll(ctx, db, `SELECT x.request_id, u.id, u.username, COALESCE(NULLIF(u.display_name, ''), u.username)
				FROM request_user x JOIN web_user u ON u.id = x.user_id
				WHERE x.request_id IN (SELECT value FROM json_each(?)) ORDER BY x.request_id, x.created_at`,
				[]any{string(b)}, func(rows *sql.Rows) error {
					var id int64
					var u RequestUser
					if err := rows.Scan(&id, &u.ID, &u.Username, &u.DisplayName); err != nil {
						return err
					}
					otherUsers[id] = append(otherUsers[id], u)
					return nil
				})
			if err != nil {
				return err
			}
		}
	}
	for i := range items {
		r := &items[i]
		if r.TitleID != nil && posters[*r.TitleID] != "" {
			r.Poster = posters[*r.TitleID]
		} else if f := strings.TrimPrefix(r.posterPath, "/"); art.FileRE.MatchString(f) {
			r.Poster = "/img/tmdb/" + tmdbPosterSize + "/" + f
		}
		r.Versions = []RequestVersion{}
		names := w.keyLabels(r.keys)
		for _, k := range r.keys {
			v := RequestVersion{Key: k, Label: names[k], GrabState: versionGrabState(w.pol, k)}
			if r.Status != ReqPending && r.Status != ReqDeclined && r.Status != ReqCanceled {
				if r.TitleID != nil {
					v.State = states[*r.TitleID][k]
				}
				v.Available = filledState(v.State)
			}
			r.Versions = append(r.Versions, v)
		}
		if viewer != nil {
			r.Mine = r.userID == viewer.UserID || joined[r.ID]
		}
		if r.Others > 0 && (manager || !r.Mine) {
			if manager {
				r.OtherUser = append(r.OtherUser, otherUsers[r.ID]...)
			}
		}
		if !manager && !r.Mine {
			// Users see their own requests only; this is a safety net.
			r.User, r.OtherUser = nil, nil
		}
		if !manager && viewer != nil && r.userID != viewer.UserID {
			// A joined request: who asked first is another member; users get no other requester (docs/design/users.md).
			r.User = nil
		}
		if !manager && r.decidedBy != 0 && (viewer == nil || r.decidedBy != viewer.UserID) && !r.deciderManages {
			// A member's auto-approval (the first requester's, or a joiner's) names another member; a manager's
			// decision keeps its name.
			r.DecidedBy = nil
		}
		if !manager {
			// How many others asked is for managers; a version's state is availability only (perms.go).
			r.Others = 0
			for j := range r.Versions {
				r.Versions[j].State = plainState(r.Versions[j].State)
			}
		}
	}
	return nil
}

func filledState(s string) bool { return s == "filled" || s == "cutoff_met" }

func (w *Web) apiRequests(rw http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id := auth.FromContext(r.Context())
	manager := w.permsOf(r).Has(auth.PermManageRequests)
	f := RequestFilter{Kind: q.Get("kind")}
	if f.Kind != "" && f.Kind != "movie" && f.Kind != "series" {
		badRequest(rw, "kind must be movie or series")
		return
	}
	if !manager || q.Get("mine") == "1" || q.Get("mine") == "true" {
		f.Mine = id.UserID
	}
	switch s := q.Get("status"); s {
	case "", "all":
	case "open":
		f.Statuses = openStates
	default:
		for _, x := range strings.Split(s, ",") {
			if !requestStates[x] {
				badRequest(rw, "status must be open, all or among pending, approved, declined, canceled, processing, "+
					"available, failed")
				return
			}
			f.Statuses = append(f.Statuses, x)
		}
	}
	var err error
	for _, p := range []struct {
		name string
		dst  *int64
	}{{"title_id", &f.TitleID}, {"tmdb_id", &f.TMDbID}} {
		if v := q.Get(p.name); v != "" {
			if *p.dst, err = strconv.ParseInt(v, 10, 64); err != nil || *p.dst <= 0 {
				badRequest(rw, p.name+" must be a positive integer")
				return
			}
		}
	}
	if f.Limit, err = intParam(r, "limit", 50); err != nil {
		badRequest(rw, err.Error())
		return
	}
	if f.Offset, err = intParam(r, "offset", 0); err != nil {
		badRequest(rw, err.Error())
		return
	}
	page, err := w.listRequests(r.Context(), f, id, manager)
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, page)
}

// RequestSummary is the sidebar badge: pending requests to decide (managers) and the caller's open ones.
type RequestSummary struct {
	Pending  int `json:"pending"`
	MineOpen int `json:"mine_open"`
	Badge    int `json:"badge"`
}

func (w *Web) apiRequestSummary(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := auth.FromContext(ctx)
	var s RequestSummary
	db := w.st.Reader()
	manager := w.permsOf(r).Has(auth.PermManageRequests)
	if manager {
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM request WHERE status = 'pending'`).Scan(&s.Pending); err != nil {
			w.internal(rw, err)
			return
		}
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM request r WHERE r.status IN ('pending', 'approved',
		'processing') AND `+requestMine, id.UserID, id.UserID).Scan(&s.MineOpen); err != nil {
		w.internal(rw, err)
		return
	}
	s.Badge = s.MineOpen
	if manager {
		s.Badge = s.Pending
	}
	writeJSON(rw, http.StatusOK, s)
}

// visible loads a request the caller may see: managers any, others their own (404 otherwise, so ids reveal
// nothing).
func (w *Web) visible(rw http.ResponseWriter, r *http.Request) (*Request, bool) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return nil, false
	}
	req, err := w.getRequest(r.Context(), id)
	if err != nil {
		w.internal(rw, err)
		return nil, false
	}
	if req == nil {
		notFound(rw)
		return nil, false
	}
	viewer := auth.FromContext(r.Context())
	manager := w.permsOf(r).Has(auth.PermManageRequests)
	items := []Request{*req}
	if err := w.fillRequests(r.Context(), items, viewer, manager); err != nil {
		w.internal(rw, err)
		return nil, false
	}
	if !manager && !items[0].Mine {
		notFound(rw)
		return nil, false
	}
	return &items[0], true
}

func (w *Web) apiRequest(rw http.ResponseWriter, r *http.Request) {
	if req, ok := w.visible(rw, r); ok {
		detail, err := w.requestDetail(r.Context(), req)
		if err != nil {
			w.internal(rw, err)
			return
		}
		writeJSON(rw, http.StatusOK, detail)
	}
}

// CreateRequest is the body of POST requests.
type CreateRequest struct {
	Kind    string `json:"kind"`
	TMDbID  int64  `json:"tmdb_id"`
	TVDbID  int64  `json:"tvdb_id"`
	TitleID int64  `json:"title_id"`
	// Name, Year, Anime and PosterPath describe a title not in the library (from the TMDb search result); a title in
	// the library uses its own.
	Name       string `json:"name"`
	Year       int    `json:"year"`
	Anime      *bool  `json:"anime"`
	PosterPath string `json:"poster_path"`
	// Seasons of a series; empty is every season.
	Seasons []int  `json:"seasons"`
	Note    string `json:"note"`
	// Versions: managers and admins may name them; for everyone else they come from the account's request set.
	Versions []string `json:"versions"`
}

// CreateResult is the answer to POST requests.
type CreateResult struct {
	Request Request `json:"request"`
	// Merged: the request joined an open one for the same title.
	Merged bool `json:"merged"`
	// Approved: auto-approve applied it at once.
	Approved bool `json:"approved"`
	// NotApproved says why an auto-approve account's ask that joined a pending request left it pending:
	// "asks_more", the request asks for versions or seasons beyond this ask.
	NotApproved string `json:"not_approved,omitempty"`
}

// NotApprovedAsksMore: the joined request asks for more than the joiner did, so it waits for a manager.
const NotApprovedAsksMore = "asks_more"

// askCovers is whether an ask (versions, seasons; no seasons is every season) holds all a request asks for: every
// version, and every season (a request for every season only when the ask is for every season too).
func askCovers(keys []string, seasons []int, reqKeys []string, reqSeasons []int) bool {
	if slices.ContainsFunc(reqKeys, func(k string) bool { return !slices.Contains(keys, k) }) {
		return false
	}
	return len(seasons) == 0 ||
		len(reqSeasons) > 0 && !slices.ContainsFunc(reqSeasons, func(n int) bool { return !slices.Contains(seasons, n) })
}

func (w *Web) apiCreateRequest(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body CreateRequest
	if err := decodeJSON(r, &body); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	if w.pol == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "no policy loaded"})
		return
	}
	viewer := auth.FromContext(ctx)
	perms := w.permsOf(r)
	kind, _ := parseKind(body.Kind)
	t, err := w.findTitle(ctx, body.TitleID, kind, body.TMDbID)
	if err != nil {
		w.internal(rw, err)
		return
	}
	if body.TitleID > 0 && t == nil {
		notFound(rw)
		return
	}
	anime := body.Anime != nil && *body.Anime
	name, year := strings.TrimSpace(body.Name), body.Year
	tmdbID, tvdbID := body.TMDbID, body.TVDbID
	var titleID *int64
	var on []string
	if t != nil {
		kind, anime, name, titleID = t.kind, anime || t.anime, t.name, &t.id
		if t.tmdbID > 0 {
			tmdbID = t.tmdbID
		}
		var y, tv sql.NullInt64
		if err := w.st.Reader().QueryRowContext(ctx, `SELECT year, tvdb_id FROM title WHERE id = ?`, t.id).
			Scan(&y, &tv); err != nil {
			w.internal(rw, err)
			return
		}
		year = int(y.Int64)
		if tv.Valid && tvdbID == 0 {
			tvdbID = tv.Int64
		}
		if on, err = w.versionsOn(ctx, t.id); err != nil {
			w.internal(rw, err)
			return
		}
	}
	if kind == "" {
		badRequest(rw, "kind must be movie or series (or give title_id)")
		return
	}
	if tmdbID <= 0 && tvdbID <= 0 {
		badRequest(rw, "tmdb_id (or tvdb_id for a series) is required")
		return
	}
	if kind == policy.KindMovie && tmdbID <= 0 {
		badRequest(rw, "a movie needs tmdb_id")
		return
	}
	if len(body.Note) > 500 {
		badRequest(rw, "note: at most 500 characters")
		return
	}
	for _, s := range body.Seasons {
		if s < 0 {
			badRequest(rw, "season numbers start at 0")
			return
		}
	}
	if name == "" {
		ar := AddRequest{TMDbID: tmdbID, TVDbID: tvdbID}
		w.fillFromTMDb(ctx, &ar, kind)
		name, year = ar.Title, ar.Year
		if tvdbID == 0 {
			tvdbID = ar.TVDbID
		}
	}
	if name == "" {
		name = fmt.Sprintf("TMDb %d", tmdbID)
	}
	poster := ""
	if f := strings.TrimPrefix(body.PosterPath, "/"); art.FileRE.MatchString(f) {
		poster = "/" + f
	}

	// The versions.
	fits := func(k string) bool { return catalog.Fits(w.pol.Versions[k], kind, anime) }
	var keys []string
	if len(body.Versions) > 0 && perms.Has(auth.PermManageRequests) {
		if err := w.checkVersions(body.Versions, kind); err != nil {
			badRequest(rw, err.Error())
			return
		}
		keys = slices.Clone(body.Versions)
	} else {
		u, err := w.auth.GetUser(ctx, viewer.UserID)
		if err != nil {
			w.internal(rw, err)
			return
		}
		for _, k := range u.RequestVersions {
			if fits(k) {
				keys = append(keys, k)
			}
		}
		if len(keys) == 0 {
			keys = catalog.DefaultVersions(w.pol, kind, anime)
		}
	}
	slices.Sort(keys)
	keys = slices.Compact(keys)
	seasons := slices.Clone(body.Seasons)
	slices.Sort(seasons)
	seasons = slices.Compact(seasons)
	if seasons == nil || kind != policy.KindSeries {
		seasons = []int{}
	}
	// A version the title already has is still requestable for a series season it does not fully monitor.
	var gaps map[string][]int
	if t != nil && kind == policy.KindSeries {
		if gaps, err = w.seasonGaps(ctx, t.id); err != nil {
			w.internal(rw, err)
			return
		}
	}
	keys = slices.DeleteFunc(keys, func(k string) bool { return slices.Contains(on, k) && !wantsMore(gaps[k], seasons) })
	if len(keys) == 0 {
		msg := "already in those libraries"
		if kind == policy.KindSeries {
			msg += " and seasons"
		}
		writeJSON(rw, http.StatusConflict, apiError{Error: msg})
		return
	}

	res, ev, err := w.createRequest(ctx, viewer.UserID, newRequest{kind: kind, anime: anime, tmdbID: tmdbID,
		tvdbID: tvdbID, titleID: titleID, name: name, year: year, poster: poster, keys: keys, seasons: seasons,
		note: strings.TrimSpace(body.Note)})
	if err != nil {
		w.internal(rw, err)
		return
	}
	autoApprove := perms.Has(auth.PermAutoApprove) && (!res.Merged || ev.Status == ReqPending)
	if autoApprove && res.Merged {
		// Joining a pending request approves it only when the request asks for nothing beyond this ask: an
		// auto-approve account approves its own ask, not more that someone else asked for.
		q, err := w.getRequest(ctx, res.Request.ID)
		if err != nil || q == nil {
			w.internal(rw, fmt.Errorf("request %d: reading back: %v", res.Request.ID, err))
			return
		}
		if !askCovers(keys, seasons, q.keys, q.Seasons) {
			autoApprove, res.NotApproved = false, NotApprovedAsksMore
		}
	}
	if autoApprove {
		if _, err := w.approve(ctx, res.Request.ID, viewer.UserID, nil, nil); err != nil {
			var bad *addError
			if errors.As(err, &bad) {
				w.log.Warn("request: auto-approve refused", "request", res.Request.ID, "err", bad.msg)
			} else {
				w.log.Error("request: auto-approve", "request", res.Request.ID, "err", err)
			}
		} else {
			res.Approved = true
		}
	}
	if !res.Approved {
		w.requestChanged(ctx, ev)
	}
	q, err := w.getRequest(ctx, res.Request.ID)
	if err != nil || q == nil {
		w.internal(rw, fmt.Errorf("request %d: reading back: %v", res.Request.ID, err))
		return
	}
	items := []Request{*q}
	if err := w.fillRequests(ctx, items, viewer, perms.Has(auth.PermManageRequests)); err != nil {
		w.internal(rw, err)
		return
	}
	res.Request = items[0]
	code := http.StatusCreated
	if res.Merged {
		code = http.StatusOK
	}
	w.log.Info("request", "request", res.Request.ID, "user", viewer.UserID, "title", name, "versions",
		strings.Join(keys, ","), "merged", res.Merged, "approved", res.Approved)
	writeJSON(rw, code, res)
}

type newRequest struct {
	kind           policy.Kind
	anime          bool
	tmdbID, tvdbID int64
	titleID        *int64
	name           string
	year           int
	poster         string
	keys           []string
	seasons        []int
	note           string
}

// seasonGaps are, per version a series has monitored, the seasons it does not fully monitor: an episode of the
// season (not removed) unmonitored for it or without a state row. A request may still ask for those seasons in that
// version ("Request more seasons"); approval then monitors just them (approve).
func (w *Web) seasonGaps(ctx context.Context, titleID int64) (map[string][]int, error) {
	out := map[string][]int{}
	err := scanAll(ctx, w.st.Reader(), `SELECT v.policy_key, e.season FROM version v JOIN episode e ON e.title_id =
		v.title_id LEFT JOIN episode_version_state s ON s.version_id = v.id AND s.episode_id = e.id
		WHERE v.title_id = ? AND v.monitored = 1 AND e.removed_at IS NULL AND NOT EXISTS (SELECT 1 FROM
		arrview_removed_version r WHERE r.version_id = v.id) GROUP BY v.policy_key, e.season
		HAVING MIN(COALESCE(s.monitored, 0)) = 0 ORDER BY v.policy_key, e.season`, []any{titleID},
		func(r *sql.Rows) error {
			var k string
			var n int
			if err := r.Scan(&k, &n); err != nil {
				return err
			}
			out[k] = append(out[k], n)
			return nil
		})
	return out, err
}

// wantsMore reports whether a request for seasons (empty: every season but the specials) asks for one of a
// version's gaps.
func wantsMore(gaps, seasons []int) bool {
	return slices.ContainsFunc(gaps, func(n int) bool {
		if len(seasons) == 0 {
			return n > 0
		}
		return slices.Contains(seasons, n)
	})
}

func nullable(n int64) any {
	if n <= 0 {
		return nil
	}
	return n
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// createRequest stores a request, merging it into an open one for the same title when it can.
func (w *Web) createRequest(ctx context.Context, userID int64, n newRequest) (*CreateResult, RequestEvent, error) {
	now := store.Millis(w.now())
	res := &CreateResult{}
	var ev RequestEvent
	err := w.st.WriteTx(ctx, func(tx *sql.Tx) error {
		q := `SELECT id, user_id, status, versions, seasons FROM request WHERE kind = ? AND status IN ('pending',
			'approved', 'processing') AND (`
		args := []any{string(n.kind)}
		var or []string
		if n.tmdbID > 0 {
			or, args = append(or, "tmdb_id = ?"), append(args, n.tmdbID)
		}
		if n.tvdbID > 0 && n.kind == policy.KindSeries {
			or, args = append(or, "tvdb_id = ?"), append(args, n.tvdbID)
		}
		if n.titleID != nil {
			or, args = append(or, "title_id = ?"), append(args, *n.titleID)
		}
		rows, err := tx.QueryContext(ctx, q+strings.Join(or, " OR ")+`) ORDER BY id`, args...)
		if err != nil {
			return err
		}
		type open struct {
			id       int64
			owner    sql.NullInt64
			status   string
			versions []string
			seasons  []int
		}
		var opens []open
		for rows.Next() {
			var o open
			var vs, ss string
			if err := rows.Scan(&o.id, &o.owner, &o.status, &vs, &ss); err != nil {
				rows.Close()
				return err
			}
			_ = json.Unmarshal([]byte(vs), &o.versions)
			_ = json.Unmarshal([]byte(ss), &o.seasons)
			opens = append(opens, o)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		join := func(o open, keys []string, seasons []int) error {
			if o.owner.Int64 != userID {
				if _, err := tx.ExecContext(ctx, `INSERT INTO request_user (request_id, user_id, versions, note,
					created_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, o.id, userID, jsonText(n.keys), n.note,
					now); err != nil {
					return err
				}
			}
			if keys != nil {
				if _, err := tx.ExecContext(ctx, `UPDATE request SET versions = ?, seasons = ?, updated_at = ?
					WHERE id = ?`, jsonText(keys), jsonText(seasons), now, o.id); err != nil {
					return err
				}
			} else if _, err := tx.ExecContext(ctx, `UPDATE request SET updated_at = ? WHERE id = ?`, now,
				o.id); err != nil {
				return err
			}
			res.Merged = true
			res.Request.ID = o.id
			ev = RequestEvent{ID: o.id, Status: o.status, Name: n.name, TitleID: n.titleID, Change: "merged"}
			return nil
		}
		// An open request covers a version when it holds it for every season asked (an empty list is every season).
		covers := func(o open, k string) bool {
			return slices.Contains(o.versions, k) && (len(o.seasons) == 0 ||
				len(n.seasons) > 0 && !slices.ContainsFunc(n.seasons, func(s int) bool { return !slices.Contains(o.seasons, s) }))
		}
		remaining := slices.Clone(n.keys)
		for _, o := range opens {
			remaining = slices.DeleteFunc(remaining, func(k string) bool { return covers(o, k) })
		}
		// Everything is covered by open requests: join the first that covers any of the versions.
		if len(remaining) == 0 {
			for _, o := range opens {
				if slices.ContainsFunc(n.keys, func(k string) bool { return covers(o, k) }) {
					return join(o, nil, nil)
				}
			}
		}
		// A pending request grows by the versions it lacks (and the seasons: an empty list is every season).
		for _, o := range opens {
			if o.status != ReqPending {
				continue
			}
			keys := append(slices.Clone(o.versions), remaining...)
			slices.Sort(keys)
			seasons := []int{}
			if len(o.seasons) > 0 && len(n.seasons) > 0 {
				seasons = append(slices.Clone(o.seasons), n.seasons...)
				slices.Sort(seasons)
				seasons = slices.Compact(seasons)
			}
			return join(o, slices.Compact(keys), seasons)
		}
		// Otherwise a new request for what no open request covers.
		r, err := tx.ExecContext(ctx, `INSERT INTO request (user_id, kind, tmdb_id, tvdb_id, title_id, name, year,
			anime, poster_path, versions, seasons, note, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`, userID, string(n.kind), nullable(n.tmdbID),
			nullable(n.tvdbID), n.titleID, n.name, nullable(int64(n.year)), n.anime, n.poster, jsonText(remaining),
			jsonText(n.seasons), n.note, now, now)
		if err != nil {
			return err
		}
		res.Request.ID, _ = r.LastInsertId()
		ev = RequestEvent{ID: res.Request.ID, Status: ReqPending, Name: n.name, TitleID: n.titleID, Change: "created"}
		return nil
	})
	return res, ev, err
}

// ApproveRequest is the body of POST requests/{id}/approve; absent fields keep the request's.
type ApproveRequest struct {
	Versions []string `json:"versions"`
	Seasons  *[]int   `json:"seasons"`
}

// ApproveResult is the answer to an approval: the request, and what the add did.
type ApproveResult struct {
	Request Request    `json:"request"`
	Add     *AddResult `json:"add"`
}

func (w *Web) apiApproveRequest(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	var body ApproveRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &body); err != nil {
			badRequest(rw, "invalid JSON: "+err.Error())
			return
		}
	}
	by := auth.FromContext(r.Context()).UserID
	add, err := w.approve(r.Context(), id, by, body.Versions, body.Seasons)
	var bad *addError
	switch {
	case errors.Is(err, errRequestNotFound):
		notFound(rw)
		return
	case errors.As(err, &bad):
		writeJSON(rw, bad.code, apiError{Error: bad.msg})
		return
	case err != nil:
		w.internal(rw, err)
		return
	}
	req, ok := w.visible(rw, r)
	if !ok {
		return
	}
	writeJSON(rw, http.StatusOK, ApproveResult{Request: *req, Add: add})
}

var errRequestNotFound = errors.New("no such request")

// approve runs a pending request through addTitle (catalog.Add with the request's versions and seasons; a search is
// asked for and runs only where catalog.Searchable allows) and marks it approved.
func (w *Web) approve(ctx context.Context, id, by int64, versions []string, seasons *[]int) (*AddResult, error) {
	req, err := w.getRequest(ctx, id)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errRequestNotFound
	}
	if req.Status != ReqPending {
		return nil, &addError{http.StatusConflict, "only a pending request can be approved; this one is " + req.Status}
	}
	kind := policy.Kind(req.Kind)
	keys := req.keys
	if versions != nil {
		if len(versions) == 0 {
			return nil, &addError{http.StatusBadRequest, "pick at least one version"}
		}
		if err := w.checkVersions(versions, kind); err != nil {
			return nil, &addError{http.StatusBadRequest, err.Error()}
		}
		keys = slices.Clone(versions)
		slices.Sort(keys)
		keys = slices.Compact(keys)
	}
	ss := req.Seasons
	if seasons != nil {
		ss = slices.Clone(*seasons)
		slices.Sort(ss)
		ss = slices.Compact(ss)
		if ss == nil {
			ss = []int{}
		}
	}
	anime := req.Anime
	ar := AddRequest{Kind: req.Kind, Title: req.Name, Anime: &anime, Versions: keys, Search: true}
	if req.TMDbID != nil {
		ar.TMDbID = *req.TMDbID
	}
	if req.TVDbID != nil {
		ar.TVDbID = *req.TVDbID
	}
	if req.Year != nil {
		ar.Year = int(*req.Year)
	}
	if kind == policy.KindSeries {
		if err := w.requestSeasons(ctx, req, keys, ss, &ar); err != nil {
			return nil, err
		}
	}
	add, err := w.addTitle(ctx, ar, "request:"+strconv.FormatInt(id, 10))
	if err != nil {
		return nil, err
	}
	if len(ss) == 0 && len(ar.moreSeasons) > 0 {
		// More seasons of a series: the request names them, so its status and progress follow just those.
		ss = ar.moreSeasons
	}
	now := store.Millis(w.now())
	res, err := w.st.Writer().ExecContext(ctx, `UPDATE request SET status = 'approved', title_id = ?, versions = ?,
		seasons = ?, decided_by = ?, decided_at = ?, reason = '', updated_at = ? WHERE id = ? AND status = 'pending'`,
		add.Title.ID, jsonText(keys), jsonText(ss), by, now, now, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, &addError{http.StatusConflict, "the request changed meanwhile"}
	}
	tid := add.Title.ID
	w.requestChanged(ctx, RequestEvent{ID: id, Status: ReqApproved, Previous: ReqPending, Name: req.Name,
		TitleID: &tid, Change: "approved"})
	w.log.Info("request approved", "request", id, "by", by, "title", tid, "versions", strings.Join(keys, ","))
	if err := w.syncRequests(ctx, id); err != nil {
		w.log.Warn("request: status after approval", "request", id, "err", err)
	}
	return add, nil
}

// requestSeasons sets the seasons an approval applies to a series. Versions the series does not have yet get the
// requested seasons monitored and its other known seasons not (seasonChoices; no seasons: every season). Versions it
// already has get just the requested seasons (every season but the specials when none were named) monitored; their
// other seasons stay as they are, so a request for more seasons never unmonitors what is there.
func (w *Web) requestSeasons(ctx context.Context, req *Request, keys []string, ss []int, ar *AddRequest) error {
	titleID := req.TitleID
	if titleID == nil && ar.TMDbID > 0 {
		t, err := w.findTitle(ctx, 0, policy.KindSeries, ar.TMDbID)
		if err != nil {
			return err
		}
		if t != nil {
			titleID = &t.id
		}
	}
	var on []string
	if titleID != nil {
		var err error
		if on, err = w.versionsOn(ctx, *titleID); err != nil {
			return err
		}
	}
	var added []string
	for _, k := range keys {
		if slices.Contains(on, k) {
			ar.moreVersions = append(ar.moreVersions, k)
		} else {
			added = append(added, k)
		}
	}
	if len(added) > 0 && len(ss) > 0 {
		ar.Seasons, ar.seasonVersions = w.seasonChoices(ctx, titleID, ar.TMDbID, ss), added
	}
	if len(ar.moreVersions) == 0 {
		return nil
	}
	if len(ss) > 0 {
		ar.moreSeasons = ss
		return nil
	}
	known, err := w.librarySeasons(ctx, *titleID)
	if err != nil {
		return err
	}
	for _, s := range known {
		if s.Number > 0 {
			ar.moreSeasons = append(ar.moreSeasons, s.Number)
		}
	}
	return nil
}

// seasonChoices turns the requested season numbers into catalog seasons: the requested ones monitored, the series'
// other known seasons (library episodes, else TMDb) not.
func (w *Web) seasonChoices(ctx context.Context, titleID *int64, tmdbID int64, want []int) []catalog.Season {
	known := map[int]bool{}
	if titleID != nil {
		if ss, err := w.librarySeasons(ctx, *titleID); err == nil {
			for _, s := range ss {
				known[s.Number] = true
			}
		}
	}
	if len(known) == 0 && tmdbID > 0 && w.add.TMDb != nil {
		if counts, err := w.add.TMDb.SeasonEpisodeCounts(ctx, int(tmdbID)); err == nil {
			for n := range counts {
				known[n] = true
			}
		}
	}
	for _, n := range want {
		known[n] = true
	}
	var nums []int
	for n := range known {
		nums = append(nums, n)
	}
	slices.Sort(nums)
	out := make([]catalog.Season, 0, len(nums))
	for _, n := range nums {
		out = append(out, catalog.Season{Number: n, Monitored: slices.Contains(want, n)})
	}
	return out
}

func (w *Web) apiDeclineRequest(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &body); err != nil {
			badRequest(rw, "invalid JSON: "+err.Error())
			return
		}
	}
	reason := strings.TrimSpace(body.Reason)
	if len(reason) > 500 {
		badRequest(rw, "reason: at most 500 characters")
		return
	}
	ctx := r.Context()
	req, err := w.getRequest(ctx, id)
	if err != nil {
		w.internal(rw, err)
		return
	}
	if req == nil {
		notFound(rw)
		return
	}
	by := auth.FromContext(ctx).UserID
	now := store.Millis(w.now())
	res, err := w.st.Writer().ExecContext(ctx, `UPDATE request SET status = 'declined', reason = ?, decided_by = ?,
		decided_at = ?, updated_at = ? WHERE id = ? AND status = 'pending'`, reason, by, now, now, id)
	if err != nil {
		w.internal(rw, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeJSON(rw, http.StatusConflict, apiError{Error: "only a pending request can be declined; this one is " +
			req.Status})
		return
	}
	w.requestChanged(ctx, RequestEvent{ID: id, Status: ReqDeclined, Previous: ReqPending, Name: req.Name,
		TitleID: req.TitleID, Change: "declined"})
	w.log.Info("request declined", "request", id, "by", by)
	if q, ok := w.visible(rw, r); ok {
		writeJSON(rw, http.StatusOK, q)
	}
}

// DELETE requests/{id}: cancel a pending request. Its owner cancels it (another requester who joined takes it over);
// someone who joined leaves it; a manager cancels it for everyone.
func (w *Web) apiCancelRequest(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	ctx := r.Context()
	viewer := auth.FromContext(ctx)
	manager := w.permsOf(r).Has(auth.PermManageRequests)
	req, err := w.getRequest(ctx, id)
	if err != nil {
		w.internal(rw, err)
		return
	}
	if req == nil {
		notFound(rw)
		return
	}
	who, err := w.requesters(ctx, req)
	if err != nil {
		w.internal(rw, err)
		return
	}
	mine := slices.Contains(who, viewer.UserID)
	if !mine && !manager {
		notFound(rw)
		return
	}
	if req.Status != ReqPending {
		writeJSON(rw, http.StatusConflict, apiError{Error: "only a pending request can be canceled; this one is " +
			req.Status})
		return
	}
	now := store.Millis(w.now())
	change := "canceled"
	err = w.st.WriteTx(ctx, func(tx *sql.Tx) error {
		switch {
		case mine && req.userID != viewer.UserID:
			change = "left"
			_, err := tx.ExecContext(ctx, `DELETE FROM request_user WHERE request_id = ? AND user_id = ?`, id,
				viewer.UserID)
			return err
		case mine && len(who) > 1 && !manager:
			change = "left"
			next := who[1]
			if _, err := tx.ExecContext(ctx, `UPDATE request SET user_id = ?, updated_at = ? WHERE id = ?`, next, now,
				id); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `DELETE FROM request_user WHERE request_id = ? AND user_id = ?`, id, next)
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE request SET status = 'canceled', decided_by = ?, decided_at = ?,
			updated_at = ? WHERE id = ? AND status = 'pending'`, viewer.UserID, now, now, id)
		return err
	})
	if err != nil {
		w.internal(rw, err)
		return
	}
	status := ReqCanceled
	if change == "left" {
		status = ReqPending
	}
	w.requestChanged(ctx, RequestEvent{ID: id, Status: status, Previous: ReqPending, Name: req.Name,
		TitleID: req.TitleID, Change: change})
	w.log.Info("request "+change, "request", id, "by", viewer.UserID)
	rw.WriteHeader(http.StatusNoContent)
}

// requestChanged publishes a request event to the requesters and the managers, and calls the OnRequest hook.
func (w *Web) requestChanged(ctx context.Context, ev RequestEvent) {
	if req, err := w.getRequest(ctx, ev.ID); err == nil && req != nil {
		if who, err := w.requesters(ctx, req); err == nil {
			ev.Users = who
		}
		if w.onRequest != nil {
			if n, err := w.requestNotification(ctx, req, ev); err != nil {
				w.log.Warn("request notification", "request", ev.ID, "err", err)
			} else {
				ev.Notification = n
			}
		}
	}
	if ev.Users == nil {
		ev.Users = []int64{}
	}
	// The ids address the event and stay out of the payload; a requester gets a copy marked mine.
	data, _ := json.Marshal(ev)
	mine := ev
	mine.Mine = true
	mineData, _ := json.Marshal(mine)
	w.hub.publish(event{name: "request", data: data, mine: mineData, need: auth.PermBrowse, toUsers: true,
		users: ev.Users, orPerm: auth.PermManageRequests})
	if w.onRequest != nil {
		w.onRequest(ev)
	}
}

// publishUsersChanged tells admins' screens that the user list changed (a new Plex account waits).
func (w *Web) publishUsersChanged() {
	w.hub.publish(event{name: "users", data: []byte(`{}`), need: auth.PermUsers})
}

// syncRequests moves approved requests along with their versions (all of them when ids is empty). A request is
// available when every requested version has its file, processing while a download for one is in hand, failed when
// the latest download of a missing version failed and none is in hand, else approved.
func (w *Web) syncRequests(ctx context.Context, ids ...int64) error {
	q := requestSelect + ` WHERE r.status IN ('approved', 'processing', 'failed', 'available') AND r.title_id IS NOT NULL`
	var args []any
	if len(ids) > 0 {
		q += ` AND r.id IN (` + store.Placeholders(len(ids)) + `)`
		for _, id := range ids {
			args = append(args, id)
		}
	}
	var reqs []Request
	if err := scanAll(ctx, w.st.Reader(), q, args, func(rows *sql.Rows) error {
		r, err := scanRequest(rows)
		if err == nil {
			reqs = append(reqs, r)
		}
		return err
	}); err != nil {
		return err
	}
	for _, r := range reqs {
		next, err := w.requestStatus(ctx, &r)
		if err != nil {
			return err
		}
		if w.onRequest != nil && next != ReqAvailable && r.Kind == "series" {
			if err := w.notifyRequestPartial(ctx, &r); err != nil {
				return err
			}
		}
		if next == r.Status {
			continue
		}
		res, err := w.st.Writer().ExecContext(ctx, `UPDATE request SET status = ?, updated_at = ? WHERE id = ?
			AND status = ?`, next, store.Millis(w.now()), r.ID, r.Status)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		w.log.Info("request status", "request", r.ID, "from", r.Status, "to", next)
		w.requestChanged(ctx, RequestEvent{ID: r.ID, Status: next, Previous: r.Status, Name: r.Name,
			TitleID: r.TitleID, Change: "status"})
	}
	return nil
}

// requestStatus is where an approved request stands now.
func (w *Web) requestStatus(ctx context.Context, r *Request) (string, error) {
	db := w.st.Reader()
	all := true
	var missing []int64
	for _, k := range r.keys {
		var vid int64
		var state string
		err := db.QueryRowContext(ctx, `SELECT id, state FROM version WHERE title_id = ? AND policy_key = ?`,
			*r.TitleID, k).Scan(&vid, &state)
		if errors.Is(err, sql.ErrNoRows) {
			all = false
			continue
		}
		if err != nil {
			return "", err
		}
		filled := filledState(state)
		if filled && r.Kind == string(policy.KindSeries) && len(r.Seasons) > 0 {
			// A version can be filled from before the request: the requested seasons' aired episodes decide.
			if filled, err = w.seasonsFilled(ctx, vid, r.Seasons); err != nil {
				return "", err
			}
		}
		if !filled {
			all = false
			missing = append(missing, vid)
		}
	}
	if all {
		return ReqAvailable, nil
	}
	if len(missing) == 0 {
		return ReqApproved, nil
	}
	ph := store.Placeholders(len(missing))
	args := make([]any, 0, len(missing)+1)
	for _, id := range missing {
		args = append(args, id)
	}
	var inHand int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM download WHERE version_id IN (`+ph+`)
		AND state NOT IN ('failed')`, args...).Scan(&inHand); err != nil {
		return "", err
	}
	if inHand > 0 {
		return ReqProcessing, nil
	}
	// Failed: since the approval, a download of a missing version failed.
	var failed int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM download WHERE version_id IN (`+ph+`)
		AND state = 'failed' AND updated_at >= ?`, append(args, r.decidedAt)...).Scan(&failed); err != nil {
		return "", err
	}
	if failed > 0 {
		return ReqFailed, nil
	}
	return ReqApproved, nil
}

// seasonsFilled reports whether a version holds every aired episode of these seasons (and at least one).
func (w *Web) seasonsFilled(ctx context.Context, versionID int64, seasons []int) (bool, error) {
	args := []any{versionID, versionID, w.now().UTC().Format(time.DateOnly)}
	for _, n := range seasons {
		args = append(args, n)
	}
	var aired, held int
	err := w.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(s.state IN ('filled', 'cutoff_met')), 0)
		FROM episode e LEFT JOIN episode_version_state s ON s.episode_id = e.id AND s.version_id = ?
		WHERE e.title_id = (SELECT title_id FROM version WHERE id = ?) AND e.removed_at IS NULL
		AND e.air_date IS NOT NULL AND e.air_date <> '' AND e.air_date <= ? AND e.season IN (`+
		store.Placeholders(len(seasons))+`)`, args...).Scan(&aired, &held)
	return aired > 0 && held == aired, err
}

// runRequestSync moves requests along every few poll intervals.
func (w *Web) runRequestSync(ctx context.Context) {
	t := time.NewTicker(w.poll * 5)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.syncRequests(ctx); err != nil && ctx.Err() == nil {
				w.log.Warn("requests: status sync", "err", err)
			}
		}
	}
}

// requestGrant is what one live request asks of its title: versions (none recorded: every version) and seasons
// (none: every season).
type requestGrant struct {
	versions []string
	seasons  []int
}

// requestScope is what a household account's activity covers, per title: the versions and seasons of the live
// requests it is on, as requester or joiner. It is the request's versions and seasons, not the account's own ask:
// a pending request grows by what joiners add, and seasons are kept per request only. nil covers everything.
type requestScope map[int64][]requestGrant

func (s requestScope) add(titleID int64, versions, seasons string) {
	var g requestGrant
	_ = json.Unmarshal([]byte(versions), &g.versions)
	_ = json.Unmarshal([]byte(seasons), &g.seasons)
	s[titleID] = append(s[titleID], g)
}

// covers is whether the scope holds the title's version for any of the seasons (none known: the whole title).
func (s requestScope) covers(titleID int64, version string, seasons []int) bool {
	if s == nil {
		return true
	}
	for _, g := range s[titleID] {
		if len(g.versions) > 0 && !slices.Contains(g.versions, version) {
			continue
		}
		if len(g.seasons) == 0 || len(seasons) == 0 ||
			slices.ContainsFunc(seasons, func(n int) bool { return slices.Contains(g.seasons, n) }) {
			return true
		}
	}
	return false
}

// requestScopeOf is the scope of a user's live requests (the activity a user sees).
func (w *Web) requestScopeOf(ctx context.Context, userID int64) (requestScope, error) {
	out := requestScope{}
	err := scanAll(ctx, w.st.Reader(), `SELECT r.title_id, r.versions, r.seasons FROM request r WHERE
		r.title_id IS NOT NULL AND r.status NOT IN ('declined', 'canceled') AND `+requestMine, []any{userID, userID},
		func(rows *sql.Rows) error {
			var id int64
			var vs, ss string
			if err := rows.Scan(&id, &vs, &ss); err != nil {
				return err
			}
			out.add(id, vs, ss)
			return nil
		})
	return out, err
}

// downloadRequesters is every account on a live request of the title (the requester and those merged into it) whose
// request covers the download's version and seasons.
func (w *Web) downloadRequesters(ctx context.Context, d *activity.Download) ([]int64, error) {
	seasons, err := downloadSeasons(ctx, w.st.Reader(), []activity.Download{*d})
	if err != nil {
		return nil, err
	}
	scopes := map[int64]requestScope{}
	var order []int64
	err = scanAll(ctx, w.st.Reader(), `SELECT r.user_id, r.versions, r.seasons FROM request r WHERE r.title_id = ? AND
		r.user_id IS NOT NULL AND r.status NOT IN ('declined', 'canceled')
		UNION ALL SELECT x.user_id, r.versions, r.seasons FROM request_user x JOIN request r ON r.id = x.request_id
		WHERE r.title_id = ? AND r.status NOT IN ('declined', 'canceled')`, []any{d.TitleID, d.TitleID},
		func(rows *sql.Rows) error {
			var id int64
			var vs, ss string
			if err := rows.Scan(&id, &vs, &ss); err != nil {
				return err
			}
			if scopes[id] == nil {
				scopes[id] = requestScope{}
				order = append(order, id)
			}
			scopes[id].add(d.TitleID, vs, ss)
			return nil
		})
	if err != nil {
		return nil, err
	}
	var out []int64
	for _, id := range order {
		if scopes[id].covers(d.TitleID, d.Version, seasons[d.ID]) {
			out = append(out, id)
		}
	}
	return out, nil
}
