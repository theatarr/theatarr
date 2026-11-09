package library

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestRefreshRefusesEmptyMount: the follower refresh refuses an empty listing as a mount that is down.
func TestRefreshRefusesEmptyMount(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	old := "Amelie.2001.1080p.WEB-DL-POKE"
	e.release(old, map[string]float64{"movie.mkv": 7000})
	e.arrFile(1, "radarr", 10, 1, filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie.WEB.mkv"), old+"/movie.mkv", "WEBDL-1080p")
	if rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr"}); err != nil || rep.Linked != 1 {
		t.Fatalf("adopt %+v %v", rep, err)
	}
	if err := os.RemoveAll(filepath.Join(e.mount, "__all__", old)); err != nil {
		t.Fatal(err)
	}
	before := strings.Join(tree(t, e.dir), "|")
	if _, err := e.lib.Refresh(e.ctx, RefreshOptions{Version: "movie-theatarr"}); !errors.Is(err, ErrMountDown) {
		t.Fatalf("refresh on an empty listing: %v", err)
	}
	if err := e.lib.RefreshFollowersAll(e.ctx, "movie-remux"); !errors.Is(err, ErrMountDown) {
		t.Fatalf("catch-up on an empty listing: %v", err)
	}
	if after := strings.Join(tree(t, e.dir), "|"); after != before {
		t.Fatal("refresh changed the tree")
	}
}

