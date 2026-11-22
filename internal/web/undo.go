package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/library"
)

// Undo an upgrade (docs/design/library.md "Undo an upgrade"): a manager puts back the file a History replacement
// ("Upgraded" or "Replaced") displaced, and the replacing release is blocked in that library (no search starts).
// History says, for each replaced file of a manager's rows, whether Undo is offered (HistoryFile.Undo, the button's
// sentence); library.UndoUpgrade does it.

type undoOps interface {
	UndoOffers(ctx context.Context, ids []int64) (map[int64]string, error)
	UndoUpgrade(ctx context.Context, actionID int64, by string) (*library.Undone, error)
}

// UndoResult is the answer of POST activity/replaced/{id}/undo.
type UndoResult struct {
	Message string `json:"message"`
}

func (w *Web) mountUndoAPI(mux *http.ServeMux) {
	w.handle(mux, "POST "+APIBase+"/activity/replaced/{id}/undo", SignedIn, auth.PermManage, w.apiUndoUpgrade)
}

// undoOffers sets, for a manager, the Undo sentence of each replaced file of History's upgrade rows that offers it.
func (w *Web) undoOffers(ctx context.Context, r *http.Request, evs []ActivityEvent) error {
	ops, ok := w.ops.(undoOps)
	if !ok || !w.permsOf(r).Bits.Has(auth.PermManage) {
		return nil
	}
	var ids []int64
	for _, x := range evs {
		if x.Event != journal.EvDeleted {
			continue
		}
		for _, f := range x.Replaced {
			if f.ID > 0 {
				ids = append(ids, f.ID)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	offers, err := ops.UndoOffers(ctx, ids)
	if err != nil {
		return err
	}
	for i := range evs {
		if evs[i].Event != journal.EvDeleted {
			continue
		}
		for j := range evs[i].Replaced {
			evs[i].Replaced[j].Undo = offers[evs[i].Replaced[j].ID]
		}
	}
	return nil
}

// POST /activity/replaced/{id}/undo: put back the file History's replacement id (a replaced file's id) displaced.
// 404 when the replacement has no saved facts (made before they were kept, or its trash copy purged); 409 with one
// sentence when it cannot be undone now.
func (w *Web) apiUndoUpgrade(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	if w.noOps(rw) {
		return
	}
	ops, ok := w.ops.(undoOps)
	if !ok {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "Undo is unavailable"})
		return
	}
	u, err := ops.UndoUpgrade(r.Context(), id, actor(r))
	var rr *library.ReturnRefused
	switch {
	case errors.As(err, &rr):
		writeJSON(rw, http.StatusConflict, apiError{Error: rr.Sentence})
		return
	case err != nil:
		w.opsError(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, UndoResult{Message: "Upgrade undone: the earlier file is back and " + u.Release +
		" is blocked in this library"})
}
