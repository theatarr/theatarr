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
)

func qualityBootstrap(t *testing.T, s *Store, p *policy.Policy) *Snapshot {
	t.Helper()
	s.SetTrashRoot("testdata/trash")
	bootstrap(t, s, p)
	snap, err := s.BootstrapQuality(context.Background(), p, "upgrade")
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestQualityBootstrapAtomicAndIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db, p := setup(t)
	first := bootstrap(t, s, p)
	s.SetTrashRoot("")
	var secretsBefore string
	if err := db.Reader().QueryRow("SELECT group_concat(id || ':' || value || ':' || updated_at) FROM setting_secret").Scan(&secretsBefore); err != nil {
		t.Fatal(err)
	}
	// A missing TRaSH tree fails before any quality records, audit or generation can be committed.
	if _, err := s.BootstrapQuality(ctx, p, "upgrade"); err == nil {
		t.Fatal("missing specifications accepted")
	}
	failed, err := s.Load(ctx, p)
	if err != nil || failed.Generation != first.Generation {
		t.Fatalf("partial bootstrap: %v", err)
	}
	s.SetTrashRoot("testdata/trash")
	next, err := s.BootstrapQuality(ctx, p, "upgrade")
	if err != nil {
		t.Fatal(err)
	}
	if next.Generation != first.Generation+1 || next.ProviderEpoch != first.ProviderEpoch {
		t.Fatal("wrong generation or provider epoch")
	}
	if !reflect.DeepEqual(first.Records.Indexers, next.Records.Indexers) || !reflect.DeepEqual(first.Records.Providers, next.Records.Providers) || !reflect.DeepEqual(first.Records.Budgets, next.Records.Budgets) {
		t.Fatal("bootstrap changed source records")
	}
	var secretsAfter string
	_ = db.Reader().QueryRow("SELECT group_concat(id || ':' || value || ':' || updated_at) FROM setting_secret").Scan(&secretsAfter)
	if secretsBefore != secretsAfter {
		t.Fatal("bootstrap touched credentials")
	}
	found := false
	for _, a := range audit(t, s) {
		if a.Key == "quality_sections" && a.Operation == "bootstrap-import" {
			b, _ := json.Marshal(a.After)
			for _, section := range []string{"profiles", "formats", "format_sets", "naming"} {
				if !strings.Contains(string(b), section) {
					t.Fatal("audit omitted section", section)
				}
			}
			found = true
		}
	}
	if !found {
		t.Fatal("missing named bootstrap audit")
	}
	for _, r := range next.Records.Formats {
		if len(r.Specification) == 0 || r.Config.File != "" {
			t.Fatal("format was not materialized", r.Key)
		}
	}
	// DB ownership survives a missing file, invalid former quality values and changed YAML hashes.
	dir := copyFixture(t)
	editFixture(t, dir, "profiles.yaml", "min_score: 0", "min_score: ignored-invalid-value")
	if err := os.Remove(filepath.Join(dir, "formats", "seadex.json")); err != nil {
		t.Fatal(err)
	}
	base, err := ReadBase(ctx, db, dir)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.BootstrapQuality(ctx, base, "restart")
	if err != nil {
		t.Fatal(err)
	}
	if again.Generation != next.Generation || again.Revision != next.Revision || !reflect.DeepEqual(again.Policy.Profiles, next.Policy.Profiles) {
		t.Fatal("restart changed database settings")
	}
	if !reflect.DeepEqual(again.Policy.Router, p.Router) || !reflect.DeepEqual(again.Policy.Library, p.Library) || !reflect.DeepEqual(again.Policy.Notify, p.Notify) || !sameModes(again.Policy.Versions, p.Versions) {
		t.Fatal("safety switches changed")
	}
}

func TestQualityInvalidSaveAndExport(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, p := setup(t)
	snap := qualityBootstrap(t, s, p)
	req := saveRequest(p, snap)
	req.Records.Formats[0].Specification = json.RawMessage(`{"name":"broken","specifications":[{"implementation":"ReleaseTitleSpecification","fields":{"value":"["}}]}`)
	if _, err := s.Save(ctx, req); err == nil {
		t.Fatal("bad regex accepted")
	}
	head, err := s.Load(ctx, p)
	if err != nil || head.Generation != snap.Generation {
		t.Fatalf("invalid save committed: %v", err)
	}
	files, _, err := head.ExportFiles()
	if err != nil {
		t.Fatal(err)
	}
	document, err := head.Export()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), document, 0600); err != nil {
		t.Fatal(err)
	}
	for name, raw := range files {
		path := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(path), 0700)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := policy.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	for key, ref := range loaded.Formats {
		raw, err := os.ReadFile(filepath.Join(dir, ref.File))
		if err != nil {
			t.Fatal(err)
		}
		var a, b any
		_ = json.Unmarshal(raw, &a)
		_ = json.Unmarshal(head.Policy.Formats[key].Specification, &b)
		if !reflect.DeepEqual(a, b) {
			t.Fatal("export changed full specification", key)
		}
	}
	for key, profile := range head.Policy.Profiles {
		for name, score := range profile.Formats {
			display, _ := head.Policy.ProfileFormat(profile, name)
			if loaded.Profiles[key].Formats[display] != score {
				t.Fatal("export changed scoped score", key, name)
			}
		}
	}
	req = saveRequest(p, head)
	req.Records.Profiles[0].Config.Cutoff = "missing"
	if _, err := s.Save(ctx, req); err == nil {
		t.Fatal("whole candidate validation bypassed")
	}
	req = saveRequest(p, head)
	req.ExpectedGeneration--
	if _, err := s.Save(ctx, req); !errors.Is(err, ErrConflict) {
		t.Fatal("stale save", err)
	}
}

