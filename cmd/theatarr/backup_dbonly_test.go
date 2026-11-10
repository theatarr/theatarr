package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/retention"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// dbOnlyInstall is a database-only install as serve bootstraps it: no YAML in polDir (missing or empty), every
// setting in the database. It returns the store, the settings store, the head snapshot and serve's policy.
func dbOnlyInstall(t *testing.T, dbPath, polDir string) (*store.Store, *settings.Store, *settings.Snapshot, *policy.Policy) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, dbPath), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	base, err := settings.ReadBase(ctx, st, polDir)
	if err != nil {
		t.Fatal(err)
	}
	sst, snap, pol, err := loadServePolicy(ctx, "db", polDir, base, st)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Generation == 0 {
		sections := []settings.Section{}
		for _, spec := range settings.Sections {
			sections = append(sections, settings.Section{Record: settings.Record{Key: spec.Key}, Config: json.RawMessage("null")})
		}
		snap, err = sst.Save(ctx, settings.SaveRequest{Base: base, Actor: "setup", Records: settings.Records{Sections: sections, Switches: &settings.GlobalSwitches{}, Profiles: []settings.QualityProfile{}, Formats: []settings.CustomFormat{}, FormatSets: []settings.FormatSet{}, Naming: []settings.VersionNaming{}}})
		if err != nil {
			t.Fatal(err)
		}
		pol = snap.Policy
	}
	if snap.Generation == 0 || pol.Dir == "" {
		t.Fatalf("bootstrap: generation %d, dir %q", snap.Generation, pol.Dir)
	}
	now := store.Millis(time.Now())
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO title (id, kind, name, year, tmdb_id, created_at, updated_at)
		VALUES (1, 'movie', 'Alpha', 2020, 101, ?1, ?1)`, now); err != nil {
		t.Fatal(err)
	}
	return st, sst, snap, pol
}

func generationOf(t *testing.T, dbPath string) int64 {
	t.Helper()
	db, err := openRO(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var gen int64
	if err := db.QueryRow(`SELECT COALESCE(MAX(generation), 0) FROM settings_generation`).Scan(&gen); err != nil {
		t.Fatal(err)
	}
	return gen
}

// A database-only install (no policy directory, or one with no YAML) writes a complete backup set that
// says so in its manifest, and the set restores: the database with its settings, the effective configuration
// validated from the staged database, and serve's settings load from the restored files.
func TestBackupRestoreDatabaseOnly(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, state string }{{"no policy directory", policyAbsent}, {"empty policy directory", policyEmpty}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			polDir := filepath.Join(dir, "policy")
			if tc.state == policyEmpty {
				if err := os.MkdirAll(polDir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			src := filepath.Join(dir, "theatarr.db")
			st, _, snap, pol := dbOnlyInstall(t, src, polDir)
			backups := filepath.Join(dir, "backups")
			base, m, err := writeBackupSet(ctx, st, pol, backups, time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC))
			if err != nil {
				t.Fatal(err)
			}
			if m.Format != 2 || m.Policy.State != tc.state || m.Policy.Revision != emptyRevision() || len(m.Policy.Files) != 0 {
				t.Fatalf("manifest policy %+v", m.Policy)
			}
			_, tarErr := os.Stat(base + ".policy.tar")
			if (tc.state == policyAbsent) != os.IsNotExist(tarErr) {
				t.Fatalf("policy archive for %s: %v", tc.state, tarErr)
			}
			raw, _ := os.ReadFile(base + ".manifest.json")
			if !bytes.Contains(raw, []byte(`"state": "`+tc.state+`"`)) {
				t.Errorf("manifest does not name the policy state:\n%s", raw)
			}
			var list bytes.Buffer
			if err := listBackups(&list, backups); err != nil || !strings.Contains(list.String(), "database only") {
				t.Errorf("list: %v\n%s", err, list.String())
			}

			// A database-only set restores without -policy-out; an empty set restores into one.
			var out bytes.Buffer
			dst := filepath.Join(dir, "restored", "theatarr.db")
			polOut := ""
			if tc.state == policyEmpty {
				polOut = filepath.Join(dir, "restored", "policy")
			}
			if err := restoreBackup(ctx, &out, base, dst, polOut, false, func(string) string { return "" }, time.Now()); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "the settings are in the database") && !strings.Contains(out.String(), "database-only install") {
				t.Errorf("restore output:\n%s", out.String())
			}
			a, _ := fingerprint(ctx, src)
			b, _ := fingerprint(ctx, dst)
			ja, _ := json.Marshal(a)
			jb, _ := json.Marshal(b)
			if string(ja) != string(jb) || a["titles"] != 1 {
				t.Errorf("fingerprints:\n%s\n%s", ja, jb)
			}
			if got := generationOf(t, dst); got != snap.Generation {
				t.Errorf("restored settings generation %d, want %d", got, snap.Generation)
			}
			// Serve's settings load from the restored database and directory.
			loadDir := polOut
			if loadDir == "" {
				loadDir = filepath.Join(dir, "restored", "no-policy")
			}
			if _, rs, err := loadSettingsSnapshot(ctx, "db", loadDir, dst); err != nil || rs == nil || rs.Generation != snap.Generation {
				t.Fatalf("restored settings: %v %+v", err, rs)
			}
			for _, leftover := range []string{dst + ".partial", dst + ".validate", dst + ".validate-wal"} {
				if _, err := os.Stat(leftover); err == nil {
					t.Errorf("left behind %s", leftover)
				}
			}
		})
	}
}

// theatarr backup now works on a database-only install: no policy directory, settings in the database.
func TestBackupNowDatabaseOnly(t *testing.T) {
	for _, env := range []string{"THEATARR_POLICY", "THEATARR_SETTINGS_SOURCE", "THEATARR_BACKUP_DIR"} {
		t.Setenv(env, "")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "theatarr.db")
	st, _, _, _ := dbOnlyInstall(t, src, filepath.Join(dir, "policy"))
	st.Close()
	var out bytes.Buffer
	backups := filepath.Join(dir, "backups")
	if err := runBackup([]string{"now", "-db", src, "-policy", filepath.Join(dir, "policy"), "-dir", backups}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "database-only install") {
		t.Errorf("output:\n%s", out.String())
	}
	sets, _ := filepath.Glob(filepath.Join(backups, "theatarr-*.manifest.json"))
	if len(sets) != 1 {
		t.Fatalf("sets %v", sets)
	}
	// No database: an error, and none is created.
	missing := filepath.Join(dir, "none.db")
	if err := runBackup([]string{"now", "-db", missing, "-policy", filepath.Join(dir, "policy"), "-dir", backups}, &out); err == nil {
		t.Error("backed up a missing database")
	}
	if _, err := os.Stat(missing); err == nil {
		t.Error("backup now created a database")
	}
}

// Restore validates the effective configuration from the staged database (settings that do not load
// stop it before the live files are touched), and a database-only set is not restored over a policy directory
// whose YAML would sit under the restored settings.
func TestRestoreDatabaseOnlyRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	st, _, _, pol := dbOnlyInstall(t, filepath.Join(dir, "theatarr.db"), filepath.Join(dir, "policy"))
	good, _, err := writeBackupSet(ctx, st, pol, filepath.Join(dir, "good"), time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	getenv := func(string) string { return "" }
	var out bytes.Buffer

	t.Run("policy files in -policy-out", func(t *testing.T) {
		polOut := filepath.Join(dir, "yaml")
		os.MkdirAll(polOut, 0o755)
		os.WriteFile(filepath.Join(polOut, "router.yaml"), []byte("router: {}\n"), 0o644)
		dst := filepath.Join(dir, "r1", "theatarr.db")
		if err := restoreBackup(ctx, &out, good, dst, polOut, true, getenv, time.Now()); err == nil ||
			!strings.Contains(err.Error(), "holds policy files") {
			t.Fatalf("restore over YAML: %v", err)
		}
		if _, err := os.Stat(dst); err == nil {
			t.Error("a database was written")
		}
	})

	t.Run("settings that do not load", func(t *testing.T) {
		if _, err := st.Writer().ExecContext(ctx, `UPDATE setting_section SET config = '{"not_a_router_field": true}' WHERE key = 'router'`); err != nil {
			t.Fatal(err)
		}
		bad, _, err := writeBackupSet(ctx, st, pol, filepath.Join(dir, "bad"), time.Date(2026, 10, 30, 4, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		live := filepath.Join(dir, "live", "theatarr.db")
		if err := restoreBackup(ctx, &out, good, live, "", false, getenv, time.Now()); err != nil {
			t.Fatal(err)
		}
		before := fileBytes(t, live)
		if err := restoreBackup(ctx, &out, bad, live, "", true, getenv, time.Now()); err == nil ||
			!strings.Contains(err.Error(), "restored settings") {
			t.Fatalf("restored settings that do not load: %v", err)
		}
		if !bytes.Equal(fileBytes(t, live), before) {
			t.Error("the live database was replaced")
		}
		if kept, _ := filepath.Glob(live + ".pre-restore-*"); len(kept) > 0 {
			t.Errorf("the live database was moved aside: %v", kept)
		}
	})
}

// A migration checkpoint on a database-only install is a complete backup set.
func TestMigrationCheckpointDatabaseOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	polDir := filepath.Join(dir, "policy")
	st, sst, snap, pol := dbOnlyInstall(t, filepath.Join(dir, "theatarr.db"), polDir)
	base, err := settings.ReadBase(ctx, st, polDir)
	if err != nil {
		t.Fatal(err)
	}
	coord, err := settings.NewRuntimeCoordinator(sst, base, snap, struct{}{},
		func(context.Context, *settings.Snapshot, *settings.Runtime) (any, error) { return struct{}{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	c := serveMigrationConfig{st: st, base: pol, manager: coord, backupDir: filepath.Join(dir, "backups"),
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), now: func() time.Time { return time.Date(2026, 10, 30, 5, 0, 0, 0, time.UTC) }}
	cp, err := migrationCheckpoint(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	m, err := readManifest(cp.Backup)
	if err != nil || m.Policy.State != policyAbsent {
		t.Fatalf("checkpoint manifest %+v %v", m, err)
	}
	var out bytes.Buffer
	if err := restoreBackup(ctx, &out, cp.Backup, filepath.Join(dir, "restored.db"), "", false,
		func(string) string { return "" }, time.Now()); err != nil {
		t.Fatal(err)
	}
}

// A failed nightly backup leaves no partial set, still prunes old sets and finished jobs, and schedules
// retention on its last attempt (earlier attempts leave it for after a backup that works).
func TestNightlyBackupFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(dir, "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	backups := filepath.Join(dir, "backups")
	os.MkdirAll(backups, 0o755)
	for _, old := range []string{"theatarr-20260101-030000", "theatarr-20260102-030000", "theatarr-20260103-030000"} {
		for _, sfx := range []string{".db", ".policy.tar", ".manifest.json", ".settings.yaml"} {
			os.WriteFile(filepath.Join(backups, old+sfx), nil, 0o644)
		}
	}
	// A policy "directory" that is a file: the database copy and the archive are written, then the revision fails.
	notDir := filepath.Join(dir, "policy")
	os.WriteFile(notDir, []byte("x"), 0o644)
	q := jobs.NewQueue(st)
	n := &nightly{st: st, q: q, pol: &policy.Policy{Dir: notDir}, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		dir: backups, keep: 2, hour: 3}
	retentionQueued := func() int {
		var c int
		st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM job WHERE kind = ?`, retention.JobKind).Scan(&c)
		return c
	}
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO job (kind, state, payload, run_at, created_at, updated_at,
		finished_at) VALUES ('old', 'done', '{}', 0, 0, 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if err := n.run(ctx, &jobs.Job{Attempt: 1, MaxAttempts: 3}); err == nil {
		t.Fatal("a failed backup returned no error")
	}
	all, _ := filepath.Glob(filepath.Join(backups, "theatarr-*"))
	if len(all) != 8 {
		t.Errorf("after a failed backup (two old sets of four files expected, no partial set): %v", all)
	}
	var old int
	st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM job WHERE kind = 'old'`).Scan(&old)
	if old != 0 {
		t.Error("finished jobs were not pruned after a failed backup")
	}
	if retentionQueued() != 0 {
		t.Error("retention was scheduled on an attempt that will be retried")
	}
	if err := n.run(ctx, &jobs.Job{Attempt: 3, MaxAttempts: 3}); err == nil {
		t.Fatal("a failed backup returned no error")
	}
	if retentionQueued() != 1 {
		t.Error("the last attempt did not schedule retention")
	}
}

// A secret named in the settings after the start is listed in the next nightly set: the nightly job records the
// settings head's secret names, and falls back to the startup policy only when the head cannot be loaded.
func TestNightlyManifestNamesHeadSecrets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	dbPath, polDir := filepath.Join(dir, "theatarr.db"), filepath.Join(dir, "policy")
	st, sst, snap, pol := dbOnlyInstall(t, dbPath, polDir)
	const env = "THEATARR_TEST_HEAD_TMDB_KEY"
	for _, s := range secretsOf(pol, os.Getenv, nil) {
		if s.Env == env {
			t.Fatal("the startup policy already names the secret")
		}
	}
	records := snap.Records
	records.Sections = append([]settings.Section(nil), records.Sections...)
	editSection(t, &records, "metadata", func(cfg map[string]any) {
		tmdb := child(child(cfg, "sources"), "tmdb")
		tmdb["api_key_env"], tmdb["url"] = env, "https://api.themoviedb.org/3"
	})
	base, err := settings.ReadBase(ctx, st, polDir)
	if err != nil {
		t.Fatal(err)
	}
	coord, err := settings.NewRuntimeCoordinator(sst, base, snap, struct{}{},
		func(context.Context, *settings.Snapshot, *settings.Runtime) (any, error) { return struct{}{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	req := settings.SaveRequest{ExpectedGeneration: snap.Generation, Actor: "test", Records: records}
	_, err = coord.SaveAndApply(ctx, req)
	var refused *settings.ConfirmationError
	if errors.As(err, &refused) && refused.Confirmation != nil {
		req.ConfirmationToken = refused.Confirmation.Token
		_, err = coord.SaveAndApply(ctx, req)
	}
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	backups := filepath.Join(dir, "backups")
	n := &nightly{st: st, q: jobs.NewQueue(st), pol: pol, policyNow: backupPolicyNow("db", polDir, dbPath, pol, log),
		log: log, dir: backups, keep: 5, hour: 3}
	if err := n.run(ctx, &jobs.Job{Attempt: 1, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	sets, _ := filepath.Glob(filepath.Join(backups, "theatarr-*.manifest.json"))
	if len(sets) != 1 {
		t.Fatalf("sets %v", sets)
	}
	m, err := readManifest(strings.TrimSuffix(sets[0], ".manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	named := false
	for _, s := range m.Secrets {
		named = named || s.Env == env
	}
	if !named || m.Policy.State != policyAbsent {
		t.Errorf("the nightly manifest does not name the head's secret: %+v", m.Secrets)
	}
	// A head that cannot be loaded falls back to the startup policy.
	if got := backupPolicyNow("bogus", polDir, dbPath, pol, log)(ctx); got != pol {
		t.Errorf("fallback: %p, want the startup policy %p", got, pol)
	}
}

// A set with stored settings restores although a policy file beside them is malformed YAML: the stored head is
// validated over the staged policy directory, where its relative overrides_dir resolves, without reading root YAML.
func TestRestoreValidatesStoredSettingsOverStagedPolicy(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	polDir := importPolicyDir(t, root)
	dbPath := filepath.Join(root, "theatarr.db")
	var out bytes.Buffer
	if err := runSettings([]string{"import", "-policy", polDir, "-db", dbPath, "-confirm"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(polDir, "router.yaml"), []byte("router: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(polDir, "overrides", "franchises.yaml"), []byte("franchises: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dbPath, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	pol, err := databasePolicyBase(polDir)
	if err != nil {
		t.Fatal(err)
	}
	base, _, err := writeBackupSet(ctx, st, pol, filepath.Join(root, "backups"), time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC))
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	// The original directory is gone: only the staged and restored copies hold the overrides.
	if err := os.RemoveAll(polDir); err != nil {
		t.Fatal(err)
	}
	dst, polOut := filepath.Join(root, "restored", "theatarr.db"), filepath.Join(root, "restored", "policy")
	out.Reset()
	if err := restoreBackup(ctx, &out, base, dst, polOut, false, func(string) string { return "" }, time.Now()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !strings.Contains(out.String(), "\n  note     the policy files alone do not validate (router.yaml: yaml: line 1: did not find expected ',' or ']'); "+
		"serve does not read them as settings\n") {
		t.Fatalf("restore output:\n%s", out.String())
	}
	p, _, err := loadSettingsSnapshot(ctx, "db", polOut, dst)
	if err != nil {
		t.Fatal(err)
	}
	if p.Metadata.OverridesDir != "overrides" || p.OverridesPath() != filepath.Join(polOut, "overrides") {
		t.Fatalf("restored overrides_dir %q resolves to %q", p.Metadata.OverridesDir, p.OverridesPath())
	}
}

// A legacy set (policy files, no settings generation) restores with the import of its policy as the next step,
// before serve starts.
func TestRestoreLegacySetNamesSettingsImport(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	polDir := importPolicyDir(t, root)
	if err := os.WriteFile(filepath.Join(polDir, "overrides", "franchises.yaml"), []byte("franchises: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(root, "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(polDir)
	if err != nil {
		t.Fatal(err)
	}
	base, _, err := writeBackupSet(ctx, st, pol, filepath.Join(root, "backups"), time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC))
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	dst, polOut := filepath.Join(root, "restored", "theatarr.db"), filepath.Join(root, "restored", "policy")
	var out bytes.Buffer
	if err := restoreBackup(ctx, &out, base, dst, polOut, false, func(string) string { return "" }, time.Now()); err != nil {
		t.Fatal(err)
	}
	want := "next: the set has no complete settings generation: import its policy before starting serve:\n" +
		"  theatarr settings import -policy " + polOut + " -db " + dst + "   (review the preview, then repeat with -confirm)\n" +
		"  then start theatarr serve with -db " + dst + " -policy " + polOut + " and the environment variables above, wait for /readyz, then\n" +
		"  theatarr library dangling -scan; theatarr library relink -dry-run   (docs/restore.md)\n"
	if !strings.HasSuffix(out.String(), want) {
		t.Fatalf("restore output:\n%s", out.String())
	}
}

// A head without one settings section still loads from the database alone: the set restores as a database set (the
// malformed router.yaml beside it is only a note), with serve as the next step, not an import.
func TestRestorePartialHeadThatLoadsIsADatabaseSet(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	polDir := importPolicyDir(t, root)
	dbPath := filepath.Join(root, "theatarr.db")
	var out bytes.Buffer
	if err := runSettings([]string{"import", "-policy", polDir, "-db", dbPath, "-confirm"}, &out); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dbPath, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().ExecContext(ctx, "DELETE FROM setting_section WHERE key = 'provider_sync'"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(polDir, "router.yaml"), []byte("router: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(polDir, "overrides", "franchises.yaml"), []byte("franchises: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pol, err := databasePolicyBase(polDir)
	if err != nil {
		t.Fatal(err)
	}
	base, _, err := writeBackupSet(ctx, st, pol, filepath.Join(root, "backups"), time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC))
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	dst, polOut := filepath.Join(root, "restored", "theatarr.db"), filepath.Join(root, "restored", "policy")
	out.Reset()
	if err := restoreBackup(ctx, &out, base, dst, polOut, false, func(string) string { return "" }, time.Now()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	want := "  note     the policy files alone do not validate (router.yaml: yaml: line 1: did not find expected ',' or ']'); " +
		"serve does not read them as settings\n" +
		"  secrets  no environment variables named by the settings; " + backupHoldsCredentials + "\n" +
		"next: start theatarr serve with -db " + dst + " -policy " + polOut + " and the environment variables above, wait for /readyz, then\n" +
		"  theatarr library dangling -scan; theatarr library relink -dry-run   (docs/restore.md)\n"
	if !strings.HasSuffix(out.String(), want) {
		t.Fatalf("restore output:\n%s", out.String())
	}
}

// legacyCheckpoint is a migration checkpoint of a generation stored before the settings sections and view identities
// moved into the database: it loads only over its policy files (examples/policy).
func legacyCheckpoint(t *testing.T, dir string) string {
	t.Helper()
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
	c := serveMigrationConfig{st: db, base: base, manager: coord, backupDir: filepath.Join(dir, "backups"),
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), now: func() time.Time { return time.Date(2026, 10, 29, 4, 0, 0, 0, time.UTC) }}
	cp, err := serveMigration(c).Checkpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return cp.Backup
}

// fixtureEnv is the value setFixtureEnv gives a credential the fixtures name.
func fixtureEnv(name string) string { return "fixture-" + name }

// A legacy set whose policy files cannot be imported (its credentials are not set) is refused with the load error:
// naming settings import would send the operator to an import that fails.
func TestRestoreRefusesLegacySetThatCannotImport(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	set := legacyCheckpoint(t, dir)
	dst := filepath.Join(dir, "restored", "theatarr.db")
	var out bytes.Buffer
	t.Setenv("THEATARR_TRASH", fixtureTrash)
	err := restoreBackup(ctx, &out, set, dst, filepath.Join(dir, "restored", "policy"), false, os.Getenv, time.Now())
	const want = "the restored settings (generation 2) do not load with this Theatarr and the restored policy, nothing was restored: " +
		"views[radarr]: view metadata is missing from the policy files; the settings import that would complete them fails: " +
		"settings: import requires a missing credential"
	if err == nil || err.Error() != want {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("database written: %v", err)
	}
}

// fixtureTrash is the TRaSH formats the fixtures' imports need.
const fixtureTrash = "../../internal/settings/testdata/trash"

// setFixtureEnv sets, in the process environment the recovery preview reads, every secret the set names to its
// fixtureEnv value, and THEATARR_TRASH to the fixtures' TRaSH formats.
func setFixtureEnv(t *testing.T, set string) {
	t.Helper()
	m, err := readManifest(setBase(set))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range m.Secrets {
		t.Setenv(s.Env, fixtureEnv(s.Env))
	}
	t.Setenv("THEATARR_TRASH", fixtureTrash)
}

// The restore's -env-file is read for names only, to report which secrets are set. The recovery preview runs with
// the process environment, as the settings import it names will: a name in the file neither shadows the real
// THEATARR_TRASH nor stands in for a credential the process lacks.
func TestRestorePreviewUsesTheProcessEnvironment(t *testing.T) {
	dir := t.TempDir()
	set := legacyCheckpoint(t, dir)
	m, err := readManifest(setBase(set))
	if err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(dir, "theatarr.env")
	text := "THEATARR_TRASH=/elsewhere/trash\n"
	for _, s := range m.Secrets {
		text += s.Env + "=from-the-env-file\n"
	}
	if err := os.WriteFile(envFile, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	restore := func(name string) (string, string, error) {
		var out bytes.Buffer
		dst := filepath.Join(dir, name, "theatarr.db")
		err := runBackup([]string{"restore", "-from", set, "-db", dst, "-policy-out", filepath.Join(dir, name, "policy"),
			"-env-file", envFile}, &out)
		return dst, out.String(), err
	}

	setFixtureEnv(t, set)
	if _, out, err := restore("with-env"); err != nil ||
		!strings.Contains(out, "next: the set has no complete settings generation: import its policy before starting serve:\n") {
		t.Fatalf("restore with the process environment: %v\n%s", err, out)
	}

	for _, s := range m.Secrets {
		t.Setenv(s.Env, "")
	}
	dst, out, err := restore("names-only")
	const want = "the restored settings (generation 2) do not load with this Theatarr and the restored policy, nothing was restored: " +
		"views[radarr]: view metadata is missing from the policy files; the settings import that would complete them fails: " +
		"settings: import requires a missing credential"
	if err == nil || err.Error() != want {
		t.Fatalf("restore with credentials named only in the env file: %v\n%s", err, out)
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("database written: %v", err)
	}
}

// A set of policy files with no settings generation is restored only when the settings import it names previews: a
// required credential missing from the environment refuses it before the database is replaced, even with -force.
func TestRestorePreviewsTheImportOfAPolicyOnlySet(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	polDir := importPolicyDir(t, root)
	if err := os.WriteFile(filepath.Join(polDir, "overrides", "franchises.yaml"), []byte("franchises: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, filepath.Join(root, "theatarr.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	pol, err := databasePolicyBase(polDir)
	if err != nil {
		t.Fatal(err)
	}
	set, m, err := writeBackupSet(ctx, st, pol, filepath.Join(root, "backups"), time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC))
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	if m.Policy.State != policyFiles {
		t.Fatalf("policy state %s", m.Policy.State)
	}
	live := filepath.Join(root, "live", "theatarr.db")
	if err := os.MkdirAll(filepath.Dir(live), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, []byte("the live database"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	t.Setenv("THEATARR_IMPORT_TEST_RD_KEY", "")
	err = restoreBackup(ctx, &out, set, live, filepath.Join(root, "restored", "policy"), true, os.Getenv, time.Now())
	const want = "the restored policy files (no settings generation) do not import with this Theatarr, nothing was restored: " +
		"settings: import requires a missing credential"
	if err == nil || err.Error() != want {
		t.Fatalf("restore without the credential: %v\n%s", err, out.String())
	}
	if b, err := os.ReadFile(live); err != nil || string(b) != "the live database" {
		t.Fatalf("live database: %q %v", b, err)
	}
	if kept, _ := filepath.Glob(live + ".pre-restore-*"); len(kept) != 0 {
		t.Fatalf("the live database was moved aside: %v", kept)
	}

	t.Setenv("THEATARR_IMPORT_TEST_RD_KEY", "rd-key")
	out.Reset()
	if err := restoreBackup(ctx, &out, set, live, filepath.Join(root, "restored", "policy"), true, os.Getenv, time.Now()); err != nil {
		t.Fatalf("restore with the credential: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "next: the set has no complete settings generation: import its policy before starting serve:\n"+
		"  theatarr settings import -policy "+filepath.Join(root, "restored", "policy")+" -db "+live+"   (review the preview, then repeat with -confirm)\n") {
		t.Fatalf("restore output:\n%s", out.String())
	}
}

// A backup archives every directory of the policy directory, empty ones too: a relative overrides_dir that is empty
// at backup time is in the staged copy, so the command-line restore loads the settings over it and restores them.
func TestRestoreKeepsAnEmptyOverridesDirectory(t *testing.T) {
	root := t.TempDir()
	set := databaseSet(t, root, nil)
	var out bytes.Buffer
	dst, polOut := filepath.Join(root, "restored", "theatarr.db"), filepath.Join(root, "restored", "policy")
	if err := restoreBackup(context.Background(), &out, set, dst, polOut, false, os.Getenv, time.Now()); err != nil {
		t.Fatalf("restore: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "next: start theatarr serve with -db "+dst+" -policy "+polOut+" and the environment variables above") {
		t.Fatalf("restore output:\n%s", out.String())
	}
	if info, err := os.Stat(filepath.Join(polOut, "overrides")); err != nil || !info.IsDir() {
		t.Fatalf("restored overrides directory: %v", err)
	}
}

// An archive written before archives kept empty directories has no entry for an empty overrides directory. The
// command-line restore of such a set creates the directory the stored settings name, empty, in the staged and the
// restored policy, and restores the settings.
func TestRestoreRecreatesAnOverridesDirectoryMissingFromAnOlderArchive(t *testing.T) {
	root := t.TempDir()
	set := databaseSet(t, root, func(polDir string) {
		if err := os.Remove(filepath.Join(polDir, "overrides")); err != nil {
			t.Fatal(err)
		}
	})
	var out bytes.Buffer
	dst, polOut := filepath.Join(root, "restored", "theatarr.db"), filepath.Join(root, "restored", "policy")
	if err := restoreBackup(context.Background(), &out, set, dst, polOut, false, os.Getenv, time.Now()); err != nil {
		t.Fatalf("restore: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "next: start theatarr serve with -db "+root+"/restored/theatarr.db -policy "+root+
		"/restored/policy and the environment variables above") {
		t.Fatalf("restore output:\n%s", out.String())
	}
	entries, err := os.ReadDir(root + "/restored/policy/overrides")
	if err != nil || len(entries) != 0 {
		t.Fatalf("restored overrides directory: %v %v", entries, err)
	}
}

// absoluteOverridesSet backs up a complete settings generation whose metadata.overrides_dir is the absolute path
// overrides (created for the import), and returns the set and the policy directory it was taken from.
func absoluteOverridesSet(t *testing.T, root string, overrides func(polDir string) string) (set, polDir string) {
	t.Helper()
	ctx := context.Background()
	polDir = importPolicyDir(t, root)
	abs := overrides(polDir)
	if err := os.MkdirAll(abs, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(polDir, "policy.yaml")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(b), "overrides_dir: overrides\n", "overrides_dir: "+abs+"\n", 1)
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "theatarr.db")
	var out bytes.Buffer
	if err := runSettings([]string{"import", "-policy", polDir, "-db", dbPath, "-confirm"}, &out); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dbPath, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pol, err := databasePolicyBase(polDir)
	if err != nil {
		t.Fatal(err)
	}
	set, _, err = writeBackupSet(ctx, st, pol, filepath.Join(root, "backups"), time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return set, polDir
}

// A stored absolute overrides_dir inside the policy directory the backup was taken from (/config/policy/overrides)
// is checked under the staged copy: the command-line restore succeeds where that path does not exist (the runbook's
// temporary container), and the stored value is left as it was. The next step installs the restored policy at that
// directory and starts serve with it, where the stored settings load as serve loads them.
func TestRestoreChecksAnAbsoluteOverridesPathInsideThePolicyUnderTheStagedCopy(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	set, polDir := absoluteOverridesSet(t, root, func(polDir string) string { return filepath.Join(polDir, "overrides") })
	if err := os.RemoveAll(polDir); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	dst, polOut := filepath.Join(root, "restored", "theatarr.db"), filepath.Join(root, "restored", "policy")
	if err := restoreBackup(ctx, &out, set, dst, polOut, false, os.Getenv, time.Now()); err != nil {
		t.Fatalf("restore: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "next: install the restored policy "+root+"/restored/policy at "+root+
		"/policy, the directory its stored metadata.overrides_dir ("+root+"/policy/overrides) is in (docs/restore.md "+
		"step 4), then\n  start theatarr serve with -db "+root+"/restored/theatarr.db -policy "+root+"/policy and the environment "+
		"variables above") {
		t.Fatalf("restore output:\n%s", out.String())
	}
	st, err := store.Open(ctx, dst, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var config string
	if err := st.Reader().QueryRowContext(ctx, `SELECT config FROM setting_section WHERE key = 'metadata'`).Scan(&config); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(config, `"overrides_dir":"`+root+`/policy/overrides"`) {
		t.Fatalf("stored metadata: %s", config)
	}
	// The next step: the restored policy installed at the source directory, the stored settings load over it as serve
	// loads them (-db, -policy), and the overrides resolve there.
	if err := os.CopyFS(root+"/policy", os.DirFS(polOut)); err != nil {
		t.Fatal(err)
	}
	fset := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags := addServeSettingsFlags(fset)
	if err := fset.Parse([]string{"-policy", root + "/policy"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := flags.load(ctx, st)
	if err != nil {
		t.Fatalf("settings over the installed policy: %v", err)
	}
	if got := loaded.policy.OverridesPath(); got != root+"/policy/overrides" {
		t.Fatalf("overrides resolve under %s", got)
	}
}

// An absolute overrides_dir outside the policy directory the backup was taken from is checked where it is: missing
// where the restore runs, the restore is refused.
func TestRestoreRefusesAMissingAbsoluteOverridesPathOutsideThePolicy(t *testing.T) {
	root := t.TempDir()
	set, _ := absoluteOverridesSet(t, root, func(string) string { return filepath.Join(root, "elsewhere", "overrides") })
	if err := os.RemoveAll(filepath.Join(root, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	dst, polOut := filepath.Join(root, "restored", "theatarr.db"), filepath.Join(root, "restored", "policy")
	err := restoreBackup(context.Background(), &out, set, dst, polOut, false, os.Getenv, time.Now())
	want := "the restored settings (generation 1) do not load with this Theatarr and the restored policy, nothing was " +
		"restored: metadata.overrides_dir: stat " + root + "/elsewhere/overrides: no such file or directory; the settings " +
		"import that would complete them fails: this database already has settings: import from Advanced › YAML " +
		"settings › Import & export in the running service"
	if err == nil || err.Error() != want {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(dst); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("refused restore wrote the database: %v", err)
	}
}
