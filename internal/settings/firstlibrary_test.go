package settings

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// Setup saves the mount and the trash before the first library: that library section has no area yet (no
// dangling roots, no version roots), so the first root is checked against its own area instead of being refused.
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
	if snap.Policy.Library == nil || hasLibraryArea(snap.Policy) {
		t.Fatalf("fixture: want a library section without an area, got %+v", snap.Policy.Library)
	}
	saved := createFirstLibrary(t, m, st, base, snap, movie, root)
	// Once the first library is saved its root's parent is the area: a root elsewhere is refused again.
	other := t.TempDir()
	if _, err := st.ValidateRoot(context.Background(), base, "tv", other); err == nil || !strings.Contains(err.Error(), "outside the configured library area") {
		t.Fatalf("root outside the area after the first library: %v", err)
	}
	req = saveRequest(base, saved)
	second := movie
	second.ID, second.Key = 0, "movies-other"
	second.Config.Library, second.Config.Client, second.Roots = other, "", []string{other}
	req.Records.Versions = append(req.Records.Versions, second)
	if _, err := saveConfirmed(t, m, req); err == nil || !strings.Contains(err.Error(), "outside the configured library area") {
		t.Fatalf("second library outside the area: %v", err)
	}
}

// On a database with no library section at all the first library is accepted the same way.
func TestFirstLibraryOnEmptyDatabase(t *testing.T) {
	t.Parallel()
	m, st, base, snap, movie := firstLibraryEnv(t)
	root, _ := firstLibraryDirs(t)
	createFirstLibrary(t, m, st, base, snap, movie, root)
}

// With an area in force a save cannot widen the area and add a root in the new part at once.
func TestLibraryAreaNotWidenedWithNewRoot(t *testing.T) {
	t.Parallel()
	s, _, p, initial := versionFixture(t)
	m := versionCoordinator(t, s, p, initial)
	wider := t.TempDir()
	root := filepath.Join(wider, "new-root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	req := saveRequest(p, initial)
	for i, section := range req.Records.Sections {
		if section.Key == "library" {
			var config map[string]any
			if err := json.Unmarshal(section.Config, &config); err != nil {
				t.Fatal(err)
			}
			config["dangling"] = map[string]any{"roots": append(p.Library.Dangling.Roots, wider)}
			b, _ := json.Marshal(config)
			req.Records.Sections[i].Config = b
		}
	}
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Library = root; v.Roots = []string{root} })
	if _, err := saveConfirmed(t, m, req); err == nil || !strings.Contains(err.Error(), "outside the configured library area") {
		t.Fatalf("widened the area and added a root in it at once: %v", err)
	}
}

// The way out a refusal names lists the areas in force and the new root's folder, never /.
func TestWiderAreaNamesBothAreas(t *testing.T) {
	t.Parallel()
	top := &policy.Policy{Versions: map[string]*policy.VersionPolicy{"movies": {Library: "/movies"}, "tv": {Library: "/tv"}}}
	if got, want := widerArea(top, "/tv"), []string{"/movies", "/tv"}; !slices.Equal(got, want) {
		t.Fatalf("top-level: %v, want %v", got, want)
	}
	deep := &policy.Policy{Versions: map[string]*policy.VersionPolicy{"movies": {Library: "/media/movies/hd"}}}
	if got, want := widerArea(deep, "/data/tv/hd"), []string{"/data/tv", "/media/movies"}; !slices.Equal(got, want) {
		t.Fatalf("deep: %v, want %v", got, want)
	}
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

// An install whose library sits directly under / and has no explicit area takes a deeper root elsewhere, as before
// the top-level rule: the candidate's own area holds it.
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

// A second top-level library is refused, naming both folders and never /; doing what it says saves and activates both.
func TestSecondTopLevelLibraryNamesBothAreas(t *testing.T) {
	t.Parallel()
	m, _, base, snap, movie := firstLibraryEnv(t)
	dirs := topLevelDirs(2)
	if len(dirs) < 2 {
		t.Skip("fewer than two top-level directories")
	}
	snap = withTrash(t, m, base, snap)
	saved, err := addLibrary(t, m, base, snap, movie, "movies", dirs[0])
	if err != nil {
		t.Fatal(err)
	}
	_, err = addLibrary(t, m, base, saved, movie, "tv", dirs[1])
	named := []string{min(dirs[0], dirs[1]), max(dirs[0], dirs[1])}
	if err == nil || !strings.Contains(err.Error(), "as "+strings.Join(named, ", ")+", then") || strings.Contains(err.Error(), "as /,") || strings.Contains(err.Error(), ", /,") {
		t.Fatalf("refusal: %v (want %v)", err, named)
	}
	followRefusal(t, m, base, saved, movie, named, dirs[1])
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

// followRefusal does what the area refusal says: library.dangling.roots as named, then the "tv" library at second,
// then both libraries active.
func followRefusal(t *testing.T, m *RuntimeCoordinator, base *policy.Policy, saved *Snapshot, movie Version, named []string, second string) {
	t.Helper()
	req := saveRequest(base, saved)
	for i, section := range req.Records.Sections {
		if section.Key == "library" {
			var config map[string]any
			if err := json.Unmarshal(section.Config, &config); err != nil {
				t.Fatal(err)
			}
			config["dangling"] = map[string]any{"roots": named}
			req.Records.Sections[i].Config, _ = json.Marshal(config)
		}
	}
	saved, err := saveConfirmed(t, m, req)
	if err != nil {
		t.Fatalf("dangling roots as named: %v", err)
	}
	if saved, err = addLibrary(t, m, base, saved, movie, "tv", second); err != nil {
		t.Fatalf("second library after the named area: %v", err)
	}
	req = saveRequest(base, saved)
	for i := range req.Records.Versions {
		req.Records.Versions[i].Config.Mode = policy.ModeActive
	}
	if saved, err = saveConfirmed(t, m, req); err != nil {
		t.Fatalf("activate both: %v", err)
	}
	for _, key := range []string{"movies", "tv"} {
		if saved.Policy.Versions[key].Mode != policy.ModeActive {
			t.Fatalf("%s not active", key)
		}
	}
}

// Doing what the refusal says works: save library.dangling.roots as named, add the library, make both active.
func TestLibraryAreaErrorNamesTheWayOut(t *testing.T) {
	t.Parallel()
	m, _, base, snap, movie := firstLibraryEnv(t)
	dir := t.TempDir()
	first, second := filepath.Join(dir, "m", "movies"), filepath.Join(dir, "t", "tv")
	for _, d := range []string{first, second} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	snap = withTrash(t, m, base, snap)
	saved, err := addLibrary(t, m, base, snap, movie, "movies", first)
	if err != nil {
		t.Fatal(err)
	}
	_, err = addLibrary(t, m, base, saved, movie, "tv", second)
	named := []string{filepath.Join(dir, "m"), filepath.Join(dir, "t")}
	if err == nil || !strings.Contains(err.Error(), "outside the configured library area") || !strings.Contains(err.Error(), "library.dangling.roots") ||
		!strings.Contains(err.Error(), "as "+strings.Join(named, ", ")+", then") {
		t.Fatalf("refusal: %v", err)
	}
	followRefusal(t, m, base, saved, movie, named, second)
}
