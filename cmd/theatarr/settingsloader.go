package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

var errNoSettings = errors.New("no settings yet: run Setup or theatarr settings import")

func settingsSourceFlag(fs *flag.FlagSet) {
	if fs.Lookup("settings-source") == nil {
		fs.String("settings-source", settingsSourceDefault(), "deprecated: only db is supported (env THEATARR_SETTINGS_SOURCE)")
	}
}

// loadCommandPolicy reads the stored generation without importing or publishing settings.
// Commands requiring settings refuse an uninitialized database.
func loadCommandPolicy(fs *flag.FlagSet, dir string) (*policy.Policy, error) {
	p, _, err := loadCommandSettings(fs, dir)
	return p, err
}

// loadCommandSettings is loadCommandPolicy with its stored settings snapshot.
func loadCommandSettings(fs *flag.FlagSet, dir string) (*policy.Policy, *settings.Snapshot, error) {
	source := settingsSourceDefault()
	if f := fs.Lookup("settings-source"); f != nil {
		source = f.Value.String()
	}
	path := defaultDBPath()
	if f := fs.Lookup("db"); f != nil {
		path = f.Value.String()
	}
	return loadSettingsSnapshot(context.Background(), source, dir, path)
}

func loadSettingsPolicy(ctx context.Context, source, dir, path string) (*policy.Policy, error) {
	p, _, err := loadSettingsSnapshot(ctx, source, dir, path)
	return p, err
}

func loadSettingsSnapshot(ctx context.Context, source, dir, path string) (*policy.Policy, *settings.Snapshot, error) {
	return loadSettingsSnapshotOpts(ctx, source, dir, path, false)
}

// loadSettingsSnapshotOpts is loadSettingsSnapshot; readOnly opens the database read-only (store.Options.ReadOnly).
func loadSettingsSnapshotOpts(ctx context.Context, source, dir, path string, readOnly bool) (*policy.Policy,
	*settings.Snapshot, error) {
	if err := checkSettingsSource(source); err != nil {
		return nil, nil, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil, errNoSettings
	} else if err != nil {
		return nil, nil, err
	}
	db, err := store.Open(ctx, path, store.Options{ReadOnly: readOnly})
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	// Offline commands never migrate a database or seed settings.
	var generation int64
	err = db.Reader().QueryRowContext(ctx, "SELECT COALESCE(MAX(generation),0) FROM settings_generation").Scan(&generation)
	if err != nil {
		var count int
		lookupErr := db.Reader().QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='settings_generation'").Scan(&count)
		if lookupErr == nil && count == 0 {
			return nil, nil, errNoSettings
		}
		return nil, nil, err
	}
	if generation == 0 {
		return nil, nil, errNoSettings
	}
	base, err := databasePolicyBase(dir)
	if err != nil {
		return nil, nil, err
	}
	st, err := settings.New(db)
	if err != nil {
		return nil, nil, err
	}
	snap, err := st.Load(ctx, base)
	if err != nil {
		return nil, nil, err
	}
	// As serve's services see it: indexers and providers disabled in the settings are left out.
	active, _, err := runtimeServicePolicy(snap)
	return active, snap, err
}

func checkSettingsSource(source string) error {
	if source == "" || source == "db" {
		return nil
	}
	if source == "yaml" {
		return errors.New("the YAML settings source is no longer read; run theatarr settings import")
	}
	return errors.New("settings-source must be db")
}

// policyDirDefault is the policy directory, THEATARR_POLICY or /config/policy: the data files the stored
// settings point at with relative paths (metadata.overrides_dir, format files) and what backups archive. It is not
// a settings source.
func policyDirDefault() string { return envOr("THEATARR_POLICY", "/config/policy") }

// databasePolicyBase is the base the stored settings load over: the policy directory, where relative data paths
// resolve (policyDirDefault when dir is empty). Root YAML files are never read by database settings loaders.
func databasePolicyBase(dir string) (*policy.Policy, error) {
	if dir == "" {
		dir = policyDirDefault()
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	return &policy.Policy{Dir: abs}, nil
}

func settingsSourceDefault() string {
	if value := os.Getenv("THEATARR_SETTINGS_SOURCE"); value != "" {
		return value
	}
	return envOr("THEATARR_SETTINGS_SOURCE", "db")
}
