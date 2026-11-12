package settings

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"go.yaml.in/yaml/v3"
)

func versionFixture(t *testing.T, edits ...func(*policy.Policy)) (*Store, *store.Store, *policy.Policy, *Snapshot) {
	t.Helper()
	s, db, p := setup(t)
	for _, edit := range edits {
		edit(p)
	}
	area := t.TempDir()
	p.Library.Dangling.Roots = []string{area}
	for key, v := range p.Versions {
		v.Library = filepath.Join(area, key)
		if err := os.Mkdir(v.Library, 0o700); err != nil {
			t.Fatal(err)
		}
		if section := p.Library.Versions[key]; section != nil {
			section.Roots = []string{v.Library}
		}
	}
	dir := copyFixture(t)
	b, err := yaml.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err = policy.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.Bootstrap(context.Background(), p, ImportRequest{Dir: dir, Actor: "fixture", Lookup: fixtureLookup})
	if err != nil {
		t.Fatal(err)
	}
	return s, db, p, snap
}

func versionCoordinator(t *testing.T, s *Store, p *policy.Policy, initial *Snapshot) *RuntimeCoordinator {
	t.Helper()
	m, err := NewRuntimeCoordinator(s, p, initial, initial.Policy, func(_ context.Context, next *Snapshot, _ *Runtime) (any, error) { return next.Policy, nil })
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func editVersion(records *Records, key string, change func(*Version)) {
	for i := range records.Versions {
		if records.Versions[i].Key == key {
			change(&records.Versions[i])
		}
	}
}

func TestVersionsUpgradeBootstrapOnlyE3(t *testing.T) {
	t.Parallel()
	s, db, p := setup(t)
	before := bootstrap(t, s, p)
	if err := s.MarkPublished(context.Background(), before.Generation); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"DELETE FROM settings_e3_bootstrap", "DELETE FROM setting_version", "DELETE FROM setting_view_binding",
		"UPDATE settings_runtime_state SET provider_epoch=9 WHERE id=1"} {
		if _, err := db.Writer().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	// Model the already-live source records, including a disabled source and its existing secret.
	if _, err := db.Writer().Exec("UPDATE setting_indexer SET enabled=0, priority=3 WHERE key='dmm'"); err != nil {
		t.Fatal(err)
	}
	var secretBefore string
	if err := db.Reader().QueryRow("SELECT value FROM setting_secret LIMIT 1").Scan(&secretBefore); err != nil {
		t.Fatal(err)
	}
	imported, err := s.Bootstrap(context.Background(), p, ImportRequest{Dir: p.Dir, Actor: "serve", Lookup: func(string) (string, bool) { t.Fatal("upgrade resolved a source secret"); return "", false }})
	if err != nil {
		t.Fatal(err)
	}
	if imported.Generation != before.Generation+1 || imported.ProviderEpoch != 9 {
		t.Fatalf("generation/epoch: %d/%d", imported.Generation, imported.ProviderEpoch)
	}
	if !reflect.DeepEqual(imported.Policy.Versions, p.Versions) || !reflect.DeepEqual(imported.Policy.Views, p.Views) {
		t.Fatal("version bootstrap changed YAML fields")
	}
	for _, record := range imported.Records.Indexers {
		if record.Key == "dmm" && (record.Enabled || record.Priority != 3) {
			t.Fatal("upgrade changed source state")
		}
	}
	var secretAfter string
	if err := db.Reader().QueryRow("SELECT value FROM setting_secret LIMIT 1").Scan(&secretAfter); err != nil || secretBefore != secretAfter {
		t.Fatal("upgrade changed credential")
	}
	for _, a := range audit(t, s) {
		if a.Generation == imported.Generation && a.Entity != "versions" && a.Entity != "views" && !(a.Entity == "settings" && a.Operation == "bootstrap-import" && a.Key == "versions,views") {
			t.Fatalf("unexpected upgrade audit: %s/%s", a.Entity, a.Operation)
		}
	}
	changed, _ := p.Clone()
	changed.Versions["movie-web"].Library = "/ignored-yaml"
	next, err := s.Bootstrap(context.Background(), changed, ImportRequest{Dir: "/does-not-exist", Actor: "serve"})
	if err != nil || next.Generation != imported.Generation || next.Policy.Versions["movie-web"].Library != p.Versions["movie-web"].Library {
		t.Fatalf("later YAML won: %v", err)
	}
}

func TestVersionsConfirmationCandidateAndGeneration(t *testing.T) {
	t.Parallel()
	s, _, p, initial := versionFixture(t)
	m := versionCoordinator(t, s, p, initial)
	req := saveRequest(p, initial)
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Mode = policy.ModeActive })
	if _, err := s.Save(context.Background(), req); err == nil {
		t.Fatal("direct store save enabled writes")
	}
	_, err := m.SaveAndApply(context.Background(), req)
	var refused *ConfirmationError
	if !errors.As(err, &refused) || len(refused.Effects) == 0 || !strings.Contains(refused.Effects[0].Effect, p.Versions["movie-web"].Library) || !strings.Contains(refused.Effects[0].Effect, "rd, ad, tb") {
		t.Fatalf("missing specific effects: %v", err)
	}
	proof, err := m.Confirm(context.Background(), req)
	if err != nil || proof.Token == "" || proof.Generation != initial.Generation+1 {
		t.Fatalf("confirmation: %+v %v", proof, err)
	}
	changed := req
	changed.Records = saveRequest(p, initial).Records
	editVersion(&changed.Records, "movie-web", func(v *Version) { v.Config.Mode = policy.ModeActive; v.Config.Upgrades = "off" })
	changed.ConfirmationToken = proof.Token
	if _, err := m.SaveAndApply(context.Background(), changed); !errors.As(err, &refused) {
		t.Fatalf("token authorized different candidate: %v", err)
	}
	proof, err = m.Confirm(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.ConfirmationToken = proof.Token
	saved, err := m.SaveAndApply(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Policy.Router.Enabled || saved.Policy.Library.Enabled || saved.Policy.Notify.Enabled {
		t.Fatal("confirmation enabled YAML switch")
	}
	if _, err = m.SaveAndApply(context.Background(), req); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale confirmation reused: %v", err)
	}
}

