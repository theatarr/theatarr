package router

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/store"
)

// noVideo reports a torrent known to hold no video file that no selection of ours will fix: a reused torrent
// (shared, never selected by us) or a finished one. Our own Real-Debrid add waiting for a selection is applyStatus's.
// It looks at paths alone (HasVideo): AllDebrid and TorBox files carry no id, and only Real-Debrid's selection
// leaves .vob and .iso out.
func noVideo(reused int, t *provider.Torrent) bool {
	return (reused == 1 || t.State == provider.StateDownloaded) && len(t.Files) > 0 && !HasVideo(t.Files)
}

// missingGrace is how long consecutive not-found reads of a torrent must span (over at least missingPolls polls)
// before the poller takes it as gone and blocklists the release: one read can miss a torrent the provider still has
// (a listing lag, a short outage answering 404).
const (
	missingGrace = 5 * time.Minute
	missingPolls = 2
)

// rdWaitingFiles is Real-Debrid's status while it waits for a selection.
const rdWaitingFiles = "waiting_files_selection"

// trySelect reads a Real-Debrid torrent once and selects its files when RD waits for that. Failures are logged and
// left for the poller.
func (r *Router) trySelect(ctx context.Context, dl int64, version string, kind policy.Kind, pk, id string) {
	rp := r.provs[pk]
	if rp == nil {
		return
	}
	t, err := rp.Torrent(ctx, id)
	if err != nil {
		r.log.Info("status read after add failed; the poller retries", "provider", pk, "torrent", id, "err", err)
		return
	}
	if _, err := r.applyStatus(ctx, dl, version, kind, pk, t, false); err != nil {
		r.log.Warn("after add", "download", dl, "err", err)
	}
}

// PollSummary is what one poll did.
type PollSummary struct {
	Read     int `json:"read"`
	Selected int `json:"selected"`
	Ready    int `json:"ready"`
	Failed   int `json:"failed"`
	Errors   int `json:"errors"`
	Unpolled int `json:"unpolled"`
	// Stalled counts the downloads failed by router.stalled_after (also in Failed); Researched the re-searches
	// queued after blocklisted failures.
	Stalled    int `json:"stalled"`
	Researched int `json:"researched"`
	// Reconciled counts downloads left in adding by an interrupted add, settled from the provider's account.
	Reconciled int `json:"reconciled"`
	// Resumed counts queued downloads with no route job that the poller queued a route for (resumeStrays).
	Resumed int `json:"resumed"`
	// Postponed counts the downloads not read because the pass reached pollPassLimit; the next pass reads them first.
	Postponed int `json:"postponed"`
}

// providerWriteLimit bounds one provider write made while recording a status (Real-Debrid's file selection, the
// delete of our own stalled or failed add): they run under the router lock, which a slow provider must not hold.
// A write cut by it is an error like any other: the selection is tried again at the next read, and a
// delete that did not finish keeps the torrent (the download still fails, with the reason).
var providerWriteLimit = 30 * time.Second

// pollPassLimit bounds the provider reads of one poll pass: a slow provider delays the downloads
// left, not the whole pass. Reads go least recently tried first (download.poll_tried_at, answered or not), so
// failing reads do not take every pass's start and none is starved.
var pollPassLimit = 2 * time.Minute

// A download whose status reads keep failing is read again after a back-off: the poll interval doubled per failed
// read in a row, at most pollErrorBackoffMax. After blindReads failed reads in a row it is judged as stalled from
// what is known (blindStalled), so read errors never keep it out of the stall timeouts.
const (
	pollErrorBackoffMax = 15 * time.Minute
	blindReads          = 3
)

// pollBackoff is when a download with errs failed reads in a row, the last tried at tried, is read again.
func (r *Router) pollBackoff(tried time.Time, errs int) time.Time {
	if errs <= 0 || tried.IsZero() {
		return time.Time{}
	}
	base := r.set.PollInterval
	if base <= 0 {
		base = time.Minute
	}
	d := min(base<<min(errs-1, 10), pollErrorBackoffMax)
	return tried.Add(d)
}

