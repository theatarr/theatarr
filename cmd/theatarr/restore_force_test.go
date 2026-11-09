package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// restoreFixture writes a backup set of a fresh database and the example policy (edited by edit, after it loaded)
// and returns the set's base.
func restoreFixture(t *testing.T, dir, name string, edit func(polDir string)) string {
	t.Helper()
	ctx := context.Background()
	polDir := copyTree(t, filepath.Join("..", "..", "examples", "policy"))
	pol, err := policy.Load(polDir)
	if err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(polDir)
	}
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(dir, name+".src.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	base, _, err := writeBackupSet(ctx, st, pol, filepath.Join(dir, name), time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return base
}

func fileBytes(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Restore -force stages and validates the database and the policy before it moves the old database
// aside, fails on a policy this Theatarr cannot load, and leaves the old database and policy in place on any failure.
func TestRestoreForceValidatesBeforeReplacing(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	good := restoreFixture(t, dir, "good", nil)
	// The restore names the settings import of these policy files only when it previews.
	setFixtureEnv(t, good)
	dst, polOut := filepath.Join(dir, "live", "theatarr.db"), filepath.Join(dir, "live", "policy")
	var out bytes.Buffer
	getenv := func(string) string { return "" }
	if err := restoreBackup(ctx, &out, good, dst, polOut, false, getenv, time.Now()); err != nil {
		t.Fatal(err)
	}
	liveDB, livePolicy := fileBytes(t, dst), fileBytes(t, filepath.Join(polOut, "router.yaml"))
	requireUntouched := func(t *testing.T) {
		t.Helper()
		if !bytes.Equal(fileBytes(t, dst), liveDB) {
			t.Error("the live database was replaced")
		}
		if !bytes.Equal(fileBytes(t, filepath.Join(polOut, "router.yaml")), livePolicy) {
			t.Error("the live policy was replaced")
		}
		if kept, _ := filepath.Glob(dst + ".pre-restore-*"); len(kept) > 0 {
			t.Errorf("the live database was moved aside: %v", kept)
		}
		if _, err := os.Stat(dst + ".partial"); err == nil {
			t.Error("a staged database was left behind")
		}
	}

	t.Run("policy that does not load", func(t *testing.T) {
		bad := restoreFixture(t, dir, "badpolicy", func(polDir string) {
			f, err := os.OpenFile(filepath.Join(polDir, "router.yaml"), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			f.WriteString("\nnot_a_policy_field: true\n")
			f.Close()
		})
		if err := restoreBackup(ctx, &out, bad, dst, polOut, true, getenv, time.Now()); err == nil {
			t.Fatal("restored a policy that does not load")
		}
		requireUntouched(t)
	})

	t.Run("database that fails its integrity check", func(t *testing.T) {
		base := restoreFixture(t, dir, "baddb", nil)
		junk := bytes.Repeat([]byte("not a database "), 512)
		if err := os.WriteFile(base+".db", junk, 0o600); err != nil {
			t.Fatal(err)
		}
		m, err := readManifest(base)
		if err != nil {
			t.Fatal(err)
		}
		if m.Database, err = describeFile(base + ".db"); err != nil {
			t.Fatal(err)
		}
		b, _ := json.MarshalIndent(m, "", "  ")
		if err := os.WriteFile(base+".manifest.json", b, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := restoreBackup(ctx, &out, base, dst, polOut, true, getenv, time.Now()); err == nil {
			t.Fatal("restored a database that fails its integrity check")
		}
		requireUntouched(t)
	})
}