func TestVersionRootChecksAndNoLinkDeletion(t *testing.T) {
	t.Parallel()
	s, db, p, initial := versionFixture(t)
	ctx := context.Background()
	root := p.Versions["movie-web"].Library
	alias := filepath.Join(p.Library.Dangling.Roots[0], "alias")
	if err := os.Symlink(p.Versions["movie-remux"].Library, alias); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"relative": "relative", "missing": filepath.Join(root, "missing"), "other-root": p.Versions["movie-remux"].Library, "alias": alias, "mount": p.Library.Mount.Path, "trash": p.Library.Trash.Dir} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.ValidateRoot(ctx, p, "movie-web", path); err == nil {
				t.Fatal("accepted unsafe root")
			}
		})
	}
	// A relative root is refused as that, at the root field, before anything else is checked.
	var verr *policy.Errors
	if _, err := s.ValidateRoot(ctx, p, "movie-web", "relative-folder"); !errors.As(err, &verr) || len(verr.List) != 1 ||
		verr.List[0].Path != "root" || !strings.HasPrefix(verr.List[0].Msg, "must be an absolute path") {
		t.Errorf("relative root: %v", err)
	}
	newRoot := filepath.Join(p.Library.Dangling.Roots[0], "new-root")
	if err := os.Mkdir(newRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidateRoot(ctx, p, "movie-web", newRoot); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(987,'movie','Fixture',0,0)`,
		`INSERT INTO version(id,title_id,policy_key,created_at,updated_at) VALUES(987,987,'movie-web',0,0)`,
		`INSERT INTO link(id,path,target,title_id,created_at,updated_at) VALUES(987,'fixture-link','fixture-target',987,0,0)`,
		`INSERT INTO link_version(link_id,version_id) VALUES(987,987)`,
		`INSERT INTO link_trash(link_id,version_key,path,trash_path,target,reason,moved_at) VALUES(987,'movie-web','fixture-link','fixture-trash','fixture-target','fixture',0)`,
	} {
		if _, err := db.Writer().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	keptLink := filepath.Join(root, "kept.mkv")
	keptTrash := filepath.Join(t.TempDir(), "kept-trash.mkv")
	keptTarget := filepath.Join(t.TempDir(), "target.mkv")
	if err := os.WriteFile(keptTarget, []byte("fixture mount content"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{keptLink, keptTrash} {
		if err := os.Symlink(keptTarget, path); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Writer().Exec("UPDATE link SET path=?,target=? WHERE id=987", keptLink, keptTarget); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec("UPDATE link_trash SET path=?,trash_path=?,target=? WHERE link_id=987", keptLink, keptTrash, keptTarget); err != nil {
		t.Fatal(err)
	}
	req := saveRequest(p, initial)
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Library = newRoot; v.Roots = []string{newRoot} })
	if _, err := s.Candidate(ctx, req); err == nil || !strings.Contains(err.Error(), "active link") || !strings.Contains(err.Error(), "docs/migration.md#changing-a-root-with-links") {
		t.Fatalf("root change: %v", err)
	}
	// Remove an unreferenced version with a link: the confirmation only stops management.
	req = saveRequest(p, initial)
	req.Records.Versions = nil
	for _, v := range initial.Records.Versions {
		if v.Key != "movie-web" {
			req.Records.Versions = append(req.Records.Versions, v)
		}
	}
	for i, v := range req.Records.Views {
		if v.Key == "radarr" {
			v.Versions = []string{"movie-remux", "anime-movie"}
			v.Profiles = nil
			req.Records.Views[i] = v
		}
	}
	// The owning arr remains YAML-owned; this fixture removes its reference before the coordinator starts.
	for key, src := range p.Import.Arr {
		if src.Version == "movie-web" {
			delete(p.Import.Arr, key)
		}
	}
	m := versionCoordinator(t, s, p, initial)
	if _, err := m.SaveAndApply(ctx, req); err == nil {
		t.Fatal("removed linked version without confirmation")
	}
	proof, err := m.Confirm(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.ConfirmationToken = proof.Token
	if _, err := m.SaveAndApply(ctx, req); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"link", "link_version", "link_trash"} {
		var n int
		if err := db.Reader().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s was changed: %d %v", table, n, err)
		}
	}
	for _, path := range []string{keptLink, keptTrash} {
		if target, err := os.Readlink(path); err != nil || target != keptTarget {
			t.Fatalf("settings changed a filesystem link: %v", err)
		}
	}
	if content, err := os.ReadFile(keptTarget); err != nil || string(content) != "fixture mount content" {
		t.Fatal("settings changed mount content")
	}
}

func TestVersionsUpgradeFailureAtomic(t *testing.T) {
	t.Parallel()
	s, db, p := setup(t)
	initial := bootstrap(t, s, p)
	for _, query := range []string{"DELETE FROM settings_e3_bootstrap", "DELETE FROM setting_version", "DELETE FROM setting_view_binding"} {
		if _, err := db.Writer().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	dir := copyFixture(t)
	// The upgrade takes versions from the base serve reads from the policy directory (ReadBase), as serve does.
	upgrade := func() error {
		base, err := ReadBase(context.Background(), db, dir)
		if err != nil {
			return err
		}
		_, err = s.Bootstrap(context.Background(), base, ImportRequest{Dir: dir, Actor: "serve"})
		return err
	}
	editFixture(t, dir, "versions.yaml", "mode: shadow", "mode: invalid")
	if err := upgrade(); err == nil {
		t.Fatal("accepted invalid upgrade")
	}
	if err := os.WriteFile(filepath.Join(dir, "versions.yaml"), []byte("versions: [\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := upgrade(); err == nil {
		t.Fatal("accepted malformed upgrade YAML")
	}
	for _, table := range []string{"setting_version", "setting_view_binding", "settings_e3_bootstrap"} {
		var n int
		if err := db.Reader().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatal("partial bootstrap")
		}
	}
	var gen int64
	if err := db.Reader().QueryRow("SELECT MAX(generation) FROM settings_generation").Scan(&gen); err != nil || gen != initial.Generation {
		t.Fatal("failed bootstrap advanced generation")
	}
}

func TestVersionsDoNotPersistNamingOrViewCredentials(t *testing.T) {
	t.Parallel()
	s, db, p := setup(t)
	snap := bootstrap(t, s, p)
	for _, table := range []string{"setting_version", "setting_view_binding"} {
		rows, err := db.Reader().Query("SELECT config FROM " + table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(raw, "from_arr") || strings.Contains(raw, "THEATARR_VIEW_") {
				t.Fatal("version records persisted naming or credentials")
			}
		}
		rows.Close()
	}
	b, _ := json.Marshal(snap.Records.Views)
	if strings.Contains(string(b), "api_key_env") {
		t.Fatal("binding exposed credential reference")
	}
}

func TestVersionWriteEffects(t *testing.T) {
	t.Parallel()
	s, db, p, initial := versionFixture(t)
	ctx := context.Background()
	active, _ := initial.Policy.Clone()
	active.Versions["movie-web"].Mode = policy.ModeActive
	active.Versions["movie-web"].Providers = []string{"rd"}
	// Only the kept list asks: a library going Live (or created Live), downloads on, starting to follow. Everyday edits
	// of a Live library (contains "") ask nothing.
	for _, tc := range []struct {
		name, contains string
		change         func(*policy.Policy)
	}{
		{"new-active", "becomes Live", func(next *policy.Policy) { v := *next.Versions["movie-web"]; next.Versions["new"] = &v }},
		{"go-live", "becomes Live", func(next *policy.Policy) { next.Versions["movie-web"].Mode = policy.ModeActive }},
		{"grab-on", "Downloads are on for Live library movie-web", func(next *policy.Policy) { next.Versions["movie-web"].Grab = true }},
		{"start-following", "starts following movie-remux", func(next *policy.Policy) { next.Versions["movie-web"].Follows = "movie-remux" }},
		{"active-root", "", func(next *policy.Policy) { next.Versions["movie-web"].Library += "-new" }},
		{"active-provider", "", func(next *policy.Policy) {
			next.Versions["movie-web"].Providers = append(next.Versions["movie-web"].Providers, "ad")
		}},
		{"remove-follows", "", func(next *policy.Policy) { next.Versions["movie-web"].Follows = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := active.Clone()
			switch tc.name {
			case "remove-follows":
				before.Versions["movie-web"].Follows = "movie-remux"
			case "go-live":
				before.Versions["movie-web"].Mode = policy.ModeShadow
			}
			next, _ := before.Clone()
			tc.change(next)
			effects, err := versionConfirmationEffects(ctx, db.Reader(), before, next)
			if err != nil {
				t.Fatal(err)
			}
			if tc.contains == "" {
				if len(effects) != 0 {
					t.Fatalf("%s asked for confirmation: %+v", tc.name, effects)
				}
				return
			}
			found := false
			for _, effect := range effects {
				if strings.Contains(effect.Effect, tc.contains) {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %s effect: %+v", tc.name, effects)
			}
		})
	}
	req := saveRequest(p, initial)
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Mode = policy.ModeActive })
	m := versionCoordinator(t, s, p, initial)
	proof, err := m.Confirm(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	// A token is one-use even when the candidate is unchanged and the first preparation fails.
	m.prepare = func(context.Context, *Snapshot, *Runtime) (any, error) {
		return nil, errors.New("fixture prepare failed")
	}
	req.ConfirmationToken = proof.Token
	if _, err = m.SaveAndApply(ctx, req); err == nil {
		t.Fatal("prepare failure ignored")
	}
	if _, err = m.SaveAndApply(ctx, req); err == nil {
		t.Fatal("consumed confirmation reused")
	}
}

func TestVersionDeletionRechecksLinksAtCommit(t *testing.T) {
	t.Parallel()
	s, db, p, initial := versionFixture(t)
	ctx := context.Background()
	for key, src := range p.Import.Arr {
		if src.Version == "movie-web" {
			delete(p.Import.Arr, key)
		}
	}
	req := saveRequest(p, initial)
	for i, v := range req.Records.Versions {
		if v.Key == "movie-web" {
			req.Records.Versions = append(req.Records.Versions[:i:i], req.Records.Versions[i+1:]...)
			break
		}
	}
	for i, v := range req.Records.Views {
		if v.Key == "radarr" {
			v.Versions = []string{"movie-remux", "anime-movie"}
			v.Profiles = nil
			req.Records.Views[i] = v
		}
	}
	m := versionCoordinator(t, s, p, initial)
	proof, err := m.Confirm(ctx, req)
	if err != nil || len(proof.Effects) != 0 {
		t.Fatalf("unlinked deletion: %+v %v", proof, err)
	}
	m.prepare = func(context.Context, *Snapshot, *Runtime) (any, error) {
		for _, query := range []string{
			`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(999,'movie','Fixture',0,0)`,
			`INSERT INTO version(id,title_id,policy_key,created_at,updated_at) VALUES(999,999,'movie-web',0,0)`,
			`INSERT INTO link(id,path,target,title_id,created_at,updated_at) VALUES(999,'fixture-new-link','fixture-target',999,0,0)`,
			`INSERT INTO link_version(link_id,version_id) VALUES(999,999)`,
		} {
			if _, err := db.Writer().Exec(query); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	var confirmation *ConfirmationError
	if _, err = m.SaveAndApply(ctx, req); !errors.As(err, &confirmation) {
		t.Fatalf("unconfirmed links at commit: %v", err)
	}
	current, err := s.Load(ctx, p)
	if err != nil || current.Generation != initial.Generation {
		t.Fatal("refused deletion advanced generation")
	}
}

func TestVersionRootWritableMountTrashAndFollowed(t *testing.T) {
	t.Parallel()
	s, db, p, initial := versionFixture(t)
	area := p.Library.Dangling.Roots[0]
	root := filepath.Join(area, "readonly")
	if err := os.Mkdir(root, 0500); err != nil {
		t.Fatal(err)
	}
	if _, err := validateOneRoot(context.Background(), policy.NewRootResolver(context.Background()), p, "new", root, true); err == nil || !strings.Contains(err.Error(), "not writable") {
		t.Fatalf("active readonly root: %v", err)
	}
	if _, err := validateOneRoot(context.Background(), policy.NewRootResolver(context.Background()), p, "new", root, false); err != nil {
		t.Fatal("shadow root requires write permission:", err)
	}
	for _, kind := range []string{"mount", "trash"} {
		target := filepath.Join(area, kind)
		if err := os.Mkdir(target, 0700); err != nil {
			t.Fatal(err)
		}
		if kind == "mount" {
			p.Library.Mount.Path = target
		} else {
			p.Library.Trash.Dir = target
		}
		alias := target + "-alias"
		if err := os.Symlink(target, alias); err != nil {
			t.Fatal(err)
		}
		if _, err := validateOneRoot(context.Background(), policy.NewRootResolver(context.Background()), p, "new", alias, false); err == nil || !strings.Contains(err.Error(), kind) {
			t.Fatalf("%s alias accepted: %v", kind, err)
		}
	}
	previous, _ := initial.Policy.Clone()
	next, _ := previous.Clone()
	follower := *next.Versions["movie-web"]
	follower.Follows = "movie-web"
	follower.Library = filepath.Join(area, "follower")
	if err := os.Mkdir(follower.Library, 0700); err != nil {
		t.Fatal(err)
	}
	follower.Naming = policy.Naming{FromVersion: "movie-web"}
	next.Versions["follower"] = &follower
	next.Versions["movie-web"].Mode = policy.ModeActive
	if err := validateVersionChanges(context.Background(), db.Reader(), previous, next, false); err == nil || !strings.Contains(err.Error(), "stay read-only") {
		t.Fatalf("followed source writable: %v", err)
	}
	if snap, err := s.Load(context.Background(), initial.Policy); err != nil || snap.Generation != initial.Generation {
		t.Fatal("validation wrote settings")
	}
}

func TestVersionFallbackRootChangeChecksActiveLinks(t *testing.T) {
	t.Parallel()
	s, db, p, initial := versionFixture(t)
	ctx := context.Background()
	root := filepath.Join(p.Library.Dangling.Roots[0], "fallback-root")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	req := saveRequest(p, initial)
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Library = root; v.Roots = []string{root} })
	head, err := s.Save(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1001,'movie','Fallback',0,0)`,
		`INSERT INTO version(id,title_id,policy_key,created_at,updated_at) VALUES(1001,1001,'movie-web',0,0)`,
		`INSERT INTO link(id,path,target,title_id,created_at,updated_at) VALUES(1001,'fallback-link','fixture-target',1001,0,0)`,
		`INSERT INTO link_version(link_id,version_id) VALUES(1001,1001)`,
	} {
		if _, err := db.Writer().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	// Startup fell back to the old root, while settings reads still expose the stored head.
	m := versionCoordinator(t, s, p, initial)
	req = saveRequest(p, head)
	if _, err := m.Confirm(ctx, req); err == nil || !strings.Contains(err.Error(), "active link") {
		t.Fatalf("fallback confirmation: %v", err)
	}
	if _, err := m.SaveAndApply(ctx, req); err == nil || !strings.Contains(err.Error(), "active link") {
		t.Fatalf("fallback save: %v", err)
	}
	if m.Current().Generation != initial.Generation {
		t.Fatal("fallback runtime changed")
	}
}

