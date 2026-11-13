package router

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/store"
)

// Cached-only on Real-Debrid and AllDebrid (docs/design/router.md "Cached-only on Real-Debrid and AllDebrid").
//
// Neither provider has a read-only cache check, so with router.cached_only the router does what decypharr does with
// download_uncached false: it adds, reads the status at once, calls the add cached when the provider holds it
// downloaded (after at most one re-read, router.cached_grace later; a Real-Debrid magnet still converting gets up to
// three), and otherwise uncached. An uncached add is
// deleted again and the next provider tried, but only when the router can prove the entry is its own new one:
//
//   - before the add, the hash was not on the account: no registry row (content_provider, from the provider sync) in
//     state present; on AllDebrid also no download of ours there with a torrent id (any state: a failed download's
//     torrent was never deleted) and not in a fresh read of the account's magnet list (on Real-Debrid an old
//     download of ours with the hash does not count: the add makes a new torrent regardless);
//   - the returned id is a new entry: on Real-Debrid always (every addMagnet creates a torrent); on AllDebrid the id
//     was not in that list (an upload of a hash already there returns the existing id); and on both, no registry row
//     or download of ours knows the id.
//
// When any of this cannot be shown (the list read failed, the id was there already), nothing is deleted: the add
// stays and the download waits for it, as before (uncached_kept_shared, logged with the reason). An add of ours whose
// delete was blocked (the gate, the job lease) or failed, and one the reads gave no verdict on, are left to the
// poller (cachedcheck.go).
//
// On AllDebrid the proof has a hole: another client uploading the same hash between our list read and our upload
// gets the same magnet, and we would take it for ours. So AllDebrid deletes only with
// router.alldebrid_delete_uncached (default on; a stored false is kept); otherwise an uncached add of ours is kept there (uncached_kept)
// and the next provider is tried, as after a delete. The option is for an AllDebrid account nothing else
// writes to.
// The delete itself is adder.Deleter, the only delete in Theatarr, which also refuses anything but an *Added its own
// AddMagnet returned moments ago.

// Step codes of the cached-only settle.
const (
	codeAddedCached        = "added"                // the add is cached: accepted
	codeUncachedDeleted    = "uncached_deleted"     // uncached, ours: deleted, next provider
	codeUncachedKeptShared = "uncached_kept_shared" // uncached, not provably ours: kept, the download waits
	codeUncachedKept       = "uncached_kept"        // uncached, ours, on AllDebrid with deletes off: kept, next provider
	// codeProviderDone: the provider is not tried again in this route (an uncached AllDebrid add was kept).
	codeProviderDone = "provider_done"
	// codeAltSkipped: the second selection of altRetry was not tried (the provider is paced past altPaceWait).
	codeAltSkipped = "alt_selection_skipped"
)

// keptOnAllDebrid ends AllDebrid for the rest of the route after an uncached add of ours stayed there (kept with the
// switch off, or kept because it could not be proven ours): each further release would leave one more untracked
// magnet on the shared account. The next route tries AllDebrid again; uncached_adds_per_hour
// caps it across routes.
func (r *Router) keptOnAllDebrid(s *routeState, pv *policy.Provider, pk, code string) {
	if pv.Kind != provider.KindAllDebrid {
		return
	}
	if s.skipProvider == nil {
		s.skipProvider = map[string]string{}
	}
	s.skipProvider[pk] = "an uncached add of ours stayed on " + pk + " earlier in this route (" + code +
		"); not tried again until the next route"
}

// maxSettleReads bounds the status reads after one add.
const maxSettleReads = 4

// settles reports whether cached_only adds on this kind are read back and, when uncached and ours, deleted.
func settles(kind string) bool {
	return kind == provider.KindRealDebrid || kind == provider.KindAllDebrid
}

// ownGuard is what the router knew about the account before an add.
type ownGuard struct {
	// before is why the hash was already on the account ("" when it was not, as far as the router can tell).
	before string
	// listed: AllDebrid's magnet list was read before the add (ids holds its ids). Always true on Real-Debrid,
	// which needs no list.
	listed  bool
	listErr string
	ids     map[string]bool
	// reuse is the id of the hash on the account per the fresh list (AllDebrid): reused, nothing added.
	reuse string
	// at is when the guard looked: a torrent manager restore of the hash since then (less addSettle) makes the
	// entry not provably ours (RestoredSince).
	at time.Time
}

