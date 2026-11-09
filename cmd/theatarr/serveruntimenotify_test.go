package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/arrview"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/web"
)

// Notification payloads and the Health webhook resolve the arr views of the current settings generation, so a
// version bound to a view after startup is in them; an operation that captured the older generation keeps it.
func TestNotifyViewsFollowSettingsRuntime(t *testing.T) {
	t.Parallel()
	db, st, base, initial := runtimeFixture(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	area := t.TempDir()
	base.Library.Dangling.Roots = []string{area}
	initial.Policy.Library.Dangling.Roots = []string{area}
	au, _, err := auth.New(ctx, db, auth.Options{Logger: log, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	services := &serveHTTPServices{ctx: ctx}
	defer services.Close()
	build := func(ctx context.Context, snap *settings.Snapshot) (*serveRuntime, error) {
		lb := &serveLibrary{pol: snap.Policy, lib: library.New(library.Options{Store: db, Policy: snap.Policy, Logger: log})}
		h, err := newServeHTTPRuntime(ctx, arrview.Config{Store: db, Auth: au, Policy: snap.Policy, Library: lb.lib, Logger: log, Getenv: func(string) string { return "" }}, web.Config{Store: db, Auth: au, Policy: snap.Policy, Logger: log}, services)
		return &serveRuntime{Policy: snap.Policy, Library: lb, HTTP: h}, err
	}
	first, err := build(ctx, initial)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := settings.NewRuntimeCoordinator(st, base, initial, first, func(ctx context.Context, snap *settings.Snapshot, _ *settings.Runtime) (any, error) {
		return build(ctx, snap)
	})
	if err != nil {
		t.Fatal(err)
	}
	views := runtimeArrView{manager: manager, fallback: first.HTTP.av}
	oldCtx := manager.Capture(ctx)
	root := filepath.Join(area, "new-version")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	req := settings.SaveRequest{ExpectedGeneration: initial.Generation, Actor: "test", Records: initial.Records}
	v := *initial.Policy.Versions["movie-web"]
	v.Library, v.Naming = root, policy.Naming{}
	req.Records.Versions = append(req.Records.Versions, settings.Version{Record: settings.Record{Key: "new-version"}, Config: v, Roots: []string{root}})
	for i, view := range req.Records.Views {
		if view.Key == "radarr" {
			view.Versions, view.Profiles = []string{"new-version"}, nil
			req.Records.Views[i] = view
		}
	}
	if _, err := manager.SaveAndApply(ctx, req); err != nil {
		t.Fatal(err)
	}
	health := func(ctx context.Context) string { return fmt.Sprint(views.HealthItems(ctx)) }
	if text := health(ctx); !strings.Contains(text, "new-version") {
		t.Fatalf("Health webhook uses the startup views: %s", text)
	}
	if text := fmt.Sprint(first.HTTP.av.HealthItems(ctx)); strings.Contains(text, "new-version") {
		t.Fatalf("fixture: the startup views already hold the new version: %s", text)
	}
	if text := health(oldCtx); strings.Contains(text, "new-version") {
		t.Fatalf("a captured operation moved to the new generation: %s", text)
	}
}
