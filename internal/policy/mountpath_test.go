package policy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/rootprobe"
	"github.com/theatarr/theatarr/internal/rootprobe/probetest"
)

// A root that is, or turns into, a symlink into the debrid mount is caught by where it leads, without a filesystem
// call inside the mount (here a mount path that does not even exist: any stat in it would fail differently).
func TestResolvePathStopsAtTheMount(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mount := filepath.Join(dir, "zurg")
	link := filepath.Join(dir, "lib")
	if err := os.Symlink(filepath.Join(mount, "__all__"), link); err != nil {
		t.Fatal(err)
	}
	via := filepath.Join(dir, "via")
	if err := os.Symlink("lib/../zurg", via); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{link, filepath.Join(link, "Movie"), filepath.Join(via, "x"), filepath.Join(mount, "a")} {
		if _, err := ResolvePath(p, []string{mount}); !errors.Is(err, ErrInMount) {
			t.Errorf("%s: %v", p, err)
		}
	}
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	got, err := ResolvePath(filepath.Join(dir, "alias", "new", "dir"), []string{mount})
	if want := filepath.Join(real, "new", "dir"); err != nil || got != want {
		t.Errorf("resolved %q %v, want %q", got, err, want)
	}
}

func TestMountAliases(t *testing.T) {
	t.Parallel()
	table := []mountEntry{{dev: "0:40", root: "/", point: "/mnt/zurg"}, {dev: "0:40", root: "/__all__", point: "/srv/all"},
		{dev: "8:1", root: "/", point: "/"}}
	got, err := mountAliases([]string{"/mnt/zurg"}, table, ResolvePath)
	if err != nil || !slices.Contains(got, "/srv/all") || slices.Contains(got, "/") {
		t.Fatalf("aliases %v", got)
	}
}

// Root separation refuses an active root that leads into the mount or contains it.
func TestRootSeparationMount(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, root string }{{"symlink into", "lib"}, {"contains", "."}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			mount := filepath.Join(dir, "zurg")
			if err := os.Symlink(filepath.Join(mount, "__all__"), filepath.Join(dir, "lib")); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(dir, tc.root)
			p := &Policy{Versions: map[string]*VersionPolicy{"K": {Kind: KindMovie, Mode: ModeActive, Library: root}},
				Library: &Library{Mount: LibraryMount{Path: mount}, Versions: map[string]*LibraryVersion{"K": {Roots: []string{root}}}}}
			if _, err := p.CheckRootSeparation("K"); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// The debrid mount's parent is on a share that stops answering. MountPaths (settings validation)
// answers within one bounded wait, without the parent's alias, and at once while that call is stuck; a strict
// resolver (a write check) gets ErrRootUnreachable naming the parent.
func TestMountPathsHungParent(t *testing.T) {
	dir := t.TempDir()
	share := filepath.Join(dir, "share")
	mount := filepath.Join(share, "zurg")
	if err := os.MkdirAll(mount, 0o755); err != nil {
		t.Fatal(err)
	}
	probetest.Mounts(t, share)
	release := make(chan struct{})
	oldTimeout := RootResolveTimeout
	ResolveLstat, RootResolveTimeout = func(p string) (os.FileInfo, error) {
		if p == share {
			<-release
		}
		return os.Lstat(p)
	}, 100*time.Millisecond
	t.Cleanup(func() {
		close(release)
		for deadline := time.Now().Add(5 * time.Second); !rootprobe.Default.Idle() && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
		ResolveLstat, RootResolveTimeout = os.Lstat, oldTimeout
	})
	p := &Policy{Library: &Library{Mount: LibraryMount{Path: mount}}}
	for i, limit := range []time.Duration{2 * time.Second, 50 * time.Millisecond} {
		start := time.Now()
		done := make(chan []string, 1)
		go func() { done <- p.MountPaths() }()
		select {
		case got := <-done:
			if !slices.Contains(got, mount) || time.Since(start) > limit {
				t.Fatalf("call %d: %v after %s", i, got, time.Since(start))
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("call %d: MountPaths hangs on the mount's parent", i)
		}
	}
	_, err := p.MountPathsWith(NewRootResolver(context.Background()))
	if !errors.Is(err, ErrRootUnreachable) || !strings.Contains(err.Error(), share) {
		t.Fatalf("strict: %v", err)
	}
}
