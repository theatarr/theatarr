package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// pieceMount is a mount in a temporary folder whose calls run in this process, with one 1 MiB file.
func pieceMount(t *testing.T) (*Mount, string) {
	t.Helper()
	dir := t.TempDir()
	rel := filepath.Join(dir, "__all__", "Rel")
	if err := os.MkdirAll(rel, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(rel, "S01E02.mkv")
	if err := os.WriteFile(f, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Mount{Path: dir, AllDir: "__all__", StatTimeout: time.Second}, f
}

// A piece inside the file reads; a piece where the file has no bytes (its head cached and readable, the rest gone)
// and a file that is not there are findings about the file. An open circuit and a cancelled context give no verdict.
func TestReadPieceVerdicts(t *testing.T) {
	m, f := pieceMount(t)
	ctx := context.Background()
	if n, err := m.ReadPiece(ctx, f, 512<<10, 256<<10, time.Second); err != nil || n != 256<<10 {
		t.Fatalf("piece inside the file: %d %v", n, err)
	}
	if n, err := m.ReadHead(ctx, f, 4<<20); err != nil || n != 1<<20 {
		t.Fatalf("head: %d %v", n, err)
	}
	if _, err := m.ReadPiece(ctx, f, 10<<20, 256<<10, time.Second); !errors.Is(err, ErrPieceUnreadable) {
		t.Errorf("piece past the bytes the file has, while its head reads: %v", err)
	}
	if _, err := m.ReadPiece(ctx, f+".gone", 0, 256<<10, time.Second); !errors.Is(err, ErrPieceUnreadable) {
		t.Errorf("a file that is not there: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := m.ReadPiece(cancelled, f, 0, 256<<10, time.Second); !errors.Is(err, ErrPieceNoVerdict) {
		t.Errorf("cancelled: %v", err)
	}
	g := m.guard()
	g.mu.Lock()
	g.open, g.openedAt = true, time.Now()
	g.mu.Unlock()
	if _, err := m.ReadPiece(ctx, f, 0, 256<<10, time.Second); !errors.Is(err, ErrPieceNoVerdict) {
		t.Errorf("circuit open: %v", err)
	}
}

// A read that hangs (a FIFO with no writer blocks in open(2), as a read on a dead FUSE mount does) is a failure after
// its time limit and counts nothing toward the mount's breaker. Without helpers, two abandoned reads stop the next one
// (no verdict) until they return.
func TestReadPieceHangInProcess(t *testing.T) {
	m, _ := pieceMount(t)
	ctx := context.Background()
	var fifos []string
	for _, name := range []string{"a.mkv", "b.mkv"} {
		p := filepath.Join(m.AllPath(), "Rel", name)
		if err := syscall.Mkfifo(p, 0o644); err != nil {
			t.Skip("mkfifo:", err)
		}
		fifos = append(fifos, p)
	}
	release := func() {
		for _, p := range fifos {
			if w, err := os.OpenFile(p, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				w.Close()
			}
		}
	}
	defer release()
	for _, p := range fifos {
		start := time.Now()
		if _, err := m.ReadPiece(ctx, p, 0, 256<<10, 100*time.Millisecond); !errors.Is(err, ErrPieceUnreadable) {
			t.Fatalf("hung read: %v", err)
		}
		if took := time.Since(start); took > time.Second {
			t.Errorf("hung read took %v", took)
		}
	}
	if _, err := m.ReadPiece(ctx, fifos[0], 0, 256<<10, 100*time.Millisecond); !errors.Is(err, ErrPieceNoVerdict) {
		t.Errorf("a third read with two abandoned: %v", err)
	}
	if b := m.Breaker(); b.Timeouts != 0 || b.State != BreakerClosed {
		t.Errorf("breaker %+v", b)
	}
	release()
	waitUntil(t, func() bool { return abandonedPieces.Load() == 0 })
}

// With helpers, a hung piece read kills only its own helper: no breaker count, no back-off for the next start. While
// that helper lingers no piece read starts, and a sweep's calls still find room under the pool's cap.
func TestReadPieceHangInHelper(t *testing.T) {
	m := helperMount(t, 2*time.Second)
	m.pool.cfg.Size, m.pool.cfg.Max = 1, 2
	var alive atomic.Bool
	alive.Store(true)
	m.pool.mu.Lock()
	m.pool.reapFn = func(p *os.Process) bool { return !alive.Load() && reapProcess(p) }
	m.pool.mu.Unlock()
	t.Cleanup(func() { alive.Store(false) })
	ctx := context.Background()
	hang := filepath.Join(m.AllPath(), "Rel", "HANG.mkv")
	if _, err := m.ReadPiece(ctx, hang, 0, 256<<10, 300*time.Millisecond); !errors.Is(err, ErrPieceUnreadable) {
		t.Fatalf("hung piece read: %v", err)
	}
	b := m.Breaker()
	if b.Timeouts != 0 || b.State != BreakerClosed || b.Helper.Kills != 1 || !b.Helper.RetryAt.IsZero() ||
		b.Helper.Lingering != 1 {
		t.Fatalf("breaker %+v helper %+v", b, b.Helper)
	}
	movie := filepath.Join(m.AllPath(), "Rel", "movie.mkv")
	if _, err := m.ReadPiece(ctx, movie, 0, 4, time.Second); !errors.Is(err, ErrPieceNoVerdict) {
		t.Errorf("piece read while the killed helper lingers: %v", err)
	}
	// A sweep's calls are not failed by it.
	if _, err := m.Stat(ctx, movie); err != nil {
		t.Errorf("stat beside a lingering piece helper: %v", err)
	}
	if ents, err := m.ReadDir(ctx, m.AllPath()); err != nil || len(ents) != 1 {
		t.Errorf("listing beside a lingering piece helper: %v %v", ents, err)
	}
	alive.Store(false)
	waitUntil(t, func() bool { return !m.pool.pieceLingering() })
	if n, err := m.ReadPiece(ctx, movie, 0, 4, time.Second); err != nil || n != 4 {
		t.Errorf("piece read after the helper went: %d %v", n, err)
	}
}

// Every helper busy with another call: the piece read gives no verdict.
func TestReadPieceBusyHelperNoVerdict(t *testing.T) {
	m := helperMount(t, 10*time.Second)
	m.pool.cfg.Size, m.pool.cfg.Max = 1, 2
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = m.Stat(ctx, filepath.Join(m.AllPath(), "BLOCK"))
	}()
	waitUntil(t, func() bool { return m.pool.state().Busy == 1 })
	movie := filepath.Join(m.AllPath(), "Rel", "movie.mkv")
	if _, err := m.ReadPiece(ctx, movie, 0, 4, 200*time.Millisecond); !errors.Is(err, ErrPieceNoVerdict) {
		t.Errorf("piece read with every helper busy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(m.Path, "RELEASE"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if b := m.Breaker(); b.Timeouts != 0 || b.Helper.Kills != 0 {
		t.Errorf("breaker %+v helper %+v", b, b.Helper)
	}
}
