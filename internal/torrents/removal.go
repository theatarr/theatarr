package torrents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/store"
)

// RemovalCleanup is the debrid clean-up after a removal that deleted the files (internal/removal): the removal's
// title, and the hashes of the removed copies' torrents (their links that were unlinked, their arr files and their
// downloads), each with the removed libraries (policy keys) it belongs to, less any hash a link that was kept or
// failed to unlink still names.
type RemovalCleanup struct {
	TitleID int64
	Hashes  map[string][]string
	By      string
}

// keptRestored is why a removal clean-up keeps the torrents of a library restored meanwhile.
const keptRestored = "restored before the clean-up ran"

// RemovalOutcome is what DeleteForRemoval did. Abort, when set, says why nothing was deleted on any provider
// ("torrents not cleaned up: ..."); otherwise each hash has its results per provider (Words).
type RemovalOutcome struct {
	Abort   string
	m       *Manager
	results map[string][]removalResult
}

type removalResult struct {
	code, kind, why string // kind: deleted, kept, failed
}

func (o *RemovalOutcome) add(hash, key, kind, why string) {
	if o.results == nil {
		o.results = map[string][]removalResult{}
	}
	code := ""
	if key != "" {
		code = o.m.code(key)
	}
	o.results[hash] = append(o.results[hash], removalResult{code: code, kind: kind, why: why})
}

// Words is the outcome for the torrents of hashes (one removed library's) in words: "deleted on RD, TB; kept on AD:
// used by <title>"; "TB: failed, <reason>"; the abort's words when it aborted.
func (o *RemovalOutcome) Words(hashes []string) string {
	if o.Abort != "" {
		return o.Abort
	}
	w := removalWords{}
	for _, h := range hashes {
		for _, r := range o.results[indexer.NormalizeHash(h)] {
			switch r.kind {
			case "deleted":
				w.deleted(r.code)
			case "failed":
				w.fail(r.code, r.why)
			default:
				w.keep(r.code, r.why)
			}
		}
	}
	return w.words()
}

