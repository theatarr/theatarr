package store

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Migrating from empty costs about 6 s per database under the race detector, so tests start from a copy of a
// database migrated once to the version they need (storetest does this for other packages; it imports store, so
// store's own tests keep these few lines instead). Version 0 is every migration, through Migrate.
var (
	templatesMu sync.Mutex
	templates   = map[int64][]byte{}
)

// migratedPath writes a database migrated to version (0: all) to a new file in a test temp directory.
func migratedPath(t *testing.T, version int64) string {
	t.Helper()
	b := template(t, version)
	path := filepath.Join(t.TempDir(), "theatarr.db")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func template(t *testing.T, version int64) []byte {
	t.Helper()
	templatesMu.Lock()
	defer templatesMu.Unlock()
	if b, ok := templates[version]; ok {
		return b
	}
	// Start from the newest cached version below the one wanted: goose applies the rest in order, as from empty.
	var start int64 = -1
	if version != 0 {
		for v := range templates {
			if v != 0 && v < version && v > start {
				start = v
			}
		}
	}
	path := filepath.Join(t.TempDir(), "template.db")
	if start >= 0 {
		if err := os.WriteFile(path, templates[start], 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	s, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if version == 0 {
		_, err = s.Migrate(ctx)
	} else {
		prov, perr := s.provider(false)
		if perr != nil {
			s.Close()
			t.Fatal(perr)
		}
		_, err = prov.UpTo(ctx, version)
	}
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + "-wal"); err == nil {
		t.Fatal("template WAL not checkpointed on close")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	templates[version] = b
	return b
}

// pendingPath is a database one step short of version (0: the newest), so a test can open it and still apply the
// last migration itself; version < 0 is a new empty file.
func pendingPath(t *testing.T, version int64) string {
	t.Helper()
	if version < 0 {
		return filepath.Join(t.TempDir(), "theatarr.db")
	}
	if version == 0 {
		latest, err := latestSchemaVersion()
		if err != nil {
			t.Fatal(err)
		}
		version = latest
	}
	return migratedPath(t, version-1)
}

// openAt opens a copy of a database migrated to version: a migration's round-trip test starts at the newest version
// it applies instead of going down from the newest of all (TestDownMigrationsFromNewest runs those downs once).
func openAt(t *testing.T, version int64) *Store {
	t.Helper()
	s, err := Open(context.Background(), migratedPath(t, version), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// Every down migration from the newest down to 00050 runs, and the schema migrates up again with its foreign keys intact.
// 50 is the lowest version a round-trip test goes down to from the newest; the round-trip tests themselves now
// start at their own migration (openAt).
func TestDownMigrationsFromNewest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTest(t, Options{})
	p, err := s.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 50); err != nil {
		t.Fatal(err)
	}
	if cur, _, err := s.SchemaVersion(ctx); err != nil || cur > 50 {
		t.Fatalf("after down: version %d, %v", cur, err)
	}
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cur, target, err := s.SchemaVersion(ctx)
	if err != nil || cur != target {
		t.Fatalf("after up: version %d of %d, %v", cur, target, err)
	}
	var fkProblems int
	if err := s.Reader().QueryRowContext(ctx, "SELECT count(*) FROM pragma_foreign_key_check").Scan(&fkProblems); err != nil {
		t.Fatal(err)
	}
	if fkProblems != 0 {
		t.Errorf("%d foreign key problems", fkProblems)
	}
}
