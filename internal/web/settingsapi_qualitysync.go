package web

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/theatarr/theatarr/internal/settings"
)

func (w *Web) qualitySyncAvailable(rw http.ResponseWriter) bool {
	if w.qualitySync != nil {
		return true
	}
	writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "quality sync is unavailable"})
	return false
}

func (w *Web) apiQualitySyncPreview(rw http.ResponseWriter, r *http.Request) {
	var body struct {
		Generation *int64          `json:"generation"`
		Key        string          `json:"key"`
		Config     json.RawMessage `json:"config"`
	}
	if !decodeSettingsJSON(rw, r, &body) {
		return
	}
	snap, ok := w.qualityAt(rw, r, body.Generation)
	if !ok {
		return
	}
	if !w.qualitySyncAvailable(rw) {
		return
	}
	var config settings.QualitySyncConfig
	if err := decodeConfig(withoutScoreState(body.Config), &config); err != nil {
		w.settingsError(rw, r, err, nil)
		return
	}
	if body.Key == "" {
		badRequest(rw, "source key is required")
		return
	}
	p, err := w.qualitySync.PreviewAndStoreAt(r.Context(), settings.QualitySyncSource{Record: settings.Record{Key: body.Key}, Config: config}, snap.Generation)
	if err != nil {
		w.settingsError(rw, r, err, nil)
		return
	}
	// A concurrent edit during the fetch may never rebase the admin's preview silently.
	if p.Generation != snap.Generation {
		w.settingsError(rw, r, settings.ErrConflict, nil)
		return
	}
	rw.Header().Set("ETag", generationETag(p.Generation))
	writeJSON(rw, 200, p)
}

func (w *Web) apiQualityReviews(rw http.ResponseWriter, r *http.Request) {
	if _, ok := w.settingsHead(rw, r); !ok {
		return
	}
	if !w.qualitySyncAvailable(rw) {
		return
	}
	rows, err := w.qualitySync.Reviews(r.Context())
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, 200, rows)
}

func (w *Web) apiQualityReviewApply(rw http.ResponseWriter, r *http.Request) {
	var body struct {
		Generation *int64 `json:"generation"`
		// ConfirmationToken confirms the effects a previous 422 listed (the review widens an active version).
		ConfirmationToken string `json:"confirmation_token"`
	}
	if !decodeSettingsJSON(rw, r, &body) {
		return
	}
	snap, ok := w.qualityAt(rw, r, body.Generation)
	if !ok {
		return
	}
	if !w.qualitySyncAvailable(rw) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(rw, "invalid review id")
		return
	}
	ctx := settings.WithConfirmationToken(r.Context(), confirmationToken(r, body.ConfirmationToken))
	saved, err := w.qualitySync.Apply(ctx, id, snap.Generation, settingsActor(r))
	if errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return
	}
	if err != nil {
		w.settingsError(rw, r, err, nil)
		return
	}
	rw.Header().Set("ETag", generationETag(saved.Generation))
	writeJSON(rw, 200, map[string]any{"generation": saved.Generation, "revision": saved.Revision})
}

// apiQualityReviewScores takes or keeps held scores of a held-score review at the current generation.
func (w *Web) apiQualityReviewScores(rw http.ResponseWriter, r *http.Request) {
	var body struct {
		Generation        *int64                   `json:"generation"`
		Decisions         []settings.ScoreDecision `json:"decisions"`
		ConfirmationToken string                   `json:"confirmation_token"`
	}
	if !decodeSettingsJSON(rw, r, &body) {
		return
	}
	snap, ok := w.qualityAt(rw, r, body.Generation)
	if !ok {
		return
	}
	if !w.qualitySyncAvailable(rw) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(rw, "invalid review id")
		return
	}
	ctx := settings.WithConfirmationToken(r.Context(), confirmationToken(r, body.ConfirmationToken))
	saved, err := w.qualitySync.ResolveScores(ctx, id, snap.Generation, body.Decisions, settingsActor(r))
	if errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return
	}
	if err != nil {
		w.settingsError(rw, r, err, nil)
		return
	}
	rw.Header().Set("ETag", generationETag(saved.Generation))
	writeJSON(rw, 200, map[string]any{"generation": saved.Generation, "revision": saved.Revision})
}

