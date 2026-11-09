package library

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/theatarr/theatarr/internal/library/naming"
	"github.com/theatarr/theatarr/internal/library/probe"
)

// ErrMountDown means the zurg mount did not answer (a hang, ENOTCONN, or the release root missing). Callers pause:
// an unhealthy mount is never read as "content missing" (docs/design/library.md).
var ErrMountDown = errors.New("library: mount unhealthy")

// Mount is the zurg mount, read-only. Every call into it goes through its guard (breaker.go), in helper processes
// when it has them (mountpool.go).
type Mount struct {
	Path        string // where this process reads it
	LinkPath    string // what symlinks point at
	AllDir      string
	StatTimeout time.Duration
	// The guard: MaxCalls outstanding calls, the circuit opens after BreakAfter timeouts in a row and lets one call
	// through after Cooldown. Zero means the defaults. Now is the guard's clock (tests).
	MaxCalls   int
	BreakAfter int
	Cooldown   time.Duration
	Now        func() time.Time

	// The state below outlives a settings generation (inherit); the fields above are configuration.
	once sync.Once
	g    *guard
	// fs is the filesystem calls on the mount; nil is the os package (tests put a slow one here).
	fs mountFS
	// pool is the helper processes mount calls run in (mountpool.go); nil runs them in this process.
	pool *helperPool
}

// mountFS is the calls the library makes on the mount.
type mountFS interface {
	Stat(name string) (os.FileInfo, error)
	Lstat(name string) (os.FileInfo, error)
	Readlink(name string) (string, error)
	ReadDir(name string) ([]os.DirEntry, error)
	EvalSymlinks(name string) (string, error)
}

type osFS struct{}

func (osFS) Stat(name string) (os.FileInfo, error)      { return os.Stat(name) }
func (osFS) Lstat(name string) (os.FileInfo, error)     { return os.Lstat(name) }
func (osFS) Readlink(name string) (string, error)       { return os.Readlink(name) }
func (osFS) ReadDir(name string) ([]os.DirEntry, error) { return os.ReadDir(name) }
func (osFS) EvalSymlinks(name string) (string, error)   { return filepath.EvalSymlinks(name) }

func (m *Mount) fsys() mountFS {
	if m.fs == nil {
		return osFS{}
	}
	return m.fs
}

// AllPath is the directory holding every release, as read here.
func (m *Mount) AllPath() string { return filepath.Join(m.Path, m.AllDir) }

// Healthy stats the release root with a timeout.
func (m *Mount) Healthy(ctx context.Context) error {
	v, err := m.withTimeout(ctx, m.AllPath(), func(f mountFS) (any, error) { return f.Stat(m.AllPath()) })
	if errors.Is(err, fs.ErrNotExist) {
		// The release root itself is missing: the mount is not there.
		return fmt.Errorf("%w: %v", ErrMountDown, err)
	}
	if err == nil && !v.(os.FileInfo).IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrMountDown, m.AllPath())
	}
	return err
}

// checkListing refuses a release-root listing that is implausibly small, as if the mount were down. A zurg that
// restarted, or lost its account for a while, can serve a present but empty (or half-loaded) __all__: Healthy
// passes, yet every link would read as missing. So for relink, the dangling report, verify and the follower refresh
// the listing of n entries counts as ErrMountDown when
//   - it is empty;
//   - it has fewer than half the entries of the largest listing accepted in this process (a restart forgets it);
//   - it has fewer than half as many entries as there are distinct release folders that Theatarr's active links point
//     into (linked, read from the link table, so it holds across restarts).
//
// Nothing is marked dead, repointed or refreshed then; the next run reads the mount again. A real shrink of the
// account below half (never expected: debrid torrents are shared) keeps the sweep refusing until Theatarr restarts, and
// while active links still point into more than twice as many folders as remain; those links are left for a human.
func (m *Mount) checkListing(n, linked int) error {
	g := m.guard()
	g.mu.Lock()
	defer g.mu.Unlock()
	root := m.AllPath()
	switch {
	case n == 0:
		return fmt.Errorf("%w: %s lists nothing", ErrMountDown, m.AllPath())
	case n*2 < g.listMax[root]:
		return fmt.Errorf("%w: %s lists %d entries, fewer than half of the %d listed before", ErrMountDown,
			m.AllPath(), n, g.listMax[root])
	case n*2 < linked:
		return fmt.Errorf("%w: %s lists %d entries, fewer than half of the %d release folders active links point into",
			ErrMountDown, m.AllPath(), n, linked)
	}
	if g.listMax == nil {
		g.listMax = map[string]int{}
	}
	g.listMax[root] = max(g.listMax[root], n)
	return nil
}

