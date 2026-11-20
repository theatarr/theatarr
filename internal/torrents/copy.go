package torrents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// "Add to other providers" (docs/design/torrents.md): a file's torrent is added to the other accounts that have it
// cached. One job per torrent; each provider's attempt is bounded, runs under the router's add lock and ends with one
// final outcome in the journal (action copy). An add that is not cached is our own new entry (the hash was not on the
// fresh list read under the lock) and is deleted again on Real-Debrid and AllDebrid; TorBox refuses it itself. Nothing
// that was on an account before is ever deleted. The library's links are not changed.

// JobCopy is a copy's job: the button's (every provider with a key) or, with CopyPayload.Auto, the one queued after an
// import (only the providers set to also add, where the torrent is known cached). The two modes have their own dedupe
// keys (copyDedupe), so a queued automatic copy never swallows a click.
const JobCopy = "torrents.copy"

// copyDedupe is a copy job's dedupe key: torrents.copy/<hash> for the button, torrents.copy.auto/<hash> automatically.
func copyDedupe(hash string, auto bool) string {
	if auto {
		return JobCopy + ".auto/" + hash
	}
	return JobCopy + "/" + hash
}

// Copy outcomes (torrent_journal.outcome of action copy; OutSending while the add is out).
const (
	CopyAdded     = "added"      // added, and the provider holds it complete
	CopyPresent   = "present"    // the account had it already (detail: still downloading, or failed there)
	CopyNotCached = "not_cached" // not cached there: our add was deleted again (TorBox refused it)
	CopyRefused   = "refused"    // Real-Debrid's name rule refuses the release or a file of it
	CopySkipped   = "skipped"    // not tried: the provider is paced or backed off (detail starts "until <time>: ")
	CopyFailed    = "failed"
)

// copyPaceWait is the longest a copy waits for the provider's pacing (Real-Debrid's min_gap); a longer wait ends the
// provider's part skipped, with the time it takes adds again.
const copyPaceWait = 5 * time.Second

// CopyPayload is a copy job's payload. Providers limits it to those keys (an automatic copy); empty is every provider
// with a key.
type CopyPayload struct {
	Hash      string   `json:"hash"`
	By        string   `json:"by,omitempty"`
	Auto      bool     `json:"auto,omitempty"`
	Providers []string `json:"providers,omitempty"`
}

// ErrUnknownHash: Theatarr knows no torrent with the hash (on an account, in the library or a download in hand).
var ErrUnknownHash = errors.New("torrents: no torrent of Theatarr's has this hash")

// EnqueueCopy queues a copy of p.Hash; one per hash and mode while queued or running. It answers the job's id.
func EnqueueCopy(ctx context.Context, q *jobs.Queue, p CopyPayload) (int64, error) {
	if q == nil {
		return 0, errors.New("torrents: no job queue")
	}
	h := indexer.NormalizeHash(p.Hash)
	if h == "" {
		return 0, errors.New("torrents: not a v1 info hash")
	}
	p.Hash = h
	id, _, err := q.Enqueue(ctx, JobCopy, p, jobs.EnqueueOptions{DedupeKey: copyDedupe(h, p.Auto), MaxAttempts: 1})
	return id, err
}

// KnownHash reports whether hash is one Theatarr knows: a torrent in the record of an account, the hash an active
// link records or shows (its folder hash too: good enough to add a torrent, never proof for a delete), or a download
// that has not failed.
func (m *Manager) KnownHash(ctx context.Context, hash string) (bool, error) {
	h := indexer.NormalizeHash(hash)
	if h == "" {
		return false, nil
	}
	var ok bool
	err := m.Store.Reader().QueryRowContext(ctx, `SELECT
		EXISTS (SELECT 1 FROM provider_torrent WHERE info_hash = ?1)
		OR EXISTS (SELECT 1 FROM download WHERE lower(info_hash) = ?1 AND state <> 'failed')
		OR EXISTS (SELECT 1 FROM link l WHERE l.state = 'active'
			AND (lower(COALESCE(l.folder_hash, '')) = ?1 OR `+router.LinkStoredHashSQL+` = ?1))`, h).Scan(&ok)
	return ok, err
}

