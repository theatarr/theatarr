package policy

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/rootprobe"
)

// Parallel versions (docs/parallel.md). A version with follows: K runs beside K's arr instance on roots of its own:
// the arr import gives it K's titles and monitoring, never K's files (internal/importarr follow.go), `theatarr library
// adopt` starts it from links of its own to the same mount targets, and it searches, grabs and upgrades on its own
// once mode: active. K itself stays as it is (usually shadow, fed by its arr).

// Followers returns the versions that follow key, sorted.
func (p *Policy) Followers(key string) []string {
	var out []string
	for _, k := range sortedKeys(p.Versions) {
		if v := p.Versions[k]; v != nil && v.Follows == key && key != "" {
			out = append(out, k)
		}
	}
	return out
}

// CollectionTarget reports whether a version may be a movie collection's target (docs/design/arrview.md, Collections):
// a movie version in mode: active that no version follows, the versions the library gate lets write
// (internal/library NewGate). A shadow version only dry-runs, and a followed one's roots are its arr's.
func (p *Policy) CollectionTarget(key string) bool {
	v := p.Versions[key]
	return v != nil && v.Kind == KindMovie && v.Mode == ModeActive && len(p.Followers(key)) == 0
}

// CollectionTargets returns every version CollectionTarget allows, sorted.
func (p *Policy) CollectionTargets() []string {
	var out []string
	for _, k := range sortedKeys(p.Versions) {
		if p.CollectionTarget(k) {
			out = append(out, k)
		}
	}
	return out
}

// Followed returns the version key follows, "" when it is not a parallel version.
func (p *Policy) Followed(key string) string {
	if v := p.Versions[key]; v != nil {
		return v.Follows
	}
	return ""
}

// versionRoots are every directory a version links into: versions[k].library and library.versions[k].roots.
func (p *Policy) versionRoots(k string) []string {
	var out []string
	if v := p.Versions[k]; v != nil && filepath.IsAbs(v.Library) {
		out = append(out, cleanAbs(v.Library))
	}
	if p.Library != nil {
		if lv := p.Library.Versions[k]; lv != nil {
			for _, r := range lv.Roots {
				if filepath.IsAbs(r) {
					out = append(out, cleanAbs(r))
				}
			}
		}
	}
	return out
}

func (p *Policy) validateFollow(e *Errors) {
	for _, k := range sortedKeys(p.Versions) {
		v := p.Versions[k]
		if v == nil || v.Follows == "" {
			continue
		}
		path := fmt.Sprintf("versions[%s].follows", k)
		f := p.Versions[v.Follows]
		switch {
		case v.Follows == k:
			e.add(path, "a library cannot follow itself")
			continue
		case f == nil:
			e.add(path, "unknown library %q", v.Follows)
			continue
		case f.Follows != "":
			e.add(path, "%q follows %q itself; a parallel library follows a library fed by its arr, never another "+
				"parallel library", v.Follows, f.Follows)
		}
		if f.Mode == ModeActive {
			// The followed version's roots are its arr's: Theatarr only reads them (settings/root.go holds the same rule
			// for DB settings).
			e.add(fmt.Sprintf("versions[%s].mode", v.Follows), "%q is followed by %s: its roots are its arr's and stay "+
				"read-only; the followed library must stay Watch only", v.Follows, k)
		}
		if v.Kind != f.Kind {
			e.add(path, "%q is a %s library and this one a %s library", v.Follows, f.Kind, v.Kind)
		}
		if (v.Anime == nil) != (f.Anime == nil) || (v.Anime != nil && *v.Anime != *f.Anime) {
			e.add(path, "anime must be the same as %q's: it takes every title %q has", v.Follows, v.Follows)
		}
		if p.Import != nil {
			for _, inst := range sortedKeys(p.Import.Arr) {
				if a := p.Import.Arr[inst]; a != nil && a.Version == k {
					e.add(path, "import.arr[%s] feeds this library; a parallel library takes its titles from %q "+
						"and its files from nothing but Theatarr", inst, v.Follows)
				}
			}
		}
		n := v.Naming
		if n.FromVersion == "" && n.FromArr == "" && n.Folder == "" && n.File == "" {
			e.add(fmt.Sprintf("versions[%s].naming", k), "a parallel library names its files explicitly: set "+
				"from_version: %s (the followed library's naming) or its own templates", v.Follows)
		}
		if p.Library == nil || p.Library.Versions[k] == nil {
			e.add(fmt.Sprintf("library.versions[%s]", k), "required: a parallel library writes only under roots "+
				"of its own")
			continue
		}
	}
	// Every version's check consults the same roots: they are resolved once for the whole validation.
	rr := newLenientResolver()
	for _, k := range sortedKeys(p.Versions) {
		v := p.Versions[k]
		if v == nil || (v.Follows == "" && (v.Mode != ModeActive || p.Library == nil || p.Library.Versions[k] == nil)) {
			continue
		}
		if _, err := p.CheckRootSeparationWith(k, rr); err != nil {
			e.add(fmt.Sprintf("library.versions[%s].roots", k), "%v", err)
		}
	}
}

