package policy

import (
	"path/filepath"
	"time"
)

// Torrents configures the provider torrent manager (internal/torrents, docs/design/torrents.md).
type Torrents struct {
	// ScanRoots lists library roots other tools manage. The usage check walks them for symlinks into the debrid mount
	// (library.mount), reading only the link targets, never the mount, so a torrent such a library uses is not
	// offered as unused. A root that cannot be read makes every otherwise unused torrent unknown.
	ScanRoots []string `yaml:"scan_roots"`
	// Backup schedules backups of the provider torrent lists. Off unless backup.every is set.
	Backup TorrentBackup `yaml:"backup"`
}

// TorrentBackup schedules the torrent list backups.
type TorrentBackup struct {
	// Every is the interval of scheduled backups; 0 (the default) is off. Minimum 1h.
	Every Duration `yaml:"every"`
	// Keep is how many backups are kept (scheduled, by hand and before a delete alike); those from the last week are
	// kept besides. Default 30.
	Keep int `yaml:"keep"`
}

// Torrent backup limits.
const (
	DefaultTorrentBackupKeep = 30
	MaxTorrentBackupKeep     = 1000
	MinTorrentBackupEvery    = time.Hour
)

// TorrentScanRoots is torrents.scan_roots, or nil.
func (p *Policy) TorrentScanRoots() []string {
	if p == nil || p.Torrents == nil {
		return nil
	}
	return p.Torrents.ScanRoots
}

// TorrentBackupEvery is torrents.backup.every; 0 is off.
func (p *Policy) TorrentBackupEvery() time.Duration {
	if p == nil || p.Torrents == nil {
		return 0
	}
	return p.Torrents.Backup.Every.D()
}

// TorrentBackupKeep is torrents.backup.keep, or the default.
func (p *Policy) TorrentBackupKeep() int {
	if p == nil || p.Torrents == nil || p.Torrents.Backup.Keep == 0 {
		return DefaultTorrentBackupKeep
	}
	return p.Torrents.Backup.Keep
}

func (p *Policy) validateTorrents(e *Errors) {
	if p.Torrents == nil {
		return
	}
	for i, r := range p.Torrents.ScanRoots {
		if r == "" || !filepath.IsAbs(r) {
			e.add("torrents.scan_roots", "root %d: an absolute path is required", i)
		}
	}
	if d := p.Torrents.Backup.Every.D(); d != 0 && d < MinTorrentBackupEvery {
		e.add("torrents.backup.every", "%v is under the %v minimum", d, MinTorrentBackupEvery)
	}
	if k := p.Torrents.Backup.Keep; k < 0 || k > MaxTorrentBackupKeep {
		e.add("torrents.backup.keep", "must be between 1 and %d", MaxTorrentBackupKeep)
	}
}