// QueueAutoCopy queues the automatic copy after an import of hash from provider from: to every other provider with a
// key and also_add on that has live cached evidence for the hash (provider_cache cached, not expired: DMM's lists,
// TorBox's check, an earlier add) and does not hold it complete in the record. No evidence, nothing queued: the
// automatic copy never tries an add of something not known cached. Nothing while paused (or the router off). It
// answers the job's id, 0 when nothing was queued.
func (m *Manager) QueueAutoCopy(ctx context.Context, from, hash string) (int64, error) {
	h := indexer.NormalizeHash(hash)
	if h == "" || !m.Policy.RouterSettings().Enabled {
		return 0, nil
	}
	// The providers with live cached evidence for the hash that do not hold it complete, in one read.
	var cached []string
	if err := scanStrings(ctx, m.Store.Reader(), `SELECT provider FROM provider_cache WHERE info_hash = ?1
		AND state = 'cached' AND (expires_at IS NULL OR expires_at > ?2)
		AND provider NOT IN (SELECT provider FROM provider_torrent WHERE info_hash = ?1 AND progress >= 100
			AND state <> 'error')`, []any{h, store.Millis(m.now())}, func(k string) { cached = append(cached, k) }); err != nil {
		return 0, err
	}
	var targets []string
	for _, k := range m.keys() {
		if pv := m.Policy.Providers[k]; k != from && pv.AlsoAdd && m.Providers[k] != nil && slices.Contains(cached, k) {
			targets = append(targets, k)
		}
	}
	if len(targets) == 0 {
		return 0, nil
	}
	return EnqueueCopy(ctx, m.Queue, CopyPayload{Hash: h, By: "auto", Auto: true, Providers: targets})
}

// CopyTargets lists the providers with a key, in fallback order, that are not among held (provider keys): where
// "Add to other providers" may add the torrent.
func (m *Manager) CopyTargets(held []string) []string {
	var out []string
	for _, k := range m.keys() {
		if m.Providers[k] != nil && !slices.Contains(held, k) {
			out = append(out, k)
		}
	}
	return out
}

// Copy runs a copy job: for every provider p names (every provider with a key when none), in fallback order, one
// bounded attempt (copyOne), journaled. It runs alone among restores and deletes (opMu). Pause does not stop the
// button's copy (the person's own action, as a restore); an automatic one is skipped while paused, and also at an
// account at its torrent-limit warning share. A paced provider ends skipped in both: the button remains.
func (m *Manager) Copy(ctx context.Context, p CopyPayload) ([]JournalEntry, error) {
	h := indexer.NormalizeHash(p.Hash)
	if h == "" {
		return nil, errors.New("torrents: not a v1 info hash")
	}
	opMu.Lock()
	defer opMu.Unlock()
	name, names := m.hashNames(ctx, h)
	var out []JournalEntry
	for _, k := range m.keys() {
		if len(p.Providers) > 0 && !slices.Contains(p.Providers, k) {
			continue
		}
		if len(p.Providers) == 0 && (m.Providers[k] == nil || m.Adders[k] == nil) {
			continue
		}
		je := JournalEntry{Action: "copy", Provider: k, Hash: h, Name: name, By: p.By}
		if err := ctx.Err(); err != nil {
			je.Outcome, je.Detail = CopySkipped, err.Error()
		} else {
			m.copyOne(ctx, &je, names, p.Auto)
		}
		if je.ID != 0 {
			if _, err := m.journalRow(context.WithoutCancel(ctx), je); err != nil {
				m.log().Error("torrents: journal", "action", "copy", "hash", h, "err", err)
			}
		} else {
			m.journal(context.WithoutCancel(ctx), je)
		}
		out = append(out, je)
	}
	return out, nil
}

// hashNames are the names Theatarr has for hash: the record's and our downloads' release titles. The first is the
// journal's name.
func (m *Manager) hashNames(ctx context.Context, h string) (string, []string) {
	var names []string
	if err := scanStrings(ctx, m.Store.Reader(), `SELECT name FROM provider_torrent WHERE info_hash = ?1 AND name <> ''
		UNION ALL SELECT release_title FROM download WHERE lower(info_hash) = ?1 AND COALESCE(release_title, '') <> ''`,
		[]any{h}, func(s string) {
			if !slices.Contains(names, s) {
				names = append(names, s)
			}
		}); err != nil {
		m.log().Warn("torrents: copy: the torrent's names", "hash", h, "err", err)
	}
	if len(names) == 0 {
		return "", nil
	}
	return names[0], names
}

