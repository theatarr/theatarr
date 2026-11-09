package migration

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/policy"
)

// freezeFixture is radarr_4k_debrid's GET bodies as the wizard reads them, with its naming.
func freezeFixture(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	s, err := decide.SnapshotDir("../decide/testdata/arr").Snapshot("radarr_4k_debrid")
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile("../library/naming/testdata/golden/radarr_4k_debrid.json")
	if err != nil {
		t.Fatal(err)
	}
	var g struct{ Naming json.RawMessage }
	if err := json.Unmarshal(golden, &g); err != nil {
		t.Fatal(err)
	}
	return map[string]json.RawMessage{"qualityprofile": s.QualityProfile, "customformat": s.CustomFormat,
		"qualitydefinition": s.QualityDefinition, "config/mediamanagement": json.RawMessage(`{}`),
		"config/indexer": json.RawMessage(`{}`), "config/naming": g.Naming}
}

func TestFreezeArr(t *testing.T) {
	t.Parallel()
	raw := freezeFixture(t)
	e, err := freezeArr("radarr_4k_debrid", "radarr", raw, "")
	if err != nil {
		t.Fatal(err)
	}
	profiles, sets, err := e.Fragments()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || len(sets) != 1 || e.Naming == nil {
		t.Fatalf("profiles %v, sets %v, naming %v", profiles, sets, e.Naming)
	}
	for _, s := range sets {
		for name, f := range s.Formats {
			if len(e.Files[f.File]) == 0 {
				t.Errorf("format %s: no file %q", name, f.File)
			}
		}
	}
	delete(raw, "config/naming")
	if plain, err := freezeArr("radarr_4k_debrid", "radarr", raw, ""); err != nil || plain.Naming != nil {
		t.Errorf("without config/naming: %v %+v", err, plain)
	}
	if _, err := freezeArr("radarr_4k_debrid", "lidarr", raw, ""); err == nil {
		t.Error("lidarr accepted")
	}
	delete(raw, "config/indexer")
	if _, err := freezeArr("radarr_4k_debrid", "radarr", raw, ""); err == nil || !strings.Contains(err.Error(), "missing config/indexer") {
		t.Errorf("missing body: %v", err)
	}
}

func TestFreezeArrRefusesWhatPolicyCannotExpress(t *testing.T) {
	t.Parallel()
	edit := func(t *testing.T, raw map[string]json.RawMessage, change func(formats []map[string]any)) {
		var formats []map[string]any
		if err := json.Unmarshal(raw["customformat"], &formats); err != nil {
			t.Fatal(err)
		}
		change(formats)
		raw["customformat"], _ = json.Marshal(formats)
	}
	for _, tc := range []struct {
		name, want string
		change     func(t *testing.T, raw map[string]json.RawMessage)
	}{
		{"indexer flags", "native schema cannot express", func(t *testing.T, raw map[string]json.RawMessage) {
			raw["config/indexer"] = json.RawMessage(`{"preferIndexerFlags":true}`)
		}},
		{"maximum size", "native schema cannot express", func(t *testing.T, raw map[string]json.RawMessage) {
			raw["config/indexer"] = json.RawMessage(`{"maximumSize":100}`)
		}},
		{"duplicate format name", "duplicate custom format name", func(t *testing.T, raw map[string]json.RawMessage) {
			edit(t, raw, func(f []map[string]any) { f[1]["name"] = f[0]["name"] })
		}},
		{"duplicate format id", "duplicate custom format id", func(t *testing.T, raw map[string]json.RawMessage) {
			edit(t, raw, func(f []map[string]any) { f[1]["id"] = f[0]["id"] })
		}},
		{"rename off", "cannot express rename=false", func(t *testing.T, raw map[string]json.RawMessage) {
			var n map[string]any
			if err := json.Unmarshal(raw["config/naming"], &n); err != nil {
				t.Fatal(err)
			}
			n["renameMovies"] = false
			raw["config/naming"], _ = json.Marshal(n)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := freezeFixture(t)
			tc.change(t, raw)
			if _, err := freezeArr("radarr_4k_debrid", "radarr", raw, ""); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
}

func TestFreezeArrProfileFields(t *testing.T) {
	t.Parallel()
	raw := freezeFixture(t)
	var profiles []map[string]any
	if err := json.Unmarshal(raw["qualityprofile"], &profiles); err != nil {
		t.Fatal(err)
	}
	profiles[0]["minUpgradeFormatScore"] = 0
	profiles[0]["upgradeAllowed"] = false
	profiles[0]["minFormatScore"] = -50
	profiles[0]["cutoffFormatScore"] = 12345
	raw["qualityprofile"], _ = json.Marshal(profiles)
	raw["config/mediamanagement"] = json.RawMessage(`{"downloadPropersAndRepacks":"doNotPrefer"}`)
	e, err := freezeArr("radarr_4k_debrid", "radarr", raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Unsupported) != 1 || !strings.Contains(e.Unsupported[0], "native profile keys replace its display name") {
		t.Errorf("unsupported %v", e.Unsupported)
	}
	defs, err := format.ParseArrFormats(e.Snapshot.Flavour, e.Snapshot.CustomFormat)
	if err != nil {
		t.Fatal(err)
	}
	native, _, err := e.Fragments()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range native {
		if p.Upgrades == nil || *p.Upgrades || p.MinUpgradeStep == nil || *p.MinUpgradeStep != 0 || p.MinScore != -50 ||
			p.CutoffScore != 12345 || p.Language != "original" || p.Propers != policy.PropersDoNotPrefer {
			t.Fatalf("lost profile fields: %+v", p)
		}
		if len(p.Formats) != len(defs) {
			t.Fatal("lost zero-score formats")
		}
		if p.Sizes["Remux-2160p"].Min != 187.4 {
			t.Fatal("lost quality definitions")
		}
	}
}
