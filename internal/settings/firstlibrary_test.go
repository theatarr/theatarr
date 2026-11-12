package settings

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// firstLibraryEnv is a fresh database (generation 0, no library section) with the example's profiles, indexers and
// providers but no version, as Setup leaves it before its library step. It returns the coordinator, the base
// policy, that snapshot and the example's movie version to create.
func firstLibraryEnv(t *testing.T) (*RuntimeCoordinator, *Store, *policy.Policy, *Snapshot, Version) {
	t.Helper()
	example, _, p := setup(t)
	bootstrap(t, example, p)
	seeded, err := example.BootstrapQuality(context.Background(), p, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	st, _, _ := setup(t)
	ctx := context.Background()
	base := &policy.Policy{}
	fresh, err := st.Load(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	m := versionCoordinator(t, st, base, fresh)
	req := saveRequest(base, seeded)
	req.ExpectedGeneration = fresh.Generation
	var movie Version
	for _, v := range req.Records.Versions {
		if v.Key == "movie-web" {
			movie = v
		}
	}
	req.Records.Versions, req.Records.Views, req.Records.Naming, req.Records.Sections = nil, nil, nil, nil
	r := &req.Records
	for i := range r.Indexers {
		r.Indexers[i].ID = 0
	}
	for i := range r.Providers {
		r.Providers[i].ID = 0
	}
	for i := range r.Budgets {
		r.Budgets[i].ID = 0
	}
	for i := range r.Profiles {
		r.Profiles[i].ID = 0
	}
	for i := range r.Formats {
		r.Formats[i].ID = 0
	}
	for i := range r.FormatSets {
		r.FormatSets[i].ID = 0
	}
	for i := range r.SyncSources {
		r.SyncSources[i].ID = 0
	}
	snap, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Policy.Library != nil {
		t.Fatalf("fixture has a library section: %+v", snap.Policy.Library)
	}
	return m, st, base, snap, movie
}

// saveConfirmed saves req, confirming its effects when the save asks for it.
func saveConfirmed(t *testing.T, m *RuntimeCoordinator, req SaveRequest) (*Snapshot, error) {
	t.Helper()
	ctx := context.Background()
	if proof, err := m.Confirm(ctx, req); err == nil && proof.Token != "" {
		req.ConfirmationToken = proof.Token
	}
	return m.SaveAndApply(ctx, req)
}

// firstLibraryDirs makes a library root and a trash directory, as the quick start has the administrator do.
func firstLibraryDirs(t *testing.T) (root, trash string) {
	t.Helper()
	dir := t.TempDir()
	root, trash = filepath.Join(dir, "media", "movies"), filepath.Join(dir, "media", ".trash")
	for _, d := range []string{root, trash} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return root, trash
}

// createFirstLibrary checks root as Setup's Check root does, then saves movie as the version "movies" at root.
func createFirstLibrary(t *testing.T, m *RuntimeCoordinator, st *Store, base *policy.Policy, snap *Snapshot, movie Version, root string) *Snapshot {
	t.Helper()
	checked, err := st.ValidateRoot(context.Background(), base, "movies", root)
	if err != nil || checked.Resolved != root || checked.Writable {
		t.Fatalf("root check of the first library: %+v %v", checked, err)
	}
	req := saveRequest(base, snap)
	movie.ID, movie.Key = 0, "movies"
	movie.Config.Library, movie.Config.Client, movie.Roots = root, "", []string{root}
	req.Records.Versions = []Version{movie}
	saved, err := saveConfirmed(t, m, req)
	if err != nil {
		t.Fatalf("first library: %v", err)
	}
	if saved.Policy.Versions["movies"] == nil || saved.Policy.Versions["movies"].Library != root {
		t.Fatalf("first library not saved: %+v", saved.Policy.Versions)
	}
	return saved
}

// Setup saves the mount and the trash before the first library; the first library then saves.
func TestFirstLibraryAfterMountAndTrash(t *testing.T) {
	t.Parallel()
	m, st, base, snap, movie := firstLibraryEnv(t)
	root, trash := firstLibraryDirs(t)
	req := saveRequest(base, snap)
	req.Records.Sections = []Section{{Record: Record{Key: "library"}, Config: json.RawMessage(`{"mount":{"path":"/mnt/debrid","link_path":"/mnt/debrid"},"trash":{"dir":"` + trash + `"}}`)}}
	snap, err := saveConfirmed(t, m, req)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Policy.Library == nil {
		t.Fatalf("fixture: want a library section, got %+v", snap.Policy.Library)
	}
	createFirstLibrary(t, m, st, base, snap, movie, root)
}

// On a database with no library section at all the first library is accepted the same way.
func TestFirstLibraryOnEmptyDatabase(t *testing.T) {
	t.Parallel()
	m, st, base, snap, movie := firstLibraryEnv(t)
	root, _ := firstLibraryDirs(t)
	createFirstLibrary(t, m, st, base, snap, movie, root)
}

// topLevelDirs is up to n existing directories directly under /, for roots that must sit there (only stat'ed).
func topLevelDirs(n int) []string {
	var out []string
	for _, dir := range []string{"/srv", "/opt", "/mnt", "/home", "/usr", "/var"} {
		if info, err := os.Lstat(dir); err == nil && info.IsDir() && len(out) < n {
			out = append(out, dir)
		}
	}
	return out
}

// needWritable skips a test that makes a library Live at existing top-level directories when the test user cannot
// write there (a Live library's root must be writable); CI runners are not root.
func needWritable(t *testing.T, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		if syscall.Access(dir, 2) != nil {
			t.Skipf("%s is not writable by this user", dir)
		}
	}
}

// Review 3.5: a first library whose root sits directly under / passes Check root and saves.
func TestFirstLibraryTopLevelRoot(t *testing.T) {
	t.Parallel()
	m, st, base, snap, movie := firstLibraryEnv(t)
	dirs := topLevelDirs(1)
	if len(dirs) == 0 {
		t.Skip("no top-level directory to use")
	}
	root := dirs[0]
	if _, err := st.ValidateRoot(context.Background(), base, "movies", root); err != nil {
		t.Fatalf("check root: %v", err)
	}
	req := saveRequest(base, snap)
	movie.ID, movie.Key = 0, "movies"
	movie.Config.Library, movie.Config.Client, movie.Roots = root, "", []string{root}
	req.Records.Versions = []Version{movie}
	if _, err := saveConfirmed(t, m, req); err != nil {
		t.Fatalf("first library at %s: %v", root, err)
	}
}

// addLibrary saves v as a new library key at root over saved.
func addLibrary(t *testing.T, m *RuntimeCoordinator, base *policy.Policy, saved *Snapshot, v Version, key, root string) (*Snapshot, error) {
	t.Helper()
	req := saveRequest(base, saved)
	v.ID, v.Key = 0, key
	v.Config.Library, v.Config.Client, v.Roots = root, "", []string{root}
	req.Records.Versions = append(req.Records.Versions, v)
	return saveConfirmed(t, m, req)
}

// An install whose library sits directly under / takes a deeper root elsewhere.
func TestTopLevelLibraryThenDeeperRoot(t *testing.T) {
	t.Parallel()
	m, _, base, snap, movie := firstLibraryEnv(t)
	dirs := topLevelDirs(1)
	if len(dirs) == 0 {
		t.Skip("no top-level directory to use")
	}
	saved, err := addLibrary(t, m, base, snap, movie, "movies", dirs[0])
	if err != nil {
		t.Fatal(err)
	}
	tv := filepath.Join(t.TempDir(), "tv")
	if err := os.Mkdir(tv, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := addLibrary(t, m, base, saved, movie, "tv", tv); err != nil {
		t.Fatalf("deeper root after a top-level library: %v", err)
	}
}

// A second top-level library (/tv after /movies) saves and goes Live.
func TestSecondTopLevelLibrarySaves(t *testing.T) {
	t.Parallel()
	m, _, base, snap, movie := firstLibraryEnv(t)
	dirs := topLevelDirs(2)
	if len(dirs) < 2 {
		t.Skip("fewer than two top-level directories")
	}
	needWritable(t, dirs...)
	snap = withTrash(t, m, base, snap)
	saved, err := addLibrary(t, m, base, snap, movie, "movies", dirs[0])
	if err != nil {
		t.Fatal(err)
	}
	if saved, err = addLibrary(t, m, base, saved, movie, "tv", dirs[1]); err != nil {
		t.Fatalf("second top-level library: %v", err)
	}
	activateAll(t, m, base, saved, "movies", "tv")
}

// withTrash saves a trash folder, which making a library active needs.
func withTrash(t *testing.T, m *RuntimeCoordinator, base *policy.Policy, snap *Snapshot) *Snapshot {
	t.Helper()
	trash := filepath.Join(t.TempDir(), ".trash")
	if err := os.Mkdir(trash, 0o700); err != nil {
		t.Fatal(err)
	}
	req := saveRequest(base, snap)
	req.Records.Sections = []Section{{Record: Record{Key: "library"}, Config: json.RawMessage(`{"trash":{"dir":"` + trash + `"}}`)}}
	next, err := saveConfirmed(t, m, req)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

// activateAll makes every library of saved Live and checks that keys ended Live.
func activateAll(t *testing.T, m *RuntimeCoordinator, base *policy.Policy, saved *Snapshot, keys ...string) {
	t.Helper()
	req := saveRequest(base, saved)
	for i := range req.Records.Versions {
		req.Records.Versions[i].Config.Mode = policy.ModeActive
	}
	saved, err := saveConfirmed(t, m, req)
	if err != nil {
		t.Fatalf("activate all: %v", err)
	}
	for _, k := range keys {
		if saved.Policy.Versions[k].Mode != policy.ModeActive {
			t.Fatalf("%s not active", k)
		}
	}
}

// A library folder may be anywhere: with /…/a/movies Live and library.dangling.roots at /…/a, /…/b/tv passes Check
// root and saves; a folder inside /…/a/movies is still refused as an overlap.
func TestLibraryFolderAnywhere(t *testing.T) {
	t.Parallel()
	m, st, base, snap, movie := firstLibraryEnv(t)
	dir := t.TempDir()
	movies, tv, inside := filepath.Join(dir, "a", "movies"), filepath.Join(dir, "b", "tv"), filepath.Join(dir, "a", "movies", "sub")
	for _, d := range []string{inside, tv} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	snap = withTrash(t, m, base, snap)
	req := saveRequest(base, snap)
	for i, section := range req.Records.Sections {
		if section.Key == "library" {
			var config map[string]any
			if err := json.Unmarshal(section.Config, &config); err != nil {
				t.Fatal(err)
			}
			config["dangling"] = map[string]any{"roots": []string{filepath.Join(dir, "a")}}
			req.Records.Sections[i].Config, _ = json.Marshal(config)
		}
	}
	saved, err := saveConfirmed(t, m, req)
	if err != nil {
		t.Fatal(err)
	}
	if saved, err = addLibrary(t, m, base, saved, movie, "movies", movies); err != nil {
		t.Fatal(err)
	}
	activateAll(t, m, base, saved, "movies")
	saved, err = st.Load(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ValidateRoot(context.Background(), base, "tv", tv); err != nil {
		t.Fatalf("check root outside the first library's folder: %v", err)
	}
	if saved, err = addLibrary(t, m, base, saved, movie, "tv", tv); err != nil {
		t.Fatalf("library outside the first library's folder: %v", err)
	}
	if _, err := st.ValidateRoot(context.Background(), base, "anime", inside); err == nil || !strings.Contains(err.Error(), "overlaps library movies") {
		t.Fatalf("check root inside another library: %v", err)
	}
	if _, err := addLibrary(t, m, base, saved, movie, "anime", inside); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("library inside another library: %v", err)
	}
}

// A Watch only library at a top-level folder can later be made Live.
func TestTopLevelWatchOnlyThenLive(t *testing.T) {
	t.Parallel()
	m, _, base, snap, movie := firstLibraryEnv(t)
	dirs := topLevelDirs(1)
	if len(dirs) == 0 {
		t.Skip("no top-level directory to use")
	}
	needWritable(t, dirs...)
	snap = withTrash(t, m, base, snap)
	saved, err := addLibrary(t, m, base, snap, movie, "movies", dirs[0])
	if err != nil {
		t.Fatal(err)
	}
	if saved.Policy.Versions["movies"].Mode == policy.ModeActive {
		t.Fatal("created live; want Watch only first")
	}
	activateAll(t, m, base, saved, "movies")
}
