package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/torrents"
)

// TorrentsSource gives the torrent manager of the current settings generation.
type TorrentsSource interface {
	Manager(ctx context.Context) (*torrents.Manager, error)
}

// The torrent manager API (docs/design/torrents.md), admin only. Reports and backups read; a restore and a delete write
// to the providers and need the confirmation token of their preview: an HMAC (per-process key) of the caller's
// confirmer and the plan digest, so it is bound to the exact set and to the session or API key that previewed. As
// with settings, an admin's API key confirms like a session (docs/design/settings.md "Who confirms").
func (w *Web) mountTorrentsAPI(mux *http.ServeMux) {
	b := APIBase + "/torrents"
	w.handle(mux, "GET "+b+"/report", SignedIn, auth.PermAdmin, w.apiTorrentsReport)
	w.handle(mux, "GET "+b+"/backups", SignedIn, auth.PermAdmin, w.apiTorrentBackups)
	w.handle(mux, "POST "+b+"/backups", SignedIn, auth.PermAdmin, w.apiTorrentBackupNow)
	w.handle(mux, "GET "+b+"/backups/{id}", SignedIn, auth.PermAdmin, w.apiTorrentBackupDownload)
	w.handle(mux, "POST "+b+"/backups/import", SignedIn, auth.PermAdmin, w.apiTorrentBackupImport)
	w.handle(mux, "POST "+b+"/restore/preview", SignedIn, auth.PermAdmin, w.apiTorrentRestorePreview)
	w.handle(mux, "POST "+b+"/restore", SignedIn, auth.PermAdmin, w.apiTorrentRestore)
	w.handle(mux, "POST "+b+"/delete/preview", SignedIn, auth.PermAdmin, w.apiTorrentDeletePreview)
	w.handle(mux, "POST "+b+"/delete", SignedIn, auth.PermAdmin, w.apiTorrentDelete)
	w.handle(mux, "GET "+b+"/journal", SignedIn, auth.PermAdmin, w.apiTorrentJournal)
}

func (w *Web) torrentManager(rw http.ResponseWriter, r *http.Request) *torrents.Manager {
	if w.torrents == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "the torrent manager is not running"})
		return nil
	}
	ctx := r.Context()
	if w.captureRuntime != nil {
		ctx = w.captureRuntime(ctx)
	}
	m, err := w.torrents.Manager(ctx)
	if err != nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: err.Error()})
		return nil
	}
	return m
}

// GET /torrents/report: every provider's torrents with their usage, and the groups.
func (w *Web) apiTorrentsReport(rw http.ResponseWriter, r *http.Request) {
	m := w.torrentManager(rw, r)
	if m == nil {
		return
	}
	rep, err := m.Report(r.Context())
	if err != nil {
		w.internal(rw, err)
		return
	}
	var hashes []string
	for _, p := range rep.Providers {
		for _, t := range p.Torrents {
			hashes = append(hashes, t.Hash)
		}
	}
	if copies := w.fileCopies(r.Context(), hashes); len(copies) > 0 {
		for i := range rep.Providers {
			for k := range rep.Providers[i].Torrents {
				t := &rep.Providers[i].Torrents[k]
				t.Copies = copies[strings.ToLower(t.Hash)]
			}
		}
	}
	writeJSON(rw, http.StatusOK, rep)
}

// GET /torrents/backups: the stored backups, newest first, with the schedule.
func (w *Web) apiTorrentBackups(rw http.ResponseWriter, r *http.Request) {
	m := w.torrentManager(rw, r)
	if m == nil {
		return
	}
	list, err := m.Backups()
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"backups": list,
		"every_s": int64(m.Policy.TorrentBackupEvery().Seconds()), "keep": m.Policy.TorrentBackupKeep()})
}

// POST /torrents/backups: queue a backup now.
func (w *Web) apiTorrentBackupNow(rw http.ResponseWriter, r *http.Request) {
	m := w.torrentManager(rw, r)
	if m == nil {
		return
	}
	id, err := torrents.EnqueueBackup(r.Context(), w.jobs, torrents.ReasonManual, actor(r))
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusAccepted, map[string]int64{"job": id})
}

// GET /torrents/backups/{id}: download a backup; ?format=dmm for Debrid Media Manager's (&provider= one account).
func (w *Web) apiTorrentBackupDownload(rw http.ResponseWriter, r *http.Request) {
	m := w.torrentManager(rw, r)
	if m == nil {
		return
	}
	b, err := m.ReadBackup(r.PathValue("id"))
	if errors.Is(err, torrents.ErrNoBackup) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	var body any = b
	name := "theatarr-torrents-" + b.ID + ".json"
	switch r.URL.Query().Get("format") {
	case "", "theatarr":
	case "dmm":
		body = b.DMM(r.URL.Query().Get("provider"))
		name = "dmm-" + b.ID + ".json"
	default:
		badRequest(rw, "format is theatarr or dmm")
		return
	}
	rw.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	writeJSON(rw, http.StatusOK, body)
}

