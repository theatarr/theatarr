package router

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/store"
)

// Unfinished cached-only checks (docs/design/router.md "Cached-only on Real-Debrid and AllDebrid"). settle (uncached.go)
// may end without finishing its check: the reads after the add gave no verdict (a failed status read or selection,
// the route's context ended, an odd status), or the verdict was uncached and the delete of our own add was blocked
// by the stop switch (mayWrite) or the job lease, or failed. It records that on the download (download.cached_check)
// and the poller finishes it after a successful read, before the stall timeouts:
//
//   - router.cached_only off: the check is dropped; the add stays.
//   - downloaded: the check is dropped and nothing deleted (it finished; no cache evidence is written).
//   - pending, queued or downloading router.cached_grace after the add (AllDebrid) or after our recorded or
//     inferred file selection's answer (Real-Debrid): uncached. Real-Debrid with no selection recorded is no verdict.
//   - uncached: deleted with the stalled delete's guard (ownRDAdd: Real-Debrid only, our own unshared add) and the
//     route's journaled delete, recorded uncached for miss_ttl and counted, and routed again with nothing
//     blocklisted (requeue): the route job tries the next provider, or, when every provider is a known miss, fails
//     the download and searches the title again (ReasonExhausted), as a route does. The largest-video second add
//     (altRetry) is not tried from here.
//   - the gate closed, a failed read or delete, or pending within the grace: the check stays for the next poll, and
//     the stall timeouts do not end the download meanwhile. A delete whose outcome is unknown keeps its journal; the
//     poller that then finds the torrent gone takes it as our delete (owedDeleted).
//   - not provably ours, or on AllDebrid (its proof was only good at the add): the check is dropped, the add kept,
//     and the reason left on the download for review.

// Values of download.cached_check.
const (
	checkPending  = "pending"  // no verdict yet
	checkUncached = "uncached" // verdict uncached; the delete of our add is owed
)

// setCachedCheck records an unfinished check on download dl while it still holds torrent tid on pk.
func (r *Router) setCachedCheck(ctx context.Context, dl int64, pk, tid, mark string) error {
	_, err := r.st.Writer().ExecContext(ctx, `UPDATE download SET cached_check = ? WHERE id = ? AND provider = ?
		AND provider_torrent_id = ? AND state IN ('added', 'downloading')`, mark, dl, pk, tid)
	if err != nil {
		return fmt.Errorf("router: download %d: %w", dl, err)
	}
	return nil
}

// dropCachedCheck ends download x's unfinished check without a delete; why, when set, is left on the row.
func (r *Router) dropCachedCheck(ctx context.Context, x pollRow, why string) error {
	_, err := r.st.Writer().ExecContext(ctx, `UPDATE download SET cached_check = NULL,
		error = COALESCE(NULLIF(?, ''), error) WHERE id = ? AND provider_torrent_id = ?`, why, x.id, x.tid)
	if err != nil {
		return fmt.Errorf("router: download %d: %w", x.id, err)
	}
	return nil
}

// checkResult is how resumeCachedCheck left a download's poll.
type checkResult int

const (
	checkGoOn checkResult = iota // no open check, or one that does not hold the poll: on as before
	// checkHold: the check stays open and settles soon (the delete owed waits for the gate or a working delete, or
	// the grace after the selection runs): the stall timeouts do not end the download this poll.
	checkHold
	checkDone // deleted and routed again (or failed): the poll of the download ends here
)

