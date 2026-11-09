package library

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/library/probe"
)

// The test binary is its own mount helper: run with THEATARR_LIBRARY_HELPER_CHILD=1 it serves the helper protocol, with
// two hooks a real mount cannot be asked for: a path holding "HANG" blocks its call forever, as a dead FUSE mount
// does, and one holding "CRASH" ends the process mid-call. THEATARR_TEST_MOUNT_HELPER=1 runs every test of the package
// with its mount calls in helper processes (make check runs it so).
func TestMain(m *testing.M) {
	if fifo := os.Getenv("THEATARR_LIBRARY_HELPER_PARENT"); fifo != "" {
		helperParent(fifo)
	}
	if os.Getenv("THEATARR_LIBRARY_HELPER_CHILD") == "1" {
		root := ""
		for i, a := range os.Args {
			if a == "-root" && i+1 < len(os.Args) {
				root = os.Args[i+1]
			}
		}
		log, _ := os.OpenFile(os.Getenv("THEATARR_LIBRARY_HELPER_LOG"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		s := &helperServer{root: root, hang: func(op, p string) {
			if log != nil {
				log.WriteString(op + "\n")
			}
			switch {
			case strings.Contains(p, "HANG"):
				for {
					time.Sleep(time.Hour)
				}
			case strings.Contains(p, "CRASH"):
				os.Exit(3)
			case strings.Contains(p, "BLOCK"): // until the test creates RELEASE in the root
				for {
					if _, err := os.Stat(filepath.Join(root, "RELEASE")); err == nil {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
		}}
		watchParent(os.Getppid, time.Second, os.Exit)
		if err := s.serve(os.Stdin, os.Stdout); err != nil {
			os.Stderr.WriteString("mount helper: " + err.Error() + "\n")
			os.Exit(1)
		}
		os.Exit(0)
	}
	if os.Getenv("THEATARR_TEST_MOUNT_HELPER") != "1" {
		os.Exit(m.Run())
	}
	// Every library test through helpers: count the calls they answered, to show they did.
	dir, err := os.MkdirTemp("", "theatarr-helper-log")
	if err != nil {
		panic(err)
	}
	logPath := filepath.Join(dir, "calls")
	testMountHelper = testHelperConfig()
	testMountHelper.Env = append(testMountHelper.Env, "THEATARR_LIBRARY_HELPER_LOG="+logPath)
	code := m.Run()
	b, _ := os.ReadFile(logPath)
	os.RemoveAll(dir)
	ops := map[string]int{}
	for _, op := range strings.Fields(string(b)) {
		ops[op]++
	}
	os.Stderr.WriteString("mount helper calls answered: " + fmt.Sprint(ops) + "\n")
	if len(ops) == 0 && code == 0 {
		os.Stderr.WriteString("THEATARR_TEST_MOUNT_HELPER=1 but no call went through a helper\n")
		code = 1
	}
	os.Exit(code)
}

func testHelperConfig() *MountHelper {
	exe, err := os.Executable()
	if err != nil {
		panic(err)
	}
	return &MountHelper{Exe: exe, Env: []string{"THEATARR_LIBRARY_HELPER_CHILD=1"}, Size: 2, IdleExit: 2 * time.Second}
}

// helperMount is a mount in a temporary directory whose calls run in helper processes.
func helperMount(t *testing.T, timeout time.Duration) *Mount {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "__all__", "Rel"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "__all__", "Rel", "movie.mkv"), []byte("not a video"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Mount{Path: dir, AllDir: "__all__", StatTimeout: timeout, BreakAfter: 3, Cooldown: time.Minute}
	m.pool = newHelperPool(dir, *testHelperConfig())
	t.Cleanup(func() {
		m.closeHelpers()
		waitUntil(t, func() bool { return m.pool.state().Lingering == 0 })
	})
	return m
}

func waitUntil(t *testing.T, ok func() bool) {
	t.Helper()
	for range 200 {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("condition not met in 2s")
}

// threads is this process's OS thread count.
func threads(t *testing.T) int {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Skip("no /proc")
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "Threads:"); ok {
			n, _ := strconv.Atoi(strings.TrimSpace(v))
			return n
		}
	}
	t.Skip("no thread count")
	return 0
}

// A call that never returns (the helper blocks in it forever) is ErrMountDown by its deadline; the helper is killed
// and a fresh one answers the next call; other calls go on meanwhile; nothing stays blocked in Theatarr.
func TestMountHelperKillsHungCall(t *testing.T) {
	m := helperMount(t, time.Second) // starting a helper is part of a call's time: generous under -race
	ctx := context.Background()
	hang := filepath.Join(m.AllPath(), "HANG")
	if _, err := m.Stat(ctx, m.AllPath()); err != nil {
		t.Fatal(err)
	}
	goroutines := runtime.NumGoroutine()

	start := time.Now()
	if _, err := m.Stat(ctx, hang); !errors.Is(err, ErrMountDown) {
		t.Fatalf("stat of a hung path: %v", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("took %v", took)
	}
	h := m.Breaker().Helper
	if h == nil || h.Kills != 1 || h.RecentKills != 1 {
		t.Fatalf("helper state %+v", h)
	}
	// A fresh helper answers.
	fi, err := m.Stat(ctx, filepath.Join(m.AllPath(), "Rel", "movie.mkv"))
	if err != nil || fi.Size() != int64(len("not a video")) {
		t.Fatalf("stat after the kill: %v %v", fi, err)
	}
	// A hung call holds one helper; the other answers meanwhile.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := m.ReadDir(ctx, filepath.Join(hang, "x")); !errors.Is(err, ErrMountDown) {
			t.Errorf("readdir of a hung path: %v", err)
		}
	}()
	time.Sleep(50 * time.Millisecond)
	if ents, err := m.ReadDir(ctx, m.AllPath()); err != nil || len(ents) != 1 || ents[0].Name() != "Rel" {
		t.Errorf("listing while a call hangs: %v %v", ents, err)
	}
	wg.Wait()
	if h := m.Breaker().Helper; h.Kills != 2 || h.Busy != 0 {
		t.Errorf("helper state %+v", h)
	}
	// The killed helpers are gone and reaped; Theatarr kept no goroutine for them.
	waitUntil(t, func() bool { return m.pool.state().Lingering == 0 })
	waitUntil(t, func() bool { return runtime.NumGoroutine() <= goroutines })
	if b := m.Breaker(); b.State != BreakerClosed || b.Timeouts != 2 {
		t.Errorf("breaker %+v", b)
	}
}

// A probe stuck in the kernel (opening a FIFO with no writer blocks in open(2), as a read on a dead FUSE mount
// does) pins a thread of the helper, not of Theatarr: ten of them leave Theatarr's thread and goroutine counts where they were.
func TestMountHelperStuckSyscallsPinNoThreads(t *testing.T) {
	m := helperMount(t, time.Second)
	m.BreakAfter = 100
	fifo := filepath.Join(m.AllPath(), "Rel", "stuck.mkv")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skip("mkfifo:", err)
	}
	ctx := context.Background()
	if _, err := m.Stat(ctx, fifo); err != nil {
		t.Fatal(err)
	}
	old := probeTimeout
	probeTimeout = 100 * time.Millisecond
	defer func() { probeTimeout = old }()
	th, goroutines := threads(t), runtime.NumGoroutine()
	for range 10 {
		if _, err := m.ProbeFile(ctx, fifo, 0, nil); !errors.Is(err, ErrMountDown) {
			t.Fatalf("probe of a stuck file: %v", err)
		}
	}
	// Kills in a row push the next start back past these short deadlines: the later calls find no helper in time.
	if h := m.Breaker().Helper; h.Kills < 3 || h.Busy != 0 {
		t.Errorf("helper state %+v", h)
	}
	waitUntil(t, func() bool { return runtime.NumGoroutine() <= goroutines })
	if now := threads(t); now > th+2 {
		t.Errorf("threads %d, were %d", now, th)
	}
}

// Kills in a row push the next start back (the third call finds no helper before its deadline); the breaker opens after BreakAfter of them and then refuses calls
// without starting a helper.
func TestMountHelperBackoffAndBreaker(t *testing.T) {
	m := helperMount(t, 100*time.Millisecond)
	ctx := context.Background()
	hang := filepath.Join(m.AllPath(), "HANG")
	for range 3 {
		if _, err := m.Stat(ctx, hang); !errors.Is(err, ErrMountDown) {
			t.Fatal(err)
		}
	}
	b := m.Breaker()
	if b.State != BreakerOpen || b.Helper.Kills < 2 || b.Helper.RecentKills != int(b.Helper.Kills) {
		t.Fatalf("breaker %+v helper %+v", b, b.Helper)
	}
	starts := b.Helper.Starts
	if _, err := m.Stat(ctx, m.AllPath()); !errors.Is(err, ErrMountDown) {
		t.Errorf("stat while open: %v", err)
	}
	if got := m.Breaker().Helper.Starts; got != starts {
		t.Errorf("%d helpers started while the circuit was open", got-starts)
	}
}

// Every helper busy with a call that does answer (a long probe) is not the mount failing: the calls that find no
// helper free in time fail, but they don't open the circuit.
func TestMountHelperBusyPoolKeepsBreakerClosed(t *testing.T) {
	t.Parallel()
	m := helperMount(t, 10*time.Second) // starting a helper is part of a call's time: generous under -race
	m.pool.cfg.Size = 1
	ctx := context.Background()
	if _, err := m.Stat(ctx, m.AllPath()); err != nil {
		t.Fatal(err)
	}
	// The one helper answers a probe only once the test lets it.
	held := make(chan error, 1)
	go func() {
		_, err := m.ProbeFile(ctx, filepath.Join(m.AllPath(), "Rel", "BLOCK.mkv"), 0, nil)
		held <- err
	}()
	waitUntil(t, func() bool { return m.pool.state().Busy == 1 })
	m.StatTimeout = 50 * time.Millisecond
	for range 4 {
		if _, err := m.Stat(ctx, m.AllPath()); !errors.Is(err, ErrMountDown) {
			t.Fatalf("stat with every helper busy: %v", err)
		}
	}
	if b := m.Breaker(); b.State != BreakerClosed || b.Timeouts != 0 || b.Opens != 0 {
		t.Errorf("a busy pool tripped the breaker: %+v", b)
	}
	if err := os.WriteFile(filepath.Join(m.Path, "RELEASE"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := <-held; errors.Is(err, ErrMountDown) {
		t.Fatalf("the held probe: %v", err)
	}
	m.StatTimeout = 10 * time.Second
	if _, err := m.Stat(ctx, m.AllPath()); err != nil {
		t.Errorf("stat once the helper is free: %v", err)
	}
}

// A helper that ends mid-call is ErrMountDown for that call, counted, and replaced.
func TestMountHelperCrashMidCall(t *testing.T) {
	t.Parallel()
	m := helperMount(t, time.Second)
	ctx := context.Background()
	if _, err := m.Stat(ctx, filepath.Join(m.AllPath(), "CRASH")); !errors.Is(err, ErrMountDown) {
		t.Fatalf("crashed call: %v", err)
	}
	if h := m.Breaker().Helper; h.Crashes != 1 || h.Kills != 0 {
		t.Errorf("helper state %+v", h)
	}
	if _, err := m.Stat(ctx, m.AllPath()); err != nil {
		t.Errorf("after a crash: %v", err)
	}
}

// A helper that cannot start is the mount down: the call never falls back to running in Theatarr.
func TestMountHelperCannotStart(t *testing.T) {
	m := helperMount(t, 200*time.Millisecond)
	m.pool.cfg.Exe = filepath.Join(t.TempDir(), "missing")
	if _, err := m.Stat(context.Background(), m.AllPath()); !errors.Is(err, ErrMountDown) {
		t.Fatalf("stat without a helper: %v", err)
	}
	if h := m.Breaker().Helper; h.Crashes != 1 || h.Running != 0 || h.LastError == "" {
		t.Errorf("helper state %+v", h)
	}
}

// A call whose context ends is given up at once; its helper is killed but not counted as the mount's fault.
func TestMountHelperCancelledCall(t *testing.T) {
	m := helperMount(t, 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := m.Stat(ctx, filepath.Join(m.AllPath(), "HANG")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled call: %v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("took %v", took)
	}
	b := m.Breaker()
	if b.Timeouts != 0 || b.Helper.Kills != 0 || b.Helper.RecentKills != 0 || b.Helper.Running != 0 {
		t.Errorf("breaker %+v helper %+v", b, b.Helper)
	}
}

// The helper reads only under its root: a symlink out of it, an unclean or relative path and any other operation
// are refused, and nothing is opened for writing. Outside the root it answers stat, lstat, readlink and listings
// only (a client's folder of symlinks into the mount): file contents and symlink resolution are refused there.
func TestMountHelperRefusesPathsOutsideRoot(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "mount")
	outside := t.TempDir()
	for _, d := range []string{filepath.Join(root, "__all__", "Rel"), filepath.Join(outside, "Rel")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(outside, "Rel", "secret.mkv")
	if err := os.WriteFile(secret, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(root, "__all__", "Escape")
	if err := os.Symlink(filepath.Join(outside, "Rel"), escape); err != nil {
		t.Fatal(err)
	}
	s := &helperServer{root: root}
	refused := func(op, p string) {
		t.Helper()
		resp := s.handle(helperRequest{Op: op, Path: []byte(p)})
		if err := resp.Err.decode(); !errors.Is(err, ErrNotOnMount) {
			t.Errorf("%s %s: %v (%+v)", op, p, err, resp)
		}
	}
	answered := func(op, p string) {
		t.Helper()
		resp := s.handle(helperRequest{Op: op, Path: []byte(p)})
		if err := resp.Err.decode(); errors.Is(err, ErrNotOnMount) {
			t.Errorf("%s %s: %v (%+v)", op, p, err, resp)
		}
	}
	for _, op := range []string{opStat, opLstat, opReadlink, opReadDir, opEval, opProbe} {
		refused(op, "__all__/Rel")
		refused(op, root+"/__all__/./Rel")
		if op == opEval || op == opProbe {
			refused(op, secret)
			refused(op, filepath.Dir(root))
			refused(op, root+"/__all__/../../"+filepath.Base(outside))
			continue
		}
		answered(op, secret)
		answered(op, filepath.Dir(root))
		answered(op, root+"/__all__/../../"+filepath.Base(outside))
	}
	for _, op := range []string{opStat, opReadDir, opEval} {
		refused(op, escape)
	}
	refused(opStat, filepath.Join(escape, "secret.mkv"))
	refused(opProbe, filepath.Join(escape, "secret.mkv"))
	// The symlink itself may be read (lstat, readlink), as zurg's listing shows it.
	if resp := s.handle(helperRequest{Op: opReadlink, Path: []byte(escape)}); resp.Err != nil || string(resp.Target) != filepath.Join(outside, "Rel") {
		t.Errorf("readlink %+v", resp)
	}
	for _, op := range []string{"write", "unlink", "rename", "mkdir", "open", ""} {
		resp := s.handle(helperRequest{Op: op, Path: []byte(filepath.Join(root, "__all__", "Rel", "new"))})
		if resp.Err == nil || !strings.Contains(resp.Err.Msg, "unknown operation") {
			t.Errorf("%q: %+v", op, resp)
		}
	}
	if ents, _ := os.ReadDir(filepath.Join(root, "__all__", "Rel")); len(ents) != 0 {
		t.Errorf("files made: %v", ents)
	}

	// Through Theatarr: a client's path outside the mount is read in a helper too (TestMountHelperRunsPathsOutsideRoot).
	m := helperMount(t, time.Second)
	if _, err := m.Stat(context.Background(), secret); err != nil {
		t.Errorf("stat outside the mount: %v", err)
	}
	if h := m.Breaker().Helper; h.Starts == 0 {
		t.Errorf("no helper started for a path outside the mount: %+v", h)
	}
}

// mkvBytes is a small Matroska file: EBML header, duration, one HEVC video track.
func mkvBytes() []byte {
	el := func(id []byte, parts ...[]byte) []byte {
		body := bytes.Join(parts, nil)
		size := make([]byte, 8)
		binary.BigEndian.PutUint64(size, uint64(len(body)))
		size[0] = 0x01
		return append(append(append([]byte{}, id...), size...), body...)
	}
	uint := func(id []byte, v uint64) []byte {
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, v)
		return el(id, b)
	}
	f64 := func(id []byte, v float64) []byte {
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, math.Float64bits(v))
		return el(id, b)
	}
	return append(el([]byte{0x1A, 0x45, 0xDF, 0xA3}, el([]byte{0x42, 0x82}, []byte("matroska"))),
		el([]byte{0x18, 0x53, 0x80, 0x67},
			el([]byte{0x15, 0x49, 0xA9, 0x66}, uint([]byte{0x2A, 0xD7, 0xB1}, 1000000), f64([]byte{0x44, 0x89}, 7200000)),
			el([]byte{0x16, 0x54, 0xAE, 0x6B}, el([]byte{0xAE}, uint([]byte{0xD7}, 1), uint([]byte{0x83}, 1),
				el([]byte{0x86}, []byte("V_MPEGH/ISO/HEVC")),
				el([]byte{0xE0}, uint([]byte{0xB0}, 3840), uint([]byte{0xBA}, 2160)))))...)
}

// Every mount call answers the same through the helper as in Theatarr itself: values, error messages, and what errors.Is
// finds in them.
func TestMountHelperMatchesInProcess(t *testing.T) {
	t.Parallel()
	hm := helperMount(t, 5*time.Second)
	root := hm.Path
	rel := filepath.Join(root, "__all__", "Rel")
	for name, body := range map[string][]byte{"movie.mkv": mkvBytes(), "notes.nfo": []byte("x"),
		"Sub/ep.mkv": []byte("short"), "Sub/Deep/x.mp4": make([]byte, 64)} {
		p := filepath.Join(rel, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("movie.mkv", filepath.Join(rel, "link.mkv")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "__all__", "Other"), 0o755); err != nil {
		t.Fatal(err)
	}
	im := &Mount{Path: root, AllDir: "__all__", StatTimeout: 5 * time.Second}
	ctx := context.Background()

	type out struct {
		V   any
		Err string
		Not bool // errors.Is(err, fs.ErrNotExist)
		Off bool // errors.Is(err, ErrNotOnMount)
	}
	norm := func(v any, err error) out {
		o := out{Err: "<nil>", Not: errors.Is(err, fs.ErrNotExist), Off: errors.Is(err, ErrNotOnMount)}
		if err != nil {
			o.Err = err.Error()
		}
		switch x := v.(type) {
		case os.FileInfo:
			if x != nil {
				o.V = [5]any{x.Name(), x.Size(), x.Mode(), x.ModTime().UnixNano(), x.IsDir()}
			}
		case []os.DirEntry:
			var names []string
			for _, e := range x {
				fi, err := e.Info()
				size := int64(-1)
				if err == nil && !e.IsDir() {
					size = fi.Size()
				}
				names = append(names, e.Name()+"|"+e.Type().String()+"|"+strconv.FormatInt(size, 10))
			}
			o.V = names
		case *probe.Result:
			if x != nil {
				o.V = *x
			}
		default:
			o.V = v
		}
		return o
	}
	calls := map[string]func(m *Mount) out{
		"healthy": func(m *Mount) out { return norm(nil, m.Healthy(ctx)) },
		"readdir all": func(m *Mount) out {
			v, err := m.ReadDir(ctx, m.AllPath())
			return norm(v, err)
		},
		"readdir release": func(m *Mount) out { v, err := m.ReadDir(ctx, rel); return norm(v, err) },
		"readdir missing": func(m *Mount) out {
			v, err := m.ReadDir(ctx, filepath.Join(rel, "nope"))
			return norm(v, err)
		},
		"readdir file": func(m *Mount) out {
			v, err := m.ReadDir(ctx, filepath.Join(rel, "notes.nfo"))
			return norm(v, err)
		},
		"stat file":    func(m *Mount) out { v, err := m.Stat(ctx, filepath.Join(rel, "movie.mkv")); return norm(v, err) },
		"stat dir":     func(m *Mount) out { v, err := m.Stat(ctx, rel); return norm(v, err) },
		"stat link":    func(m *Mount) out { v, err := m.Stat(ctx, filepath.Join(rel, "link.mkv")); return norm(v, err) },
		"stat missing": func(m *Mount) out { v, err := m.Stat(ctx, filepath.Join(rel, "nope.mkv")); return norm(v, err) },
		"find release": func(m *Mount) out { v, err := m.FindRelease(ctx, []string{"Nope", "Rel"}); return norm(v, err) },
		"find none":    func(m *Mount) out { v, err := m.FindRelease(ctx, []string{"Nope"}); return norm(v, err) },
		"video files": func(m *Mount) out {
			v, err := m.VideoFiles(ctx, filepath.Join(root, "__all__", "Other"))
			return norm(v, err)
		},
		"locate":  func(m *Mount) out { v, err := m.Locate(ctx, filepath.Join(rel, "Sub", "ep.mkv")); return norm(v, err) },
		"answers": func(m *Mount) out { v, err := m.answers(ctx, m.AllPath()); return norm(v, err) },
		"probe mkv": func(m *Mount) out {
			v, err := m.ProbeFile(ctx, filepath.Join(rel, "movie.mkv"), 0, nil)
			return norm(v, err)
		},
		"probe other": func(m *Mount) out {
			v, err := m.ProbeFile(ctx, filepath.Join(rel, "Sub", "ep.mkv"), 0, nil)
			return norm(v, err)
		},
		"probe budget": func(m *Mount) out {
			v, err := m.ProbeFile(ctx, filepath.Join(rel, "movie.mkv"), 8, nil)
			return norm(v, err)
		},
		"probe missing": func(m *Mount) out {
			v, err := m.ProbeFile(ctx, filepath.Join(rel, "gone.mkv"), 0, nil)
			return norm(v, err)
		},
	}
	for name, call := range calls {
		got, want := call(hm), call(im)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\nhelper     %+v\nin process %+v", name, got, want)
		}
	}
	if h := hm.Breaker().Helper; h.Starts == 0 || h.Kills != 0 || h.Crashes != 0 {
		t.Errorf("helper state %+v", h)
	}
	// The listing of a release (sizes, symlinks refused) matches too.
	got, gerr := hm.AllFiles(ctx, rel)
	want, werr := im.AllFiles(ctx, rel)
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(norm(nil, gerr), norm(nil, werr)) {
		t.Errorf("all files: helper %v %v, in process %v %v", got, gerr, want, werr)
	}
	// Probe errors keep their sentinel.
	if _, err := hm.ProbeFile(ctx, filepath.Join(rel, "Sub", "ep.mkv"), 0, nil); !errors.Is(err, probe.ErrUnknownFormat) {
		t.Errorf("probe error %v", err)
	}
}