// guardBefore notes whether the hash is on the provider's account before an add. dl is this route's download. fresh
// reads AllDebrid's magnet list too (never in a dry run: it sends nothing to a provider).
func (r *Router) guardBefore(ctx context.Context, pv *policy.Provider, pk, hash string, dl int64,
	fresh bool) (*ownGuard, error) {
	g := &ownGuard{listed: true, at: r.now()}
	var why string
	err := r.st.Writer().QueryRowContext(ctx, `SELECT 'registry: torrent ' || COALESCE(NULLIF(cp.torrent_id, ''), '?')
		FROM content_provider cp JOIN content c ON c.id = cp.content_id
		WHERE c.info_hash = ? AND cp.provider = ? AND cp.state = 'present' LIMIT 1`, hash, pk).Scan(&why)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("router: registry: %w", err)
	}
	// Our own downloads with a torrent id on the provider, in any state (a failed download's torrent was never
	// deleted). Not on Real-Debrid: every addMagnet there creates a new torrent, so an old download of ours with the
	// hash does not make the new id shared. The registry still counts there,
	// and proof still refuses an id any other row knows.
	if why == "" && pv.Kind != provider.KindRealDebrid {
		err = r.st.Writer().QueryRowContext(ctx, `SELECT 'download ' || id || ' (' || state || '): torrent ' ||
			provider_torrent_id FROM download WHERE info_hash = ? AND provider = ? AND provider_torrent_id IS NOT NULL
			AND id != ? ORDER BY id DESC LIMIT 1`, hash, pk, dl).Scan(&why)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("router: downloads: %w", err)
		}
	}
	if why == "" {
		// A copy to another provider (copy.go) whose add is out or not settled: its entry is not this add's.
		err = r.st.Writer().QueryRowContext(ctx, `SELECT 'copy to other providers (' || state || '): torrent ' ||
			COALESCE(torrent_id, '?') FROM provider_copy WHERE info_hash = ? AND provider = ?
			AND state IN ('adding', 'checking')`, hash, pk).Scan(&why)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("router: copies: %w", err)
		}
	}
	g.before = why
	if pv.Kind == provider.KindRealDebrid || !fresh {
		return g, nil
	}
	// AllDebrid (and TorBox, for a copy to other providers): the registry may be stale (another client added the hash
	// since the last sync), and an upload of a hash already there returns the existing id. One read lists every magnet
	// (no paging on AllDebrid).
	g.listed = false
	rp := r.provs[pk]
	if rp == nil {
		g.listErr = "no read-only client for " + pk
		return g, nil
	}
	list, err := rp.Torrents(ctx)
	if err != nil {
		g.listErr = "reading the account's magnets failed: " + err.Error()
		return g, nil
	}
	g.listed, g.ids = true, make(map[string]bool, len(list))
	for _, t := range list {
		g.ids[t.ID] = true
		if strings.EqualFold(t.Hash, hash) && t.ID != "" && g.reuse == "" {
			g.reuse = t.ID
		}
	}
	return g, nil
}