// DeleteForRemoval deletes the torrents a removal left unused (docs/design/torrents.md "Removal clean-up"). The
// removal with the files deleted is the person's explicit request, so every entry on every provider that holds a
// candidate hash is taken, whoever added it; the guard is Theatarr's own usage only.
//   - A hash of a removed library restored meanwhile (an Add) is kept; this is checked after the read and again right
//     before each delete, under the fence.
//   - The accounts are read as Torrents › Refresh reads them (the provider sync writes the record and the registry,
//     then the usage is worked out); if any provider's listing fails, nothing is deleted anywhere. Those lists are
//     the only ones read: planning and the backup use them.
//   - Each entry is planned by the normal plan rules (PlanDelete: fresh usage; a torrent another title or library,
//     a download in hand, a kept file or a configured other library's folder uses is kept, and so is one whose usage
//     is unknown); the 24-hour and still-downloading guards are lifted (allow_recent): the removal ended its download.
//   - Then it runs as that delete runs: the full backup first, which must hold every provider's list (no backup, no
//     delete), and each delete under the reference fence and the add lock after its last usage check.
//
// At most MaxDelete torrents go per removal; the rest are kept and said so. Nothing is retried.
func (m *Manager) DeleteForRemoval(ctx context.Context, c RemovalCleanup) (*RemovalOutcome, error) {
	out := &RemovalOutcome{m: m}
	keysOf := map[string][]string{}
	var hashes []string
	for h, keys := range c.Hashes {
		if h = indexer.NormalizeHash(h); h != "" {
			hashes = append(hashes, h)
			keysOf[h] = append(keysOf[h], keys...)
		}
	}
	sort.Strings(hashes)
	if len(hashes) == 0 {
		return out, nil
	}
	if m.Syncer == nil {
		out.Abort = "torrents not cleaned up: " + ErrNoSync.Error()
		return out, nil
	}
	opMu.Lock()
	defer opMu.Unlock()
	restored, err := m.restoredKeys(ctx, c.TitleID, keysOf)
	if err != nil {
		return nil, err
	}
	isRestored := func(h string) bool {
		return slices.ContainsFunc(keysOf[h], func(k string) bool { return restored[k] })
	}
	// One read of every account, as Refresh does it.
	res, err := m.Syncer.Refresh(ctx)
	if res == nil {
		return nil, err
	}
	for _, ps := range res.Providers {
		if !ps.OK || ps.Discarded {
			out.Abort = "torrents not cleaned up: could not read " + m.code(ps.Provider)
			return out, nil
		}
	}
	for _, k := range m.keys() {
		if m.Providers[k] != nil && !slices.ContainsFunc(res.Providers, func(ps *provider.ProviderSummary) bool {
			return ps.Provider == k
		}) {
			out.Abort = "torrents not cleaned up: could not read " + m.code(k)
			return out, nil
		}
	}
	if err := m.StoreUsage(ctx); err != nil {
		return nil, err
	}
	lists, err := m.recordLists(ctx)
	if err != nil {
		return nil, err
	}
	var sel []DeleteSelection
	for _, k := range m.keys() {
		for _, t := range lists[k] {
			h := indexer.NormalizeHash(t.Hash)
			switch {
			case !slices.Contains(hashes, h):
			case isRestored(h):
				out.add(h, "", "kept", keptRestored)
			default:
				sel = append(sel, DeleteSelection{Provider: k, ID: t.ID, Hash: h, AllowRecent: true})
			}
		}
	}
	if len(sel) > MaxDelete {
		for _, s := range sel[MaxDelete:] {
			out.add(s.Hash, s.Provider, "kept", fmt.Sprintf("not cleaned up: more than %d torrents in one removal; "+
				"see Torrents › Unused", MaxDelete))
		}
		sel = sel[:MaxDelete]
	}
	if len(sel) == 0 {
		return out, nil
	}
	plan, err := m.planDelete(ctx, sel, lists)
	if err != nil {
		return nil, err
	}
	if plan.Count > 0 {
		plan.lists = lists // the backup holds every account, from the same read
		veto := func(ctx context.Context, it *DeleteItem) string {
			r, err := m.restoredKeys(ctx, c.TitleID, map[string][]string{it.Hash: keysOf[it.Hash]})
			switch {
			case err != nil:
				return "checking the removal: " + err.Error()
			case len(r) > 0:
				return keptRestored
			}
			return ""
		}
		plan, err = m.execute(ctx, plan, c.By, executeOptions{allProviders: true, veto: veto})
		var pe *BackupProviderError
		switch {
		case errors.As(err, &pe):
			out.Abort = "torrents not cleaned up: backup failed (" + m.code(pe.Provider) + ")"
			return out, nil
		case errors.Is(err, ErrBackupFailed):
			out.Abort = "torrents not cleaned up: backup failed"
			return out, nil
		case err != nil:
			return nil, err
		}
	}
	for _, it := range plan.Items {
		switch {
		case it.Action != ActDelete:
			out.add(it.Hash, it.Provider, "kept", refusalWords(it))
		case it.Outcome == "deleted":
			out.add(it.Hash, it.Provider, "deleted", "")
		case it.Outcome == OutFailed:
			out.add(it.Hash, it.Provider, "failed", it.Error)
		default:
			out.add(it.Hash, it.Provider, "kept", it.Error)
		}
	}
	return out, nil
}

