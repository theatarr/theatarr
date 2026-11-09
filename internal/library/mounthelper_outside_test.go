package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// A client's path outside the mount root runs in a helper too: a folder of symlinks into the mount reaches the
// mount through them, so a stat that follows one into a dead mount blocks a helper, which is killed, and not Theatarr.
// Outside the root the helper answers stat, lstat, readlink and listings only.
func TestMountHelperRunsPathsOutsideRoot(t *testing.T) {
	m := helperMount(t, time.Second)
	m.BreakAfter = 100
	ctx := context.Background()
	local := t.TempDir()
	link := filepath.Join(local, "release")
	if err := os.Symlink(filepath.Join(m.AllPath(), "Rel"), link); err != nil {
		t.Fatal(err)
	}
	fi, err := m.Stat(ctx, filepath.Join(link, "movie.mkv"))
	if err != nil || fi.Size() != int64(len("not a video")) {
		t.Fatalf("stat through a local symlink: %v %v", fi, err)
	}
	if ents, err := m.ReadDir(ctx, link); err != nil || len(ents) != 1 || ents[0].Name() != "movie.mkv" {
		t.Errorf("listing through a local symlink: %v %v", ents, err)
	}
	if loc, err := m.Locate(ctx, link); err != nil || loc.Folder != "Rel" {
		t.Errorf("locate %+v %v", loc, err)
	}
	h := m.Breaker().Helper
	if h.Starts == 0 {
		t.Fatalf("calls outside the mount ran in Theatarr: %+v", h)
	}
	// A call that hangs there is killed with its helper; Theatarr keeps no goroutine or thread for it.
	th, goroutines := threads(t), runtime.NumGoroutine()
	for range 4 {
		if _, err := m.Stat(ctx, filepath.Join(local, "HANG")); !errors.Is(err, ErrMountDown) {
			t.Fatalf("stat of a hung local path: %v", err)
		}
	}
	if h := m.Breaker().Helper; h.Kills < 1 || h.Busy != 0 {
		t.Errorf("helper state %+v", h)
	}
	waitUntil(t, func() bool { return runtime.NumGoroutine() <= goroutines })
	if now := threads(t); now > th+2 {
		t.Errorf("threads %d, were %d", now, th)
	}
	// File contents and symlink resolution stay under the root.
	s := &helperServer{root: m.Path}
	for _, op := range []string{opProbe, opEval} {
		if err := s.handle(helperRequest{Op: op, Path: []byte(filepath.Join(link, "movie.mkv"))}).Err.decode(); !errors.Is(err, ErrNotOnMount) {
			t.Errorf("%s outside the root: %v", op, err)
		}
	}
	if err := s.handle(helperRequest{Op: opStat, Path: []byte("relative/path")}).Err.decode(); !errors.Is(err, ErrNotOnMount) {
		t.Errorf("relative path: %v", err)
	}
}
