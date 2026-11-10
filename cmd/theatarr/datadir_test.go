package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckDataDirWritable(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "new", "data")
	if err := checkDataDir(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("the check left files behind: %v %v", entries, err)
	}
}

func wantDataDirError(t *testing.T, err error, dir, what string) {
	t.Helper()
	if err == nil {
		t.Fatal("no error")
	}
	msg := err.Error()
	id := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	for _, part := range []string{"data directory " + dir + " " + what + " by uid " + id, "chown -R " + id,
		"docs/troubleshooting.md#data-directory"} {
		if !strings.Contains(msg, part) {
			t.Errorf("message lacks %q: %s", part, msg)
		}
	}
	if strings.Contains(msg, "\n") {
		t.Errorf("message is not one line: %q", msg)
	}
}

func TestCheckDataDirNotWritable(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root writes a read-only directory")
	}
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	err := checkDataDir(dir)
	wantDataDirError(t, err, dir, "is not writable")
	if !errors.Is(err, fs.ErrPermission) || !strings.Contains(err.Error(), "mode 0500") {
		t.Errorf("cause or owner missing: %v", err)
	}
}

// A path beneath a regular file fails for every user, root included.
func TestCheckDataDirCannotBeCreated(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(blocker, "data")
	wantDataDirError(t, checkDataDir(dir), dir, "cannot be created")
}

func TestDataDirErrorNamesOwner(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	err := dataDirError(dir, "is not writable", fs.ErrPermission)
	wantDataDirError(t, err, dir, "is not writable")
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("cause not wrapped: %v", err)
	}
	if fi, statErr := os.Stat(dir); statErr == nil && dirOwner(fi) != "" && !strings.Contains(err.Error(), "mode 0750): ") {
		t.Errorf("owner and mode missing: %v", err)
	}
}

// serve reports the data directory before it creates the log directory or opens the database.
func TestServeChecksDataDirFirst(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	logs := filepath.Join(tmp, "logs")
	dir := filepath.Join(blocker, "data")
	err := runServe([]string{"-db", filepath.Join(dir, "theatarr.db"), "-log-dir", logs, "-listen", "127.0.0.1:0",
		"-mount-helper=false"})
	wantDataDirError(t, err, dir, "cannot be created")
	if _, statErr := os.Stat(logs); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("the log directory was created before the check: %v", statErr)
	}
}
