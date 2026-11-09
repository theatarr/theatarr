//go:build unix

package indexer

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestLockUsesDeployerPermissions(t *testing.T) {
	db := filepath.Join(t.TempDir(), "db")
	if err := os.WriteFile(db, nil, 0600); err != nil {
		t.Fatal(err)
	}
	// The sidecar uses the requested mode masked by umask, independent of DB mode.
	old := syscall.Umask(0007)
	f, err := openSeaDexLock(db, os.OpenFile)
	syscall.Umask(old)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	info, err := os.Stat(db + ".seadex-lock")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0660 {
		t.Errorf("mode=%o, want 660", info.Mode().Perm())
	}
	// A pre-provisioned read-only lock is usable without repair.
	if err := os.Chmod(db+".seadex-lock", 0440); err != nil {
		t.Fatal(err)
	}
	f, err = openSeaDexLock(db, os.OpenFile)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	info, err = os.Stat(db + ".seadex-lock")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0440 {
		t.Errorf("provisioned mode changed to %o", info.Mode().Perm())
	}
	files, err := os.ReadDir(filepath.Dir(db))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Errorf("lock created auxiliary files: %v", files)
	}
}
