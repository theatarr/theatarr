package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// With the example policy (library and playlists disabled) the command is a dry run and says so.
func TestAnimePlaylistsDryRun(t *testing.T) {
	db := filepath.Join(t.TempDir(), "theatarr.db")
	var out bytes.Buffer
	err := runAnimePlaylists(commandSettingsArgs(t, []string{"-policy", filepath.Join("..", "..", "examples", "policy"), "-db", db}), &out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "DRY RUN (library.enabled is false)") {
		t.Errorf("output %q", out.String())
	}
}
