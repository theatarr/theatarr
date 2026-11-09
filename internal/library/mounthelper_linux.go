package library

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// helperSysProcAttr has the kernel SIGKILL a helper when Theatarr goes, even one blocked in a call that never reads its
// closed stdin. The signal follows the thread that started the helper, not the process: Go ends a thread only when a
// goroutine locked to it (runtime.LockOSThread) returns, and nothing that starts helpers locks one; at worst a helper
// would be killed early, which the caller sees as a crash. watchParent covers what this misses.
func helperSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

// openat2 with RESOLVE_BENEATH: the kernel resolves the path from the root and refuses (EXDEV) any step out of it,
// through "..", an absolute symlink or one that leads elsewhere. Kernels before 5.6 (ENOSYS) and seccomp filters
// that refuse the call (EPERM) fall back to the portable calls.
var openat2Works = sync.OnceValue(func() bool {
	fd, err := unix.Openat2(unix.AT_FDCWD, "/", &unix.OpenHow{Flags: unix.O_PATH | unix.O_CLOEXEC})
	if err != nil {
		return false
	}
	unix.Close(fd)
	return true
})

// beneathFD opens rel under the root with flags, refusing an escape.
func (s *helperServer) beneathFD(op, p, rel string, flags uint64) (int, error) {
	root, err := unix.Open(s.root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, &fs.PathError{Op: op, Path: p, Err: err}
	}
	defer unix.Close(root)
	how := &unix.OpenHow{Flags: flags | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS}
	for range 10 {
		fd, err := unix.Openat2(root, rel, how)
		switch {
		case err == nil:
			return fd, nil
		case errors.Is(err, unix.EINTR), errors.Is(err, unix.EAGAIN): // a rename raced the lookup
			continue
		case errors.Is(err, unix.EXDEV):
			return -1, &fs.PathError{Op: op, Path: p, Err: fmt.Errorf("%w (%s): resolves outside it", ErrNotOnMount, s.root)}
		default:
			return -1, &fs.PathError{Op: op, Path: p, Err: err}
		}
	}
	return -1, &fs.PathError{Op: op, Path: p, Err: unix.EAGAIN}
}

func (s *helperServer) stat(op, p, rel string, nofollow bool) (os.FileInfo, error) {
	if !openat2Works() {
		return s.portableStat(op, p, rel, nofollow)
	}
	flags := uint64(unix.O_PATH)
	if nofollow {
		flags |= unix.O_NOFOLLOW
	}
	fd, err := s.beneathFD(op, p, rel, flags)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), p)
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, &fs.PathError{Op: op, Path: p, Err: errors.Unwrap(err)}
	}
	return fi, nil
}

func (s *helperServer) readlink(p, rel string) (string, error) {
	if !openat2Works() {
		return s.portableReadlink(p, rel)
	}
	dir, err := s.beneathFD(opReadlink, p, filepath.Dir(rel), unix.O_PATH|unix.O_DIRECTORY)
	if err != nil {
		return "", err
	}
	defer unix.Close(dir)
	for n := 256; ; n *= 2 {
		buf := make([]byte, n)
		got, err := unix.Readlinkat(dir, filepath.Base(rel), buf)
		if err != nil {
			return "", &fs.PathError{Op: opReadlink, Path: p, Err: err}
		}
		if got < n {
			return string(buf[:got]), nil
		}
	}
}

// open opens p to read, a directory (dir) as os.ReadDir does (O_DIRECTORY).
func (s *helperServer) open(op, p, rel string, dir bool) (*os.File, error) {
	if !openat2Works() {
		return s.portableOpen(op, p, rel, dir)
	}
	flags := uint64(unix.O_RDONLY)
	if dir {
		flags |= unix.O_DIRECTORY
	}
	fd, err := s.beneathFD(op, p, rel, flags)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), p), nil
}

// eval resolves p's symlinks: the kernel resolves it beneath the root (openat2), and the answer is the path of what
// it opened, read from that descriptor (/proc/self/fd), not p resolved again by name, which a symlink changed in
// between could lead elsewhere.
func (s *helperServer) eval(p, rel string) (string, error) {
	if !openat2Works() {
		return s.portableEval(p, rel)
	}
	fd, err := s.beneathFD("lstat", p, rel, unix.O_PATH)
	if err != nil {
		return "", err
	}
	defer unix.Close(fd)
	real, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
	if err != nil {
		return "", &fs.PathError{Op: opEval, Path: p, Err: errors.Unwrap(err)}
	}
	return real, nil
}
