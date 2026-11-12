package main

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

const recyclarrTrash = "../../internal/recyclarr/testdata/trash"

// recyclarrSync is a quality sync over the recyclarr test TRaSH tree with profile household-web (web-2160p plus a
// SeaDex score and a WEBDL-2160p size limit the Recyclarr config never mentions).
func recyclarrSync(t *testing.T) (*settings.QualitySync, *settings.RuntimeCoordinator, *policy.Policy) {
	t.Helper()
	ctx := context.Background()
	_, st, base, _ := runtimeFixture(t)
	p := *base.Profiles["web-2160p"]
	p.Formats = map[string]int{"Seadex": 500}
	p.Sizes = map[string]policy.SizeLimit{"WEBDL-2160p": {Min: 25, Max: 400}}
	base.Profiles["household-web"] = &p
	initial, err := st.BootstrapQuality(ctx, base, "test")
	if err != nil {
		t.Fatal(err)
	}
	coord, err := settings.NewRuntimeCoordinator(st, base, initial, nil, func(context.Context, *settings.Snapshot, *settings.Runtime) (any, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	svc := &settings.QualitySync{Store: st, Coordinator: coord, TrashRoot: recyclarrTrash, ConvertRecyclarr: convertQualityRecyclarr, Impact: func(context.Context, *policy.Policy, *policy.Policy) (settings.DecisionImpact, error) {
		return settings.DecisionImpact{}, nil
	}}
	return svc, coord, base
}

// saveSource saves the Recyclarr source "household" with config yml (its remembered state carried, as an edit does)
// and returns it as saved.
func saveSource(t *testing.T, coord *settings.RuntimeCoordinator, base *policy.Policy, yml string, sizes *bool) settings.QualitySyncSource {
	t.Helper()
	ctx := context.Background()
	snap, err := coord.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := settings.QualitySyncConfig{Kind: "recyclarr", Flavour: "radarr", Recyclarr: yml, QualitySizes: sizes}
	records := snap.Records
	records.SyncSources = nil
	for _, s := range snap.Records.SyncSources {
		if s.Key == "household" {
			c.FieldState, c.ScoreState = s.Config.FieldState, s.Config.ScoreState
		} else {
			records.SyncSources = append(records.SyncSources, s)
		}
	}
	records.SyncSources = append(records.SyncSources, settings.QualitySyncSource{Record: settings.Record{Key: "household"}, Config: c})
	snap, err = coord.SaveAndApply(ctx, settings.SaveRequest{ExpectedGeneration: snap.Generation, Actor: "test", Base: base, Records: records})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snap.Records.SyncSources {
		if s.Key == "household" {
			return s
		}
	}
	t.Fatal("source not saved")
	return settings.QualitySyncSource{}
}

func editProfile(t *testing.T, coord *settings.RuntimeCoordinator, base *policy.Policy, edit func(*policy.Profile)) {
	t.Helper()
	ctx := context.Background()
	snap, err := coord.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	records := snap.Records
	records.Profiles = slices.Clone(records.Profiles)
	for i := range records.Profiles {
		if records.Profiles[i].Key == "household-web" {
			edit(&records.Profiles[i].Config)
		}
	}
	if _, err := coord.SaveAndApply(ctx, settings.SaveRequest{ExpectedGeneration: snap.Generation, Actor: "test", Base: base, Records: records}); err != nil {
		t.Fatal(err)
	}
}

// syncProfile runs the source as saved now (a run updates its remembered state) and returns the run's update log
// entry (empty when the run changed nothing) and the profile household-web after it.
func syncProfile(t *testing.T, svc *settings.QualitySync, coord *settings.RuntimeCoordinator, src settings.QualitySyncSource) (*settings.QualityPreview, *policy.Profile) {
	t.Helper()
	ctx := context.Background()
	snap, err := coord.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snap.Records.SyncSources {
		if s.Key == src.Key {
			src = s
		}
	}
	if err := svc.RunSource(ctx, src, true); err != nil {
		t.Fatal(err)
	}
	runs, err := svc.Runs(ctx)
	if err != nil || len(runs) == 0 {
		t.Fatal("no run", err)
	}
	entry := &settings.QualityPreview{}
	if runs[0].ReviewID != nil {
		changes, err := svc.Changes(ctx, 1)
		if err != nil || len(changes) != 1 || changes[0].ID != *runs[0].ReviewID {
			t.Fatalf("run %+v log %+v %v", runs[0], changes, err)
		}
		entry = &changes[0]
	}
	if snap, err = coord.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	return entry, snap.Policy.Profiles["household-web"]
}

func heldFields(r settings.QualityPreview) []string {
	var out []string
	for _, h := range r.HeldFields {
		out = append(out, h.Field)
	}
	return out
}

const recyclarrWEB = `radarr:
  household:
    quality_profiles:
      - name: WEB
        min_format_score: %MIN%
        reset_unmatched_scores:
          enabled: false
        qualities:
          - name: WEB 2160p
            qualities: [WEBDL-2160p, WEBRip-2160p]
          - name: WEBDL-1080p
    custom_formats:
      - trash_ids: [aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa]
        assign_scores_to:
          - name: WEB
            score: %SCORE%
`

func webConfig(min, score string) string {
	return strings.NewReplacer("%MIN%", min, "%SCORE%", score).Replace(recyclarrWEB)
}

// A Recyclarr sync changes only the fields the config manages, keeping what it never mentions (the SeaDex score, the
// size limit); the first run takes the values it manages.
func TestQualitySyncRecyclarrKeepsUnmanagedValues(t *testing.T) {
	svc, coord, base := recyclarrSync(t)
	src := saveSource(t, coord, base, webConfig("5", "123"), nil)
	entry, got := syncProfile(t, svc, coord, src)
	if got.Formats["Seadex"] != 500 || got.Sizes["WEBDL-2160p"].Max != 400 {
		t.Fatalf("unmanaged profile values lost after sync: formats=%v sizes=%v", got.Formats, got.Sizes)
	}
	if got.Formats["Fmt A"] != 123 {
		t.Errorf("a score the profile did not have is taken: formats=%v", got.Formats)
	}
	if got.MinScore != 5 || len(got.Qualities) != 2 || len(entry.HeldFields) != 0 {
		t.Errorf("the first run did not take the managed values: %+v kept %v", got, heldFields(*entry))
	}
}

// A managed value follows the source while it equals the source's last value. A value you changed stays yours: when
// the source changes it, the run keeps it, remembers the new value and notes it; the next run notes nothing.
func TestQualitySyncRecyclarrFollowsAndKeepsManagedFields(t *testing.T) {
	svc, coord, base := recyclarrSync(t)
	src := saveSource(t, coord, base, webConfig("5", "123"), nil)
	_, got := syncProfile(t, svc, coord, src)
	if got.MinScore != 5 || len(got.Qualities) != 2 || got.Qualities[1].Name != "WEBDL-1080p" || got.Cutoff != "WEB 2160p" {
		t.Fatalf("taken fields: min %d qualities %v cutoff %q", got.MinScore, got.Qualities, got.Cutoff)
	}
	// Our own edit stays while the source does not change that field.
	editProfile(t, coord, base, func(p *policy.Profile) { p.MinScore = 7 })
	if preview, got := syncProfile(t, svc, coord, src); got.MinScore != 7 || len(preview.HeldFields) != 0 {
		t.Fatalf("an unchanged source overwrote or noted our edit: min %d kept %v", got.MinScore, preview.HeldFields)
	}
	// The source changes it: kept (ours 7, last 5, now 9); a score still equal to the source's follows.
	src = saveSource(t, coord, base, webConfig("9", "150"), nil)
	preview, got := syncProfile(t, svc, coord, src)
	if got.MinScore != 7 || got.Formats["Fmt A"] != 150 {
		t.Fatalf("min %d score %d", got.MinScore, got.Formats["Fmt A"])
	}
	if len(preview.HeldFields) != 1 || string(preview.HeldFields[0].Ours) != "7" || string(preview.HeldFields[0].Last) != "5" || string(preview.HeldFields[0].Upstream) != "9" {
		t.Fatalf("kept %+v", preview.HeldFields)
	}
	// The next complete run: the source's new value is remembered, so ours stays and nothing is noted.
	if preview, got := syncProfile(t, svc, coord, src); got.MinScore != 7 || len(preview.HeldFields) != 0 {
		t.Fatalf("a kept value was noted again: min %d kept %v", got.MinScore, preview.HeldFields)
	}
	if got.Formats["Seadex"] != 500 || got.Sizes["WEBDL-2160p"].Max != 400 {
		t.Fatalf("unmanaged values lost: %v %v", got.Formats, got.Sizes)
	}
}

// quality_definition applies TRaSH's sizes through the same rules (the first run takes them, a size set by hand
// included), and with the source's option off it is a skipped note.
func TestQualitySyncRecyclarrQualityDefinition(t *testing.T) {
	yml := `radarr:
  household:
    quality_definition:
      type: movie
      qualities:
        - name: WEBDL-2160p
          max: "350"
          preferred: "300"
    media_naming:
      movie:
        rename: true
    custom_formats:
      - trash_ids: [aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa]
        assign_scores_to:
          - name: WEB
            score: 10
`
	svc, coord, base := recyclarrSync(t)
	off := false
	src := saveSource(t, coord, base, yml, &off)
	preview, got := syncProfile(t, svc, coord, src)
	if len(got.Sizes) != 1 || !slices.ContainsFunc(preview.Notes, func(n string) bool { return strings.Contains(n, "quality_definition: skipped") }) || !slices.ContainsFunc(preview.Notes, func(n string) bool { return strings.Contains(n, "media_naming: skipped") }) {
		t.Fatalf("sizes %v notes %v", got.Sizes, preview.Notes)
	}
	src = saveSource(t, coord, base, yml, nil)
	preview, got = syncProfile(t, svc, coord, src)
	if got.Sizes["Remux-2160p"] != (policy.SizeLimit{Min: 187.4}) || len(preview.HeldFields) != 0 {
		t.Fatalf("sizes %v kept %v", got.Sizes, heldFields(*preview))
	}
	if !slices.ContainsFunc(preview.Fields, func(c settings.ProfileFieldChange) bool { return c.Field == "sizes/Remux-2160p" }) {
		t.Errorf("log fields %+v", preview.Fields)
	}
	want, _ := json.Marshal(policy.SizeLimit{Min: 34.5, Preferred: 300, Max: 350})
	if have, _ := json.Marshal(got.Sizes["WEBDL-2160p"]); string(have) != string(want) {
		t.Fatalf("taken size %s, want %s", have, want)
	}
}

func TestQualityRecyclarrConverterNotesDefinitionsOff(t *testing.T) {
	config := "radarr:\n  household:\n    quality_definition:\n      type: movie\n      qualities:\n        - name: WEBDL-2160p\n          max: \"400\"\n"
	got, err := convertQualityRecyclarr([]byte(config), recyclarrTrash, "radarr", false)
	if err != nil || len(got.Notes) == 0 {
		t.Fatalf("accepted a size definition without applying it or a note: %+v %v", got, err)
	}
}

// reset_unmatched_scores resets scores the config does not assign, through the same rules: the first run resets our
// SeaDex score; set again by hand, it stays ours while the source does not change; an excepted score is not touched.
func TestQualitySyncRecyclarrResetUnmatched(t *testing.T) {
	yml := `radarr:
  household:
    quality_profiles:
      - name: WEB
        reset_unmatched_scores:
          enabled: true
          except: [WEB Tier 01]
    custom_formats:
      - trash_ids: [aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa]
        assign_scores_to:
          - name: WEB
            score: 10
`
	svc, coord, base := recyclarrSync(t)
	editProfile(t, coord, base, func(p *policy.Profile) { p.Formats = map[string]int{"Seadex": 500, "WEB Tier 01": 1700} })
	src := saveSource(t, coord, base, yml, nil)
	_, got := syncProfile(t, svc, coord, src)
	if _, ok := got.Formats["Seadex"]; ok || got.Formats["WEB Tier 01"] != 1700 {
		t.Fatalf("formats %v", got.Formats)
	}
	editProfile(t, coord, base, func(p *policy.Profile) {
		p.Formats = maps.Clone(p.Formats)
		p.Formats["Seadex"] = 500
	})
	if preview, got := syncProfile(t, svc, coord, src); len(preview.HeldFields) != 0 || got.Formats["Seadex"] != 500 {
		t.Fatalf("kept %v formats %v", preview.HeldFields, got.Formats)
	}
}

const recyclarrTwoProfiles = `radarr:
  household:
    quality_profiles:
      - name: WEB
        min_format_score: 5
        reset_unmatched_scores:
          enabled: false
        qualities:
          - name: WEB 2160p
            qualities: [WEBDL-2160p, WEBRip-2160p]
%UHD%    custom_formats:
      - trash_ids: [aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa]
        assign_scores_to:
          - name: WEB
            score: 123
`

const recyclarrUHD = `      - name: UHD
        reset_unmatched_scores:
          enabled: false
        qualities:
          - name: Remux-2160p
`

// The profile values a Recyclarr source manages apply by themselves, a changed quality list and a new profile in one
// run; a profile the config no longer lists is kept.
func TestRecyclarrProfileValuesApply(t *testing.T) {
	svc, coord, base := recyclarrSync(t)
	src := saveSource(t, coord, base, webConfig("5", "123"), nil)
	if _, got := syncProfile(t, svc, coord, src); len(got.Qualities) != 2 {
		t.Fatalf("first run qualities %v", got.Qualities)
	}
	src = saveSource(t, coord, base, strings.Replace(recyclarrTwoProfiles, "%UHD%", recyclarrUHD, 1), nil)
	entry, got := syncProfile(t, svc, coord, src)
	snap, _ := coord.Snapshot(context.Background())
	if len(got.Qualities) != 1 || got.Cutoff != "WEB 2160p" || snap.Policy.Profiles["household-uhd"] == nil || !slices.Equal(entry.Profiles, []string{"household-uhd", "household-web"}) {
		t.Fatalf("qualities %v cutoff %q profiles %v", got.Qualities, got.Cutoff, entry.Profiles)
	}
	src = saveSource(t, coord, base, strings.Replace(recyclarrTwoProfiles, "%UHD%", "", 1), nil)
	syncProfile(t, svc, coord, src)
	if snap, _ := coord.Snapshot(context.Background()); snap.Policy.Profiles["household-uhd"] == nil {
		t.Fatal("a profile the config dropped was deleted")
	}
}

func TestQualitySyncHealthUpToDate(t *testing.T) {
	svc, coord, base := recyclarrSync(t)
	src := saveSource(t, coord, base, webConfig("20", "123"), nil)
	syncProfile(t, svc, coord, src)
	got := qualitySyncHealth(context.Background(), svc)
	if got.Type != health.OK || got.Message != "Quality sync is up to date" {
		t.Fatalf("health: %+v", got)
	}
}
