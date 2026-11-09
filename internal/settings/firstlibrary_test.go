package settings

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
