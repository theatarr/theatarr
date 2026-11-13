package web

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/indexer/cardigann"
	"github.com/theatarr/theatarr/internal/textsearch"
)

// CardigannCatalog is the cardigann definition catalog (docs/design/cardigann.md): the definitions the sync source has
// applied, the updates it holds for review, and the Prowlarr migration reader. cmd/theatarr implements it. Nothing
// here changes an indexer or the settings: approving a hold applies a definition update, and a proposal is only
// shown.
type CardigannCatalog interface {
	Catalog() ([]cardigann.Summary, error)
	Summary(id string) (cardigann.Summary, bool)
	Status() cardigann.Status
	Holds() []cardigann.Hold
	Approve(id string) error
	Refresh(ctx context.Context) error
	ProwlarrProposals(ctx context.Context) ([]cardigann.Proposal, error)
}

// CardigannList is GET /cardigann/definitions.
type CardigannList struct {
	Status      cardigann.Status    `json:"status"`
	Total       int                 `json:"total"`
	Definitions []cardigann.Summary `json:"definitions"`
	// Unavailable says why there is no catalog (it is off), with an empty list; empty when the catalog is on.
	Unavailable string `json:"unavailable,omitempty"`
}

// catalogOffNote is the catalog-off reason the definitions list answers.
const catalogOffNote = "the definition catalog is off: turn it on under Settings › Indexers › Catalog (cardigann.definitions)"

func (w *Web) mountCardigannAPI(mux *http.ServeMux) {
	b := APIBase + "/cardigann"
	h := func(pattern string, fn http.HandlerFunc) { w.handle(mux, pattern, SignedIn, auth.PermAdmin, fn) }
	h("GET "+b+"/definitions", w.apiCardigannDefinitions)
	h("GET "+b+"/definitions/{id}", w.apiCardigannDefinition)
	h("GET "+b+"/holds", w.apiCardigannHolds)
	h("POST "+b+"/holds/{id}/approve", w.apiCardigannApprove)
	h("POST "+b+"/refresh", w.apiCardigannRefresh)
	h("GET "+b+"/prowlarr", w.apiCardigannProwlarr)
}

func (w *Web) cardigannReady(rw http.ResponseWriter) bool {
	if w.cardigann == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "the definition catalog is off: set cardigann.definitions"})
		return false
	}
	return true
}

// apiCardigannDefinitions lists the catalog. Filters: q (id, name or description), privacy (public, semi-private,
// private), status (runs, needs_account, needs_flaresolverr, unsupported), language (prefix, e.g. en).
//
// With the catalog off it answers an empty list and the reason: a state of the install, not a server error, so the
// Add indexer sheet that reads it on opening neither retries it nor logs an error.
func (w *Web) apiCardigannDefinitions(rw http.ResponseWriter, r *http.Request) {
	if w.cardigann == nil {
		writeJSON(rw, http.StatusOK, CardigannList{Definitions: []cardigann.Summary{}, Unavailable: catalogOffNote})
		return
	}
	all, err := w.cardigann.Catalog()
	if err != nil {
		w.internal(rw, err)
		return
	}
	qv := r.URL.Query()
	q := qv.Get("q")
	privacy, status, lang := qv.Get("privacy"), qv.Get("status"), strings.ToLower(qv.Get("language"))
	out := CardigannList{Status: w.cardigann.Status(), Total: len(all), Definitions: []cardigann.Summary{}}
	for _, s := range all {
		if !textsearch.Match(q, s.ID, s.Name, s.Description) {
			continue
		}
		if (privacy != "" && s.Privacy != privacy) || (status != "" && s.Status != status) ||
			(lang != "" && !strings.HasPrefix(strings.ToLower(s.Language), lang)) {
			continue
		}
		out.Definitions = append(out.Definitions, s)
	}
	writeJSON(rw, http.StatusOK, out)
}

func (w *Web) apiCardigannDefinition(rw http.ResponseWriter, r *http.Request) {
	if !w.cardigannReady(rw) {
		return
	}
	s, ok := w.cardigann.Summary(r.PathValue("id"))
	if !ok {
		notFound(rw)
		return
	}
	writeJSON(rw, http.StatusOK, s)
}

func (w *Web) apiCardigannHolds(rw http.ResponseWriter, r *http.Request) {
	if w.cardigann == nil {
		writeJSON(rw, http.StatusOK, []cardigann.Hold{})
		return
	}
	holds := w.cardigann.Holds()
	if holds == nil {
		holds = []cardigann.Hold{}
	}
	writeJSON(rw, http.StatusOK, holds)
}

// apiCardigannApprove applies a held definition update (or, for a removed definition, the removal).
func (w *Web) apiCardigannApprove(rw http.ResponseWriter, r *http.Request) {
	if !w.cardigannReady(rw) {
		return
	}
	id := r.PathValue("id")
	if err := w.cardigann.Approve(id); err != nil {
		if errors.Is(err, cardigann.ErrNoHold) {
			notFound(rw)
			return
		}
		w.internal(rw, err)
		return
	}
	w.log.Info("cardigann hold approved", "definition", id, "actor", settingsActor(r))
	writeJSON(rw, http.StatusOK, map[string]bool{"ok": true})
}

// apiCardigannRefresh checks the definitions branch now (through the gateway, on the definitions budget).
func (w *Web) apiCardigannRefresh(rw http.ResponseWriter, r *http.Request) {
	if !w.cardigannReady(rw) {
		return
	}
	if err := w.cardigann.Refresh(r.Context()); err != nil {
		w.log.Warn("cardigann refresh failed", "err", err)
		writeJSON(rw, http.StatusBadGateway, map[string]any{"error": "refresh failed; the catalog is unchanged", "status": w.cardigann.Status()})
		return
	}
	writeJSON(rw, http.StatusOK, w.cardigann.Status())
}

// apiCardigannProwlarr reads the configured Prowlarr's indexers and proposes native ones. It writes nothing.
func (w *Web) apiCardigannProwlarr(rw http.ResponseWriter, r *http.Request) {
	if !w.cardigannReady(rw) {
		return
	}
	ps, err := w.cardigann.ProwlarrProposals(r.Context())
	if err != nil {
		w.log.Warn("prowlarr read failed", "err", err)
		writeJSON(rw, http.StatusBadGateway, apiError{Error: "could not read Prowlarr's indexers: " + err.Error()})
		return
	}
	if ps == nil {
		ps = []cardigann.Proposal{}
	}
	writeJSON(rw, http.StatusOK, ps)
}
