package provider

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/store"
)

// Content states (content.state, migration 00006).
const (
	ContentUnknown      = "unknown"
	ContentPresent      = "present"
	ContentUnreferenced = "unreferenced"
	ContentAbsent       = "absent"
	ContentGone         = "gone"
)

// JobKind is the job queue kind of a registry sync. Its payload is a [SyncPayload]. Enqueue it with the dedupe key
// JobKind so only one sync is queued or running at a time.
const JobKind = "provider.sync"

// SyncPayload names the providers a sync job lists; empty means every provider the Syncer has that is not backing
// off (a scheduled sync). Retry marks a provider's own retry job (retry.go).
type SyncPayload struct {
	Providers []string `json:"providers,omitempty"`
	Retry     bool     `json:"retry,omitempty"`
}

// Syncer lists provider torrents and updates the content registry.
type Syncer struct {
	Store *store.Store
	// Providers are the clients that can be listed, by policy key.
	Providers map[string]Provider
	// Configured is every provider key in the policy, in fallback order. A content becomes absent only once every
	// one of them has had a complete listing, so a provider with no key configured keeps contents out of absent.
	Configured []string
	Log        *slog.Logger
	Now        func() time.Time
	// Queue takes the per-provider retry jobs Handle schedules; nil means a queue on Store.
	Queue *jobs.Queue
	// SettingsManaged fences registry writes with the settings provider epoch: a listing is applied only while the
	// stored epoch (settings_runtime_state.provider_epoch) still equals ProviderEpoch, the epoch of the runtime whose
	// client listed it. A listing that an account change overtook is discarded, and the next sync of the current
	// runtime lists again. A syncer built from YAML has no epoch.
	SettingsManaged bool
	ProviderEpoch   int64
}

// errStaleEpoch: the provider settings changed (the epoch moved) while a listing was in flight.
var errStaleEpoch = errors.New("provider settings changed during the listing; result discarded")

// checkEpoch refuses a registry write from a runtime whose provider epoch is no longer the stored one.
func (s *Syncer) checkEpoch(ctx context.Context, tx *sql.Tx) error {
	if !s.SettingsManaged {
		return nil
	}
	var epoch int64
	err := tx.QueryRowContext(ctx, "SELECT COALESCE(provider_epoch, 0) FROM settings_runtime_state WHERE id = 1").Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		epoch = 0
	} else if err != nil {
		return err
	}
	if epoch != s.ProviderEpoch {
		return errStaleEpoch
	}
	return nil
}

// SyncOptions select what Run does.
type SyncOptions struct {
	// Providers to list (policy keys); empty means all of s.Providers, in Configured order.
	Providers []string
	// DryRun lists and computes everything but rolls the database back, run rows included.
	DryRun bool
}

