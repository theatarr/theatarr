package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/theatarr/theatarr/internal/arrview/nativeapi"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/decide"
)

// Devices is the decision API and the import API behind POST /decide and /import; nativeapi.Service in
// theatarr serve.
type Devices interface {
	Decide(ctx context.Context, req nativeapi.DecideRequest) (*decide.Decision, error)
	Import(ctx context.Context, req nativeapi.ImportRequest) (*nativeapi.ImportResult, error)
}

func (w *Web) mountDevicesAPI(mux *http.ServeMux) {
	w.handle(mux, "POST "+APIBase+"/decide", SignedIn, auth.PermAdmin, w.apiDecide)
	w.handle(mux, "POST "+APIBase+"/import", SignedIn, auth.PermAdmin, w.apiImportRelease)
	for _, route := range []string{"GET /manualimport", "POST /manualimport", "GET /manualimport/options", "POST /manualimport/execute"} {
		method, path, _ := strings.Cut(route, " ")
		w.handle(mux, method+" "+APIBase+path, SignedIn, auth.PermAdmin, w.apiManualImport)
	}
}

func (w *Web) apiManualImport(rw http.ResponseWriter, r *http.Request) {
	service, ok := w.devices.(interface {
		ServeManualImport(http.ResponseWriter, *http.Request)
	})
	if !ok {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "manual import is not running"})
		return
	}
	service.ServeManualImport(rw, r)
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// POST /decide: the decision on one release or file name for a version of a title (accepted, rejections, quality,
// formats, score, upgrade), recording nothing.
func (w *Web) apiDecide(rw http.ResponseWriter, r *http.Request) {
	if w.devices == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "the decision engine is not running"})
		return
	}
	var req nativeapi.DecideRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	d, err := w.devices.Decide(r.Context(), req)
	if errors.Is(err, nativeapi.ErrBadRequest) {
		badRequest(rw, err.Error())
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, d)
}

// POST /import: hand a finished download (info hash or mount path) to the library for a version of a title. The
// library's gate decides: while it refuses (library disabled, version in shadow), or with dry_run, the answer is
// the plan with applied false and nothing written.
func (w *Web) apiImportRelease(rw http.ResponseWriter, r *http.Request) {
	if w.devices == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "the library is not running"})
		return
	}
	var req nativeapi.ImportRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	res, err := w.devices.Import(r.Context(), req)
	if errors.Is(err, nativeapi.ErrBadRequest) {
		badRequest(rw, err.Error())
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, res)
}
