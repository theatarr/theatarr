package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/uipages"
)

func init() {
	register(command{name: "settings", summary: "seed or recover database settings from YAML policy files (stop the service first)", run: func(args []string) error { return runSettings(args, os.Stdout) }})
}

// errSettingsInitialized refuses a CLI import into a database that already has every settings section: the
// running service imports those, with its continuity checks and publication.
var errSettingsInitialized = errors.New("this database already has settings: import from " + uipages.Import + " in the running service")

// runSettings imports a policy directory into a database with no settings yet, or into an older generation that
// lacks sections: the first seed and recovery, with the service stopped.
func runSettings(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "import" {
		const usage = "usage: theatarr settings import -policy DIR [-db FILE] [-confirm]: first seed or recovery of a database without " +
			"complete settings; stop the service first"
		if helpArg(args) {
			fmt.Fprintln(os.Stderr, usage)
			return flag.ErrHelp
		}
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("settings import", flag.ContinueOnError)
	dir := fs.String("policy", envOr("THEATARR_POLICY", ""), "directory to import")
	path := fs.String("db", defaultDBPath(), "database file")
	confirm := fs.Bool("confirm", false, "confirm the import after reviewing its preview")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *dir == "" || fs.NArg() != 0 {
		return errors.New("settings import requires -policy DIR")
	}
	// Parse before opening or migrating the target database; the settings are validated once the stored ones are
	// merged in (settings.ImportRequest).
	if _, err := policy.Read(*dir); err != nil {
		return err
	}
	ctx := context.Background()
	db, err := store.Open(ctx, *path, store.Options{})
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err = db.Migrate(ctx); err != nil {
		return err
	}
	runtime, req, err := settingsImport(ctx, db, *dir, os.LookupEnv, envOr("THEATARR_TRASH", ""))
	if err != nil {
		return err
	}
	preview, err := runtime.PreviewImport(ctx, req)
	if err != nil {
		return err
	}
	if err = json.NewEncoder(out).Encode(preview); err != nil {
		return err
	}
	fmt.Fprintln(out, preview.SectionsNote())
	approval, err := runtime.ConfirmImport(ctx, req)
	if err != nil {
		return err
	}
	if err = json.NewEncoder(out).Encode(approval); err != nil {
		return err
	}
	if !*confirm {
		fmt.Fprintln(out, "Review the preview, then repeat with -confirm to import.")
		return nil
	}
	req.ConfirmationToken = approval.Token
	saved, err := runtime.ImportAndApply(ctx, req)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Imported settings generation %d\n", saved.Generation)
	return nil
}

// settingsImport prepares the import of dir into db (migrated, with the service stopped): refused when the head
// already has every section, otherwise loaded alone or, for an older generation whose sections lived in YAML, over
// the files' other sections. A restore runs the same preview on its staged copy before naming this import.
func settingsImport(ctx context.Context, db *store.Store, dir string, lookup func(string) (string, bool), trash string) (*settings.RuntimeCoordinator, settings.ImportRequest, error) {
	st, err := settings.New(db)
	if err != nil {
		return nil, settings.ImportRequest{}, err
	}
	if complete, err := settingsComplete(ctx, db); err != nil {
		return nil, settings.ImportRequest{}, err
	} else if complete {
		return nil, settings.ImportRequest{}, errSettingsInitialized
	}
	st.SetTrashRoot(trash)
	st.SetLookup(lookup)
	base := &policy.Policy{Dir: dir}
	snap, err := st.Load(ctx, base)
	if err != nil {
		// An older generation whose sections lived in YAML does not load on its own: load it over the files' other
		// sections. This only loads the stored head; the import's sections come from importOver like any import.
		base, err = settings.ReadBase(ctx, db, dir)
		if err != nil {
			return nil, settings.ImportRequest{}, err
		}
		snap, err = st.Load(ctx, base)
		if err != nil {
			return nil, settings.ImportRequest{}, err
		}
	}
	runtime, err := settings.NewRuntimeCoordinator(st, base, snap, nil, func(context.Context, *settings.Snapshot, *settings.Runtime) (any, error) { return nil, nil })
	if err != nil {
		return nil, settings.ImportRequest{}, err
	}
	return runtime, settings.ImportRequest{Dir: dir, ExpectedGeneration: snap.Generation, Actor: "settings import", Lookup: lookup, RequireQuality: true}, nil
}

// settingsComplete reports whether the stored head is a generation with every settings section.
func settingsComplete(ctx context.Context, db *store.Store) (bool, error) {
	var generation int64
	if err := db.Reader().QueryRowContext(ctx, "SELECT COALESCE(MAX(generation),0) FROM settings_generation").Scan(&generation); err != nil || generation == 0 {
		return false, err
	}
	rows, err := db.Reader().QueryContext(ctx, "SELECT key FROM setting_section")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	stored := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return false, err
		}
		stored[key] = true
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, spec := range settings.Sections {
		if !stored[spec.Key] {
			return false, nil
		}
	}
	return true, nil
}
