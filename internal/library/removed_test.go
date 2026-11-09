package library

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A client removes the download from the queue (router.Dismiss: state failed, maybe blocklisted) while its links
// are being made. The removal wins: the import takes back its link, the file it moved aside is linked back, nothing
// of it is recorded active, and the download stays failed. at is the writer step after which the removal lands:
// the new link made (the next fence sees it) or the old file moved aside (the recording sees it).
func TestRemovedDuringImport(t *testing.T) {
	t.Parallel()
	for _, at := range []string{"symlink", "remove"} {
		t.Run(at, func(t *testing.T) {
			e, old, oldTarget := upgradeEnv(t)
			e.lib.w.after = func(op, _ string) {
				if op == at {
					e.exec(`UPDATE download SET state = 'failed', error = 'removed from the queue' WHERE id = 1`)
				}
			}
			_, err := e.lib.Import(e.ctx, 1, false)
			if !errors.Is(err, ErrRemoved) {
				t.Fatalf("import err %v, want ErrRemoved", err)
			}
			if tg, err := os.Readlink(old); err != nil || tg != oldTarget {
				t.Errorf("old link not back: %q %v", tg, err)
			}
			if got := e.one(`SELECT group_concat(state) FROM link`); got != "active" {
				t.Errorf("link rows %s, want only the old one active", got)
			}
			if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "failed" {
				t.Errorf("download %s, want failed (the removal)", got)
			}
			if got := e.one(`SELECT state FROM library_import WHERE download_id = 1`); got != "failed" {
				t.Errorf("import %s", got)
			}
			// The new link is gone from the library (kept in the trash), and every trash copy is recorded.
			ents, _ := os.ReadDir(filepath.Join(e.movies, "Amelie The Movie (2001)"))
			if len(ents) != 1 {
				t.Errorf("library folder %v, want only the old link", ents)
			}
			want := map[string]string{"symlink": "1", "remove": "2"}[at]
			if got := e.one(`SELECT COUNT(*) FROM link_trash`); got != want {
				t.Errorf("trash rows %s, want %s", got, want)
			}
		})
	}
}