// ReadDir lists a directory under the mount with the timeout.
func (m *Mount) ReadDir(ctx context.Context, dir string) ([]os.DirEntry, error) {
	v, err := m.withTimeout(ctx, dir, func(f mountFS) (any, error) { return f.ReadDir(dir) })
	if err != nil {
		return nil, err
	}
	return v.([]os.DirEntry), nil
}

// Stat stats a path under the guard, following symlinks. Paths a client names go through it too, on the mount or
// not: a local folder of symlinks into the mount (decypharr's) reaches the mount through them. Such a path, outside
// the mount root, runs in a helper too when the mount has them (helperFor).
func (m *Mount) Stat(ctx context.Context, p string) (os.FileInfo, error) {
	v, err := m.withTimeout(ctx, p, func(f mountFS) (any, error) { return f.Stat(p) })
	if err != nil {
		return nil, err
	}
	return v.(os.FileInfo), nil
}

// contained checks, under the guard, that p resolved (every symlink on the way followed) is inside the mount
// resolved: a release folder reached through a symlink out of the mount is not mount content, and nothing of it is
// linked.
func (m *Mount) contained(ctx context.Context, p string) error {
	v, err := m.withTimeout(ctx, p, func(f mountFS) (any, error) {
		root, err := f.EvalSymlinks(m.Path)
		if err != nil {
			return nil, err
		}
		real, err := f.EvalSymlinks(p)
		if err != nil {
			return nil, err
		}
		return [2]string{root, real}, nil
	})
	if err != nil {
		return err
	}
	root, real := v.([2]string)[0], v.([2]string)[1]
	if rel, err := filepath.Rel(root, real); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s resolves to %s: %w (%s)", p, real, ErrNotOnMount, m.Path)
	}
	return nil
}

// escapes reports whether rel, from filepath.Rel, leaves its base: ".." or "../...". A name that only starts with
// "..", such as a release folder "...And Justice for All", stays inside.
func escapes(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ToLink turns a path read here into the path symlinks carry.
func (m *Mount) ToLink(p string) string {
	if m.LinkPath == "" || m.LinkPath == m.Path {
		return p
	}
	rel, err := filepath.Rel(m.Path, p)
	if err != nil || escapes(rel) {
		return p
	}
	return filepath.Join(m.LinkPath, rel)
}

// FromLink turns a symlink target into the path read here, and whether it points into the mount at all.
func (m *Mount) FromLink(target string) (string, bool) {
	base := m.LinkPath
	if base == "" {
		base = m.Path
	}
	rel, err := filepath.Rel(base, target)
	if err != nil || escapes(rel) || rel == "." {
		return "", false
	}
	return filepath.Join(m.Path, rel), true
}

// ReleaseFolderNames are the folder names zurg may give a release, most likely first. zurg's own rule comes first:
// the torrent's name with strings.TrimSuffix(".mp4") and then strings.TrimSuffix(".mkv"), case sensitive, so
// "X.mkv.mp4" is "X" and "X.MKV" keeps its extension (https://notes.debridmediamanager.com/zurg/internals/naming/),
// then the same with " {hash6}" when two hashes share a name. The earlier candidates follow so nothing that matched
// before stops matching: .mkv then .mp4 trimmed in any case (decypharr's original_no_ext), with " {hash6}", and the
// untrimmed name.
func ReleaseFolderNames(hash string, names ...string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !seen[s] && !strings.ContainsAny(s, "/\x00") && s != "." && s != ".." {
			seen[s] = true
			out = append(out, s)
		}
	}
	suffix := ""
	if len(hash) >= 6 {
		suffix = " {" + strings.ToLower(hash[:6]) + "}"
	}
	withHash := func(s string) {
		add(s)
		if suffix != "" {
			add(s + suffix)
		}
	}
	for _, n := range names {
		n = strings.TrimSpace(n)
		withHash(strings.TrimSuffix(strings.TrimSuffix(n, ".mp4"), ".mkv"))
		trimmed := n
		for _, ext := range []string{".mkv", ".mp4"} {
			if strings.HasSuffix(strings.ToLower(trimmed), ext) {
				trimmed = trimmed[:len(trimmed)-len(ext)]
			}
		}
		withHash(trimmed)
		add(n)
	}
	return out
}

