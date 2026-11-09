package rootprobe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// mountTable makes lines the mount table for the test.
func mountTable(t *testing.T, lines string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseMountTable(path))
}

const twoShares = `1 0 8:1 / / rw - ext4 /dev/sda1 rw
2 1 0:50 / /nfs/movies rw - nfs4 nas:/movies rw
3 1 0:51 / /local/tv rw - nfs4 nas:/tv rw
`

func waitIdle(t *testing.T, p *Prober) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !p.Idle() {
		if time.Now().After(deadline) {
			t.Fatal("a call left behind never returned")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A call that gives up marks its root stuck: new calls on that root are refused without running, calls on other
// roots run, and once the hung call returns the root runs again.
func TestStuckRootRefusesOnlyItself(t *testing.T) {
	mountTable(t, twoShares)
	p := &Prober{}
	ctx := context.Background()
	release := make(chan struct{})
	err := p.Do(ctx, []string{"/nfs/movies"}, 50*time.Millisecond, func() error { <-release; return nil })
	var timeout *TimeoutError
	if !errors.As(err, &timeout) || timeout.Root != "/nfs/movies" {
		t.Fatalf("hung call: %v", err)
	}
	var ran atomic.Int64
	for range 3 {
		err := p.Do(ctx, []string{"/nfs/movies/", "/local/tv"}, time.Second, func() error { ran.Add(1); return nil })
		var stuck *StuckError
		if !errors.As(err, &stuck) || stuck.Root != "/nfs/movies" {
			t.Fatalf("while stuck: %v", err)
		}
	}
	if err := p.Do(ctx, []string{"/local/tv"}, time.Second, func() error { ran.Add(1); return nil }); err != nil {
		t.Fatalf("another root: %v", err)
	}
	if ran.Load() != 1 || !p.Stuck("/nfs/movies") || p.Stuck("/local/tv") {
		t.Fatalf("ran %d, stuck movies %v, tv %v", ran.Load(), p.Stuck("/nfs/movies"), p.Stuck("/local/tv"))
	}
	close(release)
	waitIdle(t, p)
	if err := p.Do(ctx, []string{"/nfs/movies"}, time.Second, func() error { return nil }); err != nil {
		t.Fatalf("after the filesystem answered: %v", err)
	}
}

// One call per root at a time: a second call waits for the first, and gives up (not stuck) if it waits too long.
func TestOneCallPerRoot(t *testing.T) {
	p := &Prober{}
	ctx := context.Background()
	started, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- p.Do(ctx, []string{"/a"}, 5*time.Second, func() error { close(started); <-release; return nil })
	}()
	<-started
	err := p.Do(ctx, []string{"/a"}, 50*time.Millisecond, func() error { t.Error("ran while /a was busy"); return nil })
	var timeout *TimeoutError
	if !errors.As(err, &timeout) || p.Stuck("/a") {
		t.Fatalf("waiting call: %v (stuck %v)", err, p.Stuck("/a"))
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	waitIdle(t, p)
}

// Slots are per mount, not per path. While a call on one share is stuck, every other path on that
// share (a bind mount of it too) is refused without a call, so a hung share holds one blocked thread however many
// paths are asked about; paths on other mounts run.
func TestStuckMountRefusesItsOtherPaths(t *testing.T) {
	mountTable(t, twoShares+"4 1 0:50 /4k /srv/movies4k rw - nfs4 nas:/movies rw\n")
	p := &Prober{}
	ctx := context.Background()
	release := make(chan struct{})
	var calls atomic.Int64
	hang := func() error { calls.Add(1); <-release; return nil }
	var timeout *TimeoutError
	if err := p.Do(ctx, []string{"/nfs/movies/a"}, 50*time.Millisecond, hang); !errors.As(err, &timeout) {
		t.Fatalf("hung call: %v", err)
	}
	for _, dir := range []string{"/nfs/movies/b", "/nfs/movies", "/srv/movies4k/c"} {
		var stuck *StuckError
		if err := p.Do(ctx, []string{dir}, time.Second, hang); !errors.As(err, &stuck) || stuck.Root != dir {
			t.Fatalf("%s while its share is stuck: %v", dir, err)
		}
		if !p.Stuck(dir) {
			t.Errorf("%s not reported stuck", dir)
		}
	}
	for _, dir := range []string{"/local/tv/x", "/srv/other"} {
		if err := p.Do(ctx, []string{dir}, time.Second, func() error { return nil }); err != nil {
			t.Fatalf("%s on another mount: %v", dir, err)
		}
	}
	close(release)
	waitIdle(t, p)
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d calls on the hung share, want 1", n)
	}
}

// The slots of several mounts are taken in one order: two calls naming the same mounts in opposite orders both run.
func TestSeveralMountsNoDeadlock(t *testing.T) {
	mountTable(t, twoShares)
	p := &Prober{}
	ctx := context.Background()
	errs := make(chan error, 40)
	for i := range 40 {
		dirs := []string{"/nfs/movies/x", "/local/tv/y"}
		if i%2 == 1 {
			dirs[0], dirs[1] = dirs[1], dirs[0]
		}
		go func() {
			errs <- p.Do(ctx, dirs, 2*time.Second, func() error { time.Sleep(time.Millisecond); return nil })
		}()
	}
	for range 40 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	waitIdle(t, p)
}

// MountsBelow lists the mounts strictly below a directory, not the one it is on nor siblings sharing a prefix.
func TestMountsBelow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mountinfo")
	table := "1 0 0:1 / / rw - rootfs rootfs rw\n2 1 0:2 / /media rw - nfs nfs rw\n" +
		"3 2 0:3 / /media/movies/4k rw - nfs nfs rw\n4 2 0:4 / /media/movies2 rw - nfs nfs rw\n" +
		"5 3 0:5 / /media/movies/4k/x\\040y rw - nfs nfs rw\n"
	if err := os.WriteFile(path, []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseMountTable(path))
	got := MountsBelow("/media/movies/")
	if !slices.Equal(got, []string{"/media/movies/4k", "/media/movies/4k/x y"}) {
		t.Fatalf("mounts below %v", got)
	}
	if got := MountsBelow("/media/movies/4k/x y"); len(got) != 0 {
		t.Fatalf("mounts below a mount point %v", got)
	}
}

