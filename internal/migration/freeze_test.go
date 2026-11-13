package migration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// freezeFixture is radarr_4k_debrid's GET bodies as the wizard reads them, with its naming.
func freezeFixture(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	read := func(name string) json.RawMessage {
		b, err := os.ReadFile("testdata/radarr_4k_debrid/snapshot/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	golden, err := os.ReadFile("../library/naming/testdata/golden/radarr_4k_debrid.json")
	if err != nil {
		t.Fatal(err)
	}
	var g struct{ Naming json.RawMessage }
	if err := json.Unmarshal(golden, &g); err != nil {
		t.Fatal(err)
	}
	return map[string]json.RawMessage{"qualityprofile": read("qualityprofile"), "customformat": read("customformat"),
		"qualitydefinition": read("qualitydefinition"), "config/mediamanagement": json.RawMessage(`{}`),
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

// FreezeStored turns stored from_arr rows into native copies from arr_frozen_input: a profile (matched by name,
// ignoring case) keeps its key and its stored propers and gets a format set; a naming gets the instance's templates;
// a profile whose instance has no snapshot becomes a minimal profile and its library Watch only. The settings then
// load, each library has one message, and a second run reads nothing.
func TestFreezeStored(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	base, err := policy.Load("../../examples/policy")
	if err != nil {
		t.Fatal(err)
	}
	st, err := settings.New(db)
	if err != nil {
		t.Fatal(err)
	}
	st.SetTrashRoot("../settings/testdata/trash")
	lookup := func(name string) (string, bool) { return "fixture-" + name, true }
	if _, err := st.Bootstrap(ctx, base, settings.ImportRequest{Dir: base.Dir, Actor: "test", Lookup: lookup}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BootstrapQuality(ctx, base, "test"); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(freezeFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Writer().ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// arr_frozen_input as migration 00125 fills it from arr_policy_snapshot.
	exec(`INSERT INTO arr_frozen_input (instance, flavour, body) VALUES ('radarr_4k_debrid', 'radarr', ?)`, string(body))
	exec(`UPDATE setting_quality_profile SET config = '{"FromArr":"radarr_4k_debrid/remux + web 2160p","Propers":"do_not_prefer"}'
		WHERE key = 'web-2160p'`)
	exec(`UPDATE setting_quality_profile SET config = '{"FromArr":"sonarr_gone/Any"}' WHERE key = 'tv-2160p'`)
	// A Radarr profile with upgrades off, minimum format score 1 and cutoff score 0 builds in the arr, but the copy
	// would fail validation (cutoff_score below min_score): it falls back.
	odd := freezeFixture(t)
	var qps []map[string]any
	if err := json.Unmarshal(odd["qualityprofile"], &qps); err != nil {
		t.Fatal(err)
	}
	for _, qp := range qps {
		qp["upgradeAllowed"], qp["minFormatScore"], qp["cutoffFormatScore"] = false, 1, 0
	}
	if odd["qualityprofile"], err = json.Marshal(qps); err != nil {
		t.Fatal(err)
	}
	oddBody, err := json.Marshal(odd)
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO arr_frozen_input (instance, flavour, body) VALUES ('radarr_odd', 'radarr', ?)`, string(oddBody))
	exec(`UPDATE setting_quality_profile SET config = '{"FromArr":"radarr_odd/Remux + WEB 2160p"}' WHERE key = 'anime-remux-1080p-movie'`)
	exec(`INSERT INTO setting_version_naming (key, config, created_at, updated_at) VALUES ('movie-remux', '{"FromArr":"radarr_4k_debrid"}', 1, 1)
		ON CONFLICT (key) DO UPDATE SET config = excluded.config`)
	if _, err := st.Load(ctx, base); err == nil {
		t.Fatal("the stored from_arr rows loaded before the copy")
	}

	if err := FreezeStored(ctx, db, time.UnixMilli(5000)); err != nil {
		t.Fatal(err)
	}
	snap, err := st.Load(ctx, base)
	if err != nil {
		t.Fatalf("settings after the copy: %v", err)
	}
	pr := snap.Policy.Profiles["web-2160p"]
	if pr == nil || pr.FormatSet == "" || snap.Policy.FormatSets[pr.FormatSet] == nil || len(pr.Formats) == 0 ||
		pr.Cutoff == "" || pr.Propers != policy.PropersDoNotPrefer {
		t.Fatalf("copied profile %+v", pr)
	}
	if n := snap.Policy.Versions["movie-remux"].Naming; n.Folder != "{Movie CleanTitle} ({Release Year})" || n.Colon != "delete" {
		t.Fatalf("copied naming %+v", n)
	}
	if v := snap.Policy.Versions["tv-2160p"]; v.Mode != policy.ModeShadow || len(snap.Policy.Profiles["tv-2160p"].Qualities) != 1 {
		t.Fatalf("tv-2160p %s, profile %+v", v.Mode, snap.Policy.Profiles["tv-2160p"])
	}
	if v := snap.Policy.Versions["anime-movie"]; v.Mode != policy.ModeShadow {
		t.Fatalf("anime-movie %s", v.Mode)
	}
	if base.Versions["movie-web"].Mode != snap.Policy.Versions["movie-web"].Mode {
		t.Fatalf("movie-web mode %s", snap.Policy.Versions["movie-web"].Mode)
	}
	messages := map[string]string{}
	rows, err := db.Reader().QueryContext(ctx, `SELECT fingerprint, message FROM error_event WHERE fingerprint LIKE 'arr-freeze:%'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k, m string
		if err := rows.Scan(&k, &m); err != nil {
			t.Fatal(err)
		}
		messages[k] = m
	}
	rows.Close()
	for key, want := range map[string]string{"arr-freeze:movie-web": "saved a copy of it. Check it in Settings › Profiles",
		"arr-freeze:movie-remux": "naming taken from Sonarr/Radarr. Theatarr saved a copy",
		"arr-freeze:tv-2160p":    "could not be copied. It is now Watch only",
		"arr-freeze:anime-movie": "could not be copied. It is now Watch only"} {
		if !strings.Contains(messages[key], want) {
			t.Errorf("%s: %q", key, messages[key])
		}
	}
	if len(messages) != 4 {
		t.Errorf("messages %v", messages)
	}
	exec(`DROP TABLE arr_frozen_input`)
	if err := FreezeStored(ctx, db, time.UnixMilli(6000)); err != nil {
		t.Fatalf("second run: %v", err)
	}
}