// FindRelease looks for the first of names under the release root. It returns ("", nil) when none is there yet,
// and an error (ErrNotOnMount) for a folder that resolves outside the mount through a symlink.
func (m *Mount) FindRelease(ctx context.Context, names []string) (string, error) {
	for _, n := range names {
		p := filepath.Join(m.AllPath(), n)
		v, err := m.withTimeout(ctx, p, func(f mountFS) (any, error) { return f.Stat(p) })
		if err == nil && v.(os.FileInfo).IsDir() {
			if err := m.contained(ctx, p); err != nil {
				return "", err
			}
			return p, nil
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	return "", nil
}

// Located is a path a client named, on the mount: the release folder's name (under the release root) and, when the
// path is inside it, the path within it (a sub-folder or one file; "" for the folder itself).
type Located struct {
	Folder string `json:"folder"`
	Rel    string `json:"rel,omitempty"`
}

// Holds reports whether a file of the release (its path inside the folder) is the located path or under it.
func (l Located) Holds(rel string) bool {
	return l.Rel == "" || rel == l.Rel || strings.HasPrefix(rel, l.Rel+string(filepath.Separator))
}

// ErrNotOnMount: a path is not on the debrid mount (nor a symlink into it). Theatarr links mount content only; it never
// copies or moves files.
var ErrNotOnMount = errors.New("not on the debrid mount")

// Locate places a path a client names (a release folder, a folder or file in it) on the mount. It accepts the path as
// read here or as links carry it, under the release root or another of zurg's directories (which hold the same
// release folders), and a symlink into the mount (decypharr's download folders). A path on the mount is placed by
// its name alone; whether it really is mount content (no symlink out of the mount on the way) is checked where the
// release is read (FindRelease, listFiles). The symlink is read under the guard: a directory on its way may itself
// be a symlink into a dead mount. A mount that does not answer (a timeout, a busy or killed helper) is ErrMountDown,
// not ErrNotOnMount, and counts against the mount's guard; ErrNotOnMount is only a path that does not exist or is
// not a symlink into the mount. Any other error (permission denied) is returned as it is.
func (m *Mount) Locate(ctx context.Context, p string) (Located, error) {
	return m.LocateMapped(ctx, p, nil)
}

// LocateMapped is Locate for a download client's path: mapTarget, when set, maps each absolute symlink target from
// the client's view to Theatarr's (its remote_path → local_path) before the mount containment check, and a path on
// the mount must exist (read under the guard): one that does not is fs.ErrNotExist, as off the mount.
func (m *Mount) LocateMapped(ctx context.Context, p string, mapTarget func(string) string) (Located, error) {
	p = filepath.Clean(p)
	if loc, ok := m.locate(p); ok {
		if mapTarget == nil {
			return loc, nil
		}
		q := p
		if l, ok := m.FromLink(p); ok {
			q = l
		}
		v, err := m.withTimeout(ctx, q, func(f mountFS) (any, error) {
			_, err := f.Stat(q)
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
				return notThere{}, nil
			}
			return nil, err
		})
		if err != nil {
			return Located{}, err
		}
		if _, gone := v.(notThere); gone {
			return Located{}, fmt.Errorf("%s: %w (%s): %w", p, ErrNotOnMount, m.Path, fs.ErrNotExist)
		}
		return loc, nil
	}
	if m.Path != "" && (p == m.Path || strings.HasPrefix(p, m.Path+string(filepath.Separator))) {
		// On the mount but not a release path (its root or a top-level directory).
		return Located{}, fmt.Errorf("%s: %w (%s)", p, ErrNotOnMount, m.Path)
	}
	v, err := m.withTimeout(ctx, p, func(f mountFS) (any, error) {
		fi, err := f.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return notThere{}, nil
		}
		if err != nil {
			return nil, err
		}
		if fi.IsDir() {
			return m.locateLinks(f, p, mapTarget)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			return "", nil
		}
		t, err := f.Readlink(p)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.EINVAL) {
			return "", nil // gone, or no longer a symlink, since the lstat
		}
		if err != nil {
			return nil, err
		}
		return t, nil
	})
	if err != nil {
		return Located{}, err
	}
	switch t := v.(type) {
	case Located:
		if t.Folder != "" {
			return t, nil
		}
	case notThere:
		// Not there (yet): a client's download folder can be reported before it is visible here.
		return Located{}, fmt.Errorf("%s: %w (%s): %w", p, ErrNotOnMount, m.Path, fs.ErrNotExist)
	case string:
		if t == "" {
			break
		}
		if !filepath.IsAbs(t) {
			t = filepath.Join(filepath.Dir(p), t)
		} else if mapTarget != nil {
			t = mapTarget(t)
		}
		if loc, ok := m.locate(filepath.Clean(t)); ok {
			return loc, nil
		}
	}
	return Located{}, fmt.Errorf("%s: %w (%s)", p, ErrNotOnMount, m.Path)
}

