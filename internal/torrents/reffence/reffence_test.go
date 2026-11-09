package reffence

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}

func shortHoldWait(t *testing.T, d time.Duration) {
	old := HoldWait
	HoldWait = d
	t.Cleanup(func() { HoldWait = old })
}

// holdWithin runs Hold and fails the test when it is still waiting after limit.
func holdWithin(t *testing.T, ctx context.Context, st *store.Store, limit time.Duration) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		rel, err := Hold(ctx, st, time.Now())
		if rel != nil {
			rel()
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(limit):
		t.Fatalf("Hold still waiting after %v", limit)
		return nil
	}
}

// A link write of this process stuck in its filesystem change (Begin held) keeps a delete waiting at
// most HoldWait (ErrWriting), and no longer than its ctx.
func TestHoldBoundedByLocalWriter(t *testing.T) {
	st := openStore(t)
	_, _, release, err := Begin(context.Background(), st, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	shortHoldWait(t, 100*time.Millisecond)
	if err := holdWithin(t, context.Background(), st, 2*time.Second); !errors.Is(err, ErrWriting) {
		t.Errorf("after HoldWait: %v, want ErrWriting", err)
	}
	shortHoldWait(t, time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := holdWithin(t, ctx, st, 2*time.Second); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("after its ctx ended: %v, want the ctx error", err)
	}
}

// A delete waiting for a stuck writer does not queue on the lock: other writers still get in (a queued RWMutex
// writer would block every new reader behind the stuck one).
func TestWaitingHoldDoesNotBlockWriters(t *testing.T) {
	st := openStore(t)
	_, _, release, err := Begin(context.Background(), st, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	shortHoldWait(t, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	held := make(chan error, 1)
	go func() {
		rel, err := Hold(ctx, st, time.Now())
		if rel != nil {
			rel()
		}
		held <- err
	}()
	time.Sleep(50 * time.Millisecond)
	wctx, wcancel := context.WithTimeout(context.Background(), time.Second)
	defer wcancel()
	_, waited, release2, err := Begin(wctx, st, time.Now())
	if err != nil || waited {
		t.Errorf("a second writer while a delete waits: waited %v, %v", waited, err)
	} else {
		release2()
	}
	if err := Write(wctx, func() error { return nil }); err != nil {
		t.Errorf("Write while a delete waits: %v", err)
	}
	cancel()
	<-held
}

// A writer waiting for a delete of this process (Hold held) gives up when its ctx ends.
func TestWriterWaitHonoursCtx(t *testing.T) {
	st := openStore(t)
	rel, err := Hold(context.Background(), st, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 2)
	go func() {
		_, waited, release, err := Begin(ctx, st, time.Now())
		if release != nil {
			release()
		}
		if err == nil || !waited {
			err = errors.New("Begin did not wait")
		}
		done <- err
	}()
	go func() { done <- Write(ctx, func() error { return nil }) }()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("writer: %v, want the ctx error", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("writer still waiting after its ctx ended")
		}
	}
}