func scanStrings(ctx context.Context, db *sql.DB, q string, args []any, f func(string)) error {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return err
		}
		f(s)
	}
	return rows.Err()
}

// copyOne is one provider's attempt, filling je (journal row; je.ID set once its add was announced as sending). The
// whole attempt, from the fresh list to the clean-up, runs under the router's add lock, so a route's add of the same
// hash cannot land between our list read and our add and be taken for ours.
func (m *Manager) copyOne(ctx context.Context, je *JournalEntry, names []string, auto bool) {
	key, h := je.Provider, je.Hash
	pv, ad, rp := m.Policy.Providers[key], m.Adders[key], m.Providers[key]
	if pv == nil || ad == nil || rp == nil {
		je.Outcome, je.Detail = CopyFailed, ErrNoClient.Error()
		return
	}
	if auto && !m.Policy.RouterSettings().Enabled {
		je.Outcome, je.Detail = CopySkipped, "paused"
		return
	}
	rs := m.Policy.RouterSettings()
	mag, err := adder.Restore(h, rs.Trackers)
	if err != nil {
		je.Outcome, je.Detail = CopyFailed, err.Error()
		return
	}
	lock := m.AddLock
	if lock == nil {
		lock = noLock{}
	}
	pc := pv.EffectivePacing()
	var before map[string]bool // the ids on the account before our add
	for try := 0; ; try++ {
		lock.Lock()
		list, err := rp.Torrents(ctx)
		if err != nil {
			lock.Unlock()
			busy(je, err, copyReadFailed+": ")
			return
		}
		before = make(map[string]bool, len(list))
		var on *provider.Torrent
		for i, t := range list {
			before[t.ID] = true
			if strings.EqualFold(t.Hash, h) && (on == nil || t.State == provider.StateDownloaded) {
				on = &list[i]
			}
		}
		if on != nil {
			lock.Unlock()
			m.copyPresent(ctx, je, on)
			return
		}
		// Real-Debrid's name rule guards only a new add: an entry already there was handled above.
		if pv.Kind == provider.KindRealDebrid {
			if n := refusedName(names); n != "" {
				lock.Unlock()
				je.Outcome, je.Detail = CopyRefused, n
				return
			}
		}
		if auto && policy.NearTorrentLimit(len(list), pv.TorrentLimit()) {
			lock.Unlock()
			je.Outcome, je.Detail = CopySkipped, fmt.Sprintf("the account holds %d of its %d torrents", len(list),
				pv.TorrentLimit())
			return
		}
		until, why, err := router.ReserveAdd(ctx, m.Store, key, pc, m.now())
		if err != nil {
			lock.Unlock()
			je.Outcome, je.Detail = CopyFailed, "pacing: "+err.Error()
			return
		}
		if until.IsZero() {
			break
		}
		lock.Unlock()
		wait := until.Sub(m.now())
		if try > 0 || wait > copyPaceWait {
			je.Outcome, je.Detail = CopySkipped, "until "+until.UTC().Format(time.RFC3339)+": "+why
			return
		}
		if err := m.sleep(ctx, max(wait, 100*time.Millisecond)); err != nil {
			je.Outcome, je.Detail = CopySkipped, err.Error()
			return
		}
	}
	defer lock.Unlock()
	e := *je
	e.Outcome = OutSending
	id, err := m.journalRow(ctx, e)
	if err != nil {
		je.Outcome, je.Detail = CopyFailed, fmt.Errorf("%w: %w", errNotAnnounced, err).Error()
		return
	}
	je.ID = id
	added, err := ad.AddMagnet(ctx, mag, adder.AddOptions{OnlyIfCached: true})
	if err != nil {
		o, _ := failedAddOutcome(ctx, err, h)
		_ = router.RecordAddOutcome(ctx, m.Store, key, pc, m.now(), o)
		var ae *adder.Error
		if errors.As(err, &ae) && ae.Code == adder.CodeNotCached {
			// TorBox's add_only_if_cached: refused before anything was made.
			m.copyEvidence(ctx, h, key, false)
			je.Outcome, je.Detail = CopyNotCached, err.Error()
			return
		}
		busy(je, err, copyAddFailed+": ")
		return
	}
	je.TorrentID = added.ID
	verdict, t, why := router.AddCached, (*provider.Torrent)(nil), "accepted with only-if-cached"
	if pv.Kind != provider.KindTorBox {
		verdict, t, why = router.ReadBackAdd(ctx, rp, ad, pv.Kind, added, rs.CachedGrace, m.sleep)
	}
	_ = router.RecordAddOutcome(ctx, m.Store, key, pc, m.now(),
		router.AddOutcome{OK: true, Uncached: verdict == router.AddNotCached || verdict == router.AddNoVerdict})
	if verdict == router.AddCached {
		m.copyEvidence(ctx, h, key, true)
		name, size := added.Name, int64(0)
		if t != nil {
			name, size = t.Name, t.Bytes
		}
		if name == "" {
			name = je.Name
		}
		// TorBox's answer shows it accepted as cached, not its files present: recorded as still finishing.
		m.recordHeld(ctx, key, added.ID, h, name, size, t == nil && pv.Kind == provider.KindTorBox)
		je.Outcome, je.Detail = CopyAdded, ""
		return
	}
	switch verdict {
	case router.AddNotCached:
		m.copyEvidence(ctx, h, key, false)
		je.Outcome, je.Detail = CopyNotCached, why
	case router.AddNameRefused:
		je.Outcome, je.Detail = CopyRefused, why
	case router.AddNothingPlayable:
		je.Outcome, je.Detail = CopyFailed, "Real-Debrid cannot serve this file type"
	default:
		je.Outcome, je.Detail = CopyNotCached, "could not tell whether it is cached ("+why+")"
	}
	if err := m.deleteOwnCopy(ctx, pv.Kind, ad, added, h, before, t); err != nil {
		je.Outcome = CopyFailed
		je.Detail = fmt.Sprintf("not cached, and our add could not be removed from %s: %v; remove it on the "+
			"Torrents page", key, err)
	}
}

