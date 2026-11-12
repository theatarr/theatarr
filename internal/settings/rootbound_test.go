package settings

import (
	"context"
	"database/sql"
	"fmt"
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

// A root in the mount is refused by its path before anything stats it, whether or not the mount answers.
func TestRootInMountRefusedByPath(t *testing.T) {
	t.Parallel()
	s, _, p, _ := versionFixture(t)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p.Library.Mount.Path = filepath.Join(file, "zurg") // any stat in it fails: not a directory
	for _, root := range []string{p.Library.Mount.Path, filepath.Join(p.Library.Mount.Path, "__all__", "lib")} {
		if _, err := validateOneRoot(context.Background(), policy.NewRootResolver(context.Background()), p, "new", root, false); err == nil || !strings.Contains(err.Error(), "overlaps the library mount") {
			t.Errorf("%s: %v", root, err)
		}
		if _, err := s.ValidateRoot(context.Background(), p, "movie-web", root); err == nil || !strings.Contains(err.Error(), "mount") {
			t.Errorf("%s: %v", root, err)
		}
	}
}

// A save's filesystem root checks run before its write transaction, and a root that stops answering fails the save
// after rootCheckTimeout instead of holding the request (and the database writer).
func TestRootChecksBoundedOutsideWriter(t *testing.T) {
	s, db, p, initial := versionFixture(t)
	newRoot := filepath.Join(p.Library.Dangling.Roots[0], "new-root")
	if err := os.Mkdir(newRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	old := rootCheckTimeout
	rootCheckTimeout = 300 * time.Millisecond
	release := make(chan struct{})
	writer := make(chan error, 1)
	rootStat = func(path string) (os.FileInfo, error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		writer <- db.WriteTx(ctx, func(tx *sql.Tx) error { return nil })
		<-release
		return os.Stat(path)
	}
	t.Cleanup(func() { close(release); waitRootChecksIdle(t); rootStat, rootCheckTimeout = os.Stat, old })
	req := saveRequest(p, initial)
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Library = newRoot; v.Roots = []string{newRoot} })
	start := time.Now()
	_, err := s.Save(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "root check gave up after") {
		t.Fatalf("save with a hung root: %v", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("save took %v", took)
	}
	if err := <-writer; err != nil {
		t.Errorf("the root check ran while the save held the writer: %v", err)
	}
}

// A root check that gave up keeps its filesystem call's slot until the call returns; while it is stuck, new checks
// are refused without another call on the filesystem, and once it returns they run again.
func TestStuckRootCheckRefusesNewChecks(t *testing.T) {
	s, _, p, _ := versionFixture(t)
	newRoot := filepath.Join(p.Library.Dangling.Roots[0], "new-root")
	if err := os.Mkdir(newRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	old := rootCheckTimeout
	rootCheckTimeout = 200 * time.Millisecond
	release := make(chan struct{})
	var calls atomic.Int64
	rootStat = func(path string) (os.FileInfo, error) {
		if calls.Add(1) == 1 {
			<-release
		}
		return os.Stat(path)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
			waitRootChecksIdle(t)
		}
		rootStat, rootCheckTimeout = os.Stat, old
	})
	ctx := context.Background()
	if _, err := s.ValidateRoot(ctx, p, "movie-web", newRoot); err == nil || !strings.Contains(err.Error(), "gave up") {
		t.Fatalf("hung root: %v", err)
	}
	for range 5 {
		if _, err := s.ValidateRoot(ctx, p, "movie-web", newRoot); err == nil || !strings.Contains(err.Error(), "still waiting") {
			t.Fatalf("while stuck: %v", err)
		}
	}
	if n := calls.Load(); n != 1 || !rootprobe.Default.Stuck(newRoot) {
		t.Fatalf("stat calls %d, stuck %v; want 1, true", n, rootprobe.Default.Stuck(newRoot))
	}
	close(release)
	released = true
	waitRootChecksIdle(t)
	if _, err := s.ValidateRoot(ctx, p, "movie-web", newRoot); err != nil {
		t.Fatalf("after the filesystem answered: %v", err)
	}
}

