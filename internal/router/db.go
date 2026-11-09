package router

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/store"
)

// States of download.state (migration 00014). The library reads StateReady and sets StateImporting and
// StateImported.
const (
	StateQueued      = "queued"
	StateAdding      = "adding"
	StateAdded       = "added"
	StateDownloading = "downloading"
	StateReady       = "ready"
	StateImporting   = "importing"
	StateImported    = "imported"
	StateFailed      = "failed"
)

// inFlightStates are the states of a download still in hand (not failed, not imported).
const inFlightStates = `('queued', 'adding', 'added', 'downloading', 'ready', 'importing')`

// inFlight reports whether the version already has this hash in flight for the title: another download row, or the
// row this route resumes when it moved on from queued (another route resumed it first).
func (r *Router) inFlight(ctx context.Context, req Request, hash string) (int64, bool, error) {
	var id int64
	err := r.st.Writer().QueryRowContext(ctx, `SELECT id FROM download WHERE title_id = ? AND version_id = ?
		AND info_hash = ? AND state IN `+inFlightStates+` AND NOT (id = ? AND state = 'queued') ORDER BY id LIMIT 1`,
		req.TitleID, req.VersionID, hash, req.DownloadID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("router: in-flight check: %w", err)
	}
	return id, true, nil
}

// openDownload returns the download row for a candidate: the queued row being resumed when it holds this hash, or a
// new row in state queued. A resumed row takes the candidate as this route refreshed it (freshness narrowed its
// episodes, a new decision its quality, score, formats and episode mapping) in the same write that resumes it, before
// any add: the library imports what was placed. rev is the policy revision the candidate was decided
// again under (nil: it was not).
func (r *Router) openDownload(ctx context.Context, req Request, c *Candidate, hash string, rev any) (int64, error) {
	now := store.Millis(r.now())
	w := r.st.Writer()
	eps, _ := json.Marshal(nonNil(c.Episodes))
	var formats any
	if c.Formats != nil {
		b, _ := json.Marshal(c.Formats)
		formats = string(b)
	}
	var epMap any
	if c.EpisodeMap != nil {
		b, _ := json.Marshal(c.EpisodeMap)
		epMap = string(b)
	}
	releaseData, err := json.Marshal(storedRelease{Release: c.Release, RouteSearch: c.Search, RouteCriteria: c.Criteria,
		RouteIdentity: c.Identity, RouteOrigin: c.Origin})
	if err != nil {
		return 0, fmt.Errorf("router: release data: %w", err)
	}
	if req.DownloadID != 0 {
		// A resumed download keeps the search criteria its candidate carries.
		var criteria any
		if c.Search && len(c.Criteria) > 0 {
			b, _ := json.Marshal(c.Criteria)
			criteria = string(b)
		}
		res, err := w.ExecContext(ctx, `UPDATE download SET updated_at = ?, quality = ?, score = ?, episodes = ?,
			formats = COALESCE(?, formats), episode_map = ?, policy_rev = COALESCE(?, policy_rev),
			release_data = CASE WHEN ? IS NOT NULL
				THEN json_set(CASE WHEN json_valid(release_data) AND json_type(release_data) = 'object' THEN release_data ELSE ? END, '$.route_search', json('true'), '$.route_criteria', json(?)) ELSE release_data END
			WHERE id = ? AND info_hash = ? AND state = 'queued'`, now, c.Quality, c.Score, string(eps), formats, epMap,
			rev, criteria, string(releaseData), criteria, req.DownloadID, hash)
		if err != nil {
			return 0, fmt.Errorf("router: resuming download %d: %w", req.DownloadID, err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return req.DownloadID, nil
		}
		// The queued row moved on. With this release it was ended (removed from the queue, failed, imported) while
		// the route waited: the route ends too, and no new download replaces it. A row with another
		// release is a different candidate's: this one gets its own row.
		var state string
		err = w.QueryRowContext(ctx, `SELECT state FROM download WHERE id = ? AND info_hash = ?`, req.DownloadID,
			hash).Scan(&state)
		switch {
		case err == nil:
			return 0, fmt.Errorf("download %d is %s: %w", req.DownloadID, state, errMovedOn)
		case !errors.Is(err, sql.ErrNoRows):
			return 0, fmt.Errorf("router: resuming download %d: %w", req.DownloadID, err)
		}
	}
	var size any
	if c.Release.Size > 0 {
		size = c.Release.Size
	}
	res, err := w.ExecContext(ctx, `INSERT INTO download (title_id, version_id, version_key, info_hash, release_title,
		indexer, quality, score, size_bytes, episodes, formats, episode_map, state, source, policy_rev, created_at,
		updated_at, release_data) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?, ?)`, req.TitleID, req.VersionID,
		req.Version, hash, c.Release.Title, c.Release.Indexer, c.Quality, c.Score, size, string(eps), formats, epMap,
		req.Source, r.pol.Revision, now, now, string(releaseData))
	if err != nil {
		return 0, fmt.Errorf("router: new download: %w", err)
	}
	return res.LastInsertId()
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func (r *Router) recordAttempt(ctx context.Context, dl int64, st Step) error {
	outcome := st.Outcome
	switch outcome {
	case "accepted", "refused", "skipped", "error", "deferred":
	default:
		return nil // would_add and miss are not provider attempts
	}
	var retry any
	if !st.RetryAt.IsZero() {
		retry = store.Millis(st.RetryAt)
	}
	_, err := r.st.Writer().ExecContext(ctx, `INSERT INTO download_attempt (download_id, provider, outcome, code,
		message, retry_at, attempted_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, dl, attemptBy(st), outcome, st.Code,
		st.Message, retry, store.Millis(r.now()))
	if err != nil {
		return fmt.Errorf("router: recording an attempt: %w", err)
	}
	return nil
}

// attemptBy is the download_attempt.provider of a step: its provider, or "client:<key>" for a client add.
func attemptBy(st Step) string {
	if st.Provider == "" && st.Client != "" {
		return clientCacheKey(st.Client)
	}
	return st.Provider
}

// errAddTaken: a write that settles an add found the download no longer where this route left it: another route
// (another process, after a lost lease) settled the add or holds the row, or the download was ended (Dismiss).
// Nothing is written over its result. Every guarded transition answers it (move).
var errAddTaken = errors.New("router: the download moved on; another route holds it")

// errMovedOn: the download a route holds was ended or taken while the route ran (removed from the queue, failed,
// settled by another route). The route stops there: nothing more is added for it and no new download replaces it.
var errMovedOn = errors.New("router: the download was ended or taken over while its route ran")

var errProviderChanged = errors.New("router: provider changed during route; retry under current settings")

// checkProvider serializes the provider account check with the settings commit. Only DB-managed routers are
// fenced; a YAML router's providers do not come from the settings tables.
func (r *Router) checkProvider(ctx context.Context, tx *sql.Tx, key string) error {
	if !r.settingsManaged {
		return nil
	}
	var epoch int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(provider_epoch, 0) FROM settings_runtime_state WHERE id = 1").Scan(&epoch); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if epoch != r.providerEpoch {
		return errProviderChanged
	}
	return nil
}

// transition is one guarded write to a download row. It applies only while the row is still where
// its writer left it: in one of the expected states, holding the add attempt or the torrent it expects. A download
// ended meanwhile (Dismiss: removed from the queue, blocklisted) or taken by another route is never moved back.
// Writes that start something (an add) also need the route job's lease. What must commit with the change (the
// provider fence, a blocklist row, the route job that resumes the download) runs in the same transaction.
type transition struct {
	from     []string        // the states the row must be in
	attempt  *sql.NullString // the add_attempt it must hold (IS ?), when not nil
	provider string          // the provider it must be on, when set
	torrent  string          // the provider_torrent_id it must hold, when set
	lease    bool            // the route job must still hold its lease (jobs.Fence), checked before the write
	set      string          // the SET clause, with args
	args     []any
	before   func(*sql.Tx) error // in the transaction, before the write
	after    func(*sql.Tx) error // in the transaction, after it
}

// move applies t to download dl. errAddTaken when the row is not where t expects it; nothing is written then.
func (r *Router) move(ctx context.Context, dl int64, t transition) error {
	if t.lease {
		if err := jobs.Fence(ctx); err != nil {
			return err
		}
	}
	where, args := `id = ? AND state IN (?`+strings.Repeat(", ?", len(t.from)-1)+`)`, []any{dl}
	for _, st := range t.from {
		args = append(args, st)
	}
	if t.attempt != nil {
		where, args = where+` AND add_attempt IS ?`, append(args, *t.attempt)
	}
	if t.provider != "" {
		where, args = where+` AND provider = ?`, append(args, t.provider)
	}
	if t.torrent != "" {
		where, args = where+` AND provider_torrent_id = ?`, append(args, t.torrent)
	}
	return r.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if t.before != nil {
			if err := t.before(tx); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx, `UPDATE download SET `+t.set+` WHERE `+where, append(t.args, args...)...)
		if err != nil {
			return fmt.Errorf("router: download %d: %w", dl, err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("download %d: %w", dl, errAddTaken)
		}
		if t.after != nil {
			return t.after(tx)
		}
		return nil
	})
}

// newAttempt is a fresh add_attempt token.
func newAttempt() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// markAdding records that an add to provider is about to be sent for download dl, which this route holds in queued,
// and returns the attempt's token. Until a write that matches the token settles it, the add may or may not have
// reached the provider (reconcile.go). updated_at is when the add was sent: nothing else writes a row in adding.
// The add cannot be taken back, so a route job that lost its lease stops here (reconcile.go). alt records, before the
// add is sent, that it is altRetry's second add (select_alt_torrent = altPending): an add whose answer
// is lost and that reconciliation adopts keeps its selection intent (markAdded).
func (r *Router) markAdding(ctx context.Context, dl int64, provider string, alt bool) (string, error) {
	attempt := newAttempt()
	var intent any
	if alt {
		intent = altPending
	}
	err := r.move(ctx, dl, transition{from: []string{StateQueued}, lease: true,
		set:  `state = 'adding', provider = ?, add_attempt = ?, error = NULL, updated_at = ?, select_alt_torrent = ?`,
		args: []any{provider, attempt, store.Millis(r.now()), intent},
		before: func(tx *sql.Tx) error {
			return r.checkProvider(ctx, tx, provider)
		}})
	return attempt, err
}

// unmarkAdding returns a download whose add the provider refused (the attempt's own answer) to queued, with no
// provider: nothing to reconcile.
func (r *Router) unmarkAdding(ctx context.Context, dl int64, attempt string) error {
	a := attemptOf(attempt)
	return r.move(ctx, dl, transition{from: []string{StateAdding}, attempt: &a,
		set:  `state = 'queued', provider = NULL, client = NULL, add_attempt = NULL, updated_at = ?`,
		args: []any{store.Millis(r.now())}})
}

// noteUnsettled records on a download in adding why its add is not settled yet (Activity and the health check show
// it), with the route job that settles it (next; nil for none). updated_at stays the time the add was sent.
func (r *Router) noteUnsettled(ctx context.Context, dl int64, attempt sql.NullString, why string,
	next func(*sql.Tx) error) error {
	err := r.move(ctx, dl, transition{from: []string{StateAdding}, attempt: &attempt, set: `error = ?`,
		args: []any{"add not settled: " + why}, after: next})
	if errors.Is(err, errAddTaken) {
		return nil // settled or ended meanwhile: nothing to note, and nothing to resume
	}
	return err
}

// failDownload fails download dl while it is still in one of the states from (a route cancelled
// after its download moved on, a status read that raced a newer one). A download that left them keeps its state and
// reason; nothing is written then. A download in adding is never failed here: its add may have landed, and only a
// write that matches its attempt (reconcile.go) may settle it.
func (r *Router) failDownload(ctx context.Context, dl int64, why string, from ...string) error {
	err := r.move(ctx, dl, transition{from: from, set: `state = 'failed', error = ?, updated_at = ?`,
		args: []any{why, store.Millis(r.now())}})
	if errors.Is(err, errAddTaken) {
		return nil
	}
	return err
}

// failProviderDownload fails and blocklists a download that the provider can no longer supply. It ends only our
// record: nothing is deleted on the provider. Queued, unsettled adds and downloads handed to the library stay put.
func (r *Router) failProviderDownload(ctx context.Context, dl int64, why string) error {
	now := store.Millis(r.now())
	var journal string
	if err := r.st.Writer().QueryRowContext(ctx, `SELECT COALESCE(error, '') FROM download WHERE id = ?`, dl).
		Scan(&journal); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("router: download %d: %w", dl, err)
	}
	deleting := strings.HasPrefix(journal, deletingPrefix)
	if deleting {
		// Our own uncached delete was in flight when its route stopped (noteDeleting): that is what took the
		// torrent, not the provider. Nothing is blocklisted.
		why += "; " + journal + " when its route was interrupted: not blocklisted"
	}
	err := r.move(ctx, dl, transition{from: []string{StateAdded, StateDownloading},
		set: `state = 'failed', error = ?, updated_at = ?`, args: []any{why, now},
		after: func(tx *sql.Tx) error {
			if deleting {
				return nil
			}
			return blocklistDownload(ctx, tx, dl, why, BlocklistProvider, now)
		}})
	if errors.Is(err, errAddTaken) {
		return nil
	}
	return err
}

func blocklistDownload(ctx context.Context, tx *sql.Tx, dl int64, why, source string, now int64) error {
	var version, hash, release string
	var titleID int64
	if err := tx.QueryRowContext(ctx, `SELECT version_key, info_hash, title_id, release_title FROM download
		WHERE id = ?`, dl).Scan(&version, &hash, &titleID, &release); err != nil {
		return err
	}
	return Blocklist(ctx, tx, version, hash, titleID, dl, release, why, source, now)
}

// queueDownload leaves the download this route holds queued until retryAt, together with the route job that resumes
// it then (next; nil for none), so a queued download is never left without its continuation.
// errMovedOn when the download was ended or taken meanwhile.
func (r *Router) queueDownload(ctx context.Context, dl int64, retryAt time.Time, next func(*sql.Tx) error) error {
	err := r.move(ctx, dl, transition{from: []string{StateQueued}, lease: true,
		set: `error = ?, updated_at = ?`, args: []any{"waiting for a provider until " +
			retryAt.UTC().Format(time.RFC3339), store.Millis(r.now())}, after: next})
	if errors.Is(err, errAddTaken) {
		return fmt.Errorf("download %d: %w", dl, errMovedOn)
	}
	return err
}

// endQueued fails the download this route holds in queued, with why. errMovedOn when it was ended or taken
// meanwhile.
func (r *Router) endQueued(ctx context.Context, dl int64, why string) error {
	err := r.move(ctx, dl, transition{from: []string{StateQueued}, set: `state = 'failed', error = ?, updated_at = ?`,
		args: []any{why, store.Millis(r.now())}})
	if errors.Is(err, errAddTaken) {
		return fmt.Errorf("download %d: %w", dl, errMovedOn)
	}
	return err
}

// addFence is what a write that records an add's outcome must find: the download in queued (a reuse found before
// any add), or in adding with the attempt it settles (NULL for a row left by a build before add_attempt).
type addFence struct {
	adding  bool
	attempt sql.NullString
	// alt: the add is altRetry's second add on Real-Debrid; markAdded records its torrent as select_alt_torrent.
	alt bool
}

// altPending in select_alt_torrent marks a download in adding whose add is altRetry's second add (markAdding): no
// torrent id yet. It never matches a torrent id, so the poller never reads it as one.
const altPending = "(alt add pending)"

// fromQueued fences a write on the download this route holds in queued.
var fromQueued = addFence{}

// fromAdding fences a write on the add attempt it settles.
func fromAdding(attempt sql.NullString) addFence { return addFence{adding: true, attempt: attempt} }

// markAdded records an accepted add (or a reused torrent) on the download, fenced by f. own_add starts unset: only
// recordOwnAdd, after a guarded add, may set it (a reconciled adoption never does). select_alt_torrent
// names the torrent when f.alt (the second selection's add) or, for a write that settles an add
// attempt, when markAdding recorded that attempt as the second add (altPending: an adoption by reconciliation after
// the answer was lost); else it is cleared.
func (r *Router) markAdded(ctx context.Context, dl int64, provider string, a *adder.Added, reused bool,
	f addFence) error {
	now := store.Millis(r.now())
	var cached any
	if a.Ready {
		cached = 1
	}
	t := transition{from: []string{StateQueued}, set: `state = 'added', provider = ?, provider_torrent_id = ?,
		provider_epoch = ?, name = COALESCE(NULLIF(?, ''), name), cached = ?, reused = ?, own_add = NULL, error = NULL, added_at = ?,
		cached_check = NULL, selected_at = NULL, updated_at = ?, select_alt_torrent = CASE WHEN ? = 1 OR (? = 1 AND select_alt_torrent = ?) THEN ? END`,
		args: []any{provider, a.ID, r.recordedEpoch(), a.Name, cached, b2i(reused), now, now, b2i(f.alt),
			b2i(f.adding), altPending, a.ID},
		before: func(tx *sql.Tx) error {
			return r.checkProvider(ctx, tx, provider)
		}}
	if f.adding {
		t.from, t.attempt = []string{StateAdding}, &f.attempt
	}
	return r.move(ctx, dl, t)
}

// attemptOf is a token as a fence takes it.
func attemptOf(token string) sql.NullString { return sql.NullString{String: token, Valid: token != ""} }

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// cacheState reads provider_cache for a hash at a provider: "miss", "uncached", "cached" or "" (nothing live).
func (r *Router) cacheState(ctx context.Context, hash, provider string, now time.Time) (string, error) {
	var state string
	err := r.st.Writer().QueryRowContext(ctx, `SELECT state FROM provider_cache WHERE info_hash = ? AND provider = ?
		AND (expires_at IS NULL OR expires_at > ?)`, hash, provider, store.Millis(now)).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("router: cache state: %w", err)
	}
	return state, nil
}

// setCache writes a provider_cache row (docs/design/providers.md).
func (r *Router) setCache(ctx context.Context, hash, provider, state, source string, expires, now time.Time) error {
	return r.setCacheSource(ctx, hash, provider, state, "router:"+source, expires, now)
}

// setCacheSource is setCache with the source as given (add:<provider> for an add the provider had at once).
// An all-provider miss leaves a live 451 row as it is (source, checked_at, expiry), so a later account hold can
// explain and lift it and a shorter miss never shortens it; and a live uncached answer of a read-only cache check
// (router:check, check:*), which keeps its own shorter life.
func (r *Router) setCacheSource(ctx context.Context, hash, provider, state, source string, expires,
	now time.Time) error {
	_, err := r.st.Writer().ExecContext(ctx, `INSERT INTO provider_cache (info_hash, provider, state, source,
		checked_at, expires_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (info_hash, provider) DO UPDATE SET
		state = excluded.state, source = excluded.source, checked_at = excluded.checked_at,
		expires_at = excluded.expires_at
		WHERE excluded.source <> 'router:miss' OR provider_cache.expires_at <= excluded.checked_at
		   OR NOT (provider_cache.state = 'miss' AND provider_cache.source = 'router:451'
		           OR provider_cache.state = 'uncached'
		              AND (provider_cache.source = 'router:check' OR provider_cache.source LIKE 'check:%'))`,
		hash, provider, state, source, store.Millis(now), store.Millis(expires))
	if err != nil {
		return fmt.Errorf("router: provider cache: %w", err)
	}
	return nil
}

// recordMiss caches a miss on every provider for ttl (miss_ttl, shorter for new content: verdictTTL), except those
// this route did not try (skip).
func (r *Router) recordMiss(ctx context.Context, hash string, providers []string, skip map[string]string,
	now time.Time, ttl time.Duration) error {
	for _, pk := range providers {
		if r.adders[pk] == nil || skip[pk] != "" {
			continue
		}
		if err := r.setCache(ctx, hash, pk, "miss", "miss", now.Add(ttl), now); err != nil {
			return err
		}
	}
	return nil
}

// recordedEpoch is the provider epoch a DB-managed router records with a torrent id (checkProvider has matched it
// in the same transaction); NULL for a YAML router, which has none.
func (r *Router) recordedEpoch() any {
	if !r.settingsManaged {
		return nil
	}
	return r.providerEpoch
}

// heldBy returns the provider's torrent id for a hash it already holds: from the registry (provider sync), or from
// a download of ours still in hand. "" when it holds none we know of. A DB-managed router trusts a download's id only
// when it was recorded under its own provider epoch: an id from a previous account (or a row from before epochs were
// recorded, NULL) is not reused, and the registry, which an account change clears, answers for those instead.
//
// With read (an active route or reconciliation; never a dry run, which sends nothing), the id is first read from the
// provider: the registry can be six hours old and a download's id older, and an entry that left the account (a zurg
// repair re-added it under a new id, another tool deleted it) would fail the download and blocklist a good release.
// An entry that is not found or holds another hash is not reused: its registry row is marked absent, a download's
// stale id is passed over, and the next known id is tried; with none left the hash is added fresh. A read that fails
// otherwise proves nothing (the provider may be the one that is down): the id comes back with unread set
// to the read's error (after one retry, heldReadRetry later), and the caller does not reuse it (a route defers the provider, reconciliation asks the
// account's list instead). Otherwise a route queued again after a provider outage
// (endUnavailable) would go straight back to the provider that was down.
func (r *Router) heldBy(ctx context.Context, hash, provider string, read bool) (id string, unread, err error) {
	var skip []string
	for range 8 {
		id, registry, err := r.knownID(ctx, hash, provider, skip)
		if err != nil || id == "" || !read {
			return id, nil, err
		}
		why, rerr := r.stillHeld(ctx, provider, id, hash)
		if rerr != nil && ctx.Err() == nil {
			// One more read after a short pause: a single glitch must not send the hash to another provider as a
			// second add. The pause respects ctx (a pass's deadline).
			if r.sleep(ctx, heldReadRetry) == nil {
				why, rerr = r.stillHeld(ctx, provider, id, hash)
			}
		}
		if rerr != nil {
			return id, rerr, nil
		}
		if why == "" {
			return id, nil, nil
		}
		r.log.Info("known torrent id not reused", "provider", provider, "torrent", id, "hash", hash, "why", why,
			"registry", registry)
		if registry {
			if _, err := r.st.Writer().ExecContext(ctx, `UPDATE content_provider SET state = 'absent', absent_at = ?
				WHERE provider = ? AND torrent_id = ? AND state = 'present'`, store.Millis(r.now()), provider,
				id); err != nil {
				return "", nil, fmt.Errorf("router: registry: %w", err)
			}
		}
		skip = append(skip, id)
	}
	return "", nil, nil
}

// heldReadRetry is the pause before heldBy reads a known torrent id a second time after a failed read.
var heldReadRetry = 2 * time.Second

// knownID is heldBy's lookup without the read: the registry first, then our downloads, leaving out the ids in skip.
func (r *Router) knownID(ctx context.Context, hash, provider string, skip []string) (string, bool, error) {
	not, notArgs := "", []any{}
	for _, id := range skip {
		not += " AND %s != ?"
		notArgs = append(notArgs, id)
	}
	var id string
	err := r.st.Writer().QueryRowContext(ctx, `SELECT cp.torrent_id FROM content_provider cp
		JOIN content c ON c.id = cp.content_id WHERE c.info_hash = ? AND cp.provider = ? AND cp.state = 'present'
		AND cp.torrent_id IS NOT NULL AND cp.torrent_id != ''`+strings.ReplaceAll(not, "%s", "cp.torrent_id")+` LIMIT 1`,
		append([]any{hash, provider}, notArgs...)...).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("router: registry: %w", err)
	}
	epoch, args := "", []any{hash, provider}
	if r.settingsManaged {
		epoch, args = " AND provider_epoch = ?", append(args, r.providerEpoch)
	}
	err = r.st.Writer().QueryRowContext(ctx, `SELECT provider_torrent_id FROM download WHERE info_hash = ?
		AND provider = ? AND provider_torrent_id IS NOT NULL AND state IN ('added', 'downloading', 'ready',
		'importing', 'imported')`+epoch+strings.ReplaceAll(not, "%s", "provider_torrent_id")+` ORDER BY id DESC LIMIT 1`,
		append(args, notArgs...)...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("router: downloads: %w", err)
	}
	return id, false, nil
}

// stillHeld reads a known torrent id back from the provider. It says why the id is not to be reused (not found, or
// the entry holds another hash), "" when the read verified it; unread is the read's error when the provider did not
// answer for it at all (a transport error, a 5xx, a gateway refusal): presence not verified either way. A provider
// without a read-only client verifies nothing and refuses nothing: "" and nil, as before.
func (r *Router) stillHeld(ctx context.Context, pk, id, hash string) (why string, unread error) {
	rp := r.provs[pk]
	if rp == nil {
		return "", nil
	}
	t, err := rp.Torrent(ctx, id)
	switch {
	case errors.Is(err, provider.ErrNotFound):
		return "not on " + pk + " any more", nil
	case err != nil:
		r.log.Info("known torrent id not read back; not reused", "provider", pk, "torrent", id, "err", err)
		return "", err
	case t != nil && t.Hash != "" && !strings.EqualFold(t.Hash, hash):
		return "the entry holds another hash", nil
	}
	return "", nil
}

// recordPlan stores a dry run for the shadow report.
func (r *Router) recordPlan(ctx context.Context, req Request, res *Result) error {
	outcome := "none"
	switch res.Outcome {
	case OutcomeWouldAdd:
		outcome = "would_add"
	case OutcomeDeferred:
		outcome = "deferred"
	}
	steps, err := json.Marshal(res.Steps)
	if err != nil {
		return err
	}
	out, err := r.st.Writer().ExecContext(ctx, `INSERT INTO router_plan (title_id, version_key, mode, outcome,
		release_title, indexer, info_hash, provider, steps, source, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.TitleID, req.Version, string(res.Mode), outcome, res.Release, res.Indexer, res.Hash, res.Provider,
		string(steps), req.Source, store.Millis(r.now()))
	if err != nil {
		return fmt.Errorf("router: recording the plan: %w", err)
	}
	res.PlanID, _ = out.LastInsertId()
	return nil
}

// storedRelease is a download's release_data: the release as the route had it, plus whether it came from a search and
// that search's criteria, so a route that resumes the download (requeue, strays, a retry) re-decides it as it was
// found. Readers of the release alone ignore the route fields; no schema change.
type storedRelease struct {
	indexer.Release
	RouteSearch   bool                   `json:"route_search,omitempty"`
	RouteCriteria []*decide.SeriesSearch `json:"route_criteria,omitempty"`
	RouteIdentity int                    `json:"route_identity,omitempty"` // Candidate.Identity
	RouteOrigin   string                 `json:"route_origin,omitempty"`   // Candidate.Origin
}
