package router

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/store"
)

// Copies to other providers (docs/design/router.md "Copies to other providers"): "Add to other providers" adds a
// torrent the library holds to every configured provider that does not hold it yet, one provider_copy row per hash
// and provider (migration 00128), each worked by one JobCopy job. A copy is no download: no download row, import,
// offer, blocklist, re-search or History entry.
//
// A copy goes queued -> adding (written before the add is sent; own_add: the account's listing did not show the
// hash) -> checking (torrent id stored right after the add) -> added, not_cached, refused, skipped or failed. An add
// that is not cached is deleted on every provider (the owner's decision; AllDebrid and TorBox through
// adder.StalledDeleter, which survives a restart); the only guard is that an entry on the account before our add is
// not ours. Recovery: at start (ResumeCopies) and on each job retry the row's own state says what is left: an adding
// row without a torrent id waits addSettle, then finds its torrent on the account by hash (own_add forced false: it
// may be an older one) or goes back to queued; a checking row is settled from the account, and a delete owed before
// the answer was lost ends not_cached when the torrent is gone.

// JobCopy works one provider_copy row (payload copyPayload).
const JobCopy = "router.copy"

// Copy states (provider_copy.state).
const (
	CopyQueued    = "queued"
	CopyAdding    = "adding"
	CopyChecking  = "checking"
	CopyAdded     = "added"
	CopyNotCached = "not_cached"
	CopyRefused   = "refused"
	CopySkipped   = "skipped"
	CopyFailed    = "failed"
)

// copyUnfinished is the SQL list of the states a copy is still being worked in.
const copyUnfinished = `('queued', 'adding', 'checking')`

// copyDeletingPrefix starts provider_copy.message while our add's delete is owed (written before the delete is sent):
// a torrent found gone afterwards is our delete's work, and the copy ends not_cached; it is never added again.
const copyDeletingPrefix = "deleting our add "

// ErrCopyPaused: Pause is on (or the router is off); nothing is added to another provider.
var ErrCopyPaused = errors.New("Paused: nothing is searched, downloaded or linked for any library.")

// ErrCopyNoHash: the file has no v1 torrent hash on record.
var ErrCopyNoHash = errors.New("Cannot add to other providers: Theatarr has no torrent hash on record for this file.")