func (w *Web) apiQualitySyncRun(rw http.ResponseWriter, r *http.Request) {
	var body struct {
		Generation *int64 `json:"generation"`
	}
	if !decodeSettingsJSON(rw, r, &body) {
		return
	}
	snap, ok := w.qualityAt(rw, r, body.Generation)
	if !ok {
		return
	}
	if !w.qualitySyncAvailable(rw) {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	for _, source := range snap.Records.SyncSources {
		if source.ID == id {
			if err := w.qualitySync.RunSource(r.Context(), source, true); err != nil {
				w.settingsError(rw, r, err, nil)
				return
			}
			writeJSON(rw, 200, map[string]bool{"ok": true})
			return
		}
	}
	writeJSON(rw, 404, apiError{Error: "sync source not found"})
}

func (w *Web) apiQualitySyncRuns(rw http.ResponseWriter, r *http.Request) {
	if _, ok := w.settingsHead(rw, r); !ok {
		return
	}
	if !w.qualitySyncAvailable(rw) {
		return
	}
	rows, err := w.qualitySync.Runs(r.Context())
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, 200, rows)
}

// syncSourceAt finds the saved sync source of the path id in snap, answering 404 when there is none.
func syncSourceAt(rw http.ResponseWriter, r *http.Request, snap *settings.Snapshot) (settings.QualitySyncSource, bool) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	for _, source := range snap.Records.SyncSources {
		if source.ID == id {
			return source, true
		}
	}
	writeJSON(rw, 404, apiError{Error: "sync source not found"})
	return settings.QualitySyncSource{}, false
}

// apiTrashLinkPreview matches the formats of a saved source's flavour against its TRaSH tree. It changes nothing.
func (w *Web) apiTrashLinkPreview(rw http.ResponseWriter, r *http.Request) {
	var body struct {
		Generation *int64 `json:"generation"`
	}
	if !decodeSettingsJSON(rw, r, &body) {
		return
	}
	snap, ok := w.qualityAt(rw, r, body.Generation)
	if !ok || !w.qualitySyncAvailable(rw) {
		return
	}
	source, ok := syncSourceAt(rw, r, snap)
	if !ok {
		return
	}
	p, err := w.qualitySync.LinkPreview(r.Context(), source)
	if err != nil {
		w.settingsError(rw, r, err, nil)
		return
	}
	if p.Generation != snap.Generation {
		w.settingsError(rw, r, settings.ErrConflict, nil)
		return
	}
	rw.Header().Set("ETag", generationETag(p.Generation))
	writeJSON(rw, 200, p)
}

// apiTrashLinkApply links the chosen formats of a link preview, at its generation and TRaSH commit.
func (w *Web) apiTrashLinkApply(rw http.ResponseWriter, r *http.Request) {
	var body struct {
		Generation        *int64               `json:"generation"`
		Commit            string               `json:"commit"`
		Links             []settings.TrashLink `json:"links"`
		ConfirmationToken string               `json:"confirmation_token"`
	}
	if !decodeSettingsJSON(rw, r, &body) {
		return
	}
	snap, ok := w.qualityAt(rw, r, body.Generation)
	if !ok || !w.qualitySyncAvailable(rw) {
		return
	}
	source, ok := syncSourceAt(rw, r, snap)
	if !ok {
		return
	}
	ctx := settings.WithConfirmationToken(r.Context(), confirmationToken(r, body.ConfirmationToken))
	saved, n, err := w.qualitySync.LinkApply(ctx, source, snap.Generation, body.Commit, body.Links, settingsActor(r))
	if errors.Is(err, settings.ErrLinkRejected) {
		writeJSON(rw, http.StatusUnprocessableEntity, apiError{Error: err.Error()})
		return
	}
	if err != nil {
		w.settingsError(rw, r, err, nil)
		return
	}
	rw.Header().Set("ETag", generationETag(saved.Generation))
	writeJSON(rw, 200, map[string]any{"generation": saved.Generation, "revision": saved.Revision, "linked": n})
}
