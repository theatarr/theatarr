package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// Upgrade approval (docs/design/router.md "Upgrade approval", docs/design/manage.md "Upgrades waiting"): a version with
// versions[*].upgrades: approve holds a release that would replace one of its files until a manager approves it.
// These routes list the holds, approve one (its release is routed through the normal router path) and decline one
// (never held again). Every route needs manage: family accounts never see holds, their releases or their scores.

// UpgradeCurrent is the file a held release would replace, as it was when the release was held.
type UpgradeCurrent struct {
	Quality string    `json:"quality,omitempty"`
	Score   *int64    `json:"score,omitempty"`
	At      time.Time `json:"at,omitzero"`
	// Files is how many files the release replaces (episodes for a series; 1 for a movie).
	Files int `json:"files"`
}

// UpgradeHold is one held upgrade.
type UpgradeHold struct {
	ID    int64         `json:"id"`
	Title ActivityTitle `json:"title"`
	// Version is the version key; VersionLabel names it for people ("4K Remux").
	Version      string `json:"version"`
	VersionLabel string `json:"version_label"`
	// Episodes are the release's episodes (S01E02), empty for a movie.
	Episodes []string `json:"episodes,omitempty"`
	Release  string   `json:"release"`
	Indexer  string   `json:"indexer,omitempty"`
	Quality  string   `json:"quality,omitempty"`
	Score    int      `json:"score"`
	// Reason is the decision engine's reason why the release upgrades the file.
	Reason  string         `json:"reason,omitempty"`
	Current UpgradeCurrent `json:"current"`
	// Source is what found it (acquire:<run>, rss, offer, ...).
	Source    string    `json:"source,omitempty"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	SeenAt    time.Time `json:"seen_at"`
	// The decision: when, by whom (the account, and the API key's name or "session"), and the note (the decline's
	// reason, or why it expired or was replaced).
	DecidedAt  *time.Time   `json:"decided_at,omitempty"`
	DecidedBy  *RequestUser `json:"decided_by,omitempty"`
	DecidedVia string       `json:"decided_via,omitempty"`
	Note       string       `json:"note,omitempty"`
	// RouteOutcome is what the approved release's route did (added, deferred, ...), DownloadID its download.
	RouteOutcome string `json:"route_outcome,omitempty"`
	DownloadID   int64  `json:"download_id,omitempty"`
	// CanApprove says whether approving would route now; ApproveReason says why not (router off, shadow version).
	CanApprove    bool   `json:"can_approve"`
	ApproveReason string `json:"approve_reason,omitempty"`
}

// UpgradeHolds is a page of holds and the count per state.
type UpgradeHolds struct {
	Items  []UpgradeHold  `json:"items"`
	Counts map[string]int `json:"counts"`
}

// DeclineUpgrade is the decline body.
type DeclineUpgrade struct {
	Note string `json:"note"`
}

var holdStates = map[string]bool{router.HoldPending: true, router.HoldApproved: true, router.HoldDeclined: true,
	router.HoldExpired: true, router.HoldReplaced: true}

func (w *Web) mountUpgradeAPI(mux *http.ServeMux) {
	b := APIBase
	w.handle(mux, "GET "+b+"/upgrades", SignedIn, auth.PermManage, w.apiUpgrades)
	w.handle(mux, "POST "+b+"/upgrades/{id}/approve", SignedIn, auth.PermManage, w.apiApproveUpgrade)
	w.handle(mux, "POST "+b+"/upgrades/{id}/decline", SignedIn, auth.PermManage, w.apiDeclineUpgrade)
}

func (w *Web) holds() *router.Holds {
	return &router.Holds{Store: w.st, Policy: w.pol, Queue: w.jobs, Now: w.now}
}

// apiUpgrades lists holds: ?state= (pending by default; a comma list, or all), ?title= one title, ?limit=.
func (w *Web) apiUpgrades(rw http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var states []string
	switch s := q.Get("state"); s {
	case "":
		states = []string{router.HoldPending}
	case "all":
	default:
		for st := range strings.SplitSeq(s, ",") {
			if !holdStates[st] {
				badRequest(rw, "state: unknown state "+strconv.Quote(st))
				return
			}
			states = append(states, st)
		}
	}
	var title int64
	if v := q.Get("title"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			badRequest(rw, "title: want a title id")
			return
		}
		title = n
	}
	limit, err := intParam(r, "limit", 100)
	if err != nil || limit == 0 || limit > 500 {
		badRequest(rw, "limit: 1 to 500")
		return
	}
	out, err := w.loadHolds(r.Context(), states, title, 0, limit)
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, out)
}

func (w *Web) apiApproveUpgrade(rw http.ResponseWriter, r *http.Request) {
	w.decideUpgrade(rw, r, func(ctx context.Context, id int64, by router.Decider) error {
		return w.holds().Approve(ctx, id, by)
	})
}

func (w *Web) apiDeclineUpgrade(rw http.ResponseWriter, r *http.Request) {
	var body DeclineUpgrade
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &body); err != nil {
			badRequest(rw, "invalid JSON: "+err.Error())
			return
		}
	}
	if len(body.Note) > 500 {
		badRequest(rw, "note: at most 500 characters")
		return
	}
	w.decideUpgrade(rw, r, func(ctx context.Context, id int64, by router.Decider) error {
		return w.holds().Decline(ctx, id, by, body.Note)
	})
}

// decideUpgrade runs an approve or decline as the caller, logs it (the audit trail is on the hold: who, how, when)
// and answers the hold as it now is.
func (w *Web) decideUpgrade(rw http.ResponseWriter, r *http.Request, do func(context.Context, int64, router.Decider) error) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	who := auth.FromContext(r.Context())
	by := router.Decider{UserID: who.UserID, Via: "session"}
	if who.Kind == "apikey" {
		by.Via = "key " + who.KeyName
	}
	err := do(r.Context(), id, by)
	var refused *router.HoldRefused
	switch {
	case errors.Is(err, router.ErrHoldNotFound):
		notFound(rw)
		return
	case errors.Is(err, router.ErrHoldNotPending):
		writeJSON(rw, http.StatusConflict, apiError{Error: "the upgrade is no longer waiting for a decision"})
		return
	case errors.As(err, &refused):
		writeJSON(rw, http.StatusConflict, apiError{Error: refused.Reason})
		return
	case err != nil:
		w.internal(rw, err)
		return
	}
	got, err := w.loadHolds(r.Context(), nil, 0, id, 1)
	if err != nil {
		w.internal(rw, err)
		return
	}
	if len(got.Items) == 0 {
		notFound(rw)
		return
	}
	h := got.Items[0]
	w.log.Info("upgrade hold decided", "hold", id, "state", h.State, "version", h.Version, "title", h.Title.ID,
		"release", h.Release, "user", who.UserID, "via", by.Via)
	writeJSON(rw, http.StatusOK, h)
}

// loadHolds reads holds in states (all when empty), of one title (0: every title) or one hold (id).
func (w *Web) loadHolds(ctx context.Context, states []string, title, id int64, limit int) (*UpgradeHolds, error) {
	db := w.st.Reader()
	out := &UpgradeHolds{Items: []UpgradeHold{}, Counts: map[string]int{}}
	var where []string
	var args []any
	if title != 0 {
		where, args = append(where, "h.title_id = ?"), append(args, title)
	}
	if id != 0 {
		where, args = append(where, "h.id = ?"), append(args, id)
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	err := scanAll(ctx, db, `SELECT h.state, COUNT(*) FROM upgrade_hold h`+cond+` GROUP BY h.state`, args,
		func(r *sql.Rows) error {
			var s string
			var n int
			if err := r.Scan(&s, &n); err != nil {
				return err
			}
			out.Counts[s] = n
			return nil
		})
	if err != nil {
		return nil, err
	}
	if len(states) > 0 {
		where = append(where, "h.state IN ("+store.Placeholders(len(states))+")")
		for _, s := range states {
			args = append(args, s)
		}
	}
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	type extra struct {
		titleKind string
		anime     bool
		episodes  string
		replaces  string
		decidedBy sql.NullInt64
	}
	var extras []extra
	var titleIDs []int64
	var epIDs []int64
	err = scanAll(ctx, db, `SELECT h.id, h.title_id, t.name, COALESCE(t.year, 0), t.kind, COALESCE(t.anime, 0),
		h.version_key, h.episodes, h.release_title, h.indexer, h.quality, h.score, h.reason, h.current_quality,
		h.current_score, h.current_at, h.replaces, h.source, h.state, h.created_at, h.seen_at, h.decided_at,
		h.decided_by, COALESCE(u.username, ''), COALESCE(u.display_name, ''), h.decided_via, h.note, h.route_outcome,
		COALESCE(h.download_id, 0)
		FROM upgrade_hold h JOIN title t ON t.id = h.title_id LEFT JOIN web_user u ON u.id = h.decided_by`+cond+`
		ORDER BY CASE h.state WHEN 'pending' THEN 0 ELSE 1 END, COALESCE(h.decided_at, h.seen_at) DESC, h.id DESC
		LIMIT ?`, append(args, limit), func(r *sql.Rows) error {
		var h UpgradeHold
		var x extra
		var curScore, decidedAt sql.NullInt64
		var curAt, created, seen int64
		var uname, dname string
		if err := r.Scan(&h.ID, &h.Title.ID, &h.Title.Name, &h.Title.Year, &x.titleKind, &x.anime, &h.Version,
			&x.episodes, &h.Release, &h.Indexer, &h.Quality, &h.Score, &h.Reason, &h.Current.Quality, &curScore, &curAt,
			&x.replaces, &h.Source, &h.State, &created, &seen, &decidedAt, &x.decidedBy, &uname, &dname, &h.DecidedVia,
			&h.Note, &h.RouteOutcome, &h.DownloadID); err != nil {
			return err
		}
		h.Current.Score = nullInt(curScore)
		if curAt > 0 {
			h.Current.At = ts(curAt)
		}
		h.CreatedAt, h.SeenAt = ts(created), ts(seen)
		if decidedAt.Valid {
			t := ts(decidedAt.Int64)
			h.DecidedAt = &t
		}
		if x.decidedBy.Valid {
			h.DecidedBy = &RequestUser{ID: x.decidedBy.Int64, Username: uname, DisplayName: dname}
		}
		for p := range strings.SplitSeq(x.episodes, ",") {
			if n, err := strconv.ParseInt(p, 10, 64); err == nil {
				epIDs = append(epIDs, n)
			}
		}
		titleIDs = append(titleIDs, h.Title.ID)
		out.Items = append(out.Items, h)
		extras = append(extras, x)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// The holds' copies named apart by key where their labels collide (no ownership in a cross-title list).
	var keys []string
	for _, h := range out.Items {
		keys = append(keys, h.Version)
	}
	names := w.keyLabels(keys)
	for i := range out.Items {
		out.Items[i].VersionLabel = names[out.Items[i].Version]
	}
	posters, err := w.posters(ctx, titleIDs)
	if err != nil {
		return nil, err
	}
	labels, err := episodeLabels(ctx, db, epIDs)
	if err != nil {
		return nil, err
	}
	for i := range out.Items {
		h, x := &out.Items[i], extras[i]
		h.Title = activityTitle(h.Title.ID, h.Title.Name, h.Title.Year, x.titleKind, x.anime, posters)
		for p := range strings.SplitSeq(x.episodes, ",") {
			if n, err := strconv.ParseInt(p, 10, 64); err == nil && labels[n] != "" {
				h.Episodes = append(h.Episodes, labels[n])
			}
		}
		var rep []int64
		_ = json.Unmarshal([]byte(x.replaces), &rep)
		h.Current.Files = max(len(rep), 1)
		if h.State == router.HoldPending {
			h.CanApprove, h.ApproveReason = w.mayApprove(h.Version)
		}
	}
	return out, nil
}

// mayApprove says whether an approval would route now (Holds.Approve checks it again).
func (w *Web) mayApprove(version string) (bool, string) {
	if w.pol == nil {
		return false, "no policy"
	}
	vp := w.pol.Versions[version]
	switch {
	case vp == nil:
		return false, "the library is no longer in the policy"
	case vp.UpgradeRule() == policy.UpgradesOff:
		return false, "upgrades are off for this library"
	case !w.pol.RouterSettings().Enabled:
		return false, "Automatic downloads are off (router.enabled is false); the upgrade keeps waiting"
	case vp.Mode != policy.ModeActive:
		return false, "This library is still in shadow mode; the upgrade keeps waiting"
	case !vp.Grab:
		return false, "This library has grab off; the upgrade keeps waiting"
	}
	return true, ""
}
