package settings

import (
	"context"
	"testing"
)

// The TRaSH data bundled with the binary serves the profile presets of the web UI only: quality sync without a
// TRaSH root still fails as before and leaves no review, no run of its own making and no new generation.
func TestQualitySyncWithoutRootStillFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, base, initial := syncService(t)
	req := saveRequest(base, initial)
	req.Records.SyncSources = []QualitySyncSource{{Record: Record{Key: "guides"}, Config: QualitySyncConfig{Kind: "trash", Flavour: "radarr", Enabled: true, Mode: "auto"}}}
	saved, err := svc.Coordinator.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	source := saved.Records.SyncSources[0]
	svc.TrashRoot = ""
	if p, err := svc.Preview(ctx, source); err == nil {
		t.Fatalf("preview without a TRaSH root: %+v", p)
	}
	if p, err := svc.PreviewAndStore(ctx, source); err == nil {
		t.Fatalf("stored preview without a TRaSH root: %+v", p)
	}
	if err := svc.RunSource(ctx, source, true); err == nil {
		t.Fatal("sync run without a TRaSH root succeeded")
	}
	pending, err := svc.Reviews(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("reviews without a TRaSH root: %+v", pending)
	}
	runs, err := svc.Runs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Outcome != "error" || runs[0].ReviewID != nil {
		t.Fatalf("run without a TRaSH root: %+v", runs)
	}
	head, err := svc.Store.Load(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if head.Generation != saved.Generation || svc.Coordinator.Current().Generation != saved.Generation {
		t.Fatalf("generation moved: %d, %d, was %d", head.Generation, svc.Coordinator.Current().Generation, saved.Generation)
	}
}
