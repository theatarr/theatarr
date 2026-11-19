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

// The grab journal (internal/journal, docs/journal.md): entries with their reasoning, what followed and their stored
// suspect flags; the flagged count for the badge.

func (w *Web) mountJournalAPI(mux *http.ServeMux) {
	b := APIBase + "/journal"
	w.handle(mux, "GET "+b, SignedIn, auth.PermAdmin, w.apiJournal)
	w.handle(mux, "GET "+b+"/{id}", SignedIn, auth.PermAdmin, w.apiJournalEntry)
	w.handle(mux, "GET "+b+"/flagged", SignedIn, auth.PermAdmin, w.apiJournalFlagged)
}

// GET /journal/flagged: how many entries carry a flag of warn or error (a COUNT over the stored flags).
func (w *Web) apiJournalFlagged(rw http.ResponseWriter, r *http.Request) {
	n, err := w.journal.FlaggedCount(r.Context())
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]int{"count": n})
}

// journalFilter reads since (RFC 3339, YYYY-MM-DD, or a duration back from now such as 72h), title (what a person
// typed: a number is the id or a whole title name, other text part of the name), title_id (one title exactly), version, kind (comma-separated or repeated), suspect, severity, cursor and limit (≤500).
func (w *Web) journalFilter(r *http.Request) (journal.Filter, error) {
	q := r.URL.Query()
	f := journal.Filter{Title: strings.TrimSpace(q.Get("title")), Version: q.Get("version"),
		Severity: q.Get("severity"), Cursor: q.Get("cursor")}
	if v := q.Get("title_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			return f, errors.New("title_id must be a title id")
		}
		f.TitleID = id
	}
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
	for _, e := range page.Items {
		w.unblockWords(e)
	}
	writeJSON(rw, http.StatusOK, page)
}

// unblockWords says, on an unblock's line, why the release was blocked in the Blocklist's words (History's Blocked
// row: blockWords and blockedReasonWords) instead of the stored raw reason ("was blocked by library: no file passed
// the sample guard"). Read at display time, so rows stored before say it too.
func (w *Web) unblockWords(e *journal.Entry) {
	if e == nil || e.Action == nil || e.Action.Action != journal.ActUnblock {
		return
	}
	rest, ok := strings.CutPrefix(e.Action.Detail, "was blocked by ")
	at := strings.Index(e.Summary, "was blocked by ")
	if !ok || at < 0 {
		return
	}
	source, reason, _ := strings.Cut(rest, ": ")
	reason, byHand := strings.CutSuffix(reason, "; taken off by a grab by hand")
	words := blockWords(source)
	if r := blockedReasonWords(source, reason, "", w.providerTag); r != "" {
		words += ": " + r
	}
	if byHand {
		words += "; taken off by a grab by hand"
	}
	e.Summary = e.Summary[:at] + "was blocked: " + words
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
	w.unblockWords(e)
	writeJSON(rw, http.StatusOK, e)
}
