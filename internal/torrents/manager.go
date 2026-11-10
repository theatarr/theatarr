package torrents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/store"
)

// Manager is the torrent manager for one settings generation: its policy, its provider clients and adders.
type Manager struct {
	Store  *store.Store
	Policy *policy.Policy
	// Providers are the read clients of the providers whose key is set; Adders their write clients.
	Providers map[string]provider.Provider
	Adders    map[string]adder.Adder
	// Dir holds the backups (<data dir>/torrent-backups).
	Dir string
	// Queue runs restores and backups as jobs (EnqueueBackup, EnqueueRestore); nil runs nothing in the background.
	Queue *jobs.Queue
	Log   *slog.Logger
	Now   func() time.Time
	// Sleep waits for pacing; nil sleeps for real (tests replace it).
	Sleep func(ctx context.Context, d time.Duration) error
	// MountPaths is the debrid mount and its aliases (policy.MountPaths); nil reads them from the policy. A scan root
	// that is or leads into one is refused without touching it.
	MountPaths []string
	// Decypharr is the library's decypharr client when the mount is decypharr's (library.mount.decypharr): a
	// torrent's folder there is the name decypharr lists its hash under. Nil with the block set: every torrent
	// without references is unknown.
	Decypharr *library.Decypharr
	// Zurg is the library's client of zurg's API (library.mount.zurg_api): the name zurg gives a hash names its
	// folder, so a folder-name reference under it counts. It only adds references; nil, a failed listing or a hash it
	// does not list leave the usage as without it.
	Zurg *library.ZurgAPI
	// AddLock is the router's add lock (router.Router.AddLock): a restore's add is sent under it, so it never lands
	// between a route's look at the account and its own add, and a delete's per-item usage checks and its deletes
	// run under it (local reference writers are kept out by reffence). nil (the CLI) relies on the journal row alone.
	AddLock sync.Locker
}

// opMu keeps restores and deletes from running at the same time in this process: each reads a provider's list and
// then writes to it.
var opMu sync.Mutex

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) log() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.Default()
}

func (m *Manager) sleep(ctx context.Context, d time.Duration) error {
	if m.Sleep != nil {
		return m.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// keys lists the policy's providers in fallback order, set or not.
func (m *Manager) keys() []string {
	var out []string
	for k := range m.Policy.Providers {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := m.Policy.Providers[out[i]], m.Policy.Providers[out[j]]
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		return out[i] < out[j]
	})
	return out
}

func (m *Manager) kind(key string) string {
	if pv := m.Policy.Providers[key]; pv != nil {
		return pv.Kind
	}
	return ""
}

// ErrNoClient: the provider is not in the policy or its key is not set.
var ErrNoClient = errors.New("torrents: provider not configured or its key is not set")

// list reads provider key's torrents completely.
func (m *Manager) list(ctx context.Context, key string) ([]provider.Torrent, error) {
	p := m.Providers[key]
	if p == nil {
		return nil, fmt.Errorf("%s: %w", key, ErrNoClient)
	}
	return p.Torrents(ctx)
}

// JournalEntry is one torrent_journal row.
type JournalEntry struct {
	ID        int64     `json:"id"`
	Action    string    `json:"action"`
	Provider  string    `json:"provider,omitempty"`
	TorrentID string    `json:"torrent_id,omitempty"`
	Hash      string    `json:"hash,omitempty"`
	Name      string    `json:"name,omitempty"`
	Bytes     int64     `json:"bytes,omitempty"`
	BackupID  string    `json:"backup_id,omitempty"`
	Outcome   string    `json:"outcome"`
	Detail    string    `json:"detail,omitempty"`
	By        string    `json:"by,omitempty"`
	At        time.Time `json:"at"`
}

func (m *Manager) journal(ctx context.Context, e JournalEntry) {
	if _, err := m.journalRow(ctx, e); err != nil {
		m.log().Error("torrents: journal", "action", e.Action, "hash", e.Hash, "err", err)
	}
}

// journalRow writes e, or with e.ID set updates that row's outcome, torrent id and detail (a restore's "sending" row
// once the add answered). It returns the row's id.
func (m *Manager) journalRow(ctx context.Context, e JournalEntry) (int64, error) {
	if e.At.IsZero() {
		e.At = m.now()
	}
	err := m.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		if e.ID != 0 {
			_, err := tx.ExecContext(ctx, `UPDATE torrent_journal SET torrent_id = ?, outcome = ?, detail = ?
				WHERE id = ?`, e.TorrentID, e.Outcome, e.Detail, e.ID)
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO torrent_journal (action, provider, torrent_id, info_hash, name,
			size_bytes, backup_id, outcome, detail, by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.Action, e.Provider, e.TorrentID, e.Hash, e.Name, e.Bytes, e.BackupID, e.Outcome, e.Detail, e.By,
			store.Millis(e.At))
		if err != nil {
			return err
		}
		e.ID, err = res.LastInsertId()
		return err
	})
	return e.ID, err
}

// Journal lists the newest torrent_journal rows, newest first.
func (m *Manager) Journal(ctx context.Context, limit int) ([]JournalEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := m.Store.Reader().QueryContext(ctx, `SELECT id, action, provider, torrent_id, info_hash, name,
		COALESCE(size_bytes, 0), backup_id, outcome, detail, by, created_at FROM torrent_journal
		ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []JournalEntry{}
	for rows.Next() {
		var e JournalEntry
		var at int64
		if err := rows.Scan(&e.ID, &e.Action, &e.Provider, &e.TorrentID, &e.Hash, &e.Name, &e.Bytes, &e.BackupID,
			&e.Outcome, &e.Detail, &e.By, &at); err != nil {
			return nil, err
		}
		e.At = store.FromMillis(at)
		out = append(out, e)
	}
	return out, rows.Err()
}
