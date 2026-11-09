package library

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/library/probe"
	"github.com/theatarr/theatarr/internal/policy"
)

// A release folder on the mount that is a symlink out of it (or holds one) is not mount content: the import stops
// with ErrNotOnMount, links nothing, and does not judge (fail, blocklist) the download.
func TestSymlinkOutOfTheMountIsNotImported(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	real := filepath.Join(e.outside, "Release")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "movie.mkv"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(e.mount, "__all__", movieRelease)); err != nil {
		t.Fatal(err)
	}
	e.probes[filepath.Join(e.mount, "__all__", movieRelease, "movie.mkv")] = &probe.Result{Container: "matroska",
		RunTimeSeconds: 7000, Video: &probe.Video{Codec: "hevc", Width: 3840, Height: 2160}}
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	before := tree(t, e.movies)
	if _, err := e.lib.Import(e.ctx, 1, false); !errors.Is(err, ErrNotOnMount) {
		t.Fatalf("import through a symlink out of the mount: %v", err)
	}
	// A file symlinked out of a real release folder.
	e.release("Other.Release", map[string]float64{})
	if err := os.MkdirAll(filepath.Join(e.mount, "__all__", "Other.Release"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(real, "movie.mkv"), filepath.Join(e.mount, "__all__", "Other.Release",
		"movie.mkv")); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		score, episodes, provider, name, state, created_at, updated_at, ready_at) VALUES (2, 1, 10, 'movie-remux',
		'ff', 'Other.Release', 'idx', 'Remux-2160p', 100, '[]', 'rd', 'Other.Release', 'ready', 0, 0, 0)`)
	if _, err := e.lib.Import(e.ctx, 2, false); !errors.Is(err, ErrNotOnMount) {
		t.Fatalf("import of a symlinked file: %v", err)
	}
	if got := tree(t, e.movies); len(got) != len(before) {
		t.Errorf("links made: %v", got)
	}
	if got := e.one(`SELECT group_concat(state) FROM download`); got != "ready,ready" {
		t.Errorf("downloads %s, want both still ready", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM router_blocklist`); got != "0" {
		t.Errorf("%s blocklisted", got)
	}
}

// infoErrFS answers ReadDir, but every entry's Info fails as a dying FUSE mount does (EIO).
type infoErrFS struct{ osFS }

type infoErrEntry struct{ fs.DirEntry }

func (e infoErrEntry) Info() (fs.FileInfo, error) {
	return nil, &fs.PathError{Op: "lstat", Path: e.Name(), Err: syscall.EIO}
}

func (f infoErrFS) ReadDir(name string) ([]os.DirEntry, error) {
	ents, err := os.ReadDir(name)
	for i := range ents {
		ents[i] = infoErrEntry{ents[i]}
	}
	return ents, err
}

// A file whose info cannot be read is the mount failing, not a release without video: the import pauses
// (ErrMountDown) and the download is neither failed nor blocklisted.
func TestDirEntryInfoErrorPausesImport(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	e.lib.mount.fs = infoErrFS{}
	if _, err := e.lib.Import(e.ctx, 1, false); !errors.Is(err, ErrMountDown) {
		t.Fatalf("import with EIO on file info: %v", err)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "ready" {
		t.Errorf("download %s, want ready", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM router_blocklist`); got != "0" {
		t.Errorf("%s blocklisted", got)
	}
}

// Locate reads a client's symlink under the guard: a path whose directories lead into a hung mount fails with
// ErrMountDown within the guard's timeout instead of blocking, and an open circuit refuses it at once.
func TestLocateGoesThroughTheGuard(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	sfs := newSlowFS()
	m := slowMount(t, sfs, &now)
	defer sfs.answer()
	link := filepath.Join(t.TempDir(), "x.mkv")
	if err := os.Symlink(filepath.Join(m.Path, "__all__", "Rel", "x.mkv"), link); err != nil {
		t.Fatal(err)
	}
	loc, err := m.Locate(context.Background(), link)
	if err != nil || loc.Folder != "Rel" || loc.Rel != "x.mkv" {
		t.Fatalf("locate %+v %v", loc, err)
	}
	sfs.hang()
	start := time.Now()
	if _, err := m.Locate(context.Background(), link); !errors.Is(err, ErrMountDown) {
		t.Errorf("locate on a hung mount: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("locate blocked %v", time.Since(start))
	}
	// Stat (the import previews) goes through it too.
	if _, err := m.Stat(context.Background(), link); !errors.Is(err, ErrMountDown) {
		t.Errorf("stat on a hung mount: %v", err)
	}
}
