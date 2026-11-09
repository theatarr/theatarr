package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

// importPolicyText is a complete policy directory's single file: every record group, one shadow movie version
// whose root (ROOT/media/movies) exists and whose client is decypharr, provider_sync at 8h and router.enabled true.
const importPolicyText = `metadata:
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
`

// newImportEnv is a settings env as serve runs it: the coordinator's base is the policy directory alone (no root
// YAML), and the Import & export handler imports that directory, which holds importPolicyText. seed, when set,
// stores settings before the coordinator starts.
func newImportEnv(t *testing.T, seed func(ctx context.Context, st *settings.Store, db *store.Store, dir string)) *settingsEnv {
	t.Helper()
	se := &settingsEnv{tester: &fakeTester{}, logs: &syncBuffer{}}
	root := t.TempDir()
	se.dir = filepath.Join(root, "policy")
	for _, d := range []string{filepath.Join(se.dir, "overrides"), filepath.Join(root, "media", "movies")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(se.dir, "policy.yaml"), []byte(strings.ReplaceAll(importPolicyText, "ROOT", root)), 0o600); err != nil {
		t.Fatal(err)
	}
	lookup := func(name string) (string, bool) { return "fixture-" + name, true }
	se.env = newEnvWith(t, func(c *Config) {
		ctx := context.Background()
		st, err := settings.New(c.Store)
		if err != nil {
			t.Fatal(err)
		}
		st.SetLookup(lookup)
		if seed != nil {
			seed(ctx, st, c.Store, se.dir)
		}
		base := &policy.Policy{Dir: se.dir}
		snap, err := st.Load(ctx, base)
		if err != nil {
			t.Fatal(err)
		}
		se.coord, err = settings.NewRuntimeCoordinator(st, base, snap, fakeRuntime{},
			func(context.Context, *settings.Snapshot, *settings.Runtime) (any, error) { return fakeRuntime{}, nil })
		if err != nil {
			t.Fatal(err)
		}
		c.Settings, c.SettingsLookup, c.PolicyDir = se.coord, lookup, se.dir
	})
	se.cookie, se.csrf = se.session(1)
	return se
}

// confirmedImport confirms the import's effects (the page's confirmation step), then imports over generation gen.
func (se *settingsEnv) confirmedImport(gen int64) map[string]any {
	se.t.Helper()
	body := map[string]any{"generation": gen, "orphan_active_downloads": false}
	proof := se.want(se.call("POST", "/import/confirm", body), http.StatusOK, "confirm import")
	if token, ok := proof["token"]; ok {
		body["confirmation_token"] = token
	}
	return se.want(se.call("POST", "/import", body), http.StatusOK, "import")
}

// importPreviewBody is the parts of an import preview these tests read.
type importPreviewBody struct {
	Generation int64 `json:"generation"`
}

// A fresh database is seeded through Import & export: preview, then import, gives generation 1 with the version.
func TestSettingsImportSeedsFreshDatabase(t *testing.T) {
	t.Parallel()
	se := newImportEnv(t, nil)
	if gen := se.generation(); gen != 0 {
		t.Fatalf("fresh generation %d", gen)
	}
	rec := se.call("POST", "/import/preview", map[string]any{"generation": 0})
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body)
	}
	var preview importPreviewBody
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil || preview.Generation != 0 {
		t.Fatalf("preview body: %v %s", err, rec.Body)
	}
	out := se.confirmedImport(0)
	if out["generation"] != float64(1) {
		t.Fatalf("import: %v", out)
	}
	cur := se.coord.Current().Policy
	if cur.Versions["movie"] == nil || cur.Versions["movie"].Client != "decypharr" || cur.Router.Enabled {
		t.Fatalf("imported policy: versions %v router %v", cur.Versions, cur.Router.Enabled)
	}
}

