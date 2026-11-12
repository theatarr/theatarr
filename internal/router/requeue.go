package router

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// requeueReused handles a reused torrent that stayed missing past missingGrace. The torrent was
// never ours: another download, the registry or the account held the hash, and it has left the account since. That
// says nothing against the release, so the download is not failed or blocklisted: it goes back to queued with no
// provider, its registry row is marked absent, and a route job resumes it with its own release (a fresh add, or a
// reuse of another entry that holds the hash now). Without a job queue (a CLI poll) it fails, still unblocklisted.
func (r *Router) requeueReused(ctx context.Context, x pollRow) error {
	queued, err := r.requeue(ctx, x, 1, "the reused torrent "+x.tid+" left "+x.pk+"; the release is routed again",
		"the reused torrent "+x.tid+" left "+x.pk+"; not blocklisted")
	if err != nil {
		return err
	}
	if !queued {
		r.log.Warn("poll: reused torrent gone; download failed without blocklisting (no route queue)",
			"download", x.id, "provider", x.pk, "torrent", x.tid)
		return nil
	}
	r.log.Warn("poll: reused torrent gone; the release is routed again, nothing blocklisted", "download", x.id,
		"provider", x.pk, "torrent", x.tid)
	return nil
}

// requeue puts download x, still on torrent x.tid and with download.reused as given, back to queued with no provider (why on the row), marks the
// torrent's registry row absent, and queues a route job that resumes it with its own release; nothing is blocklisted.
// Without a job queue (a CLI poll) it fails with failWhy, still unblocklisted, and reports false.
func (r *Router) requeue(ctx context.Context, x pollRow, reused int, why, failWhy string) (bool, error) {
	req, err := r.requestOf(ctx, x.id)
	if err != nil {
		return false, err
	}
	if r.q == nil || req == nil {
		return false, r.failDownload(ctx, x.id, failWhy, StateAdded, StateDownloading)
	}
	now := store.Millis(r.now())
	err = r.st.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE download SET state = 'queued', provider = NULL,
			provider_torrent_id = NULL, provider_status = '', name = '', progress = 0, progress_at = NULL, cached = NULL,
			files_selected = 0, selected_at = NULL, reused = 0, own_add = NULL, added_at = NULL, cached_check = NULL,
			missing_polls = 0, missing_since = NULL, poll_errors = 0, poll_error_since = NULL, poll_outage_since = NULL,
			stall_offset = 0, error = ?, updated_at = ? WHERE id = ? AND reused = ? AND state IN ('added', 'downloading')
			AND provider = ? AND provider_torrent_id = ?`, why, now, x.id, reused, x.pk, x.tid)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return nil // moved on meanwhile
		}
		if _, err := tx.ExecContext(ctx, `UPDATE content_provider SET state = 'absent', absent_at = ?
			WHERE provider = ? AND torrent_id = ? AND state = 'present'`, now, x.pk, x.tid); err != nil {
			return err
		}
		p := routePayload{Request: *req}
		_, _, err = enqueueRoute(ctx, r.q, tx, p, r.now())
		return err
	})
	if err != nil {
		return false, fmt.Errorf("router: download %d: %w", x.id, err)
	}
	return true, nil
}

// requestOf rebuilds the route request that resumes download dl from its row (DownloadRequest).
func (r *Router) requestOf(ctx context.Context, dl int64) (*Request, error) {
	return DownloadRequest(ctx, r.st.Writer(), r.pol, dl)
}

// DownloadRequest rebuilds the route request of download dl from its row: its release (release_data, with the cache
// evidence and extras the provider order uses), the decision's quality, score, formats, episodes and episode mapping,
// its source and the policy revision it was decided under, resuming dl. The kind and privacy label are the indexer's in
// pol when it knows the indexer. nil when the row is gone. A
// retry by hand (arrview) routes the same release as a new download.
func DownloadRequest(ctx context.Context, db *sql.DB, pol *policy.Policy, dl int64) (*Request, error) {
	var req Request
	var c Candidate
	var data, eps string
	var formats, epMap, source sql.NullString
	var size sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT title_id, version_id, version_key, source, info_hash,
		release_title, indexer, quality, score, size_bytes, episodes, formats, episode_map, COALESCE(release_data, ''),
		COALESCE(policy_rev, '') FROM download WHERE id = ?`, dl).Scan(&req.TitleID, &req.VersionID, &req.Version, &source,
		&c.Release.InfoHash, &c.Release.Title, &c.Release.Indexer, &c.Quality, &c.Score, &size, &eps, &formats, &epMap,
		&data, &req.DecisionRev) // the revision the row's candidate was decided under
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("router: download %d: %w", dl, err)
	}
	if data != "" {
		rel := c.Release
		if json.Unmarshal([]byte(data), &c.Release) != nil {
			c.Release = rel
			c.Release.Seeders, c.Release.Peers = -1, -1 // invalid legacy data carries no seeder evidence
		}
		c.Release.InfoHash, c.Release.Indexer = rel.InfoHash, rel.Indexer
		var route storedRelease
		if json.Unmarshal([]byte(data), &route) == nil {
			c.Search, c.Criteria = route.RouteSearch, route.RouteCriteria
			c.Identity, c.Origin = route.RouteIdentity, route.RouteOrigin
		}
	} else {
		c.Release.Seeders, c.Release.Peers = -1, -1 // a row from before release_data: not known
	}
	if c.Release.Size == 0 {
		c.Release.Size = size.Int64
	}
	if ix := pol.Indexers[c.Release.Indexer]; ix != nil {
		c.Release.Kind, c.Release.Privacy = ix.Kind, ix.Privacy
	}
	c.Release.InfoHash = indexer.NormalizeHash(c.Release.InfoHash)
	_ = json.Unmarshal([]byte(eps), &c.Episodes)
	if formats.Valid {
		_ = json.Unmarshal([]byte(formats.String), &c.Formats)
	}
	if epMap.Valid && epMap.String != "" {
		var m decide.EpisodeMap
		if json.Unmarshal([]byte(epMap.String), &m) == nil {
			c.EpisodeMap = &m
		}
	}
	req.Candidates, req.Source, req.DownloadID = []Candidate{c}, source.String, dl
	return &req, nil
}
