package library

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/theatarr/theatarr/internal/library/probe"
)

// The mount helper: a child process (theatarr mount-helper, the same binary) that makes Theatarr's filesystem calls on the
// mount. A FUSE call that never returns blocks the helper, not Theatarr: Theatarr waits for the answer with a deadline and,
// when it misses it, kills the helper and starts another (mountpool.go). docs/design/library.md "Mount guard".
//
// The protocol is one request, then one answer, on the helper's stdin and stdout: each a JSON object behind its
// length (4 bytes, big endian). Paths, names and link targets are bytes (base64 in the JSON): a Unix file name need
// not be UTF-8, and a JSON string would turn its other bytes into U+FFFD, a different name. The helper reads only:
// it stats, lists, reads links, probes file headers and reads file heads under its mount root, answers the calls
// that read no file content on a path outside it (place), and opens nothing for writing.

// helperMaxFrame bounds one frame: a release-root listing of 100k entries is a few MiB.
const helperMaxFrame = 64 << 20

// Helper operations.
const (
	opStat     = "stat"
	opLstat    = "lstat"
	opReadlink = "readlink"
	opReadDir  = "readdir"
	opEval     = "eval"
	opProbe    = "probe"
	opHead     = "head"
	opRange    = "range"
)

type helperRequest struct {
	ID   uint64 `json:"id"`
	Op   string `json:"op"`
	Path []byte `json:"path"`
	// N limits a listing to N entries, unsorted (0: all, sorted by name as os.ReadDir); Max is a probe's byte budget,
	// or a head or range read's length. Off is where a range read starts, Rate caps its bytes per second (0: none).
	N    int   `json:"n,omitempty"`
	Max  int64 `json:"max,omitempty"`
	Off  int64 `json:"off,omitempty"`
	Rate int64 `json:"rate,omitempty"`
}

type helperResponse struct {
	ID      uint64        `json:"id"`
	Err     *helperErr    `json:"err,omitempty"`
	Info    *helperInfo   `json:"info,omitempty"`
	Entries []helperEntry `json:"entries,omitempty"`
	Target  []byte        `json:"target,omitempty"`
	Probe   *probe.Result `json:"probe,omitempty"`
	Read    int64         `json:"read,omitempty"` // a head or range read's bytes
	Size    int64         `json:"size,omitempty"` // a range read's file size
}

// helperErr carries an error across the pipe: its message, and what errors.Is must still find in it (the errno,
// or one of the sentinels below).
type helperErr struct {
	Msg   string  `json:"msg"`
	Errno uintptr `json:"errno,omitempty"`
	Kind  string  `json:"kind,omitempty"`
}

var helperKinds = map[string]error{
	"not_on_mount":   ErrNotOnMount,
	"budget":         probe.ErrBudget,
	"unknown_format": probe.ErrUnknownFormat,
}

func encodeHelperErr(err error) *helperErr {
	if err == nil {
		return nil
	}
	he := &helperErr{Msg: err.Error()}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		he.Errno = uintptr(errno)
	}
	for k, s := range helperKinds {
		if errors.Is(err, s) {
			he.Kind = k
		}
	}
	return he
}

// helperError is an error the helper returned: the same message, and errors.Is finds its errno or sentinel.
type helperError struct {
	msg string
	err error
}

func (e *helperError) Error() string { return e.msg }
func (e *helperError) Unwrap() error { return e.err }

func (he *helperErr) decode() error {
	if he == nil {
		return nil
	}
	e := &helperError{msg: he.Msg}
	switch {
	case he.Kind != "":
		e.err = helperKinds[he.Kind]
	case he.Errno != 0:
		e.err = syscall.Errno(he.Errno)
	}
	return e
}

type helperInfo struct {
	Name    []byte `json:"name"`
	Size    int64  `json:"size"`
	Mode    uint32 `json:"mode"`
	ModTime int64  `json:"mtime"` // Unix nanoseconds
}

func encodeInfo(fi os.FileInfo) *helperInfo {
	return &helperInfo{Name: []byte(fi.Name()), Size: fi.Size(), Mode: uint32(fi.Mode()), ModTime: fi.ModTime().UnixNano()}
}

// helperFileInfo is a FileInfo the helper read. Sys is nil: nothing reads owners or inodes of mount files.
type helperFileInfo struct{ i helperInfo }

