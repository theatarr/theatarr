package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// reset-password for admin removes the first start's password file next to the database.
func TestResetPasswordRemovesInitialPasswordFile(t *testing.T) {
	auth.SetFastHashing()
	ctx := context.Background()
	dir := t.TempDir()
	db := filepath.Join(dir, "theatarr.db")
	st, err := store.Open(ctx, storetest.Seed(t, db), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := auth.New(ctx, st, auth.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	path := filepath.Join(dir, auth.InitialPasswordFile)
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	// Another account: the file stays.
	var out bytes.Buffer
	if err := resetPassword([]string{"-db", db, "-username", "other", "-password-stdin"},
		strings.NewReader("a long enough password\n"), &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("reset of another account removed the file: %v", err)
	}
	if err := resetPassword([]string{"-db", db}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("file after reset-password: %v", err)
	}
	if !strings.Contains(out.String(), "removed "+path) {
		t.Errorf("output %q", out.String())
	}
}

// create-key prints, once, a key that signs in as the named account.
func TestAdminCreateKey(t *testing.T) {
	auth.SetFastHashing()
	ctx := context.Background()
	dir := t.TempDir()
	db := filepath.Join(dir, "theatarr.db")
	st, err := store.Open(ctx, storetest.Seed(t, db), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	svc, _, err := auth.New(ctx, st, auth.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := createKey([]string{"-db", db, "-user", auth.AdminUsername, "-name", "agent"}, &out); err != nil {
		t.Fatal(err)
	}
	key := strings.TrimSpace(out.String())
	if key == "" || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("output %q", out.String())
	}
	id, err := svc.CheckAPIKey(ctx, key)
	if err != nil || id.KeyName != "agent" || id.Username != auth.AdminUsername {
		t.Fatalf("key identity %+v: %v", id, err)
	}
	if err := createKey([]string{"-db", db, "-user", "nobody", "-name", "agent"}, &out); err == nil || !strings.Contains(err.Error(), `no account "nobody"`) {
		t.Fatalf("unknown user: %v", err)
	}
	if err := createKey([]string{"-db", db, "-user", auth.AdminUsername}, &out); err == nil {
		t.Fatal("missing -name accepted")
	}
}
