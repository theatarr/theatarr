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
// A copy goes queued -> adding (written before the add is sent) -> checking (torrent id stored right after the add) ->
// added, not_cached, refused, skipped or failed. An add that is not cached is deleted on every provider (the owner's
// decision; AllDebrid and TorBox through adder.StalledDeleter, which survives a restart). The rules
// (docs/design/router.md "Copies to other providers", copy_rules_test.go):
//
//   - A (ownership): own_add only when a successful listing read just before the add showed neither the hash nor the
//     id the add returned, and no registry row shows the hash on that provider (addCopy). A failed listing sends
//     nothing.
//   - B (before every delete): read again; gone ends not_cached, complete ends added, another hash or a use on this
//     provider leaves it in place (deleteCopy, copyInUse). An owed delete gets the same checks.
//   - C (Pause): the gate is read before every provider write; past queued, Pause only postpones.
//   - D (never stranded): what cannot be done now waits with jobs.Later, 1 to 30 minutes (runCopy).
//   - E: a route defers while a copy of the hash there is unsettled (copyUnsettled); a copy waits for a route's
//     unsettled add (routeAddUnsettled).
//   - F: an entry already there is added; Cached on only when it is complete (alreadyThere).
//   - G: the account's limit is checked against the fresh listing too. H: the uncached verdict counts once.
//   - I: an ambiguous read is settled by a fresh listing (copyRead). J1: Real-Debrid's rule on every known name
//     (copyRefusedName).
//
// Recovery: at start (ResumeCopies) and on each job run the row's own state says what is left: an adding row without
// a torrent id waits addSettle, then finds its torrent on the account by hash (own_add false: it may be an older one)
// or goes back to queued; a checking row is settled from the account.

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

// ErrCopyPaused: Pause is on (or the router is off); nothing is added to another provider. CopyPausedWords say it.
var ErrCopyPaused = errors.New("router: paused, nothing is added to another provider")

// ErrCopyNoHash: the file has no v1 torrent hash on record. CopyNoHashWords say it.
var ErrCopyNoHash = errors.New("router: no torrent hash on record for this file")

// The words the API answers for ErrCopyPaused and ErrCopyNoHash.
const (
	CopyPausedWords = "Paused: nothing is searched, downloaded or linked for any library."
	CopyNoHashWords = "Cannot add to other providers: Theatarr has no torrent hash on record for this file."
)