func (f helperFileInfo) Name() string       { return string(f.i.Name) }
func (f helperFileInfo) Size() int64        { return f.i.Size }
func (f helperFileInfo) Mode() fs.FileMode  { return fs.FileMode(f.i.Mode) }
func (f helperFileInfo) ModTime() time.Time { return time.Unix(0, f.i.ModTime) }
func (f helperFileInfo) IsDir() bool        { return f.Mode().IsDir() }
func (f helperFileInfo) Sys() any           { return nil }

type helperEntry struct {
	Name []byte `json:"name"`
	Type uint32 `json:"type"`
}

func writeFrame(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > helperMaxFrame {
		return fmt.Errorf("mount helper: frame of %d bytes", len(b))
	}
	buf := make([]byte, 4+len(b))
	binary.BigEndian.PutUint32(buf, uint32(len(b)))
	copy(buf[4:], b)
	_, err = w.Write(buf)
	return err
}

func readFrame(r io.Reader, v any) error {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(n[:])
	if size > helperMaxFrame {
		return fmt.Errorf("mount helper: frame of %d bytes", size)
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// helperServer answers requests for paths under root.
type helperServer struct {
	root string
	// hang, when set (tests), blocks a call forever or ends the process mid-call, as a dead FUSE mount or a crash.
	hang func(op, path string)
}

// ServeMountHelper runs the mount helper (theatarr mount-helper): it answers requests on in until it reads EOF. root is
// the mount: a symlink under it that leads out of it is refused (ErrNotOnMount); a path outside it gets the calls
// that read no file content only (place). The helper also
// ends when its parent does (watchParent), even in a call that never returns.
func ServeMountHelper(root string, in io.Reader, out io.Writer) error {
	defer watchParent(os.Getppid, time.Second, os.Exit)()
	return (&helperServer{root: root}).serve(in, out)
}

// watchParent ends the process (exit(2)) once getppid changes: the parent is gone and the process was reparented.
// It runs in its own goroutine, so a call blocked in the kernel does not stop it; EOF on stdin covers only an idle
// helper, and the parent-death signal (helperSysProcAttr) only Linux. stop ends the watch.
func watchParent(getppid func() int, every time.Duration, exit func(int)) (stop func()) {
	ppid := getppid()
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if getppid() != ppid {
					exit(2)
					return
				}
			}
		}
	}()
	return func() { close(done) }
}

func (s *helperServer) serve(in io.Reader, out io.Writer) error {
	if !filepath.IsAbs(s.root) {
		return fmt.Errorf("mount helper: root %q is not absolute", s.root)
	}
	s.root = filepath.Clean(s.root)
	r := bufio.NewReader(in)
	for {
		var req helperRequest
		if err := readFrame(r, &req); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := writeFrame(out, s.handle(req)); err != nil {
			return err
		}
	}
}

func (s *helperServer) handle(req helperRequest) (resp helperResponse) {
	resp.ID = req.ID
	path := string(req.Path)
	defer func() {
		if r := recover(); r != nil {
			resp = helperResponse{ID: req.ID, Err: &helperErr{Msg: fmt.Sprintf("mount helper: %s %s: panic: %v",
				req.Op, path, r)}}
		}
	}()
	rel, outside, err := s.place(req.Op, path)
	if err != nil {
		resp.Err = encodeHelperErr(err)
		return resp
	}
	if s.hang != nil {
		s.hang(req.Op, path)
	}
	var target string
	switch req.Op {
	case opStat, opLstat:
		var fi os.FileInfo
		switch {
		case outside && req.Op == opLstat:
			fi, err = os.Lstat(path)
		case outside:
			fi, err = os.Stat(path)
		default:
			fi, err = s.stat(req.Op, path, rel, req.Op == opLstat)
		}
		if err == nil {
			resp.Info = encodeInfo(fi)
		}
	case opReadlink:
		if outside {
			target, err = os.Readlink(path)
		} else {
			target, err = s.readlink(path, rel)
		}
		if err == nil {
			resp.Target = []byte(target)
		}
	case opReadDir:
		var ents []os.DirEntry
		if ents, err = s.readDir(path, rel, req.N, outside); err == nil {
			resp.Entries = make([]helperEntry, len(ents))
			for i, e := range ents {
				resp.Entries[i] = helperEntry{Name: []byte(e.Name()), Type: uint32(e.Type())}
			}
		}
	case opEval:
		if target, err = s.eval(path, rel); err == nil {
			resp.Target = []byte(target)
		}
	case opProbe:
		resp.Probe, err = s.probe(path, rel, req.Max)
	case opHead:
		var f *os.File
		if f, err = s.open("open", path, rel, false); err == nil {
			resp.Read, err = readHead(f, req.Max)
			f.Close()
		}
	case opRange:
		var f *os.File
		if f, err = s.open("open", path, rel, false); err == nil {
			resp.Read, resp.Size, err = readRange(f, req.Off, req.Max, req.Rate)
			f.Close()
		}
	default:
		err = fmt.Errorf("mount helper: unknown operation %q", req.Op)
	}
	resp.Err = encodeHelperErr(err)
	return resp
}