// ResolvedRoot is a configured directory and its physical path. Missing suffixes keep their names so validation
// also works before a library has been created; every existing ancestor is resolved.
type ResolvedRoot struct {
	Path, Resolved string
	info           os.FileInfo
	// fs are the directory's places in their filesystems (withMounts): its own, and those of the mounts below it.
	fs []fsPlace
}

// RootStat is resolveRoot's stat of a resolved root (tests replace it with one that blocks, like a hung share).
var RootStat = os.Stat

// RootResolveTimeout bounds the filesystem resolution of one root through the prober (RootResolver); tests shorten
// it.
var RootResolveTimeout = 3 * time.Second

// ErrRootUnreachable is a root whose filesystem did not answer in RootResolveTimeout, or whose earlier check still
// waits on it. The errors that wrap it say which.
var ErrRootUnreachable = errors.New("not answering")

// RootResolver resolves the roots one operation consults (a validation, a gate check): each path once, each in its
// own call through the process's root prober (rootprobe.Default), so a root on a filesystem that stops answering (a
// hung NFS or SMB share) costs one RootResolveTimeout, is charged to its own mount and to no root checked beside
// it, and later checks of that mount are answered at once until the call returns. No slot is held while another is
// taken, so two operations never wait on each other. Strict (NewRootResolver) refuses such a root with
// ErrRootUnreachable; Validate's (lenient) compares it by its path, as written: Validate cannot see where it leads,
// and refusing would hold every save, including the ones that only turn things off. Not safe for concurrent use.
type RootResolver struct {
	ctx     context.Context
	lenient bool
	done    map[string]resolved
}

type resolved struct {
	r     ResolvedRoot
	paths []string // MountPathsWith's
	err   error
}

// NewRootResolver is a strict RootResolver for one operation; ctx ends its waits early.
func NewRootResolver(ctx context.Context) *RootResolver {
	return &RootResolver{ctx: ctx, done: map[string]resolved{}}
}

func newLenientResolver() *RootResolver {
	return &RootResolver{ctx: context.Background(), lenient: true, done: map[string]resolved{}}
}

// Resolve is resolveRoot(path, mounts) through the prober, once per operation.
func (rr *RootResolver) Resolve(path string, mounts []string) (ResolvedRoot, error) {
	key := filepath.Clean(path) + "\x00" + strings.Join(mounts, "\x00")
	if c, ok := rr.done[key]; ok {
		return c.r, c.err
	}
	r, err := proberResolveRoot(rr.ctx, path, mounts)
	if rr.lenient && errors.Is(err, ErrRootUnreachable) {
		clean := filepath.Clean(path)
		r, err = ResolvedRoot{Path: clean, Resolved: clean}, nil
	}
	rr.done[key] = resolved{r: r, err: err}
	return r, err
}

