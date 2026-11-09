package web

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/journal"
)

// The grab journal (internal/journal, docs/journal.md): entries with their reasoning, what followed and suspect
// flags; verdicts that acknowledge flags and, for "wrong" on an active version with the router on, blocklist the
// release and search again.

func (w *Web) mountJournalAPI(mux *http.ServeMux) {
	b := APIBase + "/journal"
	w.handle(mux, "GET "+b, SignedIn, auth.PermAdmin, w.apiJournal)
	w.handle(mux, "GET "+b+"/{id}", SignedIn, auth.PermAdmin, w.apiJournalEntry)
	w.handle(mux, "POST "+b+"/{id}/verdict", SignedIn, auth.PermAdmin, w.apiJournalVerdict)
}

// journalFilter reads since (RFC 3339, YYYY-MM-DD, or a duration back from now such as 72h), title (id or part of
// the name), version, kind (comma-separated or repeated), suspect, severity, cursor, limit (≤500) and scan.
func (w *Web) journalFilter(r *http.Request) (journal.Filter, error) {
	q := r.URL.Query()
	f := journal.Filter{Title: strings.TrimSpace(q.Get("title")), Version: q.Get("version"),
		Severity: q.Get("severity"), Cursor: q.Get("cursor")}
	for _, k := range q["kind"] {
		for _, kk := range strings.Split(k, ",") {
			if kk = strings.TrimSpace(kk); kk != "" {
				f.Kinds = append(f.Kinds, kk)
			}
		}
	}
	if v := q.Get("suspect"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return f, errors.New("suspect must be 1, 0, true or false")
		}
		f.Suspect = b
	}
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			if t, err = time.Parse(time.DateOnly, v); err != nil {
				d, derr := time.ParseDuration(v)
				if derr != nil || d <= 0 {
					return f, errors.New("since must be RFC 3339, YYYY-MM-DD or a duration such as 72h")
				}
				t = w.now().Add(-d)
			}
		}
		f.Since = t
	}
	var err error
	if f.Limit, err = intParam(r, "limit", 50); err != nil {
		return f, err
	}
	if f.ScanLimit, err = intParam(r, "scan", 2000); err != nil {
		return f, err
	}
	f.ScanLimit = min(max(f.ScanLimit, 1), 20000)
	return f, f.Validate()
}

func (w *Web) apiJournal(rw http.ResponseWriter, r *http.Request) {
	f, err := w.journalFilter(r)
	if err != nil {
		badRequest(rw, strings.TrimPrefix(err.Error(), "journal: "))
		return
	}
	page, err := w.journal.List(r.Context(), f)
	if errors.Is(err, journal.ErrBadFilter) {
		badRequest(rw, strings.TrimPrefix(err.Error(), "journal: "))
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, page)
}

func (w *Web) apiJournalEntry(rw http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Numeric handoffs name journal_decision; the journal's canonical search entry is run:<id>.
	if decision, err := strconv.ParseInt(id, 10, 64); err == nil {
		var run int64
		err := w.st.Reader().QueryRowContext(r.Context(), `SELECT run_id FROM journal_decision WHERE id = ? AND run_id IS NOT NULL`, decision).Scan(&run)
		if errors.Is(err, sql.ErrNoRows) {
			notFound(rw)
			return
		}
		if err != nil {
			w.internal(rw, err)
			return
		}
		id = "run:" + strconv.FormatInt(run, 10)
	}
	e, err := w.journal.Get(r.Context(), id)
	if errors.Is(err, journal.ErrNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, e)
}

// POST /journal/{id}/verdict {verdict: ok|wrong, reason}: record a verdict (both acknowledge the entry's flags);
// "wrong" on an active version with the router on also blocklists the release for the version and queues a search.
func (w *Web) apiJournalVerdict(rw http.ResponseWriter, r *http.Request) {
	var req journal.VerdictRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	by := ""
	if id := auth.FromContext(r.Context()); id != nil {
		by = id.Username
		if id.Kind == "apikey" {
			by = "key:" + id.KeyName
		}
	}
	v, err := w.journal.Judge(r.Context(), r.PathValue("id"), req, by)
	switch {
	case errors.Is(err, journal.ErrNotFound):
		notFound(rw)
	case errors.Is(err, journal.ErrBadVerdict):
		badRequest(rw, strings.TrimPrefix(err.Error(), "journal: "))
	case errors.Is(err, journal.ErrNotMigrated):
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "the database lacks the journal tables (migrate)"})
	case err != nil:
		w.internal(rw, err)
	default:
		writeJSON(rw, http.StatusOK, v)
	}
}
