package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDeleteSelection(t *testing.T) {
	t.Parallel()
	const h = "1111111111111111111111111111111111111111"
	sel, err := parseDeleteSelection([]string{"rd:10:" + h, "tb:7:" + h}, []string{"tb:7:o1-abc"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(sel) != 2 || sel[0].Override || !sel[1].Override || sel[1].OverrideToken != "o1-abc" ||
		!sel[0].AllowRecent || sel[0].Hash != h {
		t.Errorf("selection: %+v", sel)
	}
	for _, bad := range [][2][]string{
		{{"rd:10"}, nil},
		{{"rd:10:" + h}, {"rd:11:o1-x"}},
		{nil, nil},
	} {
		if _, err := parseDeleteSelection(bad[0], bad[1], false); err == nil {
			t.Errorf("%v %v: want an error", bad[0], bad[1])
		}
	}
}

// The commands that only read the database and backups run without provider keys.
func TestTorrentsBackupsCommand(t *testing.T) {
	t.Parallel()
	db := filepath.Join(t.TempDir(), "theatarr.db")
	pol := filepath.Join("..", "..", "examples", "policy")
	if err := runTorrents(commandSettingsArgs(t, []string{"backups", "-db", db, "-policy", pol})); err != nil {
		t.Fatal(err)
	}
	if err := runTorrents(commandSettingsArgs(t, []string{"journal", "-db", db, "-policy", pol})); err != nil {
		t.Fatal(err)
	}
	if err := runTorrents(commandSettingsArgs(t, []string{"nope"})); err == nil || !strings.Contains(err.Error(), "report|backup") {
		t.Errorf("unknown subcommand: %v", err)
	}
	if got := torrentBackupDir("/var/lib/theatarr/theatarr.db"); got != "/var/lib/theatarr/torrent-backups" {
		t.Errorf("backup dir %s", got)
	}
}
