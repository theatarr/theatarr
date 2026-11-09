package library

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// A helper blocked in a call goes when Theatarr does: the test binary plays Theatarr (THEATARR_LIBRARY_HELPER_PARENT), starts a
// helper and sends it a probe of a FIFO, which blocks in open(2); killing the parent ends the helper.
func TestMountHelperEndsWithParent(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "stuck.mkv")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skip("mkfifo:", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), "THEATARR_LIBRARY_HELPER_PARENT="+fifo)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("parent: %v", err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(line))
	if pid <= 0 || !processAlive(pid) {
		t.Fatalf("helper pid %q", line)
	}
	time.Sleep(200 * time.Millisecond) // the helper is in open(2)
	if !processAlive(pid) {
		t.Fatal("helper gone before its parent")
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	for range 100 {
		if !processAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("helper %d outlived its parent", pid)
}

// processAlive reports whether pid runs (a zombie has ended).
func processAlive(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return syscall.Kill(pid, 0) == nil
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	return i < 0 || i+2 >= len(s) || s[i+2] != 'Z'
}

// helperParent is the parent of TestMountHelperEndsWithParent: it starts a helper, prints its pid, sends it a probe
// of fifo and waits to be killed.
func helperParent(fifo string) {
	p := newHelperPool(filepath.Dir(fifo), *testHelperConfig())
	h, err := p.startProc()
	if err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
	os.Stdout.WriteString(strconv.Itoa(h.proc.Pid) + "\n")
	if err := writeFrame(h.in, helperRequest{ID: 1, Op: opProbe, Path: []byte(fifo)}); err != nil {
		os.Exit(1)
	}
	for {
		time.Sleep(time.Hour)
	}
}

// The parent watch ends the helper once its parent changes (the parent died and the helper was reparented).
func TestWatchParent(t *testing.T) {
	var ppid atomic.Int32
	ppid.Store(7)
	exited := make(chan int, 1)
	stop := watchParent(func() int { return int(ppid.Load()) }, 10*time.Millisecond, func(code int) { exited <- code })
	defer stop()
	select {
	case <-exited:
		t.Fatal("exited while the parent lives")
	case <-time.After(50 * time.Millisecond):
	}
	ppid.Store(1)
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("no exit after the parent changed")
	}
}
