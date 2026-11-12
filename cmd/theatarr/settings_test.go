package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

func TestSettingsImport(t *testing.T) {
	dir := importPolicyDir(t, t.TempDir())
	path := filepath.Join(t.TempDir(), "settings.db")
	file := filepath.Join(dir, "policy.yaml")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	var out bytes.Buffer
	if err := runSettings([]string{"import", "-policy", dir, "-db", path, "-confirm"}, &out); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db, err := store.Open(ctx, path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st, err := settings.New(db)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := st.Load(ctx, &policy.Policy{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Generation != 1 || len(snap.Records.Sections) != 13 || time.Duration(snap.Policy.ProviderSync.Interval).String() != "8h0m0s" || snap.Policy.Router.Enabled {
		t.Fatalf("unexpected import: %+v", snap)
	}
	if snap.Policy.Metadata.OverridesDir != "overrides" {
		t.Fatalf("override path: %s", snap.Policy.Metadata.OverridesDir)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	base, err := databasePolicyBase(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, running, pol, err := loadServePolicy(ctx, "db", dir, base, db)
	if err != nil || running.Generation != 1 || pol.Router.Enabled || time.Duration(pol.ProviderSync.Interval) != 8*time.Hour {
		t.Fatalf("serve without root YAML: %v", err)
	}
	if err := os.WriteFile(file, []byte(strings.Replace(text, "8h", "bad", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runSettings([]string{"import", "-policy", dir, "-db", path, "-confirm"}, &out); err == nil || !strings.Contains(err.Error(), `time: invalid duration "bad"`) {
		t.Fatalf("invalid import: %v", err)
	}
	snap, err = st.Load(ctx, &policy.Policy{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Generation != 1 || time.Duration(snap.Policy.ProviderSync.Interval).String() != "8h0m0s" {
		t.Fatalf("invalid import changed database: %+v", snap)
	}
}

// A recovery import whose files omit the clients section its version names uses the stored section: the files are
// only parsed before the merge. Files that parse but whose merged settings do not validate are refused at the field
// and leave the database as it was.
func TestSettingsImportValidatesMergedSettings(t *testing.T) {
	ctx := context.Background()
	dir := importPolicyDir(t, t.TempDir())
	path := filepath.Join(t.TempDir(), "settings.db")
	var out bytes.Buffer
	if err := runSettings([]string{"import", "-policy", dir, "-db", path, "-confirm"}, &out); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Writer().ExecContext(ctx, "DELETE FROM setting_section WHERE key = 'provider_sync'"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "policy.yaml")
	edit := func(from, to string) {
		t.Helper()
		b, err := os.ReadFile(file)
		if err != nil || !strings.Contains(string(b), from) {
			t.Fatalf("policy has no %q: %v", from, err)
		}
		if err := os.WriteFile(file, []byte(strings.Replace(string(b), from, to, 1)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	edit("clients:\n  decypharr: {kind: decypharr, url: \"http://decypharr:8282\", category: theatarr, callback_view: radarr}\n", "")
	edit("client: decypharr", "client: elsewhere")
	const want = "versions[movie].client: invalid policy value"
	if err := runSettings([]string{"import", "-policy", dir, "-db", path, "-confirm"}, &out); err == nil || err.Error() != want {
		t.Fatalf("unknown client: %v", err)
	}
	var gen int64
	if err := db.Reader().QueryRowContext(ctx, "SELECT MAX(generation) FROM settings_generation").Scan(&gen); err != nil || gen != 1 {
		t.Fatalf("refused import: generation %d %v", gen, err)
	}
	edit("client: elsewhere", "client: decypharr")
	out.Reset()
	if err := runSettings([]string{"import", "-policy", dir, "-db", path, "-confirm"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Imported settings generation 2\n") {
		t.Fatalf("import output: %s", out.String())
	}
}

// A first import into an empty database may leave a record group out (nothing stored can be lost): a policy with
// no views seeds generation 1.
func TestFirstSettingsImportWithoutViews(t *testing.T) {
	dir := importPolicyDir(t, t.TempDir())
	file := filepath.Join(dir, "policy.yaml")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, cut := range []string{
		"views:\n  radarr:\n    flavour: radarr\n    path: /radarr\n    api_key_env: THEATARR_IMPORT_TEST_VIEW_KEY\n    versions: [movie]\n",
		"clients:\n  decypharr: {kind: decypharr, url: \"http://decypharr:8282\", category: theatarr, callback_view: radarr}\n",
		"    client: decypharr\n",
	} {
		if !strings.Contains(text, cut) {
			t.Fatalf("policy has no %q", cut)
		}
		text = strings.Replace(text, cut, "", 1)
	}
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "settings.db")
	var out bytes.Buffer
	if err := runSettings([]string{"import", "-policy", dir, "-db", path, "-confirm"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Imported settings generation 1\n") {
		t.Fatalf("import output: %s", out.String())
	}
}

func TestCommandsRequireSettingsAndRejectYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.db")
	_, err := loadSettingsPolicy(context.Background(), "db", t.TempDir(), path)
	if err == nil || err.Error() != "no settings yet: run Setup or theatarr settings import" {
		t.Fatalf("empty command: %v", err)
	}
	_, err = loadSettingsPolicy(context.Background(), "yaml", t.TempDir(), path)
	if err == nil || err.Error() != "the YAML settings source is no longer read; run theatarr settings import" {
		t.Fatalf("removed source: %v", err)
	}
}

func TestRemovedYAMLSourceRefusesServe(t *testing.T) {
	const want = "the YAML settings source is no longer read; run theatarr settings import"
	t.Setenv("THEATARR_SETTINGS_SOURCE", "yaml")
	if err := runServe(nil); err == nil || err.Error() != want {
		t.Fatalf("serve: %v", err)
	}
	if err := runServe([]string{"--settings-source", "yaml"}); err == nil || err.Error() != want {
		t.Fatalf("flag: %v", err)
	}
}

func TestInvalidInitialImportLeavesDatabaseAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(t.TempDir(), "settings.db")
	if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte("provider_sync:\n  interval: broken\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runSettings([]string{"import", "-policy", dir, "-db", path, "-confirm"}, &out); err == nil || !strings.Contains(err.Error(), `invalid duration "broken"`) {
		t.Fatalf("import: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("database changed: %v", err)
	}
}

func TestSettingsImportCompletesLegacyGeneration(t *testing.T) {
	ctx := context.Background()
	dir := importPolicyDir(t, t.TempDir())
	path := filepath.Join(t.TempDir(), "settings.db")
	base, err := policy.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := settings.New(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.Bootstrap(ctx, base, settings.ImportRequest{Dir: dir, Actor: "legacy", Lookup: os.LookupEnv}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	var out bytes.Buffer
	if err := runSettings([]string{"import", "-policy", dir, "-db", path, "-confirm"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "policy.yaml")); err != nil {
		t.Fatal(err)
	}
	p, snap, err := loadSettingsSnapshot(ctx, "db", dir, path)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Generation != 2 || len(snap.Records.Sections) != 13 || time.Duration(p.ProviderSync.Interval) != 8*time.Hour {
		t.Fatalf("legacy import: generation=%d sections=%d policy=%+v", snap.Generation, len(snap.Records.Sections), p.ProviderSync)
	}
}

// A fresh database is seeded by the CLI: the preview passes, and -confirm writes generation 1 with the version.
func TestSettingsImportSeedsFreshDatabase(t *testing.T) {
	root := t.TempDir()
	dir := importPolicyDir(t, root)
	path := filepath.Join(t.TempDir(), "settings.db")
	var out bytes.Buffer
	if err := runSettings([]string{"import", "-policy", dir, "-db", path}, &out); err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !strings.HasSuffix(out.String(), "Review the preview, then repeat with -confirm to import.\n") {
		t.Fatalf("preview output: %s", out.String())
	}
	out.Reset()
	if err := runSettings([]string{"import", "-policy", dir, "-db", path, "-confirm"}, &out); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !strings.HasSuffix(out.String(), "Imported settings generation 1\n") {
		t.Fatalf("confirm output: %s", out.String())
	}
	p, snap, err := loadSettingsSnapshot(context.Background(), "db", dir, path)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Generation != 1 || p.Versions["movie"] == nil || p.Versions["movie"].Library != filepath.Join(root, "media", "movies") {
		t.Fatalf("seeded: generation %d versions %v", snap.Generation, p.Versions)
	}
}

// A generation without the provider_sync section gets it from the files; the preview says so.
func TestSettingsImportFillsMissingSection(t *testing.T) {
	ctx := context.Background()
	dir := importPolicyDir(t, t.TempDir())
	path := filepath.Join(t.TempDir(), "settings.db")
	var out bytes.Buffer
	if err := runSettings([]string{"import", "-policy", dir, "-db", path, "-confirm"}, &out); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().ExecContext(ctx, "DELETE FROM setting_section WHERE key = 'provider_sync'"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	out.Reset()
	if err := runSettings([]string{"import", "-policy", dir, "-db", path}, &out); err != nil {
		t.Fatalf("preview: %v", err)
	}
	var preview struct {
		YAML              string   `json:"yaml"`
		SectionsFromFiles []string `json:"sections_from_files"`
	}
	if err := json.NewDecoder(strings.NewReader(out.String())).Decode(&preview); err != nil {
		t.Fatal(err)
	}
	if strings.Join(preview.SectionsFromFiles, ",") != "provider_sync" || !strings.Contains(preview.YAML, "provider_sync:\n    interval: 8h0m0s\n") {
		t.Fatalf("preview: sections %v\n%s", preview.SectionsFromFiles, preview.YAML)
	}
	if !strings.Contains(out.String(), "\nSettings sections are kept as stored; filled from the files: provider_sync.\n") {
		t.Fatalf("preview output: %s", out.String())
	}
}

// The CLI seeds and recovers with the service stopped: a database whose generation has every section is refused
// and left unchanged.
func TestSettingsImportRefusesInitializedDatabase(t *testing.T) {
	dir := importPolicyDir(t, t.TempDir())
	path := filepath.Join(t.TempDir(), "settings.db")
	var out bytes.Buffer
	if err := runSettings([]string{"import", "-policy", dir, "-db", path, "-confirm"}, &out); err != nil {
		t.Fatal(err)
	}
	const want = "this database already has settings: import from Advanced › YAML settings › Import & export in the running service"
	for _, args := range [][]string{{"import", "-policy", dir, "-db", path}, {"import", "-policy", dir, "-db", path, "-confirm"}} {
		out.Reset()
		if err := runSettings(args, &out); err == nil || err.Error() != want {
			t.Fatalf("%v: %v", args, err)
		}
		if out.Len() != 0 {
			t.Fatalf("%v printed %s", args, out.String())
		}
	}
	_, snap, err := loadSettingsSnapshot(context.Background(), "db", dir, path)
	if err != nil || snap.Generation != 1 {
		t.Fatalf("refused import changed the database: %v", err)
	}
}

// A relative overrides_dir is stored as written and resolves under the policy directory the commands are given
// (THEATARR_POLICY), not a fixed /config/policy.
func TestStoredRelativeOverridesResolveUnderPolicyDirectory(t *testing.T) {
	dir := importPolicyDir(t, t.TempDir())
	path := filepath.Join(t.TempDir(), "settings.db")
	var out bytes.Buffer
	if err := runSettings([]string{"import", "-policy", dir, "-db", path, "-confirm"}, &out); err != nil {
		t.Fatal(err)
	}
	t.Setenv("THEATARR_POLICY", dir)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("db", path, "")
	p, snap, err := loadCommandSettings(fs, policyDirDefault())
	if err != nil {
		t.Fatal(err)
	}
	if p.Metadata.OverridesDir != "overrides" || p.OverridesPath() != filepath.Join(dir, "overrides") {
		t.Fatalf("overrides_dir %q resolves to %q", p.Metadata.OverridesDir, p.OverridesPath())
	}
	for _, section := range snap.Records.Sections {
		if section.Key == "metadata" && !strings.Contains(string(section.Config), `"overrides_dir":"overrides"`) {
			t.Fatalf("stored metadata: %s", section.Config)
		}
	}
	out.Reset()
	if err := runLibrary([]string{"dangling", "-db", path, "-json"}, &out); err != nil {
		t.Fatalf("command under THEATARR_POLICY: %v", err)
	}
}

// importPolicyDir writes a complete policy directory under root: every record group, one shadow movie version
// whose root (root/media/movies) exists and whose client is decypharr, provider_sync at 8h and router.enabled true.
// Its credentials are read from the environment it sets.
func importPolicyDir(t *testing.T, root string) string {
	t.Helper()
	t.Setenv("THEATARR_IMPORT_TEST_RD_KEY", "rd-key")
	t.Setenv("THEATARR_IMPORT_TEST_VIEW_KEY", "view-key")
	dir := filepath.Join(root, "policy")
	for _, d := range []string{filepath.Join(dir, "overrides"), filepath.Join(root, "media", "movies")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	text := strings.ReplaceAll(`metadata:
  overrides_dir: overrides
indexers:
  feed: {kind: torznab, url: "http://prowlarr:9696/1/api", privacy: public, budget: feed}
providers:
  rd: {kind: realdebrid, order: 1, url: "https://api.real-debrid.com/rest/1.0", budget: rd, api_key_env: THEATARR_IMPORT_TEST_RD_KEY}
budgets:
  feed: {requests: 10, per: 1m}
  rd: {requests: 10, per: 1m}
profiles:
  web:
    qualities: [WEBDL-1080p]
    cutoff: WEBDL-1080p
versions:
  movie:
    mode: shadow
    kind: movie
    profile: web
    library: ROOT/media/movies
    storage: debrid
    indexers: [feed]
    providers: [rd]
    client: decypharr
views:
  radarr:
    flavour: radarr
    path: /radarr
    api_key_env: THEATARR_IMPORT_TEST_VIEW_KEY
    versions: [movie]
clients:
  decypharr: {kind: decypharr, url: "http://decypharr:8282", category: theatarr, callback_view: radarr}
library:
  dangling: {roots: [ROOT/media]}
  versions:
    movie: {roots: [ROOT/media/movies]}
provider_sync:
  interval: 8h
router:
  enabled: true
`, "ROOT", root)
	if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Serve loads the stored settings over its -policy directory: a relative overrides_dir resolves there, not under a
// fixed /config/policy or the directory the settings were imported from.
func TestServeLoadsSettingsOverItsPolicyFlag(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	polDir := importPolicyDir(t, root)
	dbPath := filepath.Join(root, "theatarr.db")
	var out bytes.Buffer
	if err := runSettings([]string{"import", "-policy", polDir, "-db", dbPath, "-confirm"}, &out); err != nil {
		t.Fatal(err)
	}
	served := filepath.Join(root, "served")
	if err := os.MkdirAll(filepath.Join(served, "overrides"), 0o700); err != nil {
		t.Fatal(err)
	}
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags := addServeSettingsFlags(fs)
	if err := fs.Parse([]string{"-policy", served}); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dbPath, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// runServe's own setup (serveSettingsFlags.load, then the startup runtime and its coordinator), without listening.
	loaded, err := flags.load(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.policy.OverridesPath(); got != root+"/served/overrides" {
		t.Fatalf("overrides resolve under %s", got)
	}
	prepare := func(context.Context, *settings.Snapshot) (any, error) { return nil, nil }
	initial, components, _, err := loaded.prepareStartup(ctx, loaded.snapshot, slog.New(slog.NewTextHandler(io.Discard, nil)), prepare)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := loaded.coordinator(initial, components, func(context.Context, *settings.Snapshot, *settings.Runtime) (any, error) {
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The coordinator's loads (reloads, saves, the admin pages) resolve under the same directory.
	snap, err := rc.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Policy.OverridesPath(); got != root+"/served/overrides" {
		t.Fatalf("the coordinator's settings resolve under %s", got)
	}
}