// POST /torrents/backups/import?provider=KEY: the body is a Theatarr or Debrid Media Manager backup.
func (w *Web) apiTorrentBackupImport(rw http.ResponseWriter, r *http.Request) {
	m := w.torrentManager(rw, r)
	if m == nil {
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, 64<<20))
	if err != nil {
		badRequest(rw, "reading the backup: "+err.Error())
		return
	}
	_, info, err := m.ImportBackup(r.Context(), data, r.URL.Query().Get("provider"), actor(r))
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	writeJSON(rw, http.StatusCreated, info)
}

type torrentRestoreBody struct {
	torrents.RestoreRequest
	ConfirmationToken string `json:"confirmation_token,omitempty"`
}

// POST /torrents/restore/preview: what a restore would do; nothing is sent.
func (w *Web) apiTorrentRestorePreview(rw http.ResponseWriter, r *http.Request) {
	m := w.torrentManager(rw, r)
	if m == nil {
		return
	}
	var body torrentRestoreBody
	if err := decodeJSON(r, &body); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	plan, err := m.PlanRestore(r.Context(), body.RestoreRequest)
	if err != nil {
		w.torrentError(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, plan)
}

// POST /torrents/restore: queue the restore the preview's token confirms. 422 with the new plan when it changed.
func (w *Web) apiTorrentRestore(rw http.ResponseWriter, r *http.Request) {
	m := w.torrentManager(rw, r)
	if m == nil {
		return
	}
	var body torrentRestoreBody
	if err := decodeJSON(r, &body); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	plan, err := m.CheckRestore(r.Context(), body.RestoreRequest, body.ConfirmationToken)
	if errors.Is(err, torrents.ErrConfirmation) {
		writeJSON(rw, http.StatusUnprocessableEntity, map[string]any{"error": "explicit confirmation required",
			"plan": plan})
		return
	}
	if err != nil {
		w.torrentError(rw, err)
		return
	}
	id, err := torrents.EnqueueRestore(r.Context(), w.jobs, plan, body.RestoreRequest, actor(r))
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	writeJSON(rw, http.StatusAccepted, map[string]any{"job": id, "plan": plan})
}

type torrentDeleteBody struct {
	Items             []torrents.DeleteSelection `json:"items"`
	ConfirmationToken string                     `json:"confirmation_token,omitempty"`
}

// POST /torrents/delete/preview: what a delete would do, against fresh lists and usage; nothing is deleted.
func (w *Web) apiTorrentDeletePreview(rw http.ResponseWriter, r *http.Request) {
	m := w.torrentManager(rw, r)
	if m == nil {
		return
	}
	var body torrentDeleteBody
	if err := decodeJSON(r, &body); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	plan, err := m.PlanDelete(r.Context(), body.Items)
	if err != nil {
		w.torrentError(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, plan)
}

// POST /torrents/delete: back up, then delete what the preview's token confirms. 422 with the new plan when the
// set changed; 503 when the backup failed (nothing deleted).
func (w *Web) apiTorrentDelete(rw http.ResponseWriter, r *http.Request) {
	m := w.torrentManager(rw, r)
	if m == nil {
		return
	}
	var body torrentDeleteBody
	if err := decodeJSON(r, &body); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	plan, err := m.Delete(r.Context(), body.Items, body.ConfirmationToken, actor(r))
	switch {
	case errors.Is(err, torrents.ErrConfirmation):
		writeJSON(rw, http.StatusUnprocessableEntity, map[string]any{"error": "explicit confirmation required",
			"plan": plan})
	case errors.Is(err, torrents.ErrBackupFailed):
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: err.Error()})
	case err != nil:
		w.torrentError(rw, err)
	default:
		writeJSON(rw, http.StatusOK, plan)
	}
}

// GET /torrents/journal?limit=N: deletes, restores and backups, newest first.
func (w *Web) apiTorrentJournal(rw http.ResponseWriter, r *http.Request) {
	m := w.torrentManager(rw, r)
	if m == nil {
		return
	}
	limit := 200
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			badRequest(rw, "limit must be a positive number")
			return
		}
		limit = n
	}
	list, err := m.Journal(r.Context(), limit)
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"entries": list})
}

// torrentError answers an error of the manager: a missing backup is 404, the rest of the caller's making 400.
func (w *Web) torrentError(rw http.ResponseWriter, err error) {
	if errors.Is(err, torrents.ErrNoBackup) {
		notFound(rw)
		return
	}
	badRequest(rw, err.Error())
}