// ProviderSummary is one provider's part of a sync. It is stored as provider_sync_run.summary.
type ProviderSummary struct {
	Provider string `json:"provider"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	RunID    int64  `json:"run_id,omitempty"`
	ListMS   int64  `json:"list_ms"`
	// Torrents is the number of torrents listed; Hashes the distinct valid hashes among them.
	Torrents      int           `json:"torrents"`
	Hashes        int           `json:"hashes"`
	InvalidHashes int           `json:"invalid_hashes,omitempty"`
	Duplicates    int           `json:"duplicates,omitempty"` // extra torrents for a hash already listed
	ByState       map[State]int `json:"by_state"`
	// Registry changes for this provider.
	NewContents   int  `json:"new_contents"`   // provider torrents with no registry row before
	NamesFilled   int  `json:"names_filled"`   // contents that had no name or size
	NowPresent    int  `json:"now_present"`    // content_provider rows inserted or back from absent
	Updated       int  `json:"updated"`        // present rows whose torrent id, status, progress or size changed
	NowAbsent     int  `json:"now_absent"`     // present rows whose torrent left the list
	Unchanged     int  `json:"unchanged"`      // present rows with nothing new
	RowsUnchanged bool `json:"rows_unchanged"` // nothing in the registry changed (besides the run row)
	// Discarded: the provider settings changed while the list was read (Syncer.SettingsManaged); nothing was written,
	// not even a run row, and no retry is scheduled.
	Discarded bool `json:"discarded,omitempty"`
}

// SyncResult is a whole sync.
type SyncResult struct {
	DryRun    bool               `json:"dry_run"`
	Providers []*ProviderSummary `json:"providers"`
	// Transitions counts content state changes, "unknown->present" and so on; "new->..." for contents the sync
	// created.
	Transitions map[string]int `json:"transitions"`
	// Contents counts contents by state after the sync.
	Contents map[string]int `json:"contents"`
}

var errDryRun = errors.New("dry run: rolled back")

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Syncer) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Run lists the chosen providers one after another and applies each list in its own transaction. A provider whose
// list fails gets a failed run row and changes nothing else; the others go on. The returned error joins the failures.
func (s *Syncer) Run(ctx context.Context, o SyncOptions) (*SyncResult, error) {
	keys := o.Providers
	if len(keys) == 0 {
		for _, k := range s.Configured {
			if s.Providers[k] != nil {
				keys = append(keys, k)
			}
		}
	}
	res := &SyncResult{DryRun: o.DryRun, Transitions: map[string]int{}}
	var errs []error
	for _, k := range keys {
		p := s.Providers[k]
		if p == nil {
			errs = append(errs, fmt.Errorf("provider %q: no client (not in the policy, or its key is not set)", k))
			continue
		}
		sum, err := s.syncOne(ctx, k, p, o.DryRun, res.Transitions)
		res.Providers = append(res.Providers, sum)
		if err != nil {
			errs = append(errs, err)
		}
	}
	counts, err := ContentCounts(ctx, s.Store.Reader())
	if err != nil {
		errs = append(errs, err)
	}
	res.Contents = counts
	if o.DryRun {
		// The reader sees the committed state; show the would-be counts instead.
		res.Contents = applyTransitions(counts, res.Transitions)
	}
	return res, errors.Join(errs...)
}

func applyTransitions(counts map[string]int, tr map[string]int) map[string]int {
	out := map[string]int{}
	for k, v := range counts {
		out[k] = v
	}
	for t, n := range tr {
		from, to, _ := strings.Cut(t, "->")
		if from != "new" {
			out[from] -= n
		}
		out[to] += n
	}
	for k, v := range out {
		if v == 0 {
			delete(out, k)
		}
	}
	return out
}

func (s *Syncer) syncOne(ctx context.Context, key string, p Provider, dry bool, tr map[string]int) (*ProviderSummary, error) {
	log := s.log().With("component", "provider-sync", "provider", key)
	started := s.now()
	sum := &ProviderSummary{Provider: key, ByState: map[State]int{}}
	list, err := p.Torrents(ctx)
	sum.ListMS = s.now().Sub(started).Milliseconds()
	if err != nil {
		sum.Error = err.Error()
		log.Warn("provider list failed; registry unchanged for this provider", "err", err)
		if !dry {
			werr := s.Store.WriteTx(ctx, func(tx *sql.Tx) error {
				if err := s.checkEpoch(ctx, tx); err != nil {
					return err
				}
				_, err := insertRun(ctx, tx, sum, started, s.now())
				return err
			})
			if errors.Is(werr, errStaleEpoch) {
				return s.discarded(log, sum), nil
			}
			if werr != nil {
				return sum, errors.Join(err, werr)
			}
		}
		return sum, fmt.Errorf("provider %q: %w", key, err)
	}
	contents, err := preloadContents(ctx, s.Store.Reader(), list)
	if err != nil {
		sum.Error = err.Error()
		return sum, fmt.Errorf("provider %q: loading content: %w", key, err)
	}
	sum.OK = true
	localTr := map[string]int{}
	err = s.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		// WriteTx may retry: start from clean counters each time.
		*sum = ProviderSummary{Provider: key, OK: true, ListMS: sum.ListMS, ByState: map[State]int{}}
		clear(localTr)
		if err := s.checkEpoch(ctx, tx); err != nil {
			return err
		}
		now := store.Millis(s.now())
		created, err := applyList(ctx, tx, key, list, contents, now, sum)
		if err != nil {
			return err
		}
		if err := recompute(ctx, tx, s.Configured, key, created, localTr); err != nil {
			return err
		}
		if err := WriteRecord(ctx, tx, key, list, now); err != nil {
			return err
		}
		sum.RowsUnchanged = sum.NewContents+sum.NamesFilled+sum.NowPresent+sum.Updated+sum.NowAbsent == 0 &&
			len(localTr) == 0
		id, err := insertRun(ctx, tx, sum, started, s.now())
		if err != nil {
			return err
		}
		sum.RunID = id
		if dry {
			sum.RunID = 0
			return errDryRun
		}
		return nil
	})
	if errors.Is(err, errStaleEpoch) {
		return s.discarded(log, sum), nil
	}
	if err != nil && !errors.Is(err, errDryRun) {
		sum.OK, sum.Error = false, err.Error()
		return sum, fmt.Errorf("provider %q: registry update: %w", key, err)
	}
	for k, v := range localTr {
		tr[k] += v
	}
	log.Info("provider synced", "torrents", sum.Torrents, "new_contents", sum.NewContents,
		"now_present", sum.NowPresent, "updated", sum.Updated, "now_absent", sum.NowAbsent, "dry_run", dry)
	return sum, nil
}

// discarded is the summary of a listing an account change overtook (errStaleEpoch).
func (s *Syncer) discarded(log *slog.Logger, sum *ProviderSummary) *ProviderSummary {
	log.Info("provider settings changed during the listing; result discarded, the next sync lists again")
	return &ProviderSummary{Provider: sum.Provider, ListMS: sum.ListMS, ByState: map[State]int{}, Discarded: true,
		Error: errStaleEpoch.Error()}
}

func insertRun(ctx context.Context, tx *sql.Tx, sum *ProviderSummary, started, finished time.Time) (int64, error) {
	b, err := json.Marshal(sum)
	if err != nil {
		return 0, err
	}
	var errText any
	if sum.Error != "" {
		errText = sum.Error
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO provider_sync_run (provider, started_at, finished_at, ok, error, summary)
		VALUES (?, ?, ?, ?, ?, ?)`, sum.Provider, store.Millis(started), store.Millis(finished), boolInt(sum.OK),
		errText, string(b))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// pick chooses which torrent stands for a hash when the provider holds it more than once: a downloaded one first,
