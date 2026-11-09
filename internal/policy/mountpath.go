package policy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrInMount is a library path that is, or resolves to, a place inside the debrid mount.
var ErrInMount = errors.New("inside the library mount")

// maxSymlinks bounds ResolvePath like the kernel's ELOOP limit.
const maxSymlinks = 40

// ResolveLstat is ResolvePath's lstat (tests replace it with one that blocks, like a hung share).
var ResolveLstat = os.Lstat

// MountPaths is the debrid mount and its link path with their physical aliases: each as configured, with its
// parent's symlinks resolved, and every mount point in the mount table that shows the same filesystem (a bind mount
// of it or of a directory in it). Nothing inside the mount is touched: a stat of a hung FUSE mount blocks. The
// parent is resolved through the root prober (MountPathsWith), leniently: a parent that does not answer in
// RootResolveTimeout is left out of the aliases, so a hung share above the mount never holds a caller (a settings
// save's validation). A write check uses MountPathsWith with a strict resolver, which refuses instead.
func (p *Policy) MountPaths() []string {
	out, _ := p.MountPathsWith(newLenientResolver())
	return out
}

// MountPathsWith is MountPaths with the mount's parents resolved through rr, once per operation: a strict rr returns
// ErrRootUnreachable (naming the parent) when one does not answer, as nothing can then tell where it leads.
func (p *Policy) MountPathsWith(rr *RootResolver) ([]string, error) {
	s := p.LibrarySettings()
	var conf []string
	for _, m := range []string{s.MountPath, s.LinkPath} {
		if m != "" && filepath.IsAbs(m) {
			conf = append(conf, filepath.Clean(m))
		}
	}
	key := "\x00mounts\x00" + strings.Join(conf, "\x00")
	if c, ok := rr.done[key]; ok {
		return c.paths, c.err
	}
	out, err := mountAliases(conf, readMountInfo(), rr.ResolvePath)
	rr.done[key] = resolved{paths: out, err: err}
	return out, err
}

// mountAliases adds the physical aliases of the configured mount paths conf: each with its parent resolved
// (resolve), and the mount table's other places of the same filesystem. A parent that does not answer
// (ErrRootUnreachable) is an error; one that cannot be resolved otherwise (it does not exist yet) adds nothing.
func mountAliases(conf []string, table []mountEntry, resolve func(string, []string) (string, error)) ([]string, error) {
	out := append([]string(nil), conf...)
	add := func(m string) {
		for _, o := range out {
			if o == m {
				return
			}
		}
		out = append(out, m)
	}
	for _, m := range conf {
		parent, err := resolve(filepath.Dir(m), conf)
		if errors.Is(err, ErrRootUnreachable) {
			return conf, fmt.Errorf("the parent %q of the debrid mount %q: %w", filepath.Dir(m), m, err)
		}
		if err == nil {
			add(filepath.Join(parent, filepath.Base(m)))
		}
	}
	for _, m := range append([]string(nil), out...) {
		for _, e := range table {
			if e.point != m {
				continue
			}
			for _, o := range table {
				if o.dev == e.dev && Within(o.root, e.root) {
					add(o.point)
				}
			}
		}
	}
	return out, nil
}

// inMount reports the mount path p lies in, "" for none.
func inMount(p string, mounts []string) string {
	for _, m := range mounts {
		if Within(p, m) {
			return m
		}
	}
	return ""
}

// ResolvePath resolves path's symlinks one component at a time, as the kernel does, and appends the part that does
// not exist yet unchanged. It never makes a filesystem call on a place inside one of mounts: a path or link target
// that leads there is ErrInMount first, so a root turned into a symlink into the debrid mount is caught by where it
// leads, and the resolution cannot block on a hung FUSE mount.
func ResolvePath(path string, mounts []string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%q is not absolute", path)
	}
	done, rest := "/", strings.Split(filepath.Clean(path), "/")
	links := 0
	for len(rest) > 0 {
		c := rest[0]
		rest = rest[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			done = filepath.Dir(done)
			continue
		}
		next := filepath.Join(done, c)
		if m := inMount(next, mounts); m != "" {
			return "", fmt.Errorf("%q leads to %q: %w %q", path, next, ErrInMount, m)
		}
		fi, err := ResolveLstat(next)
		if errors.Is(err, fs.ErrNotExist) {
			out := filepath.Join(append([]string{next}, rest...)...)
			if m := inMount(out, mounts); m != "" {
				return "", fmt.Errorf("%q leads to %q: %w %q", path, out, ErrInMount, m)
			}
			return out, nil
		}
		if err != nil {
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			done = next
			continue
		}
		if links++; links > maxSymlinks {
			return "", fmt.Errorf("%q: too many levels of symbolic links", path)
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(target) {
			done = "/"
		}
		rest = append(strings.Split(target, "/"), rest...)
	}
	return done, nil
}
