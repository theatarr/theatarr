package library

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// writer is the only code in this package that changes the filesystem (TestWritesOnlyInFS enforces that). Every
// call checks the gate first, with the exact path it is about to touch. It creates directories and symlinks, and
// removes symlinks it has copied to the trash; it never removes a regular file or a directory, and never writes
// into the mount.
type writer struct {
	gate *Gate
	now  func() time.Time
	// after, when set (tests), runs after each link made, removed or copied to the trash: a crash point.
	after func(op, path string)
}

func (w *writer) done(op, path string) {
	if w.after != nil {
		w.after(op, path)
	}
}

func (w *writer) mkdirAll(version, dir string) error {
	if err := w.gate.CheckPath(version, filepath.Join(dir, ".probe")); err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o755)
}

// symlink creates or atomically replaces path as a symlink to target: a temporary link in the same directory,
// then a rename over path. An existing path must itself be a symlink.
func (w *writer) symlink(version, target, path string) error {
	if err := w.gate.CheckPath(version, path); err != nil {
		return err
	}
	old, err := os.Lstat(path)
	if err == nil && old.Mode()&os.ModeSymlink == 0 {
		return &Refused{Reason: "not_symlink", Detail: fmt.Sprintf("%q exists and is not a symlink", path)}
	}
	if err := w.mkdirAll(version, filepath.Dir(path)); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), ".theatarr-"+randHex()+".tmp")
	if err := w.gate.CheckPath(version, tmp); err != nil {
		return err
	}
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	// A replaced link keeps its owner (lchown). Best effort: without the right to
	// chown, the new link is ours.
	if old != nil {
		if uid, gid, ok := owner(old); ok {
			_ = os.Lchown(tmp, uid, gid)
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	w.done("symlink", path)
	return nil
}

// trashPath is where a link of version at path (under root) is kept: <trash>/<version>/<date>/<path under root>,
// with a suffix when that name is taken.
func (w *writer) trashPath(version, root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || escapes(rel) {
		rel = filepath.Base(path)
	}
	p := filepath.Join(w.gate.trash, version, w.now().UTC().Format("2006-01-02"), rel)
	if _, err := os.Lstat(p); err == nil {
		p += "." + randHex()
	}
	return p
}

// copyToTrash recreates the symlink at path under the trash directory and returns the trash path and the target.
// The original stays; the caller replaces or removes it.
func (w *writer) copyToTrash(version, root, path string) (string, string, error) {
	if err := w.gate.CheckPath(version, path); err != nil {
		return "", "", err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return "", "", err
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return "", "", &Refused{Reason: "not_symlink", Detail: fmt.Sprintf("%q is not a symlink; only links are moved aside", path)}
	}
	target, err := os.Readlink(path)
	if err != nil {
		return "", "", err
	}
	tp := w.trashPath(version, root, path)
	if err := w.gate.CheckTrash(version, tp); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(filepath.Dir(tp), 0o755); err != nil {
		return "", "", err
	}
	if err := os.Symlink(target, tp); err != nil {
		return "", "", err
	}
	w.done("trash", path)
	return tp, target, nil
}

// removeLink removes the symlink at path (never a file or directory). Only the link goes: its target is not
// touched.
func (w *writer) removeLink(version, path string) error {
	if err := w.gate.CheckPath(version, path); err != nil {
		return err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return &Refused{Reason: "not_symlink", Detail: fmt.Sprintf("%q is not a symlink", path)}
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	w.done("remove", path)
	return nil
}

// removeEmptyDir removes an empty directory made under a version's root.
func (w *writer) removeEmptyDir(version, dir string) error {
	if err := w.gate.CheckPath(version, filepath.Join(dir, ".probe")); err != nil {
		return err
	}
	return os.Remove(dir)
}

// purgeTrash removes a trashed symlink after keep_days.
func (w *writer) purgeTrash(version, path string) error {
	if err := w.gate.CheckTrash(version, path); err != nil {
		return err
	}
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return &Refused{Reason: "not_symlink", Detail: fmt.Sprintf("%q is not a symlink", path)}
	}
	return os.Remove(path)
}

func randHex() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
