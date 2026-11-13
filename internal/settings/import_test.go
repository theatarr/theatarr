package settings

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// An import over stored settings needs every record group in its files: a directory that omits one must not
// delete the stored records of that group.
func TestImportRequiresEveryRecordGroup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _ := setup(t)
	seed := t.TempDir()
	if err := os.WriteFile(filepath.Join(seed, "policy.yaml"), []byte("indexers: {}\nproviders: {}\nbudgets:\n  kept: {requests: 5, per: 1m}\nversions: {}\nviews: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedBase, err := policy.Load(seed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Bootstrap(ctx, seedBase, ImportRequest{Dir: seed, Actor: "admin", Lookup: fixtureLookup}); err != nil {
		t.Fatal(err)
	}
	base := &policy.Policy{Dir: seed}
	before, err := s.Load(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte("provider_sync:\n  interval: 8h\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const want = "settings: import requires indexers, providers, budgets, versions and views sections"
	req := ImportRequest{Dir: dir, Base: base, Actor: "admin", ExpectedGeneration: before.Generation, Lookup: fixtureLookup}
	if _, err := s.PreviewImport(ctx, req); err == nil || err.Error() != want {
		t.Fatalf("preview: %v", err)
	}
	if _, err := s.Import(ctx, req); err == nil || err.Error() != want {
		t.Fatalf("import: %v", err)
	}
	after, err := s.Load(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if after.Generation != before.Generation || !reflect.DeepEqual(after.Records.Budgets, before.Records.Budgets) || len(after.Records.Budgets) != 1 {
		t.Fatalf("refused import changed the database: generation %d, budgets %+v", after.Generation, after.Records.Budgets)
	}
}

// A policy written before the one search schedule imports: router.wanted.enabled false gives both backlog switches
// off, as migration 00127 does to stored settings.
func TestImportLegacyWantedKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _ := setup(t)
	seed := t.TempDir()
	if err := os.WriteFile(filepath.Join(seed, "policy.yaml"), []byte("indexers: {}\nproviders: {}\nbudgets: {}\nversions: {}\nviews: {}\n"+
		"router:\n  wanted:\n    enabled: false\n    interval: 15m\n    retry_new: true\n    missing: true\n    per_hour: 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedBase, err := policy.Load(seed)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.Bootstrap(ctx, seedBase, ImportRequest{Dir: seed, Actor: "admin", Lookup: fixtureLookup})
	if err != nil {
		t.Fatal(err)
	}
	if w := snap.Policy.WantedSettings(); w.Missing || w.CutoffUnmet != policy.CutoffOff || w.PerHour != 7 {
		t.Fatalf("switches %+v", w)
	}
}
