package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

func TestBackupRestoreRejectsNewerSchema(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		format   int
		database bool
	}{
		{"format 1 manifest", 1, false},
		{"format 2 manifest", 2, false},
		{"database despite compatible manifest", 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			base := restoreFixture(t, dir, "backup", nil)
			m, err := readManifest(base)
			if err != nil {
				t.Fatal(err)
			}
			target := m.Schema
			newer := target + 1
			m.Format, m.Schema = tc.format, newer
			if tc.database {
				m.Schema = target
				st, err := store.Open(context.Background(), base+".db", store.Options{})
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				if _, err := st.Writer().Exec("INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)", newer); err != nil {
					t.Fatal(err)
				}
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
				if m.Database, err = describeFile(base + ".db"); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(base+".manifest.json", raw, 0o600); err != nil {
				t.Fatal(err)
			}
			dst, polOut := filepath.Join(dir, "theatarr.db"), filepath.Join(dir, "policy-restored")
			original := []byte("existing database must stay untouched")
			if err := os.WriteFile(dst, original, 0o600); err != nil {
				t.Fatal(err)
			}
			// Each version source must independently refuse this set, even with -force.
			err = restoreBackup(context.Background(), io.Discard, base, dst, polOut, true,
				func(string) string { return "" }, time.Now())
			if err == nil {
				t.Error("restored a set with a newer schema")
			} else {
				for _, want := range []string{fmt.Sprint(newer), fmt.Sprint(target), "newer", "docs/restore.md"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not contain %q", err, want)
					}
				}
			}
			if !bytes.Equal(fileBytes(t, dst), original) {
				t.Error("restore replaced the existing database")
			}
			if _, err := os.Stat(polOut); !os.IsNotExist(err) {
				t.Errorf("restore created the policy output: %v", err)
			}
			if kept, err := filepath.Glob(dst + ".pre-restore-*"); err != nil || len(kept) != 0 {
				t.Errorf("restore moved the existing database: %v, %v", kept, err)
			}
		})
	}
}

func TestBackupFingerprintAllowsNewerSchema(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "theatarr.db")
	st, _, _, _ := dbOnlyInstall(t, dbPath, filepath.Join(dir, "missing-policy"))
	_, target, err := st.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().ExecContext(ctx,
		"INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)", target+1); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := fingerprint(ctx, dbPath)
	if err != nil || got["schema"] != target+1 {
		t.Fatalf("read-only diagnostic on newer schema: %v, %v", got, err)
	}
}
