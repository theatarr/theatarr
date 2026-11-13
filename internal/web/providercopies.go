package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/router"
)

// "Add to other providers" (docs/design/router.md "Copies to other providers"): a file's torrent is added to every
// configured provider that does not hold it yet. The work is the router's copy jobs; these routes resolve the hash
// and answer the rows. While paused they answer 409 with the Pause words.

// Copier is the active router's copy work (router.QueueCopies, router.TorrentHash).
type Copier interface {
	QueueCopies(ctx context.Context, req router.CopyRequest) ([]router.CopyView, error)
	TorrentHash(ctx context.Context, pk, id string) (hash, name string, err error)
}

// CopiesResult is the answer of both routes: the copies of the torrent, one per provider that did not hold it.
type CopiesResult struct {
	Copies []router.CopyView `json:"copies"`
}

// CopyTitleRequest names a library's file: a movie version's, or with episode_id an episode's.
type CopyTitleRequest struct {
	Version   string `json:"version"`
	EpisodeID int64  `json:"episode_id,omitempty"`
}

// CopyTorrentRequest names a Torrents row: its provider and torrent id.
type CopyTorrentRequest struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
}

func (w *Web) mountProviderCopiesAPI(mux *http.ServeMux) {
	w.handle(mux, "POST "+APIBase+"/titles/{id}/copies", SignedIn, auth.PermManage, w.apiCopyTitle)
	w.handle(mux, "POST "+APIBase+"/torrents/copy", SignedIn, auth.PermAdmin, w.apiCopyTorrent)
}

// copier is the request's router, or nil after answering 503.
func (w *Web) copier(rw http.ResponseWriter, r *http.Request) Copier {
	if w.copies != nil {
		ctx := r.Context()
		if w.captureRuntime != nil {
			ctx = w.captureRuntime(ctx)
		}
		if c := w.copies(ctx); c != nil {
			return c
		}
	}
	writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "the router is not running"})
	return nil
}

// POST /titles/{id}/copies {version, episode_id?}: add the file's torrent to the other providers.
func (w *Web) apiCopyTitle(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	var req CopyTitleRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	ctx := r.Context()
	var versionID int64
	err := w.st.Reader().QueryRowContext(ctx, `SELECT id FROM version WHERE title_id = ? AND policy_key = ?`, id,
		req.Version).Scan(&versionID)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	hash, name, err := fileTorrent(ctx, w.st.Reader(), versionID, req.EpisodeID)
	if err != nil {
		w.internal(rw, err)
		return
	}
	w.queueCopies(rw, r, router.CopyRequest{Hash: hash, Name: name, TitleID: id})
}

// POST /torrents/copy {provider, id}: add a Torrents row's torrent to the other providers.
func (w *Web) apiCopyTorrent(rw http.ResponseWriter, r *http.Request) {
	var req CopyTorrentRequest
	if err := decodeJSON(r, &req); err != nil || req.Provider == "" || req.ID == "" {
		badRequest(rw, "provider and id are required")
		return
	}
	c := w.copier(rw, r)
	if c == nil {
		return
	}
	hash, name, err := c.TorrentHash(r.Context(), req.Provider, req.ID)
	if err != nil {
		w.internal(rw, err)
		return
	}
	w.queueCopiesWith(rw, r, c, router.CopyRequest{Hash: hash, Name: name, Except: req.Provider})
}

func (w *Web) queueCopies(rw http.ResponseWriter, r *http.Request, req router.CopyRequest) {
	if c := w.copier(rw, r); c != nil {
		w.queueCopiesWith(rw, r, c, req)
	}
}

func (w *Web) queueCopiesWith(rw http.ResponseWriter, r *http.Request, c Copier, req router.CopyRequest) {
	out, err := c.QueueCopies(r.Context(), req)
	switch {
	case errors.Is(err, router.ErrCopyPaused):
		writeJSON(rw, http.StatusConflict, apiError{Error: err.Error()})
		return
	case errors.Is(err, router.ErrCopyNoHash):
		writeJSON(rw, http.StatusUnprocessableEntity, apiError{Error: err.Error()})
		return
	case err != nil:
		w.internal(rw, err)
		return
	}
	if out == nil {
		out = []router.CopyView{}
	}
	writeJSON(rw, http.StatusOK, CopiesResult{Copies: out})
}

// fileTorrent is the torrent hash and name on record for a version's file (with episode, the episode's): the newest
// active link's (its content, download, file metadata or adopted source), else the version's arr file's content.
func fileTorrent(ctx context.Context, db *sql.DB, versionID, episodeID int64) (hash, name string, err error) {
	ep := ""
	args := []any{versionID}
	if episodeID != 0 {
		ep = ` AND EXISTS (SELECT 1 FROM link_episode le WHERE le.link_id = l.id AND le.episode_id = ?)`
		args = append(args, episodeID)
	}
	err = db.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(c.info_hash, ''), NULLIF(d.info_hash, ''),
			CASE WHEN json_valid(l.file_metadata) THEN NULLIF(json_extract(l.file_metadata, '$.InfoHash'), '') END,
			CASE WHEN json_valid(l.adopted_source) THEN NULLIF(json_extract(l.adopted_source, '$.hash'), '') END, ''),
		COALESCE(NULLIF(l.release_title, ''),
			CASE WHEN json_valid(l.file_metadata) THEN NULLIF(json_extract(l.file_metadata, '$.SceneName'), '') END,
			CASE WHEN json_valid(l.adopted_source) THEN NULLIF(json_extract(l.adopted_source, '$.release'), '') END,
			d.release_title, '')
		FROM link l JOIN link_version lv ON lv.link_id = l.id LEFT JOIN content_file cf ON cf.id = l.content_file_id
		LEFT JOIN content c ON c.id = cf.content_id LEFT JOIN download d ON d.id = l.download_id
		WHERE lv.version_id = ? AND l.state = 'active'`+ep+` ORDER BY l.created_at DESC, l.id DESC LIMIT 1`,
		args...).Scan(&hash, &name)
	if err == nil {
		return strings.ToLower(hash), name, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	ep = ""
	if episodeID != 0 {
		ep = ` AND EXISTS (SELECT 1 FROM version_file_episode fe WHERE fe.version_file_id = vf.id AND fe.episode_id = ?)`
	}
	err = db.QueryRowContext(ctx, `SELECT COALESCE(c.info_hash, ''), COALESCE(NULLIF(vf.release_title, ''),
		vf.scene_name, '') FROM version_file vf LEFT JOIN content c ON c.id = vf.content_id
		WHERE vf.version_id = ?`+ep+` ORDER BY vf.added_at DESC LIMIT 1`, args...).Scan(&hash, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return strings.ToLower(hash), name, err
}

// fileCopies reads the copies of the torrents behind hashes for the file objects (see files only).
func (w *Web) fileCopies(ctx context.Context, hashes []string) map[string][]router.CopyView {
	out, err := router.CopiesByHash(ctx, w.st.Reader(), hashes)
	if err != nil {
		w.log.Warn("copies to other providers not read", "err", err)
		return nil
	}
	return out
}
