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
	parents := []string{"admin", "anime", "backup", "debug", "demo", "errors", "golive", "jobs", "library", "migrate",
		"parallel", "policy", "providers", "router", "settings", "shadow", "torrents", "tvdb"}
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

// theatarr library's usage lists the read-only cutover checks.
func TestLibraryUsageListsCoverageAndRenamePreview(t *testing.T) {
	for _, sub := range []string{"theatarr library coverage -library KEY", "theatarr library rename-preview -library KEY"} {
		if !strings.Contains(libraryUsage, sub) {
			t.Errorf("library usage lacks %q", sub)
		}
	}
}

// theatarr library rename-preview fails, printing nothing, when a profile its names need cannot be built: the
// library's own (movie-shadow reads its profile from an arr snapshot, and there is none) or, for a library that
// follows one, the followed library's (movie-follow follows movie-shadow; its adopted links are named with
// movie-shadow's profile). With the snapshot both run. The commands open the database read-only: a copy in rollback
// journal mode stays so, with its permissions.
func TestLibraryRenamePreviewNeedsProfile(t *testing.T) {
	dir := t.TempDir()
	if err := copyPolicyDir(filepath.Join("..", "..", "internal", "decide", "testdata", "policy"), dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "versions.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "versions:\n", `versions:
  movie-follow:
    follows: movie-shadow
    kind: movie
    profile: remux-2160p
    library: /media/movies-follow
    storage: debrid
    indexers: [public]
    providers: [rd]
    client: decypharr
    naming: {from_version: movie-shadow}
`, 1))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "library.yaml"),
		[]byte("library:\n  versions:\n    movie-follow: {roots: [/media/movies-follow]}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(t.TempDir(), "settings.db")
	base := commandSettingsArgs(t, []string{"rename-preview", "-policy", dir, "-db", db, "-library", "movie-shadow"})
	journalMode(t, db, "delete")
	if err := os.Chmod(db, 0o640); err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join("..", "..", "internal", "decide", "testdata", "arr")
	for _, lib := range []string{"movie-shadow", "movie-follow"} {
		args := append(append([]string(nil), base...), "-library", lib)
		var out bytes.Buffer
		if err := runLibrary(append(args, "-arr-snapshot", t.TempDir()), &out); err == nil ||
			!strings.Contains(err.Error(), "names cannot be rebuilt") || out.Len() != 0 {
			t.Fatalf("%s without the profile: %v, printed %q", lib, err, out.String())
		}
		if err := runLibrary(append(args, "-arr-snapshot", snap), &out); err != nil {
			t.Fatalf("%s with the profile: %v", lib, err)
		}
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
