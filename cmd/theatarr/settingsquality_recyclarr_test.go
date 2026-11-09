package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

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

func syncProfile(t *testing.T, svc *settings.QualitySync, coord *settings.RuntimeCoordinator, src settings.QualitySyncSource) (*settings.QualityPreview, *policy.Profile) {
	t.Helper()
	ctx := context.Background()
	preview, err := svc.PreviewAndStore(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if preview.ID != 0 {
		if _, err := svc.Apply(ctx, preview.ID, preview.Generation, "test"); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := coord.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return preview, snap.Policy.Profiles["household-web"]
}

func heldReview(t *testing.T, svc *settings.QualitySync) settings.QualityPreview {
	t.Helper()
	reviews, err := svc.Reviews(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reviews {
		if r.Kind == settings.ReviewScores {
			return r
		}
	}
	return settings.QualityPreview{}
}

func decideField(t *testing.T, svc *settings.QualitySync, coord *settings.RuntimeCoordinator, field, choice string) {
	t.Helper()
	ctx := context.Background()
	snap, err := coord.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r := heldReview(t, svc)
	if _, err := svc.ResolveScores(ctx, r.ID, snap.Generation, []settings.ScoreDecision{{Profile: "household-web", Field: field, Choice: choice}}, "test"); err != nil {
		t.Fatal(err)
	}
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

// An approved Recyclarr sync changes only the fields the config manages, keeping what it never mentions (the SeaDex
// score, the size limit), and a differing managed value is asked, not overwritten.
func TestQualitySyncRecyclarrKeepsUnmanagedValues(t *testing.T) {
	svc, coord, base := recyclarrSync(t)
	src := saveSource(t, coord, base, webConfig("5", "123"), nil)
	preview, got := syncProfile(t, svc, coord, src)
	if got.Formats["Seadex"] != 500 || got.Sizes["WEBDL-2160p"].Max != 400 {
		t.Fatalf("unmanaged profile values lost after sync: formats=%v sizes=%v", got.Formats, got.Sizes)
	}
	if got.Formats["Fmt A"] != 123 {
		t.Errorf("a score the profile did not have is taken: formats=%v", got.Formats)
	}
	// Qualities and min_score differ from ours on the first run: asked once, not applied.
	if fields := heldFields(heldReview(t, svc)); !slices.Equal(fields, []string{"min_score", "qualities"}) {
		t.Fatalf("held %v (preview held %v)", fields, preview.HeldFields)
	}
	if got.MinScore == 5 || len(got.Qualities) != 1 {
		t.Errorf("a held field was applied: %+v", got)
	}
}

func TestQualitySyncRecyclarrFollowsAndHoldsManagedFields(t *testing.T) {
	svc, coord, base := recyclarrSync(t)
	src := saveSource(t, coord, base, webConfig("5", "123"), nil)
	syncProfile(t, svc, coord, src)
	decideField(t, svc, coord, "min_score", "take")
	decideField(t, svc, coord, "qualities", "take")
	_, got := syncProfile(t, svc, coord, src)
	if got.MinScore != 5 || len(got.Qualities) != 2 || got.Qualities[1].Name != "WEBDL-1080p" || got.Cutoff != "WEB 2160p" {
		t.Fatalf("taken fields: min %d qualities %v cutoff %q", got.MinScore, got.Qualities, got.Cutoff)
	}
	// Our own edit stays while the source does not change that field.
	editProfile(t, coord, base, func(p *policy.Profile) { p.MinScore = 7 })
	if preview, got := syncProfile(t, svc, coord, src); got.MinScore != 7 || len(preview.HeldFields) != 0 {
		t.Fatalf("an unchanged source overwrote or asked about our edit: min %d held %v", got.MinScore, preview.HeldFields)
	}
	// The source changes it: held (ours 7, last 5, now 9); a score still equal to the source's follows.
	src = saveSource(t, coord, base, webConfig("9", "150"), nil)
	preview, got := syncProfile(t, svc, coord, src)
	if got.MinScore != 7 || got.Formats["Fmt A"] != 150 {
		t.Fatalf("min %d score %d", got.MinScore, got.Formats["Fmt A"])
	}
	if len(preview.HeldFields) != 1 || string(preview.HeldFields[0].Ours) != "7" || string(preview.HeldFields[0].Last) != "5" || string(preview.HeldFields[0].Upstream) != "9" {
		t.Fatalf("held %+v", preview.HeldFields)
	}
	decideField(t, svc, coord, "min_score", "keep")
	if preview, got := syncProfile(t, svc, coord, src); got.MinScore != 7 || len(preview.HeldFields) != 0 {
		t.Fatalf("a kept value was asked again: min %d held %v", got.MinScore, preview.HeldFields)
	}
	if got.Formats["Seadex"] != 500 || got.Sizes["WEBDL-2160p"].Max != 400 {
		t.Fatalf("unmanaged values lost: %v %v", got.Formats, got.Sizes)
	}
}

// quality_definition applies TRaSH's sizes through the same rules (a size set by hand is asked), and with the
// source's option off it is a skipped note.
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
	if got.Sizes["Remux-2160p"] != (policy.SizeLimit{Min: 187.4}) || got.Sizes["WEBDL-2160p"].Max != 400 {
		t.Fatalf("sizes %v", got.Sizes)
	}
	if fields := heldFields(heldReview(t, svc)); !slices.Equal(fields, []string{"sizes/WEBDL-2160p"}) {
		t.Fatalf("held %v", fields)
	}
	if !slices.ContainsFunc(preview.Fields, func(c settings.ProfileFieldChange) bool { return c.Field == "sizes/Remux-2160p" }) {
		t.Errorf("preview fields %+v", preview.Fields)
	}
	decideField(t, svc, coord, "sizes/WEBDL-2160p", "take")
	snap, _ := coord.Snapshot(context.Background())
	want, _ := json.Marshal(policy.SizeLimit{Min: 34.5, Preferred: 300, Max: 350})
	if have, _ := json.Marshal(snap.Policy.Profiles["household-web"].Sizes["WEBDL-2160p"]); string(have) != string(want) {
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

// reset_unmatched_scores resets scores the config does not assign, through the same rules: our SeaDex score is
// asked once, kept, and not asked again; an excepted score is not touched.
func TestQualitySyncRecyclarrResetUnmatchedAsks(t *testing.T) {
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
	if fields := heldFields(heldReview(t, svc)); !slices.Equal(fields, []string{"formats/Seadex"}) || got.Formats["Seadex"] != 500 || got.Formats["WEB Tier 01"] != 1700 {
		t.Fatalf("held %v formats %v", fields, got.Formats)
	}
	decideField(t, svc, coord, "formats/Seadex", "keep")
	if preview, got := syncProfile(t, svc, coord, src); len(preview.HeldFields) != 0 || got.Formats["Seadex"] != 500 {
		t.Fatalf("held %v formats %v", preview.HeldFields, got.Formats)
	}
}

// In auto mode an upstream score change of a Recyclarr source waits for approval like its other managed
// values.
func TestAutoRecyclarrScoreWaitsForApproval(t *testing.T) {
	svc, coord, base := recyclarrSync(t)
	src := saveSource(t, coord, base, webConfig("5", "123"), nil)
	syncProfile(t, svc, coord, src)
	decideField(t, svc, coord, "min_score", "take")
	decideField(t, svc, coord, "qualities", "take")
	syncProfile(t, svc, coord, src)
	src = saveSource(t, coord, base, webConfig("5", "150"), nil)
	ctx := context.Background()
	snap, err := coord.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	records := snap.Records
	for i := range records.SyncSources {
		if records.SyncSources[i].Key == src.Key {
			records.SyncSources[i].Config.Mode = "auto"
		}
	}
	if snap, err = coord.SaveAndApply(ctx, settings.SaveRequest{ExpectedGeneration: snap.Generation, Actor: "test", Base: base, Records: records}); err != nil {
		t.Fatal(err)
	}
	for _, s := range snap.Records.SyncSources {
		if s.Key == src.Key {
			src = s
		}
	}
	if err = svc.RunSource(ctx, src, true); err != nil {
		t.Fatal(err)
	}
	if snap, err = coord.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if score := snap.Policy.Profiles["household-web"].Formats["Fmt A"]; score != 123 {
		t.Fatalf("unapproved upstream score applied: %d", score)
	}
}

func TestQualitySyncHealthPointsToReviews(t *testing.T) {
	svc, coord, base := recyclarrSync(t)
	src := saveSource(t, coord, base, webConfig("20", "123"), nil)
	preview, err := svc.PreviewAndStore(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if preview.ID == 0 {
		t.Fatal("expected a pending review")
	}
	got := qualitySyncHealth(context.Background(), svc)
	if !strings.Contains(got.Message, "await approval in Reviews") {
		t.Fatalf("health message: %s", got.Message)
	}
}
