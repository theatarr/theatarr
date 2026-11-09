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

// The arr views' RouterDisabledCheck (arrview versionHealth) follows router.enabled saved in the DB settings, without
// a restart.
func TestArrViewHealthFollowsRouterSwitch(t *testing.T) {
	t.Parallel()
	db, st, base, _ := runtimeFixture(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	area := t.TempDir()
	base.Library.Dangling.Roots = []string{area}
	initial, err := st.BootstrapSwitches(ctx, base, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
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
	save := func(edit func(*settings.Records)) {
		t.Helper()
		head, err := manager.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		req := settings.SaveRequest{ExpectedGeneration: head.Generation, Actor: "test", Records: head.Records}
		edit(&req.Records)
		proof, err := manager.Confirm(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		req.ConfirmationToken = proof.Token
		if _, err := manager.SaveAndApply(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(area, "movie-web")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	save(func(r *settings.Records) {
		for i := range r.Versions {
			if r.Versions[i].Key == "movie-web" {
				r.Versions[i].Config.Mode, r.Versions[i].Config.Grab, r.Versions[i].Config.Library = policy.ModeActive, true, root
				r.Versions[i].Roots = []string{root}
			}
		}
	})
	disabled := func() bool { return strings.Contains(fmt.Sprint(views.HealthItems(ctx)), "Router disabled") }
	if !disabled() {
		t.Fatal("fixture: router is not off for the active version")
	}
	save(func(r *settings.Records) { r.Switches.RouterEnabled = true })
	if disabled() {
		t.Fatal("health kept the router off after it was enabled")
	}
	save(func(r *settings.Records) { r.Switches.RouterEnabled = false })
	if !disabled() {
		t.Fatal("health kept the router on after it was disabled")
	}
}
