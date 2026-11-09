package router

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/store"
)

// Adds after a crash (docs/design/router.md "Crash between add and record").
//
// A route job runs at least once, and the provider add and its record are two steps: a crash, a lost lease or an
// answer that does not say (a transport error, a 5xx, a timeout, an answer that does not parse) leaves the download
// in adding with the provider it was sent to and the attempt's token (markAdding), and the add may or may not have
// reached the account. Adding again would make a second entry of the same hash on the account. So a download found
// in adding is settled from the provider first, and only then may it be added again:
//
//   - the hash is held already (the registry from the provider sync, a download of ours) or the account's torrent
//     list shows it: the download points at that torrent, as a reuse would, and nothing is added (code reconciled);
//   - the list does not show it and the add was sent more than addSettle ago (no request still in flight from any
//     process can land after that): the add never landed. A route resumes the download (queued again, then added
//     as usual); the poller, which cannot resume a route, fails it with that reason;
//   - the add was sent less than addSettle ago, the list cannot be read, or no read-only client is there: it stays
//     in adding, unsettled, with the reason on the row (Activity, and health.UnsettledAdds past 10 minutes). Time
//     alone never settles it: an unknown outcome never turns into permission to add again.
//
// A row from a build before add_attempt that does not name its provider is checked on every provider of its
// version. Every write that settles an add matches the attempt's token, so a route that lost the row to another
// process (which settled the add, or resumed and added it) changes nothing, and a late answer cannot overwrite a
// newer result.
//
// Reconciliation reads; it never deletes anything. The entry it adopts is never treated as provably ours (own_add
// stays unset), so neither the cached-only settle nor the stall timeout can delete it. An entry no other download of
// ours holds is the one our journaled add (the adding row and its attempt) most likely made: it is adopted as an add
// of ours, not a reuse, so the poller selects its files on Real-Debrid; otherwise it would sit in
// waiting_files_selection until the stall timeout blocklisted a good release. An entry another
// download of ours holds is a reuse, as at the add.

// Step code for a download settled from the account after an interrupted add.
const codeReconciled = "reconciled"

// Step code for an add whose answer does not say whether the provider took it.
const codeUnsettled = "add_unsettled"

// reconcileAfter is how long the poller leaves a download in adding before it settles it from the account (a route
// job retried sooner settles it itself).
const reconcileAfter = time.Minute

// addSettle is how long after it was sent an add can no longer land: well past the gateway's whole-request timeout
// (60s) and its wait for budget (60s). A list without the hash before then proves nothing.
const addSettle = 5 * time.Minute

// reconciled is how reconcileAdding ended.
type reconciled int

const (
	recNone    reconciled = iota // not in adding: nothing to do
	recFound                     // on the account: the download is added now (torrent id returned)
	recAbsent                    // not on the account: the caller queues the download again or fails it
	recUnknown                   // cannot tell yet: left in adding
)

// addCheck is what reconcileAdding found.
type addCheck struct {
	rec      reconciled
	pk, tid  string
	why      string
	attempt  sql.NullString // the add attempt it settles: the fence for the caller's write
	provider sql.NullString // the row's provider as stored (NULL on an old row)
	sent     time.Time      // when the add was sent
	hash     string
	shared   bool   // recFound: another download of ours holds the torrent (a reuse); else it is adopted as our add
	client   string // a client download's client (settleClient); pk is then ""
}

// reconcileAdding settles download dl if it is in adding.
func (r *Router) reconcileAdding(ctx context.Context, dl int64) (addCheck, error) {
	c, err := r.lookupAdding(ctx, dl, nil)
	if err != nil {
		return c, err
	}
	return r.applyAdding(ctx, dl, c)
}

// accountLists holds each provider's account list for one recovery sweep: one listing per provider, however many
// interrupted adds it settles. Each listing has its own share of the pass (slice), so one provider that
// does not answer cannot use up the pass and starve the interrupted adds on the others.
type accountLists struct {
	m     map[string]accountList
	slice time.Duration
}

type accountList struct {
	list []provider.Torrent
	err  error
}

// errListingCut is a listing cut short by its share of the recovery pass: it shows neither presence nor absence.
var errListingCut = errors.New("the account listing took longer than its share of the recovery pass")

// accountList reads provider pk's torrent list once per sweep (lists nil: every call reads, with no slice).
func (r *Router) accountList(ctx context.Context, rp provider.Provider, pk string, lists *accountLists) (
	[]provider.Torrent, error) {
	if lists == nil {
		return rp.Torrents(ctx)
	}
	if l, ok := lists.m[pk]; ok {
		return l.list, l.err
	}
	sctx, cancel := context.WithTimeout(ctx, lists.slice)
	defer cancel()
	list, err := rp.Torrents(sctx)
	// Only the listing's own deadline is a cut; any other error (a refused key, a 5xx) is recorded as it is.
	if err != nil && errors.Is(sctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		err = errListingCut
	}
	lists.m[pk] = accountList{list, err}
	return list, err
}

