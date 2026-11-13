package main

import (
	"bytes"
	"database/sql"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A command with subcommands prints its usage for -h, -help or --help in place of a subcommand, and main exits 0.
func TestParentCommandsHelp(t *testing.T) {
	parents := []string{"admin", "anime", "backup", "debug", "demo", "errors", "jobs", "library", "migrate",
		"policy", "providers", "router", "settings", "torrents", "tvdb"}
	for _, name := range parents {
		c, ok := commands[name]
		if !ok {
			t.Fatalf("no command %s", name)
		}
		for _, arg := range []string{"-h", "-help", "--help"} {
			if err := c.run([]string{arg}); !errors.Is(err, flag.ErrHelp) {
				t.Errorf("theatarr %s %s: %v, want the usage", name, arg, err)
			}
		}
	}
}

// theatarr library's usage lists the read-only rename preview.
func TestLibraryUsageListsRenamePreview(t *testing.T) {
	for _, sub := range []string{"theatarr library rename-preview -library KEY"} {
		if !strings.Contains(libraryUsage, sub) {
			t.Errorf("library usage lacks %q", sub)
		}
	}
}

// theatarr library rename-preview runs on a database it opens read-only: a copy in rollback journal mode stays so,
// with its permissions.
func TestLibraryRenamePreviewNeedsProfile(t *testing.T) {
	dir := t.TempDir()
	if err := copyPolicyDir(filepath.Join("..", "..", "internal", "decide", "testdata", "policy"), dir); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(t.TempDir(), "settings.db")
	args := commandSettingsArgs(t, []string{"rename-preview", "-policy", dir, "-db", db, "-library", "movie-remux"})
	journalMode(t, db, "delete")
	if err := os.Chmod(db, 0o640); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runLibrary(args, &out); err != nil {
		t.Fatalf("movie-remux: %v", err)
	}
	if fi, err := os.Stat(db); err != nil || fi.Mode().Perm() != 0o640 {
		t.Fatalf("permissions changed: %v %v", fi.Mode(), err)
	}
	if got := journalMode(t, db, ""); got != "delete" {
		t.Fatalf("journal mode now %s", got)
	}
}

// journalMode reads the database file's journal mode, setting it first when mode is not empty.
func journalMode(t *testing.T, path, mode string) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	q := "PRAGMA journal_mode"
	if mode != "" {
		q += " = " + mode
	}
	var got string
	if err := db.QueryRow(q).Scan(&got); err != nil {
		t.Fatal(err)
	}
	return got
}
