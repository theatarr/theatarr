package store

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func requireMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s: mode %o, want %o", filepath.Base(path), got, want)
	}
}

// The database holds plaintext provider and indexer keys, so it, its WAL/SHM and its backups are
// owner-only from their first byte, whatever the umask; a data or backup directory Theatarr creates is too.
func TestDatabaseAndBackupsAreOwnerOnly(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	ctx := context.Background()
	root := t.TempDir()
	db := filepath.Join(root, "data", "theatarr.db")
	st, err := Open(ctx, db, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	requireMode(t, filepath.Dir(db), 0o700)
	for _, p := range []string{db, db + "-wal", db + "-shm"} {
		requireMode(t, p, 0o600)
	}
	backup := filepath.Join(root, "backups", "theatarr-1.db")
	if err := st.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	requireMode(t, filepath.Dir(backup), 0o700)
	requireMode(t, backup, 0o600)

	// An existing database left world-readable is tightened on open.
	loose := filepath.Join(root, "loose.db")
	st2, err := Open(ctx, loose, Options{})
	if err != nil {
		t.Fatal(err)
	}
	st2.Close()
	if err := os.Chmod(loose, 0o644); err != nil {
		t.Fatal(err)
	}
	st2, err = Open(ctx, loose, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	requireMode(t, loose, 0o600)
}
