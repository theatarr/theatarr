package settings

import (
	"context"
	"testing"

	"github.com/theatarr/theatarr/internal/trashbundle"
)

// Quality sync without a TRaSH root (THEATARR_TRASH unset, the default empty origin) previews and runs against the
// TRaSH data bundled with the binary, attributed to the bundle's commit.
func TestQualitySyncWithoutRootUsesBundle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, base, initial := syncService(t)
	req := saveRequest(base, initial)
	req.Records.SyncSources = []QualitySyncSource{{Record: Record{Key: "guides"}, Config: QualitySyncConfig{Kind: "trash", Flavour: "radarr", Enabled: true}}}
	saved, err := svc.Coordinator.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	source := saved.Records.SyncSources[0]
	svc.TrashRoot = ""
	p, err := svc.Preview(ctx, source)
	if err != nil {
		t.Fatalf("preview without a TRaSH root: %v", err)
	}
	if p.Commit != trashbundle.Commit() {
		t.Fatalf("preview commit %q, want the bundle's %q", p.Commit, trashbundle.Commit())
	}
	if err := svc.RunSource(ctx, source, true); err != nil {
		t.Fatalf("sync run without a TRaSH root: %v", err)
	}
	runs, err := svc.Runs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Outcome == "error" {
		t.Fatalf("run without a TRaSH root: %+v", runs)
	}
}

// A scheduled run without a TRaSH root fails as before the bundle: an error run, no log entry, no new generation.
func TestQualitySyncScheduledWithoutRootStillFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, base, initial := syncService(t)
	req := saveRequest(base, initial)
	req.Records.SyncSources = []QualitySyncSource{{Record: Record{Key: "guides"}, Config: QualitySyncConfig{Kind: "trash", Flavour: "radarr", Enabled: true}}}
	saved, err := svc.Coordinator.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	svc.TrashRoot = ""
	_ = svc.RunDue(ctx)
	runs, err := svc.Runs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Outcome != "error" || runs[0].ReviewID != nil {
		t.Fatalf("scheduled run without a TRaSH root: %+v", runs)
	}
	if changes, err := svc.Changes(ctx, 10); err != nil || len(changes) != 0 {
		t.Fatalf("update log: %+v, %v", changes, err)
	}
	if g := svc.Coordinator.Current().Generation; g != saved.Generation {
		t.Fatalf("generation moved to %d, was %d", g, saved.Generation)
	}
}
