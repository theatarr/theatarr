package recyclarr

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

func testOptions() Options {
	return Options{TrashRoot: "testdata/trash"}
}

func parseTestConfig(t *testing.T) *Config {
	t.Helper()
	b, err := os.ReadFile("testdata/recyclarr.yml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestParse(t *testing.T) {
	cfg := parseTestConfig(t)
	if len(cfg.Instances) != 1 {
		t.Fatalf("instances = %d", len(cfg.Instances))
	}
	in := cfg.Instances[0]
	if in.Name != "radarr_x" || in.Flavour != Radarr || len(in.CustomFormats) != 2 || len(in.QualityProfiles) != 1 {
		t.Fatalf("instance = %+v", in)
	}
	if !slices.Contains(in.Ignored, "api_key") || !slices.Contains(in.Ignored, "base_url") {
		t.Errorf("ignored = %v (the !secret key must be skipped, not resolved)", in.Ignored)
	}
	if a := in.CustomFormats[0].AssignTo[0]; a.Name != "P" || a.Score == nil || *a.Score != 500 {
		t.Errorf("assign = %+v", a)
	}
	if in.CustomFormats[1].AssignTo[0].Score != nil {
		t.Error("an assignment without a score must stay without one")
	}
	if in.QualityProfiles[0].ResetUnmatched {
		t.Error("reset_unmatched_scores.enabled false read as true")
	}
}

func TestParseRefuses(t *testing.T) {
	for name, y := range map[string]string{
		"include":       "radarr:\n  x:\n    include:\n      - template: foo\n",
		"groups":        "radarr:\n  x:\n    custom_format_groups:\n      add:\n        - trash_id: abc\n",
		"unknown key":   "radarr:\n  x:\n    nonsense: 1\n",
		"no trash_ids":  "radarr:\n  x:\n    custom_formats:\n      - assign_scores_to:\n          - name: P\n",
		"profile by id": "radarr:\n  x:\n    custom_formats:\n      - trash_ids: [a]\n        assign_scores_to:\n          - trash_id: abc\n",
	} {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestConvert(t *testing.T) {
	_, err := Convert(parseTestConfig(t), testOptions())
	if err == nil || !strings.Contains(err.Error(), "no qualities") {
		t.Fatalf("err = %v: a profile needs its qualities in the config", err)
	}
	cfg := parseTestConfig(t)
	cfg.Instances[0].QualityProfiles[0].Qualities = []ProfileQuality{{Name: "WEBDL-1080p", Enabled: true}}
	min := 25
	cfg.Instances[0].QualityProfiles[0].MinFormatScore = &min
	cfg.Instances[0].QualityProfiles[0].ScoreSet = "sqp-1"
	outs, err := Convert(cfg, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	p := outs[0].Profiles[0]
	// The score set picks TRaSH's sqp-1 score for Fmt B.
	if p.Policy.Formats["Fmt B"] != 70 || p.Policy.Formats["Fmt A"] != 500 || len(p.Policy.Formats) != 2 {
		t.Errorf("scores = %v", p.Policy.Formats)
	}
	if p.Policy.MinScore != 25 || p.Policy.Cutoff != "WEBDL-1080p" {
		t.Errorf("profile = %+v", p.Policy)
	}
}

const sizesConfig = `radarr:
  main:
    quality_definition:
      type: movie
      qualities:
        - name: WEBDL-2160p
          max: "400"
          preferred: "300"
    quality_profiles:
      - name: WEB
        min_format_score: 10
        reset_unmatched_scores:
          enabled: true
          except: [Keep Me]
        qualities:
          - name: WEB 2160p
            qualities: [WEBDL-2160p, WEBRip-2160p]
      - name: Remux
        quality_definition:
          type: movie
          preferred_ratio: 0.5
        qualities:
          - name: Remux-2160p
`

func TestConvertQualityDefinitionSizes(t *testing.T) {
	cfg, err := Parse([]byte(sizesConfig))
	if err != nil {
		t.Fatal(err)
	}
	outs, err := Convert(cfg, Options{TrashRoot: "testdata/trash", QualitySizes: true, Reconcile: true})
	if err != nil {
		t.Fatal(err)
	}
	web, remux := outs[0].Profiles[0], outs[0].Profiles[1]
	if got := web.Policy.Sizes["WEBDL-2160p"]; got != (policy.SizeLimit{Min: 34.5, Preferred: 300, Max: 400}) {
		t.Errorf("WEBDL-2160p = %+v", got)
	}
	if got := web.Policy.Sizes["Remux-2160p"]; got != (policy.SizeLimit{Min: 187.4}) {
		t.Errorf("unlimited max and preferred = %+v", got)
	}
	// The profile's own quality_definition replaces the instance's: no override, preferred halfway to the cap.
	if got := remux.Policy.Sizes["WEBDL-2160p"]; got != (policy.SizeLimit{Min: 34.5, Preferred: 1016.8}) {
		t.Errorf("preferred_ratio = %+v", got)
	}
	for _, f := range []string{"qualities", "min_score", "sizes/WEBDL-2160p"} {
		if !slices.Contains(web.Fields, f) {
			t.Errorf("fields %v lack %s", web.Fields, f)
		}
	}
	if slices.Contains(web.Fields, "cutoff") || slices.Contains(web.Fields, "upgrades") {
		t.Errorf("fields %v name values the config does not set", web.Fields)
	}
	if web.Reset == nil || web.Reset("keep me") || !web.Reset("Other") || remux.Reset != nil {
		t.Error("reset_unmatched_scores and its except list are not reported")
	}
	// Without the option the definition is noted, not applied.
	outs, err = Convert(cfg, Options{TrashRoot: "testdata/trash", Reconcile: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(outs[0].Profiles[0].Policy.Sizes) != 0 || !slices.ContainsFunc(outs[0].Notes, func(n string) bool { return strings.Contains(n, "quality_definition") }) {
		t.Errorf("sizes %v notes %v", outs[0].Profiles[0].Policy.Sizes, outs[0].Notes)
	}
	for name, y := range map[string]string{
		"unknown type":    "radarr:\n  h:\n    quality_definition:\n      type: nope\n    quality_profiles:\n      - name: P\n        qualities:\n          - name: WEBDL-1080p\n",
		"unknown quality": "radarr:\n  h:\n    quality_definition:\n      type: movie\n      qualities:\n        - name: Nope\n          min: \"1\"\n    quality_profiles:\n      - name: P\n        qualities:\n          - name: WEBDL-1080p\n",
		"out of order":    "radarr:\n  h:\n    quality_definition:\n      type: movie\n      qualities:\n        - name: WEBDL-1080p\n          max: \"5\"\n    quality_profiles:\n      - name: P\n        qualities:\n          - name: WEBDL-1080p\n",
	} {
		cfg, err := Parse([]byte(y))
		if err == nil {
			_, err = Convert(cfg, Options{TrashRoot: "testdata/trash", QualitySizes: true})
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