// The upgrade of a database with only source records checks the quality import before committing the version
// records: a start that would fail BootstrapQuality (here, no TRaSH tree for trash_id formats) imports nothing. And
// it takes versions from the base serve read, so source policy files the database already owns may be gone.
func TestVersionsUpgradePreflightsQualityAndIgnoresSourceFiles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db, p := setup(t)
	before := bootstrap(t, s, p)
	for _, query := range []string{"DELETE FROM settings_e3_bootstrap", "DELETE FROM setting_version", "DELETE FROM setting_view_binding"} {
		if _, err := db.Writer().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	dir := copyFixture(t)
	for _, file := range []string{"indexers.yaml", "providers.yaml", "budgets.yaml"} {
		if err := os.Remove(filepath.Join(dir, file)); err != nil {
			t.Fatal(err)
		}
	}
	base, err := ReadBase(ctx, db, dir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetTrashRoot("")
	if _, err := s.Bootstrap(ctx, base, ImportRequest{Dir: dir, Actor: "serve", RequireQuality: true}); err == nil {
		t.Fatal("version records imported though the quality records cannot be")
	}
	var marker int
	if err := db.Reader().QueryRow("SELECT COUNT(*) FROM settings_e3_bootstrap").Scan(&marker); err != nil || marker != 0 {
		t.Fatalf("version records committed before the quality preflight: %d %v", marker, err)
	}
	var gen int64
	if err := db.Reader().QueryRow("SELECT MAX(generation) FROM settings_generation").Scan(&gen); err != nil || gen != before.Generation {
		t.Fatalf("generation moved: %d %v", gen, err)
	}
	s.SetTrashRoot("testdata/trash")
	imported, err := s.Bootstrap(ctx, base, ImportRequest{Dir: dir, Actor: "serve", RequireQuality: true})
	if err != nil {
		t.Fatal(err)
	}
	if imported.Generation != before.Generation+1 || !reflect.DeepEqual(imported.Policy.Versions, p.Versions) {
		t.Fatal("upgrade without source files changed the versions")
	}
	if _, err := s.BootstrapQuality(ctx, base, "serve"); err != nil {
		t.Fatal(err)
	}
}

// Saves of unchanged versions never touch the debrid mount: a dead FUSE mount (stat fails with something other
// than "not found") must not block source or quality edits or hold the write transaction.
func TestVersionsSaveIgnoresUnreachableMount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, p := setup(t)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	base, _ := p.Clone()
	base.Library.Mount.Path = filepath.Join(file, "zurg") // stat: not a directory
	snap, err := s.Bootstrap(ctx, base, ImportRequest{Dir: exampleDir, Actor: "admin", Lookup: fixtureLookup})
	if err != nil {
		t.Fatal(err)
	}
	records := snap.Records
	records.Budgets = append([]Budget(nil), records.Budgets...)
	records.Budgets[0].Config.Requests++
	if _, err := s.Save(ctx, SaveRequest{ExpectedGeneration: snap.Generation, Actor: "admin", Base: base, Records: records}); err != nil {
		t.Fatalf("budget save blocked by the mount: %v", err)
	}
}

