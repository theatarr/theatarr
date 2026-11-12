package library

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
)

func renameMeta(t *testing.T) string {
	return linkMetadata(t, decide.ExistingFile{Quality: "Remux-2160p", Revision: 1, RelativePath: "source.mkv",
		SceneName: "Movie.Release"})
}

// Relink and refresh races.

// theatarrLink makes an active link of version 10 (movie-remux) at p into the release root, with its row.
func (e *env) theatarrLink(id int64, p, target string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		e.t.Fatal(err)
	}
	e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at) VALUES (?, ?, ?, 1, 'active', 0, 0)`,
		id, p, target)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (?, 10)`, id)
}

// TestRelinkRefusesEmptyMount: a release root that is there but lists nothing (zurg restarted and serves
// an empty listing) passes Healthy. Relink, the dangling report and verify must treat it as a mount that is down:
// no link is marked dead and no link-only title flips to wanted. With the account listing again, the sweep runs.
func TestRelinkRefusesEmptyMount(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	p := filepath.Join(e.movies, "Amelie (2001)", "Amelie.mkv")
	e.theatarrLink(1, p, filepath.Join(e.mount, "__all__", "Gone", "movie.mkv"))
	e.exec(`UPDATE version SET state = 'filled' WHERE id = 10`)
	if _, err := e.lib.Relink(e.ctx, "", false); !errors.Is(err, ErrMountDown) {
		t.Fatalf("relink on an empty listing: %v", err)
	}
	if _, err := e.lib.DanglingReport(e.ctx); !errors.Is(err, ErrMountDown) {
		t.Fatalf("dangling report on an empty listing: %v", err)
	}
	scans, err := e.lib.Verify(e.ctx, "movie-remux")
	if err != nil || len(scans) != 1 || !strings.Contains(scans[0].Mount, "lists nothing") || len(scans[0].Dangling) != 0 {
		t.Fatalf("verify on an empty listing: %+v %v", scans, err)
	}
	if got := e.one(`SELECT state FROM link WHERE id = 1`) + " " + e.one(`SELECT state FROM version WHERE id = 10`) + " " +
		e.one(`SELECT COUNT(*) FROM library_action WHERE kind = 'dead'`) + " " + e.one(`SELECT COUNT(*) FROM library_dangling_scan`); got != "active filled 0 0" {
		t.Fatalf("link, version, dead actions, dangling scans: %s", got)
	}
	// The account lists its releases again (not this one): now the link is dead.
	e.release("Other.Release", map[string]float64{"other.mkv": 7000})
	if _, err := e.lib.Relink(e.ctx, "", false); err != nil {
		t.Fatal(err)
	}
	// The miss is confirmed by a later sweep.
	e.now = e.now.Add(deadConfirm)
	if _, err := e.lib.Relink(e.ctx, "", false); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT state FROM link WHERE id = 1`); got != "dead" {
		t.Fatalf("link after a real sweep: %s", got)
	}
}

// TestRelinkRefusesShrunkMount: a listing with fewer than half the entries listed before in this process, or fewer
// than half as many entries as the release folders active links point into, is a half-loaded zurg, not content
// that went away.
func TestRelinkRefusesShrunkMount(t *testing.T) {
	t.Parallel()
	t.Run("below half of the last listing", func(t *testing.T) {
		e := newEnv(t, true, policy.ModeActive)
		for _, f := range []string{"A", "B", "C", "D"} {
			e.release(f, map[string]float64{"movie.mkv": 7000})
		}
		e.theatarrLink(1, filepath.Join(e.movies, "Amelie (2001)", "Amelie.mkv"), filepath.Join(e.mount, "__all__", "A", "movie.mkv"))
		if _, err := e.lib.Relink(e.ctx, "", false); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"A", "B", "C"} {
			if err := os.RemoveAll(filepath.Join(e.mount, "__all__", f)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := e.lib.Relink(e.ctx, "", false); !errors.Is(err, ErrMountDown) {
			t.Fatalf("1 of 4 entries left: %v", err)
		}
		if got := e.one(`SELECT state FROM link WHERE id = 1`); got != "active" {
			t.Fatalf("link %s", got)
		}
	})
	t.Run("below half of the linked folders", func(t *testing.T) {
		e := newEnv(t, true, policy.ModeActive)
		for i, f := range []string{"F1", "F2", "F3", "F4", "F5", "F6"} {
			if i < 2 {
				e.release(f, map[string]float64{"movie.mkv": 7000})
			}
			e.theatarrLink(int64(i+1), filepath.Join(e.movies, f, f+".mkv"), filepath.Join(e.mount, "__all__", f, "movie.mkv"))
		}
		if _, err := e.lib.Relink(e.ctx, "", false); !errors.Is(err, ErrMountDown) {
			t.Fatalf("2 entries for 6 linked folders: %v", err)
		}
		if got := e.one(`SELECT COUNT(*) FROM link WHERE state = 'active'`); got != "6" {
			t.Fatalf("active links %s", got)
		}
	})
}

// TestRenameLosesToConcurrentRelink: after rename made its destination, a relink repairs the original
// link (its symlink and row now carry another target). The rename must not record its move or remove the repaired
// original: it removes its own new link and reports the change.
func TestRenameLosesToConcurrentRelink(t *testing.T) {
	t.Parallel()
	for _, rowToo := range []bool{true, false} {
		t.Run(map[bool]string{true: "disk and row", false: "disk only"}[rowToo], func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			old := filepath.Join(e.movies, "Old", "old.mkv")
			newPath := filepath.Join(e.movies, "New", "new.mkv")
			target := filepath.Join(e.mount, "__all__", "Name", "movie.mkv")
			repaired := filepath.Join(e.mount, "__all__", "Name {abcdef}", "movie.mkv")
			e.theatarrLink(1, old, target)
			done := false
			e.lib.w.after = func(op, p string) {
				if op == "symlink" && p == newPath && !done {
					done = true
					if err := os.Remove(old); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(repaired, old); err != nil {
						t.Fatal(err)
					}
					if rowToo {
						e.exec(`UPDATE link SET target = ? WHERE id = 1`, repaired)
					}
				}
			}
			err := e.lib.RenameLink(e.ctx, 1, newPath)
			if !errors.Is(err, ErrLinkChanged) {
				t.Fatalf("rename: %v", err)
			}
			var ref interface{ Refused() bool }
			if !errors.As(err, &ref) {
				t.Fatalf("not a refusal: %T", err)
			}
			if tg, err := os.Readlink(old); err != nil || tg != repaired {
				t.Fatalf("the repaired link -> %q %v", tg, err)
			}
			if _, err := os.Lstat(newPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the rename's new link stayed: %v", err)
			}
			if got := e.one(`SELECT path FROM link WHERE id = 1`); got != old {
				t.Fatalf("row path %s", got)
			}
			if got := e.one(`SELECT COUNT(*) FROM job WHERE kind = 'plex.scan'`); got != "0" {
				t.Fatalf("plex scans after nothing moved: %s", got)
			}
		})
	}
}

// TestRelinkLosesToConcurrentRefresh (relink side): the sweep read two dangling links; while it repoints
// the first, a refresh repoints the second (disk and row). The sweep must leave the second alone.
func TestRelinkLosesToConcurrentRefresh(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release("A.New", map[string]float64{"a.mkv": 7000})
	e.release("B.New", map[string]float64{"b.mkv": 7000})
	e.release("B.Refreshed", map[string]float64{"b.mkv": 7000})
	a := filepath.Join(e.movies, "A (2001)", "A.mkv")
	b := filepath.Join(e.movies, "B (2002)", "B.mkv")
	e.theatarrLink(1, a, filepath.Join(e.mount, "__all__", "A.Old", "a.mkv"))
	e.theatarrLink(2, b, filepath.Join(e.mount, "__all__", "B.Old", "b.mkv"))
	refreshed := filepath.Join(e.mount, "__all__", "B.Refreshed", "b.mkv")
	done := false
	e.lib.w.after = func(op, p string) {
		if op == "symlink" && p == a && !done {
			done = true
			if err := os.Remove(b); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(refreshed, b); err != nil {
				t.Fatal(err)
			}
			e.exec(`UPDATE link SET target = ? WHERE id = 2`, refreshed)
		}
	}
	scans, err := e.lib.RelinkRenamed(e.ctx, map[string]string{"A.Old": "A.New", "B.Old": "B.New"})
	if err != nil {
		t.Fatal(err)
	}
	var sc *LinkScan
	for _, s := range scans {
		if s.Version == "movie-remux" {
			sc = s
		}
	}
	if sc == nil || len(sc.Relinked) != 1 || sc.Causes["changed"] != 1 || len(sc.Dangling) != 0 {
		t.Fatalf("scan %+v", sc)
	}
	if tg, err := os.Readlink(b); err != nil || tg != refreshed {
		t.Fatalf("the refreshed link -> %q %v", tg, err)
	}
	if got := e.one(`SELECT state || ' ' || target FROM link WHERE id = 2`); got != "active "+refreshed {
		t.Fatalf("row %s", got)
	}
	if tg, _ := os.Readlink(a); tg != filepath.Join(e.mount, "__all__", "A.New", "a.mkv") {
		t.Fatalf("the first link -> %q", tg)
	}
}

// TestRelinkDeadMarkSkipsRepointedLink: the sweep found a link dangling, but before it records the dead mark a
// refresh repointed it: the mark needs the target the sweep saw.
func TestRelinkDeadMarkSkipsRepointedLink(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release("A.New", map[string]float64{"a.mkv": 7000})
	e.release("Fresh", map[string]float64{"b.mkv": 7000})
	a := filepath.Join(e.movies, "A (2001)", "A.mkv")
	b := filepath.Join(e.movies, "B (2002)", "B.mkv")
	e.theatarrLink(1, a, filepath.Join(e.mount, "__all__", "A.Old", "a.mkv"))
	e.theatarrLink(2, b, filepath.Join(e.mount, "__all__", "Gone", "b.mkv"))
	fresh := filepath.Join(e.mount, "__all__", "Fresh", "b.mkv")
	done := false
	e.lib.w.after = func(op, p string) {
		if op == "symlink" && p == a && !done {
			done = true
			e.exec(`UPDATE link SET target = ? WHERE id = 2`, fresh)
		}
	}
	if _, err := e.lib.RelinkRenamed(e.ctx, map[string]string{"A.Old": "A.New"}); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT state || ' ' || target FROM link WHERE id = 2`) + " " +
		e.one(`SELECT COUNT(*) FROM library_action WHERE kind = 'dead'`); got != "active "+fresh+" 0" {
		t.Fatalf("row, dead actions: %s", got)
	}
}

