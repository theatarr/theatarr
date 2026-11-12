package web

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/arrimport"
	"github.com/theatarr/theatarr/internal/auth"
)

// ArrImporter runs the one-time import from Sonarr/Radarr (internal/arrimport) with the services of the request's
// settings generation.
type ArrImporter interface {
	Check(ctx context.Context, flavour, url, key string) (*arrimport.CheckResult, error)
	// Validate makes the refusals that need no read of the instance.
	Validate(ctx context.Context, req arrimport.Request) error
	Run(ctx context.Context, req arrimport.Request, progress func(arrimport.Progress)) (*arrimport.Report, error)
}

// ArrImportCheckRequest is POST /arr-import/check. The key is used for this request only.
type ArrImportCheckRequest struct {
	Flavour string `json:"flavour"`
	URL     string `json:"url"`
	APIKey  string `json:"api_key"`
}

// ArrImportRun is the server's one import run (the last one until the next starts or a restart). It never holds the
// API key.
type ArrImportRun struct {
	ID         int64              `json:"id"`
	State      string             `json:"state"` // running, done or failed
	DryRun     bool               `json:"dry_run"`
	Flavour    string             `json:"flavour"`
	URL        string             `json:"url"`
	Library    string             `json:"library"`
	Profiles   []int              `json:"profiles"`
	Paths      map[string]string  `json:"paths"`
	Progress   arrimport.Progress `json:"progress"`
	Report     *arrimport.Report  `json:"report"`
	Error      string             `json:"error,omitempty"`
	StartedAt  time.Time          `json:"started_at"`
	FinishedAt *time.Time         `json:"finished_at"`
}

// ArrImportState is GET /arr-import and the answer to a POST: run is null before the first run.
type ArrImportState struct {
	Run *ArrImportRun `json:"run"`
}

// arrImportRuns holds the run in memory.
type arrImportRuns struct {
	mu   sync.Mutex
	seq  int64
	run  *ArrImportRun
	done chan struct{} // closed when the current run ends (tests)
}

func (w *Web) mountArrImportAPI(mux *http.ServeMux) {
	b := APIBase + "/arr-import"
	h := func(pattern string, fn http.HandlerFunc) { w.handle(mux, pattern, SignedIn, auth.PermAdmin, fn) }
	h("POST "+b+"/check", w.apiArrImportCheck)
	h("POST "+b, w.apiArrImportStart)
	h("GET "+b, w.apiArrImportGet)
}

func (w *Web) arrImportReady(rw http.ResponseWriter) bool {
	if w.arrImport == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "the import from Sonarr/Radarr is not available"})
		return false
	}
	return true
}

// arrImportError answers an import error: 400 bad input, 409 a refusal, 502 the instance did not answer.
func (w *Web) arrImportError(rw http.ResponseWriter, err error) {
	var in *arrimport.InputError
	var ref *arrimport.Refusal
	var ce *arrimport.CheckError
	switch {
	case errors.As(err, &in):
		badRequest(rw, in.Msg)
	case errors.As(err, &ref):
		writeJSON(rw, http.StatusConflict, apiError{Error: ref.Msg})
	case errors.As(err, &ce):
		writeJSON(rw, http.StatusBadGateway, apiError{Error: ce.Msg})
	default:
		w.internal(rw, err)
	}
}

// POST /arr-import/check reads an instance's version, profiles, root folders and title counts.
func (w *Web) apiArrImportCheck(rw http.ResponseWriter, r *http.Request) {
	if !w.arrImportReady(rw) {
		return
	}
	var req ArrImportCheckRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	res, err := w.arrImport.Check(r.Context(), req.Flavour, req.URL, req.APIKey)
	if err != nil {
		w.arrImportError(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, res)
}

// POST /arr-import starts a Preview (dry_run) or an Import, one at a time, after the refusals that need no read.
func (w *Web) apiArrImportStart(rw http.ResponseWriter, r *http.Request) {
	if !w.arrImportReady(rw) {
		return
	}
	var req arrimport.Request
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	runs := &w.arrImportRuns
	runs.mu.Lock()
	if runs.run != nil && runs.run.State == "running" {
		runs.mu.Unlock()
		writeJSON(rw, http.StatusConflict, apiError{Error: "An import is running; wait for it to finish."})
		return
	}
	// Held while validating, so two quick POSTs cannot both start.
	defer runs.mu.Unlock()
	if err := w.arrImport.Validate(r.Context(), req); err != nil {
		w.arrImportError(rw, err)
		return
	}
	runs.seq++
	run := &ArrImportRun{ID: runs.seq, State: "running", DryRun: req.DryRun, Flavour: req.Flavour, URL: req.URL,
		Library: req.Library, Profiles: req.Profiles, Paths: req.Paths, StartedAt: w.now()}
	if run.Paths == nil {
		run.Paths = map[string]string{}
	}
	runs.run = run
	done := make(chan struct{})
	runs.done = done
	// The run keeps the request's pinned settings generation, not its cancellation.
	ctx := context.WithoutCancel(r.Context())
	go func() {
		defer close(done)
		rep, err := w.arrImport.Run(ctx, req, func(p arrimport.Progress) {
			runs.mu.Lock()
			run.Progress = p
			runs.mu.Unlock()
		})
		runs.mu.Lock()
		defer runs.mu.Unlock()
		now := w.now()
		run.FinishedAt, run.Report, run.State = &now, rep, "done"
		if err != nil {
			run.State, run.Error = "failed", err.Error()
			w.log.Warn("arr import failed", "flavour", req.Flavour, "library", req.Library, "err", err)
		}
	}()
	writeJSON(rw, http.StatusAccepted, ArrImportState{Run: run.copy()})
}

// GET /arr-import is the current or last run.
func (w *Web) apiArrImportGet(rw http.ResponseWriter, r *http.Request) {
	runs := &w.arrImportRuns
	runs.mu.Lock()
	defer runs.mu.Unlock()
	var out ArrImportState
	if runs.run != nil {
		out.Run = runs.run.copy()
	}
	writeJSON(rw, http.StatusOK, out)
}

// copy is a snapshot of the run for an answer (the caller holds the lock).
func (r *ArrImportRun) copy() *ArrImportRun {
	c := *r
	return &c
}
