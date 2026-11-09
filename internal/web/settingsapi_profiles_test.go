package web

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

const presetsTrash = "../settings/testdata/trashpresets"
const webPreset = "0537bd27bcf8da604f30a862343b1742" // TRaSH's Radarr "WEB 2160p"

type impactCall struct{ before, after *policy.Policy }

// newProfileEnv is newQualityEnv with the TRaSH presets tree and an impact function that records its policies.
func newProfileEnv(t *testing.T, calls *[]impactCall) *settingsEnv {
	t.Helper()
	var coord *settings.RuntimeCoordinator
	se := newSettingsEnvBase(t, nil, nil, func(c *Config) {
		st, err := settings.New(c.Store)
		if err != nil {
			t.Fatal(err)
		}
		st.SetTrashRoot("../settings/testdata/trash")
		snap, err := st.BootstrapQuality(context.Background(), c.Policy, "upgrade")
		if err != nil {
			t.Fatal(err)
		}
		coord, err = settings.NewRuntimeCoordinator(st, c.Policy, snap, fakeRuntime{}, func(context.Context, *settings.Snapshot, *settings.Runtime) (any, error) { return fakeRuntime{}, nil })
		if err != nil {
			t.Fatal(err)
		}
		c.Settings, c.TrashRoot = coord, presetsTrash
		c.QualitySync = &settings.QualitySync{Store: st, Coordinator: coord, TrashRoot: "../settings/testdata/trash", Impact: func(_ context.Context, before, after *policy.Policy) (settings.DecisionImpact, error) {
			*calls = append(*calls, impactCall{before, after})
			return settings.DecisionImpact{Files: 3, ScoreChanged: 2, Examples: []settings.ImpactExample{{Title: "Dune", Version: "movie-remux", BeforeScore: 1900, AfterScore: 2400}}}, nil
		}}
	})
	se.coord = coord
	return se
}