// lookupAdding is reconcileAdding's reads: what the provider shows for download dl in adding. It writes nothing of
// the download (applyAdding does, fenced by the attempt it read), so the poller runs it without the router lock.
//
// A listing cut short (errListingCut) that leaves the hash unfound returns recNone: nothing is settled or noted from
// it, and the caller only records that it was tried.
func (r *Router) lookupAdding(ctx context.Context, dl int64, lists *accountLists) (addCheck, error) {
	var c addCheck
	var state, hash, version string
	var sent int64
	err := r.st.Writer().QueryRowContext(ctx, `SELECT state, provider, info_hash, version_key, add_attempt,
		updated_at FROM download WHERE id = ?`, dl).Scan(&state, &c.provider, &hash, &version, &c.attempt, &sent)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("router: download %d: %w", dl, err)
	}
	if state != StateAdding {
		return c, nil
	}
	c.hash = hash
	c.sent = store.FromMillis(sent)
	pks := []string{c.provider.String}
	if !c.provider.Valid || c.provider.String == "" {
		pks = nil
		if vp := r.pol.Versions[version]; vp != nil {
			pks = vp.Providers
		}
	}
	c.pk = strings.Join(pks, ", ")
	var unknown []string
	cut := false
	for _, pk := range pks {
		// A known id the provider did not answer for is not adopted: the account's list below decides,
		// and when that cannot be read either the outcome stays unknown.
		tid, unread, err := r.heldBy(ctx, hash, pk, true)
		if err != nil {
			return c, err
		}
		if tid != "" && unread == nil {
			c.pk, c.tid, c.why = pk, tid, "the hash is held on "+pk+" already (torrent "+tid+")"
			break
		}
		rp := r.provs[pk]
		if rp == nil {
			unknown = append(unknown, "no read-only client for "+pk)
			continue
		}
		list, err := r.accountList(ctx, rp, pk, lists)
		if errors.Is(err, errListingCut) {
			cut = true
			continue
		}
		if err != nil {
			unknown = append(unknown, "reading "+pk+"'s torrents failed: "+err.Error())
			continue
		}
		for _, t := range list {
			if strings.EqualFold(t.Hash, hash) && t.ID != "" {
				c.pk, c.tid, c.why = pk, t.ID, "the account shows the hash on "+pk+" (torrent "+t.ID+")"
				break
			}
		}
		if c.tid != "" {
			break
		}
	}
	if c.tid != "" {
		c.rec = recFound
		err := r.st.Writer().QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM download WHERE provider = ?
			AND provider_torrent_id = ? AND id != ?)`, c.pk, c.tid, dl).Scan(&c.shared)
		if err != nil {
			return c, fmt.Errorf("router: download %d: %w", dl, err)
		}
		return c, nil
	}
	if cut {
		return c, nil // recNone: incomplete, neither found nor absent
	}
	age := r.now().Sub(store.FromMillis(sent))
	switch {
	case len(pks) == 0:
		unknown = append(unknown, "the interrupted add does not name its provider")
	case age < addSettle:
		unknown = append(unknown, fmt.Sprintf("sent %s ago, it may still land", age.Round(time.Second)))
	}
	if len(unknown) > 0 {
		c.rec, c.why = recUnknown, strings.Join(unknown, "; ")
		return c, nil
	}
	c.rec, c.why = recAbsent, "the interrupted add is not on "+c.pk
	return c, nil
}

// applyAdding records what lookupAdding found for download dl: an unknown outcome is noted on the row, a found
// torrent adopted. Both writes match the attempt lookupAdding read, so a download settled or resumed meanwhile is
// left alone. recAbsent is the caller's (a route resumes the download, the poller fails it).
func (r *Router) applyAdding(ctx context.Context, dl int64, c addCheck) (addCheck, error) {
	switch c.rec {
	case recUnknown:
		return c, r.noteUnsettled(ctx, dl, c.attempt, c.why, nil)
	case recFound:
	default:
		return c, nil
	}
	// Nothing proves the entry is our own new one, so nothing may ever delete it (own_add stays unset). Unless
	// another download of ours holds it, it is adopted as our add (reused 0) so its files are selected.
	if err := r.markAdded(ctx, dl, c.pk, &adder.Added{ID: c.tid, Hash: c.hash}, c.shared, fromAdding(c.attempt)); err != nil {
		if errors.Is(err, errAddTaken) {
			c.rec = recNone // settled by another route meanwhile
			return c, nil
		}
		return c, err
	}
	r.log.Info("interrupted add settled from the provider", "download", dl, "provider", c.pk, "torrent", c.tid,
		"why", c.why)
	return c, r.recordAttempt(ctx, dl, Step{Provider: c.pk, Outcome: "accepted", Code: codeReconciled,
		Message: c.why + "; nothing added"})
}

// Recovery in the poll pass is bounded: at most reconcileBatch interrupted adds per pass, least recently
// checked first, and their provider reads stop at reconcilePassLimit. The reads run without the router lock, with one
// account listing per provider per pass; the results are applied under it, each fenced by its add attempt. Each
// provider's listing gets an equal share of the pass, and every add looked up is marked tried (poll_tried_at), even
// when its lookup was cut short, so a provider that never answers in time cannot keep its adds first in line and
// starve the others'.
var reconcilePassLimit = time.Minute

const reconcileBatch = 25

// stuckCheck is one interrupted add the poller looked up.
type stuckCheck struct {
	id int64
	c  addCheck
}

// checkStuck looks up the downloads left in adding for reconcileAfter with no route job to settle them (the
// poller, without r.mu): the reads only.
func (r *Router) checkStuck(ctx context.Context, now time.Time) ([]stuckCheck, error) {
	// Only adds the poller can check: one naming a provider with a read-only client, or an old row naming none.
	var keys []string
	for k, p := range r.provs {
		if p != nil {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	args := []any{store.Millis(now.Add(-reconcileAfter))}
	known := ""
	if len(keys) > 0 {
		known = " OR provider IN (?" + strings.Repeat(", ?", len(keys)-1) + ")"
		for _, k := range keys {
			args = append(args, k)
		}
	}
	args = append(args, reconcileBatch)
	// A download whose route job is queued or running is the route's to settle: the route resumes it when the add
	// never landed, where the poller could only fail it (and an ended download is never resumed: Dismiss).
	rows, err := r.st.Writer().QueryContext(ctx, `SELECT id FROM download WHERE state = 'adding'
		AND updated_at < ? AND client IS NULL AND (COALESCE(provider, '') = ''`+known+`) AND NOT `+pendingRouteSQL+`
		ORDER BY COALESCE(poll_tried_at, 0), id LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("router: poll: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, reconcilePassLimit)
	defer cancel()
	lists := &accountLists{m: map[string]accountList{}, slice: reconcilePassLimit / time.Duration(max(len(keys), 1))}
	var out []stuckCheck
	for _, id := range ids {
		if rctx.Err() != nil {
			break
		}
		c, err := r.lookupAdding(rctx, id, lists)
		if rctx.Err() != nil && ctx.Err() == nil {
			// Cut by the deadline: what it read is incomplete. It is marked tried (no result), so the next pass
			// starts with the adds not looked at yet.
			r.log.Info("poll: recovery deadline reached; the rest is checked next pass", "limit", reconcilePassLimit)
			out = append(out, stuckCheck{id: id})
			break
		}
		if err != nil {
			return out, err
		}
		out = append(out, stuckCheck{id, c})
	}
	return out, nil
}