// Beyond the root, providers, mode and follows: changes to how an active version writes or what it may replace
// also need the confirmation; the same changes on a shadow version do not.
// Storage, client, import, upgrades and enable of a Live library are everyday edits: they save at once.
func TestVersionConfirmationSkipsActiveWriteSettings(t *testing.T) {
	t.Parallel()
	p, err := policy.Load(exampleDir)
	if err != nil {
		t.Fatal(err)
	}
	p.Versions["movie-web"].Mode, p.Versions["movie-web"].Upgrades = policy.ModeActive, policy.UpgradesOff
	p.Versions["movie-web"].Enable = "request"
	next, _ := p.Clone()
	v := next.Versions["movie-web"]
	v.Storage, v.Client, v.Import = policy.StorageSeedbox, "other-client", "copy"
	v.Upgrades, v.Enable = policy.UpgradesAuto, "default"
	effects, err := versionConfirmationEffects(context.Background(), nil, p, next)
	if err != nil || len(effects) != 0 {
		t.Fatalf("everyday edits asked for confirmation: %+v %v", effects, err)
	}
}

// The optional library name is stored only when set: an ordinary save of a library without one writes no name key
// (an older build's strict decoder still loads the row), a set name round-trips, and an over-long one is refused.
func TestVersionNameOptional(t *testing.T) {
	t.Parallel()
	s, db, p, initial := versionFixture(t)
	ctx := context.Background()
	stored := func() map[string]string {
		t.Helper()
		rows, err := db.Reader().Query("SELECT key, config FROM setting_version")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var key, raw string
			if err := rows.Scan(&key, &raw); err != nil {
				t.Fatal(err)
			}
			out[key] = raw
		}
		return out
	}
	before := stored()
	if len(before) == 0 {
		t.Fatal("no stored versions")
	}
	m := versionCoordinator(t, s, p, initial)
	req := saveRequest(p, initial)
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Edition = "Extended" })
	snap, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	// The generation snapshot is the policy marshalled as JSON: an unnamed library adds no key there either.
	snaps, err := db.Reader().Query("SELECT COALESCE(snapshot, '') FROM settings_generation")
	if err != nil {
		t.Fatal(err)
	}
	for snaps.Next() {
		var raw string
		if err := snaps.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, `"Name":""`) {
			t.Errorf("a generation snapshot stored an empty library name")
		}
	}
	snaps.Close()
	for key, raw := range stored() {
		if strings.Contains(raw, `"name"`) {
			t.Errorf("%s stored a name key without a name: %s", key, raw)
		}
		if key != "movie-web" && raw != before[key] {
			t.Errorf("%s changed on a save of another library:\n%s\n%s", key, before[key], raw)
		}
	}
	req = saveRequest(p, snap)
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Name = "Films in 4K" })
	if snap, err = m.SaveAndApply(ctx, req); err != nil {
		t.Fatal(err)
	}
	if got := snap.Policy.Versions["movie-web"].Name; got != "Films in 4K" || !strings.Contains(stored()["movie-web"], `"name":"Films in 4K"`) {
		t.Fatalf("name not stored: %q %s", got, stored()["movie-web"])
	}
	req = saveRequest(p, snap)
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Name = "" })
	if snap, err = m.SaveAndApply(ctx, req); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored()["movie-web"], `"name"`) {
		t.Fatalf("a cleared name left its key: %s", stored()["movie-web"])
	}
	for _, bad := range []string{strings.Repeat("é", 41), "Films\n4K"} {
		req = saveRequest(p, snap)
		editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Name = bad })
		if _, err := m.SaveAndApply(ctx, req); err == nil || !strings.Contains(err.Error(), "versions[movie-web].name") {
			t.Errorf("name %q: %v", bad, err)
		}
	}
	req = saveRequest(p, snap)
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Name = strings.Repeat("é", 40) })
	if _, err := m.SaveAndApply(ctx, req); err != nil {
		t.Errorf("a 40-character name was refused: %v", err)
	}
}