// resumeCachedCheck finishes download x's unfinished cached-only check from the read t, under r.mu.
func (r *Router) resumeCachedCheck(ctx context.Context, x pollRow, t *provider.Torrent, now time.Time,
	sum *PollSummary) (checkResult, error) {
	if x.cachedCheck == "" {
		return checkGoOn, nil
	}
	switch {
	case !r.set.CachedOnly:
		r.log.Info("poll: cached_only is off; the unfinished cache check is dropped and the add stays",
			"download", x.id, "provider", x.pk, "torrent", x.tid)
		return checkGoOn, r.dropCachedCheck(ctx, x, "")
	case t.State == provider.StateDownloaded:
		return checkGoOn, r.dropCachedCheck(ctx, x, "")
	}
	if x.cachedCheck == checkPending {
		if (t.State != provider.StateQueued && t.State != provider.StateDownloading) || t.Status == rdWaitingFiles {
			return checkGoOn, nil
		}
		from := x.addedAt
		if pv := r.pol.Providers[x.pk]; pv != nil && pv.Kind == provider.KindRealDebrid {
			// Real-Debrid: no verdict before our file selection is recorded or inferred, and the grace counts from
			// it. A selection we sent whose answer was lost is inferred here, before the stall timeouts (which
			// would otherwise end the download before applyStatus infers it), and holds the poll: its grace runs
			// from now. A converting magnet, or one with no selection sent, is no verdict; the stall timeout still
			// applies to it.
			if x.filesSelected == 0 {
				var reused, tried int
				if err := r.st.Writer().QueryRowContext(ctx, `SELECT reused, COALESCE(error, '') LIKE
					'selecting files: %' FROM download WHERE id = ?`, x.id).Scan(&reused, &tried); err != nil {
					return checkGoOn, fmt.Errorf("router: download %d: %w", x.id, err)
				}
				inferred, err := r.inferSelection(ctx, x.id, x.pk, t, reused, tried, store.Millis(r.now()))
				if err != nil || !inferred {
					return checkGoOn, err
				}
				return checkHold, nil
			}
			if x.selectedAt.IsZero() {
				return checkGoOn, nil
			}
			from = x.selectedAt
		}
		if from.IsZero() {
			return checkGoOn, nil
		}
		if now.Sub(from) < r.set.CachedGrace {
			return checkHold, nil
		}
		if err := r.setCachedCheck(ctx, x.id, x.pk, x.tid, checkUncached); err != nil {
			return checkGoOn, err
		}
		r.log.Info("cached-only: the add is uncached (checked again by the poller)", "download", x.id,
			"provider", x.pk, "torrent", x.tid, "status", t.Status)
	}
	why, err := r.ownRDAdd(ctx, x, t, "after the add's own check")
	if err != nil {
		r.log.Warn("cached-only: checking the torrent id failed; the delete is tried at the next poll",
			"download", x.id, "provider", x.pk, "torrent", x.tid, "err", err)
		return checkHold, nil
	}
	done := false
	if why == "" {
		if done, why, err = r.deleteOwnPolled(ctx, x); err != nil {
			return checkGoOn, err
		}
	}
	if why != "" {
		why = "uncached add kept: " + why + "; review it"
		r.log.Warn("cached-only: uncached add kept after the poller's check; review it", "download", x.id,
			"provider", x.pk, "torrent", x.tid, "why", why)
		return checkGoOn, r.dropCachedCheck(ctx, x, why)
	}
	if !done {
		return checkHold, nil
	}
	return checkDone, r.owedDeleted(ctx, x, now, sum)
}

// deleteOwnPolled sends the route's journaled delete (settle) of download x's torrent, our own Real-Debrid add as
// ownRDAdd proved it (resumeCachedCheck, pollNameRefused): a crash after it, or an answer that does not say, leaves
// the journal, and the poller that then finds the torrent gone takes it as our delete (owedDeleted). done: it is gone.
// Otherwise why says why it cannot be deleted at all, or is "" when it waits for the gate or the delete failed: it is
// tried again at the next poll.
func (r *Router) deleteOwnPolled(ctx context.Context, x pollRow) (done bool, why string, err error) {
	del, ok := r.adders[x.pk].(adder.StalledDeleter)
	if !ok {
		return false, "the adder for " + x.pk + " cannot delete", nil
	}
	if err := r.mayWrite(x.version); err != nil {
		r.log.Info("the delete of our add waits for the gate", "download", x.id, "provider", x.pk,
			"torrent", x.tid, "why", err)
		return false, "", nil
	}
	if err := r.noteDeleting(ctx, x.id, x.pk, x.tid); err != nil {
		return false, "", err
	}
	wctx, cancel := context.WithTimeout(ctx, providerWriteLimit)
	err = del.DeleteOwnStalledAdd(wctx, x.tid, adder.OwnAdd{NotOnAccountBefore: true, NewEntry: true})
	cancel()
	if err != nil {
		gone, present := r.readAfterDelete(ctx, x.pk, x.tid)
		if !gone {
			r.log.Warn("the delete of our add failed; tried again at the next poll", "download", x.id,
				"provider", x.pk, "torrent", x.tid, "err", err, "still_there", present)
			if present {
				// Not deleted: the journal ends. Otherwise it stays, for a later not-found.
				if _, err := r.st.Writer().ExecContext(ctx, `UPDATE download SET error = NULL WHERE id = ?
					AND error LIKE ?`, x.id, deletingPrefix+"%"); err != nil {
					return false, "", fmt.Errorf("router: download %d: %w", x.id, err)
				}
			}
			return false, "", nil
		}
	}
	return true, "", nil
}

