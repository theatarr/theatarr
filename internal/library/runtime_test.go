package library

import (
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/library/probe"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestQualityRuntimePinsImportAndDoesNotRenameLinks(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	started, release := make(chan struct{}), make(chan struct{})
	original := e.lib.probe
	var once sync.Once
	e.lib.probe = func(path string, maxBytes int64) (*probe.Result, error) {
		once.Do(func() { close(started); <-release })
		return original(path, maxBytes)
	}
	old := e.lib.WithPolicy(e.pol, e.lib.engine, nil, nil)
	var current atomic.Pointer[Library]
	current.Store(old)
	e.lib.SetRuntimeResolver(func(ctx context.Context) (context.Context, *Library) { return ctx, current.Load() })
	type result struct {
		plan *Plan
		err  error
	}
	done := make(chan result, 1)
	go func() { p, err := e.lib.Import(e.ctx, 1, false); done <- result{p, err} }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("import did not reach probe")
	}
	updated, err := e.pol.Clone()
	if err != nil {
		t.Fatal(err)
	}
	updated.Versions["movie-remux"].Naming.File = "NEW {Movie Title}"
	engine, err := decide.New(updated, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	current.Store(e.lib.WithPolicy(updated, engine, nil, nil))
	close(release)
	var first result
	select {
	case first = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("import did not finish")
	}
	if first.err != nil {
		t.Fatal(first.err)
	}
	if len(first.plan.Links) != 1 || strings.Contains(first.plan.Links[0].Path, "NEW ") {
		t.Fatalf("import changed generation: %+v", first.plan)
	}
	oldPath := first.plan.Links[0].Path
	target, err := os.Readlink(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	// Planning a subsequent import uses new naming; the already-created link stays at its original path.
	next, err := e.lib.Import(e.ctx, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Links) != 1 || !strings.Contains(next.Links[0].Path, "NEW ") {
		t.Fatalf("new import kept old naming: %+v", next)
	}
	still, err := os.Readlink(oldPath)
	if err != nil || still != target {
		t.Fatal("settings publication renamed existing link", err)
	}
	if _, err := os.Lstat(next.Links[0].Path); !os.IsNotExist(err) {
		t.Fatal("preview wrote a new link")
	}
}

func TestRuntimeSwitchOffStopsLibraryWrites(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	before := strings.Join(tree(t, e.dir), "\n")
	off, err := e.pol.Clone()
	if err != nil {
		t.Fatal(err)
	}
	off.Library.Enabled = false
	disabled := e.lib.WithPolicy(off, e.lib.engine, nil, nil)
	e.lib.SetRuntimeResolver(func(ctx context.Context) (context.Context, *Library) { return ctx, disabled })
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.DryRun || !strings.Contains(plan.Refused, "disabled") {
		t.Fatalf("import not gated: %+v", plan)
	}
	if strings.Join(tree(t, e.dir), "\n") != before {
		t.Fatal("disabled import changed files")
	}
	if err := disabled.w.symlink("movie-remux", "/target", e.movies+"/unexpected.mkv"); err == nil {
		t.Fatal("writer retained old gate")
	}
	if !e.lib.Gate().Enabled() {
		t.Fatal("new generation mutated old gate")
	}
}