// place places a path by its name: rel under the root, or outside. A path under the root must be clean; outside
// it, only the calls that read no file content (stat, lstat, readlink, listing) are answered: a client's local
// folder of symlinks into the mount (a manual import preview) is read here, so that following those symlinks into
// a dead mount blocks a helper and not Theatarr. Such a call does what it would in Theatarr, with no root to stay under; file
// contents (probe) and symlink resolution (eval) stay under the root. A relative path is refused.
func (s *helperServer) place(op, p string) (rel string, outside bool, err error) {
	if !filepath.IsAbs(p) {
		return "", false, &fs.PathError{Op: op, Path: p, Err: fmt.Errorf("%w (%s): not an absolute path", ErrNotOnMount, s.root)}
	}
	rel, err = filepath.Rel(s.root, filepath.Clean(p))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		switch op {
		case opStat, opLstat, opReadlink, opReadDir:
			return "", true, nil
		}
		return "", false, &fs.PathError{Op: op, Path: p, Err: fmt.Errorf("%w (%s)", ErrNotOnMount, s.root)}
	}
	if filepath.Clean(p) != p {
		return "", false, &fs.PathError{Op: op, Path: p, Err: fmt.Errorf("%w (%s): not a clean absolute path", ErrNotOnMount, s.root)}
	}
	return rel, false, nil
}

func (s *helperServer) readDir(p, rel string, n int, outside bool) ([]os.DirEntry, error) {
	if outside && n <= 0 {
		return os.ReadDir(p)
	}
	var f *os.File
	var err error
	if outside {
		f, err = os.Open(p)
	} else {
		f, err = s.open("open", p, rel, true)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if n > 0 {
		ents, err := f.ReadDir(n)
		if errors.Is(err, io.EOF) {
			err = nil
		}
		return ents, err
	}
	ents, err := f.ReadDir(-1)
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	return ents, err
}

func (s *helperServer) probe(p, rel string, max int64) (*probe.Result, error) {
	f, err := s.open("open", p, rel, false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return probe.Reader(f, st.Size(), max)
}

// readHead reads f from its start, at most max bytes, and drops them: the now playing pre-warm's one sequential
// read. It returns how many bytes it read; the end of the file is not an error.
func readHead(f io.Reader, max int64) (int64, error) {
	n, err := io.CopyBuffer(io.Discard, io.LimitReader(f, max), make([]byte, 256<<10))
	return n, err
}

// readRange reads f from off, at most n bytes, in order, at most rate bytes per second (0: no cap), and drops them:
// a piece of the now playing pre-warm's whole-file read. It returns how many bytes it read and the file's size.
func readRange(f *os.File, off, n, rate int64) (int64, int64, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return 0, st.Size(), err
	}
	buf := make([]byte, 256<<10)
	start := time.Now()
	var read int64
	for read < n {
		k, err := f.Read(buf[:min(int64(len(buf)), n-read)])
		read += int64(k)
		if err == io.EOF {
			break
		}
		if err != nil {
			return read, st.Size(), err
		}
		if rate > 0 {
			if ahead := time.Duration(float64(read)/float64(rate)*float64(time.Second)) - time.Since(start); ahead > 0 {
				time.Sleep(ahead)
			}
		}
	}
	return read, st.Size(), nil
}

// The portable calls, for systems without openat2 (mounthelper_linux.go): os.Root resolves each path from a
// descriptor of the root, one component at a time without following a symlink by name, and refuses any step out of
// the root ("..", an absolute symlink, one that leads elsewhere), as RESOLVE_BENEATH does. What it checks is what it
// opens: nothing is resolved twice by name. On unix os.Root holds against a symlink swapped mid-call; Go does not
// promise that on every other system (Windows reparse points, plan9), which Theatarr does not run on.

func (s *helperServer) openRoot(op, p string) (*os.Root, error) {
	r, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, &fs.PathError{Op: op, Path: p, Err: errors.Unwrap(err)}
	}
	return r, nil
}