// recordLists are every account's torrents as the record holds them (provider_torrent), just written by the sync.
func (m *Manager) recordLists(ctx context.Context) (map[string][]provider.Torrent, error) {
	out := map[string][]provider.Torrent{}
	for _, k := range m.keys() {
		if m.Providers[k] != nil {
			out[k] = []provider.Torrent{}
		}
	}
	rows, err := m.Store.Reader().QueryContext(ctx, `SELECT provider, torrent_id, info_hash, name, original_name,
		size_bytes, status, state, progress, added_at FROM provider_torrent ORDER BY provider, torrent_id`)
	if err != nil {
		return nil, fmt.Errorf("torrents: reading the record: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, state string
		var t provider.Torrent
		var added sql.NullInt64
		if err := rows.Scan(&k, &t.ID, &t.Hash, &t.Name, &t.OriginalName, &t.Bytes, &t.Status, &state, &t.Progress,
			&added); err != nil {
			return nil, err
		}
		t.State = provider.State(state)
		if added.Valid {
			t.Added = store.FromMillis(added.Int64)
		}
		if _, ok := out[k]; ok {
			out[k] = append(out[k], t)
		}
	}
	return out, rows.Err()
}

// restoredKeys are the libraries (policy keys) among keysOf's of title that are in the library again: their version
// is no longer listed as removed (an Add restored it).
func (m *Manager) restoredKeys(ctx context.Context, titleID int64, keysOf map[string][]string) (map[string]bool, error) {
	var keys []any
	for _, ks := range keysOf {
		for _, k := range ks {
			if !slices.Contains(keys, any(k)) {
				keys = append(keys, k)
			}
		}
	}
	out := map[string]bool{}
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := m.Store.Reader().QueryContext(ctx, `SELECT v.policy_key FROM version v WHERE v.title_id = ?
		AND v.policy_key IN (`+store.Placeholders(len(keys))+`)
		AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id)`,
		append([]any{titleID}, keys...)...)
	if err != nil {
		return nil, fmt.Errorf("torrents: restored libraries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// refusalWords says why a clean-up kept a torrent: the titles that use it, else the plan's refusal.
func refusalWords(it DeleteItem) string {
	if it.Usage == Used {
		var names []string
		for _, r := range it.RefList {
			n := r.Title
			if n == "" {
				n = r.Detail
			}
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
		if len(names) > 0 {
			return "used by " + strings.Join(names, ", ")
		}
	}
	if it.Usage == Unknown && len(it.Reasons) > 0 {
		return "usage unknown: " + it.Reasons[0]
	}
	return it.Refusal
}

// code is the provider's short name in the words (RD, AD, TB), else its key.
func (m *Manager) code(key string) string {
	if c := indexer.CacheCode(m.kind(key)); c != "" {
		return strings.ToUpper(c)
	}
	return key
}

// removalWords gathers a clean-up's outcome per provider code ("" for one that is not a provider's): the providers it
// deleted on, and the distinct reasons it kept or failed torrents on each (at most maxRemovalReasons, then how many
// more).
type removalWords struct {
	del    []string
	kept   map[string][]string
	failed map[string][]string
}

const maxRemovalReasons = 3

func (w *removalWords) deleted(code string) {
	if !slices.Contains(w.del, code) {
		w.del = append(w.del, code)
	}
}

func addReason(m *map[string][]string, code, why string) {
	if *m == nil {
		*m = map[string][]string{}
	}
	if !slices.Contains((*m)[code], why) {
		(*m)[code] = append((*m)[code], why)
	}
}

func (w *removalWords) keep(code, why string) { addReason(&w.kept, code, why) }

func (w *removalWords) fail(code, why string) { addReason(&w.failed, code, why) }

func reasonList(rs []string) string {
	if len(rs) > maxRemovalReasons {
		return strings.Join(rs[:maxRemovalReasons], "; ") + fmt.Sprintf(" (and %d more)", len(rs)-maxRemovalReasons)
	}
	return strings.Join(rs, "; ")
}

func (w *removalWords) words() string {
	var parts []string
	if len(w.del) > 0 {
		parts = append(parts, "deleted on "+strings.Join(w.del, ", "))
	}
	for _, c := range sortedKeys(w.kept) {
		on := "kept on " + c
		if c == "" {
			on = "kept"
		}
		parts = append(parts, on+": "+reasonList(w.kept[c]))
	}
	for _, c := range sortedKeys(w.failed) {
		parts = append(parts, c+": failed, "+reasonList(w.failed[c]))
	}
	if len(parts) == 0 {
		return "no torrent of it on the providers"
	}
	return strings.Join(parts, "; ")
}

func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
