package decide

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestProfileFormatSets(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := []byte(`{"name":"ignored","specifications":[],"includeCustomFormatWhenRenaming":true}`)
	if err := os.WriteFile(filepath.Join(dir, "format.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	no := false
	p := &policy.Policy{Dir: dir,
		Formats: map[string]*policy.FormatRef{"Global": {File: "format.json", Flavour: "radarr"}},
		FormatSets: map[string]*policy.FormatSet{
			"one": {Flavour: "radarr", Formats: map[string]*policy.FormatRef{"Own name": {Ref: "Global"}}},
			"two": {Flavour: "radarr", Formats: map[string]*policy.FormatRef{"Own name": {File: "format.json", Flavour: "radarr", IncludeWhenRenaming: &no}}},
		}, Profiles: map[string]*policy.Profile{}, Versions: map[string]*policy.VersionPolicy{}}
	for _, key := range []string{"one", "two", "legacy"} {
		set := key
		if key == "legacy" {
			set = ""
		}
		p.Profiles[key] = &policy.Profile{Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p", FormatSet: set}
		p.Versions[key] = &policy.VersionPolicy{Kind: policy.KindMovie, Profile: key}
	}
	eng, err := New(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"one", "two", "legacy"} {
		pr := eng.Profile(key, format.Radarr)
		want := "Own name"
		if key == "legacy" {
			want = "Global"
		}
		if len(pr.Formats) != 1 || pr.Formats[0].Name != want || pr.Formats[0].IncludeWhenRenaming != (key != "two") {
			t.Fatalf("%s: %+v", key, pr.Formats)
		}
	}
	p.Profiles["one"].Formats = map[string]int{"Global": 5}
	if _, err := New(p, Options{}); err == nil {
		t.Fatal("scored global format outside selected set")
	}
	p.Profiles["one"].Formats = nil
	p.Profiles["one"].FormatSet = "missing"
	if _, err := New(p, Options{}); err == nil {
		t.Fatal("unknown set accepted")
	}
	p.Profiles["one"].FormatSet = "two"
	p.FormatSets["two"].Flavour = "sonarr"
	if _, err := New(p, Options{}); err == nil {
		t.Fatal("wrong flavour accepted")
	}
}