// pollNameRefused is settleNameRefused from the poller: download x, our Real-Debrid add waiting for its selection,
// would select a video Real-Debrid refuses by name (nr). It is deleted when ownRDAdd proves it ours (deleteOwnPolled;
// one that waits for the gate or failed is tried again at the next poll) and kept otherwise, with nothing held for
// review; the miss is recorded for the content's miss TTL and the download routed again (requeue), nothing
// blocklisted.
func (r *Router) pollNameRefused(ctx context.Context, x pollRow, t *provider.Torrent, nr *nameRefusedError,
	now time.Time, sum *PollSummary) error {
	why, err := r.ownRDAdd(ctx, x, t, "")
	if err != nil {
		r.log.Warn("checking the torrent id failed; tried again at the next poll", "download", x.id,
			"provider", x.pk, "torrent", x.tid, "err", err)
		return nil
	}
	if why == "" {
		done, no, err := r.deleteOwnPolled(ctx, x)
		if err != nil || (!done && no == "") {
			return err
		}
		why = no
	}
	done := "deleted"
	if why != "" {
		done = "kept (" + why + ")"
	}
	rctx := context.WithoutCancel(ctx)
	if err := r.setCache(rctx, x.hash, x.pk, "miss", "rd_name", now.Add(r.downloadMissTTL(rctx, x, now)), now); err != nil {
		return err
	}
	r.log.Warn(nr.Error()+"; a miss on "+x.pk+", the release is routed again", "download", x.id, "provider", x.pk,
		"torrent", x.tid, "our_add", done)
	queued, err := r.requeue(rctx, x, 0, nr.Error()+"; our add was "+done,
		nr.Error()+"; our add was "+done+"; not blocklisted (no route queue)")
	if err == nil && !queued {
		sum.Failed++
	}
	return err
}

// pollTooLarge is download x on a provider that refuses its torrent as too big for it after the add (AllDebrid
// statusCode 8, provider.Torrent.TooLarge): as Real-Debrid's 29 and TorBox's DOWNLOAD_TOO_LARGE at the add, a miss
// there for the content's miss TTL and the release routed again (requeue) to the next provider; nothing blocklisted.
func (r *Router) pollTooLarge(ctx context.Context, x pollRow, now time.Time, sum *PollSummary) error {
	rctx := context.WithoutCancel(ctx)
	if err := r.setCache(rctx, x.hash, x.pk, "miss", "too_large", now.Add(r.downloadMissTTL(rctx, x, now)), now); err != nil {
		return err
	}
	why := "too big for " + x.pk + " (File too big.)"
	r.log.Warn(why+"; a miss on "+x.pk+", the release is routed again", "download", x.id, "provider", x.pk, "torrent", x.tid)
	// The magnet stays on the account, and its registry row with it: Torrents lists it until the owner clears it.
	queued, err := r.requeueWith(rctx, x, x.reused, why+"; the release is routed again", why+"; not blocklisted (no route queue)",
		false)
	if err == nil && !queued {
		sum.Failed++
	}
	return err
}

// owedDeleted records an owed delete of our uncached add as done: the miss (provider_cache uncached for the content's
// miss TTL, as a route writes it; counted unless counted when it started downloading) and the download routed again,
// nothing blocklisted.
func (r *Router) owedDeleted(ctx context.Context, x pollRow, now time.Time, sum *PollSummary) error {
	rctx := context.WithoutCancel(ctx)
	if err := r.setCache(rctx, x.hash, x.pk, "uncached", "add", now.Add(r.downloadMissTTL(rctx, x, now)), now); err != nil {
		return err
	}
	if x.state == StateAdded {
		r.uncachedAdd(rctx, x.pk) // one already downloading was counted when it started (applyStatus)
	}
	r.log.Info("cached-only: uncached add deleted; the release is routed again, nothing blocklisted",
		"download", x.id, "provider", x.pk, "torrent", x.tid)
	queued, err := r.requeue(rctx, x, 0, "uncached on "+x.pk+"; our add was deleted and the release is routed again",
		"uncached on "+x.pk+"; our add was deleted; not blocklisted (no route queue)")
	if err == nil && !queued {
		sum.Failed++
	}
	return err
}

// downloadMissTTL is the miss TTL of download x's content (missTTL), miss_ttl when its row cannot be read.
func (r *Router) downloadMissTTL(ctx context.Context, x pollRow, now time.Time) time.Duration {
	vp := r.pol.Versions[x.version]
	if vp == nil {
		return r.set.MissTTL
	}
	var title int64
	var eps string
	if err := r.st.Reader().QueryRowContext(ctx, `SELECT COALESCE(title_id, 0), COALESCE(episodes, '[]')
		FROM download WHERE id = ?`, x.id).Scan(&title, &eps); err != nil {
		r.log.Warn("download not read; the miss keeps miss_ttl", "download", x.id, "err", err)
		return r.set.MissTTL
	}
	var ids []int64
	_ = json.Unmarshal([]byte(eps), &ids)
	return r.missTTL(ctx, vp.Kind, title, ids, now)
}

// unfinishedChecksSQL counts the downloads with an unfinished cached-only check (router status).
const unfinishedChecksSQL = `SELECT COUNT(*) FROM download WHERE cached_check IS NOT NULL
	AND state IN ('added', 'downloading')`
