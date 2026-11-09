package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestOpenRejectsNewerSchema(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTest(t, Options{})
	_, target, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	newer := target + 1
	if _, err := s.Writer().ExecContext(ctx,
		"INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)", newer); err != nil {
		t.Fatal(err)
	}
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := Open(ctx, path, Options{})
	if got != nil {
		got.Close()
		t.Error("Open returned a store for a newer schema")
	}
	requireNewerSchemaError(t, err, newer, target)
}

func TestMigrateRejectsNewerSchema(t *testing.T) {
	t.Parallel()
	for _, version := range []int64{82, 0} {
		t.Run(fmt.Sprintf("from %d", version), func(t *testing.T) {
			testMigrateRejectsNewerSchema(t, version)
		})
	}
}

func testMigrateRejectsNewerSchema(t *testing.T, version int64) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, pendingPath(t, version), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := s.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate another binary upgrading after Open, both with and without known migrations pending.
	if version == 0 {
		_, err = p.Up(ctx)
	} else {
		_, err = p.UpTo(ctx, version)
	}
	if err != nil {
		t.Fatal(err)
	}
	_, target, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	newer := target + 1
	if _, err := s.Writer().ExecContext(ctx,
		"INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)", newer); err != nil {
		t.Fatal(err)
	}
	res, err := s.Migrate(ctx)
	requireNewerSchemaError(t, err, newer, target)
	if len(res) != 0 {
		t.Errorf("applied %d migrations to a newer schema", len(res))
	}
	if version == 0 {
		return
	}
	var count int
	if err := s.Reader().QueryRowContext(ctx,
		"SELECT count(*) FROM goose_db_version WHERE version_id > ? AND version_id <= ?", version, target).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("wrote %d migration records before refusing the newer schema", count)
	}
}

func TestOpenAllowsCompatibleSchema(t *testing.T) {
	t.Parallel()
	for _, version := range []int64{-1, 82, 0} {
		t.Run(fmt.Sprintf("from %d", version), func(t *testing.T) {
			ctx := context.Background()
			path := pendingPath(t, version)
			s, err := Open(ctx, path, Options{})
			if err != nil {
				t.Fatal(err)
			}
			p, err := s.provider(false)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case version == 0:
				_, err = p.Up(ctx)
			case version > 0:
				_, err = p.UpTo(ctx, version)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(ctx, path, Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if version == -1 {
				var exists bool
				if err := s.Reader().QueryRowContext(ctx,
					"SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE name = ?)", MigrationTable).Scan(&exists); err != nil {
					t.Fatal(err)
				}
				if exists {
					t.Error("Open created the migration table")
				}
			}
			if _, err := s.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func requireNewerSchemaError(t *testing.T, err error, current, target int64) {
	t.Helper()
	if err == nil {
		t.Fatal("accepted a newer schema")
	}
	for _, want := range []string{fmt.Sprint(current), fmt.Sprint(target), "newer", "docs/restore.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}
