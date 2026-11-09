package router

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/store"
)

// Stalled downloads (router.stalled_after, docs/design/router.md "Stalled downloads"; decypharr's remove_stalled_after
// with queue cleanup blacklist_research). A download still in state added (the provider accepted it, it is not
// ready and not fetching: RD still converting the magnet or waiting for a selection, AD queued) stalled_after after
// its add fails as stalled:
//
//   - its hash is blocklisted for the version (source stalled), in the same transaction as the failure;
//   - our add is deleted on the provider, with the same guard as an uncached add: only on Real-Debrid, only when the
//     router proved at the add that the entry was a new one of ours (download.own_add; never set by reconciliation
//     after a crash mid-add, which adopts the entry with reused=0 and own_add unset: it gets its file selection
//     but is never deleted), and only when the entry still holds the hash and no other
//     download or registry content knows its id. Never a reused or shared entry, and never on AllDebrid (every upload of a hash returns the same magnet, so after the add attempt nothing
//     proves another client has not uploaded it meanwhile). When the guard fails the torrent stays;
//   - the title is searched again (research.go).

// BlocklistStalled is the blocklist source of a stalled download.
const BlocklistStalled = "stalled"

// pollRow is one download the poller reads.
type pollRow struct {
	id               int64
	version, pk, tid string
	state            string
	addedAt          time.Time
	ownAdd, reused   int
	filesSelected    int
	// selectedAt is when the router's file selection was recorded (zero: none, or before it was recorded), and
	// cachedCheck the unfinished cached-only check (download.cached_check: "", pending, uncached; cachedcheck.go).
	selectedAt    time.Time
	cachedCheck   string
	hash, indexer string
	// missingPolls and missingSince are the consecutive not-found reads so far (missingRead).
	missingPolls int
	missingSince time.Time
	// progress is the provider progress last recorded, and progressAt when it last rose (zero: not seen
	// downloading yet).
	progress   float64
	progressAt time.Time
	// pollErrors and pollErrorSince are the failed status reads in a row and when they began (notePollAttempt).
	pollErrors     int
	pollErrorSince time.Time
	// pausedSince is when the gated pause of the stall clock began (zero: none; pollGated), and stallOffset the
	// paused time since the add, which stalled_after does not count.
	pausedSince time.Time
	stallOffset time.Duration
}

// stalled reports whether a download has stalled: still added, not downloaded, its provider's stalled_after past its
// add, not counting the time it waited behind the write gate (stallOffset).
func (r *Router) stalled(x pollRow, t *provider.Torrent, now time.Time) bool {
	d := r.stallAfter(x.pk)
	return d > 0 && x.state == StateAdded && !x.addedAt.IsZero() &&
		now.Sub(x.addedAt)-x.stallOffset >= d && t.State != provider.StateDownloaded &&
		t.State != provider.StateDownloading
}

// stallAfter is the stall timeout for provider pk's downloads: router.stalled_after, or the provider's own
// stalled_after; AllDebrid waits at least 25 minutes by default, since its healthy uncached jobs report size 0 and
// zero progress for about ten minutes (https://notes.debridmediamanager.com/zurg/internals/torrent-lifecycle/).
func (r *Router) stallAfter(pk string) time.Duration {
	return r.pol.Providers[pk].EffectiveStalledAfter(r.set.StalledAfter)
}

// noProgress reports whether a download in downloading has made no progress for router.downloading_timeout:
// its progress has not risen since progressAt, and the provider's read now shows no more. The clock starts
// when the poller first sees it downloading, so a download from before progress_at was recorded gets the whole
// timeout.
func (r *Router) noProgress(x pollRow, t *provider.Torrent, now time.Time) bool {
	return r.set.DownloadingTimeout > 0 && x.state == StateDownloading && !x.progressAt.IsZero() &&
		now.Sub(x.progressAt) >= r.set.DownloadingTimeout && t.State != provider.StateDownloaded &&
		t.Progress <= x.progress
}

