package settings

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/policy"
)

// testdata/trashpresets is a minimal copy of the TRaSH guides tree (SOURCE.txt names the commit): two Radarr
// quality profiles, their default cf-groups and the custom formats they reference, unchanged.
const (
	presetWeb2160      = "0537bd27bcf8da604f30a862343b1742" // WEB 2160p
	presetRemuxWeb2160 = "fd161a61e3ab826d3a22d53f935696dd" // Remux + WEB 2160p
)

func presetTrash(t *testing.T) *format.Trash {
	t.Helper()
	tr, err := format.LoadTrash("testdata/trashpresets", format.Radarr)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func names(items []policy.QualityItem) []string {
	out := []string{}
	for _, q := range items {
		out = append(out, q.Name)
	}
	return out
}

func TestBuildPresetFromTrashJSON(t *testing.T) {
	t.Parallel()
	tr := presetTrash(t)
	build, err := BuildPreset(tr, presetWeb2160, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pr := build.Profile
	if got := names(pr.Qualities); !reflect.DeepEqual(got, []string{"WEB 2160p"}) || !pr.Qualities[0].Group ||
		!reflect.DeepEqual(pr.Qualities[0].Qualities, []string{"WEBRip-2160p", "WEBDL-2160p"}) {
		t.Fatalf("qualities %+v", pr.Qualities)
	}
	if pr.Cutoff != "WEB 2160p" || pr.CutoffScore != 10000 || pr.MinScore != 0 || *pr.MinUpgradeStep != 1 || !*pr.Upgrades || pr.Language != "original" {
		t.Fatalf("settings %+v", pr)
	}
	// Every quality is ordered, the disallowed ones below the allowed.
	if len(pr.QualityOrder) != 25 || pr.QualityOrder[0].Name != "WEB 2160p" || pr.QualityOrder[1].Name != "WEB 1080p" {
		t.Fatalf("quality order %v", names(pr.QualityOrder))
	}
	// formatItems plus the required and default formats of the default cf-groups that include the profile, scored
	// from TRaSH's default score set. Audio formats belong to a group that excludes WEB 2160p.
	want := map[string]int{"WEB Tier 01": 1700, "WEB Tier 03": 1600, "Repack/Proper": 5, "Repack3": 7, "BR-DISK": -10000,
		"x265 (no HDR/DV)": -10000, "AMZN": 0, "HDR": 500}
	for name, score := range want {
		if got, ok := pr.Formats[name]; !ok || got != score {
			t.Errorf("%s = %d, %v; want %d", name, got, ok, score)
		}
	}
	for _, absent := range []string{"DD+", "TrueHD ATMOS", "Remux Tier 01", "UHD Bluray Tier 01", "DV (w/o HDR fallback)"} {
		if _, ok := pr.Formats[absent]; ok {
			t.Errorf("%s scored without a modifier or default group", absent)
		}
	}
	if pr.Propers != "do_not_prefer" {
		t.Fatalf("propers %q: TRaSH scores repacks through formats", pr.Propers)
	}
	// The format set carries each format's TRaSH JSON, unchanged, keyed by display name.
	ref := build.FormatSet.Config.Formats["WEB Tier 01"]
	if ref == nil || ref.TrashID != "c20f169ef63c5f40c2def54abaf4438e" || string(build.FormatSet.Specifications["WEB Tier 01"]) != string(tr.Raw(ref.TrashID)) {
		t.Fatalf("format set entry %+v", ref)
	}
	if len(build.FormatSet.Config.Formats) != len(pr.Formats) || build.FormatSet.Config.Flavour != "radarr" {
		t.Fatalf("format set %d formats for %d scores", len(build.FormatSet.Config.Formats), len(pr.Formats))
	}
	if !reflect.DeepEqual(build.Origin, ProfileOrigin{Preset: presetWeb2160, Name: "WEB 2160p", Flavour: "radarr"}) {
		t.Fatalf("origin %+v", build.Origin)
	}

	// A standalone format with the same trash_id is referenced, not copied.
	globals := []CustomFormat{{Record: Record{Key: "web-t1"}, Config: policy.FormatRef{TrashID: "c20f169ef63c5f40c2def54abaf4438e"}}}
	build, err = BuildPreset(tr, presetWeb2160, nil, globals)
	if err != nil {
		t.Fatal(err)
	}
	if ref := build.FormatSet.Config.Formats["WEB Tier 01"]; ref.Ref != "web-t1" || build.FormatSet.Specifications["WEB Tier 01"] != nil {
		t.Fatalf("global format copied: %+v", ref)
	}

	presets, err := Presets(tr)
	if err != nil {
		t.Fatal(err)
	}
	if len(presets) != 2 || presets[0].Name != "Remux + WEB 2160p" || presets[0].Group != 1 || presets[1].Name != "WEB 2160p" ||
		presets[1].Description != "Quality Profile that covers: - WEBDL: 2160p" || presets[1].Formats != len(pr.Formats) {
		t.Fatalf("presets %+v", presets)
	}
}

func TestPresetModifiers(t *testing.T) {
	t.Parallel()
	tr := presetTrash(t)
	build, err := BuildPreset(tr, presetRemuxWeb2160, []string{ModBlockDVNoHDR, ModFallbacks1080p, ModBluray2160p}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pr := build.Profile
	// Bluray-2160p right below Remux-2160p; the 1080p fallbacks below every 2160p quality, best first.
	if got := names(pr.Qualities); !reflect.DeepEqual(got, []string{"Remux-2160p", "Bluray-2160p", "WEB 2160p", "Remux-1080p", "Bluray-1080p", "WEB 1080p"}) {
		t.Fatalf("qualities %v", got)
	}
	if pr.Cutoff != "Remux-2160p" || len(build.Notes) != 0 {
		t.Fatalf("cutoff %q notes %v", pr.Cutoff, build.Notes)
	}
	for name, score := range map[string]int{"UHD Bluray Tier 01": 1800, "UHD Bluray Tier 03": 1700, "DV (w/o HDR fallback)": -10000, "Remux Tier 01": 1950, "DD+": 1750} {
		if got, ok := pr.Formats[name]; !ok || got != score {
			t.Errorf("%s = %d, %v; want %d", name, got, ok, score)
		}
	}
	if !reflect.DeepEqual(build.Origin.Modifiers, []string{ModFallbacks1080p, ModBluray2160p, ModBlockDVNoHDR}) {
		t.Fatalf("modifiers kept in their own order: %v", build.Origin.Modifiers)
	}
	// The ordered list still holds every quality once.
	seen := map[string]bool{}
	for _, q := range pr.QualityOrder {
		if seen[q.Name] {
			t.Fatalf("%s ordered twice", q.Name)
		}
		seen[q.Name] = true
	}

	// On a WEB-only preset, Bluray encodes become the best quality and the cutoff stays: the build says so.
	build, err = BuildPreset(tr, presetWeb2160, []string{ModBluray2160p}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(build.Profile.Qualities); !reflect.DeepEqual(got, []string{"Bluray-2160p", "WEB 2160p"}) || len(build.Notes) != 1 {
		t.Fatalf("qualities %v notes %v", got, build.Notes)
	}

	var verr *policy.Errors
	if _, err := BuildPreset(tr, presetWeb2160, []string{"allow_cam"}, nil); !errors.As(err, &verr) {
		t.Fatalf("unknown modifier accepted: %v", err)
	}
	if _, err := BuildPreset(tr, "no-such-profile", nil, nil); !errors.As(err, &verr) {
		t.Fatalf("unknown preset accepted: %v", err)
	}
}

// presetStore is a bootstrapped settings store with a Radarr TRaSH sync source to bind new profiles to.
func presetStore(t *testing.T) (*Store, *policy.Policy, *Snapshot) {
	t.Helper()
	st, _, p := setup(t)
	snap := qualityBootstrap(t, st, p)
	req := saveRequest(p, snap)
	req.Records.SyncSources = append(req.Records.SyncSources, QualitySyncSource{Record: Record{Key: "trash-radarr"},
		Config: QualitySyncConfig{Kind: "trash", Flavour: "radarr", Schedule: "24h", Mode: "review"}})
	snap, err := st.Save(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return st, p, snap
}

func TestApplyPresetStoresOriginAndFollowsTrash(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, p, snap := presetStore(t)
	tr := presetTrash(t)
	build, err := BuildPreset(tr, presetWeb2160, []string{ModFallbacks1080p}, snap.Records.Formats)
	if err != nil {
		t.Fatal(err)
	}
	req := saveRequest(p, snap)
	source, err := ApplyPreset(&req.Records, "web-2160p-trash", build, nil)
	if err != nil || source != "trash-radarr" {
		t.Fatalf("bound to %q: %v", source, err)
	}
	if _, err := ApplyPreset(&req.Records, "web-2160p-trash", build, nil); err == nil {
		t.Fatal("duplicate key accepted")
	}
	if _, err := st.Save(ctx, req); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.Load(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	var stored *QualityProfile
	for i := range loaded.Records.Profiles {
		if loaded.Records.Profiles[i].Key == "web-2160p-trash" {
			stored = &loaded.Records.Profiles[i]
		}
	}
	if stored == nil || stored.Origin == nil || stored.Origin.Preset != presetWeb2160 || !reflect.DeepEqual(stored.Origin.Modifiers, []string{ModFallbacks1080p}) {
		t.Fatalf("origin not stored: %+v", stored)
	}
	pr := loaded.Policy.Profiles["web-2160p-trash"]
	if pr == nil || pr.FormatSet != "web-2160p-trash" || pr.Formats["WEB Tier 01"] != 1700 {
		t.Fatalf("runtime profile %+v", pr)
	}
	var bound QualitySyncSource
	for _, s := range loaded.Records.SyncSources {
		if s.Key == "trash-radarr" {
			bound = s
		}
	}
	state := bound.Config.ScoreState["web-2160p-trash"]
	if bound.Config.Profiles["web-2160p-trash"] != "default" || state.Set != "default" || len(build.Unscored) == 0 || len(state.Scores) != len(pr.Formats)-len(build.Unscored) || state.Scores["HDR"].Trash != 500 {
		t.Fatalf("binding %+v state %+v", bound.Config.Profiles, state)
	}

	// Every score of the new profile follows TRaSH, and the profile is its preset with nothing changed.
	ov := QualityOverviewOf(OverviewInput{Policy: loaded.Policy, Profiles: loaded.Records.Profiles, Formats: loaded.Records.Formats,
		Sources: loaded.Records.SyncSources, Trash: map[string]*format.Trash{"radarr": tr}})
	s := profileSummary(t, ov, "web-2160p-trash")
	if s.Guessed || s.Origin == nil || s.Diff == nil || !s.Diff.Same || s.Binding != "trash-radarr" || len(s.UsedBy) != 0 || s.Flavour != "radarr" {
		t.Fatalf("summary %+v diff %+v", s, s.Diff)
	}
	for _, row := range s.Scores {
		if row.Trash == nil && row.State == StateNoScore {
			continue
		}
		if row.State != StateFollows || row.Definition != DefinitionTrash || row.Trash == nil || *row.Trash != row.Score {
			t.Fatalf("score %+v", row)
		}
	}
	// TRaSH scores streaming services only in other score sets: no score to follow, so the 0 is ours.
	if row := scoreRow(t, s, "AMZN"); row.State != StateNoScore || row.Score != 0 {
		t.Fatalf("AMZN %+v", row)
	}
	if row := scoreRow(t, s, "HDR"); row.Group != "[HDR Formats] HDR" {
		t.Fatalf("HDR grouped as %q", row.Group)
	}
	if row := scoreRow(t, s, "WEB Tier 01"); row.Group != groupProfile {
		t.Fatalf("tier grouped as %q", row.Group)
	}
	// The formats page lists each TRaSH format once with the profiles scoring it.
	web := formatSummary(t, ov, "WEB Tier 01")
	if web.Definition != DefinitionTrash || !slices.ContainsFunc(web.ScoredBy, func(u FormatUse) bool { return u.Profile == "web-2160p-trash" && u.State == StateFollows }) {
		t.Fatalf("format %+v", web)
	}
}

func profileSummary(t *testing.T, ov QualityOverview, key string) ProfileSummary {
	t.Helper()
	for _, s := range ov.Profiles {
		if s.Key == key {
			return s
		}
	}
	t.Fatalf("no profile %s", key)
	return ProfileSummary{}
}

func scoreRow(t *testing.T, s ProfileSummary, name string) ScoreRow {
	t.Helper()
	for _, r := range s.Scores {
		if r.Format == name {
			return r
		}
	}
	t.Fatalf("%s has no score %s", s.Key, name)
	return ScoreRow{}
}

func formatSummary(t *testing.T, ov QualityOverview, name string) FormatSummary {
	t.Helper()
	for _, f := range ov.Formats {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no format %s", name)
	return FormatSummary{}
}

// A worked example: a live profile reads as "preset + our edits", whether its origin is stored or guessed, and each
// edited score has its score state.
func TestOriginDiffAndGuess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, p, snap := presetStore(t)
	tr := presetTrash(t)
	build, err := BuildPreset(tr, presetWeb2160, []string{ModFallbacks1080p}, nil)
	if err != nil {
		t.Fatal(err)
	}
	edited := build.Profile
	edited.Formats = map[string]int{}
	for k, v := range build.Profile.Formats {
		edited.Formats[k] = v
	}
	edited.Formats["WEB Tier 01"] = 2000 // ours
	delete(edited.Formats, "Repack3")    // not scored
	edited.Formats["Our Group"] = 150    // a format of ours
	edited.Qualities = slices.DeleteFunc(slices.Clone(edited.Qualities), func(q policy.QualityItem) bool { return q.Name == "Bluray-1080p" })
	edited.MinScore = 100
	req := saveRequest(p, snap)
	if _, err := ApplyPreset(&req.Records, "radarr-4k-web", build, &edited); err != nil {
		t.Fatal(err)
	}
	set := &req.Records.FormatSets[len(req.Records.FormatSets)-1]
	set.Config.Formats["Our Group"] = &policy.FormatRef{Flavour: "radarr"}
	set.Specifications["Our Group"] = json.RawMessage(`{"name":"Our Group","includeCustomFormatWhenRenaming":false,"specifications":[{"name":"group","implementation":"ReleaseGroupSpecification","negate":false,"required":true,"fields":{"value":"^(OURS)$"}}]}`)
	delete(set.Config.Formats, "Repack3")
	delete(set.Specifications, "Repack3")
	if _, err := st.Save(ctx, req); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.Load(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	in := OverviewInput{Policy: loaded.Policy, Profiles: loaded.Records.Profiles, Formats: loaded.Records.Formats, Sources: loaded.Records.SyncSources,
		Held: []HeldScore{{Profile: "radarr-4k-web", Format: "HDR", Trash: 600}}, Trash: map[string]*format.Trash{"radarr": tr}}
	s := profileSummary(t, QualityOverviewOf(in), "radarr-4k-web")
	d := s.Diff
	if d == nil || d.Same || d.Preset != "WEB 2160p + 1080p fallbacks" {
		t.Fatalf("diff %+v", d)
	}
	if !reflect.DeepEqual(d.QualitiesRemoved, []string{"Bluray-1080p"}) || len(d.QualitiesAdded) != 0 || d.Cutoff != nil {
		t.Fatalf("qualities %+v", d)
	}
	if len(d.ScoresChanged) != 1 || d.ScoresChanged[0].Format != "WEB Tier 01" || *d.ScoresChanged[0].Trash != 1700 ||
		len(d.FormatsAdded) != 1 || d.FormatsAdded[0].Format != "Our Group" || d.FormatsAdded[0].TrashID != "" ||
		len(d.FormatsMissing) != 1 || d.FormatsMissing[0].Format != "Repack3" {
		t.Fatalf("formats %+v", d)
	}
	wantSummary := []string{"− Bluray-1080p", "min score 100 (TRaSH 0)", "WEB Tier 01 +2000 (TRaSH 1700)", "Our Group +150 (ours)", "1 of the preset's scored formats not scored"}
	if !reflect.DeepEqual(d.Summary, wantSummary) {
		t.Fatalf("summary %q", d.Summary)
	}
	// Score states: the edited score was not equal to TRaSH's when bound, so it is ours (pinned); the held one waits.
	for name, want := range map[string]string{"WEB Tier 01": StatePinned, "HDR": StateHeld, "Our Group": StateOurs, "WEB Tier 02": StateFollows} {
		if got := scoreRow(t, s, name).State; got != want {
			t.Errorf("%s state %s, want %s", name, got, want)
		}
	}
	if s.Held != 1 {
		t.Fatalf("held %d", s.Held)
	}

	// Without a stored origin the closest preset is found and marked as a guess.
	for i := range in.Profiles {
		if in.Profiles[i].Key == "radarr-4k-web" {
			in.Profiles[i].Origin = nil
		}
	}
	guessed := profileSummary(t, QualityOverviewOf(in), "radarr-4k-web")
	if !guessed.Guessed || guessed.Origin == nil || guessed.Origin.Preset != presetWeb2160 || !reflect.DeepEqual(guessed.Origin.Modifiers, []string{ModFallbacks1080p}) {
		t.Fatalf("guess %+v", guessed.Origin)
	}
	// Unbound, the same edit has no score state: TRaSH's score is shown only.
	in.Sources = nil
	in.Held = nil
	if got := scoreRow(t, profileSummary(t, QualityOverviewOf(in), "radarr-4k-web"), "WEB Tier 01").State; got != StateUnbound {
		t.Fatalf("unbound state %s", got)
	}
	// The frozen example profiles of the fixture compare with nothing and say so: no TRaSH id, no guess.
	if rem := profileSummary(t, QualityOverviewOf(in), "remux-2160p"); len(rem.UsedBy) == 0 || rem.UsedBy[0].Key == "" {
		t.Fatalf("used by %+v", rem.UsedBy)
	}
}

func TestProfileDefaultsStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, p, snap := presetStore(t)
	got, err := st.ProfileDefaults(ctx)
	if err != nil || len(got) != 0 {
		t.Fatalf("defaults %v %v", got, err)
	}
	if _, err := st.SetProfileDefaults(ctx, snap.Policy, "admin", ProfileDefaults{"movie": "web-2160p", "series": "no-such"}); err == nil {
		t.Fatal("unknown profile accepted")
	}
	if _, err := st.SetProfileDefaults(ctx, snap.Policy, "admin", ProfileDefaults{"music": "web-2160p"}); err == nil {
		t.Fatal("unknown category accepted")
	}
	saved, err := st.SetProfileDefaults(ctx, snap.Policy, "admin", ProfileDefaults{"movie": "web-2160p", "anime_series": "anime-tv", "series": ""})
	if err != nil {
		t.Fatal(err)
	}
	want := ProfileDefaults{"movie": "web-2160p", "anime_series": "anime-tv"}
	if !reflect.DeepEqual(saved, want) {
		t.Fatalf("saved %v", saved)
	}
	if got, _ := st.ProfileDefaults(ctx); !reflect.DeepEqual(got, want) {
		t.Fatalf("read back %v", got)
	}
	// Audited at the head generation, which it does not advance.
	rows := audit(t, st)
	last := rows[len(rows)-1]
	if last.Entity != "profile_defaults" || last.Generation != snap.Generation || last.Actor != "admin" {
		t.Fatalf("audit %+v", last)
	}
	if head, _ := st.Load(ctx, p); head.Generation != snap.Generation {
		t.Fatalf("generation moved to %d", head.Generation)
	}
	if saved, err := st.SetProfileDefaults(ctx, snap.Policy, "admin", ProfileDefaults{}); err != nil || len(saved) != 0 {
		t.Fatalf("clear %v %v", saved, err)
	}
}
