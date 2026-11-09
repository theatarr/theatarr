package library

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// The now playing page's reads: zurg's /stats, every new log line, and the pre-warm read of a link target.

func TestZurgStats(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false, policy.ModeShadow)
	z := e.withZurg(newZurgServer(t))
	st, err := z.Stats(e.ctx)
	if err != nil || st.ActiveStreams != 2 || len(st.BlockedReads) != 1 || st.BlockedReads[0].Filename != "e.mkv" ||
		st.BlockedReads[0].Reason != "starved" || st.BlockedReads[0].Attempts != 3 {
		t.Fatalf("%v %+v", err, st)
	}
}

func TestZurgLogOnLine(t *testing.T) {
	t.Parallel()
	srv := &logServer{body: "old line\n"}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	var lines []string
	z := &ZurgLogHTTPFollower{URL: ts.URL + "/logs", GW: plainDoer{ts.Client()}, OnLine: func(l string) { lines = append(lines, l) }}
	polled(t, z)
	srv.append("Large transfer: 1.0 GiB of e.mkv from realdebrid for rclone/v1.75.2\npartial")
	polled(t, z)
	if len(lines) != 1 || lines[0] != "Large transfer: 1.0 GiB of e.mkv from realdebrid for rclone/v1.75.2\n" {
		t.Errorf("lines %q", lines)
	}
}

func TestPrewarm(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false, policy.ModeShadow)
	e.release("Show.S01.WEB", map[string]float64{"Show.S01E02.mkv": 2700})
	target := filepath.Join(e.mount, "__all__", "Show.S01.WEB", "Show.S01E02.mkv")
	// Not a container the probe knows ("video"): read all the same.
	if err := e.lib.Prewarm(e.ctx, target, 4<<20); err != nil {
		t.Errorf("prewarm: %v", err)
	}
	if err := e.lib.Prewarm(e.ctx, filepath.Join(e.mount, "__all__", "Gone", "x.mkv"), 4<<20); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: %v", err)
	}
	if err := e.lib.Prewarm(context.Background(), "/elsewhere/x.mkv", 4<<20); !errors.Is(err, ErrNotOnMount) {
		t.Errorf("outside the mount: %v", err)
	}
}

func TestPrewarmRejectsEmptyFile(t *testing.T) {
	check := func(t *testing.T, m *Mount) {
		p := filepath.Join(m.Path, "empty.mkv")
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		lib := &Library{mount: m}
		if err := lib.Prewarm(context.Background(), p, 4<<20); err == nil {
			t.Fatal("empty next episode passed the head check")
		}
	}
	t.Run("in process", func(t *testing.T) {
		check(t, &Mount{Path: t.TempDir(), AllDir: "__all__", StatTimeout: time.Second})
	})
	t.Run("helper", func(t *testing.T) { check(t, helperMount(t, time.Second)) })
}

// The pre-warm is one sequential read of the head, at most max bytes from offset 0, and the file closed at once:
// not the header probe, whose reads seek (a fifo refuses a seek, and its writer counts what was read).
func TestPrewarmHeadRead(t *testing.T) {
	const max = 4 << 20
	check := func(t *testing.T, m *Mount, root string) {
		dir := filepath.Join(root, "__all__", "Fifo")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		fifo := filepath.Join(dir, "e.mp4")
		if err := syscall.Mkfifo(fifo, 0o644); err != nil {
			t.Skip("no fifo:", err)
		}
		written := make(chan int64, 1)
		go func() {
			w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
			if err != nil {
				written <- -1
				return
			}
			defer w.Close()
			// An MP4 whose moov box follows 8 MiB of media data: a probe would seek past the head.
			chunk := make([]byte, 64<<10)
			copy(chunk, []byte{0, 0, 0, 16, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 2, 0, 0, 0x80, 0, 8, 'm', 'd', 'a', 't'})
			var n int64
			for n < 8<<20 {
				k, err := w.Write(chunk)
				n += int64(k)
				if err != nil {
					break
				}
				chunk = make([]byte, 64<<10)
			}
			written <- n
		}()
		lib := &Library{mount: m}
		if err := lib.Prewarm(context.Background(), fifo, max); err != nil {
			t.Fatalf("prewarm: %v", err)
		}
		select {
		case n := <-written:
			// What was read, plus what the pipe buffered before the reader closed it.
			if n < max || n > max+(1<<20) {
				t.Errorf("writer wrote %d bytes, want the 4 MiB head read then a closed reader", n)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the reader never closed the file")
		}
	}
	t.Run("in process", func(t *testing.T) {
		root := t.TempDir()
		check(t, &Mount{Path: root, AllDir: "__all__", StatTimeout: 5 * time.Second}, root)
	})
	t.Run("helper", func(t *testing.T) {
		m := helperMount(t, 5*time.Second)
		check(t, m, m.Path)
	})
}

// The caching read: the whole file in order, at the rate cap, progress after each piece; in process and in a helper.
func TestCacheFile(t *testing.T) {
	size := int64(rangePiece + 5<<20)
	check := func(t *testing.T, m *Mount, root string) {
		dir := filepath.Join(root, "__all__", "Big")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "e.mkv")
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		lib := &Library{mount: m}
		if got, err := lib.MountFileSize(context.Background(), p); err != nil || got != size {
			t.Fatalf("size %d %v", got, err)
		}
		var calls [][2]int64
		start := time.Now()
		n, err := lib.CacheFile(context.Background(), p, 200<<20, func(done, sz int64) { calls = append(calls, [2]int64{done, sz}) })
		if err != nil || n != size {
			t.Fatalf("read %d %v", n, err)
		}
		// 37 MiB at 200 MiB/s takes at least 185 ms.
		if el := time.Since(start); el < 150*time.Millisecond {
			t.Errorf("read in %v: the rate cap did not hold", el)
		}
		if len(calls) != 2 || calls[0] != [2]int64{rangePiece, size} || calls[1] != [2]int64{size, size} {
			t.Errorf("progress %v", calls)
		}
		if _, err := lib.CacheFile(context.Background(), filepath.Join(dir, "gone.mkv"), 0, nil); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("missing file: %v", err)
		}
	}
	t.Run("in process", func(t *testing.T) {
		root := t.TempDir()
		check(t, &Mount{Path: root, AllDir: "__all__", StatTimeout: 5 * time.Second}, root)
	})
	t.Run("helper", func(t *testing.T) {
		m := helperMount(t, 5*time.Second)
		check(t, m, m.Path)
	})
}
