package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/jobs"
)

// The job queue view and failed-job management (internal/jobs manage.go): a failed job needs attention until a later
// done job of the same kind and payload resolves it, it is retried, or someone dismisses it. Admins use a session
// with CSRF or an API key, which can also retry a confirmed restore (its owner's permissions decide). Nothing deletes
// a job.

func (w *Web) mountJobsAPI(mux *http.ServeMux) {
	b := APIBase + "/jobs"
	w.handle(mux, "GET "+b, SignedIn, auth.PermAdmin, w.apiJobs)
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
