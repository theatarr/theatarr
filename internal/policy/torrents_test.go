package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTorrentsSection(t *testing.T) {
	t.Parallel()
	var none *Policy
	if none.TorrentBackupEvery() != 0 || none.TorrentBackupKeep() != DefaultTorrentBackupKeep || none.TorrentScanRoots() != nil {
		t.Error("defaults without a torrents section")
	}
	for _, tc := range []struct {
		yaml, err string
		every     time.Duration
		keep      int
	}{
		{yaml: "torrents:\n  scan_roots: [/srv/other]\n", keep: DefaultTorrentBackupKeep},
		{yaml: "torrents:\n  backup:\n    every: 24h\n    keep: 5\n", every: 24 * time.Hour, keep: 5},
		{yaml: "torrents:\n  backup:\n    every: 30m\n", err: "torrents.backup.every"},
		{yaml: "torrents:\n  backup:\n    keep: -1\n", err: "torrents.backup.keep"},
		{yaml: "torrents:\n  scan_roots: [relative/dir]\n", err: "torrents.scan_roots"},
	} {
		dir := copyExample(t)
		if err := os.WriteFile(filepath.Join(dir, "torrents.yaml"), []byte(tc.yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		p, err := Load(dir)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%q: want %s error, got %v", tc.yaml, tc.err, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", tc.yaml, err)
			continue
		}
		if p.TorrentBackupEvery() != tc.every || p.TorrentBackupKeep() != tc.keep {
			t.Errorf("%q: every %v keep %d", tc.yaml, p.TorrentBackupEvery(), p.TorrentBackupKeep())
		}
		if c, err := p.Clone(); err != nil || c.TorrentBackupKeep() != tc.keep {
			t.Errorf("%q: clone lost the section: %v", tc.yaml, err)
		}
	}
}