// TestRefreshContinuesPastFailedLink: one link whose swap fails (here the gate refuses its folder, a symlink out of
// the root) is recorded, and the version's other links are still refreshed.
func TestRefreshContinuesPastFailedLink(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES (203, 2, 2, 1, 'Third', 0, 0)`)
	rel, up := "Show.S01.1080p.WEB-DL-GRP", "Show.S01.2160p.Remux-BYNDR"
	files := map[string]float64{"Show.S01E01.mkv": 2700, "Show.S02E01.mkv": 2700}
	e.release(rel, files)
	arr := filepath.Join(e.shows, "Show (2020)")
	e.arrFile(1, "sonarr", 20, 2, filepath.Join(arr, "Season 01", "Show - S01E01.mkv"), rel+"/Show.S01E01.mkv", "WEBDL-1080p", 201)
	e.arrFile(2, "sonarr", 20, 2, filepath.Join(arr, "Season 02", "Show - S02E01.mkv"), rel+"/Show.S02E01.mkv", "WEBDL-1080p", 203)
	if rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "tv-theatarr"}); err != nil || rep.Linked != 2 {
		t.Fatalf("adopt %+v %v", rep, err)
	}
	e.release(up, files)
	e.arrUpgrade(1, "", 3, "sonarr", 20, 2, filepath.Join(arr, "Season 01", "Show - S01E01 Remux.mkv"), up+"/Show.S01E01.mkv",
		"Bluray-2160p Remux", 201)
	e.arrUpgrade(2, "", 4, "sonarr", 20, 2, filepath.Join(arr, "Season 02", "Show - S02E01 Remux.mkv"), up+"/Show.S02E01.mkv",
		"Bluray-2160p Remux", 203)
	s1 := e.one(`SELECT l.path FROM link l JOIN link_episode le ON le.link_id = l.id WHERE le.episode_id = 201`)
	// Season 01 of the follower becomes a symlink out of the root: the gate refuses every write in it.
	season1 := filepath.Dir(s1)
	moved := filepath.Join(e.outside, "Season 01")
	if err := os.Rename(season1, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, season1); err != nil {
		t.Fatal(err)
	}
	rep, err := e.lib.Refresh(e.ctx, RefreshOptions{Version: "tv-theatarr"})
	if err != nil || rep.Links != 2 || rep.Swapped != 1 || rep.Failed != 1 || len(rep.Problems) != 1 ||
		rep.Problems[0].Reason != RefreshFailed || rep.Problems[0].Path != s1 {
		t.Fatalf("refresh %+v %v", rep, err)
	}
	if got := e.one(`SELECT l.target FROM link l JOIN link_episode le ON le.link_id = l.id WHERE le.episode_id = 203
		AND l.state = 'active'`); got != filepath.Join(e.mount, "__all__", up, "Show.S02E01.mkv") {
		t.Fatalf("S02E01 -> %s", got)
	}
	if got := e.one(`SELECT state || ' ' || target FROM link WHERE path = ?`, s1); got != "active "+filepath.Join(e.mount, "__all__", rel, "Show.S01E01.mkv") {
		t.Fatalf("S01E01 row %s", got)
	}
	// The automatic refresh reports the failed link, so the catch-up runs again at the next import.
	if err := e.lib.RefreshFollowersAll(e.ctx, "tv"); err == nil || !strings.Contains(err.Error(), "1 link(s) failed") {
		t.Fatalf("catch-up with a failing link: %v", err)
	}
}

// TestRefreshLosesToConcurrentRename (refresh side): a rename moves the adopted link while the refresh
// swaps it. The refresh leaves the link to the rename: it removes the new link it made and does not retire the row.
func TestRefreshLosesToConcurrentRename(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	old := "Amelie.2001.1080p.WEB-DL-POKE"
	e.release(old, map[string]float64{"movie.mkv": 7000})
	arrDir := filepath.Join(e.movies, "Amelie The Movie (2001)")
	e.arrFile(1, "radarr", 10, 1, filepath.Join(arrDir, "Amelie.WEB.mkv"), old+"/movie.mkv", "WEBDL-1080p")
	if rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr"}); err != nil || rep.Linked != 1 {
		t.Fatalf("adopt %+v %v", rep, err)
	}
	folder := filepath.Join(e.theatarrMovies, "Amelie The Movie (2001)")
	adopted := filepath.Join(folder, "Amelie The Movie (2001) {imdb-tt0211915} - [WEBDL-1080p]-GRP.mkv")
	renamed := filepath.Join(folder, "Renamed.mkv")
	upgraded := "Amelie.2001.2160p.Remux-BYNDR"
	e.release(upgraded, map[string]float64{"movie.mkv": 7000})
	e.arrUpgrade(1, "", 2, "radarr", 10, 1, filepath.Join(arrDir, "Amelie.Remux.mkv"), upgraded+"/movie.mkv", "Remux-2160p")
	want := filepath.Join(folder, "Amelie The Movie (2001) {imdb-tt0211915} - [Remux-2160p]-GRP.mkv")
	done := false
	e.lib.w.after = func(op, p string) {
		if op == "symlink" && p == want && !done {
			done = true
			// The rename: the link moves on disk and in its row.
			if err := os.Rename(adopted, renamed); err != nil {
				t.Fatal(err)
			}
			e.exec(`UPDATE link SET path = ? WHERE id = 1`, renamed)
		}
	}
	rep, err := e.lib.Refresh(e.ctx, RefreshOptions{Version: "movie-theatarr"})
	if err != nil || rep.Swapped != 0 || rep.Failed != 1 || rep.Problems[0].Reason != RefreshChanged {
		t.Fatalf("refresh %+v %v", rep, err)
	}
	if _, err := os.Lstat(want); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the refresh's new link stayed: %v", err)
	}
	if got := e.one(`SELECT GROUP_CONCAT(id || ' ' || state || ' ' || path, ', ') FROM link`); got != "1 active "+renamed {
		t.Fatalf("link rows %s", got)
	}
	if tg, err := os.Readlink(renamed); err != nil || tg != filepath.Join(e.mount, "__all__", old, "movie.mkv") {
		t.Fatalf("renamed link -> %q %v", tg, err)
	}
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

// sameNameRefreshEnv is an adopted movie link whose arr file the arr replaced with another release of the same
// quality and group: the refresh swaps the link in place (same path, new target). It returns the link and both
// targets.
func sameNameRefreshEnv(t *testing.T) (*parallelEnv, string, string, string) {
	t.Helper()
	e := newParallelEnv(t, true)
	old := "Amelie.2001.1080p.WEB-DL-GRP"
	e.release(old, map[string]float64{"movie.mkv": 7000})
	arrDir := filepath.Join(e.movies, "Amelie The Movie (2001)")
	e.arrFile(1, "radarr", 10, 1, filepath.Join(arrDir, "Amelie.WEB.mkv"), old+"/movie.mkv", "WEBDL-1080p")
	if rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr"}); err != nil || rep.Linked != 1 {
		t.Fatalf("adopt %+v %v", rep, err)
	}
	link := e.one(`SELECT path FROM link WHERE id = 1`)
	repack := "Amelie.2001.1080p.WEB-DL.REPACK-GRP"
	e.release(repack, map[string]float64{"movie.mkv": 7000})
	e.arrUpgrade(1, "", 2, "radarr", 10, 1, filepath.Join(arrDir, "Amelie.WEB.REPACK.mkv"), repack+"/movie.mkv", "WEBDL-1080p")
	return e, link, filepath.Join(e.mount, "__all__", old, "movie.mkv"), filepath.Join(e.mount, "__all__", repack, "movie.mkv")
}

// A refresh that stalled past its lease (here during its trash copy) while a newer refresh took the
// lease over and refreshed the link must not overwrite the newer link: it stops before replacing anything.
func TestRefreshStalledPastLeaseKeepsNewerLink(t *testing.T) {
	t.Parallel()
	e, link, _, _ := sameNameRefreshEnv(t)
	newer := filepath.Join(e.mount, "__all__", "Newer", "movie.mkv")
	done := false
	e.lib.w.after = func(op, p string) {
		if op == "trash" && p == link && !done {
			done = true
			e.now = e.now.Add(leaseFor + time.Minute)
			// The newer refresh: lease taken over, link and row on a newer target.
			e.exec(`UPDATE library_lease SET owner = 'newer', until = ?`, e.now.Add(leaseFor).UnixMilli())
			if err := os.Remove(link); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(newer, link); err != nil {
				t.Fatal(err)
			}
			e.exec(`UPDATE link SET target = ? WHERE id = 1`, newer)
		}
	}
	if _, err := e.lib.Refresh(e.ctx, RefreshOptions{Version: "movie-theatarr"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("stalled refresh: %v, want ErrBusy", err)
	}
	if !done {
		t.Fatal("the stall did not happen")
	}
	if tg, err := os.Readlink(link); err != nil || tg != newer {
		t.Fatalf("link -> %q %v, want the newer refresh's %q", tg, err, newer)
	}
	if got := e.one(`SELECT target FROM link WHERE id = 1`); got != newer {
		t.Fatalf("row target %s", got)
	}
}

// When the row commit of an in-place swap loses (the row moved on after the disk swap), the previous
// symlink is put back, as a separate new path is removed.
func TestRefreshSamePathRestoredWhenCommitLoses(t *testing.T) {
	t.Parallel()
	e, link, oldTarget, newTarget := sameNameRefreshEnv(t)
	done := false
	e.lib.w.after = func(op, p string) {
		if op == "symlink" && p == link && !done {
			done = true
			e.exec(`UPDATE link SET updated_at = updated_at + 1, target = target || '.moved' WHERE id = 1`)
		}
	}
	rep, err := e.lib.Refresh(e.ctx, RefreshOptions{Version: "movie-theatarr"})
	if err != nil || rep.Swapped != 0 || rep.Failed != 1 || rep.Problems[0].Reason != RefreshChanged {
		t.Fatalf("refresh %+v %v", rep, err)
	}
	if !done {
		t.Fatalf("no in-place swap happened (new target %s)", newTarget)
	}
	if tg, err := os.Readlink(link); err != nil || tg != oldTarget {
		t.Fatalf("link -> %q %v, want the previous %q restored", tg, err, oldTarget)
	}
}
