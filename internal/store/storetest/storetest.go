// Package storetest gives tests a migrated database without running every migration per test.
//
// Running the full migration set costs about 0.2 s per database, and about 6 s under the race detector (modernc
// SQLite is Go code, so the detector instruments it). [Seed] migrates one database, keeps its bytes, and writes a
// copy to each path it is given. Callers still open the file with store.Open and may still call Migrate, which then
// finds nothing pending.
//
// The migrated template is also cached in the system temp directory under a name derived from the migration files'
// contents, so the test binaries of other packages (and later runs) copy it instead of migrating again. When the
// migration sources cannot be read, each binary migrates its own template.
package storetest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"testing"

	"github.com/theatarr/theatarr/internal/store"
)

var (
	once     sync.Once
	template []byte
	tmplErr  error
)

// load returns the template from the shared cache, building and caching it when it is missing.
func load() ([]byte, error) {
	key, ok := migrationsKey()
	if !ok {
		return build()
	}
	cached := filepath.Join(os.TempDir(), "theatarr-storetest-"+key+".db")
	if b, err := os.ReadFile(cached); err == nil && len(b) > 0 {
		return b, nil
	}
	b, err := build()
	if err != nil {
		return nil, err
	}
	// Write then rename, so a concurrent reader sees the old name missing or the whole file.
	if f, err := os.CreateTemp(os.TempDir(), "theatarr-storetest-*.tmp"); err == nil {
		_, werr := f.Write(b)
		cerr := f.Close()
		if werr != nil || cerr != nil || os.Rename(f.Name(), cached) != nil {
			os.Remove(f.Name())
		}
	}
	return b, nil
}

// migrationsKey hashes the migration files next to this source file (../migrations); false when they cannot be read.
func migrationsKey() (string, bool) {
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		return "", false
	}
	dir := filepath.Join(filepath.Dir(self), "..", "migrations")
	names, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil || len(names) == 0 {
		return "", false
	}
	sort.Strings(names)
	h := sha256.New()
	h.Write([]byte(runtime.Version()))
	for _, name := range names {
		b, err := os.ReadFile(name)
		if err != nil {
			return "", false
		}
		h.Write([]byte(filepath.Base(name)))
		h.Write([]byte{0})
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16], true
}

func build() ([]byte, error) {
	dir, err := os.MkdirTemp("", "storetest-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "template.db")
	ctx := context.Background()
	st, err := store.Open(ctx, path, store.Options{})
	if err != nil {
		return nil, err
	}
	if _, err := st.Migrate(ctx); err != nil {
		st.Close()
		return nil, err
	}
	// Closing the last connection checkpoints the WAL into the main file and removes it.
	if err := st.Close(); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path + "-wal"); err == nil {
		return nil, errors.New("storetest: the template's WAL was not checkpointed on close")
	}
	return os.ReadFile(path)
}

// Seed writes a fully migrated database to path, which must not exist yet, and returns path.
func Seed(tb testing.TB, path string) string {
	tb.Helper()
	once.Do(func() { template, tmplErr = load() })
	if tmplErr != nil {
		tb.Fatalf("storetest: build template: %v", tmplErr)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		tb.Fatalf("storetest: %v", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		tb.Fatalf("storetest: %v", err)
	}
	if _, err := f.Write(template); err != nil {
		f.Close()
		tb.Fatalf("storetest: %v", err)
	}
	if err := f.Close(); err != nil {
		tb.Fatalf("storetest: %v", err)
	}
	return path
}

// Path returns a fresh migrated database file in a test temp directory.
func Path(tb testing.TB) string {
	tb.Helper()
	return Seed(tb, filepath.Join(tb.TempDir(), "theatarr.db"))
}

// Open returns a migrated store at a fresh temp path, closed when the test ends.
func Open(tb testing.TB) *store.Store {
	tb.Helper()
	st, err := store.Open(context.Background(), Path(tb), store.Options{})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { st.Close() })
	return st
}
