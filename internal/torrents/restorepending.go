package torrents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/store"
)

// Restore selections (RestoreItem.Selection): how a provider that waits for a file selection (Real-Debrid) gets one.
const (
	// SelFiles: the backup's selected files, matched by path to the new torrent's files.
	SelFiles = "files"
	// SelAll: the backup never read the torrent's files (an import from Debrid Media Manager, or the detail budget):
	// every file is selected, as the preview says.
	SelAll = "all"
	// SelUnknown: the backup knows the files but not the selection (Real-Debrid still awaited it, a version 1
	// backup's empty list, or an empty selection): never taken as every file. The torrent is added and left
	// waiting for the person's selection (outcome incomplete).
	SelUnknown = "unknown"
)

// selectionOf is how a restore selects t's files.
func selectionOf(t BackupTorrent) string {
	switch {
	case t.FilesKnown && len(t.Files) > 0:
		return SelFiles
	case t.FilesKnown || t.SelectionUnknown:
		return SelUnknown
	default:
		return SelAll
	}
}

// pendingKey names a torrent on a provider.
type pendingKey struct{ provider, id string }

// pendingSel is one torrent_restore_pending row: a restore's add whose file selection did not finish.
type pendingSel struct {
	key       pendingKey
	hash      string
	selection string
	files     []BackupFile
}

type pendingList []pendingSel

// find is the pending selection of hash on target, the newest when there are several.
func (l pendingList) find(target, hash string) *pendingSel {
	for i := len(l) - 1; i >= 0; i-- {
		if l[i].key.provider == target && l[i].hash == hash {
			return &l[i]
		}
	}
	return nil
}

func (m *Manager) pendingSelections(ctx context.Context) (pendingList, error) {
	rows, err := m.Store.Reader().QueryContext(ctx, `SELECT provider, torrent_id, lower(info_hash), selection, files
		FROM torrent_restore_pending ORDER BY created_at, provider, torrent_id`)
	if err != nil {
		return nil, fmt.Errorf("torrents: reading pending selections: %w", err)
	}
	defer rows.Close()
	var out pendingList
	for rows.Next() {
		var p pendingSel
		var files string
		if err := rows.Scan(&p.key.provider, &p.key.id, &p.hash, &p.selection, &files); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(files), &p.files); err != nil {
			return nil, fmt.Errorf("torrents: pending selection of %s %s: %w", p.key.provider, p.key.id, err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// savePending records it (just added as it.NewID) as awaiting its selection, before the selection is tried.
func (m *Manager) savePending(ctx context.Context, it *RestoreItem, backup string) error {
	files, err := json.Marshal(nonNilFiles(it.files))
	if err != nil {
		return err
	}
	now := store.Millis(m.now())
	return m.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO torrent_restore_pending (provider, torrent_id, info_hash, backup_id,
			files, selection, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (provider, torrent_id) DO UPDATE SET updated_at = excluded.updated_at`,
			it.Target, it.NewID, it.Hash, backup, string(files), it.Selection, now, now)
		return err
	})
}

func nonNilFiles(f []BackupFile) []BackupFile {
	if f == nil {
		return []BackupFile{}
	}
	return f
}

// notePending records why a pending selection failed again.
func (m *Manager) notePending(ctx context.Context, k pendingKey, why string) {
	if err := m.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE torrent_restore_pending SET error = ?, updated_at = ?
			WHERE provider = ? AND torrent_id = ?`, why, store.Millis(m.now()), k.provider, k.id)
		return err
	}); err != nil {
		m.log().Warn("torrents: noting a pending selection", "provider", k.provider, "torrent", k.id, "err", err)
	}
}

// forgetPending removes pending selections: done, or of torrents the provider no longer lists.
func (m *Manager) forgetPending(ctx context.Context, keys []pendingKey) {
	if len(keys) == 0 {
		return
	}
	if err := m.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, k := range keys {
			if _, err := tx.ExecContext(ctx, `DELETE FROM torrent_restore_pending WHERE provider = ? AND torrent_id = ?`,
				k.provider, k.id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		m.log().Warn("torrents: forgetting pending selections", "count", len(keys), "err", err)
	}
}

// selectNew selects the files of it, just added as it.NewID on a provider that waits for a selection.
// The add is provably the restore's own torrent (the provider answered with its id), so it is recorded as pending
// first; a failed detail read or selection leaves it pending, and a restore again resumes it (ActResume). An
// unknown selection, or one none of whose files is in the torrent, is left to the person. incomplete says the
// provider still waits for the selection.
func (m *Manager) selectNew(ctx context.Context, ad adder.Adder, it *RestoreItem) (provider.State, bool) {
	if it.Selection == SelUnknown {
		it.Error = fmt.Sprintf("added as %s, but the backup does not know its file selection (the provider awaited "+
			"one when it was backed up); select its files on %s's site", it.NewID, it.Target)
		return "", true
	}
	k := pendingKey{it.Target, it.NewID}
	saveErr := m.savePending(ctx, it, it.backup)
	state, err := m.selectRestored(ctx, ad, it)
	switch {
	case err == nil:
		m.forgetPending(ctx, []pendingKey{k})
		return state, false
	case errors.Is(err, errNoMatch) || errors.Is(err, errNoPlayable) || saveErr != nil:
		m.forgetPending(ctx, []pendingKey{k})
		it.Error = fmt.Sprintf("added as %s, but selecting files failed: %v; select its files on %s's site", it.NewID,
			err, it.Target)
	default:
		m.notePending(ctx, k, err.Error())
		it.Error = fmt.Sprintf("added as %s, but selecting files failed: %v; restore it again to resume the selection",
			it.NewID, err)
	}
	return state, true
}

// resumeOne finishes the file selection of a torrent an earlier restore added (ActResume): it.NewID, it.files and
// it.Selection come from its pending row. A torrent selected meanwhile (on the provider's site) is done too.
func (m *Manager) resumeOne(ctx context.Context, it *RestoreItem) {
	ad := m.Adders[it.Target]
	if ad == nil {
		it.Outcome, it.Error = OutFailed, ErrNoClient.Error()
		return
	}
	k := pendingKey{it.Target, it.NewID}
	state, err := m.selectRestored(ctx, ad, it)
	switch {
	case err == nil:
		m.forgetPending(ctx, []pendingKey{k})
		it.Outcome, it.Error = OutUncached, "file selection resumed"
		if state == provider.StateDownloaded {
			it.Outcome = OutAdded
		}
	case errors.Is(err, errNoMatch) || errors.Is(err, errNoPlayable):
		m.forgetPending(ctx, []pendingKey{k})
		it.Outcome = OutIncomplete
		it.Error = fmt.Sprintf("%s: %v; select its files on %s's site", it.NewID, err, it.Target)
	default:
		m.notePending(ctx, k, err.Error())
		it.Outcome = OutIncomplete
		it.Error = fmt.Sprintf("%s: resuming the file selection failed: %v; restore it again to resume it", it.NewID,
			err)
	}
}