// Poll reads every added or downloading download back from its provider: file selection for a non-reused
// Real-Debrid torrent waiting for it (a write: only through the gate), progress, ready, or failed on a provider
// error status; a download in added past router.stalled_after fails as stalled (stalled.go: blocklisted, and our
// own Real-Debrid add deleted, the only delete outside a route). A download whose torrent left the provider fails
// with that reason. Then it fails ready downloads past router.ready_timeout without a live import claim
// (failure.go), and queues re-searches for new blocklisted failures (research.go). The status reads run without the
// router lock and within pollPassLimit; each result is recorded under it, on a download still where the pass found it.
func (r *Router) Poll(ctx context.Context) (PollSummary, error) {
	// One pass at a time. The sweeps and the writes run under r.mu; the provider status reads do not, so routes and
	// runtime preparation are not held up behind them, and the pass ends at pollPassLimit.
	r.shared.poll.Lock()
	defer r.shared.poll.Unlock()
	var sum PollSummary
	now := r.now()
	// Interrupted adds first: settled from the account where it can tell (reconcile.go); the rest stay in adding,
	// unsettled, however long that takes. The account reads run without r.mu and are bounded.
	checks, err := r.checkStuck(ctx, now)
	if err != nil {
		return sum, err
	}
	r.mu.Lock()
	sum.Reconciled, sum.Failed, err = r.settleStuck(ctx, checks, now)
	var todo []pollRow
	var resting map[string]int
	if err == nil {
		todo, resting, err = r.pollSweeps(ctx, now, &sum)
	}
	r.mu.Unlock()
	if err != nil {
		return sum, err
	}
	passCtx, cancel := context.WithTimeout(ctx, pollPassLimit)
	defer cancel()
	// A failed read is recorded after the pass's other reads: how the provider answered the others tells a torrent's
	// failure from the provider's outage (providerReads).
	reads := map[string]*providerReads{}
	type failedRead struct {
		x   pollRow
		err error
	}
	var failed []failedRead
	// read reads one download's status and records it, or keeps it for later: a failed read in failed, a read the
	// gateway refused for the provider's failure back-off (not sent) in its providerReads.held.
	read := func(x pollRow) error {
		pr := reads[x.pk]
		if pr == nil {
			pr = &providerReads{resting: resting[x.pk]}
			reads[x.pk] = pr
		}
		t, err := r.provs[x.pk].Torrent(passCtx, x.tid)
		sum.Read++
		if err != nil && passCtx.Err() != nil && ctx.Err() == nil {
			// Cut by the pass deadline, not answered by the provider: tried (it goes behind the others next
			// pass), neither a failed read nor an answer: the failed-read streak stands.
			return r.notePollAttempt(ctx, x.id, now, pollCut)
		}
		if failureBackoff(err) {
			pr.held = append(pr.held, x)
			return nil
		}
		switch {
		case err == nil || torrentAnswer(err):
			pr.answered++
		case !errors.Is(err, provider.ErrNotFound):
			pr.failed++
			var ae *provider.APIError
			pr.failed5xx = pr.failed5xx || (errors.As(err, &ae) && ae.Status >= 500) ||
				errors.Is(err, provider.ErrUnavailable)
		}
		if err != nil && !errors.Is(err, provider.ErrNotFound) {
			failed = append(failed, failedRead{x, err})
			return nil
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.pollOne(ctx, x, t, err, false, now, &sum)
	}
	for i, x := range todo {
		if ctx.Err() != nil {
			return sum, ctx.Err()
		}
		if passCtx.Err() != nil {
			sum.Postponed = len(todo) - i
			r.log.Info("poll: pass deadline reached; the rest is read next pass", "postponed", sum.Postponed,
				"limit", pollPassLimit)
			break
		}
		if r.provs[x.pk] == nil {
			continue
		}
		if err := read(x); err != nil {
			return sum, err
		}
	}
	// Per provider: is it answering? Most of its reads failing at once is its outage, whatever its account says. A
	// few failing reads (5xx) that no other read answered are checked against the account, once per round: the
	// check goes through the failure back-off those reads may have set (gateway.HealthCheck), and when it answers,
	// the reads that back-off refused are read again, so the decision rests on what the provider said about them.
	for pk, pr := range reads {
		for round := 0; round < pollCheckRounds && !pr.outage() && passCtx.Err() == nil; round++ {
			if len(pr.held) == 0 && (pr.answered > 0 || pr.up || !pr.failed5xx) {
				break
			}
			if _, err := r.provs[pk].Account(gateway.HealthCheck(passCtx)); err != nil {
				r.log.Info("poll: the account check failed too", "provider", pk, "err", err)
				break
			}
			pr.up = true
			again := pr.held
			pr.held = nil
			for i, x := range again {
				if passCtx.Err() != nil {
					pr.held = append(pr.held, again[i:]...)
					break
				}
				if err := read(x); err != nil {
					return sum, err
				}
			}
		}
	}
	for _, f := range failed {
		// Not an answer about the torrent (a gateway refusal, a transport error, a 5xx) while the provider is down:
		// the provider's outage, not the torrent's.
		outage := !torrentAnswer(f.err) && reads[f.x.pk].down()
		r.mu.Lock()
		err := r.pollOne(ctx, f.x, nil, f.err, outage, now, &sum)
		r.mu.Unlock()
		if err != nil {
			return sum, err
		}
	}
	for _, pr := range reads {
		for _, x := range pr.held {
			// Refused for the provider's failure back-off, never sent: a failed read when the provider is down,
			// otherwise only tried (the account answered, the pass ended before it was read again).
			var err error
			if pr.down() {
				r.mu.Lock()
				err = r.pollOne(ctx, x, nil, errHeldBack, true, now, &sum)
				r.mu.Unlock()
			} else {
				err = r.notePollAttempt(ctx, x.id, now, pollCut)
			}
			if err != nil {
				return sum, err
			}
		}
	}
	// Client downloads: one torrents/info read per client (client.go).
	if err := r.pollClients(ctx, now, &sum); err != nil {
		return sum, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Failures behind new blocklist rows: search again now.
	n, err := r.researchBlocklisted(ctx)
	sum.Researched += n
	return sum, err
}

// pollSweeps is the first part of a poll pass, under r.mu, after the interrupted adds (checkStuck, settleStuck):
// stray queued downloads, deleted versions, ready deadlines; it returns the downloads whose status is read next, least
// recently polled first, and per provider how many downloads rest this pass because their reads keep failing.
func (r *Router) pollSweeps(ctx context.Context, now time.Time, sum *PollSummary) ([]pollRow, map[string]int, error) {
	// Queued downloads no route job will resume (jobs.go resumeStrays).
	resumed, failed, err := r.resumeStrays(ctx, now)
	if err != nil {
		return nil, nil, err
	}
	sum.Resumed, sum.Failed = resumed, sum.Failed+failed
	ended, err := r.endDeletedVersions(ctx, now)
	if err != nil {
		return nil, nil, err
	}
	sum.Failed += ended
	missingRows, err := r.st.Writer().QueryContext(ctx, `SELECT provider, COUNT(*) FROM download
		WHERE state IN ('adding', 'added', 'downloading', 'ready') AND provider IS NOT NULL GROUP BY provider`)
	if err != nil {
		return nil, nil, fmt.Errorf("router: poll providers: %w", err)
	}
	for missingRows.Next() {
		var key string
		var count int
		if err := missingRows.Scan(&key, &count); err != nil {
			missingRows.Close()
			return nil, nil, err
		}
		if r.provs[key] == nil {
			sum.Unpolled += count
			r.log.Warn("poll: provider unavailable; downloads left unpolled", "provider", key, "count", count)
		}
	}
	missingRows.Close()
	if err := missingRows.Err(); err != nil {
		return nil, nil, err
	}
	expired, err := r.expireReady(ctx, now)
	if err != nil {
		return nil, nil, err
	}
	sum.Failed += expired
	rows, err := r.st.Writer().QueryContext(ctx, `SELECT id, version_key, provider, provider_torrent_id, state,
		COALESCE(added_at, 0), COALESCE(own_add, 0), reused, info_hash, indexer, missing_polls,
		COALESCE(missing_since, 0), progress, COALESCE(progress_at, 0), COALESCE(poll_tried_at, 0), poll_errors,
		COALESCE(poll_error_since, 0), COALESCE(poll_outage_since, 0), stall_offset, files_selected,
		COALESCE(selected_at, 0), COALESCE(cached_check, '') FROM download
		WHERE state IN ('added', 'downloading') AND provider IS NOT NULL AND provider_torrent_id IS NOT NULL
		ORDER BY COALESCE(poll_tried_at, 0), id`)
	if err != nil {
		return nil, nil, fmt.Errorf("router: poll: %w", err)
	}
	var todo []pollRow
	resting := map[string]int{}
	for rows.Next() {
		var x pollRow
		var addedAt, missingSince, progressAt, triedAt, errorSince, pausedSince, offset, selectedAt int64
		if err := rows.Scan(&x.id, &x.version, &x.pk, &x.tid, &x.state, &addedAt, &x.ownAdd, &x.reused, &x.hash,
			&x.indexer, &x.missingPolls, &missingSince, &x.progress, &progressAt, &triedAt, &x.pollErrors,
			&errorSince, &pausedSince, &offset, &x.filesSelected, &selectedAt, &x.cachedCheck); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if errorSince > 0 {
			x.pollErrorSince = store.FromMillis(errorSince)
		}
		if pausedSince > 0 {
			x.pausedSince = store.FromMillis(pausedSince)
		}
		x.stallOffset = time.Duration(offset) * time.Millisecond
		if next := r.pollBackoff(ms(triedAt), x.pollErrors); next.After(now) {
			resting[x.pk]++
			continue // its reads keep failing: backed off
		}
		if addedAt > 0 {
			x.addedAt = store.FromMillis(addedAt)
		}
		if missingSince > 0 {
			x.missingSince = store.FromMillis(missingSince)
		}
		if progressAt > 0 {
			x.progressAt = store.FromMillis(progressAt)
		}
		if selectedAt > 0 {
			x.selectedAt = store.FromMillis(selectedAt)
		}
		todo = append(todo, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return todo, resting, nil
}

// pollOutageReads is how many status reads of one provider must fail at once, and be most of its reads in the
// pass, for the failures to be the provider's outage whatever its account says: a provider whose
// torrent endpoint fails while its account endpoint answers is still out. Fewer failures are a torrent's own,
// once another read or the account check shows the provider answering.
const pollOutageReads = 3

// pollCheckRounds bounds the account checks per provider in a pass: each round reads again what the failure
// back-off refused.
const pollCheckRounds = 3

// errHeldBack stands for a status read the gateway refused for the provider's failure back-off when it is recorded
// as an outage read.
var errHeldBack = errors.New("not read: the provider is backing off after failed reads")

// providerReads is how one provider's status reads went in a poll pass.
type providerReads struct {
	answered  int       // the provider's answer about the torrent: its status, not found, a 4xx
	failed    int       // sent, and failed without such an answer (5xx, 429, transport error, other refusals)
	failed5xx bool      // one of those was a 5xx or the provider saying it is unavailable (ErrUnavailable)
	held      []pollRow // refused for the provider's failure back-off: not sent
	up        bool      // the account check answered
	// resting: downloads not read this pass because their reads keep failing (pollBackoff). They count as failing:
	// with reads backed off one by one, a pass reads only some of an outage's downloads.
	resting int
}

// outage: most of the provider's downloads are failing at once, at least pollOutageReads of them: the reads that
// failed this pass and the downloads resting after failed reads. The reads the back-off held count toward the
// total, not the failures.
func (p *providerReads) outage() bool {
	failing := p.failed + p.resting
	return p.failed > 0 && failing >= pollOutageReads && 2*failing > p.answered+failing+len(p.held)
}

// down: the provider's outage, or nothing of it answered, neither a read nor the account check.
func (p *providerReads) down() bool {
	return p.outage() || (p.answered == 0 && !p.up)
}

// failureBackoff reports whether a read was refused by the gateway's back-off without being sent.
func failureBackoff(err error) bool {
	var refused *gateway.Refused
	return errors.As(err, &refused) && refused.Reason == gateway.ReasonBackoff
}

// pollOne records one status read (t, or the read's error) of download x, under r.mu. A download that moved on
// since the pass listed it (its state or torrent changed while the read was out) is left alone. outage: the read
// failed while the provider is down in the pass (providerReads.down): past the stall limit it ends as provider
// unavailable instead of stalled (endUnavailable).
func (r *Router) pollOne(ctx context.Context, x pollRow, t *provider.Torrent, err error, outage bool, now time.Time,
	sum *PollSummary) error {
	var cur, curTID string
	if qerr := r.st.Writer().QueryRowContext(ctx, `SELECT state, COALESCE(provider_torrent_id, '') FROM download
		WHERE id = ?`, x.id).Scan(&cur, &curTID); qerr != nil && !errors.Is(qerr, sql.ErrNoRows) {
		return fmt.Errorf("router: download %d: %w", x.id, qerr)
	}
	if cur != x.state || curTID != x.tid {
		return nil
	}
	kind := policy.KindMovie
	if vp := r.pol.Versions[x.version]; vp != nil {
		kind = vp.Kind
	}
	if errors.Is(err, provider.ErrNotFound) {
		if err := r.notePollAttempt(ctx, x.id, now, pollOK); err != nil {
			return err
		}
		x.endPause(now)
		if r.ignoringMissing(x.pk, now) {
			sum.Failed++
			if err := r.failDownload(ctx, x.id, "provider could not find torrent after settings change; release was not blocklisted",
				StateAdded, StateDownloading); err != nil {
				return err
			}
			r.log.Warn("poll: provider did not find torrent after settings change; release not blocklisted",
				"download", x.id, "provider", x.pk)
			return nil
		}
		gone, err := r.missingRead(ctx, x, now)
		if err != nil {
			return err
		}
		if !gone {
			return nil
		}
		if x.reused == 1 {
			// A torrent we reused (shared, not our add) that left the account says nothing about the release:
			// the route is queued again and adds it fresh (requeueReused). Nothing is blocklisted.
			return r.requeueReused(ctx, x)
		}
		if x.cachedCheck == checkUncached {
			// Our owed cached-only delete whose answer did not say (its journal kept): it went through. A miss, the
			// release routed again, nothing blocklisted.
			var journal string
			if err := r.st.Writer().QueryRowContext(ctx, `SELECT COALESCE(error, '') FROM download WHERE id = ?`,
				x.id).Scan(&journal); err != nil {
				return fmt.Errorf("router: download %d: %w", x.id, err)
			}
			if strings.HasPrefix(journal, deletingPrefix) {
				return r.owedDeleted(ctx, x, now, sum)
			}
		}
		sum.Failed++
		return r.failProviderDownload(ctx, x.id, "the torrent is no longer on "+x.pk)
	}
	if err != nil {
		// A failed read is no movement: the stall clock runs on (zurg-style). Past the
		// stall limit the download ends: as stalled and blocklisted when the provider answers otherwise (the
		// torrent's own failure), as provider unavailable, not blocklisted and routed again, when the provider is
		// down in this pass (unavailable).
		sum.Errors++
		if err := r.notePollAttempt(ctx, x.id, now, pollFailed); err != nil {
			return err
		}
		if !x.pausedSince.IsZero() {
			x.endPause(now)
			x.pollErrors, x.pollErrorSince = 0, time.Time{}
		}
		x.pollErrors++
		if x.pollErrorSince.IsZero() {
			x.pollErrorSince = now
		}
		r.log.Info("poll: status read failed", "provider", x.pk, "torrent", x.tid, "failed_reads", x.pollErrors,
			"provider_down", outage, "err", err)
		if why := r.blindStalled(x, now); why != "" {
			sum.Failed++
			if outage {
				return r.endUnavailable(ctx, x, why, now)
			}
			sum.Stalled++
			return r.failStalled(ctx, x, &provider.Torrent{}, now, why) // not seen: nothing deleted
		}
		return nil
	}
	// Waiting for a file selection the router may not send now (Pause on, a gate: our own switch):
	// the stall clock waits with it, so turning Pause on does not blocklist the release as stalled.
	// This is the only pause of the stall clock.
	gated := r.selectionGated(x, kind, t)
	if gated {
		if err := r.notePollAttempt(ctx, x.id, now, pollGated); err != nil {
			return err
		}
		if x.pausedSince.IsZero() {
			x.pausedSince = now
		}
	} else {
		if err := r.notePollAttempt(ctx, x.id, now, pollOK); err != nil {
			return err
		}
		x.endPause(now)
	}
	if x.missingPolls > 0 {
		if _, err := r.st.Writer().ExecContext(ctx, `UPDATE download SET missing_polls = 0, missing_since = NULL
			WHERE id = ?`, x.id); err != nil {
			return err
		}
	}
	// Explicit failures go through failure handling, even when the stall timeout has also elapsed.
	if err := r.checkTorrentHash(ctx, x.id, x.hash, t); err != nil {
		sum.Failed++
		r.log.Warn("poll: torrent check failed the download", "download", x.id, "provider", x.pk,
			"torrent", x.tid, "err", err)
		return nil
	}
	failed := t.State == provider.StateError || noVideo(x.reused, t) ||
		(x.reused == 0 && t.Status == rdWaitingFiles && len(t.Files) > 0 && len(ChooseFiles(kind, t.Files)) == 0)
	// An unfinished cached-only check is settled before the stall timeouts: an uncached add of ours is a miss
	// (deleted, routed again, nothing blocklisted), as the route's own check would have made it.
	// One it leaves open to settle soon (the owed delete waiting for the gate or a working delete, the grace after the
	// selection) is not ended by the stall timeouts meanwhile: that would blocklist the release and drop the check.
	check := checkGoOn
	if !failed {
		var err error
		if check, err = r.resumeCachedCheck(ctx, x, t, now, sum); err != nil || check == checkDone {
			return err
		}
	}
	if !failed && !gated && check != checkHold && r.stalled(x, t, now) {
		sum.Failed++
		sum.Stalled++
		why := fmt.Sprintf("stalled: not ready %s after the add (provider status %s)", r.stallAfter(x.pk), t.Status)
		return r.failStalled(ctx, x, t, now, why)
	}
	if !failed && check != checkHold && r.noProgress(x, t, now) {
		sum.Failed++
		sum.Stalled++
		why := fmt.Sprintf("stalled: no progress for %s while downloading (provider status %s, %.1f%%)",
			now.Sub(x.progressAt).Round(time.Minute), t.Status, t.Progress)
		return r.failStalled(ctx, x, t, now, why)
	}
	sel, err := r.applyStatus(ctx, x.id, x.version, kind, x.pk, t, true)
	if err != nil {
		sum.Errors++
		r.log.Warn("poll", "download", x.id, "err", err)
		return nil
	}
	if sel {
		sum.Selected++
	}
	state, _ := r.downloadState(ctx, x.id)
	switch state {
	case StateReady:
		sum.Ready++
	case StateFailed:
		sum.Failed++
	}
	return nil
}

// pollMark is how a status read ended, for notePollAttempt.
type pollMark int

const (
	// pollOK: the provider answered (the torrent's status, or that it is not found). The failed reads in a row are
	// cleared, and a gated pause of the stall clock ends (pollGated).
	pollOK pollMark = iota
	// pollFailed: a failed read, whether the provider is down or not: no movement, the stall clock runs on. It
	// counts a failed read in a row (after a gated pause the count starts again, and the pause ends).
	pollFailed
	// pollCut: cut by the pass deadline. Only the attempt time is recorded (it used to clear the streak).
	pollCut
	// pollGated: the provider answered, and the torrent waits for a file selection the router may not send now
	// (selectionGated). The failed reads in a row are cleared and the stall clocks stop from its start
	// (poll_outage_since, an older column name) until a read that is not gated. Our own switch is the only
	// pause: provider outages no longer stop the clock.
	pollGated
)

// selectionGated reports whether a read shows our Real-Debrid add waiting for the file selection that the write
// gate (mayWrite: grab or the router off, the version not grabbing) keeps the router from sending: the wait is the
// gate's, not the torrent's, so it does not count toward stalled_after.
func (r *Router) selectionGated(x pollRow, kind policy.Kind, t *provider.Torrent) bool {
	pv := r.pol.Providers[x.pk]
	if x.reused != 0 || x.filesSelected != 0 || x.state != StateAdded || t.Status != rdWaitingFiles ||
		pv == nil || pv.Kind != provider.KindRealDebrid || len(ChooseFiles(kind, t.Files)) == 0 {
		return false
	}
	if ad := r.adders[x.pk]; ad == nil || !ad.SelectsFiles() {
		return false
	}
	return r.mayWrite(x.version) != nil
}

// endPause is notePollAttempt's end of a gated pause in x as read: its time no longer counts on the stall clocks.
func (x *pollRow) endPause(now time.Time) {
	if x.pausedSince.IsZero() {
		return
	}
	d := max(now.Sub(x.pausedSince), 0)
	x.stallOffset += d
	if !x.progressAt.IsZero() {
		x.progressAt = x.progressAt.Add(d)
	}
	x.pausedSince = time.Time{}
}

// endPauseSQL is the SET part that ends a running gated pause at ?1 (pollRow.endPause).
const endPauseSQL = `stall_offset = stall_offset + CASE WHEN poll_outage_since IS NULL THEN 0
		ELSE MAX(?1 - poll_outage_since, 0) END,
	progress_at = CASE WHEN poll_outage_since IS NOT NULL AND progress_at IS NOT NULL
		THEN progress_at + MAX(?1 - poll_outage_since, 0) ELSE progress_at END,
	poll_outage_since = NULL`

// notePollAttempt records a status read of download id tried at now, ended as mark says.
func (r *Router) notePollAttempt(ctx context.Context, id int64, now time.Time, mark pollMark) error {
	var q string
	switch mark {
	case pollOK:
		q = `UPDATE download SET poll_tried_at = ?1, poll_errors = 0, poll_error_since = NULL, ` + endPauseSQL +
			` WHERE id = ?2`
	case pollFailed:
		q = `UPDATE download SET poll_tried_at = ?1,
			poll_errors = CASE WHEN poll_outage_since IS NULL THEN poll_errors + 1 ELSE 1 END,
			poll_error_since = CASE WHEN poll_outage_since IS NULL THEN COALESCE(poll_error_since, ?1) ELSE ?1 END, ` +
			endPauseSQL + ` WHERE id = ?2`
	case pollGated:
		q = `UPDATE download SET poll_tried_at = ?1, poll_errors = 0, poll_error_since = NULL,
			poll_outage_since = COALESCE(poll_outage_since, ?1) WHERE id = ?2`
	default:
		q = `UPDATE download SET poll_tried_at = ?1 WHERE id = ?2`
	}
	if _, err := r.st.Writer().ExecContext(ctx, q, store.Millis(now), id); err != nil {
		return fmt.Errorf("router: download %d: %w", id, err)
	}
	return nil
}

// torrentAnswer reports whether a failed status read is the provider's own answer about that torrent: an error
// answer below 500 that is neither a rate limit, an authentication refusal nor the provider saying it is unavailable.
// Anything else (a gateway refusal, a transport error, a 5xx, a 429, a refused key, AllDebrid's MAINTENANCE or
// TorBox's server errors on HTTP 200 (ErrUnavailable), an answer that does not parse) says nothing
// about the torrent.
func torrentAnswer(err error) bool {
	var ae *provider.APIError
	if !errors.As(err, &ae) || errors.Is(err, provider.ErrAuth) || errors.Is(err, provider.ErrUnavailable) {
		return false
	}
	return ae.Status < 500 && ae.Status != http.StatusTooManyRequests
}

// blindStalled says why a download whose status reads keep failing counts as stalled ("" when it does not): at
// least blindReads failed reads in a row, now past the stall timeout that applies (stalled_after since the add while
// added, downloading_timeout since the last progress while downloading). A failed read is no movement, so read
// errors neither stop nor restart the clock; the reads in a row only make sure one
// failed read does not decide. Nothing is deleted: the torrent was not seen.
func (r *Router) blindStalled(x pollRow, now time.Time) string {
	if x.pollErrors < blindReads || x.pollErrorSince.IsZero() {
		return ""
	}
	blind := now.Sub(x.pollErrorSince)
	switch {
	case x.state == StateAdded && !x.addedAt.IsZero():
		if d := r.stallAfter(x.pk); d > 0 && now.Sub(x.addedAt)-x.stallOffset >= d {
			return fmt.Sprintf("stalled: not seen ready %s after the add; its status reads failed %d times in a "+
				"row over %s", d, x.pollErrors, blind.Round(time.Minute))
		}
	case x.state == StateDownloading && !x.progressAt.IsZero():
		if d := r.set.DownloadingTimeout; d > 0 && now.Sub(x.progressAt) >= d {
			return fmt.Sprintf("stalled: no progress seen for %s while downloading; its status reads failed %d "+
				"times in a row over %s", now.Sub(x.progressAt).Round(time.Minute), x.pollErrors,
				blind.Round(time.Minute))
		}
	}
	return ""
}

// missingRead records one not-found read of a download's torrent and reports whether the torrent counts as gone:
// missingPolls consecutive misses spanning missingGrace.
func (r *Router) missingRead(ctx context.Context, x pollRow, now time.Time) (bool, error) {
	since := x.missingSince
	if x.missingPolls == 0 || since.IsZero() {
		since = now
	}
	polls := x.missingPolls + 1
	if polls >= missingPolls && now.Sub(since) >= missingGrace {
		return true, nil
	}
	if _, err := r.st.Writer().ExecContext(ctx, `UPDATE download SET missing_polls = ?, missing_since = ? WHERE id = ?`,
		polls, store.Millis(since), x.id); err != nil {
		return false, fmt.Errorf("router: download %d: %w", x.id, err)
	}
	r.log.Info("poll: provider did not find the torrent; failing it only if it stays missing", "download", x.id,
		"provider", x.pk, "torrent", x.tid, "misses", polls, "since", since.UTC(), "grace", missingGrace)
	return false, nil
}

func (r *Router) downloadState(ctx context.Context, id int64) (string, error) {
	var s string
	err := r.st.Writer().QueryRowContext(ctx, `SELECT state FROM download WHERE id = ?`, id).Scan(&s)
	return s, err
}

// applyStatus records a provider's view of a download and selects Real-Debrid files for non-reused torrents waiting
// for them; it reports whether it selected files. The selection is ChooseFiles', or altPick's for the torrent of a
// cached-only route's second add (select_alt_torrent, altRetry): recorded with the torrent, so the poller and a
// restart send the set the route meant when its own selection failed.
func (r *Router) applyStatus(ctx context.Context, dl int64, version string, kind policy.Kind, pk string,
	t *provider.Torrent, polled bool) (bool, error) {
	w := r.st.Writer()
	now := store.Millis(r.now())
	var selected, cachedNull, reused, triedSelect, alt int
	var cur, hash string
	if err := w.QueryRowContext(ctx, `SELECT state, files_selected, cached IS NULL, reused, info_hash,
		COALESCE(error, '') LIKE 'selecting files: %', COALESCE(select_alt_torrent = ?, 0) FROM download WHERE id = ?`,
		t.ID, dl).Scan(&cur, &selected, &cachedNull, &reused, &hash, &triedSelect, &alt); err != nil {
		return false, fmt.Errorf("router: download %d: %w", dl, err)
	}
	pick := ChooseFiles
	if alt == 1 {
		pick = altPick
	}
	if cur != StateAdded && cur != StateDownloading {
		return false, nil
	}
	if err := r.checkTorrentHash(ctx, dl, hash, t); err != nil {
		return false, err
	}
	// Sonarr v5's grab-time file check (extensions.go): once the provider lists the files, a torrent with a
	// potentially dangerous or executable file fails and is blocklisted, before any selection or import.
	if len(t.Files) > 0 {
		paths := make([]string, len(t.Files))
		for i, f := range t.Files {
			paths[i] = f.Path
		}
		if why := TorrentFileRejection(paths); why != "" {
			r.deleteUnselected(ctx, dl, t)
			return false, r.rejectTorrentFiles(ctx, dl, version, why)
		}
	}
	// A reused or finished torrent with no video: nothing to import, and no selection of ours can change that. It
	// fails now, blocklisted on our side only; the torrent stays on the provider (a reused one is shared).
	if noVideo(reused, t) {
		why := "no video file in the torrent; the finished torrent was kept on " + pk
		if reused == 1 {
			why = "no video file in the torrent; the reused torrent is shared and was kept on " + pk
		}
		return false, r.failProviderDownload(ctx, dl, why)
	}
	pv := r.pol.Providers[pk]
	selects := pv != nil && pv.Kind == provider.KindRealDebrid
	if t.Status == rdWaitingFiles && selected == 0 && reused == 0 {
		ids := pick(kind, t.Files)
		if len(ids) == 0 {
			if len(t.Files) == 0 {
				return false, nil // files not known yet
			}
			r.deleteUnselected(ctx, dl, t)
			return false, r.failProviderDownload(ctx, dl, "no video file in the torrent; nothing selected")
		}
		if err := r.mayWrite(version); err != nil {
			r.log.Warn("files not selected: the router may not write for this version now", "download", dl,
				"version", version, "why", err)
			return false, nil
		}
		ad := r.adders[pk]
		if ad == nil || !ad.SelectsFiles() {
			return false, nil
		}
		wctx, cancel := context.WithTimeout(ctx, providerWriteLimit)
		err := ad.SelectFiles(wctx, t.ID, ids)
		cancel()
		if err != nil {
			_, _ = w.ExecContext(ctx, `UPDATE download SET error = ?, updated_at = ? WHERE id = ?`,
				"selecting files: "+err.Error(), now, dl)
			return false, err
		}
		r.log.Info("files selected", "provider", pk, "torrent", t.ID, "files", len(ids), "download", dl)
		// The selection's grace (cachedcheck.go) counts from its answer, not from before the request.
		_, err = w.ExecContext(ctx, `UPDATE download SET files_selected = 1, selected_at = ?, provider_status = ?,
			name = ?, error = NULL, updated_at = ?, polled_at = ? WHERE id = ?`, store.Millis(r.now()), t.Status, t.Name,
			now, now, dl)
		return err == nil, err
	}
	if selects && selected == 0 {
		inferred, err := r.inferSelection(ctx, dl, pk, t, reused, triedSelect, now)
		if err != nil {
			return false, err
		}
		if inferred {
			selected = 1
		}
	}
	state := cur
	var errMsg any
	switch t.State {
	case provider.StateDownloaded:
		state = StateReady
	case provider.StateDownloading:
		// Real-Debrid converts the magnet before it waits for a selection; it only downloads after one.
		if t.Status != rdWaitingFiles && (selected == 1 || reused == 1 || !selects) {
			state = StateDownloading
		}
	case provider.StateQueued:
		// Queued on the provider (AllDebrid's queue, RD converting or queued, TorBox queued or stalled) is not
		// fetching: a download still added stays added, so the stall timeout applies. One already
		// downloading stays so, under the no-progress timeout.
	case provider.StateError:
		state, errMsg = StateFailed, "provider status "+t.Status
	}
	// cached: known once the first read after the add shows it downloaded (1) or still fetching (0).
	cached := any(nil)
	if cachedNull == 1 {
		switch state {
		case StateReady:
			if cur == StateAdded {
				cached = 1
			}
		case StateDownloading:
			cached = 0
		}
	}
	var polledAt any
	if polled {
		polledAt = now
	}
	err := r.st.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE download SET state = ?, provider_status = ?,
		name = COALESCE(NULLIF(?, ''), name), progress = ?, cached = COALESCE(cached, ?),
		original_name = COALESCE(NULLIF(?, ''), original_name),
		error = CASE WHEN ? IS NOT NULL THEN ? ELSE error END,
		ready_at = CASE WHEN ? = 'ready' AND ready_at IS NULL THEN ? ELSE ready_at END,
		progress_at = CASE WHEN ? = 'downloading' AND (progress_at IS NULL OR state <> 'downloading' OR ? > progress)
			THEN ? ELSE progress_at END,
		polled_at = COALESCE(?, polled_at), updated_at = ? WHERE id = ? AND state = ?`,
			state, t.Status, t.Name, t.Progress, cached, t.OriginalName, errMsg, errMsg, state, now, state, t.Progress,
			now, polledAt, now, dl, cur)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errStatusMoved // moved on since it was read: nothing recorded over it
		}
		if state == StateFailed {
			if err := blocklistDownload(ctx, tx, dl, "provider status "+t.Status, BlocklistProvider, now); err != nil {
				return err
			}
		}
		return recordProgress(ctx, tx, dl, now, pk, t.ID, t.Progress,
			state == StateDownloading && t.State == provider.StateDownloading)
	})
	if errors.Is(err, errStatusMoved) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("router: download %d: %w", dl, err)
	}
	if state == StateDownloading && cur == StateAdded {
		r.uncachedAdd(ctx, pk)
	}
	if state == StateReady {
		r.notifyReady()
	}
	return false, nil
}

// errStatusMoved: the download left the state applyStatus read before its write.
var errStatusMoved = errors.New("router: the download moved on before its status was recorded")

func (r *Router) checkTorrentHash(ctx context.Context, dl int64, hash string, t *provider.Torrent) error {
	if t == nil || indexer.NormalizeHash(t.Hash) == "" {
		return nil
	}
	if indexer.NormalizeHash(t.Hash) == indexer.NormalizeHash(hash) {
		return nil
	}
	why := "provider returned a different torrent for this ID"
	if err := r.failDownload(ctx, dl, why, StateAdded, StateDownloading); err != nil {
		return err
	}
	return errors.New(why)
}

// rejectTorrentFiles fails a download whose torrent holds a potentially dangerous or executable file and blocklists
// its hash for the version, in one transaction, as v5 blocklists the release (TorrentClientBase.ValidateFileNames).
// Nothing is selected and nothing is deleted on the provider.
func (r *Router) rejectTorrentFiles(ctx context.Context, dl int64, version, why string) error {
	now := store.Millis(r.now())
	err := r.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var hash, release string
		var titleID int64
		if err := tx.QueryRowContext(ctx, `SELECT info_hash, title_id, release_title FROM download WHERE id = ?`, dl).
			Scan(&hash, &titleID, &release); err != nil {
			return err
		}
		r.log.Warn("torrent rejected; blocklisting release", "download", dl, "release", release, "why", why)
		res, err := tx.ExecContext(ctx, `UPDATE download SET state = 'failed', error = ?, updated_at = ? WHERE id = ?
			AND state IN ('added', 'downloading')`, why, now, dl)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return nil // ended meanwhile: its own reason stands
		}
		return Blocklist(ctx, tx, version, hash, titleID, dl, release, why, BlocklistTorrentFiles, now)
	})
	if err != nil {
		return fmt.Errorf("router: download %d: %w", dl, err)
	}
	return nil
}

// uncachedAdd counts an add that turned out uncached against the provider's hourly cap.
func (r *Router) uncachedAdd(ctx context.Context, pk string) {
	now := r.now()
	if err := updatePace(ctx, r.st, pk, now, func(_ *sql.Tx, p *pace) (bool, error) {
		p.uncached(now)
		return true, nil
	}); err != nil {
		r.log.Warn("pacing", "provider", pk, "err", err)
	}
}

// Run polls every poll_interval until ctx ends. It does nothing while no download is in flight.
func (r *Router) Run(ctx context.Context) { r.RunEvery(ctx, r.set.PollInterval) }

// RunEvery is Run with the poll interval given: theatarr serve restarts the loop with the interval of a newly saved
// router section, while each pass polls through the current runtime's router.
func (r *Router) RunEvery(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = r.set.PollInterval
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			active, sum, err := r.pollOnce(ctx)
			if active == nil {
				return
			}
			if err != nil && ctx.Err() == nil {
				active.log.Warn("poll", "err", err)
			} else if sum.Read > 0 || sum.Failed > 0 || sum.Unpolled > 0 {
				active.log.Info("poll", "read", sum.Read, "ready", sum.Ready, "failed", sum.Failed,
					"unpolled", sum.Unpolled, "errors", sum.Errors)
			}
		}
	}
}

func (r *Router) pollOnce(ctx context.Context) (*Router, PollSummary, error) {
	active, pollCtx := r, ctx
	if r.runtime != nil {
		pollCtx, active = r.runtime(ctx)
	}
	if active == nil {
		return nil, PollSummary{}, nil
	}
	sum, err := active.Poll(pollCtx)
	return active, sum, err
}

// endDeletedVersions ends downloads of a version deleted from settings (no longer in policy versions): queued, on
// the provider or ready, they fail in Theatarr's records with the reason. Nothing is blocklisted (the release says nothing
// about itself) and no provider is read or written: the torrent stays on the account, as every torrent Theatarr did not
// prove its own and delete at once does. Unsettled adds (reconciliation settles them first), imports and ready
// downloads a live import claim holds are left alone; the next poll sees them once settled or released.
func (r *Router) endDeletedVersions(ctx context.Context, now time.Time) (int, error) {
	// No versions left (the last one was removed) means every version-bound download belongs to a deleted version.
	keys := make([]any, 0, len(r.pol.Versions)+2)
	at := store.Millis(now)
	keys = append(keys, at, at)
	notIn := ""
	if len(r.pol.Versions) > 0 {
		notIn = ` AND version_key NOT IN (?` + strings.Repeat(", ?", len(r.pol.Versions)-1) + `)`
		for k := range r.pol.Versions {
			keys = append(keys, k)
		}
	}
	res, err := r.st.Writer().ExecContext(ctx, `UPDATE download SET state = 'failed',
		error = 'version "' || version_key || '" is no longer in settings; the download was ended, nothing blocklisted',
		updated_at = ? WHERE state IN ('queued', 'added', 'downloading', 'ready')
		AND NOT EXISTS (SELECT 1 FROM library_claim c WHERE c.download_id = download.id AND c.until > ?)
		AND COALESCE(version_key, '') <> ''`+notIn, keys...)
	if err != nil {
		return 0, fmt.Errorf("router: end downloads of deleted versions: %w", err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		r.log.Warn("poll: downloads of versions no longer in settings ended; nothing blocklisted or deleted", "count", n)
	}
	return int(n), nil
}

// inferSelection records the file selection of download dl on Real-Debrid (pk) as made when it was lost: a selection
// of ours that failed on our side (cut by providerWriteLimit, a lost answer) may have reached Real-Debrid, and a later
// read t showing our own add past the selection wait means it did. Recorded as selected, selected_at now, so the
// download goes on instead of stalling at added. It requires a selection we sent (triedSelect) on a non-reused
// torrent; deletion ownership (own_add) is independent, and can be absent when cached-only and stalled_after are off.
// The caller checks the provider is Real-Debrid and no selection is recorded.
func (r *Router) inferSelection(ctx context.Context, dl int64, pk string, t *provider.Torrent, reused, triedSelect int,
	now int64) (bool, error) {
	if reused != 0 || triedSelect != 1 || t.Status == rdWaitingFiles || t.Status == "magnet_conversion" ||
		(t.State != provider.StateDownloading && t.State != provider.StateDownloaded && t.State != provider.StateQueued) {
		return false, nil
	}
	if _, err := r.st.Writer().ExecContext(ctx, `UPDATE download SET files_selected = 1, selected_at = ?1,
		error = NULL, updated_at = ?1 WHERE id = ?2 AND files_selected = 0`, now, dl); err != nil {
		return false, fmt.Errorf("router: download %d: %w", dl, err)
	}
	r.log.Info("files selection inferred: the provider moved past the selection wait after our selection "+
		"request failed", "provider", pk, "torrent", t.ID, "status", t.Status, "download", dl)
	return true, nil
}
