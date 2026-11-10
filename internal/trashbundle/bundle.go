// Package trashbundle is the TRaSH-Guides snapshot the binary carries, so the TRaSH quality profiles can be listed
// and used as presets without a checkout (THEATARR_TRASH). It holds the files format.LoadTrash reads (metadata.json
// and docs/json/{radarr,sonarr}/{cf,cf-groups,quality-profiles}) at one pinned commit, as a git archive.
// TRaSH-Guides is MIT licensed; its licence text is LICENSE-TRaSH-Guides beside this file and inside the archive.
//
// scripts/update-trash-bundle.sh regenerates guides.tar.gz, COMMIT and LICENSE-TRaSH-Guides.
package trashbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed guides.tar.gz
var archive []byte

//go:embed COMMIT
var commitLine string

//go:embed LICENSE-TRaSH-Guides
var license string

var load = sync.OnceValues(func() (fs.FS, error) { return unpack(archive) })

// FS is the snapshot as a file system rooted like a TRaSH-Guides checkout. It is unpacked into memory on the first
// call and shared afterwards; the files are read-only.
func FS() (fs.FS, error) { return load() }

// Commit is the TRaSH-Guides commit the snapshot was taken from (40 hex characters).
func Commit() string {
	commit, _, _ := strings.Cut(strings.TrimSpace(commitLine), " ")
	return commit
}

// Date is that commit's date, YYYY-MM-DD.
func Date() string {
	_, date, _ := strings.Cut(strings.TrimSpace(commitLine), " ")
	return date
}

// License is the TRaSH-Guides licence text (MIT), as published at Commit.
func License() string { return license }

func unpack(gz []byte) (fs.FS, error) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("trashbundle: %w", err)
	}
	m := memFS{files: map[string][]byte{}, dirs: map[string][]string{".": nil}}
	children := map[string]map[string]bool{}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("trashbundle: %w", err)
		}
		// Directories come from the file paths; the archive's global header (the commit) is not a file.
		if h.Typeflag != tar.TypeReg {
			continue
		}
		name := path.Clean(h.Name)
		if !fs.ValidPath(name) || name == "." {
			return nil, fmt.Errorf("trashbundle: bad path %q", h.Name)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("trashbundle: %s: %w", name, err)
		}
		m.files[name] = b
		for child := name; child != "."; child = path.Dir(child) {
			dir := path.Dir(child)
			if children[dir] == nil {
				children[dir] = map[string]bool{}
			}
			children[dir][path.Base(child)] = true
		}
	}
	for dir, set := range children {
		names := make([]string, 0, len(set))
		for name := range set {
			names = append(names, name)
		}
		sort.Strings(names)
		m.dirs[dir] = names
	}
	return m, nil
}

// memFS is the unpacked archive: file contents by slash path, and each directory's sorted child names.
type memFS struct {
	files map[string][]byte
	dirs  map[string][]string
}

func (m memFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if b, ok := m.files[name]; ok {
		return &memFile{info: memInfo{name: path.Base(name), size: int64(len(b))}, r: bytes.NewReader(b)}, nil
	}
	if children, ok := m.dirs[name]; ok {
		d := &memDir{info: memInfo{name: path.Base(name), dir: true}}
		for _, c := range children {
			full := path.Join(name, c)
			if b, ok := m.files[full]; ok {
				d.entries = append(d.entries, memInfo{name: c, size: int64(len(b))})
			} else {
				d.entries = append(d.entries, memInfo{name: c, dir: true})
			}
		}
		return d, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// ReadFile returns a copy, so no caller can change the shared snapshot.
func (m memFS) ReadFile(name string) ([]byte, error) {
	b, ok := m.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return bytes.Clone(b), nil
}

type memInfo struct {
	name string
	size int64
	dir  bool
}

func (i memInfo) Name() string { return i.name }
func (i memInfo) Size() int64  { return i.size }
func (i memInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}
func (i memInfo) ModTime() time.Time         { return time.Time{} }
func (i memInfo) IsDir() bool                { return i.dir }
func (i memInfo) Sys() any                   { return nil }
func (i memInfo) Type() fs.FileMode          { return i.Mode().Type() }
func (i memInfo) Info() (fs.FileInfo, error) { return i, nil }

type memFile struct {
	info memInfo
	r    *bytes.Reader
}

func (f *memFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *memFile) Read(p []byte) (int, error) { return f.r.Read(p) }
func (f *memFile) Close() error               { return nil }

type memDir struct {
	info    memInfo
	entries []memInfo
	at      int
}

func (d *memDir) Stat() (fs.FileInfo, error) { return d.info, nil }
func (d *memDir) Close() error               { return nil }
func (d *memDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.info.name, Err: errors.New("is a directory")}
}

func (d *memDir) ReadDir(n int) ([]fs.DirEntry, error) {
	rest := d.entries[d.at:]
	if n > 0 && len(rest) > n {
		rest = rest[:n]
	}
	if n > 0 && len(rest) == 0 {
		return nil, io.EOF
	}
	d.at += len(rest)
	out := make([]fs.DirEntry, len(rest))
	for i, e := range rest {
		out[i] = e
	}
	return out, nil
}
