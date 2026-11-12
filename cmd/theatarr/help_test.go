package main

import (
	"bytes"
	"errors"
	"flag"
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

// theatarr library rename-preview fails, printing nothing, when the library's profile cannot be built (movie-shadow
// reads its profile from an arr snapshot, and there is none; its names would lack {Custom Formats}); with the
// snapshot it runs.
func TestLibraryRenamePreviewNeedsProfile(t *testing.T) {
	pol := filepath.Join("..", "..", "internal", "decide", "testdata", "policy")
	db := filepath.Join(t.TempDir(), "settings.db")
	args := commandSettingsArgs(t, []string{"rename-preview", "-policy", pol, "-db", db, "-library", "movie-shadow"})
	var out bytes.Buffer
	if err := runLibrary(append(args, "-arr-snapshot", t.TempDir()), &out); err == nil ||
		!strings.Contains(err.Error(), "names cannot be rebuilt") || out.Len() != 0 {
		t.Fatalf("without the profile: %v, printed %q", err, out.String())
	}
	snap := filepath.Join("..", "..", "internal", "decide", "testdata", "arr")
	if err := runLibrary(append(args, "-arr-snapshot", snap), &out); err != nil {
		t.Fatalf("with the profile: %v", err)
	}
}
