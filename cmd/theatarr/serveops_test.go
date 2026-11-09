package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/theatarr/theatarr/internal/arrview"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
	"github.com/theatarr/theatarr/internal/web"
)

type renameFakeLibrary struct {
	arrview.Library
	applyErr error
	applied  []int64
}

func (l *renameFakeLibrary) PreviewRename(_ context.Context, titleID int64, _ *int) (*library.RenamePreview, error) {
	return &library.RenamePreview{Files: []library.RenameFile{{LinkID: 7, Version: "movie-remux", ExistingPath: "/a.mkv",
		NewPath: "/b.mkv"}}}, nil
}

func (l *renameFakeLibrary) ApplyRename(_ context.Context, titleID int64, _ *int) (*library.RenamePreview, error) {
	l.applied = append(l.applied, titleID)
	return &library.RenamePreview{}, l.applyErr
}

func (l *renameFakeLibrary) ReassignEpisodes(_ context.Context, linkID int64, eps []int64, replace bool) (*library.Reassigned, error) {
	l.applied = append(l.applied, linkID)
	return &library.Reassigned{LinkID: linkID, Path: "/b.mkv"}, l.applyErr
}

// TestServeOpsRename: the web UI's rename (internal/web/rename.go) reaches the library
// through serveOps; it used to answer 503 because serveOps had neither method. A gate refusal or a link that
// changed under the rename is a refusal (409), not a server error.
func TestServeOpsRename(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	au, _, err := auth.New(ctx, st, auth.Options{Logger: log, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(filepath.Join("..", "..", "examples", "policy"))
	if err != nil {
		t.Fatal(err)
	}
	lib := &renameFakeLibrary{}
	av, err := arrview.New(ctx, arrview.Config{Store: st, Auth: au, Policy: pol, Logger: log, Library: lib,
		Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	var ops web.Ops = serveOps{av}
	ren, ok := ops.(interface {
		PreviewRename(context.Context, int64, *int) (*library.RenamePreview, error)
		ApplyRename(context.Context, int64, *int) (*library.RenamePreview, error)
	})
	if !ok {
		t.Fatal("serveOps does not offer the rename to the web UI")
	}
	p, err := ren.PreviewRename(ctx, 1, nil)
	if err != nil || len(p.Files) != 1 || p.Files[0].NewPath != "/b.mkv" {
		t.Fatalf("preview %+v %v", p, err)
	}
	var refusal interface{ Refused() bool }
	for _, e := range []error{&library.Refused{Reason: "shadow", Detail: "version \"movie-remux\" is not mode: active"},
		&library.LinkChanged{ID: 7, Path: "/a.mkv", Target: "/t", Detail: "its row changed"}} {
		lib.applyErr = e
		if _, err := ren.ApplyRename(ctx, 1, nil); !errors.As(err, &refusal) {
			t.Fatalf("%v: not a refusal (%T)", e, err)
		}
	}
	lib.applyErr = nil
	if _, err := ren.ApplyRename(ctx, 1, nil); err != nil || len(lib.applied) != 3 {
		t.Fatalf("apply %v %v", err, lib.applied)
	}
}

// TestServeOpsReassign: the episode overview's "Change episodes" reaches the library through serveOps, and a gate
// refusal or a conflict is a refusal (409).
func TestServeOpsReassign(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	au, _, err := auth.New(ctx, st, auth.Options{Logger: log, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(filepath.Join("..", "..", "examples", "policy"))
	if err != nil {
		t.Fatal(err)
	}
	lib := &renameFakeLibrary{}
	av, err := arrview.New(ctx, arrview.Config{Store: st, Auth: au, Policy: pol, Logger: log, Library: lib,
		Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	var ops web.Ops = serveOps{av}
	re, ok := ops.(interface {
		ReassignEpisodes(context.Context, int64, []int64, bool) (*library.Reassigned, error)
	})
	if !ok {
		t.Fatal("serveOps does not offer the episode change to the web UI")
	}
	if r, err := re.ReassignEpisodes(ctx, 7, []int64{1}, false); err != nil || r.Path != "/b.mkv" || len(lib.applied) != 1 {
		t.Fatalf("reassign %+v %v", r, err)
	}
	lib.applyErr = &library.Refused{Reason: "reassign", Detail: "S01E02 already has a file in this library"}
	var refusal interface{ Refused() bool }
	if _, err := re.ReassignEpisodes(ctx, 7, []int64{1}, false); !errors.As(err, &refusal) {
		t.Fatalf("not a refusal (%T)", err)
	}
}