// waitRootChecksIdle waits for root checks left behind to return their slots, so later tests are not refused.
func waitRootChecksIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !rootprobe.Default.Idle() {
		if time.Now().After(deadline) {
			t.Fatal("a stuck root check never released its slot")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A check stuck on one root holds only checks of that root: saves that touch no root (grab off, a version back to
// shadow, the router switch) and saves of other roots go through, and a save of the stuck root names it.
func TestStuckRootCheckHoldsOnlyItsRoot(t *testing.T) {
	s, _, p, initial := versionFixture(t)
	hung := filepath.Join(p.Library.Dangling.Roots[0], "hung")
	other := filepath.Join(p.Library.Dangling.Roots[0], "other")
	for _, dir := range []string{hung, other} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	probetest.Mounts(t, hung) // the hung share
	old := rootCheckTimeout
	rootCheckTimeout = 200 * time.Millisecond
	release := make(chan struct{})
	rootStat = func(path string) (os.FileInfo, error) {
		if path == hung {
			<-release
		}
		return os.Stat(path)
	}
	t.Cleanup(func() { close(release); waitRootChecksIdle(t); rootStat, rootCheckTimeout = os.Stat, old })
	ctx := context.Background()
	if _, err := s.ValidateRoot(ctx, p, "movie-web", hung); err == nil || !strings.Contains(err.Error(), "gave up") {
		t.Fatalf("hung root: %v", err)
	}

	req := saveRequest(p, initial)
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Mode = policy.ModeShadow })
	req.Records.Switches = &GlobalSwitches{Paused: true}
	saved, err := s.Save(ctx, req)
	if err != nil {
		t.Fatalf("grab off and router off while a root check is stuck: %v", err)
	}

	req = saveRequest(p, saved)
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Library = other; v.Roots = []string{other} })
	if saved, err = s.Save(ctx, req); err != nil {
		t.Fatalf("a save of another root while a root check is stuck: %v", err)
	}

	req = saveRequest(p, saved)
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Library = hung; v.Roots = []string{hung} })
	if _, err := s.Save(ctx, req); err == nil || !strings.Contains(err.Error(), hung) || !strings.Contains(err.Error(), "still waiting") {
		t.Fatalf("a save of the stuck root: %v", err)
	}
}

// policy.Validate runs on every load and save and checks the separation of active versions' roots. When an
// active version's root stops answering, saves that turn things off still go through (Validate compares that root
// by its path after one bounded wait), and a save that checks roots names the hung root instead of hanging or
// charging the hang to the root it changes.
func TestHungActiveRootLeavesOffSavesOpen(t *testing.T) {
	m, p, initial := activeWebFixture(t)
	hung := p.Versions["movie-web"].Library
	other := filepath.Join(p.Library.Dangling.Roots[0], "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	probetest.Mounts(t, hung) // the hung share
	old, oldPolicy := rootCheckTimeout, policy.RootResolveTimeout
	rootCheckTimeout, policy.RootResolveTimeout = time.Second, 100*time.Millisecond
	release := make(chan struct{})
	policy.RootStat = func(path string) (os.FileInfo, error) {
		if path == hung {
			<-release
		}
		return os.Stat(path)
	}
	t.Cleanup(func() {
		close(release)
		waitRootChecksIdle(t)
		policy.RootStat, rootCheckTimeout, policy.RootResolveTimeout = os.Stat, old, oldPolicy
	})
	ctx := context.Background()
	start := time.Now()
	req := saveRequest(p, initial)
	req.Records.Switches = &GlobalSwitches{Paused: true}
	saved, err := m.SaveAndApply(ctx, req)
	if err != nil || time.Since(start) > 5*time.Second {
		t.Fatalf("router off while an active root hangs: %v after %s", err, time.Since(start))
	}
	// Another version's new root: the hung root of movie-web is named, the new root is not marked stuck.
	var key string
	for _, k := range sortedPolicyKeys(p.Versions) {
		if k != "movie-web" && p.Library.Versions[k] != nil {
			key = k
			break
		}
	}
	if key == "" {
		t.Fatal("fixture: no second version with library roots")
	}
	req = saveRequest(p, saved)
	editVersion(&req.Records, key, func(v *Version) {
		v.Config.Library = other
		v.Roots = []string{other}
	})
	start = time.Now()
	if _, err := m.SaveAndApply(ctx, req); err == nil || !strings.Contains(err.Error(), hung) || !strings.Contains(err.Error(), "not answering") {
		t.Fatalf("a root check beside a hung root: %v", err)
	}
	if time.Since(start) > 5*time.Second || rootprobe.Default.Stuck(other) {
		t.Fatalf("the hang was charged to %s (%s)", other, time.Since(start))
	}
	req = saveRequest(p, saved)
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Mode = policy.ModeShadow })
	if _, err := m.SaveAndApply(ctx, req); err != nil {
		t.Fatalf("the hung version back to shadow: %v", err)
	}
}