// failStalled fails and blocklists a stalled download (not fetching after the add, or no progress while
// downloading), then deletes our own Real-Debrid add when the guard holds.
func (r *Router) failStalled(ctx context.Context, x pollRow, t *provider.Torrent, now time.Time, why string) error {
	kept := ""
	if x.reused == 1 {
		kept = "this reused torrent is shared and was not deleted"
	} else {
		kept = r.deleteStalled(ctx, x, t)
	}
	if kept == "" {
		why += "; our add was deleted on " + x.pk
	} else {
		why += "; kept on " + x.pk + ": " + kept
	}
	at := store.Millis(now)
	err := r.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var titleID int64
		var release string
		if err := tx.QueryRowContext(ctx, `SELECT title_id, release_title FROM download WHERE id = ?`, x.id).
			Scan(&titleID, &release); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE download SET state = 'failed', error = ?,
			provider_status = COALESCE(NULLIF(?, ''), provider_status),
			updated_at = ? WHERE id = ? AND state IN ('added', 'downloading') AND provider_torrent_id = ?`, why,
			t.Status, at, x.id, x.tid)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return nil // ended or moved on meanwhile: its own outcome stands
		}
		return Blocklist(ctx, tx, x.version, x.hash, titleID, x.id, release, why, BlocklistStalled, at)
	})
	if err != nil {
		return fmt.Errorf("router: download %d: %w", x.id, err)
	}
	r.log.Warn("download stalled; blocklisted", "download", x.id, "provider", x.pk, "torrent", x.tid,
		"version", x.version, "why", why)
	return nil
}

// deleteStalled deletes the stalled torrent when it is provably our own Real-Debrid add; it returns why it did not
// ("" when deleted).
//
// Only our own stalled RD add is deleted.
// Debrid torrents are shared and never deleted, except our own just-made add; Real-Debrid creates a new torrent id
// for every addMagnet, so an id we got back and no one else knows is ours. AllDebrid has no such guarantee (see
// router.alldebrid_delete_uncached), so it is never deleted here.
func (r *Router) deleteStalled(ctx context.Context, x pollRow, t *provider.Torrent) string {
	if why, err := r.ownRDAdd(ctx, x, t, "when it stalls"); err != nil {
		return "checking the torrent id failed: " + err.Error()
	} else if why != "" {
		return why
	}
	if err := r.mayWrite(x.version); err != nil {
		return err.Error()
	}
	del, ok := r.adders[x.pk].(adder.StalledDeleter)
	if !ok {
		return "the adder for " + x.pk + " cannot delete"
	}
	wctx, cancel := context.WithTimeout(ctx, providerWriteLimit)
	defer cancel()
	if err := del.DeleteOwnStalledAdd(wctx, x.tid, adder.OwnAdd{NotOnAccountBefore: true,
		NewEntry: true}); err != nil {
		return "the delete failed: " + err.Error()
	}
	r.log.Info("stalled add of ours deleted", "provider", x.pk, "torrent", x.tid, "download", x.id)
	return ""
}

// ownRDAdd is the ownership half of the stalled delete's guard (deleteStalled), shared with the poller's owed
// cached-only delete (cachedcheck.go): why torrent t of download x is not provably our own unshared Real-Debrid add
// ("" when it is). when ends the message for a provider other than Real-Debrid. err: the id check could not be read.
func (r *Router) ownRDAdd(ctx context.Context, x pollRow, t *provider.Torrent, when string) (string, error) {
	pv := r.pol.Providers[x.pk]
	switch {
	case pv == nil || pv.Kind != provider.KindRealDebrid:
		return "only a Real-Debrid add of ours is deleted " + when, nil
	case x.reused == 1:
		return "the torrent was reused from the account", nil
	case x.ownAdd != 1:
		return "the add was not provably a new entry of ours", nil
	case t.Hash == "" || !strings.EqualFold(t.Hash, x.hash):
		return "the entry does not show the hash added", nil
	}
	var known string
	err := r.st.Writer().QueryRowContext(ctx, `SELECT 'download ' || id FROM download WHERE provider = ?
		AND provider_torrent_id = ? AND id != ? UNION ALL SELECT 'registry content ' || c.info_hash
		FROM content_provider cp JOIN content c ON c.id = cp.content_id WHERE cp.provider = ? AND cp.torrent_id = ?
		AND c.info_hash != ? LIMIT 1`, x.pk, x.tid, x.id, x.pk, x.tid, x.hash).Scan(&known)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if known != "" {
		return "torrent " + x.tid + " is known to " + known, nil
	}
	return "", nil
}

// deleteUnselected deletes our own Real-Debrid add that fails before any file was selected (a dangerous file, no
// video) with the stalled delete's proof (deleteStalled); otherwise it sat on the shared account in
// waiting_files_selection for good. The failure's reason stays the release's; the log says what happened to the
// torrent. Anything but our own RD add is left alone, as before.
func (r *Router) deleteUnselected(ctx context.Context, dl int64, t *provider.Torrent) {
	x := pollRow{id: dl}
	var ownAdd sql.NullInt64
	err := r.st.Writer().QueryRowContext(ctx, `SELECT version_key, COALESCE(provider, ''),
		COALESCE(provider_torrent_id, ''), state, own_add, reused, info_hash, indexer FROM download WHERE id = ?`, dl).
		Scan(&x.version, &x.pk, &x.tid, &x.state, &ownAdd, &x.reused, &x.hash, &x.indexer)
	if err != nil {
		r.log.Warn("failed add before selection: kept; reading the download failed", "download", dl, "err", err)
		return
	}
	x.ownAdd = int(ownAdd.Int64)
	if pv := r.pol.Providers[x.pk]; pv == nil || pv.Kind != provider.KindRealDebrid || x.reused == 1 ||
		x.tid == "" || x.tid != t.ID {
		return
	}
	if kept := r.deleteStalled(ctx, x, t); kept != "" {
		r.log.Info("failed add before selection kept", "provider", x.pk, "torrent", x.tid, "download", dl,
			"why", kept)
	}
}

// endUnavailable ends a download that reached its stall limit while its provider was down (most of the provider's
// downloads failing at once, or the account check failing or refused): zurg-style, the provider gave up, not the
// release. Nothing is blocklisted. The provider's adds back off (router pacing, unless a
// back-off already runs), so the release, routed again, goes to the next provider in the version's order or waits
// for this one. Like a blind stall, nothing is deleted: the torrent was not seen, so its ownership cannot be proved
// on it. Without a route queue (a CLI poll) the download fails, still not blocklisted.
func (r *Router) endUnavailable(ctx context.Context, x pollRow, why string, now time.Time) error {
	why = strings.Replace(why, "stalled: ", "", 1)
	why = "provider unavailable: " + x.pk + " was down (" + why + "); not blocklisted, the release is routed again"
	if pv := r.pol.Providers[x.pk]; pv != nil {
		if err := updatePace(ctx, r.st, x.pk, now, func(_ *sql.Tx, p *pace) (bool, error) {
			if p.BackoffUntil.After(now) {
				// A back-off runs already: it now means the provider is down (no reuse during it).
				save := !p.BackoffDown
				p.BackoffDown = true
				return save, nil
			}
			p.fail(now, pv.Pacing, "provider unavailable: downloads stalled while it was down", time.Time{}, false, true)
			return true, nil
		}); err != nil {
			r.log.Warn("pacing", "provider", x.pk, "err", err)
		}
	}
	req, err := r.requestOf(ctx, x.id)
	if err != nil {
		return err
	}
	if r.q == nil || req == nil {
		r.log.Warn("poll: provider unavailable past the stall limit; download failed without blocklisting (no route "+
			"queue)", "download", x.id, "provider", x.pk, "torrent", x.tid)
		return r.failDownload(ctx, x.id, why, StateAdded, StateDownloading)
	}
	at := store.Millis(now)
	err = r.st.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE download SET state = 'queued', provider = NULL,
			provider_torrent_id = NULL, provider_status = '', name = '', progress = 0, progress_at = NULL, cached = NULL,
			files_selected = 0, selected_at = NULL, reused = 0, own_add = NULL, added_at = NULL, cached_check = NULL,
			missing_polls = 0, missing_since = NULL, poll_errors = 0, poll_error_since = NULL, poll_outage_since = NULL,
			stall_offset = 0, error = ?,
			updated_at = ? WHERE id = ? AND state IN ('added', 'downloading') AND provider = ?
			AND provider_torrent_id = ?`, why, at, x.id, x.pk, x.tid)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return nil // moved on meanwhile
		}
		p := routePayload{Request: *req}
		_, _, err = enqueueRoute(ctx, r.q, tx, p, now)
		return err
	})
	if err != nil {
		return fmt.Errorf("router: download %d: %w", x.id, err)
	}
	r.log.Warn("poll: provider unavailable past the stall limit; the release is routed again, nothing blocklisted",
		"download", x.id, "provider", x.pk, "torrent", x.tid)
	return nil
}