// then the oldest (the one the mount most likely shows), then the lowest id.
func better(a, b *Torrent) bool {
	if (a.State == StateDownloaded) != (b.State == StateDownloaded) {
		return a.State == StateDownloaded
	}
	if !a.Added.Equal(b.Added) {
		if a.Added.IsZero() || b.Added.IsZero() {
			return !a.Added.IsZero()
		}
		return a.Added.Before(b.Added)
	}
	return a.ID < b.ID
}

type cpRow struct {
	torrentID, folder, state, status sql.NullString
	progress                         sql.NullFloat64
	size, links, added               sql.NullInt64
	dups                             int
}

// applyList writes one provider's complete list into content and content_provider. Every write compares first.
// It returns the ids of the contents it created.
func applyList(ctx context.Context, tx *sql.Tx, key string, list []Torrent, contents map[string]contentRow, now int64, sum *ProviderSummary) (map[int64]bool, error) {
	sum.Torrents = len(list)
	primary := map[string]*Torrent{}
	dups := map[string]int{}
	for i := range list {
		t := &list[i]
		sum.ByState[t.State]++
		if _, ok := normHash(t.Hash); !ok {
			sum.InvalidHashes++
			continue
		}
		if cur := primary[t.Hash]; cur != nil {
			dups[t.Hash]++
			sum.Duplicates++
			if better(t, cur) {
				primary[t.Hash] = t
			}
			continue
		}
		primary[t.Hash] = t
	}
	sum.Hashes = len(primary)

	existing := map[int64]cpRow{}
	rows, err := tx.QueryContext(ctx, `SELECT content_id, torrent_id, folder_name, state, status, progress, size_bytes,
		links, added_at, duplicates FROM content_provider WHERE provider = ?`, key)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var r cpRow
		if err := rows.Scan(&id, &r.torrentID, &r.folder, &r.state, &r.status, &r.progress, &r.size, &r.links,
			&r.added, &r.dups); err != nil {
			rows.Close()
			return nil, err
		}
		existing[id] = r
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	hashes := make([]string, 0, len(primary))
	for h := range primary {
		hashes = append(hashes, h)
	}
	sort.Strings(hashes)
	listed := make(map[int64]bool, len(hashes))
	created := map[int64]bool{}
	for _, h := range hashes {
		t := primary[h]
		n := sum.NewContents
		cid, err := contentFor(ctx, tx, t, contents, now, sum)
		if err != nil {
			return nil, err
		}
		if sum.NewContents > n {
			created[cid] = true
		}
		listed[cid] = true
		var added any
		if !t.Added.IsZero() {
			added = store.Millis(t.Added)
		}
		var links any
		if t.Links >= 0 {
			links = t.Links
		}
		cur, ok := existing[cid]
		switch {
		case !ok:
			if _, err := tx.ExecContext(ctx, `INSERT INTO content_provider (content_id, provider, torrent_id, folder_name,
				seen_at, state, status, progress, size_bytes, links, duplicates, added_at)
				VALUES (?, ?, ?, ?, ?, 'present', ?, ?, ?, ?, ?, ?)`, cid, key, t.ID, t.Name, now, t.Status, t.Progress,
				t.Bytes, links, dups[h], added); err != nil {
				return nil, err
			}
			sum.NowPresent++
		case cur.state.String != "present":
			if _, err := tx.ExecContext(ctx, `UPDATE content_provider SET torrent_id = ?, folder_name = ?, seen_at = ?,
				state = 'present', status = ?, progress = ?, size_bytes = ?, links = ?, duplicates = ?, added_at = ?,
				absent_at = NULL WHERE content_id = ? AND provider = ?`, t.ID, t.Name, now, t.Status, t.Progress,
				t.Bytes, links, dups[h], added, cid, key); err != nil {
				return nil, err
			}
			sum.NowPresent++
		case cur.torrentID.String != t.ID || cur.folder.String != t.Name || cur.status.String != t.Status ||
			cur.progress.Float64 != t.Progress || !cur.progress.Valid || cur.size.Int64 != t.Bytes ||
			!eqNullInt(cur.links, links) || cur.dups != dups[h] || !eqNullInt(cur.added, added):
			if _, err := tx.ExecContext(ctx, `UPDATE content_provider SET torrent_id = ?, folder_name = ?, status = ?,
				progress = ?, size_bytes = ?, links = ?, duplicates = ?, added_at = ?
				WHERE content_id = ? AND provider = ?`, t.ID, t.Name, t.Status, t.Progress, t.Bytes, links, dups[h],
				added, cid, key); err != nil {
				return nil, err
			}
			sum.Updated++
		default:
			sum.Unchanged++
		}
	}
	for cid, r := range existing {
		if listed[cid] || r.state.String != "present" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE content_provider SET state = 'absent', absent_at = ?
			WHERE content_id = ? AND provider = ?`, now, cid, key); err != nil {
			return nil, err
		}
		sum.NowAbsent++
	}
	return created, nil
}

// WriteRecord replaces provider key's rows of the record (provider_torrent, migration 00130) with its complete list,
// every torrent as listed (two of one hash are two rows), and sets the time of that read. It runs in the sync's
// transaction, so a failed listing or a failed registry update leaves the previous record and its time.
func WriteRecord(ctx context.Context, tx *sql.Tx, key string, list []Torrent, now int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM provider_torrent WHERE provider = ?`, key); err != nil {
		return err
	}
	ins, err := tx.PrepareContext(ctx, `INSERT INTO provider_torrent (provider, torrent_id, info_hash, name,
		original_name, size_bytes, status, state, progress, added_at, files, links)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (provider, torrent_id) DO UPDATE SET info_hash = excluded.info_hash, name = excluded.name,
			original_name = excluded.original_name, size_bytes = excluded.size_bytes, status = excluded.status,
			state = excluded.state, progress = excluded.progress, added_at = excluded.added_at,
			files = excluded.files, links = excluded.links`)
	if err != nil {
		return err
	}
	defer ins.Close()
	for _, t := range list {
		var added any
		if !t.Added.IsZero() {
			added = store.Millis(t.Added)
		}
		files := -1
		if t.Files != nil {
			files = 0
			for _, f := range t.Files {
				if f.Selected {
					files++
				}
			}
		}
		orig := t.OriginalName
		if orig == "" {
			orig = OriginalName(t.Files, t.Name)
		}
		if _, err := ins.ExecContext(ctx, key, t.ID, strings.ToLower(t.Hash), t.Name, orig, t.Bytes, t.Status,
			string(t.State), t.Progress, added, files, t.Links); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO provider_torrent_read (provider, read_at) VALUES (?, ?)
		ON CONFLICT (provider) DO UPDATE SET read_at = excluded.read_at`, key, now)
	return err
}

// ClearRecord removes provider key's record and its read time (an account changed or removed by settings): the
// account reads "never read" until a sync lists it. The stored usage of its hashes goes too: another account still
// holding one reads it as not worked out until the usage is worked out again.
func ClearRecord(ctx context.Context, tx *sql.Tx, key string) error {
	for _, q := range []string{
		`DELETE FROM provider_hash_usage WHERE info_hash IN (SELECT info_hash FROM provider_torrent WHERE provider = ?)`,
		`DELETE FROM provider_torrent WHERE provider = ?`,
		`DELETE FROM provider_torrent_read WHERE provider = ?`} {
		if _, err := tx.ExecContext(ctx, q, key); err != nil {
			return err
		}
	}
	return nil
}

func eqNullInt(n sql.NullInt64, v any) bool {
	if v == nil {
		return !n.Valid
	}
	switch x := v.(type) {
	case int:
		return n.Valid && n.Int64 == int64(x)
	case int64:
		return n.Valid && n.Int64 == x
	}
	return false
}

// contentRow is immutable across WriteTx retries; only the reader preload owns this map.
type contentRow struct {
	id   int64
	name string
	size sql.NullInt64
}

// preloadContents reads the required hashes together before taking the single writer. Missing hashes are created
// in the transaction; a concurrent creator is resolved there, and fill-only updates recheck the stored fields.
func preloadContents(ctx context.Context, db *sql.DB, list []Torrent) (map[string]contentRow, error) {
	hashes := make([]string, 0, len(list))
	seen := map[string]bool{}
	for _, t := range list {
		if _, ok := normHash(t.Hash); ok && !seen[t.Hash] {
			hashes = append(hashes, t.Hash)
			seen[t.Hash] = true
		}
	}
	out := make(map[string]contentRow, len(hashes))
	if len(hashes) == 0 {
		return out, nil
	}
	b, err := json.Marshal(hashes)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT info_hash, id, name, size_bytes FROM content
		WHERE info_hash IN (SELECT value FROM json_each(?))`, string(b))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var hash string
		var c contentRow
		if err := rows.Scan(&hash, &c.id, &c.name, &c.size); err != nil {
			return nil, err
		}
		out[hash] = c
	}
	return out, rows.Err()
}