// proof says whether the add is provably a new entry of ours, and why not.
func (r *Router) proof(ctx context.Context, g *ownGuard, pv *policy.Provider, pk, hash string, dl int64,
	a *adder.Added, t *provider.Torrent) (adder.OwnAdd, string, error) {
	switch {
	case g.before != "":
		return adder.OwnAdd{}, "the hash was on the account before the add (" + g.before + ")", nil
	case !g.listed:
		return adder.OwnAdd{}, "cannot tell whether the hash was on the account: " + g.listErr, nil
	case pv.Kind == provider.KindAllDebrid && g.ids[a.ID]:
		return adder.OwnAdd{NotOnAccountBefore: true}, "AllDebrid returned magnet " + a.ID +
			", which was on the account before the add", nil
	case a.Hash != "" && !strings.EqualFold(a.Hash, hash), t != nil && t.Hash != "" && !strings.EqualFold(t.Hash, hash):
		return adder.OwnAdd{NotOnAccountBefore: true}, "the entry's hash is not the one added", nil
	}
	if pv.Kind != provider.KindRealDebrid {
		// Real-Debrid makes a new torrent per add; elsewhere a restore sent meanwhile may own this entry.
		if why, err := RestoredSince(ctx, r.st.Writer(), pk, hash, g.at.Add(-addSettle)); err != nil || why != "" {
			return adder.OwnAdd{NotOnAccountBefore: true}, why, err
		}
	}
	var known string
	err := r.st.Writer().QueryRowContext(ctx, `SELECT 'registry' FROM content_provider WHERE provider = ?
		AND torrent_id = ? UNION ALL SELECT 'download ' || id FROM download WHERE provider = ?
		AND provider_torrent_id = ? AND id != ? UNION ALL SELECT 'copy to other providers ' || id FROM provider_copy
		WHERE provider = ? AND torrent_id = ? AND state IN ('adding', 'checking') LIMIT 1`, pk, a.ID, pk, a.ID, dl,
		pk, a.ID).Scan(&known)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return adder.OwnAdd{}, "", fmt.Errorf("router: torrent ids: %w", err)
	}
	if known != "" {
		return adder.OwnAdd{NotOnAccountBefore: true}, "torrent " + a.ID + " is known already (" + known + ")", nil
	}
	return adder.OwnAdd{NotOnAccountBefore: true, NewEntry: true}, "", nil
}

// cacheVerdict is what the reads after an add showed.
type cacheVerdict int

const (
	cvUnknown  cacheVerdict = iota // could not tell, or the provider reports an error: as before, no delete
	cvCached                       // downloaded at once (or within the grace)
	cvUncached                     // still converting, queued or downloading after the grace
)

// readAfterAdd reads a fresh add back: decypharr's CheckStatus (RD: select files when RD waits for that, then the
// status; AD: magnet/status by id), with at most one re-read after router.cached_grace while the provider is
// downloading or queued, and a grace after selecting files. A Real-Debrid magnet still converting (queued, no file
// selection sent) is no verdict yet, as in the poller: it is re-read after each grace up to maxSettleReads reads and
// called uncached only when it is still converting at the last one; a selection sent at the last read is followed by
// one more read after its grace. handled is true when applyStatus already
// recorded the last read (a selection, or a rejected torrent); t is then nil.
func (r *Router) readAfterAdd(ctx context.Context, s *routeState, pv *policy.Provider, pk, hash string,
	a *adder.Added) (cacheVerdict, *provider.Torrent, string) {
	return r.readBack(ctx, pv, pk, a, func(t *provider.Torrent) (string, error) {
		sel, err := r.applyStatus(ctx, s.dlID, s.req.Version, s.vp.Kind, pk, t, false)
		if err != nil {
			return "", err
		}
		if state, _ := r.downloadState(ctx, s.dlID); state != StateAdded || !sel {
			return "files not selected (download " + state + ")", nil
		}
		return "", nil
	})
}

