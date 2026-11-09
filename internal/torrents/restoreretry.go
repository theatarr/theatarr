package torrents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/router"
)

// Real-Debrid caches a torrent per selected file set (docs/design/router.md "File selection on Real-Debrid"). A restore
// of a backup's known selection tries that exact set first: it was cached when the backup was taken. When it comes
// back uncached, a strict subset of the playable files stays selected: it may be a deliberate partial pack. A set
// whose playable part is such a subset but that holds other files too (episodes with their subtitles, a movie with
// its nfo: files Real-Debrid packs into a RAR) is added once more with that playable part alone, still the pack's
// part the backup held. For other sets, the restore's own add is deleted and the torrent added once
// more with every playable file (router.Playable), the set Debrid Media Manager, decypharr and zurg select and so
// the one most likely cached. One retry at most: the second add stays whatever it shows. A torrent whose backup never read the files (SelAll)
// selects every playable file at once and has no other set to try.

// mayRetry reports whether it's uncached add may be given up for every playable file: a known selection, on a
// Real-Debrid target whose adder can delete its own add and whose files can be read back.
func (m *Manager) mayRetry(it *RestoreItem) bool {
	pv := m.Policy.Providers[it.Target]
	_, del := m.Adders[it.Target].(adder.Deleter)
	return it.Selection == SelFiles && pv != nil && pv.Kind == provider.KindRealDebrid && del &&
		m.Providers[it.Target] != nil
}

// retryPlayable deletes added, the restore's own uncached add of it whose files t lists, and readies it for one
// more add with the known set's playable part when that is a strict subset of the playable files, else with every
// playable file; it reports whether to send that add. Nothing is deleted when the known set is a subset of the
// playable files (including equality), when the provider is paced past restoreWait (the add
// would not go, and the torrent would be lost), when anything else here knows the torrent id, or when the run
// stopped for the provider.
// The first add's journal row is completed here; je is reset for the second.
func (m *Manager) retryPlayable(ctx context.Context, it *RestoreItem, added *adder.Added, t *provider.Torrent,
	stopped map[string]string, je *JournalEntry) bool {
	if t == nil || stopped[it.Target] != "" || ctx.Err() != nil || !m.mayRetry(it) {
		return false
	}
	all := router.PlayableIDs(t.Files)
	selected := pickFiles(t.Files, it.files, false)
	if len(all) == 0 || router.SameFiles(all, selected) {
		return false
	}
	playable := make(map[string]bool, len(all))
	for _, id := range all {
		playable[id] = true
	}
	var part []string // the known set's playable part
	for _, id := range selected {
		if playable[id] {
			part = append(part, id)
		}
	}
	partial := len(part) > 0 && len(part) < len(all)
	if partial && len(part) == len(selected) {
		return false
	}
	pv := m.Policy.Providers[it.Target]
	if until, _, err := router.PacedUntil(ctx, m.Store, it.Target, pv.EffectivePacing(), m.now()); err != nil ||
		until.Sub(m.now()) > restoreWait {
		return false
	}
	if known, err := m.knownTorrent(ctx, it.Target, added.ID); err != nil || known {
		return false
	}
	// Real-Debrid makes a new torrent for every addMagnet, and the plan made at the start of this run found the
	// hash not on the target (ActReadd): the entry is the restore's own new one.
	del := m.Adders[it.Target].(adder.Deleter)
	if err := del.DeleteOwnUncachedAdd(ctx, added, adder.OwnAdd{NotOnAccountBefore: true, NewEntry: true}); err != nil {
		it.Error = fmt.Sprintf("the backup's file selection is not cached on %s; the add stays (deleting it to try "+
			"every playable file failed: %v)", it.Target, err)
		return false
	}
	m.forgetPending(ctx, []pendingKey{{it.Target, added.ID}})
	first := *je
	first.TorrentID, first.Outcome = added.ID, OutUncached
	if partial {
		first.Detail = fmt.Sprintf("the backup's %d selected files were not cached; this add was deleted and the "+
			"torrent added again with their playable part alone (%d files)", len(it.files), len(part))
	} else {
		first.Detail = fmt.Sprintf("the backup's %d selected files were not cached; this add was deleted and the "+
			"torrent added again with every playable file (%d)", len(it.files), len(all))
	}
	m.journal(ctx, first)
	je.ID, je.TorrentID, je.Outcome, je.Detail = 0, "", "", ""
	if partial {
		it.files = playableFiles(t.Files, it.files, part)
	} else {
		it.Selection, it.files = SelAll, nil
	}
	it.NewID, it.Outcome, it.Error = "", "", ""
	return true
}

// knownTorrent reports whether the registry or a download of the router knows torrent id on target.
func (m *Manager) knownTorrent(ctx context.Context, target, id string) (bool, error) {
	var n int
	err := m.Store.Writer().QueryRowContext(ctx, `SELECT 1 FROM content_provider WHERE provider = ? AND torrent_id = ?
		UNION ALL SELECT 1 FROM download WHERE provider = ? AND provider_torrent_id = ? LIMIT 1`,
		target, id, target, id).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// playableFiles is the part of want, a backup's known selection, whose files in files are among ids.
func playableFiles(files []provider.File, want []BackupFile, ids []string) []BackupFile {
	keep := make(map[string]bool, len(ids))
	for _, id := range ids {
		keep[id] = true
	}
	byPath := map[string]string{}
	for _, f := range files {
		byPath[strings.TrimPrefix(f.Path, "/")] = f.ID
	}
	var out []BackupFile
	for _, w := range want {
		if keep[byPath[strings.TrimPrefix(w.Path, "/")]] {
			out = append(out, w)
		}
	}
	return out
}