// Renaming an active library changes nothing it does, so it asks for no confirmation.
func TestVersionRenameNeedsNoConfirmation(t *testing.T) {
	t.Parallel()
	p, err := policy.Load(exampleDir)
	if err != nil {
		t.Fatal(err)
	}
	p.Versions["movie-web"].Mode = policy.ModeActive
	next, _ := p.Clone()
	next.Versions["movie-web"].Name = "Films in 4K"
	if effects, err := versionConfirmationEffects(context.Background(), nil, p, next); err != nil || len(effects) != 0 {
		t.Fatalf("a rename asked for confirmation: %+v %v", effects, err)
	}
}

// The optional minimum availability and availability delay of a movie library are stored only when set: an ordinary
// save of a library without them writes neither key, in the stored record or the generation snapshot (an older
// build's strict decoder still loads both), set values round-trip, cleared ones leave no key, and a bad minimum or
// a series library is refused.
func TestVersionAvailabilityOptional(t *testing.T) {
	t.Parallel()
	s, db, p, initial := versionFixture(t)
	ctx := context.Background()
	stored := func(query string) map[string]string {
		t.Helper()
		rows, err := db.Reader().Query(query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var key, raw string
			if err := rows.Scan(&key, &raw); err != nil {
				t.Fatal(err)
			}
			out[key] = raw
		}
		return out
	}
	versions := func() map[string]string { return stored("SELECT key, config FROM setting_version") }
	unset := func(what string) {
		t.Helper()
		for key, raw := range versions() {
			if strings.Contains(raw, "availab") {
				t.Errorf("%s: %s stored an availability key: %s", what, key, raw)
			}
		}
		for id, raw := range stored("SELECT CAST(generation AS TEXT), COALESCE(snapshot, '') FROM settings_generation") {
			if strings.Contains(raw, `"MinimumAvailability"`) || strings.Contains(raw, `"AvailabilityDelay"`) {
				t.Errorf("%s: generation %s stored an availability key", what, id)
			}
		}
	}
	before := versions()
	m := versionCoordinator(t, s, p, initial)
	req := saveRequest(p, initial)
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.Edition = "Extended" })
	snap, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	unset("an ordinary save")
	if snaps := stored("SELECT CAST(generation AS TEXT), COALESCE(snapshot, '') FROM settings_generation"); len(snaps) == 0 ||
		!strings.Contains(snaps["2"], `"Edition":"Extended"`) {
		t.Fatalf("no generation snapshot to check: %d", len(snaps))
	}
	for key, raw := range versions() {
		if key != "movie-web" && raw != before[key] {
			t.Errorf("%s changed on a save of another library:\n%s\n%s", key, before[key], raw)
		}
	}
	req = saveRequest(p, snap)
	editVersion(&req.Records, "movie-web", func(v *Version) {
		v.Config.MinimumAvailability, v.Config.AvailabilityDelay = "inCinemas", -3
	})
	if snap, err = m.SaveAndApply(ctx, req); err != nil {
		t.Fatal(err)
	}
	got := snap.Policy.Versions["movie-web"]
	if raw := versions()["movie-web"]; got.MinimumAvailability != "inCinemas" || got.AvailabilityDelay != -3 ||
		!strings.Contains(raw, `"minimum_availability":"inCinemas"`) || !strings.Contains(raw, `"availability_delay":-3`) {
		t.Fatalf("not stored: %q %d %s", got.MinimumAvailability, got.AvailabilityDelay, raw)
	}
	req = saveRequest(p, snap)
	editVersion(&req.Records, "movie-web", func(v *Version) {
		v.Config.MinimumAvailability, v.Config.AvailabilityDelay = "", 0
	})
	if snap, err = m.SaveAndApply(ctx, req); err != nil {
		t.Fatal(err)
	}
	if raw := versions()["movie-web"]; strings.Contains(raw, "availab") {
		t.Fatalf("cleared settings left their keys: %s", raw)
	}
	for key, change := range map[string]func(v *Version){
		"movie-web": func(v *Version) { v.Config.MinimumAvailability = "soon" },
		"tv-2160p":  func(v *Version) { v.Config.AvailabilityDelay = 2 },
	} {
		req = saveRequest(p, snap)
		editVersion(&req.Records, key, change)
		if _, err := m.SaveAndApply(ctx, req); err == nil || !strings.Contains(err.Error(), "versions["+key+"].") {
			t.Errorf("%s: %v", key, err)
		}
	}
	// A submitted section: both sent unset are dropped, set ones stay.
	sent := `{"movie-web":{"kind":"movie","minimum_availability":"","availability_delay":0},"movie-remux":{"minimum_availability":"announced","availability_delay":-1}}`
	want := `{"movie-remux":{"availability_delay":-1,"minimum_availability":"announced"},"movie-web":{"kind":"movie"}}`
	if got := string(DropUnsetOptional("versions", json.RawMessage(sent))); got != want {
		t.Errorf("DropUnsetOptional = %s, want %s", got, want)
	}
}
