package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

// Command tests explicitly initialize their fixture settings before invoking tools.
func commandSettingsArgs(t *testing.T, input []string) []string {
	t.Helper()
	args := append([]string(nil), input...)
	dir, path := "", ""
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "-policy":
			dir = args[i+1]
		case "-db":
			path = args[i+1]
		case "-settings-source", "--settings-source":
			if args[i+1] != "db" {
				t.Fatalf("commandSettingsArgs: -settings-source %s is no longer accepted; drop it", args[i+1])
			}
		}
	}
	if dir == "" {
		return args
	}
	if path == "" {
		path = filepath.Join(t.TempDir(), "settings.db")
		args = append(args, "-db", path)
	}
	if path == "none" {
		path = filepath.Join(t.TempDir(), "settings.db")
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-db" {
				args[i+1] = path
			}
		}
	}
	ctx := context.Background()
	db, err := store.Open(ctx, path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var gen int64
	if err = db.Reader().QueryRowContext(ctx, "SELECT COALESCE(MAX(generation),0) FROM settings_generation").Scan(&gen); err != nil {
		t.Fatal(err)
	}
	if gen > 0 {
		return withoutPolicyFlag(args)
	}
	base, err := policy.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if base.Metadata != nil && base.Metadata.OverridesDir != "" {
		base.Metadata.OverridesDir = base.OverridesPath()
	}
	st, err := settings.New(db)
	if err != nil {
		t.Fatal(err)
	}
	st.SetTrashRoot("../../internal/settings/testdata/trash")
	lookup := func(name string) (string, bool) {
		if v, ok := os.LookupEnv(name); ok && v != "" {
			return v, true
		}
		return "fixture-" + name, true
	}
	if _, err = st.Bootstrap(ctx, base, settings.ImportRequest{Dir: dir, Actor: "test fixture", Lookup: lookup}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.BootstrapQuality(ctx, base, "test fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.BootstrapSwitches(ctx, base, "test fixture", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = st.BootstrapSections(ctx, base, "test fixture", nil); err != nil {
		t.Fatal(err)
	}
	return withoutPolicyFlag(args)
}

func withoutPolicyFlag(args []string) []string {
	out := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "-policy" && i+1 < len(args) {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}
