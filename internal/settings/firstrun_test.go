package settings

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// A fresh install: the first bootstrap of an empty database from a missing or empty policy directory saves an empty
// generation instead of failing, and the quality bootstrap after it too. An explicit import still needs every
// section.
func TestBootstrapFirstRunWithoutPolicy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, dir := range []string{t.TempDir(), filepath.Join(t.TempDir(), "missing")} {
		db, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if _, err := db.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		s, err := New(db)
		if err != nil {
			t.Fatal(err)
		}
		base, err := ReadBase(ctx, db, dir)
		if err != nil {
			t.Fatal(err)
		}
		snap, err := s.Bootstrap(ctx, base, ImportRequest{Dir: dir, Actor: "admin", Lookup: fixtureLookup})
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		if snap.Generation == 0 || len(snap.Policy.Versions) != 0 || len(snap.Records.Indexers) != 0 || len(snap.Records.Providers) != 0 {
			t.Fatalf("snapshot = gen %d, %d versions", snap.Generation, len(snap.Policy.Versions))
		}
		if _, err := s.BootstrapQuality(ctx, base, "admin"); err != nil {
			t.Fatalf("quality bootstrap: %v", err)
		}
		loaded, err := s.Load(ctx, base)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Generation < snap.Generation {
			t.Fatalf("load = gen %d", loaded.Generation)
		}
		_, err = s.Import(ctx, ImportRequest{Dir: dir, Actor: "admin", Lookup: fixtureLookup, Base: base, ExpectedGeneration: loaded.Generation})
		if err == nil || !strings.Contains(err.Error(), "cannot parse policy import") {
			t.Fatalf("import of an empty directory: %v", err)
		}
	}
}