// CopyView is one copy as the title page and the Torrents page show it.
type CopyView struct {
	Provider  string    `json:"provider"`
	State     string    `json:"state"`
	Message   string    `json:"message,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CopyRequest names the torrent to add to the other providers.
type CopyRequest struct {
	Hash, Name string
	TitleID    int64
	// Except is a provider known to hold it (the download's, a Torrents row's): no row for it.
	Except string
}

type copyPayload struct {
	ID int64 `json:"id"`
}

// copyRow is one provider_copy row.
type copyRow struct {
	id                  int64
	hash, name, pk, tid string
	titleID             int64
	ownAdd              bool
	state, message      string
	updatedAt           time.Time
}

// copyPaused says whether Pause is on for this router or the active one.
func (r *Router) copyPaused() bool {
	if !r.set.Enabled {
		return true
	}
	if r.current != nil {
		if cur := r.current(); cur != nil && cur != r && !cur.set.Enabled {
			return true
		}
	}
	return false
}

// providerWords names a provider kind for people.
func providerWords(kind string) string {
	switch kind {
	case provider.KindRealDebrid:
		return "Real-Debrid"
	case provider.KindAllDebrid:
		return "AllDebrid"
	case provider.KindTorBox:
		return "TorBox"
	}
	return kind
}

// copyCodes are the provider codes in the words (RD, AD, TB), as "Cached on" shows them.
func copyCode(pv *policy.Provider, pk string) string {
	if pv != nil {
		if c := indexer.CacheCode(pv.Kind); c != "" {
			return strings.ToUpper(c)
		}
	}
	return pk
}

// QueueCopies creates (or returns, while unfinished) one copy per configured provider that does not hold the
// torrent complete, and queues their jobs. ErrCopyPaused while paused; ErrCopyNoHash without a v1 hash.
func (r *Router) QueueCopies(ctx context.Context, req CopyRequest) ([]CopyView, error) {
	hash := indexer.NormalizeHash(req.Hash)
	if hash == "" {
		return nil, ErrCopyNoHash
	}
	if r.copyPaused() {
		return nil, ErrCopyPaused
	}
	if r.q == nil {
		return nil, errors.New("router: no job queue")
	}
	held, err := r.heldComplete(ctx, hash)
	if err != nil {
		return nil, err
	}
	var keys []string
	for k := range r.adders {
		if r.pol.Providers[k] != nil && k != req.Except && !held[k] {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	now := r.now()
	var titleID any
	if req.TitleID != 0 {
		titleID = req.TitleID
	}
	for _, pk := range keys {
		var id int64
		err := r.st.WriteTx(ctx, func(tx *sql.Tx) error {
			var state string
			err := tx.QueryRowContext(ctx, `SELECT id, state FROM provider_copy WHERE info_hash = ? AND provider = ?`,
				hash, pk).Scan(&id, &state)
			switch {
			case err == nil && strings.Contains(copyUnfinished, "'"+state+"'"):
				id = 0
				return nil // unfinished: returned as it is (a double click, a season pack's other episodes)
			case err != nil && !errors.Is(err, sql.ErrNoRows):
				return err
			}
			at := store.Millis(now)
			return tx.QueryRowContext(ctx, `INSERT INTO provider_copy (info_hash, name, title_id, provider, state,
				created_at, updated_at) VALUES (?, ?, ?, ?, 'queued', ?, ?) ON CONFLICT (info_hash, provider) DO UPDATE
				SET name = excluded.name, title_id = COALESCE(excluded.title_id, provider_copy.title_id),
				torrent_id = NULL, own_add = 0, state = 'queued', message = '', created_at = excluded.created_at,
				updated_at = excluded.updated_at RETURNING id`, hash, req.Name, titleID, pk, at, at).Scan(&id)
		})
		if err != nil {
			return nil, fmt.Errorf("router: copy to %s: %w", pk, err)
		}
		if id == 0 {
			continue
		}
		// Queued after the row commits; a crash between the two is picked up by ResumeCopies at start.
		if _, _, err := r.q.Enqueue(ctx, JobCopy, copyPayload{ID: id}, copyJobOptions(id, now)); err != nil {
			return nil, err
		}
	}
	views, err := CopiesByHash(ctx, r.st.Writer(), []string{hash})
	if err != nil {
		return nil, err
	}
	return views[hash], nil
}

// copyJobOptions: the run time is in the dedupe key, so a new request for a row whose last job is still finishing is
// not dropped (an enqueue with the key of a running job is a no-op).
func copyJobOptions(id int64, at time.Time) jobs.EnqueueOptions {
	return jobs.EnqueueOptions{RunAt: at, DedupeKey: copyJobKey(id) + strconv.FormatInt(store.Millis(at), 10)}
}

func copyJobKey(id int64) string { return JobCopy + "/" + strconv.FormatInt(id, 10) + "/" }

// ResumeCopies queues a job for each unfinished copy with none queued or running (theatarr serve, at start): a copy
// interrupted by a restart, or whose job ran out of attempts, is finished from the account.
func (r *Router) ResumeCopies(ctx context.Context) (int, error) {
	if r.q == nil {
		return 0, nil
	}
	var ids []int64
	err := func() error {
		rows, err := r.st.Writer().QueryContext(ctx, `SELECT c.id FROM provider_copy c WHERE c.state IN `+
			copyUnfinished+` AND NOT EXISTS (SELECT 1 FROM job j WHERE j.kind = ? AND j.state IN ('queued', 'running')
			AND substr(j.dedupe_key, 1, length(? || c.id || '/')) = ? || c.id || '/') ORDER BY c.id`,
			JobCopy, JobCopy+"/", JobCopy+"/")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	}()
	if err != nil {
		return 0, fmt.Errorf("router: unfinished copies: %w", err)
	}
	now := r.now()
	for _, id := range ids {
		if _, _, err := r.q.Enqueue(ctx, JobCopy, copyPayload{ID: id}, copyJobOptions(id, now)); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

// CopiesByHash reads the copies of each hash, by provider key.
func CopiesByHash(ctx context.Context, db interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}, hashes []string) (map[string][]CopyView, error) {
	out := map[string][]CopyView{}
	var args []any
	for _, h := range hashes {
		if h = indexer.NormalizeHash(h); h != "" && !slices.Contains(args, any(h)) {
			args = append(args, h)
		}
	}
	if len(args) == 0 {
		return out, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT info_hash, provider, state, message, updated_at FROM provider_copy
		WHERE info_hash IN (`+store.Placeholders(len(args))+`) ORDER BY info_hash, provider`, args...)
	if err != nil {
		return nil, fmt.Errorf("router: copies: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		var v CopyView
		var at int64
		if err := rows.Scan(&h, &v.Provider, &v.State, &v.Message, &at); err != nil {
			return nil, err
		}
		if strings.HasPrefix(v.Message, copyDeletingPrefix) {
			v.Message = "" // the owed delete's journal is internal
		}
		v.UpdatedAt = store.FromMillis(at)
		out[h] = append(out[h], v)
	}
	return out, rows.Err()
}

