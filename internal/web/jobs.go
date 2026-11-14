package web

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/store"
)

// The job queue view and failed-job management (internal/jobs manage.go): a failed job needs attention until a later
// done job of the same kind and payload resolves it, it is retried, or someone dismisses it. Admins use a session
// with CSRF or an API key, which can also retry a confirmed restore (its owner's permissions decide). Nothing deletes
// a job.

func (w *Web) mountJobsAPI(mux *http.ServeMux) {
	b := APIBase + "/jobs"
	w.handle(mux, "GET "+b, SignedIn, auth.PermAdmin, w.apiJobs)
	w.handle(mux, "GET "+b+"/recurring", SignedIn, auth.PermAdmin, w.apiJobsRecurring)
	w.handle(mux, "POST "+b+"/{id}/retry", SignedIn, auth.PermAdmin, w.apiJobRetry)
	w.handle(mux, "POST "+b+"/{id}/dismiss", SignedIn, auth.PermAdmin, w.apiJobDismiss)
	w.handle(mux, "POST "+b+"/dismiss-resolved", SignedIn, auth.PermAdmin, w.apiJobsDismissResolved)
}

// GET /jobs?state&kind&attention&dismissed&limit: the counts (per state, plus attention) and the jobs.
func (w *Web) apiJobs(rw http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !jobStates[q.Get("state")] {
		badRequest(rw, "state must be queued, running, done or failed")
		return
	}
	limit, err := intParam(r, "limit", 100)
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	f := jobs.ListFilter{State: q.Get("state"), Kind: q.Get("kind"), Limit: limit}
	if v := q.Get("attention"); v != "" {
		if f.Attention, err = strconv.ParseBool(v); err != nil {
			badRequest(rw, "attention must be true or false")
			return
		}
	}
	if v := q.Get("dismissed"); v != "" {
		show, err := strconv.ParseBool(v)
		if err != nil {
			badRequest(rw, "dismissed must be true or false")
			return
		}
		f.HideDismissed = !show
	}
	jl, err := w.listJobsFilter(r.Context(), f)
	if err != nil {
		w.jobError(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, jl)
}

// JobRetry is the answer to POST /jobs/{id}/retry.
type JobRetry struct {
	// Job is the fresh job (or the retry already queued or running, with Created false).
	Job     Job   `json:"job"`
	RetryOf int64 `json:"retry_of"`
	Created bool  `json:"created"`
}

// POST /jobs/{id}/retry: queue a fresh run of a failed job with the same kind and payload, linked to it.
func (w *Web) apiJobRetry(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	newID, created, err := w.jobs.Retry(r.Context(), id)
	if err != nil {
		w.jobError(rw, err)
		return
	}
	s, err := w.jobs.Status(r.Context(), newID)
	if err != nil {
		w.internal(rw, err)
		return
	}
	w.log.Info("job retried", "job", id, "as", newID, "created", created, "by", requester(r))
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	writeJSON(rw, code, JobRetry{Job: jobFromStatus(s), RetryOf: id, Created: created})
}

// POST /jobs/{id}/dismiss: hide a failed job from attention. The job stays in the history.
func (w *Web) apiJobDismiss(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	s, err := w.jobs.Dismiss(r.Context(), id, requester(r))
	if err != nil {
		w.jobError(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, jobFromStatus(s))
}

// POST /jobs/dismiss-resolved: dismiss every failed job a later done job resolved.
func (w *Web) apiJobsDismissResolved(rw http.ResponseWriter, r *http.Request) {
	n, err := w.jobs.DismissResolved(r.Context(), requester(r))
	if err != nil {
		w.jobError(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]int64{"dismissed": n})
}

func (w *Web) jobError(rw http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		notFound(rw)
	case errors.Is(err, jobs.ErrNotFailed):
		writeJSON(rw, http.StatusConflict, apiError{Error: "only a failed job can be retried or dismissed"})
	case errors.Is(err, jobs.ErrNotMigrated):
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "the database lacks migration 00025 (migrate)"})
	default:
		w.internal(rw, err)
	}
}

