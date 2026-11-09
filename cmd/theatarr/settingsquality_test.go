package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

func TestCommandSettingsLoaderAndPinnedQualityRuntime(t *testing.T) {
	ctx := context.Background()
	db, st, base, _ := runtimeFixture(t)
	_, err := st.BootstrapQuality(ctx, base, "upgrade")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := st.BootstrapSections(ctx, base, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	prepare := func(_ context.Context, snap *settings.Snapshot, _ *settings.Runtime) (any, error) {
		return decide.New(snap.Policy, decide.Options{})
	}
	component, err := prepare(ctx, initial, nil)
	if err != nil {
		t.Fatal(err)
	}
	coord, err := settings.NewRuntimeCoordinator(st, base, initial, component, prepare)
	if err != nil {
		t.Fatal(err)
	}
	pinned := coord.Capture(ctx)
	records := initial.Records
	records.Profiles[0].Config.MinScore = 17
	records.Profiles[0].Config.CutoffScore = 17
	records.Naming[0].Config = policy.Naming{File: "{Movie Title}"}
	// Use a movie naming record even if the first sorted key belongs to series.
	for i := range records.Naming {
		if base.Versions[records.Naming[i].Key].Kind == policy.KindSeries {
			records.Naming[i].Config = base.Versions[records.Naming[i].Key].Naming
		}
	}
	saved, err := coord.SaveAndApply(ctx, settings.SaveRequest{ExpectedGeneration: initial.Generation, Actor: "admin", Records: records})
	if err != nil {
		t.Fatal(err)
	}
	old := settings.ContextRuntime(pinned)
	if old.Generation != initial.Generation || old.Components.(*decide.Engine).Policy.Profiles[records.Profiles[0].Key].MinScore == 17 {
		t.Fatal("in-flight engine changed")
	}
	if settings.ContextRuntime(coord.Capture(ctx)).Generation != saved.Generation {
		t.Fatal("new operation did not capture new generation")
	}
	dir := t.TempDir()
	if err := copyPolicyDir(base.Dir, dir); err != nil {
		t.Fatal(err)
	}
	// The command reads the same DB snapshot as serve and never reopens file-backed formats.
	if err := os.RemoveAll(filepath.Join(dir, "formats")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(db.Path()), filepath.Base(db.Path()))
	got, err := loadSettingsPolicy(ctx, "db", dir, path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Profiles, saved.Policy.Profiles) || !reflect.DeepEqual(got.Formats, saved.Policy.Formats) || got.Revision != saved.Revision {
		t.Fatal("CLI differs from server generation")
	}

}

func TestQualityDecisionImpactHeldFiles(t *testing.T) {
	t.Parallel()
	db, _, _, _ := runtimeFixture(t)
	for _, q := range []string{
		`INSERT INTO title(id,kind,name,year,runtime_minutes,created_at,updated_at) VALUES(1,'movie','Example',2026,120,0,0)`,
		`INSERT INTO version(id,title_id,policy_key,created_at,updated_at) VALUES(1,1,'movie',0,0),(2,1,'no-upgrades',0,0)`,
		`INSERT INTO version_file(version_id,title_id,instance,arr_file_id,path,relative_path,size_bytes,quality,scene_name,languages) VALUES(1,1,'fixture',1,'/movie.mkv','movie.mkv',10000000000,'WEBDL-1080p','Example.2026.1080p.WEB-DL-GRP','["English"]'),(2,1,'fixture',2,'/other.mkv','other.mkv',10000000000,'WEBDL-1080p','Example.2026.1080p.WEB-DL-GRP','["English"]')`,
	} {
		if _, err := db.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	before := &policy.Policy{Versions: map[string]*policy.VersionPolicy{"movie": {Kind: policy.KindMovie, Profile: "p"}, "no-upgrades": {Kind: policy.KindMovie, Profile: "p", Upgrades: policy.UpgradesOff}}, Profiles: map[string]*policy.Profile{"p": {Qualities: []policy.QualityItem{{Name: "WEBDL-1080p"}}, Cutoff: "WEBDL-1080p", CutoffScore: 50, Formats: map[string]int{"group": 100}}}, Formats: map[string]*policy.FormatRef{"group": {Flavour: "radarr", Specification: json.RawMessage(`{"name":"Group","specifications":[{"name":"Group","implementation":"ReleaseTitleSpecification","fields":{"value":"GRP"}}]}`)}}}
	after, err := before.Clone()
	if err != nil {
		t.Fatal(err)
	}
	after.Profiles["p"].Formats["group"] = 0
	impact, err := qualityDecisionImpact(context.Background(), db, before, after, "")
	if err != nil {
		t.Fatal(err)
	}
	if impact.Files != 2 || impact.ScoreChanged != 2 || impact.CutoffChanged != 2 || impact.UpgradeChanged != 1 || len(impact.Examples) != 2 {
		t.Fatalf("impact=%+v", impact)
	}
}

func TestQualityRecyclarrConverter(t *testing.T) {
	t.Parallel()
	raw := []byte("radarr:\n  household:\n    quality_profiles:\n      - name: WEB\n        qualities:\n          - name: WEB 1080p\n            qualities: [WEBDL-1080p, WEBRip-1080p]\n    custom_formats:\n      - trash_ids: [aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa]\n        assign_scores_to:\n          - name: WEB\n            score: 123\n")
	got, err := convertQualityRecyclarr(raw, "../../internal/recyclarr/testdata/trash", "radarr", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Formats) != 1 || len(got.Profiles) != 1 || got.Profiles[0].Config.Formats["Fmt A"] != 123 || !slices.Equal(got.Profiles[0].Fields, []string{"qualities", "formats/Fmt A"}) {
		t.Fatalf("conversion=%+v", got)
	}
	var spec format.FormatJSON
	if err := json.Unmarshal(got.Formats[0].Specification, &spec); err != nil || spec.TrashID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatal("missing full TRaSH JSON", err)
	}
}