// readBack is readAfterAdd's reads, shared with the copies to other providers (copy.go): selectFiles sends a
// Real-Debrid torrent's file selection when it waits for one, and says why not when it did not ("" when it did).
func (r *Router) readBack(ctx context.Context, pv *policy.Provider, pk string, a *adder.Added,
	selectFiles func(t *provider.Torrent) (string, error)) (cacheVerdict, *provider.Torrent, string) {
	if pv.Kind == provider.KindAllDebrid && a.Ready {
		return cvCached, nil, "AllDebrid answered ready"
	}
	rp := r.provs[pk]
	if rp == nil {
		return cvUnknown, nil, "no read-only client for " + pk
	}
	graced, selected := false, false
	var t *provider.Torrent
	// limit: a selection sent at the last read gets one more read after its grace.
	limit := maxSettleReads
	for i := 0; i < limit; i++ {
		last := i == limit-1
		var err error
		t, err = rp.Torrent(ctx, a.ID)
		if err != nil {
			return cvUnknown, nil, "status read failed: " + err.Error()
		}
		switch {
		case t.State == provider.StateDownloaded:
			return cvCached, t, "status " + t.Status
		case pv.Kind == provider.KindRealDebrid && t.Status == rdWaitingFiles && !selected:
			why, err := selectFiles(t)
			if err != nil {
				return cvUnknown, nil, "selecting files: " + err.Error()
			}
			if why != "" {
				return cvUnknown, nil, why
			}
			if last {
				limit++ // selected on the last read (after a slow conversion): read the outcome once more
			}
			selected, graced = true, true
			if err := r.sleep(ctx, r.set.CachedGrace); err != nil {
				return cvUnknown, nil, err.Error()
			}
		case t.State == provider.StateDownloading || t.State == provider.StateQueued:
			// Real-Debrid still converting (no selection sent): no verdict until the last read.
			if pv.Kind == provider.KindRealDebrid && t.State == provider.StateQueued && !selected && graced {
				if last {
					return cvUncached, t, "status " + t.Status + " (still converting) after " +
						(time.Duration(i) * r.set.CachedGrace).String()
				}
				if err := r.sleep(ctx, r.set.CachedGrace); err != nil {
					return cvUnknown, nil, err.Error()
				}
				continue
			}
			if graced {
				return cvUncached, t, "status " + t.Status + " after " + r.set.CachedGrace.String()
			}
			graced = true
			if err := r.sleep(ctx, r.set.CachedGrace); err != nil {
				return cvUnknown, nil, err.Error()
			}
		default:
			return cvUnknown, t, "status " + t.Status
		}
	}
	return cvUnknown, t, "no verdict after " + fmt.Sprint(maxSettleReads) + " reads"
}

