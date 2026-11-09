package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

func TestVersionRuntimeRoutesAndLibraryStayPinned(t *testing.T) {
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
	key, _, err := au.CreateKey(ctx, "versions-runtime", 1)
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
	first.Library.manager = manager
	oldCtx := manager.Capture(ctx)
	handler := runtimeHTTPHandler(manager, first.HTTP.mux)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan *httptest.ResponseRecorder, 1)
	first.HTTP.mux.HandleFunc("GET /in-flight", func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		fmt.Fprint(w, settings.ContextGeneration(r.Context()))
	})
	go func() {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/in-flight", nil))
		done <- w
	}()
	<-entered
	root := filepath.Join(area, "new-version")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	req := settings.SaveRequest{ExpectedGeneration: initial.Generation, Actor: "test", Records: initial.Records}
	v := *initial.Policy.Versions["movie-web"]
	v.Library = root
	v.Naming = policy.Naming{}
	req.Records.Versions = append(req.Records.Versions, settings.Version{Record: settings.Record{Key: "new-version"}, Config: v, Roots: []string{root}})
	for i, view := range req.Records.Views {
		if view.Key == "radarr" {
			view.Versions = []string{"new-version"}
			view.Profiles = nil
			req.Records.Views[i] = view
		}
	}
	if _, err := manager.SaveAndApply(ctx, req); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if w := <-done; w.Code != 200 || w.Body.String() != fmt.Sprint(initial.Generation) {
		t.Fatal("in-flight route dropped its runtime")
	}
	_, oldLib := first.Library.forOperation(oldCtx)
	_, newLib := first.Library.forOperation(ctx)
	if oldLib != first.Library || oldLib == newLib || len(oldLib.lib.Gate().Roots("new-version")) != 0 || len(newLib.lib.Gate().Roots("new-version")) != 1 {
		t.Fatal("library roots mixed generations")
	}
	for _, tc := range []struct {
		ctx     context.Context
		code    int
		binding string
	}{{ctx, 200, "new-version"}, {oldCtx, 404, initial.Policy.Versions["movie-web"].Library}} {
		r := httptest.NewRequest("GET", "/v/new-version/radarr/api/v3/system/status", nil).WithContext(tc.ctx)
		r.Header.Set(auth.APIKeyHeader, key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("per-version route: %d: %s", w.Code, w.Body)
		}
		r = httptest.NewRequest("GET", "/radarr/api/v3/rootfolder", nil).WithContext(tc.ctx)
		r.Header.Set(auth.APIKeyHeader, key)
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		var roots []map[string]any
		if err = json.Unmarshal(w.Body.Bytes(), &roots); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || !strings.Contains(w.Body.String(), tc.binding) {
			t.Fatalf("view binding ignored runtime: %s", w.Body)
		}
	}
}
