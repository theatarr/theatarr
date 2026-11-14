package library

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// A removed title's links leave its season and title folders empty: RemoveEmptyDirs takes them out, strictly below
// the library root (the root stays even when the title was its last), never through a symlinked folder, never a
// folder that still holds something, never one outside the version's roots, and nothing while the gate refuses.
// A dead symlink (its target gone) is unlinked like a live one.
func TestRemoveEmptyDirsStopsBelowTheRoot(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	mk := func(p string) {
		t.Helper()
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The show's only file: a dead symlink (its target is gone) in Season 01 under the show's folder.
	show := filepath.Join(e.shows, "Show (2020)")
	season := filepath.Join(show, "Season 01")
	mk(season)
	p := filepath.Join(season, "Show (2020) - S01E01.mkv")
	if err := os.Symlink(filepath.Join(e.mount, "__all__", "Gone", "e1.mkv"), p); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at)
		VALUES (1, ?, ?, 2, 'dead', 0, 0)`, p, filepath.Join(e.mount, "__all__", "Gone", "e1.mkv"))
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 20)`)
	if res, err := e.lib.Unlink(e.ctx, "tv", p, 1, "removed"); err != nil || res.DryRun || res.TrashPath == "" {
		t.Fatalf("unlink of a dead link: %+v %v", res, err)
	}
	if got := e.one(`SELECT state FROM link WHERE id = 1`); got != "unlinked" {
		t.Errorf("dead link row: %s", got)
	}
	got := e.lib.RemoveEmptyDirs("tv", p)
	if !slices.Equal(got, []string{season, show}) {
		t.Errorf("removed %v", got)
	}
	if _, err := os.Stat(e.shows); err != nil {
		t.Errorf("the library root went: %v", err)
	}
	// A live link goes the same way; what it points at on the mount, and its folder there, stay.
	e.release("Live.Release", map[string]float64{"live.mkv": 7000})
	tgt := filepath.Join(e.mount, "__all__", "Live.Release", "live.mkv")
	movie := filepath.Join(e.movies, "Live (2004)")
	mk(movie)
	lp := filepath.Join(movie, "Live (2004).mkv")
	if err := os.Symlink(tgt, lp); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at) VALUES (2, ?, ?, 1, 'active', 0, 0)`,
		lp, tgt)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (2, 10)`)
	if res, err := e.lib.Unlink(e.ctx, "movie-remux", lp, 2, "removed"); err != nil || res.DryRun {
		t.Fatalf("unlink: %+v %v", res, err)
	}
	if got := e.lib.RemoveEmptyDirs("movie-remux", lp); !slices.Equal(got, []string{movie}) {
		t.Errorf("removed %v", got)
	}
	if _, err := os.Stat(tgt); err != nil {
		t.Errorf("the mount's file went: %v", err)
	}
	// The root itself is never removed, even empty and named directly.
	if got := e.lib.RemoveEmptyDirs("tv", filepath.Join(e.shows, "x.mkv")); len(got) != 0 {
		t.Errorf("removed at the root: %v", got)
	}
	// A folder that still holds a file stays, and so do its parents.
	keep := filepath.Join(e.movies, "Movie (2001)")
	mk(filepath.Join(keep, "Extras"))
	if err := os.WriteFile(filepath.Join(keep, "poster.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := e.lib.RemoveEmptyDirs("movie-remux", filepath.Join(keep, "Extras", "a.mkv")); !slices.Equal(got,
		[]string{filepath.Join(keep, "Extras")}) {
		t.Errorf("removed %v", got)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("a folder with a file went: %v", err)
	}
	// A symlinked folder is not followed or removed; neither is what it points at.
	elsewhere := filepath.Join(e.outside, "empty")
	mk(elsewhere)
	link := filepath.Join(e.movies, "Linked (2002)")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatal(err)
	}
	if got := e.lib.RemoveEmptyDirs("movie-remux", filepath.Join(link, "a.mkv")); len(got) != 0 {
		t.Errorf("removed through a symlink: %v", got)
	}
	for _, d := range []string{link, elsewhere} {
		if _, err := os.Lstat(d); err != nil {
			t.Errorf("%s went: %v", d, err)
		}
	}
	// Theatarr's own folders stay.
	own := filepath.Join(e.movies, ".theatarr-x", "sub")
	mk(own)
	if got := e.lib.RemoveEmptyDirs("movie-remux", filepath.Join(own, "a.mkv")); len(got) != 0 {
		t.Errorf("removed Theatarr's own folder: %v", got)
	}
	// Outside the version's roots: nothing.
	out := filepath.Join(e.outside, "Title", "Season 01")
	mk(out)
	if got := e.lib.RemoveEmptyDirs("movie-remux", filepath.Join(out, "a.mkv")); len(got) != 0 {
		t.Errorf("removed outside the roots: %v", got)
	}
	// The gate refusing the version: nothing.
	off := newEnv(t, false, policy.ModeActive)
	dir := filepath.Join(off.movies, "Off (2003)")
	mk(dir)
	if got := off.lib.RemoveEmptyDirs("movie-remux", filepath.Join(dir, "a.mkv")); len(got) != 0 {
		t.Errorf("removed with the library off: %v", got)
	}
}

// Folder removal stops at every library root, a root nested below another root of the library included (the gate
// refuses roots of different libraries that overlap), and touches nothing reached through a symlink anywhere below
// the root (not only the last folder).
func TestRemoveEmptyDirsStopsAtNestedRootsAndSymlinks(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	nested := filepath.Join(e.shows, "Kids")
	e.pol.Library.Versions["tv"].Roots = []string{e.shows, nested}
	g := NewGate(e.pol)
	e.lib.gate, e.lib.w.gate = g, g
	season := filepath.Join(nested, "Show (2020)", "Season 01")
	if err := os.MkdirAll(season, 0o755); err != nil {
		t.Fatal(err)
	}
	got := e.lib.RemoveEmptyDirs("tv", filepath.Join(season, "e1.mkv"))
	if !slices.Equal(got, []string{season, filepath.Dir(season)}) {
		t.Errorf("removed %v", got)
	}
	if _, err := os.Stat(nested); err != nil {
		t.Errorf("the nested root went: %v", err)
	}
	// A symlinked folder in the middle of the path: nothing goes, not even the real empty folder behind it.
	real := filepath.Join(e.movies, "Real")
	sub := filepath.Join(real, "Sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(e.movies, "Link")); err != nil {
		t.Fatal(err)
	}
	if got := e.lib.RemoveEmptyDirs("movie-remux", filepath.Join(e.movies, "Link", "Sub", "a.mkv")); len(got) != 0 {
		t.Errorf("removed through a symlink: %v", got)
	}
	if _, err := os.Stat(sub); err != nil {
		t.Errorf("the folder behind the symlink went: %v", err)
	}
}