// requester names who made the request: the user, or key:<name> for an API key.
func requester(r *http.Request) string {
	id := auth.FromContext(r.Context())
	switch {
	case id == nil:
		return ""
	case id.Kind == "apikey":
		return "key:" + id.KeyName
	}
	return id.Username
}

// JobSchedule is a recurring task and the interval Theatarr knows for it from its settings, with its last finished
// run and, when one is already queued for later, its next run.
type JobSchedule struct {
	Kind string `json:"kind"`
	// EverySeconds is how often it runs; At is the local time of day ("09:00") of a daily task when known.
	EverySeconds int64      `json:"every_seconds"`
	At           string     `json:"at,omitempty"`
	LastRun      *time.Time `json:"last_run,omitempty"`
	LastState    string     `json:"last_state,omitempty"`
	NextRun      *time.Time `json:"next_run,omitempty"`
}

// JobSchedules is GET /jobs/recurring.
type JobSchedules struct {
	Items []JobSchedule `json:"items"`
}

// acquireRoundEvery mirrors internal/acquire's roundEvery: the search round for wanted items is queued every 15 min.
const acquireRoundEvery = 15 * time.Minute

// recurringJobs are the recurring tasks the settings in force schedule, in the order Tasks lists them. Tasks run on
// demand (searches, metadata refreshes, quality work) are not here.
func (w *Web) recurringJobs() []JobSchedule {
	day := int64(24 * time.Hour / time.Second)
	sec := func(d time.Duration) int64 { return int64(d / time.Second) }
	var out []JobSchedule
	if sh := w.pol.ShadowSettings(); sh.RSS {
		out = append(out, JobSchedule{Kind: "shadow.rss", EverySeconds: sec(sh.RSSInterval)})
	}
	out = append(out, JobSchedule{Kind: "acquire.wanted", EverySeconds: sec(acquireRoundEvery)})
	if len(w.pol.Providers) > 0 {
		out = append(out, JobSchedule{Kind: "provider.sync", EverySeconds: sec(w.pol.ProviderSyncInterval())})
	}
	if e := w.pol.TorrentBackupEvery(); e > 0 {
		out = append(out, JobSchedule{Kind: "torrents.backup", EverySeconds: sec(e)})
	}
	out = append(out, JobSchedule{Kind: "db.nightly", EverySeconds: day}, JobSchedule{Kind: "db.retention", EverySeconds: day})
	if n := w.pol.NotifySettings(); n.Enabled {
		out = append(out, JobSchedule{Kind: "notify.digest", EverySeconds: day, At: fmt.Sprintf("%02d:%02d", n.DigestHour, n.DigestMinute)})
	}
	return out
}

// GET /jobs/recurring: the recurring tasks with their interval, last run and next run (when one is queued).
func (w *Web) apiJobsRecurring(rw http.ResponseWriter, r *http.Request) {
	items := w.recurringJobs()
	at := map[string]int{}
	for i, s := range items {
		at[s.Kind] = i
	}
	ctx := r.Context()
	err := scanAll(ctx, w.st.Reader(), `SELECT j.kind, j.finished_at, j.state FROM job j
		WHERE j.finished_at IS NOT NULL AND j.id = (SELECT k.id FROM job k WHERE k.kind = j.kind
		AND k.finished_at IS NOT NULL ORDER BY k.finished_at DESC, k.id DESC LIMIT 1)`, nil, func(rows *sql.Rows) error {
		var kind, state string
		var fin int64
		if err := rows.Scan(&kind, &fin, &state); err != nil {
			return err
		}
		if i, ok := at[kind]; ok {
			t := ts(fin)
			items[i].LastRun, items[i].LastState = &t, state
		}
		return nil
	})
	if err == nil {
		err = scanAll(ctx, w.st.Reader(), `SELECT kind, MIN(run_at) FROM job WHERE state = 'queued' AND run_at > ?
			GROUP BY kind`, []any{store.Millis(w.now())}, func(rows *sql.Rows) error {
			var kind string
			var run int64
			if err := rows.Scan(&kind, &run); err != nil {
				return err
			}
			if i, ok := at[kind]; ok {
				t := ts(run)
				items[i].NextRun = &t
			}
			return nil
		})
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, JobSchedules{Items: items})
}
