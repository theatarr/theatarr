package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/store"
)

// maxSearchNowItems caps the rows one POST /wanted/search may name; "all" has no cap (the hourly budget paces it).
const maxSearchNowItems = 200

// searchNower is the part of acquire.Acquirer the Wanted page's Search now uses.
type searchNower interface {
	RequestSearchNow(ctx context.Context, only string, items []acquire.SearchNowItem) (*acquire.SearchNowResult, error)
	RequestSearchNowAll(ctx context.Context, q acquire.WantedQuery) (*acquire.SearchNowResult, error)
	ClearSearchNow(ctx context.Context) (int, error)
}

func (w *Web) mountWantedAPI(mux *http.ServeMux) {
	b := APIBase
	w.handle(mux, "POST "+b+"/wanted/search", SignedIn, auth.PermManage, w.apiWantedSearch)
	w.handle(mux, "DELETE "+b+"/wanted/search", SignedIn, auth.PermManage, w.apiWantedSearchClear)
}

func wantedWaiting(v string) bool {
	switch v {
	case "", acquire.WaitDue, acquire.WaitScheduled, acquire.WaitInFlight, acquire.WaitNotScheduled,
		acquire.WaitDownloadsOff:
		return true
	}
	return false
}

// POST /wanted/search {only, items: [{title_id, version, season?}]} or {only, all: {version?, waiting?}}: queue a
// search of the named rows of the Wanted list, or of every row the list shows for the filter: they become due, asked
// for, and start first under router.wanted.per_hour; the answer says how many started at once and how many wait. 409
// while paused.
func (w *Web) apiWantedSearch(rw http.ResponseWriter, r *http.Request) {
	var req struct {
		Only  string                  `json:"only"`
		Items []acquire.SearchNowItem `json:"items"`
		All   *struct {
			Version string `json:"version"`
			Waiting string `json:"waiting"`
		} `json:"all"`
	}
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	switch {
	case req.Only != acquire.OnlyMissing && req.Only != acquire.OnlyCutoff:
		badRequest(rw, "only must be missing or cutoff")
		return
	case (req.Items != nil) == (req.All != nil):
		badRequest(rw, "give exactly one of items and all")
		return
	case req.All == nil && (len(req.Items) == 0 || len(req.Items) > maxSearchNowItems):
		badRequest(rw, "items must name 1 to "+strconv.Itoa(maxSearchNowItems)+" rows")
		return
	case req.All != nil && !wantedWaiting(req.All.Waiting):
		badRequest(rw, "invalid waiting filter")
		return
	}
	for _, it := range req.Items {
		if it.TitleID <= 0 || it.Version == "" || (it.Season != nil && *it.Season < 0) {
			badRequest(rw, "each item needs a title_id and a version; season, when given, is 0 or more")
			return
		}
	}
	sn, ok := w.add.Acquirer.(searchNower)
	if !ok {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "acquirer is not running"})
		return
	}
	var out *acquire.SearchNowResult
	var err error
	if req.All != nil {
		out, err = sn.RequestSearchNowAll(r.Context(), acquire.WantedQuery{Only: req.Only, Version: req.All.Version,
			Waiting: req.All.Waiting})
	} else {
		out, err = sn.RequestSearchNow(r.Context(), req.Only, req.Items)
	}
	switch {
	case errors.Is(err, acquire.ErrDownloadsOff):
		writeJSON(rw, http.StatusConflict, apiError{Error: "Theatarr is paused, so nothing can be searched."})
		return
	case err != nil:
		w.internal(rw, err)
		return
	}
	w.log.Info("search now", "only", req.Only, "all", req.All != nil, "queued", out.Queued, "started", out.Started,
		"pending", out.Pending, "skipped", len(out.Skipped))
	writeJSON(rw, http.StatusOK, out)
}

// DELETE /wanted/search: the rows Search now made due go back to the regular schedule. Searches already started are
// not touched.
func (w *Web) apiWantedSearchClear(rw http.ResponseWriter, r *http.Request) {
	sn, ok := w.add.Acquirer.(searchNower)
	if !ok {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "acquirer is not running"})
		return
	}
	n, err := sn.ClearSearchNow(r.Context())
	if err != nil {
		w.internal(rw, err)
		return
	}
	w.log.Info("search now queue cleared", "cleared", n)
	writeJSON(rw, http.StatusOK, map[string]int{"cleared": n})
}

// WantedPage names the page's titles in one read; the acquirer owns selection and waiting reasons.
type WantedPage struct {
	*acquire.WantedList
	Libraries map[string]string `json:"libraries"`
	Titles    map[int64]string  `json:"titles"`
}

func (w *Web) apiWanted(rw http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("only") != acquire.OnlyMissing && q.Get("only") != acquire.OnlyCutoff {
		badRequest(rw, "only must be missing or cutoff")
		return
	}
	if !wantedWaiting(q.Get("waiting")) {
		badRequest(rw, "invalid waiting filter")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if w.add.Acquirer == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "acquirer is not running"})
		return
	}
	list, err := w.add.Acquirer.WantedList(r.Context(), acquire.WantedQuery{Only: q.Get("only"), Version: q.Get("version"), Waiting: q.Get("waiting"), Limit: limit, Offset: offset})
	if err != nil {
		w.internal(rw, err)
		return
	}
	out := WantedPage{WantedList: list, Titles: map[int64]string{}, Libraries: w.keyLabels(w.pol.ActiveVersions())}
	var ids []any
	for _, row := range list.Items {
		if _, ok := out.Titles[row.TitleID]; !ok {
			ids = append(ids, row.TitleID)
			out.Titles[row.TitleID] = ""
		}
	}
	if len(ids) > 0 {
		err = scanAll(r.Context(), w.st.Reader(), `SELECT id,name FROM title WHERE id IN (`+store.Placeholders(len(ids))+`)`, ids, func(rows *sql.Rows) error {
			var id int64
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				return err
			}
			out.Titles[id] = name
			return nil
		})
		if err != nil {
			w.internal(rw, err)
			return
		}
	}
	writeJSON(rw, http.StatusOK, out)
}
