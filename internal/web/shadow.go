package web

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/shadow"
)

// The shadow report (docs/design/shadow.md): summary, diff rows with filters, and one row with both decisions.

func (w *Web) mountShadowAPI(mux *http.ServeMux) {
	b := APIBase + "/shadow"
	w.handle(mux, "GET "+b+"/summary", SignedIn, auth.PermAdmin, w.apiShadowSummary)
	w.handle(mux, "GET "+b+"/diffs", SignedIn, auth.PermAdmin, w.apiShadowDiffs)
	w.handle(mux, "GET "+b+"/diffs/{id}", SignedIn, auth.PermAdmin, w.apiShadowDiff)
}

func (w *Web) apiShadowSummary(rw http.ResponseWriter, r *http.Request) {
	s, err := shadow.LoadSummary(r.Context(), w.st.Reader(), w.now())
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, s)
}

// diffFilter reads the diff filters: bucket, reason, version, title_id, since (RFC 3339 or a date),
// search_found_better (1 or true), limit (≤500), offset.
func diffFilter(r *http.Request) (shadow.DiffFilter, error) {
	q := r.URL.Query()
	f := shadow.DiffFilter{Bucket: q.Get("bucket"), Reason: q.Get("reason"), Version: q.Get("version")}
	if v := q.Get("search_found_better"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return f, errors.New("search_found_better must be 1, 0, true or false")
		}
		f.SearchFoundBetter = b
	}
	if f.Bucket != "" && !slices.Contains(shadow.Buckets, f.Bucket) {
		return f, fmt.Errorf("bucket must be one of %s", strings.Join(shadow.Buckets, ", "))
	}
	if v := q.Get("title_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			return f, errors.New("title_id must be a positive integer")
		}
		f.TitleID = id
	}
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			if t, err = time.Parse(time.DateOnly, v); err != nil {
				return f, errors.New("since must be RFC 3339 or YYYY-MM-DD")
			}
		}
		f.Since = t
	}
	var err error
	if f.Limit, err = intParam(r, "limit", 50); err != nil {
		return f, err
	}
	if f.Limit == 0 || f.Limit > 500 {
		f.Limit = min(max(f.Limit, 50), 500)
	}
	if f.Offset, err = intParam(r, "offset", 0); err != nil {
		return f, err
	}
	return f, nil
}

func (w *Web) apiShadowDiffs(rw http.ResponseWriter, r *http.Request) {
	f, err := diffFilter(r)
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	items, total, err := shadow.ListDiffs(r.Context(), w.st.Reader(), f)
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"items": items, "total": total, "limit": f.Limit, "offset": f.Offset})
}

func (w *Web) apiShadowDiff(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	d, err := shadow.LoadDiff(r.Context(), w.st.Reader(), id)
	if errors.Is(err, shadow.ErrNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, d)
}
