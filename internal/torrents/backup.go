package torrents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/provider"
)

// Backups are JSON files in Manager.Dir (0700), each 0600: what was on every configured provider when it was taken,
// enough to put it back. Debrid Media Manager's backup (an array of {"filename", "hash"}) is read and written too.

// BackupFormat names Theatarr's backup format in its "format" field.
const BackupFormat = "theatarr-torrents-backup"

// BackupVersion is the format's version. Version 2 records an unknown selection (SelectionUnknown);
// in version 1 an empty known selection may be one captured while Real-Debrid still awaited the selection, so it is
// read as unknown (normalize).
const BackupVersion = 2

// Backup reasons.
const (
	ReasonManual    = "manual"
	ReasonScheduled = "scheduled"
	ReasonPreDelete = "pre-delete"
	ReasonImport    = "import"
)

// keepRecent: backups younger than this are kept whatever torrents.backup.keep says.
const keepRecent = 7 * 24 * time.Hour

// detailBudget bounds the per-torrent reads a backup makes per provider for torrents whose list has no files (RD and
// AD lists); files already known from an earlier backup are reused, so each backup fills in more.
const detailBudget = 50

// Backup is one backup file.
type Backup struct {
	Format    string           `json:"format"`
	Version   int              `json:"version"`
	ID        string           `json:"id"`
	CreatedAt time.Time        `json:"created_at"`
	Reason    string           `json:"reason"`
	By        string           `json:"by,omitempty"`
	Providers []BackupProvider `json:"providers"`
}

// BackupProvider is one account's list. OK is false when it could not be read (Error says why).
type BackupProvider struct {
	Provider string          `json:"provider"`
	Kind     string          `json:"kind,omitempty"`
	OK       bool            `json:"ok"`
	Error    string          `json:"error,omitempty"`
	Torrents []BackupTorrent `json:"torrents"`
}

// BackupTorrent is one torrent. Files are the selected files when FilesKnown: the provider had settled the selection
// (an empty list is a known empty selection). SelectionUnknown says the files may be known but the selection is not:
// Real-Debrid still awaited it when the backup read the torrent (or a version 1 backup holds an empty list). Neither
// set: the backup never read the torrent's files.
type BackupTorrent struct {
	ID         string       `json:"id,omitempty"`
	Hash       string       `json:"hash"`
	Name       string       `json:"name"`
	Bytes      int64        `json:"bytes,omitempty"`
	Added      time.Time    `json:"added,omitzero"`
	Status     string       `json:"status,omitempty"`
	FilesKnown bool         `json:"files_known"`
	Files      []BackupFile `json:"files,omitempty"`
	// SelectionUnknown: see above. A restore never selects every file for it.
	SelectionUnknown bool `json:"selection_unknown,omitempty"`
}

// selectionKnown says whether the backup knows the torrent's selection state: the selected files, or that the
// provider still awaited a selection when it was read (a pre-delete backup must know one or the other).
func (t *BackupTorrent) selectionKnown() bool { return t.FilesKnown || t.SelectionUnknown }

// rdConverting is Real-Debrid's status while it converts a magnet, before it lists the files.
const rdConverting = "magnet_conversion"

// awaitingSelection says whether a torrent's provider status is Real-Debrid's wait for a file selection (or the
// magnet conversion before it): its files carry no selection yet.
func awaitingSelection(status string) bool { return status == rdWaitingFiles || status == rdConverting }

// BackupFile is one selected file.
type BackupFile struct {
	ID    string `json:"id,omitempty"`
	Path  string `json:"path"`
	Bytes int64  `json:"bytes,omitempty"`
}

// BackupInfo summarises a backup for the list.
type BackupInfo struct {
	ID        string            `json:"id"`
	CreatedAt time.Time         `json:"created_at"`
	Reason    string            `json:"reason"`
	By        string            `json:"by,omitempty"`
	Torrents  int               `json:"torrents"`
	Bytes     int64             `json:"bytes"`
	FileSize  int64             `json:"file_size"`
	Providers []BackupProvCount `json:"providers"`
}

