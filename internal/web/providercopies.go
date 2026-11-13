package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/router"
)

// "Add to other providers" (docs/design/router.md "Copies to other providers"): a file's torrent is added to every
// configured provider that does not hold it yet. The work is the router's copy jobs; these routes resolve the hash
// and answer the rows. While paused they answer 409 with the Pause words.

// Copier is the active router's copy work (router.QueueCopies, router.TorrentHash).
type Copier interface {
	QueueCopies(ctx context.Context, req router.CopyRequest) ([]router.CopyView, error)
	TorrentHash(ctx context.Context, pk, id string) (hash string, names []string, err error)
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
	w.handle(mux, "GET "+APIBase+"/torrents/copies", SignedIn, auth.PermAdmin, w.apiTorrentCopies)
}

// GET /torrents/copies?hash=: the copies of one torrent, which the Torrents page reads again while one is unfinished
// instead of the whole report (which reads every account).
func (w *Web) apiTorrentCopies(rw http.ResponseWriter, r *http.Request) {
	hash := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hash")))
	if hash == "" {
		badRequest(rw, "hash is required")
		return
	}
	out, err := router.CopiesByHash(r.Context(), w.st.Reader(), []string{hash})
	if err != nil {
		w.internal(rw, err)
		return
	}
	copies := out[hash]
	if copies == nil {
		copies = []router.CopyView{}
	}
	writeJSON(rw, http.StatusOK, CopiesResult{Copies: w.withCodes(copies)})
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
	hash, names, found, err := w.fileTorrent(r.Context(), id, req.Version, req.EpisodeID)
	if err != nil {
		w.internal(rw, err)
		return
	}
	if !found {
		notFound(rw)
		return
	}
	w.queueCopies(rw, r, router.CopyRequest{Hash: hash, Names: names, TitleID: id})
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
	hash, names, err := c.TorrentHash(r.Context(), req.Provider, req.ID)
	if err != nil {
		w.internal(rw, err)
		return
	}
	w.queueCopiesWith(rw, r, c, router.CopyRequest{Hash: hash, Names: names, Except: req.Provider})
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
		writeJSON(rw, http.StatusConflict, apiError{Error: router.CopyPausedWords})
		return
	case errors.Is(err, router.ErrCopyNoHash):
		writeJSON(rw, http.StatusUnprocessableEntity, apiError{Error: router.CopyNoHashWords})
		return
	case err != nil:
		w.internal(rw, err)
		return
	}
	if out == nil {
		out = []router.CopyView{}
	}
	writeJSON(rw, http.StatusOK, CopiesResult{Copies: w.withCodes(out)})
}

// fileTorrent is the torrent hash and name of a library's file exactly as the file objects show them (the movie's
// current file on the title page, the episode window's file: linkHashSQL, fileHashSQL), so "Cached on", the Torrent
// link and this action always name the same torrent. found is false for an unknown title or version.
func (w *Web) fileTorrent(ctx context.Context, titleID int64, version string, episodeID int64) (hash string,
	names []string, found bool, err error) {
	if episodeID != 0 {
		var versionID int64
		err := w.st.Reader().QueryRowContext(ctx, `SELECT v.id FROM version v JOIN episode e ON e.title_id = v.title_id
			WHERE v.title_id = ? AND v.policy_key = ? AND e.id = ?`, titleID, version, episodeID).Scan(&versionID)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil, false, nil
		}
		if err != nil {
			return "", nil, false, err
		}
		f, err := w.episodeFile(ctx, w.st.Reader(), versionID, episodeID)
		if err != nil || f == nil {
			return "", nil, true, err
		}
		return strings.ToLower(f.InfoHash), []string{f.ReleaseTitle, f.SceneName}, true, nil
	}
	d, err := w.getTitle(ctx, titleID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && d == nil {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	if err := w.enrichTitle(ctx, d); err != nil {
		return "", nil, false, err
	}
	for _, v := range d.Versions {
		if v.Key != version {
			continue
		}
		if v.Current == nil {
			return "", nil, true, nil
		}
		return strings.ToLower(v.Current.InfoHash), []string{v.Current.ReleaseTitle}, true, nil
	}
	return "", nil, false, nil
}

// fileCopies reads the copies of the torrents behind hashes for the file objects (see files only), with their codes.
func (w *Web) fileCopies(ctx context.Context, hashes []string) map[string][]router.CopyView {
	out, err := router.CopiesByHash(ctx, w.st.Reader(), hashes)
	if err != nil {
		w.log.Warn("copies to other providers not read", "err", err)
		return nil
	}
	for h, vs := range out {
		out[h] = w.withCodes(vs)
	}
	return out
}

// withCodes fills each copy's cache code (rd, ad, tb) from its provider key, so the UI labels and matches it as Cached
// on does.
func (w *Web) withCodes(vs []router.CopyView) []router.CopyView {
	for i := range vs {
		if w.pol != nil {
			if p := w.pol.Providers[vs[i].Provider]; p != nil {
				vs[i].Code = indexer.CacheCode(p.Kind)
			}
		}
	}
	return vs
}