func TestQualitySpecificationsRespectScoredFlavour(t *testing.T) {
	t.Parallel()
	p := &policy.Policy{
		Formats:  map[string]*policy.FormatRef{"Movie year": {Specification: json.RawMessage(`{"name":"Movie year","specifications":[{"implementation":"YearSpecification","fields":{"min":2000,"max":2030}}]}`)}},
		Profiles: map[string]*policy.Profile{"series": {Formats: map[string]int{"Movie year": 10}}},
		Versions: map[string]*policy.VersionPolicy{"tv": {Kind: policy.KindSeries, Profile: "series"}},
	}
	if err := validateSpecifications(p); err == nil || !strings.Contains(err.Error(), "versions[tv]") {
		t.Fatal("accepted a scored movie specification in a series profile", err)
	}
	p.Profiles["series"].Formats = nil
	if err := validateSpecifications(p); err != nil {
		t.Fatal("rejected an unscored format valid for another flavour", err)
	}
}

func TestQualityImportRejectsEscapingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "format.json")
	if err := os.WriteFile(outside, []byte(`{"name":"outside","specifications":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "format.json")); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"format.json", "../format.json", outside} {
		if _, err := formatSpecification(dir, policy.FormatRef{File: file}); err == nil {
			t.Fatal("import accepted escaping file", file)
		}
	}
}

func TestQualityFreshBootstrapPreflightIsAtomic(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, spec string }{
		{"parse", `{"name":`},
		{"validation", `{"name":"Seadex","specifications":[{"name":"Invalid pattern","implementation":"ReleaseTitleSpecification","fields":{"value":"["}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, _ := setup(t)
			dir := copyFixture(t)
			if err := os.WriteFile(filepath.Join(dir, "formats", "seadex.json"), []byte(tc.spec), 0600); err != nil {
				t.Fatal(err)
			}
			p, err := policy.Read(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Bootstrap(context.Background(), p, ImportRequest{Dir: dir, Actor: "bootstrap", RequireQuality: true, TrashRoot: "testdata/trash", Lookup: func(string) (string, bool) { return "invented", true }}); err == nil {
				t.Fatal("invalid quality bootstrap accepted")
			}
			for _, table := range []string{"settings_generation", "setting_secret", "setting_quality_profile", "settings_audit"} {
				var n int
				if err := db.Reader().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 0 {
					t.Fatal("failed preflight wrote records", table, n)
				}
			}
		})
	}
}

// The missing-specification message names the TRaSH tree variable by its preferred name (THEATARR_TRASH still works).
func TestRequireQualitySpecificationsNamesTrashVar(t *testing.T) {
	t.Parallel()
	err := requireQualitySpecifications(Records{Formats: []CustomFormat{{Record: Record{Key: "x"}}}})
	if err == nil || !strings.Contains(err.Error(), "THEATARR_TRASH") {
		t.Fatalf("err %v", err)
	}
}

// The first import of the quality sections copies the YAML the service already runs on: formats scored by an active
// version's profile change no definition a user could confirm, so the import never asks. A fresh install whose YAML
// has active versions (the example policy with the demo overlay) could not start. The import changes no mode, grab
// or switch.
func TestQualityBootstrapWithActiveVersions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _ := setup(t)
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(exampleDir)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"versions-theatarr.yaml", "library.yaml"} {
		b, err := os.ReadFile(filepath.Join("../../examples/demo", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := policy.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetTrashRoot("") // source records first: serve imports the quality sections in their own generation
	first, err := s.Bootstrap(ctx, p, ImportRequest{Dir: dir, Actor: "serve", Lookup: fixtureLookup})
	if err != nil {
		t.Fatal(err)
	}
	if first.Policy.Versions["tv-2160p-theatarr"].Mode != policy.ModeActive {
		t.Fatal("the overlay's active version was not imported active")
	}
	s.SetTrashRoot("testdata/trash")
	next, err := s.BootstrapQuality(ctx, p, "serve")
	if err != nil {
		t.Fatalf("quality import with active versions: %v", err)
	}
	if !reflect.DeepEqual(first.Records.Versions, next.Records.Versions) || !reflect.DeepEqual(first.Records.Switches, next.Records.Switches) {
		t.Fatal("the quality import changed versions or switches")
	}
}