// ResolvePath is ResolvePath(path, mounts) through the prober, once per operation: a directory a write goes to (a
// link's parent) or the debrid mount's parent, which may be on a share that stops answering like a root. Strict, one
// that does not answer is ErrRootUnreachable; lenient, it is taken as written.
func (rr *RootResolver) ResolvePath(path string, mounts []string) (string, error) {
	if !filepath.IsAbs(path) {
		return ResolvePath(path, mounts) // refused without a filesystem call
	}
	clean := filepath.Clean(path)
	key := "\x00path\x00" + clean + "\x00" + strings.Join(mounts, "\x00")
	if c, ok := rr.done[key]; ok {
		return c.r.Resolved, c.err
	}
	var out string
	var rerr error
	err := proberErr(rootprobe.Default.Do(rr.ctx, []string{clean}, RootResolveTimeout, func() error {
		out, rerr = ResolvePath(clean, mounts)
		return nil
	}))
	if err == nil {
		err = rerr
	} else {
		out = "" // may still be written by the call left behind
	}
	if rr.lenient && errors.Is(err, ErrRootUnreachable) {
		out, err = clean, nil
	}
	rr.done[key] = resolved{r: ResolvedRoot{Path: clean, Resolved: out}, err: err}
	return out, err
}

// ResolveLibraryRootWith resolves a library root through rr, never following it into the debrid mount (MountPaths):
// a root that is or leads into it is ErrInMount, found without a filesystem call on the mount.
func (p *Policy) ResolveLibraryRootWith(rr *RootResolver, path string) (ResolvedRoot, error) {
	mounts, err := p.MountPathsWith(rr)
	if err != nil {
		return ResolvedRoot{Path: filepath.Clean(path)}, err
	}
	return rr.Resolve(path, mounts)
}

// proberResolveRoot is resolveRoot through rootprobe.Default, ErrRootUnreachable when the root does not answer.
func proberResolveRoot(ctx context.Context, path string, mounts []string) (ResolvedRoot, error) {
	var r ResolvedRoot
	var rerr error
	err := proberErr(rootprobe.Default.Do(ctx, []string{path}, RootResolveTimeout, func() error {
		r, rerr = resolveRoot(path, mounts)
		return nil
	}))
	if err != nil {
		// r and rerr may still be written by the call left behind: never read them here.
		return ResolvedRoot{Path: filepath.Clean(path)}, err
	}
	return r, rerr
}

// proberErr words a prober refusal or timeout as ErrRootUnreachable.
func proberErr(err error) error {
	var stuck *rootprobe.StuckError
	var timeout *rootprobe.TimeoutError
	switch {
	case errors.As(err, &stuck):
		return fmt.Errorf("%w: an earlier check is still waiting on its filesystem; try again once that directory "+
			"answers", ErrRootUnreachable)
	case errors.As(err, &timeout):
		return fmt.Errorf("%w: its filesystem gave no answer in %s; try again once that directory answers",
			ErrRootUnreachable, timeout.After)
	}
	return err
}

func resolveRoot(path string, mounts []string) (ResolvedRoot, error) {
	r := ResolvedRoot{Path: filepath.Clean(path)}
	abs, err := filepath.Abs(r.Path)
	if err != nil {
		return r, err
	}
	r.Resolved, err = ResolvePath(abs, mounts)
	if err != nil {
		return r, err
	}
	info, err := RootStat(r.Resolved)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return r, nil
	case err != nil:
		return r, err
	case !info.IsDir():
		return r, fmt.Errorf("%q is not a directory", path)
	}
	r.info = info
	return r, nil
}

// Overlaps includes bind mounts of the same directory: SameFile compares the device and inode from Stat.Sys. With
// the mount table (withMounts) it also catches an alias below either directory: a bind mount of one root inside
// another has neither the other's path nor its device and inode, but the same filesystem and path in it.
func (r ResolvedRoot) Overlaps(other ResolvedRoot) bool {
	if Within(r.Resolved, other.Resolved) || Within(other.Resolved, r.Resolved) ||
		(r.info != nil && other.info != nil && os.SameFile(r.info, other.info)) {
		return true
	}
	for _, a := range r.fs {
		for _, b := range other.fs {
			if a.dev == b.dev && (Within(a.path, b.path) || Within(b.path, a.path)) {
				return true
			}
		}
	}
	return false
}

