package main

import (
	"errors"
	"flag"
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
