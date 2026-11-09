package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/errinbox"
)

// The error inbox (internal/errinbox, docs/design/errors.md): unexpected failures grouped by fingerprint, for admins.
// Reads never change a row; a status change is explicit (seen, resolved, ignored, or new to reopen).

func (w *Web) mountErrorsAPI(mux *http.ServeMux) {
	b := APIBase + "/errors"
	w.handle(mux, "GET "+b, SignedIn, auth.PermAdmin, w.apiErrors)
	w.handle(mux, "GET "+b+"/{id}", SignedIn, auth.PermAdmin, w.apiError)
	w.handle(mux, "POST "+b+"/{id}", SignedIn, auth.PermAdmin, w.apiErrorUpdate(""))
	w.handle(mux, "POST "+b+"/bulk", SignedIn, auth.PermAdmin, w.apiErrorsBulk)
}

// ParseSince reads a since value: a duration back from now ("24h", "90m") or an RFC 3339 time.
func ParseSince(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Time{}, errors.New("since must be a duration such as 24h or an RFC 3339 time")
}

func (w *Web) errorFilter(r *http.Request) (errinbox.Filter, string) {
	q := r.URL.Query()
	f := errinbox.Filter{Status: q.Get("status"), Source: q.Get("source")}
	if !errinbox.ValidStatus(f.Status, true) {
		return f, "status must be open, " + strings.Join(errinbox.Statuses, ", ")
	}
	if !errinbox.ValidSource(f.Source) {
		return f, "source must be one of " + strings.Join(errinbox.Sources, ", ")
	}
	var err error
	if f.Since, err = ParseSince(q.Get("since"), w.now()); err != nil {
		return f, err.Error()
	}
	if f.Limit, err = intParam(r, "limit", 100); err != nil {
		return f, err.Error()
	}
	if f.Offset, err = intParam(r, "offset", 0); err != nil {
		return f, err.Error()
	}
	return f, ""
}

// GET /errors?status&source&since&limit&offset: the rows (newest sighting first, without stacks), the matching
// total, the count per status and the dropped-event count.
func (w *Web) apiErrors(rw http.ResponseWriter, r *http.Request) {
	f, bad := w.errorFilter(r)
	if bad != "" {
		badRequest(rw, bad)
		return
	}
	p, err := errinbox.List(r.Context(), w.st, f)
	if err != nil {
		w.internal(rw, err)
		return
	}
	if in := errinbox.Installed(); in != nil {
		p.Drops = max(p.Drops, in.Drops())
	}
	writeJSON(rw, http.StatusOK, p)
}

// GET /errors/{id}: one row with its context and stack.
func (w *Web) apiError(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	row, err := errinbox.Get(r.Context(), w.st, id)
	if errors.Is(err, errinbox.ErrNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, row)
}

// ErrorUpdate is the body of POST /errors/{id}: a new status, a note, or both.
type ErrorUpdate struct {
	Status string  `json:"status,omitempty"`
	Note   *string `json:"note,omitempty"`
}

// POST /errors/{id}: change a row's status or note. status forces one (the MCP errors_resolve tool).
func (w *Web) apiErrorUpdate(status string) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r)
		if !ok {
			notFound(rw)
			return
		}
		var body ErrorUpdate
		if err := decodeJSON(r, &body); err != nil {
			badRequest(rw, "invalid JSON: "+err.Error())
			return
		}
		if status != "" {
			body.Status = status
		}
		if body.Status == "" && body.Note == nil {
			badRequest(rw, "give a status or a note")
			return
		}
		if body.Status != "" && !errinbox.ValidStatus(body.Status, false) {
			badRequest(rw, "status must be one of "+strings.Join(errinbox.Statuses, ", "))
			return
		}
		if body.Note != nil && len(*body.Note) > 4096 {
			badRequest(rw, "note is longer than 4096 bytes")
			return
		}
		row, err := errinbox.Update(r.Context(), w.st, id, body.Status, body.Note, w.now())
		if errors.Is(err, errinbox.ErrNotFound) {
			notFound(rw)
			return
		}
		if err != nil {
			w.internal(rw, err)
			return
		}
		writeJSON(rw, http.StatusOK, row)
	}
}

// ErrorsBulk is the body of POST /errors/bulk: the ids, or a filter (where), and the status to set.
type ErrorsBulk struct {
	IDs   []int64 `json:"ids,omitempty"`
	Where *struct {
		Status string `json:"status,omitempty"`
		Source string `json:"source,omitempty"`
		Since  string `json:"since,omitempty"`
	} `json:"where,omitempty"`
	Status string  `json:"status"`
	Note   *string `json:"note,omitempty"`
}

// POST /errors/bulk: set the status of the given rows, or of every row a filter selects (resolve all open http
// errors, say). An empty filter is refused.
func (w *Web) apiErrorsBulk(rw http.ResponseWriter, r *http.Request) {
	var body ErrorsBulk
	if err := decodeJSON(r, &body); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	if !errinbox.ValidStatus(body.Status, false) {
		badRequest(rw, "status must be one of "+strings.Join(errinbox.Statuses, ", "))
		return
	}
	if (len(body.IDs) > 0) == (body.Where != nil) {
		badRequest(rw, "give ids or where, not both")
		return
	}
	if len(body.IDs) > 1000 {
		badRequest(rw, "at most 1000 ids")
		return
	}
	var f errinbox.Filter
	if body.Where != nil {
		f.Status, f.Source = body.Where.Status, body.Where.Source
		if !errinbox.ValidStatus(f.Status, true) || !errinbox.ValidSource(f.Source) {
			badRequest(rw, "where.status or where.source is not valid")
			return
		}
		var err error
		if f.Since, err = ParseSince(body.Where.Since, w.now()); err != nil {
			badRequest(rw, err.Error())
			return
		}
	}
	n, err := errinbox.Bulk(r.Context(), w.st, body.IDs, f, body.Status, body.Note, w.now())
	if err != nil && strings.HasPrefix(err.Error(), "give ids") {
		badRequest(rw, err.Error())
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]int64{"changed": n})
}
