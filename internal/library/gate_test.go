package library

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/rootprobe"
	"github.com/theatarr/theatarr/internal/rootprobe/probetest"
)

func TestGatePhysicalRoots(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, alias string
		allowed     bool
	}{
		{"distinct", "", true},
		{"symlink alias", "arr", false},
		{"nested via symlink", "nested", false},
		{"around via symlink", "around", false},
		{"other version library only", "library", false},
		{"other version extra root", "extra", false},
		{"trash alias", "trash", false},
		{"into the mount", "mount", false},
		{"trash into the mount", "trash-mount", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			arr, mine, trash := filepath.Join(dir, "arr"), filepath.Join(dir, "theatarr"), filepath.Join(dir, "trash")
			mount := filepath.Join(dir, "zurg")
			for _, root := range []string{arr, mine, trash, mount, filepath.Join(mount, "__all__")} {
				if err := os.Mkdir(root, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			p := &policy.Policy{Versions: map[string]*policy.VersionPolicy{
				"F": {Library: arr}, "K": {Mode: policy.ModeActive, Library: mine},
			}, Library: &policy.Library{Enabled: true, Trash: policy.LibraryTrash{Dir: trash}, Mount: policy.LibraryMount{Path: mount},
				Versions: map[string]*policy.LibraryVersion{"K": {Roots: []string{mine}}}}}
			if tc.alias == "extra" {
				p.Library.Versions["F"] = &policy.LibraryVersion{Roots: []string{filepath.Join(dir, "extra")}}
			}
			g := NewGate(p)
			if err := g.CheckPath("K", filepath.Join(mine, "movie.mkv")); err != nil {
				t.Fatalf("before alias: %v", err)
			}
			// Change the physical layout after the gate was built: every write must recheck it.
			alias, target := mine, arr
			switch tc.alias {
			case "nested":
				target = filepath.Join(arr, "child")
				if err := os.Mkdir(target, 0o755); err != nil {
					t.Fatal(err)
				}
			case "around":
				target = dir
			case "library":
				alias, target = arr, mine
			case "extra":
				alias, target = filepath.Join(dir, "extra"), mine
			case "trash":
				alias, target = trash, arr
			case "mount":
				target = filepath.Join(mount, "__all__")
			case "trash-mount":
				alias, target = trash, filepath.Join(mount, "__all__")
			}
			if tc.alias != "" {
				if tc.alias != "extra" {
					if err := os.Remove(alias); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, alias); err != nil {
					t.Fatal(err)
				}
			}
			for _, err := range []error{g.CheckPath("K", filepath.Join(mine, "movie.mkv")),
				g.CheckTrash("K", filepath.Join(trash, "movie.mkv"))} {
				if (err == nil) != tc.allowed || (err != nil && !errors.Is(err, ErrRefused)) {
					t.Errorf("allowed %v: %v", tc.allowed, err)
				}
			}
		})
	}
}

// The gate resolves every version's roots before each write. When another version's root sits on a
// share that stops answering, a write is refused ("not answering") after one bounded wait, and at once while that
// stat is stuck, instead of hanging with the caller's link locks and worker slot; the hung share gets one stat.
func TestGateHungRootRefusesWithoutHanging(t *testing.T) {
	dir := t.TempDir()
	arr, mine, trash := filepath.Join(dir, "arr"), filepath.Join(dir, "theatarr"), filepath.Join(dir, "trash")
	for _, root := range []string{arr, mine, trash} {
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	probetest.Mounts(t, arr, mine, trash)
	release := make(chan struct{})
	var hungCalls atomic.Int64
	oldTimeout := policy.RootResolveTimeout
	policy.RootStat, policy.RootResolveTimeout = func(path string) (os.FileInfo, error) {
		if path == arr {
			hungCalls.Add(1)
			<-release
		}
		return os.Stat(path)
	}, 100*time.Millisecond
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
		for deadline := time.Now().Add(5 * time.Second); !rootprobe.Default.Idle() && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
		policy.RootStat, policy.RootResolveTimeout = os.Stat, oldTimeout
	})
	p := &policy.Policy{Versions: map[string]*policy.VersionPolicy{
		"F": {Library: arr}, "K": {Mode: policy.ModeActive, Library: mine},
	}, Library: &policy.Library{Enabled: true, Trash: policy.LibraryTrash{Dir: trash},
		Versions: map[string]*policy.LibraryVersion{"K": {Roots: []string{mine}}}}}
	g := NewGate(p)
	for i, limit := range []time.Duration{2 * time.Second, 50 * time.Millisecond, 50 * time.Millisecond} {
		start := time.Now()
		done := make(chan error, 1)
		go func() { done <- g.CheckPath("K", filepath.Join(mine, "movie.mkv")) }()
		var err error
		select {
		case err = <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("check %d hangs on the stuck root", i)
		}
		var ref *Refused
		if !errors.As(err, &ref) || ref.Reason != "unreachable" || !strings.Contains(err.Error(), "not answering") ||
			!strings.Contains(err.Error(), arr) || time.Since(start) > limit {
			t.Fatalf("check %d: %v after %s", i, err, time.Since(start))
		}
	}
	if n := hungCalls.Load(); n != 1 {
		t.Fatalf("%d stat calls on the hung root, want 1", n)
	}
	close(release)
	released = true
	for deadline := time.Now().Add(5 * time.Second); !rootprobe.Default.Idle() && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	if err := g.CheckPath("K", filepath.Join(mine, "movie.mkv")); err != nil {
		t.Fatalf("after the share answered: %v", err)
	}
}