// Failure details the page words without the provider's own text (that stays in the journal).
const (
	copyReadFailed = "could not read the account"
	copyAddFailed  = "the add failed"
)

// busy ends je for err: skipped with the time ("until <time>: ...") when err is a back-off, hold, rate limit or
// pacing that says when the provider takes requests again (the gateway's or the provider's own), else failed with
// prefix and err's words.
func busy(je *JournalEntry, err error, prefix string) {
	var at time.Time
	var refused *gateway.Refused
	var ae *adder.Error
	switch {
	case errors.As(err, &refused):
		at = refused.RetryAt
	case errors.As(err, &ae):
		at = ae.RetryAt
	}
	if !at.IsZero() {
		je.Outcome, je.Detail = CopySkipped, "until "+at.UTC().Format(time.RFC3339)+": "+prefix+err.Error()
		return
	}
	je.Outcome, je.Detail = CopyFailed, prefix+err.Error()
}

// refusedName is the first of names Real-Debrid's name rule refuses, or "".
func refusedName(names []string) string {
	for _, n := range names {
		if indexer.RealDebridRefuses(n, false) {
			return n
		}
	}
	return ""
}

// copyPresent ends a provider that already has the hash: present, never touched. A complete entry goes into the record
// at once, so the file's provider tags show it; one that failed there gets no record row.
func (m *Manager) copyPresent(ctx context.Context, je *JournalEntry, t *provider.Torrent) {
	je.Outcome, je.TorrentID = CopyPresent, t.ID
	switch {
	case t.State == provider.StateError:
		je.Detail = "failed there"
	case t.State == provider.StateDownloaded:
		name := t.Name
		if name == "" {
			name = je.Name
		}
		m.recordHeld(ctx, je.Provider, t.ID, je.Hash, name, t.Bytes, false)
	default:
		je.Detail = fmt.Sprintf("still downloading (%.0f%%)", t.Progress)
	}
}

// recordHeld puts torrent id of hash in key's record, complete, as an import does (provider.RecordImport), or, when
// only accepted (unconfirmed), at 100% and still finishing (provider.RecordAccepted), and asks for the usage to be
// worked out again.
func (m *Manager) recordHeld(ctx context.Context, key, id, hash, name string, size int64, unconfirmed bool) {
	if err := m.Store.WriteTx(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		if unconfirmed {
			return provider.RecordAccepted(ctx, tx, key, id, hash, name, size)
		}
		return provider.RecordImport(ctx, tx, key, m.kind(key), id, hash, name, size)
	}); err != nil {
		m.log().Warn("torrents: copy: recording the torrent", "provider", key, "hash", hash, "err", err)
		return
	}
	if m.UsageStale != nil {
		m.UsageStale()
	}
}

