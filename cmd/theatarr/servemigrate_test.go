package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

func TestMigrationCheckpointIsARestorableBackupSet(t *testing.T) {
	ctx := context.Background()
	db, st, base, _ := runtimeFixture(t)
	initial, err := st.BootstrapQuality(ctx, base, "test")
	if err != nil {
		t.Fatal(err)
	}
	coord, err := settings.NewRuntimeCoordinator(st, base, initial, struct{}{},
		func(context.Context, *settings.Snapshot, *settings.Runtime) (any, error) { return struct{}{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	c := serveMigrationConfig{st: db, base: base, manager: coord, backupDir: filepath.Join(dir, "backups"),
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), now: func() time.Time { return time.Date(2026, 10, 29, 4, 0, 0, 0, time.UTC) }}
	m := serveMigration(c)
	cp, err := m.Checkpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(cp.Backup) != "theatarr-20261029-040000" || cp.Generation != initial.Generation || cp.Settings != cp.Backup+".settings.yaml" {
		t.Fatalf("checkpoint %+v", cp)
	}
	b, err := os.ReadFile(cp.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("fixture-")) || !bytes.Contains(b, []byte("movie-remux")) {
		t.Errorf("settings copy:\n%s", b)
	}
	var out bytes.Buffer
	dst := filepath.Join(dir, "restored", "theatarr.db")
	setFixtureEnv(t, cp.Backup)
	if err := restoreBackup(ctx, &out, cp.Backup, dst, filepath.Join(dir, "restored", "policy"), false,
		os.Getenv, time.Now()); err != nil {
		t.Fatalf("restore: %v\n%s", err, out.String())
	}
	// The fixture's generation predates the settings sections and view identities: it loads only over its files, so
	// the restore names the import that completes it, which succeeds with the fixture's credentials.
	if !strings.Contains(out.String(), "next: the set has no complete settings generation: import its policy before starting serve:\n") {
		t.Errorf("restore output:\n%s", out.String())
	}

	c.backupDir = ""
	if _, err := serveMigration(c).Checkpoint(ctx); err == nil || !strings.Contains(err.Error(), "backups are off") {
		t.Errorf("without backups: %v", err)
	}

	// verify's golive check runs inside serve on the stored head, without the service state.
	r, err := m.Verifier.Golive(ctx, "movie-remux")
	if err != nil || r.Version != "movie-remux" || len(r.Results) == 0 {
		t.Fatalf("golive %+v %v", r, err)
	}
}

// verify's golive check and the startup log ask the generation whether a credential resolves: provider keys and
// view and section credentials stored in the settings database count, though no env variable is set.
func TestServeGoliveResolvesStoredCredentials(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, st, base, initial := runtimeFixture(t) // the import stored every credential in the database
	coord, err := settings.NewRuntimeCoordinator(st, base, initial, struct{}{},
		func(context.Context, *settings.Snapshot, *settings.Runtime) (any, error) { return struct{}{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	m := serveMigration(serveMigrationConfig{st: db, base: base, manager: coord,
		log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	r, err := m.Verifier.Golive(ctx, "movie-remux")
	if err != nil {
		t.Fatal(err)
	}
	var fallback string
	for _, res := range r.Results {
		if res.Check == "router fallback" {
			fallback = string(res.Status)
		}
		if res.Section == "secrets" && strings.HasPrefix(res.Check, "provider ") && res.Status != "PASS" {
			t.Errorf("stored provider key: %+v", res)
		}
		if strings.Contains(res.Check, policy.SettingsSecretPrefix) || strings.Contains(res.Check, settings.CredentialSentinel) {
			t.Errorf("a stored credential named by its derived env name: %+v", res)
		}
	}
	if fallback == "FAIL" || fallback == "" {
		t.Fatalf("router fallback %q with every provider key stored:\n%+v", fallback, r.Results)
	}
	// The startup log names only what resolves nowhere: env-named credentials left unset, never a stored one.
	stored := map[string]bool{}
	for _, r := range initial.Records.Providers {
		stored[r.Config.APIKeyEnv] = true
	}
	for _, r := range initial.Records.Indexers {
		stored[r.Config.APIKeyEnv] = true
	}
	unset := settings.Credentials{Policy: initial.Policy, Snap: initial, Lookup: func(string) (string, bool) { return "", false }}.Unset()
	for _, name := range unset {
		if stored[name] || strings.Contains(name, "(settings)") || strings.HasPrefix(name, policy.SettingsSecretPrefix) {
			t.Errorf("startup would warn about a stored credential: %s (of %v)", name, unset)
		}
	}
	if len(unset) == 0 || !slices.Contains(unset, "THEATARR_TMDB_KEY") {
		t.Errorf("an env-named credential left unset is still reported: %v", unset)
	}
}
