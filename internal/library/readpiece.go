package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

// The piece read of a file someone is playing (docs/design/now-playing.md "A file that stops working while it plays"):
// Theatarr's own evidence that one file does not read through the mount, the same on a zurg, decypharr or plain
// rclone mount. It is a finding about that one file, never about the mount: a read that does not answer in its time
// limit kills only its own helper and counts neither toward the mount's circuit breaker (guard.timedOut) nor toward
// the helper pool's back-off after kills.

// ErrPieceUnreadable is a verdict: the piece read started and failed (a read error, zero bytes where the file should have
// them, or no answer within the time limit).
var ErrPieceUnreadable = errors.New("library: the file did not read")

// ErrPieceNoVerdict means the piece read says nothing about the file: it did not start or was given up for a reason of its
// own (the mount's circuit open, every helper busy or held back, a helper an earlier piece read killed still there,
// too many abandoned reads in this process, a cancelled context).
var ErrPieceNoVerdict = errors.New("library: no verdict on the file")

// errPieceTimedOut is the unreadable verdict of a read that missed its time limit (a come-back pass ends at it).
var errPieceTimedOut = errors.New("no answer")

// maxAbandonedPieces: without helper processes a piece read that misses its time limit is abandoned, its goroutine
// perhaps blocked in the kernel; while this many are, no other starts.
const maxAbandonedPieces = 2

// abandonedPieces counts the piece reads of this process still blocked after their time limit (no helpers).
var abandonedPieces atomic.Int32

// ReadPiece reads n bytes of a file on the mount from off, with limit as its time limit, and drops them. It returns
// the bytes read; an error wraps ErrPieceUnreadable (a finding about this file) or ErrPieceNoVerdict (none). With helper
// processes the read runs in one of them; one that misses the limit is killed, and no piece read starts while a
// helper so killed has not gone yet (killed helpers count against the pool's cap). Without helpers it runs here and
// stops starting after maxAbandonedPieces abandoned reads.
func (m *Mount) ReadPiece(ctx context.Context, path string, off, n int64, limit time.Duration) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrPieceNoVerdict, err)
	}
	g := m.guard()
	g.mu.Lock()
	open := g.open
	g.mu.Unlock()
	if open {
		return 0, fmt.Errorf("%w: the mount's circuit is open", ErrPieceNoVerdict)
	}
	if pool := m.helperFor(path); pool != nil {
		return m.pieceInHelper(ctx, pool, path, off, n, limit)
	}
	return m.pieceHere(ctx, path, off, n, limit)
}

func (m *Mount) pieceInHelper(ctx context.Context, pool *helperPool, path string, off, n int64,
	limit time.Duration) (int64, error) {
	if pool.pieceLingering() {
		return 0, fmt.Errorf("%w: a helper killed by an earlier piece read is still there", ErrPieceNoVerdict)
	}
	resp, err := pool.callPiece(ctx, time.Now().Add(limit), helperRequest{Op: opRange, Path: []byte(path), Off: off,
		Max: n})
	switch {
	case err == nil:
	case errors.Is(err, errHelperTimeout):
		return 0, fmt.Errorf("%w: %w in %v", ErrPieceUnreadable, errPieceTimedOut, limit)
	default: // busy, held back, stuck, crashed, could not start, or ctx ended: nothing said about the file
		return 0, fmt.Errorf("%w: %v", ErrPieceNoVerdict, err)
	}
	if err := resp.Err.decode(); err != nil {
		return resp.Read, fmt.Errorf("%w: %v", ErrPieceUnreadable, err)
	}
	if resp.Read == 0 {
		return 0, fmt.Errorf("%w: nothing read at byte %d", ErrPieceUnreadable, off)
	}
	return resp.Read, nil
}

func (m *Mount) pieceHere(ctx context.Context, path string, off, n int64, limit time.Duration) (int64, error) {
	if abandonedPieces.Load() >= maxAbandonedPieces {
		return 0, fmt.Errorf("%w: %d earlier piece reads still blocked", ErrPieceNoVerdict, maxAbandonedPieces)
	}
	type result struct {
		n   int64
		err error
	}
	ch := make(chan result, 1)
	// state: 0 running, 1 returned, 2 given up (counted in abandonedPieces until it returns).
	var state atomic.Int32
	go func() {
		var r result
		f, err := os.Open(path)
		if err == nil {
			r.n, _, r.err = readRange(f, off, n, 0)
			f.Close()
		} else {
			r.err = err
		}
		if !state.CompareAndSwap(0, 1) {
			abandonedPieces.Add(-1)
		}
		ch <- r
	}()
	giveUp := func() {
		abandonedPieces.Add(1)
		if !state.CompareAndSwap(0, 2) {
			abandonedPieces.Add(-1) // it returned meanwhile
		}
	}
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case r := <-ch:
		switch {
		case r.err != nil:
			return r.n, fmt.Errorf("%w: %v", ErrPieceUnreadable, r.err)
		case r.n == 0:
			return 0, fmt.Errorf("%w: nothing read at byte %d", ErrPieceUnreadable, off)
		}
		return r.n, nil
	case <-timer.C:
		giveUp()
		return 0, fmt.Errorf("%w: %w in %v", ErrPieceUnreadable, errPieceTimedOut, limit)
	case <-ctx.Done():
		giveUp()
		return 0, fmt.Errorf("%w: %v", ErrPieceNoVerdict, ctx.Err())
	}
}

// ReadPiece reads n bytes of a link target from off through the mount (Mount.ReadPiece): the now playing check of a
// file someone is playing. It never asks a provider for anything.
func (l *Library) ReadPiece(ctx context.Context, target string, off, n int64, limit time.Duration) (int64, error) {
	if l.mount == nil {
		return 0, fmt.Errorf("%w: no mount", ErrPieceNoVerdict)
	}
	p, ok := l.mount.FromLink(target)
	if !ok {
		return 0, fmt.Errorf("%w: %s: %w", ErrPieceNoVerdict, target, ErrNotOnMount)
	}
	return l.mount.ReadPiece(ctx, p, off, n, limit)
}
