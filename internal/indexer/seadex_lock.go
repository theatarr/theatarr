package indexer

import (
	"context"
	"path/filepath"
	"sync"
)

var seaDexMemoryLocks = struct {
	sync.Mutex
	entries map[any]*seaDexMemoryLock
}{entries: make(map[any]*seaDexMemoryLock)}

type seaDexMemoryLock struct {
	ch   chan struct{}
	refs int
}

// References include waiters so removal cannot split one lock into two.
func lockSeaDexMemory(ctx context.Context, key any) (func(), error) {
	seaDexMemoryLocks.Lock()
	m := seaDexMemoryLocks.entries[key]
	if m == nil {
		m = &seaDexMemoryLock{ch: make(chan struct{}, 1)}
		seaDexMemoryLocks.entries[key] = m
	}
	m.refs++
	seaDexMemoryLocks.Unlock()
	release := func() {
		seaDexMemoryLocks.Lock()
		m.refs--
		if m.refs == 0 {
			delete(seaDexMemoryLocks.entries, key)
		}
		seaDexMemoryLocks.Unlock()
	}
	select {
	case m.ch <- struct{}{}:
		return func() { <-m.ch; release() }, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}

// lockSeaDex serializes fetch through commit, across processes and settings generations.
// The sidecar must never be removed: unlinking it would let writers lock different inodes.
// A nil store is a read-only overlay and takes no lock.
func (e *Evidence) lockSeaDex(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e == nil || e.st == nil {
		return func() {}, nil
	}
	path := e.st.Path()
	if path == "" || path == ":memory:" {
		return lockSeaDexMemory(ctx, e.st)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	return lockSeaDexPath(ctx, path)
}