// BackupProvCount is one provider in a BackupInfo.
type BackupProvCount struct {
	Provider string `json:"provider"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	Torrents int    `json:"torrents"`
	Bytes    int64  `json:"bytes"`
}

// DMMEntry is one entry of Debrid Media Manager's backup.
type DMMEntry struct {
	Filename string `json:"filename"`
	Hash     string `json:"hash"`
}

var backupID = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}\.[0-9]{3}Z-(manual|scheduled|pre-delete|import)$`)

// ErrNoBackup: no backup with that id.
var ErrNoBackup = errors.New("torrents: no such backup")

func (b *Backup) info(size int64) BackupInfo {
	in := BackupInfo{ID: b.ID, CreatedAt: b.CreatedAt, Reason: b.Reason, By: b.By, FileSize: size,
		Providers: []BackupProvCount{}}
	for _, p := range b.Providers {
		c := BackupProvCount{Provider: p.Provider, OK: p.OK, Error: p.Error, Torrents: len(p.Torrents)}
		for _, t := range p.Torrents {
			c.Bytes += t.Bytes
		}
		in.Torrents += c.Torrents
		in.Bytes += c.Bytes
		in.Providers = append(in.Providers, c)
	}
	return in
}

// BackupOptions tune one backup.
type BackupOptions struct {
	Reason string
	By     string
	// Lists are lists already read (a delete's fresh lists); the other providers are listed now.
	Lists map[string][]provider.Torrent
	// Need are torrents (provider -> ids) whose files must be in the backup: their detail is read now whatever the
	// budget and whatever an earlier backup holds (a selection can be made since), and the backup fails
	// when it cannot be.
	Need map[string]map[string]bool
}

// TakeBackup lists every configured provider and writes a backup. A provider that cannot be listed is in it with its
// error; the caller decides whether that is enough (a delete needs its own providers). It prunes old backups after.
func (m *Manager) TakeBackup(ctx context.Context, o BackupOptions) (*Backup, BackupInfo, error) {
	if o.Reason == "" {
		o.Reason = ReasonManual
	}
	prev := m.fileCache()
	now := m.now().UTC()
	b := &Backup{Format: BackupFormat, Version: BackupVersion, ID: now.Format("20060102T150405.000Z") + "-" + o.Reason,
		CreatedAt: now, Reason: o.Reason, By: o.By}
	for _, k := range m.keys() {
		bp := BackupProvider{Provider: k, Kind: m.kind(k), Torrents: []BackupTorrent{}}
		list, ok := o.Lists[k]
		var err error
		if !ok {
			list, err = m.list(ctx, k)
		}
		if err != nil {
			bp.Error = err.Error()
			b.Providers = append(b.Providers, bp)
			continue
		}
		budget := detailBudget
		for _, t := range list {
			bt := BackupTorrent{ID: t.ID, Hash: strings.ToLower(t.Hash), Name: t.Name, Bytes: t.Bytes, Added: t.Added,
				Status: t.Status}
			files, status := t.Files, t.Status
			if files == nil {
				need := o.Need[k][t.ID]
				if c, ok := prev[k+"\x00"+t.ID+"\x00"+bt.Hash]; ok && !need && !awaitingSelection(t.Status) {
					bt.FilesKnown, bt.Files = true, c
				} else if need || budget > 0 {
					if !need {
						budget--
					}
					full, err := m.Providers[k].Torrent(ctx, t.ID)
					switch {
					case err == nil && full.Files != nil:
						files, status = full.Files, full.Status
					case need:
						return nil, BackupInfo{}, fmt.Errorf("torrents: backup: reading the files of %s %s: %v", k,
							t.ID, err)
					}
				}
			}
			if files != nil && awaitingSelection(status) {
				// The provider awaits a selection: no file is selected yet, and the selection made later is not
				// this one. Not cached by later backups (fileCache takes FilesKnown only).
				bt.SelectionUnknown = true
			} else if files != nil {
				bt.FilesKnown = true
				for _, f := range files {
					if f.Selected {
						bt.Files = append(bt.Files, BackupFile{ID: f.ID, Path: f.Path, Bytes: f.Bytes})
					}
				}
			}
			bp.Torrents = append(bp.Torrents, bt)
		}
		bp.OK = true
		b.Providers = append(b.Providers, bp)
	}
	info, err := m.write(b)
	if err != nil {
		return nil, BackupInfo{}, err
	}
	m.prune(ctx, b.ID)
	return b, info, nil
}