// seedImport stores the policy directory's import as generation 1, as a first import from Import & export does.
func seedImport(t *testing.T) func(ctx context.Context, st *settings.Store, db *store.Store, dir string) {
	return func(ctx context.Context, st *settings.Store, _ *store.Store, dir string) {
		t.Helper()
		lookup := func(name string) (string, bool) { return "fixture-" + name, true }
		coord := seedCoordinator(t, st, dir)
		req := settings.ImportRequest{Dir: dir, Actor: "seed", Lookup: lookup}
		approval, err := coord.ConfirmImport(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		req.ConfirmationToken = approval.Token
		if _, err := coord.ImportAndApply(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
}

// seedCoordinator is a coordinator over the stored settings for a fixture's seed writes.
func seedCoordinator(t *testing.T, st *settings.Store, dir string) *settings.RuntimeCoordinator {
	t.Helper()
	base := &policy.Policy{Dir: dir}
	snap, err := st.Load(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	coord, err := settings.NewRuntimeCoordinator(st, base, snap, fakeRuntime{},
		func(context.Context, *settings.Snapshot, *settings.Runtime) (any, error) { return fakeRuntime{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	return coord
}

// editImportPolicy replaces from with to in the policy directory's file.
func (se *settingsEnv) editImportPolicy(from, to string) {
	se.t.Helper()
	path := filepath.Join(se.dir, "policy.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		se.t.Fatal(err)
	}
	if !strings.Contains(string(b), from) {
		se.t.Fatalf("policy has no %q", from)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(b), from, to, 1)), 0o600); err != nil {
		se.t.Fatal(err)
	}
}

// Importing the same directory again over stored settings whose versions name a client previews and imports; the
// router switch stored off stays off although the file says enabled: true.
func TestSettingsImportOverStoredSettings(t *testing.T) {
	t.Parallel()
	se := newImportEnv(t, seedImport(t))
	if gen := se.generation(); gen != 1 {
		t.Fatalf("seeded generation %d", gen)
	}
	rec := se.call("POST", "/import/preview", map[string]any{"generation": 1})
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body)
	}
	var preview struct {
		SectionsNote      string   `json:"sections_note"`
		SectionsFromFiles []string `json:"sections_from_files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.SectionsNote != "Settings sections are kept as stored; none is filled from the files." || len(preview.SectionsFromFiles) != 0 {
		t.Fatalf("preview sections: %q %v", preview.SectionsNote, preview.SectionsFromFiles)
	}
	if out := se.confirmedImport(1); out["generation"] != float64(2) {
		t.Fatalf("import: %v", out)
	}
	cur := se.coord.Current().Policy
	if cur.Versions["movie"].Client != "decypharr" || cur.Router.Enabled {
		t.Fatalf("after import: client %q router %v", cur.Versions["movie"].Client, cur.Router.Enabled)
	}
}

// A generation without the provider_sync section gets it from the files; the preview says so.
func TestSettingsImportFillsMissingSection(t *testing.T) {
	t.Parallel()
	se := newImportEnv(t, func(ctx context.Context, st *settings.Store, db *store.Store, dir string) {
		seedImport(t)(ctx, st, db, dir)
		if _, err := db.Writer().ExecContext(ctx, "DELETE FROM setting_section WHERE key = 'provider_sync'"); err != nil {
			t.Fatal(err)
		}
	})
	if p := se.coord.Current().Policy; p.ProviderSync != nil {
		t.Fatalf("fixture still has provider_sync: %+v", p.ProviderSync)
	}
	rec := se.call("POST", "/import/preview", map[string]any{"generation": 1})
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body)
	}
	var preview struct {
		YAML              string   `json:"yaml"`
		SectionsNote      string   `json:"sections_note"`
		SectionsFromFiles []string `json:"sections_from_files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.SectionsNote != "Settings sections are kept as stored; filled from the files: provider_sync." ||
		strings.Join(preview.SectionsFromFiles, ",") != "provider_sync" {
		t.Fatalf("preview sections: %q %v", preview.SectionsNote, preview.SectionsFromFiles)
	}
	if !strings.Contains(preview.YAML, "provider_sync:\n    interval: 8h0m0s\n") {
		t.Fatalf("preview yaml lacks provider_sync 8h:\n%s", preview.YAML)
	}
	se.confirmedImport(1)
	if got := time.Duration(se.coord.Current().Policy.ProviderSync.Interval); got != 8*time.Hour {
		t.Fatalf("provider_sync interval %s", got)
	}
}

// A stored section that differs from the files stays as stored, and so do the switches: router on stays on when
// the file says off.
func TestSettingsImportKeepsStoredSectionsAndSwitches(t *testing.T) {
	t.Parallel()
	se := newImportEnv(t, func(ctx context.Context, st *settings.Store, db *store.Store, dir string) {
		seedImport(t)(ctx, st, db, dir)
		coord := seedCoordinator(t, st, dir)
		snap, err := coord.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		records := snap.Records
		records.Switches = &settings.GlobalSwitches{RouterEnabled: true}
		req := settings.SaveRequest{ExpectedGeneration: snap.Generation, Actor: "seed", Records: records}
		approval, err := coord.Confirm(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		req.ConfirmationToken = approval.Token
		if _, err := coord.SaveAndApply(ctx, req); err != nil {
			t.Fatal(err)
		}
	})
	if !se.coord.Current().Policy.Router.Enabled {
		t.Fatal("fixture router is off")
	}
	se.editImportPolicy("interval: 8h", "interval: 12h")
	se.editImportPolicy("enabled: true", "enabled: false")
	se.confirmedImport(2)
	cur := se.coord.Current().Policy
	if got := time.Duration(cur.ProviderSync.Interval); got != 8*time.Hour || !cur.Router.Enabled {
		t.Fatalf("after import: provider_sync %s router %v", got, cur.Router.Enabled)
	}
}

// Importing the stored settings' files again with a default left out (the version's mode: shadow) changes nothing:
// the imported records carry the defaults the stored ones were saved with.
func TestSettingsReimportWithDefaultLeftOutChangesNothing(t *testing.T) {
	t.Parallel()
	se := newImportEnv(t, seedImport(t))
	se.editImportPolicy("    mode: shadow\n", "")
	out := se.want(se.call("POST", "/import/preview", map[string]any{"generation": 1}), http.StatusOK, "preview")
	if changes, _ := json.Marshal(out["changes"]); string(changes) != "[]" {
		t.Fatalf("re-import changes: %s", changes)
	}
}

// Import files without a clients section whose version names the stored client decypharr preview: the stored
// section is what the merged settings use. A version naming a client neither has is refused at its field.
func TestSettingsImportUsesStoredSectionsItsFilesOmit(t *testing.T) {
	t.Parallel()
	se := newImportEnv(t, seedImport(t))
	se.editImportPolicy("clients:\n  decypharr: {kind: decypharr, url: \"http://decypharr:8282\", category: theatarr, callback_view: radarr}\n", "")
	se.want(se.call("POST", "/import/preview", map[string]any{"generation": 1}), http.StatusOK, "preview")
	se.editImportPolicy("client: decypharr", "client: elsewhere")
	rec := se.call("POST", "/import/preview", map[string]any{"generation": 1})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown client: %d %s", rec.Code, rec.Body)
	}
	var refused struct {
		Fields []struct {
			Path    string `json:"path"`
			Message string `json:"message"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &refused); err != nil {
		t.Fatal(err)
	}
	if len(refused.Fields) != 1 || refused.Fields[0].Path != "versions[movie].client" {
		t.Fatalf("refusal: %s", rec.Body)
	}
	if gen := se.generation(); gen != 1 {
		t.Fatalf("refused import changed the generation to %d", gen)
	}
}

// storedSections is the keys of the head generation's settings sections.
func (se *settingsEnv) storedSections() []string {
	se.t.Helper()
	snap, err := se.coord.Snapshot(context.Background())
	if err != nil {
		se.t.Fatal(err)
	}
	keys := []string{}
	for _, s := range snap.Records.Sections {
		keys = append(keys, s.Key)
	}
	return keys
}

const allSections = "cardigann,clients,egress,import,library,metadata,notify,playlists,provider_sync,router,shadow,torrents,ui"

// The first save on a fresh database, a section's or a record's, creates generation 1 with every settings section:
// the saved one as sent, the others at their defaults.
func TestFirstSaveSeedsEverySection(t *testing.T) {
	t.Parallel()
	se := newImportEnv(t, nil)
	se.want(se.call("PUT", "/sections/provider_sync", map[string]any{"generation": 0, "config": map[string]any{"interval": "6h"}}),
		http.StatusOK, "first section save")
	if got := strings.Join(se.storedSections(), ","); got != allSections {
		t.Fatalf("sections after a section save: %s", got)
	}
	if got := time.Duration(se.coord.Current().Policy.ProviderSync.Interval); got != 6*time.Hour || se.generation() != 1 {
		t.Fatalf("provider_sync %s, generation %d", got, se.generation())
	}

	se = newImportEnv(t, nil)
	se.want(se.call("POST", "/budgets", map[string]any{"generation": 0, "key": "extra", "config": map[string]any{"requests": 10, "per": "1m"}}),
		http.StatusOK, "first record save")
	if got := strings.Join(se.storedSections(), ","); got != allSections || se.generation() != 1 {
		t.Fatalf("sections after a record save: %s", got)
	}
}

// A generation an older Setup wrote without section rows still serves on the sections' defaults; a section save over
// it saves that section over its default and stores every other section in the generation it creates.
func TestSectionSaveCompletesAnOldHeadWithoutSections(t *testing.T) {
	t.Parallel()
	se := newImportEnv(t, func(ctx context.Context, st *settings.Store, db *store.Store, dir string) {
		// Setup's first save: one budget. The older code stored no section with it.
		if _, err := st.Save(ctx, settings.SaveRequest{Actor: "setup", Base: &policy.Policy{Dir: dir}, Records: settings.Records{
			Budgets: []settings.Budget{{Record: settings.Record{Key: "extra"}, Config: policy.Budget{Requests: 10, Per: policy.Duration(time.Minute)}}}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Writer().ExecContext(ctx, "DELETE FROM setting_section"); err != nil {
			t.Fatal(err)
		}
	})
	if got := strings.Join(se.storedSections(), ","); got != "" || se.generation() != 1 {
		t.Fatalf("fixture: sections %q, generation %d", got, se.generation())
	}
	se.want(se.call("PUT", "/sections/provider_sync", map[string]any{"generation": 1, "config": map[string]any{"interval": "6h"}}),
		http.StatusOK, "section save over an old head")
	if got := strings.Join(se.storedSections(), ","); got != allSections || se.generation() != 2 {
		t.Fatalf("sections after the save: %q, generation %d", got, se.generation())
	}
	if got := time.Duration(se.coord.Current().Policy.ProviderSync.Interval); got != 6*time.Hour {
		t.Fatalf("provider_sync %s", got)
	}
}

// An import over a generation 1 that Setup created keeps its (default) sections, the library area and clients
// among them, and fills none from the files: a version naming the files' client is refused, and without it the
// version's root is admitted through the version's own library roots.
func TestImportOverSetupGenerationKeepsDefaultSections(t *testing.T) {
	t.Parallel()
	se := newImportEnv(t, nil)
	se.want(se.call("POST", "/budgets", map[string]any{"generation": 0, "key": "extra", "config": map[string]any{"requests": 10, "per": "1m"}}),
		http.StatusOK, "setup save")
	refused := se.want(se.call("POST", "/import/preview", map[string]any{"generation": 1}), http.StatusUnprocessableEntity, "client only in the files")
	if refused["error"] != "settings refused: versions[movie].client: invalid policy value" {
		t.Fatalf("refusal: %v", refused["error"])
	}
	se.editImportPolicy("    client: decypharr\n", "")
	out := se.want(se.call("POST", "/import/preview", map[string]any{"generation": 1}), http.StatusOK, "preview")
	if out["sections_note"] != "Settings sections are kept as stored; none is filled from the files." {
		t.Fatalf("sections note: %v", out["sections_note"])
	}
	if out := se.confirmedImport(1); out["generation"] != float64(2) {
		t.Fatalf("import: %v", out)
	}
	cur := se.coord.Current().Policy
	if cur.Versions["movie"] == nil || cur.ProviderSync != nil || len(cur.Library.Dangling.Roots) != 0 ||
		len(cur.Library.Versions["movie"].Roots) != 1 || cur.Library.Versions["movie"].Roots[0] != filepath.Join(filepath.Dir(se.dir), "media", "movies") {
		t.Fatalf("after import: versions %v provider_sync %+v library %+v", cur.Versions, cur.ProviderSync, cur.Library)
	}
}
