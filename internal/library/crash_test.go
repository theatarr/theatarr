package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/library/probe"
	"github.com/theatarr/theatarr/internal/policy"
)

// upgradeEnv is an upgrade: the version's current file is an active link of ours into Old.Release, and download 1
// brings the new release.
func upgradeEnv(t *testing.T) (e *env, old, oldTarget string) {
	e = newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.release("Old.Release", map[string]float64{"old.mkv": 7000})
	oldTarget = filepath.Join(e.mount, "__all__", "Old.Release", "old.mkv")
	folder := filepath.Join(e.movies, "Amelie The Movie (2001)")
	old = filepath.Join(folder, "Amelie The Movie (2001) - old.mkv")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(oldTarget, old); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO link (id, path, target, kind, title_id, release_title, state, created_at, updated_at)
		VALUES (7, ?, ?, 'symlink', 1, 'Old.Release', 'active', 0, 0)`, old, oldTarget)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (7, 10)`)
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	return e, old, oldTarget
}

// finished checks the end state of the upgrade, however many runs it took: the new link, the old one moved aside
// once (its row unlinked), the download imported, and the mount untouched.
func (e *env) finished(old, oldTarget string, trashRows string) {
	e.t.Helper()
	if _, err := os.Lstat(old); !errors.Is(err, os.ErrNotExist) {
		e.t.Errorf("old link still in the library: %v", err)
	}
	if got := e.one(`SELECT state FROM link WHERE id = 7`); got != "unlinked" {
		e.t.Errorf("old link row %s, want unlinked", got)
	}
	if got := e.one(`SELECT count(*) FROM link WHERE state = 'active'`); got != "1" {
		e.t.Errorf("%s active links, want the new one", got)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "imported" {
		e.t.Errorf("download %s", got)
	}
	if got := e.one(`SELECT count(*) FROM link_trash`); got != trashRows {
		e.t.Errorf("%s trash rows, want %s", got, trashRows)
	}
	if _, err := os.Stat(oldTarget); err != nil {
		e.t.Errorf("mount content touched: %v", err)
	}
}

// Crash immediately before the link replacement: the import lost its claim (it stalled past claimFor and another
// run may hold it). The fence stops it before any link changes; the next run does the whole import.
func TestImportFencedBeforeLinkReplacement(t *testing.T) {
	t.Parallel()
	e, old, oldTarget := upgradeEnv(t)
	before := tree(t, e.dir)
	inner := e.lib.probe
	e.lib.probe = func(p string, n int64) (*probe.Result, error) {
		e.now = e.now.Add(claimFor + time.Minute) // the stall
		return inner(p, n)
	}
	if _, err := e.lib.Import(e.ctx, 1, false); !errors.Is(err, ErrClaimed) {
		t.Fatalf("stalled import: %v, want ErrClaimed from the fence", err)
	}
	if after := tree(t, e.dir); len(after) != len(before) {
		t.Fatalf("the fenced import changed the filesystem:\n%v\n%v", before, after)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "importing" {
		t.Errorf("download %s", got)
	}
	e.lib.probe = inner
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	e.finished(old, oldTarget, "1")
}

// Crash immediately after the new link is made, before the old one is moved aside and before anything is recorded:
// the retry finds the new link in place, still moves the old one aside, and records both.
func TestImportCrashAfterLinkMade(t *testing.T) {
	t.Parallel()
	e, old, oldTarget := upgradeEnv(t)
	ctx, cancel := context.WithCancel(e.ctx)
	e.lib.w.after = func(op, path string) {
		if op == "symlink" {
			cancel()
		}
	}
	if _, err := e.lib.Import(ctx, 1, false); err == nil {
		t.Fatal("an interrupted import reported success")
	}
	if _, err := os.Lstat(old); err != nil {
		t.Fatalf("the old link went although the fence stopped the run: %v", err)
	}
	if got := e.one(`SELECT count(*) FROM link`); got != "1" {
		t.Errorf("%s link rows after the crash, want the old one only", got)
	}
	e.lib.w.after = nil
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Links[0].Exists {
		t.Error("the retry did not find the new link in place")
	}
	e.finished(old, oldTarget, "1")
}

// Crash immediately after the old link was moved aside, before the database: the retry records the new link and
// unlinks the old row it finds gone.
func TestImportCrashAfterOldMovedAside(t *testing.T) {
	t.Parallel()
	e, old, oldTarget := upgradeEnv(t)
	ctx, cancel := context.WithCancel(e.ctx)
	e.lib.w.after = func(op, path string) {
		if op == "remove" {
			cancel()
		}
	}
	if _, err := e.lib.Import(ctx, 1, false); err == nil {
		t.Fatal("an interrupted import reported success")
	}
	if got := e.one(`SELECT state FROM link WHERE id = 7`); got != "active" {
		t.Fatalf("old row %s before the retry", got)
	}
	e.lib.w.after = nil
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	// The interrupted run's trash copy exists on disk but has no row: the retry cannot know its path.
	e.finished(old, oldTarget, "0")
}