// contentFor uses the preloaded identity, or creates the content for t's hash. A concurrent insert after preload
// is the only path that needs a point lookup inside the writer. Existing names/sizes are never overwritten.
func contentFor(ctx context.Context, tx *sql.Tx, t *Torrent, contents map[string]contentRow, now int64, sum *ProviderSummary) (int64, error) {
	c, ok := contents[t.Hash]
	if !ok {
		first := now
		if !t.Added.IsZero() && store.Millis(t.Added) < now {
			first = store.Millis(t.Added)
		}
		var sz any
		if t.Bytes > 0 {
			sz = t.Bytes
		}
		err := tx.QueryRowContext(ctx, `INSERT INTO content (info_hash, name, size_bytes, state, first_seen_at,
			last_seen_at) VALUES (?, ?, ?, 'unknown', ?, ?) ON CONFLICT(info_hash) DO NOTHING RETURNING id`,
			t.Hash, t.Name, sz, first, now).Scan(&c.id)
		if err == nil {
			sum.NewContents++
			return c.id, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT id, name, size_bytes FROM content WHERE info_hash = ?`, t.Hash).
			Scan(&c.id, &c.name, &c.size); err != nil {
			return 0, err
		}
	}
	if c.name == "" && t.Name != "" || !c.size.Valid && t.Bytes > 0 {
		var sz any
		if t.Bytes > 0 {
			sz = t.Bytes
		}
		res, err := tx.ExecContext(ctx, `UPDATE content SET name = CASE WHEN name = '' THEN ? ELSE name END,
			size_bytes = COALESCE(size_bytes, ?) WHERE id = ? AND
			((name = '' AND ? != '') OR (size_bytes IS NULL AND ? > 0))`, t.Name, sz, c.id, t.Name, t.Bytes)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		sum.NamesFilled += int(n)
	}
	return c.id, nil
}

// recompute sets every content's state from content_provider and the references (docs/design/library.md). current is the provider
// being applied in this transaction, which counts as completely listed even though its run row is not written yet.
// Contents created in this transaction count their transition from "new".
func recompute(ctx context.Context, tx *sql.Tx, configured []string, current string, created map[int64]bool,
	tr map[string]int) error {
	allListed := true
	for _, k := range configured {
		if k == current {
			continue
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM provider_sync_run WHERE provider = ? AND ok = 1`, k).
			Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			allListed = false
		}
	}
	keys := configured
	if len(keys) == 0 {
		keys = []string{current}
	}
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	in := store.Placeholders(len(keys))
	rows, err := tx.QueryContext(ctx, `SELECT c.id, c.state,
		EXISTS (SELECT 1 FROM content_provider cp WHERE cp.content_id = c.id AND cp.state = 'present'
			AND cp.provider IN (`+in+`)),
		EXISTS (SELECT 1 FROM content_version cv WHERE cv.content_id = c.id)
			OR EXISTS (SELECT 1 FROM version_file vf WHERE vf.content_id = c.id)
			OR EXISTS (SELECT 1 FROM content_file f JOIN link l ON l.content_file_id = f.id
				WHERE f.content_id = c.id AND l.state != 'unlinked')
		FROM content c`, args...)
	if err != nil {
		return err
	}
	type change struct {
		id       int64
		from, to string
	}
	var changes []change
	for rows.Next() {
		var (
			id        int64
			state     string
			onP, refd bool
		)
		if err := rows.Scan(&id, &state, &onP, &refd); err != nil {
			rows.Close()
			return err
		}
		to := nextState(state, onP, refd, allListed)
		from := state
		if created[id] {
			from = "new"
		}
		if to != state || created[id] {
			changes = append(changes, change{id, from, to})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range changes {
		if _, err := tx.ExecContext(ctx, `UPDATE content SET state = ? WHERE id = ?`, c.to, c.id); err != nil {
			return err
		}
		tr[c.from+"->"+c.to]++
	}
	return nil
}

// nextState is the registry rule (docs/design/providers.md).
func nextState(cur string, onProvider, referenced, allListed bool) string {
	switch {
	case onProvider && referenced:
		return ContentPresent
	case onProvider:
		return ContentUnreferenced
	case cur == ContentGone:
		return ContentGone
	case allListed:
		return ContentAbsent
	}
	return ContentUnknown
}

// ContentCounts counts contents by state.
func ContentCounts(ctx context.Context, db *sql.DB) (map[string]int, error) {
	rows, err := db.QueryContext(ctx, `SELECT state, count(*) FROM content GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}

// Handle runs a sync job (kind JobKind). One provider failing is a partial success (retry.go):
//   - a scheduled sync (no providers in the payload) lists the providers that are not backing off;
//   - a provider whose list fails gets its own retry job at its next attempt, so retries never list the healthy
//     providers again;
//   - the job succeeds when at least one provider synced, or none was due. When every provider it listed failed it
//     fails, and the queue retries it; by then those providers are backing off, so the retry only finishes it;
//   - a retry job lists its provider unless a sync since has listed it completely, and never fails: the next retry
//     is scheduled instead.
func (s *Syncer) Handle(ctx context.Context, j *jobs.Job) error {
	var p SyncPayload
	if len(j.Payload) > 0 {
		if err := j.Decode(&p); err != nil {
			return jobs.Permanent(fmt.Errorf("provider sync: bad payload: %w", err))
		}
	}
	for _, k := range p.Providers {
		if s.Providers[k] == nil {
			return jobs.Permanent(fmt.Errorf("provider sync: unknown provider %q", k))
		}
	}
	keys := p.Providers
	switch {
	case len(keys) == 0:
		now := s.now()
		for _, k := range s.Configured {
			if s.Providers[k] == nil {
				continue
			}
			rs, err := ReadRetryState(ctx, s.Store.Reader(), k)
			if err != nil {
				return err
			}
			if rs.RetryAt.After(now) {
				s.log().Info("provider backing off; not listed in this sync", "provider", k, "failures", rs.Failures,
					"retry_at", rs.RetryAt.UTC())
				continue
			}
			keys = append(keys, k)
		}
		if len(keys) == 0 {
			return nil
		}
	case p.Retry:
		var due []string
		for _, k := range keys {
			rs, err := ReadRetryState(ctx, s.Store.Reader(), k)
			if err != nil {
				return err
			}
			if rs.Failures > 0 {
				due = append(due, k)
			}
		}
		if keys = due; len(keys) == 0 {
			return nil // listed completely since the retry was scheduled
		}
	}
	res, runErr := s.Run(ctx, SyncOptions{Providers: keys})
	synced := 0
	var errs []error
	for _, ps := range res.Providers {
		if ps.OK {
			synced++
			continue
		}
		if ps.Discarded {
			continue // the current runtime's next sync lists it again; no retry, no back-off
		}
		if err := s.scheduleRetry(ctx, ps.Provider); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(append(errs, runErr)...)
	}
	if runErr != nil && synced == 0 && !p.Retry {
		return runErr
	}
	if runErr != nil {
		s.log().Warn("provider sync partly failed", "synced", synced, "failed", len(res.Providers)-synced, "err", runErr)
	}
	return nil
}

// Refresh lists every provider the Syncer has now, back-off or not (the Torrents page's Refresh), and waits for it:
// the same sync as a job, each provider in its own transaction, and a provider whose list fails gets its retry job
// as in Handle. The returned error joins the providers' failures.
func (s *Syncer) Refresh(ctx context.Context) (*SyncResult, error) {
	res, runErr := s.Run(ctx, SyncOptions{})
	var errs []error
	for _, ps := range res.Providers {
		if ps.OK || ps.Discarded {
			continue
		}
		if err := s.scheduleRetry(ctx, ps.Provider); err != nil {
			errs = append(errs, err)
		}
	}
	return res, errors.Join(append(errs, runErr)...)
}

// RegisterJobs registers the sync handler on a worker.
func (s *Syncer) RegisterJobs(w *jobs.Worker) { w.Handle(JobKind, s.Handle) }
