//go:build unix

package indexer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

func lockSeaDexPath(ctx context.Context, path string) (func(), error) {
	f, err := openSeaDexLock(path, os.OpenFile)
	if err != nil {
		return nil, err
	}
	if err := flockSeaDex(ctx, f); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}

func flockSeaDex(ctx context.Context, f *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func openSeaDexLock(path string, open func(string, int, os.FileMode) (*os.File, error)) (*os.File, error) {
	db, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	sidecar := path + ".seadex-lock"
	// Refuse static aliases before opening: closing a SQLite descriptor releases its POSIX locks.
	if fi, err := os.Lstat(sidecar); err == nil {
		if !fi.Mode().IsRegular() || os.SameFile(fi, db) {
			return nil, fmt.Errorf("SeaDex lock is not a separate regular file: %s", sidecar)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := open(sidecar, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0660)
	if err != nil {
		f, err = open(sidecar, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	}
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || os.SameFile(fi, db) {
		f.Close()
		return nil, fmt.Errorf("SeaDex lock is not a separate regular file: %s", sidecar)
	}
	return f, nil
}