// settle decides a cached-only add on Real-Debrid or AllDebrid after it was accepted and recorded (markAdded).
func (r *Router) settle(ctx context.Context, s *routeState, pv *policy.Provider, pk, hash string, ad adder.Adder,
	a *adder.Added, g *ownGuard) (verdict, time.Time, Step, error) {
	cv, t, why := r.readAfterAdd(ctx, s, pv, pk, hash, a)
	apply := func() error {
		if t == nil {
			return nil
		}
		_, err := r.applyStatus(ctx, s.dlID, s.req.Version, s.vp.Kind, pk, t, false)
		return err
	}
	switch cv {
	case cvCached:
		now := r.now()
		// The cheap learning step: a provider's own instant answer is evidence for later searches and routes.
		if err := r.setCacheSource(ctx, hash, pk, "cached", "add:"+pk, now.Add(r.pol.CacheEvidenceSettings().TTL),
			now); err != nil {
			return 0, time.Time{}, Step{}, err
		}
		if err := apply(); err != nil {
			r.log.Warn("after add", "download", s.dlID, "err", err)
		}
		return vAccepted, time.Time{}, Step{Outcome: "accepted", Code: codeAddedCached,
			Message: "cached: " + why}, nil
	case cvUnknown:
		if err := apply(); err != nil {
			r.log.Warn("after add", "download", s.dlID, "err", err)
		}
		// The check is not over: the poller finishes it (cachedcheck.go).
		if err := r.setCachedCheck(context.WithoutCancel(ctx), s.dlID, pk, a.ID, checkPending); err != nil {
			return 0, time.Time{}, Step{}, err
		}
		r.log.Info("cached-only: no verdict on the add; it stays and the poller checks it again", "provider", pk,
			"hash", hash, "torrent", a.ID, "why", why)
		return vAccepted, time.Time{}, Step{Outcome: "accepted", Code: codeAddedCached,
			Message: "cache state unknown (" + why + "); the add stays and the poller checks it again"}, nil
	}
	// Uncached.
	proof, notOurs, err := r.proof(ctx, g, pv, pk, hash, s.dlID, a, t)
	if err != nil {
		return 0, time.Time{}, Step{}, err
	}
	// AllDebrid: deleting needs router.alldebrid_delete_uncached (default on). The proof cannot rule out another
	// client uploading the same hash between our list read and our upload, which hands us its magnet's id.
	keep := notOurs == "" && pv.Kind == provider.KindAllDebrid && !r.set.AllDebridDeleteUncached
	del, canDelete := ad.(adder.Deleter)
	if notOurs == "" && !keep && !canDelete {
		notOurs = "the adder for " + pk + " cannot delete"
	}
	// owed: ours, but the delete was blocked (the stop switch, the job lease) or failed: the poller sends it later
	// (download.cached_check uncached, cachedcheck.go).
	owed, journal := false, false
	if notOurs == "" && !keep {
		if err := r.mayWrite(s.req.Version); err != nil {
			notOurs, owed = err.Error(), true
		} else if err := jobs.Fence(ctx); err != nil {
			notOurs, owed = "the route lost its job lease: "+err.Error(), true
		} else if err := r.noteDeleting(ctx, s.dlID, pk, a.ID); err != nil {
			return 0, time.Time{}, Step{}, err
		} else if err := del.DeleteOwnUncachedAdd(ctx, a, proof); err != nil {
			// The provider may have deleted it before the answer was lost. Only a definite not-found, read twice
			// router.cached_grace apart, proves the delete completed: a single not-found is not trusted (the
			// poller needs repeated misses too, missingPolls), and a present torrent or another failed read keeps
			// the add.
			gone, present := r.readAfterDelete(ctx, pk, a.ID)
			if !gone {
				notOurs, owed = "the delete failed: "+err.Error(), true
				// The reads could not tell: the delete may have gone through, so its journal stays for the poller
				// (a later not-found is our delete: owedDeleted).
				journal = !present
			}
		}
	}
	if notOurs != "" {
		// Not provably ours: keep it, as before cached-only deletes existed. The download waits for it, and
		// applyStatus counts the uncached add. A delete journaled and shown not done is no longer pending (one the
		// reads could not tell keeps its journal). An owed delete is left to the poller.
		var mark any
		if owed {
			mark = checkUncached
		}
		if _, err := r.st.Writer().ExecContext(context.WithoutCancel(ctx), `UPDATE download
			SET error = CASE WHEN error LIKE ?1 AND NOT ?6 THEN NULL ELSE error END,
			cached_check = CASE WHEN ?2 IS NOT NULL AND provider = ?3 AND provider_torrent_id = ?4
				AND state IN ('added', 'downloading') THEN ?2 ELSE cached_check END WHERE id = ?5`,
			deletingPrefix+"%", mark, pk, a.ID, s.dlID, journal); err != nil {
			return 0, time.Time{}, Step{}, fmt.Errorf("router: download %d: %w", s.dlID, err)
		}
		r.log.Warn("cached-only: uncached add kept; nothing deleted", "provider", pk, "hash", hash, "torrent", a.ID,
			"download", s.dlID, "why", notOurs, "delete_left_to_poller", owed)
		r.keptOnAllDebrid(s, pv, pk, codeUncachedKeptShared)
		if err := apply(); err != nil {
			r.log.Warn("after add", "download", s.dlID, "err", err)
		}
		return vAccepted, time.Time{}, Step{Outcome: "accepted", Code: codeUncachedKeptShared,
			Message: "uncached (" + why + "), kept: " + notOurs}, nil
	}
	// The delete (or the kept add) happened: what it means is recorded even if ctx ends now, as an
	// add is, so the download is not left pointing at a deleted torrent.
	rctx := context.WithoutCancel(ctx)
	now := r.now()
	code, done := codeUncachedDeleted, "deleted"
	if keep {
		// Kept on AllDebrid: it finishes downloading there (no uncached-add limit is known for AllDebrid), and this
		// download moves on to the next provider as after a delete.
		code, done = codeUncachedKept, "kept (router.alldebrid_delete_uncached is off)"
		r.keptOnAllDebrid(s, pv, pk, codeUncachedKept)
		r.log.Info("cached-only: uncached add kept on AllDebrid; trying the next provider", "provider", pk,
			"hash", hash, "torrent", a.ID, "download", s.dlID, "why", why)
	} else {
		r.log.Info("cached-only: uncached add deleted; trying the next provider", "provider", pk, "hash", hash,
			"torrent", a.ID, "download", s.dlID, "why", why)
	}
	if err := r.setCache(rctx, hash, pk, "uncached", "add", now.Add(r.verdictTTL(rctx, s, now)), now); err != nil {
		return 0, time.Time{}, Step{}, err
	}
	r.uncachedAdd(rctx, pk)
	if err := r.unmarkAdded(rctx, s.dlID, pk, a.ID, "uncached on "+pk+"; our add was "+done); err != nil {
		return 0, time.Time{}, Step{}, err
	}
	s.res.TorrentID = ""
	msg := "uncached (" + why + "); our add " + a.ID + " was " + done
	if !keep && pv.Kind == provider.KindRealDebrid && !s.alt[altKey(pk, hash)] && t != nil &&
		len(AltFiles(s.vp.Kind, t.Files)) > 0 {
		// Real-Debrid caches per selected set: the movie's largest video alone may be cached where every
		// playable file was not (altRetry). The uncached verdict above stands for the set tried.
		s.retry = pk
		msg += "; adding it once more with the largest video alone selected"
	}
	return vContent, time.Time{}, Step{Outcome: "refused", Code: code, Message: msg}, nil
}

