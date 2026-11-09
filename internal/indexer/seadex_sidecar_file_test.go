//go:build unix

package indexer

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestNonRegularSidecarMustNotBlock(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "db.sqlite")
	if err := os.WriteFile(db, []byte("db"), 0600); err != nil {
		t.Fatal(err)
	}
	sidecar := db + ".seadex-lock"
	if err := syscall.Mkfifo(sidecar, 0444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sidecar, 0444); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		f, err := openSeaDexLock(db, func(path string, flag int, mode os.FileMode) (*os.File, error) {
			if flag&os.O_RDWR != 0 {
				return nil, os.ErrPermission
			} // service uid cannot write a 0444 FIFO
			return os.OpenFile(path, flag, mode)
		})
		if err == nil {
			f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("non-regular sidecar accepted")
		}
	case <-time.After(700 * time.Millisecond):
		t.Fatal("read-only FIFO open blocked despite context deadline")
	}
}
