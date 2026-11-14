package torrents

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/store"
)

// RemovalCleanup is the debrid clean-up after a removal that deleted the files (internal/removal): the hashes of the
// removed copies' torrents (their links that were unlinked, their arr files and their downloads), less any hash a
// link that was kept or failed to unlink still names.
type RemovalCleanup struct {
	Hashes []string
	By     string
}

// How long DeleteForRemoval waits for a copy to another provider that is adding or checking one of its hashes to
// settle, and how often it looks.
const (
	removalSettleWait = 10 * time.Minute
	removalSettlePoll = 5 * time.Second
)

// DeleteForRemoval deletes the torrents a removal left unused (docs/design/torrents.md "Removal clean-up"), and
// returns what it did per provider in words for the Removed event ("deleted on RD; kept on AD: used by <title>";
// "TB: failed, <reason>"). The candidates are worked out live, after the removal:
//   - it first waits (at most removalSettleWait) for any copy to another provider adding or checking one of the
//     hashes to settle, so a torrent that copy adds is listed below; one still unsettled then is kept;
//   - it lists every configured provider now and takes each torrent of a candidate hash (a fresh add, a copy on
//     another provider, an older entry Theatarr reused: all as the Torrents page's delete would take them);
//   - each is planned as that delete plans it (PlanDelete: fresh usage from the reference loader, so another
//     title's or library's links, a download in hand, a kept file and the configured folders of other libraries
//     keep it), with the 24-hour guard lifted for these torrents only; a torrent whose usage is unknown only because
//     the registry has not synced it yet (a fresh add) is taken too, any other unknown is kept;
//   - then it runs exactly as that delete runs: the full backup first (no backup, no delete: "torrents not cleaned
//     up: backup failed"), and each delete under the reference fence and the add lock after its last usage check.
//
// Nothing is retried: a torrent kept or failed stays for Torrents > Unused.
func (m *Manager) DeleteForRemoval(ctx context.Context, c RemovalCleanup) (string, error) {
	var hashes []string
	for _, h := range c.Hashes {
		if h = indexer.NormalizeHash(h); h != "" && !slices.Contains(hashes, h) {
			hashes = append(hashes, h)
		}
	}
	if len(hashes) == 0 {
		return "no torrent to clean up", nil
	}
	unsettled, err := m.waitCopies(ctx, hashes)
	if err != nil {
		return "", err
	}
	opMu.Lock()
	defer opMu.Unlock()
	out := removalWords{m: m}
	var sel []DeleteSelection
	for _, k := range m.keys() {
		if m.Providers[k] == nil {
			continue
		}
		list, err := m.list(ctx, k)
		if err != nil {
			out.fail(k, "could not be listed: "+err.Error())
			continue
		}
		for _, t := range list {
			h := indexer.NormalizeHash(t.Hash)
			if !slices.Contains(hashes, h) {
				continue
			}
			if unsettled[h+"\x00"+k] {
				out.keep(k, "a copy to it is still being added")
				continue
			}
			sel = append(sel, DeleteSelection{Provider: k, ID: t.ID, Hash: h, AllowRecent: true})
		}
	}
	if len(sel) == 0 {
		return out.words(), nil
	}
	if len(sel) > MaxDelete {
		sel = sel[:MaxDelete]
	}
	plan, err := m.PlanDelete(ctx, sel)
	if err != nil {
		return "", err
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
			return "", err
		}
	}
	if plan.Count > 0 {
		plan, err = m.execute(ctx, plan, c.By)
		if errors.Is(err, ErrBackupFailed) {
			var ps []string
			for _, it := range plan.Items {
				if it.Action == ActDelete && !slices.Contains(ps, m.code(it.Provider)) {
					ps = append(ps, m.code(it.Provider))
				}
			}
			return "torrents not cleaned up: backup failed (" + strings.Join(ps, ", ") + ")", nil
		}
		if err != nil {
			return "", err
		}
	}
	for _, it := range plan.Items {
		switch {
		case it.Action != ActDelete:
			out.keep(it.Provider, refusalWords(it))
		case it.Outcome == "deleted":
			out.deleted(it.Provider)
		case it.Outcome == OutFailed:
			out.fail(it.Provider, it.Error)
		default:
			out.keep(it.Provider, it.Error)
		}
	}
	return out.words(), nil
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

// removalWords gathers a clean-up's outcome per provider: the providers it deleted on, and the distinct reasons it
// kept or failed torrents on each (at most maxRemovalReasons, then how many more).
type removalWords struct {
	m      *Manager
	del    []string
	kept   map[string][]string
	failed map[string][]string
}

const maxRemovalReasons = 3

func (w *removalWords) deleted(key string) {
	if c := w.m.code(key); !slices.Contains(w.del, c) {
		w.del = append(w.del, c)
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

func (w *removalWords) keep(key, why string) { addReason(&w.kept, w.m.code(key), why) }

func (w *removalWords) fail(key, why string) { addReason(&w.failed, w.m.code(key), why) }

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
		parts = append(parts, "kept on "+c+": "+reasonList(w.kept[c]))
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
