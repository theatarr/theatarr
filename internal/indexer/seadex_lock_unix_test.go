//go:build unix

package indexer

import (
	"context"
	"os"
	"syscall"
	"testing"
)

func TestReadOnlySidecarFallback(t *testing.T) {
	st := lockMappedStore(t)
	path := st.Path() + ".seadex-lock"
	if err := os.WriteFile(path, nil, 0444); err != nil {
		t.Fatal(err)
	}
	var flags []int
	f, err := openSeaDexLock(st.Path(), func(path string, flag int, mode os.FileMode) (*os.File, error) {
		flags = append(flags, flag)
		if flag == os.O_RDWR|os.O_CREATE|syscall.O_NONBLOCK|syscall.O_NOFOLLOW {
			return nil, os.ErrPermission
		} // reproduce the service uid opening a root-created sidecar
		return os.OpenFile(path, flag, mode)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if len(flags) != 2 || flags[1] != os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW {
		t.Fatalf("open flags=%v", flags)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("read-only flock: %v", err)
	}
	dbInfo, err := os.Stat(st.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	} // only this isolated test owns the sidecar
	unlock, err := (&Evidence{st: st}).lockSeaDex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	lockInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	dbOwner, lockOwner := dbInfo.Sys().(*syscall.Stat_t), lockInfo.Sys().(*syscall.Stat_t)
	if dbOwner.Uid != lockOwner.Uid || dbOwner.Gid != lockOwner.Gid {
		t.Fatal("sidecar owner differs from database")
	}
}

func TestMemoryLockEntriesReleased(t *testing.T) {
	key := new(int)
	unlock, err := lockSeaDexMemory(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := lockSeaDexMemory(ctx, key); err == nil {
		t.Fatal("cancelled waiter acquired lock")
	}
	unlock()
	seaDexMemoryLocks.Lock()
	defer seaDexMemoryLocks.Unlock()
	if _, ok := seaDexMemoryLocks.entries[key]; ok {
		t.Fatal("unused memory lock retained")
	}
}