// DoOnce counts its timeout from when it holds the slot: a call that waits behind a slow but
// answering read runs in full instead of giving up; a call waiting behind one that gave up is refused at once.
func TestDoOnceTimesFromTheSlot(t *testing.T) {
	p := &Prober{}
	ctx := context.Background()
	started, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- p.Do(ctx, []string{"/a"}, 5*time.Second, func() error { close(started); <-release; return nil })
	}()
	<-started
	time.AfterFunc(150*time.Millisecond, func() { close(release) })
	ran := false
	if err := p.DoOnce(ctx, []string{"/a"}, 100*time.Millisecond, func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("waiting call: %v (ran %v)", err, ran)
	}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	waitIdle(t, p)

	hung, done := make(chan struct{}), make(chan struct{})
	waiting := make(chan error, 1)
	go func() {
		waiting <- p.DoOnce(ctx, []string{"/a"}, 50*time.Millisecond, func() error { <-hung; return nil })
	}()
	time.Sleep(10 * time.Millisecond)
	go func() {
		defer close(done)
		err := p.DoOnce(ctx, []string{"/a"}, time.Hour, func() error { t.Error("ran behind a stuck call"); return nil })
		var stuck *StuckError
		if !errors.As(err, &stuck) {
			t.Errorf("behind a stuck call: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a call behind a stuck one waited on")
	}
	var timeout *TimeoutError
	if err := <-waiting; !errors.As(err, &timeout) {
		t.Fatalf("hung call: %v", err)
	}
	close(hung)
	waitIdle(t, p)
}