// settleStuck applies checkStuck's results (under r.mu). It returns how many downloads it settled either way, and
// how many of those it failed.
func (r *Router) settleStuck(ctx context.Context, checks []stuckCheck, now time.Time) (n, failed int, err error) {
	for _, x := range checks {
		id := x.id
		if _, err := r.st.Writer().ExecContext(ctx, `UPDATE download SET poll_tried_at = ? WHERE id = ?
			AND state = 'adding'`, store.Millis(now), id); err != nil {
			return n, failed, fmt.Errorf("router: download %d: %w", id, err)
		}
		c, err := r.applyAdding(ctx, id, x.c)
		if err != nil {
			return n, failed, err
		}
		switch c.rec {
		case recFound:
			n++
		case recAbsent:
			ok, err := r.settleAdding(ctx, id, c.attempt, StateFailed, "the add was interrupted and never reached "+c.pk)
			if err != nil {
				return n, failed, err
			}
			if !ok {
				continue
			}
			n++
			failed++
			if err := r.recordAttempt(ctx, id, Step{Provider: c.pk, Outcome: "error", Code: codeReconciled,
				Message: c.why}); err != nil {
				return n, failed, err
			}
		case recUnknown:
			r.log.Info("interrupted add not settled yet", "download", id, "provider", c.pk, "why", c.why)
		}
	}
	return n, failed, nil
}

// settleAdding moves download dl out of adding, if it is still in the attempt reconcileAdding read: to queued with
// no provider (a route resumes it) or to failed with why. It reports whether it did.
func (r *Router) settleAdding(ctx context.Context, dl int64, attempt sql.NullString, state, why string) (bool,
	error) {
	var errText any
	if state == StateFailed {
		errText = why
	}
	err := r.move(ctx, dl, transition{from: []string{StateAdding}, attempt: &attempt,
		set: `state = ?, provider = CASE WHEN ? = 'queued' THEN NULL ELSE provider END,
		client = CASE WHEN ? = 'queued' THEN NULL ELSE client END, add_attempt = NULL, error = ?, updated_at = ?`,
		args: []any{state, state, state, errText, store.Millis(r.now())}})
	if errors.Is(err, errAddTaken) {
		return false, nil
	}
	return err == nil, err
}
