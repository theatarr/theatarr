package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/library"
)

// Change episodes (docs/design/library.md "Which file is which episode"): an admin corrects the episodes one of
// Theatarr's series links holds, in its one library. The library renames the link for them through its gate,
// updates the episodes' state and keeps the choice for later imports of the download.

type reassignOps interface {
	ReassignEpisodes(ctx context.Context, linkID int64, episodeIDs []int64, replace bool) (*library.Reassigned, error)
}

// ReassignRequest is the body of POST /links/{id}/episodes.
type ReassignRequest struct {
	EpisodeIDs []int64 `json:"episode_ids"`
	// Replace unlinks another file of the library holding a chosen episode (moved to the trash) instead of refusing.
	Replace bool `json:"replace"`
}

// ReassignResult is the answer of a change of episodes.
type ReassignResult struct {
	Version      string   `json:"version"`
	PreviousPath string   `json:"previous_path"`
	Path         string   `json:"path"`
	Previous     []string `json:"previous"`
	Episodes     []string `json:"episodes"`
	Replaced     []string `json:"replaced"`
	Message      string   `json:"message"`
}

func (w *Web) mountReassignAPI(mux *http.ServeMux) {
	w.handle(mux, "POST "+APIBase+"/links/{id}/episodes", SignedIn, auth.PermAdmin, w.apiReassign)
}

// POST /links/{id}/episodes: the link holds the chosen episodes from now on (409 when the gate refuses, a chosen
// episode has another file and replace is off, or the link cannot change).
func (w *Web) apiReassign(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	var req ReassignRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	if len(req.EpisodeIDs) == 0 {
		badRequest(rw, "episode_ids is required")
		return
	}
	if w.noOps(rw) {
		return
	}
	ops, ok := w.ops.(reassignOps)
	if !ok {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "the library episode change is unavailable"})
		return
	}
	res, err := ops.ReassignEpisodes(r.Context(), id, req.EpisodeIDs, req.Replace)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return
	}
	if err != nil {
		w.opsError(rw, err)
		return
	}
	out := ReassignResult{Version: res.Version, PreviousPath: res.PreviousPath, Path: res.Path, Previous: res.Previous,
		Episodes: res.Episodes, Replaced: res.Replaced, Message: "The file now holds " + strings.Join(res.Episodes, ", ")}
	if out.Replaced == nil {
		out.Replaced = []string{}
	}
	if n := len(out.Replaced); n > 0 {
		out.Message += "; the file it replaced is in the trash"
		if n > 1 {
			out.Message = "The file now holds " + strings.Join(res.Episodes, ", ") + "; the files it replaced are in the trash"
		}
	}
	w.log.Info("episodes changed by hand", "link", id, "by", orSomeone(actor(r)), "episodes", strings.Join(res.Episodes, ","))
	writeJSON(rw, http.StatusOK, out)
}