// copyEvidence stores what the add showed in provider_cache, as a route's add does: cached for the cache evidence
// TTL (source add:<key>), uncached for miss_ttl.
func (m *Manager) copyEvidence(ctx context.Context, hash, key string, cached bool) {
	now := m.now()
	state, source, ttl := "cached", "add:"+key, m.Policy.CacheEvidenceSettings().TTL
	if !cached {
		state, source, ttl = "uncached", "router:add", m.Policy.RouterSettings().MissTTL
	}
	if err := router.RecordCacheEvidence(context.WithoutCancel(ctx), m.Store.Writer(), hash, key, state, source,
		now.Add(ttl), now); err != nil {
		m.log().Warn("torrents: copy: cache evidence", "provider", key, "hash", hash, "err", err)
	}
}

// deleteOwnCopy deletes our own add a again (adder.Deleter, by the *Added the add returned, within its window). The
// proof: the hash was not on the fresh list read before the add (before: its ids), the entry holds the hash, and on
// AllDebrid the returned id was not on that list (an upload of a hash already there hands back the existing magnet).
func (m *Manager) deleteOwnCopy(ctx context.Context, kind string, ad adder.Adder, a *adder.Added, hash string,
	before map[string]bool, t *provider.Torrent) error {
	del, ok := ad.(adder.Deleter)
	if !ok || (kind != provider.KindRealDebrid && kind != provider.KindAllDebrid) {
		return errors.New("this provider has no delete of our own adds")
	}
	if a.Hash != "" && !strings.EqualFold(a.Hash, hash) || t != nil && t.Hash != "" && !strings.EqualFold(t.Hash, hash) {
		return errors.New("the entry's hash is not the one added")
	}
	proof := adder.OwnAdd{NotOnAccountBefore: true, NewEntry: kind == provider.KindRealDebrid || !before[a.ID]}
	return del.DeleteOwnUncachedAdd(context.WithoutCancel(ctx), a, proof)
}

// CopyResult is one provider's newest copy outcome for a hash.
type CopyResult struct {
	Provider string `json:"provider"`
	// Code is the provider's cache code (rd, ad, tb), "" for a kind without one.
	Code    string `json:"code"`
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
	// Until is when a skipped provider takes adds again.
	Until *time.Time `json:"until,omitempty"`
	By    string     `json:"by,omitempty"`
	At    time.Time  `json:"at"`
}

// CopyState is a hash's copy: whether a copy job of it is queued or running, and each provider's newest outcome.
type CopyState struct {
	Running bool         `json:"running"`
	Results []CopyResult `json:"results"`
}

// CopyStatus reads hash's copy state.
func (m *Manager) CopyStatus(ctx context.Context, hash string) (*CopyState, error) {
	h := indexer.NormalizeHash(hash)
	out := &CopyState{Results: []CopyResult{}}
	db := m.Store.Reader()
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM job WHERE dedupe_key IN (?, ?)
		AND state IN ('queued', 'running'))`, copyDedupe(h, false), copyDedupe(h, true)).Scan(&out.Running); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT provider, outcome, detail, by, created_at FROM torrent_journal
		WHERE action = 'copy' AND info_hash = ? ORDER BY created_at DESC, id DESC`, h)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var r CopyResult
		var at int64
		if err := rows.Scan(&r.Provider, &r.Outcome, &r.Detail, &r.By, &at); err != nil {
			return nil, err
		}
		if seen[r.Provider] {
			continue
		}
		seen[r.Provider] = true
		r.At, r.Code = store.FromMillis(at), indexer.CacheCode(m.kind(r.Provider))
		if rest, ok := strings.CutPrefix(r.Detail, "until "); ok && r.Outcome == CopySkipped {
			if ts, why, ok := strings.Cut(rest, ": "); ok {
				if u, err := time.Parse(time.RFC3339, ts); err == nil {
					r.Until, r.Detail = &u, why
				}
			}
		}
		out.Results = append(out.Results, r)
	}
	return out, rows.Err()
}