func decodeAs[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestProfilePresetsAndOverviewAPI(t *testing.T) {
	t.Parallel()
	var calls []impactCall
	se := newProfileEnv(t, &calls)
	res := se.call("GET", "/presets?flavour=radarr", nil)
	se.want(res, 200, "presets")
	presets := decodeAs[struct {
		Presets []settings.Preset `json:"presets"`
	}](t, res.Body.Bytes())
	if len(presets.Presets) != 2 || presets.Presets[1].TrashID != webPreset || len(presets.Presets[1].Modifiers) != 3 {
		t.Fatalf("presets %+v", presets)
	}
	se.want(se.call("GET", "/presets?flavour=lidarr", nil), 400, "unknown flavour")
	if out := se.want(se.call("GET", "/presets?flavour=sonarr", nil), 200, "no sonarr tree in the fixture"); out["unavailable"] == nil || len(out["presets"].([]any)) != 0 {
		t.Fatalf("presets without a sonarr tree: %v", out)
	}

	res = se.call("POST", "/presets/build", map[string]any{"flavour": "radarr", "preset": webPreset, "modifiers": []string{"fallbacks_1080p"}})
	se.want(res, 200, "build")
	built := decodeAs[struct {
		Profile map[string]any         `json:"profile"`
		Origin  settings.ProfileOrigin `json:"origin"`
	}](t, res.Body.Bytes())
	if len(built.Profile["qualities"].([]any)) != 4 || built.Origin.Preset != webPreset {
		t.Fatalf("built %+v", built)
	}
	se.want(se.call("POST", "/presets/build", map[string]any{"flavour": "radarr", "preset": webPreset, "modifiers": []string{"nope"}}), 422, "unknown modifier")

	// A TRaSH sync source to bind to, then a profile from the preset with one score of ours.
	se.want(se.call("POST", "/sync_sources", map[string]any{"generation": se.generation(), "key": "trash-radarr",
		"config": map[string]any{"kind": "trash", "flavour": "radarr", "mode": "review", "schedule": "24h"}}), 200, "sync source")
	config := built.Profile
	config["formats"].(map[string]any)["HDR"] = 900
	gen := se.generation()
	res = se.call("POST", "/presets/create", map[string]any{"generation": gen, "key": "web-trash", "flavour": "radarr", "preset": webPreset,
		"modifiers": []string{"fallbacks_1080p"}, "config": config})
	out := se.want(res, 200, "create from preset")
	if out["binding"] != "trash-radarr" || int64(out["generation"].(float64)) != gen+1 {
		t.Fatalf("create %v", out)
	}
	se.want(se.call("POST", "/presets/create", map[string]any{"generation": se.generation(), "key": "web-trash", "flavour": "radarr", "preset": webPreset}), 422, "duplicate key")

	res = se.call("GET", "/quality/overview", nil)
	se.want(res, 200, "overview")
	ov := decodeAs[qualityOverviewAnswer](t, res.Body.Bytes())
	if ov.Source != "db" || ov.Generation != se.generation() || !reflect.DeepEqual(ov.Trash, []string{"radarr"}) || len(ov.Formats) == 0 ||
		!reflect.DeepEqual(ov.Categories, []string{"movie", "series", "anime_movie", "anime_series"}) {
		t.Fatalf("overview %+v", ov)
	}
	var web, remux settings.ProfileSummary
	for _, p := range ov.Profiles {
		switch p.Key {
		case "web-trash":
			web = p
		case "remux-2160p":
			remux = p
		}
	}
	if web.Origin == nil || web.Guessed || web.Diff == nil || !reflect.DeepEqual(web.Diff.Summary, []string{"HDR +900 (TRaSH 500)"}) || len(web.UsedBy) != 0 || web.Binding != "trash-radarr" {
		t.Fatalf("web-trash %+v %+v", web, web.Diff)
	}
	for _, s := range web.Scores {
		if s.Format == "HDR" && s.State != settings.StatePinned {
			t.Fatalf("our HDR score %+v", s)
		}
	}
	if len(remux.UsedBy) != 1 || remux.UsedBy[0].Key != "movie-remux" || remux.UsedBy[0].Active {
		t.Fatalf("remux used by %+v", remux.UsedBy)
	}

	// Reads return the origin; a plain edit keeps it; null clears it.
	id := qualityID(t, se, "profiles", "web-trash")
	res = se.call("GET", fmt.Sprintf("/profiles/%d", id), nil)
	se.want(res, 200, "read")
	rec := decodeAs[QualityRecord](t, res.Body.Bytes())
	if rec.Origin == nil || rec.Origin.Preset != webPreset || rec.Config["origin"] != nil {
		t.Fatalf("record %+v", rec)
	}
	rec.Config["min_score"] = 10
	se.want(se.call("PUT", fmt.Sprintf("/profiles/%d", id), map[string]any{"generation": se.generation(), "config": rec.Config}), 200, "plain edit")
	res = se.call("GET", fmt.Sprintf("/profiles/%d", id), nil)
	if decodeAs[QualityRecord](t, res.Body.Bytes()).Origin == nil {
		t.Fatal("edit dropped the origin")
	}
	se.want(se.call("PUT", fmt.Sprintf("/profiles/%d", id), map[string]any{"generation": se.generation(), "config": rec.Config, "origin": map[string]any{"preset": "x", "bogus": 1}}), 422, "strict origin")
	se.want(se.call("PUT", fmt.Sprintf("/profiles/%d", id), map[string]any{"generation": se.generation(), "config": rec.Config, "origin": nil}), 200, "clear origin")
	res = se.call("GET", fmt.Sprintf("/profiles/%d", id), nil)
	if decodeAs[QualityRecord](t, res.Body.Bytes()).Origin != nil {
		t.Fatal("origin not cleared")
	}
}

func TestProfileImpactForPlainEdit(t *testing.T) {
	t.Parallel()
	var calls []impactCall
	se := newProfileEnv(t, &calls)
	id := qualityID(t, se, "profiles", "remux-2160p")
	rec := decodeAs[QualityRecord](t, se.call("GET", fmt.Sprintf("/profiles/%d", id), nil).Body.Bytes())
	rec.Config["formats"].(map[string]any)["Remux Tier 01"] = 2400
	gen := se.generation()
	res := se.call("POST", "/profiles/impact", map[string]any{"generation": gen, "key": "remux-2160p", "config": rec.Config})
	se.want(res, 200, "impact")
	answer := decodeAs[profileImpactAnswer](t, res.Body.Bytes())
	if !answer.Available || answer.Impact.Files != 3 || len(answer.Impact.Examples) != 1 || len(answer.Versions) != 1 || answer.Versions[0].Key != "movie-remux" {
		t.Fatalf("impact %+v", answer)
	}
	// The engine compared the stored policy with the edited one, and nothing was saved.
	if len(calls) != 1 || calls[0].before.Profiles["remux-2160p"].Formats["Remux Tier 01"] == 2400 || calls[0].after.Profiles["remux-2160p"].Formats["Remux Tier 01"] != 2400 {
		t.Fatalf("impact policies %+v", calls)
	}
	if se.generation() != gen {
		t.Fatal("impact saved")
	}
	se.want(se.call("POST", "/profiles/impact", map[string]any{"generation": gen, "key": "missing", "config": rec.Config}), 404, "unknown profile")
	se.want(se.call("POST", "/profiles/impact", map[string]any{"generation": gen - 1, "key": "remux-2160p", "config": rec.Config}), 409, "stale generation")
	rec.Config["cutoff"] = "no-such-quality"
	se.want(se.call("POST", "/profiles/impact", map[string]any{"generation": gen, "key": "remux-2160p", "config": rec.Config}), 422, "invalid edit")
	if len(calls) != 1 {
		t.Fatal("impact ran for an invalid edit")
	}
}

func TestProfileDefaultsAPI(t *testing.T) {
	t.Parallel()
	var calls []impactCall
	se := newProfileEnv(t, &calls)
	out := se.want(se.call("GET", "/profile-defaults", nil), 200, "defaults")
	if len(out["defaults"].(map[string]any)) != 0 {
		t.Fatalf("defaults %v", out)
	}
	se.want(se.call("PUT", "/profile-defaults", map[string]any{"defaults": map[string]string{"movie": "missing"}}), 422, "unknown profile")
	out = se.want(se.call("PUT", "/profile-defaults", map[string]any{"defaults": map[string]string{"movie": "web-2160p", "series": "tv-2160p"}}), 200, "set defaults")
	if !reflect.DeepEqual(out["defaults"], map[string]any{"movie": "web-2160p", "series": "tv-2160p"}) {
		t.Fatalf("defaults %v", out)
	}
	ov := decodeAs[qualityOverviewAnswer](t, se.call("GET", "/quality/overview", nil).Body.Bytes())
	if ov.Defaults["series"] != "tv-2160p" {
		t.Fatalf("overview defaults %v", ov.Defaults)
	}

}

// Without a TRaSH checkout the presets answer is an empty list with the reason (THEATARR_TRASH unset, or set to a
// directory without the guides), not a server error, and the profiles overview carries the same reason.
func TestPresetsWithoutTrashCheckout(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ root, want string }{
		"unset": {"", "THEATARR_TRASH is not set"},
		"empty": {t.TempDir(), "THEATARR_TRASH is set but holds no TRaSH-Guides checkout"},
	} {
		t.Run(name, func(t *testing.T) {
			se := newSettingsEnv(t, nil, func(c *Config) { c.TrashRoot = tc.root })
			for range 3 {
				out := se.want(se.call("GET", "/presets?flavour=radarr", nil), 200, "presets")
				if note, _ := out["unavailable"].(string); !strings.Contains(note, tc.want) || len(out["presets"].([]any)) != 0 {
					t.Fatalf("presets: %v", out)
				}
			}
			overview := se.want(se.call("GET", "/quality/overview", nil), 200, "overview")
			if note, _ := overview["trash_note"].(string); !strings.Contains(note, tc.want) {
				t.Fatalf("overview trash_note: %v", overview["trash_note"])
			}
			if logs := se.logs.String(); strings.Contains(logs, "level=ERROR") {
				t.Fatalf("logged an error:\n%s", logs)
			}
		})
	}
}