// fsPlace is a directory in a filesystem: the device (major:minor) and the path inside that filesystem.
type fsPlace struct{ dev, path string }

// mountEntry is one line of the mount table: the filesystem's device, the directory of it that is mounted (root)
// and where (point).
type mountEntry struct{ dev, root, point string }

// mountInfoPath is the mount table (tests point it at a file of their own).
var mountInfoPath = "/proc/self/mountinfo"

// readMountInfo reads the mount table; nil when there is none (not Linux), which leaves Overlaps to paths and
// SameFile.
func readMountInfo() []mountEntry {
	b, err := os.ReadFile(mountInfoPath)
	if err != nil {
		return nil
	}
	var out []mountEntry
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		out = append(out, mountEntry{dev: f[2], root: unescapeMount(f[3]), point: unescapeMount(f[4])})
	}
	return out
}

// unescapeMount undoes the mount table's octal escapes (\040 for a space).
func unescapeMount(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// withMounts places the resolved directory in its filesystems: the mount it is in (the deepest mount point above it;
// a later mount on the same point covers an earlier one) gives its own place, and every mount below it adds the
// mounted directory's.
func (r ResolvedRoot) withMounts(mounts []mountEntry) ResolvedRoot {
	if r.Resolved == "" || len(mounts) == 0 {
		return r
	}
	var in *mountEntry
	for i := range mounts {
		m := &mounts[i]
		if !Within(r.Resolved, m.point) {
			if Within(m.point, r.Resolved) {
				r.fs = append(r.fs, fsPlace{m.dev, m.root})
			}
			continue
		}
		if in == nil || len(m.point) >= len(in.point) {
			in = m
		}
	}
	if in != nil {
		rel, err := filepath.Rel(in.point, r.Resolved)
		if err == nil {
			r.fs = append(r.fs, fsPlace{in.dev, filepath.Join(in.root, rel)})
		}
	}
	return r
}

// CheckRootSeparation resolves every version's roots and the trash, refusing aliases for key and trash writes.
// The returned roots are key's, including when an overlap is found, for the go-live report. Each root is resolved
// through the prober (NewRootResolver): one that does not answer is refused by name (ErrRootUnreachable) after one
// RootResolveTimeout, and at once while that call is stuck, instead of holding the caller (the library's write gate
// runs it before every write, with link locks held).
func (p *Policy) CheckRootSeparation(key string) ([]ResolvedRoot, error) {
	return p.CheckRootSeparationWith(key, NewRootResolver(context.Background()))
}

// CheckRootSeparationWith is CheckRootSeparation with the roots resolved through rr, shared with the rest of the
// caller's operation.
func (p *Policy) CheckRootSeparationWith(key string, rr *RootResolver) ([]ResolvedRoot, error) {
	debrid, err := p.MountPathsWith(rr)
	if err != nil {
		return nil, err
	}
	return p.checkRootSeparation(key, debrid, rr.Resolve)
}

// checkRootSeparation is CheckRootSeparation with the debrid mount's paths and the root resolution resolve.
func (p *Policy) checkRootSeparation(key string, debrid []string, resolve func(string, []string) (ResolvedRoot, error)) ([]ResolvedRoot, error) {
	roots := map[string][]ResolvedRoot{}
	mounts := readMountInfo()
	for _, k := range sortedKeys(p.Versions) {
		for _, path := range p.versionRoots(k) {
			r, err := resolve(path, debrid)
			if err != nil {
				return roots[key], fmt.Errorf("root %q of library %s: %w", path, k, err)
			}
			roots[k] = append(roots[k], r.withMounts(mounts))
		}
	}
	var trash *ResolvedRoot
	if p.Library != nil && filepath.IsAbs(p.Library.Trash.Dir) {
		r, err := resolve(p.Library.Trash.Dir, debrid)
		if err != nil {
			return roots[key], fmt.Errorf("trash %q: %w", p.Library.Trash.Dir, err)
		}
		r = r.withMounts(mounts)
		trash = &r
	}
	// The mount below a written directory: its own paths are compared, never stat'ed.
	written := append([]ResolvedRoot(nil), roots[key]...)
	if trash != nil {
		written = append(written, *trash)
	}
	for _, r := range written {
		for _, m := range debrid {
			if Within(m, r.Path) || Within(m, r.Resolved) {
				return roots[key], fmt.Errorf("%q resolves to %q and contains the library mount %q", r.Path, r.Resolved, m)
			}
		}
	}
	for _, k := range sortedKeys(roots) {
		for _, other := range roots[k] {
			if trash != nil && trash.Overlaps(other) {
				return roots[key], fmt.Errorf("trash %q resolves to %q and overlaps %q (resolved %q) of library %s",
					trash.Path, trash.Resolved, other.Path, other.Resolved, k)
			}
			if k == key {
				continue
			}
			for _, mine := range roots[key] {
				if mine.Overlaps(other) {
					return roots[key], fmt.Errorf("%q resolves to %q and overlaps %q (resolved %q) of library %s",
						mine.Path, mine.Resolved, other.Path, other.Resolved, k)
				}
			}
		}
	}
	return roots[key], nil
}

// validateNamingFromVersion checks naming.from_version: only a parallel version, naming like the version it follows.
func (p *Policy) validateNamingFromVersion(e *Errors, path, k string, v *VersionPolicy) {
	n := v.Naming
	if n.FromArr != "" || n.Folder != "" || n.File != "" || n.Season != "" || n.Specials != "" || n.Daily != "" ||
		n.Anime != "" || n.Colon != "" || n.MultiEpisode != "" {
		e.add(path, "from_version takes the whole naming from that library; set nothing else")
	}
	switch {
	case v.Follows == "":
		e.add(path+".from_version", "uses the naming of %q; give this library its own copy first (Settings › Libraries › Naming › %s › Copy %s's naming), then stop following",
			n.FromVersion, k, n.FromVersion)
	case n.FromVersion != v.Follows:
		e.add(path+".from_version", "must be the followed library %q, got %q", v.Follows, n.FromVersion)
	}
}

// dirOverlap names the library directory dir overlaps (a version's root, the trash or the mount), or "": compared
// as written and resolved (symlinks, bind mounts).
func (p *Policy) dirOverlap(dir string) string {
	type named struct{ name, path string }
	var list []named
	for _, k := range sortedKeys(p.Versions) {
		for _, r := range p.versionRoots(k) {
			list = append(list, named{fmt.Sprintf("root %q of library %s", r, k), r})
		}
	}
	if p.Library != nil {
		for _, d := range []named{{"library.trash.dir", p.Library.Trash.Dir}, {"library.mount.path", p.Library.Mount.Path},
			{"library.mount.link_path", p.Library.Mount.LinkPath}} {
			if filepath.IsAbs(d.path) {
				list = append(list, named{fmt.Sprintf("%s %q", d.name, d.path), cleanAbs(d.path)})
			}
		}
	}
	clean := cleanAbs(dir)
	mounts := readMountInfo()
	rr := newLenientResolver()
	debrid, _ := p.MountPathsWith(rr) // lenient: never an error
	mine, err := rr.Resolve(clean, debrid)
	if errors.Is(err, ErrInMount) {
		return "library.mount.path " + strconv.Quote(p.Library.Mount.Path)
	}
	resolved := err == nil
	mine = mine.withMounts(mounts)
	for _, o := range list {
		if Within(clean, o.path) || Within(o.path, clean) {
			return o.name
		}
		if !resolved {
			continue
		}
		if inMount(o.path, debrid) != "" {
			// The mount itself is compared by its paths only: a stat of a hung FUSE mount blocks.
			for _, m := range debrid {
				if Within(m, mine.Resolved) || Within(mine.Resolved, m) {
					return o.name
				}
			}
			continue
		}
		if r, err := rr.Resolve(o.path, debrid); err == nil && mine.Overlaps(r.withMounts(mounts)) {
			return o.name
		}
	}
	return ""
}

// FollowedBy returns the active library following version, or an empty string.
func (p *Policy) FollowedBy(version string) string {
	for k, v := range p.Versions {
		if v.Mode == ModeActive && v.Follows == version {
			return k
		}
	}
	return ""
}