// Validating a root for a new version consults every other root and the trash.
// When an existing root hangs, each is resolved through its own probe slot: the check names the hung root, the
// healthy proposed roots are not marked stuck, and trying other proposed paths adds no blocked call on the hung share.
func TestNewVersionRootBesideHungRoot(t *testing.T) {
	s, _, p, _ := versionFixture(t)
	hung := p.Versions["movie-web"].Library
	area := p.Library.Dangling.Roots[0]
	cand := []string{filepath.Join(area, "new-a"), filepath.Join(area, "new-b")}
	for _, dir := range cand {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	probetest.Mounts(t, hung, cand[0], cand[1])
	old, oldPolicy := rootCheckTimeout, policy.RootResolveTimeout
	rootCheckTimeout, policy.RootResolveTimeout = time.Second, 100*time.Millisecond
	release := make(chan struct{})
	var hungCalls atomic.Int64
	policy.RootStat = func(path string) (os.FileInfo, error) {
		if path == hung {
			hungCalls.Add(1)
			<-release
		}
		return os.Stat(path)
	}
	t.Cleanup(func() {
		close(release)
		waitRootChecksIdle(t)
		policy.RootStat, rootCheckTimeout, policy.RootResolveTimeout = os.Stat, old, oldPolicy
	})
	ctx := context.Background()
	for _, dir := range append(cand, cand...) {
		start := time.Now()
		_, err := s.ValidateRoot(ctx, p, "brand-new", dir)
		if err == nil || !strings.Contains(err.Error(), hung) || !strings.Contains(err.Error(), "not answering") {
			t.Fatalf("%s beside a hung root: %v", dir, err)
		}
		if time.Since(start) > 3*time.Second || rootprobe.Default.Stuck(dir) {
			t.Fatalf("the hang was charged to %s (%s)", dir, time.Since(start))
		}
	}
	if n := hungCalls.Load(); n != 1 {
		t.Fatalf("%d stat calls on the hung root, want 1", n)
	}
}

// A root check holds no probe slot while it waits for another, so checks on one filesystem (a root
// slot per mount) never wait on themselves or on each other: concurrent checks of an active version's root that
// consult the same roots all answer.
func TestConcurrentRootChecksDoNotWaitOnEachOther(t *testing.T) {
	m, p, _ := activeWebFixture(t)
	area := p.Library.Dangling.Roots[0]
	old, oldPolicy := rootCheckTimeout, policy.RootResolveTimeout
	rootCheckTimeout, policy.RootResolveTimeout = 2*time.Second, time.Second
	policy.RootStat = func(path string) (os.FileInfo, error) {
		time.Sleep(5 * time.Millisecond) // a slow share that answers
		return os.Stat(path)
	}
	t.Cleanup(func() {
		waitRootChecksIdle(t)
		policy.RootStat, rootCheckTimeout, policy.RootResolveTimeout = os.Stat, old, oldPolicy
	})
	ctx := context.Background()
	errs := make(chan error, 6)
	for i := range 6 {
		dir := filepath.Join(area, fmt.Sprintf("cand-%d", i))
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		go func() {
			_, err := m.store.ValidateRoot(ctx, p, "movie-web", dir)
			errs <- err
		}()
	}
	for range 6 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent root check: %v", err)
		}
	}
}

// The filesystem root is never a library folder, given directly or through a symlink, whatever else is configured.
func TestFilesystemRootRefused(t *testing.T) {
	t.Parallel()
	_, _, p, _ := versionFixture(t)
	p.Library = nil
	alias := filepath.Join(t.TempDir(), "slash")
	if err := os.Symlink("/", alias); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"/", alias} {
		if _, err := validateOneRoot(context.Background(), policy.NewRootResolver(context.Background()), p, "new", root, false); err == nil || !strings.Contains(err.Error(), "filesystem root") {
			t.Errorf("%s: %v", root, err)
		}
	}
	// With a library mount configured, "/" is still refused as the filesystem root, not as overlapping the mount.
	_, _, withMount, _ := versionFixture(t)
	if withMount.Library.Mount.Path == "" {
		withMount.Library.Mount.Path = filepath.Join(t.TempDir(), "mount")
	}
	if _, err := validateOneRoot(context.Background(), policy.NewRootResolver(context.Background()), withMount, "new", "/", false); err == nil || err.Error() != "directory cannot be the filesystem root" {
		t.Errorf("/ with a library mount: %v", err)
	}
}