// TorrentHash is the hash and name of torrent id on provider pk: from the registry, else a read of the provider.
func (r *Router) TorrentHash(ctx context.Context, pk, id string) (string, string, error) {
	var hash, name string
	err := r.st.Writer().QueryRowContext(ctx, `SELECT c.info_hash, COALESCE(NULLIF(cp.folder_name, ''), c.name)
		FROM content_provider cp JOIN content c ON c.id = cp.content_id WHERE cp.provider = ? AND cp.torrent_id = ?
		LIMIT 1`, pk, id).Scan(&hash, &name)
	if err == nil {
		return indexer.NormalizeHash(hash), name, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	rp := r.provs[pk]
	if rp == nil {
		return "", "", nil
	}
	t, err := rp.Torrent(ctx, id)
	if err != nil {
		return "", "", err
	}
	return indexer.NormalizeHash(t.Hash), t.Name, nil
}

// heldComplete lists the providers whose account holds hash complete (the registry, as "Cached on" shows it).
func (r *Router) heldComplete(ctx context.Context, hash string) (map[string]bool, error) {
	out := map[string]bool{}
	rows, err := r.st.Writer().QueryContext(ctx, `SELECT cp.provider FROM content_provider cp JOIN content c
		ON c.id = cp.content_id WHERE c.info_hash = ? AND cp.state = 'present' AND cp.progress >= 100`, hash)
	if err != nil {
		return nil, fmt.Errorf("router: registry: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var pk string
		if err := rows.Scan(&pk); err != nil {
			return nil, err
		}
		out[pk] = true
	}
	return out, rows.Err()
}

// handleCopyJob is the job handler of copies.
func (r *Router) handleCopyJob(ctx context.Context, j *jobs.Job) error {
	var p copyPayload
	if err := j.Decode(&p); err != nil {
		return jobs.Permanent(fmt.Errorf("router: bad payload: %w", err))
	}
	return r.runCopy(ctx, p.ID, j.Attempt >= j.MaxAttempts)
}

func (r *Router) loadCopy(ctx context.Context, id int64) (*copyRow, error) {
	c := &copyRow{id: id}
	var tid sql.NullString
	var title sql.NullInt64
	var at int64
	err := r.st.Writer().QueryRowContext(ctx, `SELECT info_hash, name, title_id, provider, torrent_id, own_add, state,
		message, updated_at FROM provider_copy WHERE id = ?`, id).Scan(&c.hash, &c.name, &title, &c.pk, &tid, &c.ownAdd,
		&c.state, &c.message, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("router: copy %d: %w", id, err)
	}
	c.tid, c.titleID, c.updatedAt = tid.String, title.Int64, store.FromMillis(at)
	return c, nil
}

// setCopy moves the row from the state it is in to state with msg (and set, a further assignment with args).
func (r *Router) setCopy(ctx context.Context, c *copyRow, state, msg string, set string, args ...any) error {
	q := `UPDATE provider_copy SET state = ?, message = ?, updated_at = ?`
	if set != "" {
		q += ", " + set
	}
	all := append([]any{state, msg, store.Millis(r.now())}, args...)
	all = append(all, c.id, c.state)
	_, err := r.st.Writer().ExecContext(context.WithoutCancel(ctx), q+` WHERE id = ? AND state = ?`, all...)
	if err != nil {
		return fmt.Errorf("router: copy %d: %w", c.id, err)
	}
	c.state, c.message = state, msg
	return nil
}

// runCopy works copy id from the state it is in. last: the job's last attempt; an error then ends the copy failed
// (queued) or leaves it for ResumeCopies (adding, checking: what is on the account is finished from it).
func (r *Router) runCopy(ctx context.Context, id int64, last bool) error {
	c, err := r.loadCopy(ctx, id)
	if err != nil || c == nil {
		return err
	}
	err = r.workCopy(ctx, c)
	if err != nil && last && c.state == CopyQueued && !isLater(err) && ctx.Err() == nil {
		if serr := r.setCopy(ctx, c, CopyFailed, err.Error(), ""); serr != nil {
			return serr
		}
		return nil
	}
	return err
}

// isLater says whether err is jobs.Later's hand-back (not a failure).
func isLater(err error) bool { return err != nil && strings.HasPrefix(err.Error(), "jobs: not now") }

// laterUntil hands the job back until at.
func (r *Router) laterUntil(at time.Time) error {
	d := at.Sub(r.now())
	if d < time.Second {
		d = time.Second
	}
	return jobs.Later(d)
}

func (r *Router) workCopy(ctx context.Context, c *copyRow) error {
	for {
		switch c.state {
		case CopyQueued:
			if err := r.startCopy(ctx, c); err != nil || c.state == CopyQueued {
				return err
			}
		case CopyAdding:
			if err := r.recoverAdding(ctx, c); err != nil || c.state == CopyAdding {
				return err
			}
		case CopyChecking:
			return r.settleCopy(ctx, c, nil)
		default:
			return nil
		}
	}
}

// startCopy is steps 1 to 3: stop early, read-only evidence, then the add.
func (r *Router) startCopy(ctx context.Context, c *copyRow) error {
	pv := r.pol.Providers[c.pk]
	code := copyCode(pv, c.pk)
	ad := r.adders[c.pk]
	if r.copyPaused() {
		return r.setCopy(ctx, c, CopySkipped, "Skipped: paused", "")
	}
	if pv == nil || ad == nil {
		return r.setCopy(ctx, c, CopySkipped, "Skipped: "+c.pk+" is not configured", "")
	}
	if held, err := r.heldComplete(ctx, c.hash); err != nil {
		return err
	} else if held[c.pk] {
		return r.setCopy(ctx, c, CopyAdded, "Already on "+code, "")
	}
	if near, err := r.nearTorrentLimit(ctx, pv, c.pk); err != nil {
		return err
	} else if near {
		return r.setCopy(ctx, c, CopySkipped, "Skipped: account near its torrent limit", "")
	}
	now := r.now()
	p, err := loadPace(ctx, r.st.Writer(), c.pk)
	if err != nil {
		return err
	}
	if p.BackoffUntil.After(now) || p.HoldUntil.After(now) {
		until := p.BackoffUntil
		if p.HoldUntil.After(until) {
			until = p.HoldUntil
		}
		return r.setCopy(ctx, c, CopySkipped, "Skipped: "+providerWords(pv.Kind)+" is holding adds until "+
			until.Local().Format("15:04 MST"), "")
	}
	m, err := adder.BareMagnet(c.hash, c.name, r.set.Trackers)
	if err != nil {
		return r.setCopy(ctx, c, CopyFailed, err.Error(), "")
	}
	if pv.Kind == provider.KindRealDebrid && indexer.RealDebridRefuses(c.name, true) {
		return r.setCopy(ctx, c, CopyRefused, "Real-Debrid refuses this name", "")
	}
	switch cached, err := r.checkHash(ctx, pv, c.pk, c.hash); {
	case err != nil:
		return err
	case cached == hashUncached:
		return r.setCopy(ctx, c, CopyNotCached, "Not cached on "+code, "")
	}
	return r.addCopy(ctx, c, pv, ad, m)
}

// nearTorrentLimit says whether the account holds TorrentLimitWarnPercent of its limit, by its last complete sync
// (as the health check counts it).
func (r *Router) nearTorrentLimit(ctx context.Context, pv *policy.Provider, pk string) (bool, error) {
	limit := pv.TorrentLimit()
	if limit <= 0 {
		return false, nil
	}
	var summary string
	err := r.st.Writer().QueryRowContext(ctx, `SELECT summary FROM provider_sync_run WHERE provider = ? AND ok = 1
		ORDER BY finished_at DESC, id DESC LIMIT 1`, pk).Scan(&summary)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("router: provider sync: %w", err)
	}
	var sum struct {
		Torrents *int `json:"torrents"`
	}
	if json.Unmarshal([]byte(summary), &sum) != nil || sum.Torrents == nil {
		return false, nil
	}
	return policy.NearTorrentLimit(*sum.Torrents, limit), nil
}

// hashEvidence is what CheckHash knows.
type hashEvidence int

const (
	hashUnknown hashEvidence = iota
	hashCached
	hashUncached
)

// checkHash is the read-only evidence on hash at pk: a live provider_cache row; else DMM's availability for
// Real-Debrid and AllDebrid (an unlisted hash is unknown, never uncached); else TorBox's own cache check. A fresh
// answer is stored in provider_cache as a search stores it. A failed read is unknown.
func (r *Router) checkHash(ctx context.Context, pv *policy.Provider, pk, hash string) (hashEvidence, error) {
	now := r.now()
	switch state, err := r.cacheState(ctx, hash, pk, now); {
	case err != nil:
		return hashUnknown, err
	case state == "cached":
		return hashCached, nil
	case state == "uncached":
		return hashUncached, nil
	}
	switch pv.Kind {
	case provider.KindRealDebrid, provider.KindAllDebrid:
		if r.availHash == nil {
			return hashUnknown, nil
		}
		listed, _, err := r.availHash(ctx, indexer.CacheCode(pv.Kind), hash)
		if err != nil {
			r.log.Info("copy: DMM availability not read; the add tells", "provider", pk, "hash", hash, "err", err)
		}
		if listed {
			return hashCached, nil
		}
	case provider.KindTorBox:
		rp := r.provs[pk]
		if rp == nil || !rp.Cache().Supported {
			return hashUnknown, nil
		}
		got, err := rp.CheckCached(ctx, provider.CacheQuery{Hashes: []string{hash}})
		if err != nil {
			r.log.Info("copy: TorBox cache check failed; the add tells", "provider", pk, "hash", hash, "err", err)
			return hashUnknown, nil
		}
		if got[hash] {
			return hashCached, r.setCacheSource(ctx, hash, pk, "cached", "check:"+pk,
				now.Add(r.pol.CacheEvidenceSettings().TTL), now)
		}
		return hashUncached, r.setCache(ctx, hash, pk, "uncached", "check",
			now.Add(min(indexer.CheckUncachedTTL, r.set.MissTTL)), now)
	}
	return hashUnknown, nil
}

// addCopy is step 3, under the add lock: the account's listing before the add (AllDebrid, TorBox), the pacing
// reservation, adding with own_add, the add, then checking with the torrent id. The settle follows without the lock.
func (r *Router) addCopy(ctx context.Context, c *copyRow, pv *policy.Provider, ad adder.Adder, m adder.Magnet) error {
	code := copyCode(pv, c.pk)
	r.mu.Lock()
	locked := true
	unlock := func() {
		if locked {
			locked = false
			r.mu.Unlock()
		}
	}
	defer unlock()
	g, err := r.guardBefore(ctx, pv, c.pk, c.hash, 0, true)
	if err != nil {
		return err
	}
	switch {
	case !g.listed:
		// A failed listing is not absence: nothing is sent; the job tries again.
		return fmt.Errorf("router: copy to %s waits: %s", c.pk, g.listErr)
	case g.reuse != "":
		// On the account already: added, and "Cached on" learns it.
		if err := r.writeCachedOn(ctx, c.hash, c.name, c.pk, g.reuse); err != nil {
			return err
		}
		return r.setCopy(ctx, c, CopyAdded, "Already on "+code, "torrent_id = ?", g.reuse)
	case pv.Kind == provider.KindRealDebrid && g.before != "":
		// The registry lists it on Real-Debrid (not complete, or "Cached on" would show it): not added again.
		return r.setCopy(ctx, c, CopyAdded, "Already on "+code+" ("+g.before+")", "")
	}
	pc := pv.EffectivePacing()
	now := r.now()
	var before, after pace
	var until time.Time
	if err := updatePace(ctx, r.st, c.pk, now, func(_ *sql.Tx, q *pace) (bool, error) {
		if until, _ = q.blocked(now, pc); !until.IsZero() {
			return false, nil
		}
		before = *q
		q.added(now, pc)
		after = *q
		return true, nil
	}); err != nil {
		return err
	}
	if !until.IsZero() {
		return r.laterUntil(until) // paced: the same row runs again then
	}
	if err := jobs.Fence(ctx); err != nil {
		return err
	}
	// The listing showed no entry of the hash: an entry the add makes is ours.
	if err := r.setCopy(ctx, c, CopyAdding, "", "own_add = 1, torrent_id = NULL"); err != nil {
		return err
	}
	c.ownAdd = true
	added, aerr := ad.AddMagnet(ctx, m, adder.AddOptions{OnlyIfCached: pv.Kind == provider.KindTorBox})
	rctx := context.WithoutCancel(ctx)
	now = r.now()
	if aerr != nil && ctx.Err() != nil {
		return fmt.Errorf("router: copy to %s interrupted: %w", c.pk, context.Cause(ctx))
	}
	unsent := notSent(aerr)
	is451 := false
	if ae := (*adder.Error)(nil); errors.As(aerr, &ae) && ae.Code == adder.CodeAccountHold {
		is451 = true
	}
	var v verdict
	var retryAt time.Time
	var st Step
	held := false
	var explained []string
	if err := updatePace(rctx, r.st, c.pk, now, func(tx *sql.Tx, q *pace) (bool, error) {
		if unsent {
			q.unadd(before, after)
		}
		held, explained = false, nil
		if is451 {
			var err error
			if held, explained, err = note451(rctx, tx, c.pk, c.hash, c.titleID, now, r.set.MissTTL,
				q.HoldUntil.After(now)); err != nil {
				return false, err
			}
		}
		v, retryAt, st = r.classify(q, pv, now, c.hash, aerr, held)
		return true, nil
	}); err != nil {
		return err
	}
	_ = v
	switch {
	case aerr == nil:
	case unsent:
		// The gateway never sent it: queued again, run when it lets requests through.
		if err := r.setCopy(rctx, c, CopyQueued, "", "own_add = 0"); err != nil {
			return err
		}
		if retryAt.IsZero() {
			retryAt = now.Add(BackoffBase)
		}
		return r.laterUntil(retryAt)
	case addUncertain(aerr):
		// The answer does not say whether the provider took it: the row stays adding without a torrent id and is
		// finished from the account after addSettle (recoverAdding).
		r.log.Warn("copy: add outcome unknown; settled from the account", "provider", c.pk, "hash", c.hash,
			"err", aerr.Error())
		return r.laterUntil(now.Add(addSettle))
	case is451:
		if held {
			if err := r.liftMisses(rctx, c.pk, explained); err != nil {
				return err
			}
			return r.setCopy(rctx, c, CopyFailed, providerWords(pv.Kind)+" is holding adds until "+
				st.RetryAt.Local().Format("15:04 MST"), "own_add = 0")
		}
		if err := r.setCache(rctx, c.hash, c.pk, "miss", "451", now.Add(r.set.MissTTL), now); err != nil {
			return err
		}
		return r.setCopy(rctx, c, CopyFailed, "Real-Debrid refused it (451)", "own_add = 0")
	case st.Code == string(adder.CodeNotCached):
		// TorBox refused the cached-only add: nothing was created there.
		if err := r.setCache(rctx, c.hash, c.pk, "uncached", "add", now.Add(r.set.MissTTL), now); err != nil {
			return err
		}
		return r.setCopy(rctx, c, CopyNotCached, "Not cached on "+code, "own_add = 0")
	default:
		return r.setCopy(rctx, c, CopyFailed, aerr.Error(), "own_add = 0")
	}
	if err := r.setCopy(rctx, c, CopyChecking, "", "torrent_id = ?", added.ID); err != nil {
		return err
	}
	c.tid = added.ID
	r.log.Info("copy: added", "provider", c.pk, "hash", c.hash, "torrent", added.ID, "ready", added.Ready)
	unlock()
	return r.settleCopy(ctx, c, added)
}

// recoverAdding finishes an adding row (no torrent id) from the account: after addSettle, a torrent of the hash
// found there is taken up with own_add false (it may be an older one), else the copy is queued again.
func (r *Router) recoverAdding(ctx context.Context, c *copyRow) error {
	if wait := c.updatedAt.Add(addSettle); wait.After(r.now()) {
		return r.laterUntil(wait)
	}
	rp := r.provs[c.pk]
	if rp == nil {
		return r.setCopy(ctx, c, CopyFailed, "no read-only client for "+c.pk, "own_add = 0")
	}
	list, err := rp.Torrents(ctx)
	if err != nil {
		return fmt.Errorf("router: copy to %s: reading the account: %w", c.pk, err)
	}
	for _, t := range list {
		if strings.EqualFold(t.Hash, c.hash) && t.ID != "" {
			c.ownAdd, c.tid = false, t.ID
			return r.setCopy(ctx, c, CopyChecking, "", "torrent_id = ?, own_add = 0", t.ID)
		}
	}
	return r.setCopy(ctx, c, CopyQueued, "", "own_add = 0")
}

// settleCopy is step 4: read the add back (Real-Debrid: select every playable file, then the grace). Complete:
// "Cached on" learns it, added. Not complete: our add is deleted (own_add only) and the copy ends not_cached. a is
// the add of this run (nil after a restart).
func (r *Router) settleCopy(ctx context.Context, c *copyRow, a *adder.Added) error {
	pv := r.pol.Providers[c.pk]
	ad := r.adders[c.pk]
	if pv == nil || ad == nil {
		return r.setCopy(ctx, c, CopyFailed, c.pk+" is no longer configured", "")
	}
	code := copyCode(pv, c.pk)
	if r.copyPaused() {
		// Pause postpones the settle and any cleanup; the copy stays unfinished.
		return jobs.Later(5 * time.Minute)
	}
	if strings.HasPrefix(c.message, copyDeletingPrefix) {
		return r.deleteCopy(ctx, c, pv, ad, nil, "")
	}
	if a == nil {
		a = &adder.Added{ID: c.tid, Hash: c.hash}
	}
	cv, t, why := r.readBack(ctx, pv, c.pk, a, func(t *provider.Torrent) (string, error) {
		ids := PlayableIDs(t.Files)
		if len(ids) == 0 {
			return "no playable file to select", nil
		}
		return "", ad.SelectFiles(ctx, t.ID, ids)
	})
	now := r.now()
	switch cv {
	case cvCached:
		if err := r.setCacheSource(ctx, c.hash, c.pk, "cached", "add:"+c.pk,
			now.Add(r.pol.CacheEvidenceSettings().TTL), now); err != nil {
			return err
		}
		if err := r.writeCachedOn(ctx, c.hash, c.name, c.pk, c.tid); err != nil {
			return err
		}
		return r.setCopy(ctx, c, CopyAdded, "Added to "+code, "")
	case cvUncached:
		return r.deleteCopy(ctx, c, pv, ad, t, why)
	}
	if t != nil && t.State == provider.StateError {
		// Dead on the provider (an error status): it will not complete.
		return r.deleteCopy(ctx, c, pv, ad, t, why)
	}
	// No verdict: a torrent gone from the account ends the copy; anything else is read again later.
	if rp := r.provs[c.pk]; rp != nil {
		if _, err := rp.Torrent(ctx, c.tid); errors.Is(err, provider.ErrNotFound) {
			return r.setCopy(ctx, c, CopyFailed, "the torrent left "+code+" before it was complete", "")
		}
	}
	return fmt.Errorf("router: copy to %s not settled: %s", c.pk, why)
}

// deleteCopy deletes our add that is not complete, when the listing before it proved the entry ours (own_add) and
// it still holds the hash and no download or registry content of another hash knows its id. The delete is journaled
// on the row first; a lost answer is settled by reading the torrent back.
func (r *Router) deleteCopy(ctx context.Context, c *copyRow, pv *policy.Provider, ad adder.Adder, t *provider.Torrent,
	why string) error {
	code := copyCode(pv, c.pk)
	now := r.now()
	owed := strings.HasPrefix(c.message, copyDeletingPrefix)
	if !owed {
		if !c.ownAdd {
			return r.setCopy(ctx, c, CopyNotCached, "Not cached on "+code+"; left on the account: it was there "+
				"before our add", "")
		}
		if t != nil && t.Hash != "" && !strings.EqualFold(t.Hash, c.hash) {
			return r.setCopy(ctx, c, CopyNotCached, "Not cached on "+code+"; left on the account: the entry "+
				"holds another hash", "")
		}
		var known string
		err := r.st.Writer().QueryRowContext(ctx, `SELECT 'download ' || id FROM download WHERE provider = ?
			AND provider_torrent_id = ? UNION ALL SELECT 'registry content ' || c.info_hash FROM content_provider cp
			JOIN content c ON c.id = cp.content_id WHERE cp.provider = ? AND cp.torrent_id = ? AND c.info_hash != ?
			LIMIT 1`, c.pk, c.tid, c.pk, c.tid, c.hash).Scan(&known)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if known != "" {
			return r.setCopy(ctx, c, CopyNotCached, "Not cached on "+code+"; left on the account: torrent "+c.tid+
				" is known to "+known, "")
		}
		if err := jobs.Fence(ctx); err != nil {
			return err
		}
		if err := r.setCopy(ctx, c, CopyChecking, copyDeletingPrefix+c.tid+" on "+c.pk, ""); err != nil {
			return err
		}
	}
	del, ok := ad.(adder.StalledDeleter)
	if !ok {
		return r.setCopy(ctx, c, CopyNotCached, "Not cached on "+code+"; the adder cannot delete it", "")
	}
	gone := false
	if owed {
		// A delete was sent before: a torrent now gone is our delete's work.
		if rp := r.provs[c.pk]; rp != nil {
			if _, err := rp.Torrent(ctx, c.tid); errors.Is(err, provider.ErrNotFound) {
				gone = true
			}
		}
	}
	if !gone {
		wctx, cancel := context.WithTimeout(ctx, providerWriteLimit)
		err := del.DeleteOwnStalledAdd(wctx, c.tid, adder.OwnAdd{NotOnAccountBefore: true, NewEntry: true})
		cancel()
		if err != nil {
			if g, _ := r.readAfterDelete(ctx, c.pk, c.tid); !g {
				// The row stays checking with the delete owed; the job tries again.
				return fmt.Errorf("router: copy to %s: deleting our add %s: %w", c.pk, c.tid, err)
			}
		}
	}
	rctx := context.WithoutCancel(ctx)
	if err := r.setCache(rctx, c.hash, c.pk, "uncached", "add", now.Add(r.set.MissTTL), now); err != nil {
		return err
	}
	r.uncachedAdd(rctx, c.pk)
	r.log.Info("copy: not cached; our add was deleted", "provider", c.pk, "hash", c.hash, "torrent", c.tid,
		"why", why)
	return r.setCopy(rctx, c, CopyNotCached, "Not cached on "+code+"; our add was deleted", "")
}

// writeCachedOn records that pk holds hash complete under torrent tid, as an import does (library apply.go): the
// registry row present at 100, and the content present.
func (r *Router) writeCachedOn(ctx context.Context, hash, name, pk, tid string) error {
	now := store.Millis(r.now())
	return r.st.WriteTx(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO content (info_hash, name, state, first_seen_at, last_seen_at)
			VALUES (?, ?, 'present', ?, ?) ON CONFLICT (info_hash) DO NOTHING`, hash, name, now, now); err != nil {
			return err
		}
		var contentID int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM content WHERE info_hash = ?`, hash).Scan(&contentID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO content_provider (content_id, provider, torrent_id, seen_at, state,
			progress) VALUES (?, ?, NULLIF(?, ''), ?, 'present', 100) ON CONFLICT (content_id, provider) DO UPDATE
			SET state = 'present', progress = 100, absent_at = NULL, seen_at = excluded.seen_at,
			torrent_id = COALESCE(excluded.torrent_id, content_provider.torrent_id)
			WHERE content_provider.state <> 'present' OR content_provider.progress IS NULL
				OR content_provider.progress < 100`, contentID, pk, tid, now)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			_, err = tx.ExecContext(ctx, `UPDATE content SET state = 'present' WHERE id = ? AND state <> 'present'`,
				contentID)
		}
		return err
	})
}