// CopyView is one copy as the title page and the Torrents page show it.
type CopyView struct {
	Provider string `json:"provider"`
	// Code is the provider's cache code (rd, ad, tb) as Cached on shows it; the web API fills it from the policy.
	Code      string    `json:"code,omitempty"`
	State     string    `json:"state"`
	Message   string    `json:"message,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	// Until is when a hold that skipped or failed the copy ends (until_at), for the browser to show in its own zone.
	Until *time.Time `json:"until,omitempty"`
}

// CopyRequest names the torrent to add to the other providers.
type CopyRequest struct {
	Hash    string
	TitleID int64
	// Names are the torrent's known names, the one shown first (the file's release, a provider entry's display and
	// original names): Real-Debrid's name rule is applied to every one (rule J1).
	Names []string
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
	name := ""
	if len(req.Names) > 0 {
		name = req.Names[0]
	}
	// Real-Debrid's name rule, once per request (rule J1).
	refused := ""
	if slices.ContainsFunc(keys, func(k string) bool { return r.pol.Providers[k].Kind == provider.KindRealDebrid }) {
		if refused, err = r.copyRefusedName(ctx, hash, req.Names...); err != nil {
			return nil, err
		}
	}
	for _, pk := range keys {
		var id int64
		unfinished := false
		state, msg := CopyQueued, ""
		if refused != "" && r.pol.Providers[pk].Kind == provider.KindRealDebrid {
			// Never sent (rule J1): one of its names is refused there.
			state, msg = CopyRefused, "Real-Debrid refuses this name ("+refused+")"
		}
		err := r.st.WriteTx(ctx, func(tx *sql.Tx) error {
			var cur string
			err := tx.QueryRowContext(ctx, `SELECT id, state FROM provider_copy WHERE info_hash = ? AND provider = ?`,
				hash, pk).Scan(&id, &cur)
			switch {
			case err == nil && cur == CopyQueued && state == CopyRefused:
				// Not sent yet, and this request brings a refused name: refused.
			case err == nil && copyOpen(cur):
				unfinished = true
				return nil // unfinished: returned as it is (a double click, a season pack's other episodes)
			case err != nil && !errors.Is(err, sql.ErrNoRows):
				return err
			}
			at := store.Millis(now)
			return tx.QueryRowContext(ctx, `INSERT INTO provider_copy (info_hash, name, title_id, provider, state,
				message, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (info_hash, provider) DO UPDATE
				SET name = excluded.name, title_id = COALESCE(excluded.title_id, provider_copy.title_id),
				torrent_id = NULL, own_add = 0, state = excluded.state, message = excluded.message, until_at = NULL,
				created_at = excluded.created_at, updated_at = excluded.updated_at RETURNING id`, hash, name, titleID,
				pk, state, msg, at, at).Scan(&id)
		})
		if err != nil {
			return nil, fmt.Errorf("router: copy to %s: %w", pk, err)
		}
		if state != CopyQueued && !unfinished {
			continue
		}
		if unfinished {
			// An unfinished row keeps its job; one whose job is gone gets a new one (rule D).
			if has, err := r.copyHasJob(ctx, id); err != nil || has {
				if err != nil {
					return nil, err
				}
				continue
			}
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

// QueueCopiesForDownload is the automatic form (router.copy_to_providers), after download dl was imported: the same
// work for its torrent, when the setting is on and the download came from a provider. No provider is asked here.
func (r *Router) QueueCopiesForDownload(ctx context.Context, dl int64) error {
	if !r.pol.RouterSettings().CopyToProviders || r.copyPaused() {
		return nil
	}
	var req CopyRequest
	var name string
	err := r.st.Writer().QueryRowContext(ctx, `SELECT info_hash, release_title, title_id, COALESCE(provider, '')
		FROM download WHERE id = ? AND client IS NULL`, dl).Scan(&req.Hash, &name, &req.TitleID, &req.Except)
	req.Names = []string{name}
	if errors.Is(err, sql.ErrNoRows) || err == nil && req.Except == "" {
		return nil
	}
	if err != nil {
		return fmt.Errorf("router: download %d: %w", dl, err)
	}
	_, err = r.QueueCopies(ctx, req)
	if errors.Is(err, ErrCopyNoHash) || errors.Is(err, ErrCopyPaused) {
		return nil
	}
	return err
}

// copyJobOptions: the run time is in the dedupe key, so a new request for a row whose last job is still finishing is
// not dropped (an enqueue with the key of a running job is a no-op).
func copyJobOptions(id int64, at time.Time) jobs.EnqueueOptions {
	return jobs.EnqueueOptions{RunAt: at, DedupeKey: copyJobKey(id) + strconv.FormatInt(store.Millis(at), 10)}
}

func copyJobKey(id int64) string { return JobCopy + "/" + strconv.FormatInt(id, 10) + "/" }

// copyHasJob says whether copy id has a job queued or running.
func (r *Router) copyHasJob(ctx context.Context, id int64) (bool, error) {
	key := copyJobKey(id)
	var n int
	err := r.st.Writer().QueryRowContext(ctx, `SELECT COUNT(*) FROM job WHERE kind = ? AND state IN ('queued', 'running')
		AND substr(dedupe_key, 1, ?) = ?`, JobCopy, len(key), key).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("router: copy jobs: %w", err)
	}
	return n > 0, nil
}

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
	rows, err := db.QueryContext(ctx, `SELECT info_hash, provider, state, message, updated_at, until_at FROM provider_copy
		WHERE info_hash IN (`+store.Placeholders(len(args))+`) ORDER BY info_hash, provider`, args...)
	if err != nil {
		return nil, fmt.Errorf("router: copies: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		var v CopyView
		var at int64
		var until sql.NullInt64
		if err := rows.Scan(&h, &v.Provider, &v.State, &v.Message, &at, &until); err != nil {
			return nil, err
		}
		if until.Valid {
			t := store.FromMillis(until.Int64)
			v.Until = &t
		}
		if strings.HasPrefix(v.Message, copyDeletingPrefix) {
			v.Message = "" // the owed delete's journal is internal
		}
		v.UpdatedAt = store.FromMillis(at)
		out[h] = append(out[h], v)
	}
	return out, rows.Err()
}

// TorrentHash is the hash and the known names of torrent id on provider pk: the registry's, else (no hash there) a
// read of the provider (its display and original names).
func (r *Router) TorrentHash(ctx context.Context, pk, id string) (string, []string, error) {
	var hash, name string
	err := r.st.Writer().QueryRowContext(ctx, `SELECT c.info_hash, COALESCE(NULLIF(cp.folder_name, ''), c.name)
		FROM content_provider cp JOIN content c ON c.id = cp.content_id WHERE cp.provider = ? AND cp.torrent_id = ?
		LIMIT 1`, pk, id).Scan(&hash, &name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", nil, err
	}
	if h := indexer.NormalizeHash(hash); h != "" {
		return h, []string{name}, nil
	}
	rp := r.provs[pk]
	if rp == nil {
		return "", nil, nil
	}
	t, err := rp.Torrent(ctx, id)
	if err != nil {
		return "", nil, err
	}
	return indexer.NormalizeHash(t.Hash), slices.DeleteFunc([]string{t.Name, t.OriginalName},
		func(s string) bool { return s == "" }), nil
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
	return r.runCopy(ctx, p.ID)
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

// errCopyLost: the row is no longer in the state this worker read (a new request changed it): the worker stops and
// sends nothing more.
var errCopyLost = errors.New("router: the copy changed under this worker")

// setCopy moves the row from the state it is in to state with msg (and set, a further assignment with args).
// errCopyLost when the row is no longer in that state.
func (r *Router) setCopy(ctx context.Context, c *copyRow, state, msg string, set string, args ...any) error {
	q := `UPDATE provider_copy SET state = ?, message = ?, updated_at = ?`
	if set != "" {
		q += ", " + set
	}
	all := append([]any{state, msg, store.Millis(r.now())}, args...)
	all = append(all, c.id, c.state)
	res, err := r.st.Writer().ExecContext(context.WithoutCancel(ctx), q+` WHERE id = ? AND state = ?`, all...)
	if err != nil {
		return fmt.Errorf("router: copy %d: %w", c.id, err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return errCopyLost
	}
	c.state, c.message, c.updatedAt = state, msg, r.now()
	return nil
}

// Waits of an unfinished copy (rule D): at least copyWaitMin, then as long as the row has waited since its last
// change, at most copyWaitMax.
const (
	copyWaitMin = time.Minute
	copyWaitMax = 30 * time.Minute
)

// runCopy works copy id from the state it is in. Nothing it cannot do now ends the job: an error hands it back with
// a wait that grows with the time since the row last changed (copyWaitMin to copyWaitMax), so an unfinished copy
// never uses up the job's attempts and is never stranded.
func (r *Router) runCopy(ctx context.Context, id int64) error {
	c, err := r.loadCopy(ctx, id)
	if err != nil || c == nil {
		return err
	}
	err = r.workCopy(ctx, c)
	if errors.Is(err, errCopyLost) {
		return nil // another request owns the row now
	}
	if err == nil || isLater(err) || ctx.Err() != nil || !copyOpen(c.state) {
		return err
	}
	wait := min(max(r.now().Sub(c.updatedAt), copyWaitMin), copyWaitMax)
	r.log.Info("copy: waits", "provider", c.pk, "hash", c.hash, "state", c.state, "wait", wait, "err", err)
	return jobs.Later(wait)
}

// copyOpen says whether a copy in state is still being worked.
func copyOpen(state string) bool {
	return state == CopyQueued || state == CopyAdding || state == CopyChecking
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

// copyRefusedName returns the first known name of hash Real-Debrid's name rule refuses (rule J1): the given names
// (the file's, a provider entry's), the release and original names of its downloads, the release names of the links
// and arr files tied to it (by content, download or an arr file's download id) and the registry's names. Every read
// is by hash.
func (r *Router) copyRefusedName(ctx context.Context, hash string, names ...string) (string, error) {
	err := func() error {
		rows, err := r.st.Writer().QueryContext(ctx, `SELECT release_title FROM download WHERE info_hash = ?1
			UNION SELECT COALESCE(original_name, '') FROM download WHERE info_hash = ?1
			UNION SELECT COALESCE(l.release_title, '') FROM download d JOIN link l ON l.download_id = d.id
				WHERE d.info_hash = ?1
			UNION SELECT name FROM content WHERE info_hash = ?1
			UNION SELECT COALESCE(cp.folder_name, '') FROM content c JOIN content_provider cp ON cp.content_id = c.id
				WHERE c.info_hash = ?1
			UNION SELECT COALESCE(l.release_title, '') FROM content c JOIN content_file cf ON cf.content_id = c.id
				JOIN link l ON l.content_file_id = cf.id WHERE c.info_hash = ?1
			UNION SELECT COALESCE(vf.release_title, '') || char(10) || COALESCE(vf.scene_name, '') FROM content c
				JOIN version_file vf ON vf.content_id = c.id WHERE c.info_hash = ?1
			UNION SELECT COALESCE(release_title, '') || char(10) || COALESCE(scene_name, '') FROM version_file
				WHERE download_id IN (?1, upper(?1))`, hash)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				return err
			}
			names = append(names, strings.Split(n, "\n")...)
		}
		return rows.Err()
	}()
	if err != nil {
		return "", fmt.Errorf("router: names of %s: %w", hash, err)
	}
	for _, n := range names {
		if n != "" && indexer.RealDebridRefuses(n, true) {
			return n, nil
		}
	}
	return "", nil
}

// startCopy is steps 1 and 2 (stop early, read-only evidence), then the add.
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
		return r.setCopy(ctx, c, CopySkipped, copyNearLimit, "")
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
		return r.setCopy(ctx, c, CopySkipped, "Skipped: "+providerWords(pv.Kind)+" is holding adds", "until_at = ?",
			store.Millis(until))
	}
	m, err := adder.BareMagnet(c.hash, c.name, r.set.Trackers)
	if err != nil {
		return r.setCopy(ctx, c, CopyFailed, err.Error(), "")
	}
	switch cached, err := r.checkHash(ctx, pv, c.pk, c.hash); {
	case err != nil:
		return err
	case cached == hashUncached:
		return r.setCopy(ctx, c, CopyNotCached, "Not cached on "+code, "")
	}
	return r.addCopy(ctx, c, pv, ad, m)
}

// copyNearLimit is the skip of an account near its torrent limit.
const copyNearLimit = "Skipped: account near its torrent limit"

// routeAddUnsettled says whether a download of ours is adding hash to pk (or to a provider not recorded yet).
func (r *Router) routeAddUnsettled(ctx context.Context, pk, hash string) (bool, error) {
	var n int
	err := r.st.Writer().QueryRowContext(ctx, `SELECT COUNT(*) FROM download WHERE info_hash = ? AND state = 'adding'
		AND client IS NULL AND (provider = ? OR COALESCE(provider, '') = '')`, hash, pk).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("router: unsettled adds: %w", err)
	}
	return n > 0, nil
}

// copyUnsettled says why a route must not add hash to pk now: a copy of it there is adding or settling (rule E).
func (r *Router) copyUnsettled(ctx context.Context, pk, hash string) (string, error) {
	var why string
	err := r.st.Writer().QueryRowContext(ctx, `SELECT 'a copy to other providers is ' || state || ' on ' || provider
		FROM provider_copy WHERE info_hash = ? AND provider = ? AND state IN ('adding', 'checking')`, hash, pk).Scan(&why)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("router: copies: %w", err)
	}
	return why, nil
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

// alreadyThere ends a copy whose hash the account's listing shows (rule F): added, with Cached on written only when
// the entry is complete; one still downloading says so with its progress.
func (r *Router) alreadyThere(ctx context.Context, c *copyRow, code string, t provider.Torrent) error {
	if t.State == provider.StateDownloaded {
		if err := r.writeCachedOn(ctx, c.hash, c.name, c.pk, t.ID); err != nil {
			return err
		}
		return r.setCopy(ctx, c, CopyAdded, "Already on "+code, "torrent_id = ?, own_add = 0", t.ID)
	}
	return r.setCopy(ctx, c, CopyAdded, fmt.Sprintf("Already on %s, still downloading (%.0f%%)", code, t.Progress),
		"torrent_id = ?, own_add = 0", t.ID)
}

// addCopy is step 3, under the add lock: a fresh listing of the account (every provider; a failed one sends nothing),
// the limit by that listing, the pacing reservation, adding with the ownership known so far, the gate, the add, then
// checking with the torrent id and the final ownership (rule A). The settle follows without the lock.
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
	// A route's add of the hash there is not settled: wait for it, under the add lock (rule E).
	if busy, err := r.routeAddUnsettled(ctx, c.pk, c.hash); err != nil {
		return err
	} else if busy {
		return r.laterUntil(r.now().Add(reconcileAfter))
	}
	rp := r.provs[c.pk]
	if rp == nil {
		return fmt.Errorf("router: copy to %s waits: no read-only client", c.pk)
	}
	list, err := rp.Torrents(ctx)
	if err != nil {
		// A failed listing is not absence: nothing is sent; the row waits.
		return fmt.Errorf("router: copy to %s waits: reading the account failed: %w", c.pk, err)
	}
	ids := make(map[string]bool, len(list))
	for _, t := range list {
		ids[t.ID] = true
		if strings.EqualFold(t.Hash, c.hash) && t.ID != "" {
			return r.alreadyThere(ctx, c, code, t)
		}
	}
	if limit := pv.TorrentLimit(); policy.NearTorrentLimit(len(list), limit) {
		return r.setCopy(ctx, c, CopySkipped, copyNearLimit, "")
	}
	// The registry's entries of the hash on this provider: an id among them is not ours, and one shown present makes
	// nothing the add makes provably ours (an absent one is an old torrent since removed).
	regIDs := map[string]bool{}
	inRegistry := false
	if err := func() error {
		rows, err := r.st.Writer().QueryContext(ctx, `SELECT COALESCE(cp.torrent_id, ''), cp.state = 'present'
			FROM content_provider cp JOIN content c ON c.id = cp.content_id WHERE c.info_hash = ? AND cp.provider = ?`,
			c.hash, c.pk)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var present bool
			if err := rows.Scan(&id, &present); err != nil {
				return err
			}
			inRegistry = inRegistry || present
			regIDs[id] = true
		}
		return rows.Err()
	}(); err != nil {
		return fmt.Errorf("router: registry: %w", err)
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
	unreserve := func() {
		_ = updatePace(context.WithoutCancel(ctx), r.st, c.pk, r.now(), func(_ *sql.Tx, q *pace) (bool, error) {
			q.unadd(before, after)
			return true, nil
		})
	}
	// The gate again, right before the write (rule C): a Pause during the reads above stops the add.
	if r.copyPaused() {
		unreserve()
		return r.setCopy(ctx, c, CopySkipped, "Skipped: paused", "")
	}
	if err := jobs.Fence(ctx); err != nil {
		unreserve()
		return err
	}
	own := !inRegistry
	if err := r.setCopy(ctx, c, CopyAdding, "", "own_add = ?, torrent_id = NULL", b2i(own)); err != nil {
		return err
	}
	c.ownAdd = own
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
		_, retryAt, st = r.classify(q, pv, now, c.hash, aerr, held)
		return true, nil
	}); err != nil {
		return err
	}
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
			return r.setCopy(rctx, c, CopyFailed, providerWords(pv.Kind)+" is holding adds", "own_add = 0, until_at = ?",
				store.Millis(st.RetryAt))
		}
		if err := r.setCache(rctx, c.hash, c.pk, "miss", "451", now.Add(r.set.MissTTL), now); err != nil {
			return err
		}
		return r.setCopy(rctx, c, CopyFailed, "Real-Debrid refuses this file (451)", "own_add = 0")
	case st.Code == string(adder.CodeNotCached):
		// TorBox refused the cached-only add: nothing was created there.
		if err := r.setCache(rctx, c.hash, c.pk, "uncached", "add", now.Add(r.set.MissTTL), now); err != nil {
			return err
		}
		return r.setCopy(rctx, c, CopyNotCached, "Not cached on "+code, "own_add = 0")
	default:
		return r.setCopy(rctx, c, CopyFailed, aerr.Error(), "own_add = 0")
	}
	// An id the listing or the registry already had is an existing entry, not a new one of ours.
	own = own && !ids[added.ID] && !regIDs[added.ID]
	if err := r.setCopy(rctx, c, CopyChecking, "", "torrent_id = ?, own_add = ?", added.ID, b2i(own)); err != nil {
		return err
	}
	c.tid, c.ownAdd = added.ID, own
	r.log.Info("copy: added", "provider", c.pk, "hash", c.hash, "torrent", added.ID, "ready", added.Ready, "own", own)
	unlock()
	return r.settleCopy(ctx, c, added)
}

// recoverAdding finishes an adding row (no torrent id) from the account: after addSettle, a torrent of the hash
// found there is taken up with own_add false (it may be an older one), else the copy is queued again.
func (r *Router) recoverAdding(ctx context.Context, c *copyRow) error {
	if wait := c.updatedAt.Add(addSettle); wait.After(r.now()) {
		return r.laterUntil(wait)
	}
	if r.copyPaused() {
		return jobs.Later(5 * time.Minute) // postponed, not rewound (rule C)
	}
	rp := r.provs[c.pk]
	if rp == nil {
		return fmt.Errorf("router: copy to %s waits: no read-only client", c.pk)
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
// "Cached on" learns it, added. Not complete (or dead): the uncached verdict is recorded and counted once, with the
// owed delete, and our add is deleted under deleteCopy's checks. Real-Debrid with no playable file to select: the
// copy fails (the file type), and our add is deleted the same way. A delete already owed goes straight to the checks.
// Pause postpones it all (rule C). a is the add of this run (nil after a restart).
func (r *Router) settleCopy(ctx context.Context, c *copyRow, a *adder.Added) error {
	pv := r.pol.Providers[c.pk]
	ad := r.adders[c.pk]
	if pv == nil || ad == nil {
		return fmt.Errorf("router: copy to %s waits: the provider is not configured", c.pk)
	}
	code := copyCode(pv, c.pk)
	if r.copyPaused() {
		return jobs.Later(5 * time.Minute)
	}
	if strings.HasPrefix(c.message, copyDeletingPrefix) {
		return r.deleteCopy(ctx, c, pv, ad)
	}
	if a == nil {
		a = &adder.Added{ID: c.tid, Hash: c.hash}
	}
	paused, unplayable := false, false
	cv, t, why := r.readBack(ctx, pv, c.pk, a, func(t *provider.Torrent) (string, error) {
		ids := PlayableIDs(t.Files)
		if len(ids) == 0 {
			unplayable = true
			return copyUnplayable, nil
		}
		if r.copyPaused() { // the gate right before the write
			paused = true
			return "paused", nil
		}
		return "", ad.SelectFiles(ctx, t.ID, ids)
	})
	if paused || r.copyPaused() {
		return jobs.Later(5 * time.Minute)
	}
	now := r.now()
	switch {
	case cv == cvCached:
		return r.keepComplete(ctx, c, code)
	case unplayable:
		if !c.ownAdd {
			return r.setCopy(ctx, c, CopyFailed, copyUnplayable, "")
		}
		if err := r.oweDelete(ctx, c, false); err != nil {
			return err
		}
		return r.deleteCopy(ctx, c, pv, ad)
	case cv == cvUncached, t != nil && t.State == provider.StateError:
		r.log.Info("copy: not cached", "provider", c.pk, "hash", c.hash, "torrent", c.tid, "why", why)
		if err := r.setCache(context.WithoutCancel(ctx), c.hash, c.pk, "uncached", "add", now.Add(r.set.MissTTL),
			now); err != nil {
			return err
		}
		if !c.ownAdd {
			// Counted, and kept: it was not provably our own new entry.
			if err := r.uncachedVerdict(ctx, c, ""); err != nil {
				return err
			}
			return r.setCopy(ctx, c, CopyNotCached, "Not cached on "+code+"; left on the account: it was not "+
				"provably our own new entry", "")
		}
		if err := r.oweDelete(ctx, c, true); err != nil {
			return err
		}
		return r.deleteCopy(ctx, c, pv, ad)
	}
	// No verdict: a torrent gone from the account ends the copy; anything else is read again later.
	if _, present, err := r.copyRead(ctx, c); err == nil && !present {
		return r.setCopy(ctx, c, CopyFailed, "the torrent left "+code+" before it was complete", "")
	}
	return fmt.Errorf("router: copy to %s not settled: %s", c.pk, why)
}

// copyUnplayable ends a Real-Debrid copy whose torrent has no file Real-Debrid's selection takes (an .iso, a DVD's
// .vob); its owed delete ends with these words too.
const copyUnplayable = "Real-Debrid cannot serve this file type"

// oweDelete records on the row, before any delete is sent, that our add's delete is owed; uncached also counts the
// uncached verdict in the same transaction (rule H), so an interruption can neither lose nor repeat the count.
func (r *Router) oweDelete(ctx context.Context, c *copyRow, uncached bool) error {
	msg := copyDeletingPrefix + c.tid + " on " + c.pk
	if !uncached {
		msg += "; " + copyUnplayable
	}
	if uncached {
		return r.uncachedVerdict(ctx, c, msg)
	}
	return r.setCopy(ctx, c, CopyChecking, msg, "")
}

// uncachedVerdict counts the uncached verdict in pacing and, with msg, records the owed delete on the row, in one
// transaction.
func (r *Router) uncachedVerdict(ctx context.Context, c *copyRow, msg string) error {
	now := r.now()
	err := updatePace(context.WithoutCancel(ctx), r.st, c.pk, now, func(tx *sql.Tx, p *pace) (bool, error) {
		p.uncached(now)
		if msg == "" {
			return true, nil
		}
		res, err := tx.ExecContext(ctx, `UPDATE provider_copy SET message = ?, updated_at = ? WHERE id = ? AND state = ?`,
			msg, store.Millis(now), c.id, c.state)
		if err != nil {
			return false, err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return false, errCopyLost
		}
		return true, nil
	})
	if errors.Is(err, errCopyLost) {
		return err
	}
	if err != nil {
		return fmt.Errorf("router: copy %d: %w", c.id, err)
	}
	if msg != "" {
		c.message, c.updatedAt = msg, now
	}
	return nil
}

// keepComplete ends a copy whose torrent is complete: cached evidence, Cached on, added.
func (r *Router) keepComplete(ctx context.Context, c *copyRow, code string) error {
	now := r.now()
	if err := r.setCacheSource(ctx, c.hash, c.pk, "cached", "add:"+c.pk, now.Add(r.pol.CacheEvidenceSettings().TTL),
		now); err != nil {
		return err
	}
	if err := r.writeCachedOn(ctx, c.hash, c.name, c.pk, c.tid); err != nil {
		return err
	}
	return r.setCopy(ctx, c, CopyAdded, "Added to "+code, "")
}

// copyRead reads the copy's torrent: present, or absent only when the read answers not found AND a successful listing
// of the account shows neither the id nor the hash (rule I; a provider can answer a short-lived 404, and TorBox's
// read cannot tell its own listing apart, so it is listed again). err: no answer either way.
func (r *Router) copyRead(ctx context.Context, c *copyRow) (*provider.Torrent, bool, error) {
	rp := r.provs[c.pk]
	if rp == nil {
		return nil, false, fmt.Errorf("router: copy to %s: no read-only client", c.pk)
	}
	t, err := rp.Torrent(ctx, c.tid)
	switch {
	case err == nil:
		return t, true, nil
	case !errors.Is(err, provider.ErrNotFound):
		return nil, false, fmt.Errorf("router: copy to %s: reading torrent %s: %w", c.pk, c.tid, err)
	}
	list, err := rp.Torrents(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("router: copy to %s: listing after a not-found read: %w", c.pk, err)
	}
	for _, x := range list {
		if x.ID == c.tid || strings.EqualFold(x.Hash, c.hash) {
			return nil, false, fmt.Errorf("router: copy to %s: torrent %s not found but still listed", c.pk, c.tid)
		}
	}
	return nil, false, nil
}

// copyInUse says what on this provider uses the copy's torrent ("" when nothing): a download of ours that names its
// id, or the hash on this provider in a live state (an imported one included: its links read it); a registry entry
// that gives the id another hash; another copy row with the id.
func (r *Router) copyInUse(ctx context.Context, c *copyRow) (string, error) {
	var why string
	err := r.st.Writer().QueryRowContext(ctx, `SELECT 'download ' || id || ' (' || state || ')' FROM download
			WHERE provider = ?1 AND (provider_torrent_id = ?2 OR info_hash = ?3) AND state != 'failed'
		UNION ALL SELECT 'registry content ' || c.info_hash FROM content_provider cp JOIN content c ON c.id = cp.content_id
			WHERE cp.provider = ?1 AND cp.torrent_id = ?2 AND c.info_hash != ?3
		UNION ALL SELECT 'copy ' || id FROM provider_copy WHERE provider = ?1 AND torrent_id = ?2 AND id != ?4
		LIMIT 1`, c.pk, c.tid, c.hash, c.id).Scan(&why)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return why, err
}

// deleteCopy deletes our add, owed since the verdict, after checking again, every attempt (rule B): the torrent is
// still on the account (gone: our delete's work), still not complete (complete: kept, cached evidence, Cached on,
// added), still holds the hash; then, under the add lock so no route adopts it in between, nothing on this provider
// uses it, the gate (rule C) and the delete. A failed delete waits and is checked again; a lost answer is settled by
// the next read.
func (r *Router) deleteCopy(ctx context.Context, c *copyRow, pv *policy.Provider, ad adder.Adder) error {
	code := copyCode(pv, c.pk)
	state, done := CopyNotCached, "Not cached on "+code+"; our add was deleted"
	if strings.HasSuffix(c.message, copyUnplayable) {
		state, done = CopyFailed, copyUnplayable
	}
	// decide ends the copy from a read when it must not be deleted; ok when the delete may go on.
	decide := func() (bool, error) {
		t, present, err := r.copyRead(ctx, c)
		switch {
		case err != nil:
			return false, err
		case !present:
			return false, r.setCopy(ctx, c, state, done, "")
		case t.State == provider.StateDownloaded:
			return false, r.keepComplete(ctx, c, code)
		case t.Hash != "" && !strings.EqualFold(t.Hash, c.hash):
			return false, r.setCopy(ctx, c, CopyFailed, "Not cached on "+code+"; left in place: the entry holds "+
				"another hash", "")
		}
		return true, nil
	}
	if ok, err := decide(); !ok || err != nil { // an early exit before the lock
		return err
	}
	del, ok := ad.(adder.StalledDeleter)
	if !ok {
		return r.setCopy(ctx, c, CopyFailed, "Not cached on "+code+"; left in place: the adder cannot delete", "")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// The decision comes from a read under the lock: the torrent may have completed while the lock was waited for.
	if ok, err := decide(); !ok || err != nil {
		return err
	}
	if why, err := r.copyInUse(ctx, c); err != nil {
		return err
	} else if why != "" {
		return r.setCopy(ctx, c, CopyFailed, "Not cached on "+code+"; left in place: in use ("+why+")", "")
	}
	if r.copyPaused() {
		return jobs.Later(5 * time.Minute)
	}
	if err := jobs.Fence(ctx); err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, providerWriteLimit)
	err := del.DeleteOwnStalledAdd(wctx, c.tid, adder.OwnAdd{NotOnAccountBefore: true, NewEntry: true})
	cancel()
	if err != nil {
		return fmt.Errorf("router: copy to %s: deleting our add %s: %w", c.pk, c.tid, err)
	}
	r.log.Info("copy: our add was deleted", "provider", c.pk, "hash", c.hash, "torrent", c.tid)
	return r.setCopy(ctx, c, state, done, "")
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