// fileCache is the files of every torrent in the newest backup that knew them, by provider, id and hash: a settled
// selection does not change, so a backup reads each torrent's detail once. An unknown selection (one Real-Debrid still
// awaited, or a version 1 backup's empty list) is not cached; a delete's backup reads its torrents again anyway.
func (m *Manager) fileCache() map[string][]BackupFile {
	out := map[string][]BackupFile{}
	list, err := m.Backups()
	if err != nil {
		return out
	}
	for _, in := range list {
		b, err := m.ReadBackup(in.ID)
		if err != nil || b.Reason == ReasonImport {
			continue
		}
		for _, p := range b.Providers {
			for _, t := range p.Torrents {
				k := p.Provider + "\x00" + t.ID + "\x00" + t.Hash
				if _, seen := out[k]; !seen && t.FilesKnown && t.ID != "" {
					out[k] = t.Files
				}
			}
		}
		if len(out) > 0 {
			break // the newest backup with files is enough; older ones only repeat it
		}
	}
	return out
}

func (m *Manager) write(b *Backup) (BackupInfo, error) {
	if m.Dir == "" {
		return BackupInfo{}, errors.New("torrents: no backup directory")
	}
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		return BackupInfo{}, err
	}
	if err := os.Chmod(m.Dir, 0o700); err != nil {
		return BackupInfo{}, err
	}
	data, err := json.MarshalIndent(b, "", " ")
	if err != nil {
		return BackupInfo{}, err
	}
	path := filepath.Join(m.Dir, b.ID+".json")
	if _, err := os.Lstat(path); err == nil {
		return BackupInfo{}, fmt.Errorf("torrents: backup %s exists", b.ID)
	}
	tmp, err := os.CreateTemp(m.Dir, ".backup-*")
	if err != nil {
		return BackupInfo{}, err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return BackupInfo{}, err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return BackupInfo{}, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return BackupInfo{}, err
	}
	if err := tmp.Close(); err != nil {
		return BackupInfo{}, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return BackupInfo{}, err
	}
	return b.info(int64(len(data))), nil
}

