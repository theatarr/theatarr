package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// RotatingWriter appends complete JSON records to base; maxFiles includes the current file.
// A single record larger than maxBytes is kept intact in its own file.
type RotatingWriter struct {
	mu        sync.Mutex
	dir, base string
	maxBytes  int64
	maxFiles  int
	f         *os.File
	size      int64
}

func NewRotatingWriter(dir, base string, maxBytes int64, maxFiles int) (*RotatingWriter, error) {
	if dir == "" || base == "" || base == "." || filepath.Base(base) != base || maxBytes <= 0 || maxFiles < 1 {
		return nil, fmt.Errorf("invalid log rotation settings")
	}
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if suffix, ok := strings.CutPrefix(e.Name(), base+"."); ok {
			if n, err := strconv.Atoi(suffix); err == nil && n >= maxFiles {
				if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
					return nil, err
				}
			}
		}
	}
	w := &RotatingWriter{dir: dir, base: base, maxBytes: maxBytes, maxFiles: maxFiles}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *RotatingWriter) open() error {
	f, err := os.OpenFile(filepath.Join(w.dir, w.base), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f, w.size = f, st.Size()
	return nil
}

func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, os.ErrClosed
	}
	if w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *RotatingWriter) rotate() (err error) {
	if err = w.f.Close(); err != nil {
		return err
	}
	w.f = nil
	// Reopen even after a rename failure so later writes can recover.
	defer func() {
		openErr := w.open()
		if err == nil {
			err = openErr
		}
	}()
	path := func(n int) string {
		if n == 0 {
			return filepath.Join(w.dir, w.base)
		}
		return filepath.Join(w.dir, fmt.Sprintf("%s.%d", w.base, n))
	}
	if err := os.Remove(path(w.maxFiles - 1)); err != nil && !os.IsNotExist(err) {
		return err
	}
	for i := w.maxFiles - 2; i >= 0; i-- {
		if err := os.Rename(path(i), path(i+1)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
