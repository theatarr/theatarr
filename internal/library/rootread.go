package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/rootprobe"
)

// The read phase of a scan of a library root (the relink sweep, Verify, the dangling summary, ReconcileLinks) goes
// through the root prober like the gate's root checks: Gate.Reachable only shows the root answered a
// moment ago, and a share can answer that cached stat and then hang on a directory listing, a readlink or an lstat.
// Each read is a short call through rootprobe.Default, charged to the filesystem of the directory it reads and
// bounded by policy.RootResolveTimeout: a call that does not answer skips the root ("unreachable", ErrRootDown), and
// while it stays stuck later reads of that filesystem are refused at once, so a hung share holds one blocked thread,
// not one per scan. The calls are kept short (a chunk of a listing, or a few readlinks or lstats of one directory)
// so that a scan never holds a filesystem's slot long enough to make a gate check beside it wait out its own
// timeout. Each call's bound starts once it holds the filesystem's slot (rootprobe DoOnce), and a
// directory is listed a chunk per call, so a large directory on a slow share that keeps answering is read whole
// instead of being taken for a hung one.

// rootReadBatch is the most readlinks or lstats one prober call makes; rootReadChunk the most entries one listing
// call reads.
const rootReadBatch = 32

var rootReadChunk = 512

// The scan's filesystem reads; tests replace them with ones that block, like a hung share.
var (
	rootOpen     = os.Open
	rootReadDir  = func(f *os.File, n int) ([]os.DirEntry, error) { return f.ReadDir(n) }
	rootReadlink = os.Readlink
	rootLstat    = os.Lstat
)

// rootRead runs f, reads of dir's filesystem, through the root prober, bounded. A filesystem that does not answer
// (or whose earlier read is still stuck) is a Refused "unreachable" (ErrRootDown). f's results may still be written
// by a call left behind: the caller reads them only when rootRead returns nil.
func rootRead(ctx context.Context, dir string, f func() error) error {
	err := rootprobe.Default.DoOnce(ctx, []string{dir}, policy.RootResolveTimeout, f)
	var stuck *rootprobe.StuckError
	var timeout *rootprobe.TimeoutError
	switch {
	case errors.As(err, &stuck):
		return &Refused{Reason: "unreachable", Detail: fmt.Sprintf("%q: an earlier read is still waiting on its "+
			"filesystem; try again once that directory answers", dir)}
	case errors.As(err, &timeout):
		return &Refused{Reason: "unreachable", Detail: fmt.Sprintf("%q: its filesystem gave no answer in %s; try "+
			"again once that directory answers", dir, timeout.After)}
	}
	return err
}

// rootEntry is one entry a bounded walk found: its path, its type, and a symlink's target (ok false when it could
// not be read).
type rootEntry struct {
	path   string
	typ    fs.FileMode
	target string
	ok     bool
}

// walkRoot walks root as filepath.WalkDir does (lexical order, symlinks not followed, an unreadable directory below
// the root skipped, an unreadable root an error), calling visit for each entry below the root, with every read
// bounded (rootRead). A filesystem that does not answer ends the walk with ErrRootDown; the caller drops what visit
// saw.
func walkRoot(ctx context.Context, root string, visit func(rootEntry)) error {
	return walkRootMode(ctx, root, false, visit)
}

// walkRootMode is walkRoot; strict, a directory below the root that cannot be read ends the walk with its error too.
func walkRootMode(ctx context.Context, root string, strict bool, visit func(rootEntry)) error {
	var fi os.FileInfo
	var lerr error
	if err := rootRead(ctx, root, func() error {
		fi, lerr = rootLstat(root)
		return nil
	}); err != nil {
		return err
	}
	if lerr != nil {
		return lerr
	}
	if !fi.IsDir() {
		// WalkDir visits a root that is not a directory (a symlink is not followed) as its only entry.
		e := rootEntry{path: root, typ: fi.Mode().Type()}
		if e.typ&fs.ModeSymlink != 0 {
			if err := readTargets(ctx, root, []*rootEntry{&e}); err != nil {
				return err
			}
		}
		visit(e)
		return nil
	}
	return walkDir(ctx, root, true, strict, visit)
}

// WalkLinks walks the local directory root with every read bounded and charged to its filesystem, as the library's
// own scans do (rootRead), for the torrent manager's scan roots: visit gets each symlink below root
// with its target as written. root must be a directory. Any read that fails (a directory or a symlink that cannot
// be read) ends the walk with its error, and a filesystem that does not answer with ErrRootDown: the caller's view
// is incomplete then and must not be taken as the whole.
func WalkLinks(ctx context.Context, root string, visit func(path, target string)) error {
	var fi os.FileInfo
	var lerr error
	if err := rootRead(ctx, root, func() error {
		fi, lerr = rootLstat(root)
		return nil
	}); err != nil {
		return err
	}
	if lerr != nil {
		return lerr
	}
	if !fi.IsDir() {
		return errors.New("not a directory")
	}
	var bad error
	err := walkDir(ctx, root, true, true, func(e rootEntry) {
		if e.typ&fs.ModeSymlink == 0 || bad != nil {
			return
		}
		if !e.ok {
			bad = fmt.Errorf("%s: its target could not be read", e.path)
			return
		}
		visit(e.path, e.target)
	})
	if err != nil {
		return err
	}
	return bad
}