// TestRenameQueuesPlexScans: a rename queues debounced Plex scans of the old and the new folder, also for
// the links a batch moved before it failed; a closed gate moves nothing and queues nothing.
func TestRenameQueuesPlexScans(t *testing.T) {
	t.Parallel()
	scans := func(e *env) string {
		return e.one(`SELECT COALESCE(GROUP_CONCAT(p, '|'), '') FROM (SELECT json_extract(payload, '$.path') AS p FROM job
			WHERE kind = 'plex.scan' AND json_extract(payload, '$.section') = 18 ORDER BY p)`)
	}
	t.Run("moved", func(t *testing.T) {
		e := newEnv(t, true, policy.ModeActive)
		old := filepath.Join(e.movies, "Amelie (2001)", "old.mkv")
		e.theatarrLink(1, old, filepath.Join(e.mount, "__all__", "x.mkv"))
		e.exec(`UPDATE link SET file_metadata = ? WHERE id = 1`, renameMeta(t))
		p, err := e.lib.ApplyRename(e.ctx, 1, nil)
		if err != nil || len(p.Files) != 1 {
			t.Fatalf("apply %+v %v", p, err)
		}
		if got, want := scans(e), strings.Join(sortedKeys(map[string]bool{filepath.Dir(old): true,
			filepath.Dir(p.Files[0].NewPath): true}), "|"); got != want {
			t.Fatalf("scans %q, want %q", got, want)
		}
	})
	t.Run("batch failed after one move", func(t *testing.T) {
		e := newEnv(t, true, policy.ModeActive)
		first := filepath.Join(e.movies, "A (2001)", "old.mkv")
		second := filepath.Join(e.movies, "B (2001)", "old.mkv")
		e.theatarrLink(1, first, filepath.Join(e.mount, "__all__", "x.mkv"))
		e.theatarrLink(2, second, filepath.Join(e.mount, "__all__", "y.mkv"))
		e.exec(`UPDATE link SET file_metadata = ?`, renameMeta(t))
		// Both links name the same file: the second move finds the first one there.
		p, err := e.lib.ApplyRename(e.ctx, 1, nil)
		if err == nil || p == nil || len(p.Files) != 2 {
			t.Fatalf("apply %+v %v", p, err)
		}
		if got, want := scans(e), strings.Join(sortedKeys(map[string]bool{filepath.Dir(first): true,
			filepath.Dir(p.Files[0].NewPath): true}), "|"); got != want {
			t.Fatalf("scans %q, want %q", got, want)
		}
	})
	t.Run("gate closed", func(t *testing.T) {
		e := newEnv(t, true, policy.ModeShadow)
		old := filepath.Join(e.movies, "Amelie (2001)", "old.mkv")
		e.theatarrLink(1, old, filepath.Join(e.mount, "__all__", "x.mkv"))
		e.exec(`UPDATE link SET file_metadata = ?`, renameMeta(t))
		if _, err := e.lib.ApplyRename(e.ctx, 1, nil); !errors.Is(err, ErrRefused) {
			t.Fatalf("shadow version: %v", err)
		}
		if _, err := os.Lstat(old); err != nil || scans(e) != "" {
			t.Fatalf("old link %v, scans %q", err, scans(e))
		}
	})
}
