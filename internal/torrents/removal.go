package torrents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
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

// How long DeleteForRemoval waits for a copy to another provider that is adding or checking one of its hashes to
// settle, and how often it looks.
const (
	removalSettleWait = 10 * time.Minute
	removalSettlePoll = 5 * time.Second
)

// Reasons a removal clean-up keeps a torrent.
const (
	keptNotOurs    = "it was on the account before Theatarr added it"
	keptRestored   = "restored before the clean-up ran"
	keptStillAdded = "a copy to it is still being added"
)

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

// DeleteForRemoval deletes the torrents a removal left unused (docs/design/torrents.md "Removal clean-up"). It is
// conservative everywhere: keeping a torrent is always acceptable, deleting a wrong one never is.
//   - It waits (at most removalSettleWait) for any copy to another provider adding or checking one of the hashes to
//     settle, so a torrent that copy adds is listed below; one still unsettled then is kept.
//   - A hash of a removed library that was restored meanwhile (an Add) is kept; this is checked after the wait and
//     again right before each delete.
//   - Every configured provider is listed now; if any listing fails, nothing is deleted anywhere.
//   - Only an entry Theatarr itself added is taken (a download of ours that was not a reuse, or a copy of ours that
//     was not on the account before: own-add evidence for that provider and torrent id). An entry Theatarr reused, one
//     that was on the account before, or one whose ownership is unknown is kept.
//   - Each is planned as the Torrents page's delete plans it (PlanDelete: fresh usage from the reference loader, so
//     another title's or library's links, a download in hand, a kept file and the configured folders of other
//     libraries keep it), with the 24-hour guard lifted, and the still-downloading guard lifted for our own add (the
//     removal ended its download). A torrent whose usage is unknown only because the registry has not synced it yet
//     (a fresh add) is taken too; any other unknown is kept.
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
	unsettled, err := m.waitCopies(ctx, hashes)
	if err != nil {
		return nil, err
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
	owned, err := m.ownAdds(ctx)
	if err != nil {
		return nil, err
	}
	var sel []DeleteSelection
	for _, k := range m.keys() {
		if m.Providers[k] == nil {
			continue
		}
		list, err := m.list(ctx, k)
		if err != nil {
			out.Abort = "torrents not cleaned up: could not read " + m.code(k)
			return out, nil
		}
		for _, t := range list {
			h := indexer.NormalizeHash(t.Hash)
			switch {
			case !slices.Contains(hashes, h):
			case isRestored(h):
				out.add(h, "", "kept", keptRestored)
			case unsettled[h+"\x00"+k]:
				out.add(h, k, "kept", keptStillAdded)
			case !owned[k+"\x00"+t.ID]:
				out.add(h, k, "kept", keptNotOurs)
			default:
				sel = append(sel, DeleteSelection{Provider: k, ID: t.ID, Hash: h, removal: true, owned: true})
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
	plan, err := m.PlanDelete(ctx, sel)
	if err != nil {
		return nil, err
	}
	// A fresh add the registry does not know yet is unknown for that reason alone: it is ours, so it goes.
	again := false
	for i, it := range plan.Items {
		if it.NeedsOverride && it.Usage == Unknown && onlyNotInRegistry(it.Reasons) {
			sel[i].Override, sel[i].OverrideToken = true, it.ExpectedOverride
			again = true
		}
	}
	if again {
		if plan, err = m.PlanDelete(ctx, sel); err != nil {
			return nil, err
		}
	}
	if plan.Count > 0 {
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
		if errors.Is(err, ErrBackupFailed) {
			var ps []string
			for _, it := range plan.Items {
				if it.Action == ActDelete && !slices.Contains(ps, m.code(it.Provider)) {
					ps = append(ps, m.code(it.Provider))
				}
			}
			m.log().Warn("torrents: removal clean-up: backup failed", "err", err)
			out.Abort = "torrents not cleaned up: backup failed (" + strings.Join(ps, ", ") + ")"
			return out, nil
		}
		if err != nil {
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

// ownAdds are the provider entries Theatarr itself added, as "provider\x00torrent id": a download of ours that was
// not a reuse of an entry already on the account, and a copy to another provider that was not on the account before
// (own_add).
func (m *Manager) ownAdds(ctx context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	rows, err := m.Store.Reader().QueryContext(ctx, `SELECT provider, provider_torrent_id FROM download
			WHERE reused = 0 AND COALESCE(provider, '') <> '' AND COALESCE(provider_torrent_id, '') <> ''
		UNION SELECT provider, torrent_id FROM provider_copy WHERE own_add = 1 AND COALESCE(torrent_id, '') <> ''`)
	if err != nil {
		return nil, fmt.Errorf("torrents: own adds: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p, id sql.NullString
		if err := rows.Scan(&p, &id); err != nil {
			return nil, err
		}
		out[p.String+"\x00"+id.String] = true
	}
	return out, rows.Err()
}

// waitCopies waits for the copies to other providers adding or checking one of hashes to settle (router/copy.go),
// at most removalSettleWait; it returns those still unsettled then, as "hash\x00provider".
func (m *Manager) waitCopies(ctx context.Context, hashes []string) (map[string]bool, error) {
	args := make([]any, len(hashes))
	for i, h := range hashes {
		args[i] = h
	}
	deadline := m.now().Add(removalSettleWait)
	for {
		out := map[string]bool{}
		rows, err := m.Store.Reader().QueryContext(ctx, `SELECT info_hash, provider FROM provider_copy
			WHERE state IN ('adding', 'checking') AND info_hash IN (`+store.Placeholders(len(args))+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("torrents: copies being added: %w", err)
		}
		for rows.Next() {
			var h, p string
			if err := rows.Scan(&h, &p); err != nil {
				rows.Close()
				return nil, err
			}
			out[h+"\x00"+p] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(out) == 0 || !m.now().Before(deadline) {
			return out, nil
		}
		if err := m.sleep(ctx, removalSettlePoll); err != nil {
			return nil, err
		}
	}
}

func onlyNotInRegistry(reasons []string) bool {
	return len(reasons) > 0 && !slices.ContainsFunc(reasons, func(r string) bool { return r != reasonNotInRegistry })
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