func walkDir(ctx context.Context, dir string, isRoot, strict bool, visit func(rootEntry)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ents, rerr, err := listDir(ctx, dir)
	if err != nil {
		return err
	}
	if rerr != nil && (isRoot || strict) {
		return rerr
	}
	// A listing that failed part way still walks what it read, as WalkDir does.
	out := make([]rootEntry, len(ents))
	var links []*rootEntry
	for i, d := range ents {
		out[i] = rootEntry{path: filepath.Join(dir, d.Name()), typ: d.Type()}
		if out[i].typ&fs.ModeSymlink != 0 {
			links = append(links, &out[i])
		}
	}
	if err := readTargets(ctx, dir, links); err != nil {
		return err
	}
	for _, e := range out {
		visit(e)
		if e.typ.IsDir() {
			if err := walkDir(ctx, e.path, false, strict, visit); err != nil {
				return err
			}
		}
	}
	return nil
}

// listDir lists dir sorted by name, as os.ReadDir does, a chunk of entries per bounded call (rootRead): rerr is the
// listing's own error (what was read before it is kept), err a filesystem that does not answer (ErrRootDown).
func listDir(ctx context.Context, dir string) (ents []os.DirEntry, rerr, err error) {
	var f *os.File
	if err := rootRead(ctx, dir, func() error {
		f, rerr = rootOpen(dir)
		return nil
	}); err != nil {
		return nil, nil, err
	}
	if rerr != nil {
		return nil, rerr, nil
	}
	for {
		var chunk []os.DirEntry
		var cerr error
		if err := rootRead(ctx, dir, func() error {
			chunk, cerr = rootReadDir(f, rootReadChunk)
			return nil
		}); err != nil {
			// The call left behind may still use f: it is not closed here (its finalizer closes it once dropped).
			return nil, nil, err
		}
		ents = append(ents, chunk...)
		if cerr != nil {
			if !errors.Is(cerr, io.EOF) {
				rerr = cerr
			}
			break
		}
		if len(chunk) == 0 {
			break
		}
	}
	if err := rootRead(ctx, dir, func() error { return f.Close() }); err != nil && Unavailable(err) {
		return nil, nil, err
	}
	slices.SortFunc(ents, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return ents, rerr, nil
}

// readTargets reads the targets of links, all in dir, a batch per prober call.
func readTargets(ctx context.Context, dir string, links []*rootEntry) error {
	for len(links) > 0 {
		batch := links[:min(rootReadBatch, len(links))]
		links = links[len(batch):]
		got := make([]rootEntry, len(batch))
		if err := rootRead(ctx, dir, func() error {
			for i, e := range batch {
				t, err := rootReadlink(e.path)
				got[i] = rootEntry{target: t, ok: err == nil}
			}
			return nil
		}); err != nil {
			return err
		}
		for i, e := range batch {
			e.target, e.ok = got[i].target, got[i].ok
		}
	}
	return nil
}

// missingPaths reports, for each of paths, whether lstat says it does not exist, a batch of paths of one directory
// per prober call. A filesystem that does not answer is ErrRootDown.
func missingPaths(ctx context.Context, paths []string) ([]bool, error) {
	out := make([]bool, len(paths))
	for i := 0; i < len(paths); {
		dir := filepath.Dir(paths[i])
		j := i + 1
		for j < len(paths) && j-i < rootReadBatch && filepath.Dir(paths[j]) == dir {
			j++
		}
		batch := paths[i:j]
		got := make([]bool, len(batch))
		if err := rootRead(ctx, dir, func() error {
			for k, p := range batch {
				_, err := rootLstat(p)
				got[k] = errors.Is(err, fs.ErrNotExist)
			}
			return nil
		}); err != nil {
			return nil, err
		}
		copy(out[i:j], got)
		i = j
	}
	return out, nil
}

// rootUnavailable says why a library root cannot be trusted to tell a missing link: it does not exist, is not a
// readable directory, or is empty. "" when it can. A root that does not answer is ErrRootDown.
func rootUnavailable(ctx context.Context, root string) (string, error) {
	var why string
	err := rootRead(ctx, root, func() error {
		why = rootListable(root)
		return nil
	})
	if err != nil {
		return "", err
	}
	return why, nil
}

func rootListable(root string) string {
	f, err := os.Open(root)
	if err != nil {
		return "unavailable: " + err.Error()
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "unavailable: " + err.Error()
	}
	if !fi.IsDir() {
		return "unavailable: not a directory"
	}
	if _, err := f.Readdirnames(1); errors.Is(err, io.EOF) {
		return "empty: nothing is marked missing as a failsafe"
	} else if err != nil {
		return "unavailable: " + err.Error()
	}
	return ""
}

// linkRead is what readLinkBounded found at a path: lstat's answer (fi, lerr) and, for a symlink, readlink's
// (target, rerr).
type linkRead struct {
	fi     os.FileInfo
	lerr   error
	target string
	rerr   error
}

// readLinkBounded lstats p and reads its target when it is a symlink, in one bounded call charged to p's
// directory's filesystem (rootRead): adopt and refresh read the followed arr's links and their own paths this way,
// so a share that hangs after the gate's check stops them (ErrRootDown, retried) instead of
// holding them in the kernel.
func readLinkBounded(ctx context.Context, p string) (linkRead, error) {
	var r linkRead
	err := rootRead(ctx, filepath.Dir(p), func() error {
		var got linkRead
		got.fi, got.lerr = rootLstat(p)
		if got.lerr == nil && got.fi.Mode()&os.ModeSymlink != 0 {
			got.target, got.rerr = rootReadlink(p)
		}
		r = got
		return nil
	})
	if err != nil {
		return linkRead{}, err
	}
	return r, nil
}
