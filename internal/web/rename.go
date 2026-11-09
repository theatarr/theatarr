package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/library"
)

type renameOps interface {
	PreviewRename(context.Context, int64, *int) (*library.RenamePreview, error)
	ApplyRename(context.Context, int64, *int) (*library.RenamePreview, error)
}

type renameResponse struct {
	Files   []renameFileResponse `json:"files"`
	Skipped int                  `json:"skipped"`
}

type renameFileResponse struct {
	LinkID       int64  `json:"link_id"`
	Version      string `json:"version"`
	ExistingPath string `json:"existing_path"`
	NewPath      string `json:"new_path"`
}

func toRenameResponse(p *library.RenamePreview) renameResponse {
	out := renameResponse{Files: []renameFileResponse{}, Skipped: p.Skipped}
	for _, f := range p.Files {
		out.Files = append(out.Files, renameFileResponse{LinkID: f.LinkID, Version: f.Version, ExistingPath: f.ExistingPath, NewPath: f.NewPath})
	}
	return out
}

func (w *Web) mountRenameAPI(mux *http.ServeMux) {
	b := APIBase
	w.handle(mux, "GET "+b+"/titles/{id}/rename", SignedIn, auth.PermAdmin, w.apiRenamePreview)
	w.handle(mux, "POST "+b+"/titles/{id}/rename", SignedIn, auth.PermAdmin, w.apiRenameApply)
}

func (w *Web) renameSeason(r *http.Request) (*int, error) {
	if r.URL.Query().Get("season") == "" {
		return nil, nil
	}
	n, err := intParam(r, "season", 0)
	if err == nil && n < 0 {
		return nil, errors.New("season must be non-negative")
	}
	return &n, err
}

func (w *Web) apiRenamePreview(rw http.ResponseWriter, r *http.Request) {
	w.apiRename(rw, r, false)
}

func (w *Web) apiRenameApply(rw http.ResponseWriter, r *http.Request) {
	w.apiRename(rw, r, true)
}

func (w *Web) apiRename(rw http.ResponseWriter, r *http.Request, apply bool) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	var exists int
	if err := w.st.Reader().QueryRowContext(r.Context(), `SELECT 1 FROM title WHERE id = ?`, id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return
	} else if err != nil {
		w.internal(rw, err)
		return
	}
	season, err := w.renameSeason(r)
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	ops, ok := w.ops.(renameOps)
	if !ok {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "the library rename operation is unavailable"})
		return
	}
	var p *library.RenamePreview
	if apply {
		p, err = ops.ApplyRename(r.Context(), id, season)
	} else {
		p, err = ops.PreviewRename(r.Context(), id, season)
	}
	if errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return
	}
	if err != nil {
		w.opsError(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, toRenameResponse(p))
}
