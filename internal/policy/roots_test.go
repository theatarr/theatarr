package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/rootprobe"
	"github.com/theatarr/theatarr/internal/rootprobe/probetest"
)

// parallelPolicy is the example policy with one more version (the YAML under versions:) and library.versions entry.
func parallelPolicy(t *testing.T, version, libraryVersion string) string {
	t.Helper()
	dir := copyExample(t)
	if err := os.WriteFile(filepath.Join(dir, "parallel.yaml"), []byte("versions:\n"+version), 0o644); err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(dir, "library.yaml")
	b, err := os.ReadFile(lib)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(string(b), "\n  versions:\n", "\n  versions:\n"+libraryVersion, 1)
	if err := os.WriteFile(lib, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const liveYAML = `  movie-new:
    mode: active
    kind: movie
    anime: false
    profile: remux-2160p
    library: /media/movies-new
    storage: debrid
    indexers: [prowlarr-public]
    providers: [rd]
    client: decypharr
`

const liveLib = "    movie-new: {roots: [/media/movies-new]}\n"

// A Live library's roots may not overlap another library's.
func TestLiveRootSeparation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, version, lib, err string
	}{
		{"separate", liveYAML, liveLib, ""},
		{"root inside another's", strings.Replace(liveYAML, "/media/movies-new", "/media/movies/theatarr", 1),
			"    movie-new: {roots: [/media/movies/theatarr]}\n", "overlaps"},
		{"root around another's", strings.Replace(liveYAML, "/media/movies-new", "/media", 1),
			"    movie-new: {roots: [/media]}\n", "overlaps"},
		{"same root", strings.Replace(liveYAML, "/media/movies-new", "/media/movies-web", 1),
			"    movie-new: {roots: [/media/movies-web]}\n", "of library movie-web"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(parallelPolicy(t, tc.version, tc.lib))
			switch {
			case tc.err == "" && err != nil:
				t.Fatal(err)
			case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
				t.Fatalf("want an error about %s, got %v", tc.err, err)
			}
		})
	}
}

func TestLiveRootPhysicalSeparation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, target, suffix string
		wantError            bool
	}{
		{"distinct", "", "", false},
		{"symlink alias", "arr", "", true},
		{"nested via symlink", "arr", "new/library", true},
		{"around via symlink", ".", "", true},
		{"trash alias", "trash", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			arr, mine, trash := filepath.Join(dir, "arr"), filepath.Join(dir, "theatarr"), filepath.Join(dir, "trash")
			for _, root := range []string{arr, trash} {
				if err := os.Mkdir(root, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if tc.target == "" {
				if err := os.Mkdir(mine, 0o755); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(filepath.Join(dir, tc.target), mine); err != nil {
				t.Fatal(err)
			}
			mine = filepath.Join(mine, tc.suffix)
			p := &Policy{Versions: map[string]*VersionPolicy{
				"F": {Kind: KindMovie, Library: arr},
				"K": {Kind: KindMovie, Mode: ModeActive, Library: mine},
			}, Library: &Library{Trash: LibraryTrash{Dir: trash}, Versions: map[string]*LibraryVersion{
				"K": {Roots: []string{mine}},
			}}}
			e := &Errors{}
			p.validateRootSeparation(e)
			if (len(e.List) != 0) != tc.wantError {
				t.Fatalf("validation: %v", e)
			}
		})
	}
}

func TestRootDirectoryIdentity(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first, err := ResolveRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ResolveRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Bind mounts can have distinct resolved names while Stat reports the same device and inode.
	second.Resolved = filepath.Join(t.TempDir(), "bind")
	if !first.Overlaps(second) {
		t.Fatal("same device and inode not detected")
	}
}

// Validate runs on every settings load and save. A root on a share that stops answering costs one bounded
// wait, later validations answer at once while that call is stuck, the root is compared by its path, and other
// roots are still resolved (an alias of the trash is still found).
func TestValidateBoundsHungRoot(t *testing.T) {
	dir := t.TempDir()
	arr, mine, trash := filepath.Join(dir, "arr"), filepath.Join(dir, "theatarr"), filepath.Join(dir, "trash")
	for _, root := range []string{arr, mine, trash} {
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	probetest.Mounts(t, arr) // the hung share
	release := make(chan struct{})
	oldTimeout := RootResolveTimeout
	RootStat, RootResolveTimeout = func(path string) (os.FileInfo, error) {
		if path == arr {
			<-release
		}
		return os.Stat(path)
	}, 100*time.Millisecond
	t.Cleanup(func() {
		close(release)
		for deadline := time.Now().Add(5 * time.Second); !rootprobe.Default.Idle() && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
		RootStat, RootResolveTimeout = os.Stat, oldTimeout
	})
	p := &Policy{Versions: map[string]*VersionPolicy{
		"F": {Kind: KindMovie, Library: arr},
		"K": {Kind: KindMovie, Mode: ModeActive, Library: mine},
	}, Library: &Library{Trash: LibraryTrash{Dir: trash}, Versions: map[string]*LibraryVersion{
		"K": {Roots: []string{mine}},
	}}}
	for i, limit := range []time.Duration{2 * time.Second, 50 * time.Millisecond} {
		start := time.Now()
		e := &Errors{}
		p.validateRootSeparation(e)
		if len(e.List) != 0 || time.Since(start) > limit {
			t.Fatalf("validation %d: %v after %s", i, e, time.Since(start))
		}
	}
	if !rootprobe.Default.Stuck(arr) {
		t.Fatal("the hung root is not marked stuck")
	}
	// A reachable alias is still refused while the hung root waits.
	if err := os.Remove(mine); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(trash, mine); err != nil {
		t.Fatal(err)
	}
	e := &Errors{}
	p.validateRootSeparation(e)
	if len(e.List) == 0 || !strings.Contains(e.Error(), "trash") {
		t.Fatalf("trash alias beside a hung root: %v", e)
	}
}

// Collection targets are the versions the library gate lets write: Live movie libraries.
func TestCollectionTargets(t *testing.T) {
	t.Parallel()
	p := &Policy{Versions: map[string]*VersionPolicy{
		"movie-remux": {Kind: KindMovie, Mode: ModeShadow},
		"movie-4k":    {Kind: KindMovie, Mode: ModeActive},
		"movie-new":   {Kind: KindMovie, Mode: ModeActive},
		"tv-2160p":    {Kind: KindSeries, Mode: ModeActive},
	}}
	if got, want := strings.Join(p.CollectionTargets(), ","), "movie-4k,movie-new"; got != want {
		t.Errorf("targets %s, want %s", got, want)
	}
	if p.CollectionTarget("movie-remux") || p.CollectionTarget("tv-2160p") || p.CollectionTarget("nope") {
		t.Error("a Watch-only, series or unknown version is a target")
	}
}