// Backups lists the backups, newest first, from their summaries (summary): a backup file is decoded once per
// process, as a lightweight summary without its torrents' files. ReadBackup reads and validates a whole one.
func (m *Manager) Backups() ([]BackupInfo, error) {
	out := []BackupInfo{}
	if m.Dir == "" {
		return out, nil
	}
	entries, err := os.ReadDir(m.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !backupID.MatchString(id) || !e.Type().IsRegular() {
			continue
		}
		in, err := m.summary(id, e)
		if err != nil {
			continue
		}
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// summaries caches each backup file's BackupInfo by path, with the size and modification time it was read at: a
// backup is written once (atomically) and never changed, so a listing decodes only new files.
var summaries sync.Map

type cachedSummary struct {
	size int64
	mod  time.Time
	info BackupInfo
}

// backupSummary is the part of a backup file its listing needs.
type backupSummary struct {
	Format    string    `json:"format"`
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Reason    string    `json:"reason"`
	By        string    `json:"by,omitempty"`
	Providers []struct {
		Provider string `json:"provider"`
		OK       bool   `json:"ok"`
		Error    string `json:"error,omitempty"`
		Torrents []struct {
			Bytes int64 `json:"bytes,omitempty"`
		} `json:"torrents"`
	} `json:"providers"`
}

func (m *Manager) summary(id string, e os.DirEntry) (BackupInfo, error) {
	path := filepath.Join(m.Dir, id+".json")
	fi, err := e.Info()
	if err != nil {
		return BackupInfo{}, err
	}
	if c, ok := summaries.Load(path); ok {
		if c := c.(cachedSummary); c.size == fi.Size() && c.mod.Equal(fi.ModTime()) {
			return c.info, nil
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return BackupInfo{}, err
	}
	var b backupSummary
	if err := json.Unmarshal(data, &b); err != nil || b.Format != BackupFormat || b.ID != id {
		return BackupInfo{}, fmt.Errorf("torrents: backup %s is unreadable", id)
	}
	in := BackupInfo{ID: b.ID, CreatedAt: b.CreatedAt, Reason: b.Reason, By: b.By, FileSize: int64(len(data)),
		Providers: []BackupProvCount{}}
	for _, p := range b.Providers {
		c := BackupProvCount{Provider: p.Provider, OK: p.OK, Error: p.Error, Torrents: len(p.Torrents)}
		for _, t := range p.Torrents {
			c.Bytes += t.Bytes
		}
		in.Torrents += c.Torrents
		in.Bytes += c.Bytes
		in.Providers = append(in.Providers, c)
	}
	summaries.Store(path, cachedSummary{size: fi.Size(), mod: fi.ModTime(), info: in})
	return in, nil
}

// ReadBackup reads backup id whole and validates it (a restore and a delete's file cache use it).
func (m *Manager) ReadBackup(id string) (*Backup, error) {
	b, _, err := m.readBackup(id)
	return b, err
}

func (m *Manager) readBackup(id string) (*Backup, int64, error) {
	if !backupID.MatchString(id) || m.Dir == "" {
		return nil, 0, ErrNoBackup
	}
	data, err := os.ReadFile(filepath.Join(m.Dir, id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, ErrNoBackup
	}
	if err != nil {
		return nil, 0, err
	}
	var b Backup
	if err := json.Unmarshal(data, &b); err != nil || b.validate(id) != nil {
		return nil, 0, fmt.Errorf("torrents: backup %s is unreadable", id)
	}
	b.normalize()
	return &b, int64(len(data)), nil
}

// validate checks a backup read from file id: the format and a version this build reads, the id it was written as,
// and a provider and a hash on every list and torrent.
func (b *Backup) validate(id string) error {
	if b.Format != BackupFormat || b.Version < 1 || b.Version > BackupVersion || b.ID != id {
		return errors.New("not this backup")
	}
	for _, p := range b.Providers {
		if p.Provider == "" {
			return errors.New("a list names no provider")
		}
		for _, t := range p.Torrents {
			if t.Hash == "" {
				return errors.New("a torrent has no hash")
			}
		}
	}
	return nil
}

// normalize reads a version 1 backup's empty known selection as unknown: it may have been captured while
// Real-Debrid still awaited the selection.
func (b *Backup) normalize() {
	if b.Version >= 2 {
		return
	}
	for i := range b.Providers {
		for j := range b.Providers[i].Torrents {
			if t := &b.Providers[i].Torrents[j]; t.FilesKnown && len(t.Files) == 0 {
				t.FilesKnown, t.SelectionUnknown = false, true
			}
		}
	}
}

// prune keeps the newest torrents.backup.keep backups and every one from the last week, and never keep.
func (m *Manager) prune(ctx context.Context, keep string) {
	list, err := m.Backups()
	if err != nil {
		return
	}
	n := m.Policy.TorrentBackupKeep()
	now := m.now()
	for i, in := range list {
		if i < n || in.ID == keep || now.Sub(in.CreatedAt) < keepRecent {
			continue
		}
		path := filepath.Join(m.Dir, in.ID+".json")
		if err := os.Remove(path); err != nil {
			m.log().Warn("torrents: pruning a backup", "backup", in.ID, "err", err)
			continue
		}
		summaries.Delete(path)
	}
	_ = ctx
}

// DMM is b as Debrid Media Manager's backup: one entry per hash (of provider only, when set).
func (b *Backup) DMM(provider string) []DMMEntry {
	out := []DMMEntry{}
	seen := map[string]bool{}
	for _, p := range b.Providers {
		if provider != "" && p.Provider != provider {
			continue
		}
		for _, t := range p.Torrents {
			if t.Hash == "" || seen[t.Hash] {
				continue
			}
			seen[t.Hash] = true
			out = append(out, DMMEntry{Filename: t.Name, Hash: t.Hash})
		}
	}
	return out
}

// maxImport bounds an uploaded backup.
const maxImport = 64 << 20

// ImportBackup stores an uploaded backup: Theatarr's own (kept as it is, its providers renamed to target when set) or
// DMM's (its entries go to target, which is then required). It is stored as a backup of reason import.
func (m *Manager) ImportBackup(ctx context.Context, data []byte, target, by string) (*Backup, BackupInfo, error) {
	if len(data) > maxImport {
		return nil, BackupInfo{}, fmt.Errorf("torrents: the backup is over %d MiB", maxImport>>20)
	}
	if target != "" && m.Policy.Providers[target] == nil {
		return nil, BackupInfo{}, fmt.Errorf("torrents: provider %q is not in the policy", target)
	}
	now := m.now().UTC()
	b := &Backup{Format: BackupFormat, Version: BackupVersion, ID: now.Format("20060102T150405.000Z") + "-" + ReasonImport,
		CreatedAt: now, Reason: ReasonImport, By: by}
	trimmed := bytes.TrimSpace(data)
	switch {
	case len(trimmed) > 0 && trimmed[0] == '[':
		var dmm []DMMEntry
		if err := json.Unmarshal(trimmed, &dmm); err != nil {
			return nil, BackupInfo{}, errors.New("torrents: not a Debrid Media Manager backup (an array of filename and hash)")
		}
		if target == "" {
			return nil, BackupInfo{}, errors.New("torrents: a Debrid Media Manager backup needs the provider to restore to")
		}
		bp := BackupProvider{Provider: target, Kind: m.kind(target), OK: true, Torrents: []BackupTorrent{}}
		seen := map[string]bool{}
		for _, e := range dmm {
			h := indexer.NormalizeHash(e.Hash)
			if h == "" {
				h = strings.ToLower(strings.TrimSpace(e.Hash))
			}
			if seen[h] {
				continue
			}
			seen[h] = true
			bp.Torrents = append(bp.Torrents, BackupTorrent{Hash: h, Name: e.Filename})
		}
		b.Providers = []BackupProvider{bp}
	default:
		var in Backup
		if err := json.Unmarshal(trimmed, &in); err != nil || in.Format != BackupFormat || in.Version < 1 ||
			in.Version > BackupVersion {
			return nil, BackupInfo{}, errors.New("torrents: not a Theatarr torrent backup or a Debrid Media Manager backup")
		}
		in.normalize()
		for _, p := range in.Providers {
			if target != "" {
				p.Provider, p.Kind = target, m.kind(target)
			}
			for i := range p.Torrents {
				p.Torrents[i].Hash = strings.ToLower(p.Torrents[i].Hash)
			}
			b.Providers = append(b.Providers, p)
		}
	}
	info, err := m.write(b)
	if err != nil {
		return nil, BackupInfo{}, err
	}
	m.journal(ctx, JournalEntry{Action: "backup", BackupID: b.ID, Outcome: "ok", By: by,
		Detail: fmt.Sprintf("imported: %d torrents", info.Torrents)})
	m.prune(ctx, b.ID)
	return b, info, nil
}
