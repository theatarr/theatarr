package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func TestRouterStatusAndArgs(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "theatarr.db")
	st, err := store.Open(ctx, storetest.Seed(t, db), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	st.Close()
	pol := filepath.Join("..", "..", "examples", "policy")
	var out bytes.Buffer
	if err := runRouter(commandSettingsArgs(t, []string{"status", "-db", db, "-policy", pol}), &out, nil); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "pause      ON: nothing is searched, downloaded or linked") ||
		!strings.Contains(s, "DOWNLOADS  PROVIDERS") || !strings.Contains(s, "Watch only  no") ||
		!strings.Contains(s, "active     none") ||
		!strings.Contains(s, "0 unfinished cache check(s)") || !strings.Contains(s, "movie-remux") {
		t.Errorf("status:\n%s", s)
	}
	out.Reset()
	if err := runRouter(commandSettingsArgs(t, []string{"status", "-db", db, "-policy", pol, "-json"}), &out, nil); err != nil ||
		!strings.Contains(out.String(), `"enabled": false`) {
		t.Errorf("json: %v\n%s", err, out.String())
	}
	if err := runRouter(commandSettingsArgs(t, []string{"dry-run", "-db", db, "-policy", pol}), &out, nil); err == nil ||
		!strings.Contains(err.Error(), "-library") {
		t.Errorf("dry-run without a version: %v", err)
	}
	if err := runRouter(commandSettingsArgs(t, nil), &out, nil); err == nil {
		t.Error("no subcommand accepted")
	}
	if err := runRouter(commandSettingsArgs(t, []string{"add"}), &out, nil); err == nil {
		t.Error("unknown subcommand accepted")
	}
}
