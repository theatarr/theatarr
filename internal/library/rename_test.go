package library

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRenameLink(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			e := newEnv(t, enabled)
			old := filepath.Join(e.movies, "Old", "old.mkv")
			newPath := filepath.Join(e.movies, "New", "new.mkv")
			target := filepath.Join(e.mount, "__all__", "movie.mkv")
			if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, old); err != nil {
				t.Fatal(err)
			}
			e.exec(`INSERT INTO link (id,path,target,title_id,state,file_metadata,created_at,updated_at)
				VALUES (1,?,?,1,'active','{"Revision":2}',0,0)`, old, target)
			e.exec(`INSERT INTO link_version (link_id,version_id) VALUES (1,10)`)
			err := e.lib.RenameLink(e.ctx, 1, newPath)
			if !enabled {
				if !errors.Is(err, ErrRefused) {
					t.Fatalf("got %v, want gate refusal", err)
				}
				if _, err := os.Lstat(old); err != nil {
					t.Fatalf("old link changed: %v", err)
				}
				if _, err := os.Lstat(newPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("new link exists: %v", err)
				}
				if got := e.one(`SELECT COUNT(*) FROM notify_rename`); got != "0" {
					t.Fatalf("refused rename queued event: %s", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(old); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("old link remains: %v", err)
			}
			if got, err := os.Readlink(newPath); err != nil || got != target {
				t.Fatalf("target %q, %v", got, err)
			}
			if got := e.one(`SELECT path FROM link WHERE id=1`); got != newPath {
				t.Fatalf("row path %q", got)
			}
			if err := e.lib.RenameLink(e.ctx, 1, newPath); err != nil {
				t.Fatalf("second apply: %v", err)
			}
			outside := filepath.Join(e.outside, "escape.mkv")
			if err := e.lib.RenameLink(e.ctx, 1, outside); !errors.Is(err, ErrRefused) {
				t.Fatalf("outside path: %v", err)
			}
			if got := e.one(`SELECT COUNT(*) FROM notify_rename`); got != "1" {
				t.Fatalf("rename must queue once: %s", got)
			}
			if got := e.one(`SELECT previous_path FROM notify_rename`); got != old {
				t.Fatalf("previous path %q, want %q", got, old)
			}
			if got := e.one(`SELECT quality_version FROM notify_rename`); got != "2" {
				t.Fatalf("quality revision %q, want 2", got)
			}
		})
	}
	t.Run("stale row at the destination", testRenameLinkOntoStaleRow)
}

// testRenameLinkOntoStaleRow: a row left unlinked at the destination (an earlier upgrade's or unlink's) makes way for
// the rename, also on a retry whose own link is already there; a dead row (its link can come back when its file
// returns) and an active row still refuse, and nothing changes.
func testRenameLinkOntoStaleRow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, state string
		onDisk      bool // the destination already points at the target (a retry of a rename that stopped)
		ok          bool
	}{
		{"unlinked", "unlinked", false, true},
		{"unlinked retry", "unlinked", true, true},
		{"dead", "dead", false, false},
		{"dead retry", "dead", true, false},
		{"active", "active", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, true)
			old := filepath.Join(e.movies, "Old", "old.mkv")
			newPath := filepath.Join(e.movies, "New", "new.mkv")
			target := filepath.Join(e.mount, "__all__", "movie.mkv")
			if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, old); err != nil {
				t.Fatal(err)
			}
			if tc.onDisk {
				if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, newPath); err != nil {
					t.Fatal(err)
				}
			}
			e.exec(`INSERT INTO link (id,path,target,title_id,state,file_metadata,created_at,updated_at)
				VALUES (1,?,?,1,'active','{"Revision":2}',0,0)`, old, target)
			e.exec(`INSERT INTO link_version (link_id,version_id) VALUES (1,10)`)
			e.exec(`INSERT INTO link (id,path,target,title_id,state,created_at,updated_at)
				VALUES (2,?,?,1,?,0,0)`, newPath, filepath.Join(e.mount, "__all__", "older.mkv"), tc.state)
			e.exec(`INSERT INTO link_version (link_id,version_id) VALUES (2,10)`)
			err := e.lib.RenameLink(e.ctx, 1, newPath)
			if tc.ok {
				if err != nil {
					t.Fatal(err)
				}
				if got := e.one(`SELECT path FROM link WHERE id=1`); got != newPath {
					t.Fatalf("row path %q", got)
				}
				if got := e.one(`SELECT COUNT(*) FROM link WHERE id=2`); got != "0" {
					t.Fatalf("stale row kept: %s", got)
				}
				if got, err := os.Readlink(newPath); err != nil || got != target {
					t.Fatalf("target %q, %v", got, err)
				}
				if _, err := os.Lstat(old); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("old link remains: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("rename onto a held path succeeded")
			}
			if got := e.one(`SELECT path FROM link WHERE id=1`); got != old {
				t.Fatalf("row path %q", got)
			}
			if got := e.one(`SELECT state FROM link WHERE id=2`); got != tc.state {
				t.Fatalf("destination row %q", got)
			}
			if got, err := os.Readlink(old); err != nil || got != target {
				t.Fatalf("old link %q, %v", got, err)
			}
			if _, err := os.Lstat(newPath); tc.onDisk == errors.Is(err, os.ErrNotExist) {
				t.Fatalf("destination on disk: %v", err)
			}
			if got := e.one(`SELECT COUNT(*) FROM notify_rename`); got != "0" {
				t.Fatalf("refused rename queued event: %s", got)
			}
		})
	}
}