// altKey names a release on a provider in routeState.alt.
func altKey(pk, hash string) string { return pk + "\x00" + strings.ToLower(hash) }

// altPick is the selection of altRetry's second add: AltFiles, or ChooseFiles when the torrent has no other set
// after all.
func altPick(k policy.Kind, files []provider.File) []string {
	if ids := AltFiles(k, files); ids != nil {
		return ids
	}
	return ChooseFiles(k, files)
}

// altPaceWait bounds the wait for the provider's pacing before altRetry's second add (Real-Debrid's min_gap is
// 20s by default); a longer one (a burst cooldown, uncached_adds_per_hour used up) skips the second add.
var altPaceWait = time.Minute

// altRetry prepares the second add of the current release on pk that settle asked for (routeState.retry): on
// Real-Debrid with router.cached_only, after every playable file came back uncached and our add was deleted, a
// movie is added once more with only its largest video selected, the set tools streaming one file select. It
// waits for the provider's pacing (at most altPaceWait, without the add lock: the caller holds r.mu, as Route does)
// and reports whether to send it; the add itself is the
// route's normal one (provider): it takes a pacing slot, and an uncached answer is counted, deleted and cached as
// uncached as the first was. It is tried once per release and provider.
func (r *Router) altRetry(ctx context.Context, s *routeState, pk, hash string) (bool, string, error) {
	s.retry = ""
	if s.alt == nil {
		s.alt = map[string]bool{}
	}
	key := altKey(pk, hash)
	if s.alt[key] {
		return false, "", nil
	}
	s.alt[key] = true
	pv := r.pol.Providers[pk]
	if pv == nil {
		return false, "", nil
	}
	p, err := loadPace(ctx, r.st.Writer(), pk)
	if err != nil {
		return false, "", err
	}
	now := r.now()
	until, why := p.blocked(now, pv.EffectivePacing())
	if until.IsZero() {
		return true, "", nil
	}
	if d := until.Sub(now); d > altPaceWait {
		return false, "the second selection was not tried: " + pk + " is paced until " +
			until.UTC().Format(time.RFC3339) + " (" + why + ")", nil
	}
	// The wait (up to altPaceWait) does not hold the add lock: polls, other routes, restores and
	// deletes go on meanwhile. What they change is checked again by the add itself: it is fenced on the download
	// still queued (markAdding), reserves its pacing slot in one transaction, and reads the account first.
	r.mu.Unlock()
	err = r.sleep(ctx, until.Sub(now))
	r.mu.Lock()
	if err != nil {
		return false, "", err
	}
	return true, "", nil
}

// deletingPrefix starts download.error while our uncached add's delete is in flight (noteDeleting): a crash between
// the delete and unmarkAdded leaves it, and the poller that then finds the torrent gone fails the download without
// blocklisting the release (failProviderDownload): its own delete, not the provider, took it.
const deletingPrefix = "deleting our uncached add "

// noteDeleting journals on the download, before the delete is sent, that our uncached add tid on pk is being deleted.
func (r *Router) noteDeleting(ctx context.Context, dl int64, pk, tid string) error {
	_, err := r.st.Writer().ExecContext(ctx, `UPDATE download SET error = ?, updated_at = ? WHERE id = ?
		AND provider = ? AND provider_torrent_id = ? AND state IN ('added', 'downloading')`,
		deletingPrefix+tid+" on "+pk, store.Millis(r.now()), dl, pk, tid)
	if err != nil {
		return fmt.Errorf("router: download %d: %w", dl, err)
	}
	return nil
}

