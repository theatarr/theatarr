package torrents

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/router"
)

// The folder hash (docs/design/torrents.md "Folder hash"; migration 00145): for an active library link that records
// no torrent hash (router.LinkStoredHashSQL gives none) and whose target is in a mount folder, link.folder_hash is
// the one hash of the record's torrents (every provider, every state) whose folders (torrentFolders) include that
// folder; NULL for none or several, and for every other link. A folder match is an inference, not proof, so only the
// web's display readers ("Cached on", the provider pills and filter) read it: removal, the router and the usage index
// never do. StoreUsage works it out after each provider sync, with the index it already built.

// folderHashChange is one link whose folder hash changes; hash "" writes NULL.
type folderHashChange struct {
	id   int64
	hash string
}

// folderInputsComplete reports whether every input of the inference was read: each configured provider listed
// completely, zurg's listing when zurg is configured and a link is known only by its folder (the index reads it then),
// and decypharr's index when the mount is decypharr's. Without them an unread account or index could hold a second
// hash for a folder.
func (m *Manager) folderInputsComplete(ix *refIndex) bool {
	for _, k := range m.keys() {
		if !ix.synced[k] {
			return false
		}
	}
	if ix.decyOn && ix.decy == nil {
		return false
	}
	return m.Zurg == nil || !ix.folderOnly || ix.zurg != nil
}

// folderHashChanges works out the folder hash of every link from ix and the record's torrents, and returns the links
// whose stored value differs. Nothing when the inputs are incomplete.
func (m *Manager) folderHashChanges(ctx context.Context, ix *refIndex, list []provider.Torrent) ([]folderHashChange,
	error) {
	if !m.folderInputsComplete(ix) {
		return nil, nil
	}
	byFolder := map[string]map[string]bool{}
	for _, t := range list {
		h := indexer.NormalizeHash(t.Hash)
		if h == "" {
			h = strings.ToLower(t.Hash)
		}
		if h == "" {
			continue
		}
		for _, f := range torrentFolders(ix.decy, ix.zurg, h, t) {
			if byFolder[f] == nil {
				byFolder[f] = map[string]bool{}
			}
			byFolder[f][h] = true
		}
	}
	mv := newMountView(m.Policy)
	rows, err := m.Store.Reader().QueryContext(ctx, `SELECT l.id, l.target, l.state = 'active',
		`+router.LinkStoredHashSQL+` IS NOT NULL, COALESCE(l.folder_hash, '') FROM link l
		WHERE l.state = 'active' OR l.folder_hash IS NOT NULL ORDER BY l.id`)
	if err != nil {
		return nil, fmt.Errorf("torrents: reading links for their folder hash: %w", err)
	}
	defer rows.Close()
	var out []folderHashChange
	for rows.Next() {
		var id int64
		var target, cur string
		var active, stored bool
		if err := rows.Scan(&id, &target, &active, &stored, &cur); err != nil {
			return nil, err
		}
		want := ""
		if active && !stored {
			if folder, _ := mv.folder(target); folder != "" && len(byFolder[folder]) == 1 {
				for h := range byFolder[folder] {
					want = h
				}
			}
		}
		if want != cur {
			out = append(out, folderHashChange{id, want})
		}
	}
	return out, rows.Err()
}

// writeFolderHashes writes the changed folder hashes in tx.
func writeFolderHashes(ctx context.Context, tx *sql.Tx, changes []folderHashChange) error {
	if len(changes) == 0 {
		return nil
	}
	up, err := tx.PrepareContext(ctx, `UPDATE link SET folder_hash = ? WHERE id = ?`)
	if err != nil {
		return err
	}
	defer up.Close()
	for _, c := range changes {
		var h any
		if c.hash != "" {
			h = c.hash
		}
		if _, err := up.ExecContext(ctx, h, c.id); err != nil {
			return err
		}
	}
	return nil
}