// notThere is Locate's answer for a path that does not exist.
type notThere struct{}

// locateLinks places a directory of symlinks into the mount (a decypharr download folder: one link per file of the
// release, in sub-folders as the torrent has them) on the release folder every link points into. Files that are not
// links are passed over (the release is read from the mount, never from here). No link, a link out of the mount or
// links into two release folders: Located{} (ErrNotOnMount). mapTarget (when set) maps absolute targets first. Run
// under the mount guard.
func (m *Mount) locateLinks(f mountFS, dir string, mapTarget func(string) string) (Located, error) {
	var folder string
	var walk func(d string, depth int) (bool, error)
	walk = func(d string, depth int) (bool, error) {
		entries, err := f.ReadDir(d)
		if err != nil {
			return false, err
		}
		for _, e := range entries {
			p := filepath.Join(d, e.Name())
			switch {
			case e.IsDir():
				if depth >= maxLinkDepth {
					return false, nil
				}
				if ok, err := walk(p, depth+1); !ok || err != nil {
					return ok, err
				}
			case e.Type()&os.ModeSymlink != 0:
				t, err := f.Readlink(p)
				if err != nil {
					return false, err
				}
				if !filepath.IsAbs(t) {
					t = filepath.Join(d, t)
				} else if mapTarget != nil {
					t = mapTarget(t)
				}
				loc, ok := m.locate(filepath.Clean(t))
				if !ok || (folder != "" && loc.Folder != folder) {
					return false, nil
				}
				folder = loc.Folder
			}
		}
		return true, nil
	}
	ok, err := walk(dir, 0)
	if err != nil || !ok {
		return Located{}, err
	}
	return Located{Folder: folder}, nil
}

// maxLinkDepth bounds the sub-folders of a download folder locateLinks reads (a season pack's extras folder).
const maxLinkDepth = 3