// readAfterDelete reads torrent id back after a failed delete: gone only when two reads router.cached_grace apart
// both answer not found; present when a read shows the torrent. Neither: the reads could not tell (the delete may
// have gone through).
func (r *Router) readAfterDelete(ctx context.Context, pk, id string) (gone, present bool) {
	rp := r.provs[pk]
	if rp == nil {
		return false, false
	}
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), providerWriteLimit)
	defer cancel()
	for i := range 2 {
		if i > 0 && r.sleep(checkCtx, r.set.CachedGrace) != nil {
			return false, false
		}
		_, err := rp.Torrent(checkCtx, id)
		if err == nil {
			return false, true
		}
		if !errors.Is(err, provider.ErrNotFound) {
			return false, false
		}
	}
	return true, false
}

// unmarkAdded returns a download whose add was deleted (or kept on AllDebrid) to queued, with no provider, for the
// next provider: only while it still holds that add. One removed or blocklisted during the settle stays ended
// (errMovedOn), and the route stops.
func (r *Router) unmarkAdded(ctx context.Context, dl int64, pk, tid, why string) error {
	err := r.move(ctx, dl, transition{from: []string{StateAdded, StateDownloading}, provider: pk, torrent: tid,
		set: `state = 'queued', provider = NULL, provider_torrent_id = NULL, provider_status = '', name = '',
		progress = 0, cached = NULL, files_selected = 0, selected_at = NULL, reused = 0, own_add = NULL,
		added_at = NULL, cached_check = NULL, error = ?, updated_at = ?`, args: []any{why, store.Millis(r.now())}})
	if errors.Is(err, errAddTaken) {
		return fmt.Errorf("download %d left %s torrent %s: %w", dl, pk, tid, errMovedOn)
	}
	return err
}

// sleepCtx waits d or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// cachedOnlyNote is the dry run's word on what cached-only does after an add on this provider.
func (r *Router) cachedOnlyNote(ctx context.Context, pv *policy.Provider, pk, hash string) (string, error) {
	if !r.set.CachedOnly || !settles(pv.Kind) {
		return "", nil
	}
	g, err := r.guardBefore(ctx, pv, pk, hash, 0, false)
	if err != nil {
		return "", err
	}
	if g.before != "" {
		return "; cached_only: if it is not ready at once it stays (" + codeUncachedKeptShared + ": the hash was " +
			"on the account: " + g.before + ")", nil
	}
	note := "; cached_only: if it is not ready at once it is deleted (" + codeUncachedDeleted + ") and the next " +
		"provider tried"
	if pv.Kind == provider.KindAllDebrid {
		if !r.set.AllDebridDeleteUncached {
			return "; cached_only: if it is not ready at once it stays on AllDebrid (" + codeUncachedKept +
				": router.alldebrid_delete_uncached is off) and the next provider is tried", nil
		}
		note += ", unless the account's magnet list, read before the add, shows the hash or the returned id"
	}
	return note, nil
}

// recordOwnAdd stores whether the add is provably a new entry of ours (download.own_add), for the stall timeout.
func (r *Router) recordOwnAdd(ctx context.Context, dl int64, g *ownGuard, pv *policy.Provider, pk, hash string,
	a *adder.Added) error {
	proof, _, err := r.proof(ctx, g, pv, pk, hash, dl, a, nil)
	if err != nil {
		return err
	}
	_, err = r.st.Writer().ExecContext(ctx, `UPDATE download SET own_add = ? WHERE id = ?`,
		b2i(proof.NotOnAccountBefore && proof.NewEntry), dl)
	if err != nil {
		return fmt.Errorf("router: download %d: %w", dl, err)
	}
	return nil
}

// triedAny reports whether a candidate's steps include anything but skips without a request: an add, a refusal, a
// deferral, an error, a would-add, or a provider's cache check.
func triedAny(steps []Step) bool {
	for _, st := range steps {
		switch st.Outcome {
		case "skipped", "miss", outcomeOrdered:
			if st.Outcome == "skipped" && st.Code == "known_uncached" && strings.Contains(st.Message, "cache check") {
				return true
			}
			continue
		}
		return true
	}
	return false
}