// rootErr is an os.Root error as os's own calls word it (op and the full path), ErrNotOnMount for an escape.
func (s *helperServer) rootErr(op, p string, err error) error {
	var pe *fs.PathError
	if !errors.As(err, &pe) {
		return err
	}
	if pe.Err.Error() == "path escapes from parent" { // os.Root's escape error; not exported
		return &fs.PathError{Op: op, Path: p, Err: fmt.Errorf("%w (%s): resolves outside it", ErrNotOnMount, s.root)}
	}
	return &fs.PathError{Op: op, Path: p, Err: pe.Err}
}

func (s *helperServer) portableStat(op, p, rel string, nofollow bool) (os.FileInfo, error) {
	r, err := s.openRoot(op, p)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var fi os.FileInfo
	if nofollow {
		fi, err = r.Lstat(rel)
	} else {
		fi, err = r.Stat(rel)
	}
	if err != nil {
		return nil, s.rootErr(op, p, err)
	}
	return fi, nil
}

func (s *helperServer) portableReadlink(p, rel string) (string, error) {
	r, err := s.openRoot(opReadlink, p)
	if err != nil {
		return "", err
	}
	defer r.Close()
	t, err := r.Readlink(rel)
	if err != nil {
		return "", s.rootErr(opReadlink, p, err)
	}
	return t, nil
}

func (s *helperServer) portableOpen(op, p, rel string, dir bool) (*os.File, error) {
	r, err := s.openRoot(op, p)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	f, err := r.Open(rel)
	if err != nil {
		return nil, s.rootErr(op, p, err)
	}
	if dir {
		if fi, err := f.Stat(); err != nil || !fi.IsDir() {
			f.Close()
			if err == nil {
				err = syscall.ENOTDIR
			}
			return nil, &fs.PathError{Op: op, Path: p, Err: err}
		}
	}
	return f, nil
}

// portableEval resolves rel's symlinks one component at a time, each read (lstat, readlink) through os.Root, so no
// step leaves the root; the answer is the root joined with the resolved components, by construction inside it.
// Under a concurrent change it may describe a path that has since changed, but never one outside the root.
func (s *helperServer) portableEval(p, rel string) (string, error) {
	r, err := s.openRoot(opEval, p)
	if err != nil {
		return "", err
	}
	defer r.Close()
	var done []string
	todo := strings.Split(rel, string(filepath.Separator))
	for links := 0; len(todo) > 0; {
		c := todo[0]
		todo = todo[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			if len(done) == 0 {
				return "", &fs.PathError{Op: "lstat", Path: p, Err: fmt.Errorf("%w (%s): resolves outside it", ErrNotOnMount, s.root)}
			}
			done = done[:len(done)-1]
			continue
		}
		next := filepath.Join(append(done, c)...)
		fi, err := r.Lstat(next)
		if err != nil {
			return "", s.rootErr("lstat", p, err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			done = append(done, c)
			continue
		}
		if links++; links > 255 {
			return "", &fs.PathError{Op: "lstat", Path: p, Err: syscall.ELOOP}
		}
		t, err := r.Readlink(next)
		if err != nil {
			return "", s.rootErr("readlink", p, err)
		}
		if filepath.IsAbs(t) {
			return "", &fs.PathError{Op: "lstat", Path: p, Err: fmt.Errorf("%w (%s): resolves outside it", ErrNotOnMount, s.root)}
		}
		todo = append(strings.Split(t, string(filepath.Separator)), todo...)
	}
	return filepath.Join(append([]string{s.root}, done...)...), nil
}