func (m *Mount) locate(p string) (Located, bool) {
	if m.Path == "" {
		return Located{}, false
	}
	if q, ok := m.FromLink(p); ok {
		p = q
	}
	rel, err := filepath.Rel(m.Path, p)
	if err != nil || rel == "." || escapes(rel) {
		return Located{}, false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if m.AllDir != "" {
		// <all_dir>/<release>/..., or <another zurg directory>/<release>/...
		if len(parts) < 2 {
			return Located{}, false
		}
		parts = parts[1:]
	}
	return Located{Folder: parts[0], Rel: filepath.Join(parts[1:]...)}, true
}

// MountFile is a file in a release folder.
type MountFile struct {
	Path string // as read here
	Rel  string // path inside the release folder
	Size int64
}

// VideoFiles lists the video files of a release folder (recursively, at most 4 levels), largest first.
func (m *Mount) VideoFiles(ctx context.Context, dir string) ([]MountFile, error) {
	return m.listFiles(ctx, dir, func(name string) bool { return naming.IsVideoExtension(filepath.Ext(name)) })
}

// AllFiles lists every file of a release folder (recursively, at most 4 levels), largest first.
func (m *Mount) AllFiles(ctx context.Context, dir string) ([]MountFile, error) {
	return m.listFiles(ctx, dir, func(string) bool { return true })
}

// dirEntry is a directory entry read under the guard, with its size for files (Info is an lstat on the mount too:
// its error is the mount's, and fails the listing like ReadDir's would). A symlink fails the listing too
// (ErrNotOnMount): zurg has none, one could lead out of the mount, and skipping it would judge the release by what
// is left.
type dirEntry struct {
	name string
	dir  bool
	size int64
}

func (m *Mount) listFiles(ctx context.Context, dir string, keep func(name string) bool) ([]MountFile, error) {
	var out []MountFile
	var walk func(d string, depth int) error
	walk = func(d string, depth int) error {
		v, err := m.withTimeout(ctx, d, func(f mountFS) (any, error) {
			ents, err := f.ReadDir(d)
			if err != nil {
				return nil, err
			}
			var des []dirEntry
			for _, e := range ents {
				if e.Type()&os.ModeSymlink != 0 {
					return nil, fmt.Errorf("%s is a symlink: %w (%s)", filepath.Join(d, e.Name()), ErrNotOnMount,
						m.Path)
				}
				if e.IsDir() {
					des = append(des, dirEntry{name: e.Name(), dir: true})
					continue
				}
				if !keep(e.Name()) {
					continue
				}
				info, err := e.Info()
				if err != nil {
					return nil, err
				}
				des = append(des, dirEntry{name: e.Name(), size: info.Size()})
			}
			return des, nil
		})
		if err != nil {
			return err
		}
		for _, e := range v.([]dirEntry) {
			p := filepath.Join(d, e.name)
			if e.dir {
				if depth < 4 {
					if err := walk(p, depth+1); err != nil {
						return err
					}
				}
				continue
			}
			rel, _ := filepath.Rel(dir, p)
			out = append(out, MountFile{Path: p, Rel: rel, Size: e.size})
		}
		return nil
	}
	if err := walk(dir, 0); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	return out, nil
}

// ProbeFile runs a header probe of a file on the mount (probe.File, at most max bytes read) under the guard, with
// probeTimeout: in a helper process when the mount has them. A probe that hangs is ErrMountDown, not a probe
// failure: the file is not judged on a mount that does not answer. custom, when set (tests), probes instead, in
// this process.
func (m *Mount) ProbeFile(ctx context.Context, path string, max int64,
	custom func(string, int64) (*probe.Result, error)) (*probe.Result, error) {
	where := path
	if custom != nil {
		where = ""
	}
	v, err := m.guarded(ctx, probeTimeout, where, func(f mountFS) (any, error) {
		switch hf, ok := f.(*helperFS); {
		case custom != nil:
			return custom(path, max)
		case ok:
			return hf.probe(path, max)
		}
		return probe.File(path, max)
	})
	if err != nil {
		return nil, err
	}
	return v.(*probe.Result), nil
}

// ReadHead reads a file on the mount from its start, at most max bytes, in one sequential read, and closes it at
// once: under the guard with probeTimeout, in a helper process when the mount has them. It returns the bytes read.
// A read that hangs is ErrMountDown.
func (m *Mount) ReadHead(ctx context.Context, path string, max int64) (int64, error) {
	v, err := m.guarded(ctx, probeTimeout, path, func(f mountFS) (any, error) {
		if hf, ok := f.(*helperFS); ok {
			return hf.head(path, max)
		}
		file, err := os.Open(path)
		if err != nil {
			return int64(0), err
		}
		defer file.Close()
		return readHead(file, max)
	})
	if err != nil {
		return 0, err
	}
	return v.(int64), nil
}

// rangePiece is one guarded call of Mount.ReadAll: at most this many bytes, read in order.
const rangePiece = 32 << 20

// ReadAll reads a whole file on the mount in order, from offset 0 to its end, at most rate bytes per second (0: no
// cap), and drops the bytes: the now playing pre-warm's caching read (rclone's VFS cache keeps the file). Each
// piece of rangePiece bytes is a guarded call of its own (in a helper when the mount has them), bounded to
// probeTimeout plus the piece's time at rate, so a mount that stops answering fails it with ErrMountDown. progress
// is called after each piece with the bytes read so far and the file's size. It stops at the first error.
func (m *Mount) ReadAll(ctx context.Context, path string, rate int64, progress func(done, size int64)) (int64, error) {
	t := probeTimeout
	if rate > 0 {
		t += time.Duration(float64(rangePiece) / float64(rate) * float64(time.Second))
	}
	var done int64
	for {
		type piece struct{ n, size int64 }
		v, err := m.guarded(ctx, t, path, func(f mountFS) (any, error) {
			if hf, ok := f.(*helperFS); ok {
				n, size, err := hf.readRange(path, done, rangePiece, rate)
				return piece{n, size}, err
			}
			file, err := os.Open(path)
			if err != nil {
				return piece{}, err
			}
			defer file.Close()
			n, size, err := readRange(file, done, rangePiece, rate)
			return piece{n, size}, err
		})
		p, _ := v.(piece)
		done += p.n
		if err != nil {
			return done, err
		}
		if progress != nil {
			progress(done, p.size)
		}
		if p.n < rangePiece || done >= p.size {
			return done, nil
		}
	}
}

// answers stats dir and reads at most one name from it, under the guard: the health check's question (does the
// mount answer, and is it mounted at all).
func (m *Mount) answers(ctx context.Context, dir string) (int, error) {
	v, err := m.withTimeout(ctx, dir, func(f mountFS) (any, error) {
		fi, err := f.Stat(dir)
		if err != nil {
			return 0, err
		}
		if !fi.IsDir() {
			return 0, errors.New("not a directory")
		}
		var ents []os.DirEntry
		if hf, ok := f.(*helperFS); ok {
			ents, err = hf.readDir(dir, 1)
		} else {
			ents, err = f.ReadDir(dir)
		}
		return min(len(ents), 1), err
	})
	if err != nil {
		return 0, err
	}
	return v.(int), nil
}

// inherit takes prev's state for the same mount, a later settings generation's Mount: the guard (outstanding
// calls, circuit breaker, largest listings) and the helper processes, both bound to Path. The configuration (AllDir,
// StatTimeout, LinkPath) stays m's own. A mount at another Path, or a test filesystem, keeps its fresh state.
func (m *Mount) inherit(prev *Mount) {
	if prev == nil || prev == m || m.Path != prev.Path || m.fs != nil || prev.fs != nil {
		return
	}
	g := prev.guard()
	m.once.Do(func() { m.g = g })
	if m.g != g {
		return // m's guard was already in use; keep it rather than split its calls
	}
	if prev.pool != nil {
		m.pool = prev.pool
	}
}

// closeHelpers ends the mount's idle helper processes (tests; theatarr serve's end closes their pipes).
func (m *Mount) closeHelpers() {
	if m.pool != nil {
		m.pool.close()
	}
}
