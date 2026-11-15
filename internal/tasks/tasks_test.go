package tasks

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func open(t *testing.T) (*Table, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tb := New(context.Background(), st, nil)
	tb.Backoff = func(int) time.Duration { return time.Millisecond }
	return tb, st
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
	}
}

// A run updates the task's row (last start and finish, error, last good run, next run) and writes no job row; a
// failed run keeps the last good one.
func TestRunRecordsTheRow(t *testing.T) {
	ctx := context.Background()
	tb, st := open(t)
	start := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	now := start
	tb.Now = func() time.Time { return now }
	fail := true
	defer tb.Register(ctx, Task{Key: "k", Name: "Do it", Every: 15 * time.Minute, Run: func(context.Context) error {
		now = now.Add(2 * time.Second)
		if fail {
			return errors.New("boom")
		}
		return nil
	}}, start.Add(5*time.Minute))()
	got, err := tb.List(ctx)
	if err != nil || len(got) != 1 || got[0].NextRun == nil || !got[0].NextRun.Equal(start.Add(5*time.Minute)) || got[0].LastStarted != nil {
		t.Fatalf("before a run: %+v %v", got, err)
	}
	if row, _ := tb.Read(ctx, "k"); !row.NextRun.Equal(start.Add(5 * time.Minute)) {
		t.Fatalf("next run not written: %+v", row)
	}
	if ran, err := tb.Run(ctx, "k", start.Add(15*time.Minute)); !ran || err == nil {
		t.Fatalf("run: %v %v", ran, err)
	}
	got, _ = tb.List(ctx)
	s := got[0]
	if !s.LastStarted.Equal(start) || !s.LastFinished.Equal(start.Add(2*time.Second)) || s.LastError != "boom" ||
		s.LastOK != nil || !s.NextRun.Equal(start.Add(15*time.Minute)) || s.EverySeconds != 900 || s.Running != nil {
		t.Fatalf("after a failed run: %+v", s)
	}
	if n := tb.Failed(); n != 1 {
		t.Fatalf("failed %d", n)
	}
	fail = false
	if _, err := tb.Run(ctx, "k", time.Time{}); err != nil {
		t.Fatal(err)
	}
	got, _ = tb.List(ctx)
	if got[0].LastError != "" || got[0].LastOK == nil || !got[0].NextRun.Equal(start.Add(15*time.Minute)) {
		t.Fatalf("after a run that worked (next kept): %+v", got[0])
	}
	if n := tb.Failed(); n != 0 {
		t.Fatalf("failed %d", n)
	}
	var jobs int
	if err := st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM job`).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("job rows: %d %v", jobs, err)
	}
}

// A run with nothing to do (ErrSkip) records no run; only the next run shown moves.
func TestSkipRecordsNothing(t *testing.T) {
	ctx := context.Background()
	tb, _ := open(t)
	next := time.Now().Add(time.Hour)
	defer tb.Register(ctx, Task{Key: "k", Name: "Idle", Every: time.Minute, Run: func(context.Context) error { return ErrSkip }},
		time.Time{})()
	if ran, err := tb.Run(ctx, "k", next); ran || err != nil {
		t.Fatalf("ran %v %v", ran, err)
	}
	got, _ := tb.List(ctx)
	if got[0].LastStarted != nil || got[0].NextRun == nil || !got[0].NextRun.Equal(next) {
		t.Fatalf("after a skip: %+v", got[0])
	}
}

// Two runs of one task never overlap: a Run or Run now while one goes on does nothing.
func TestRunDoesNotOverlap(t *testing.T) {
	ctx := context.Background()
	tb, _ := open(t)
	var n atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{})
	defer tb.Register(ctx, Task{Key: "k", Name: "Slow", Every: time.Minute, Run: func(context.Context) error {
		if n.Add(1) == 1 {
			close(started)
		}
		<-release
		return nil
	}}, time.Time{})()
	if err := tb.RunNow("k"); err != nil {
		t.Fatal(err)
	}
	<-started
	if ran, _ := tb.Run(ctx, "k", time.Time{}); ran {
		t.Fatal("a second run started while the first ran")
	}
	if tb.Start(ctx, "k", time.Time{}) {
		t.Fatal("Start while running")
	}
	if err := tb.RunNow("k"); !errors.Is(err, ErrRunning) {
		t.Fatalf("Run now while running: %v", err)
	}
	if got, _ := tb.List(ctx); got[0].Running == nil {
		t.Fatal("not shown as running")
	}
	close(release)
	waitFor(t, "Run now finished", func() bool {
		got, _ := tb.List(ctx)
		return got[0].Running == nil && got[0].LastFinished != nil
	})
	if n.Load() != 1 {
		t.Fatalf("runs: %d", n.Load())
	}
}

// A failed try is tried again up to the task's attempts.
func TestRunRetries(t *testing.T) {
	ctx := context.Background()
	tb, _ := open(t)
	var n int
	defer tb.Register(ctx, Task{Key: "k", Name: "Flaky", Every: time.Hour, Attempts: 3, Run: func(context.Context) error {
		n++
		if n < 3 {
			return errors.New("not yet")
		}
		return nil
	}}, time.Time{})()
	if _, err := tb.Run(ctx, "k", time.Time{}); err != nil || n != 3 {
		t.Fatalf("tries %d, err %v", n, err)
	}
	n = -10
	if _, err := tb.Run(ctx, "k", time.Time{}); err == nil || n != -7 {
		t.Fatalf("tries until attempts: %d, %v", n, err)
	}
}

// Only registered tasks are listed and run; a paused one shows no next run and refuses Run now; a run now leaves the
// next run as it is.
func TestRegisterListAndRunNow(t *testing.T) {
	ctx := context.Background()
	tb, _ := open(t)
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.Local)
	tb.Now = func() time.Time { return now }
	done := make(chan struct{})
	defer tb.Register(ctx, Task{Key: "d", Name: "B daily", At: "03:00", Run: func(context.Context) error { close(done); return nil }},
		NextAt("03:00", now))()
	defer tb.Register(ctx, Task{Key: "p", Name: "A paused", Every: time.Minute, Paused: true}, now)()
	gone := tb.Register(ctx, Task{Key: "g", Name: "C gone", Every: time.Minute, Run: func(context.Context) error { return nil }}, now)
	gone()
	if err := tb.RunNow("g"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("unregistered: %v", err)
	}
	if ran, err := tb.Run(ctx, "g", time.Time{}); ran || !errors.Is(err, ErrUnknown) {
		t.Fatalf("Run of an unregistered task: %v %v", ran, err)
	}
	if err := tb.RunNow("p"); !errors.Is(err, ErrPaused) {
		t.Fatalf("paused: %v", err)
	}
	if err := tb.RunNow("d"); err != nil {
		t.Fatal(err)
	}
	<-done
	var got []Status
	waitFor(t, "run now recorded", func() bool {
		got, _ = tb.List(ctx)
		return len(got) == 2 && got[1].LastFinished != nil
	})
	if got[0].Key != "p" || !got[0].Paused || got[0].NextRun != nil {
		t.Fatalf("paused task: %+v", got[0])
	}
	if want := time.Date(2026, 11, 16, 3, 0, 0, 0, time.Local); got[1].NextRun == nil || !got[1].NextRun.Equal(want) {
		t.Fatalf("daily next run: %+v, want %v", got[1].NextRun, want)
	}
	if row, err := tb.Read(ctx, "d"); err != nil || !row.LastStarted.Equal(now) || !row.LastOK.Equal(now) {
		t.Fatalf("row: %+v %v", row, err)
	}
}

// Loop runs the task at once and then every interval, one run at a time, and unregisters it when ctx ends.
func TestLoopRunsEveryInterval(t *testing.T) {
	ctx := context.Background()
	tb, _ := open(t)
	var n atomic.Int32
	lctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		tb.Loop(lctx, Task{Key: "k", Name: "Tick", Every: 20 * time.Millisecond, Run: func(context.Context) error {
			n.Add(1)
			return nil
		}}, 0)
		close(done)
	}()
	waitFor(t, "three runs", func() bool { return n.Load() >= 3 })
	got, _ := tb.List(ctx)
	if len(got) != 1 || got[0].NextRun == nil || got[0].LastStarted == nil {
		t.Fatalf("row %+v", got)
	}
	cancel()
	<-done
	if got, _ := tb.List(ctx); len(got) != 0 {
		t.Fatalf("listed after the loop ended: %+v", got)
	}
}
