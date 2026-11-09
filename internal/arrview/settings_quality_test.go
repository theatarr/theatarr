package arrview

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestQualityReadsUsePinnedRuntime(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "format.json")
	raw := json.RawMessage(`{"name":"HDR","trash_id":"fixture","includeCustomFormatWhenRenaming":true,"specifications":[{"name":"HDR","implementation":"ReleaseTitleSpecification","negate":false,"required":true,"fields":{"value":"HDR"}}]}`)
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Dir: dir,
		Versions: map[string]*policy.VersionPolicy{"a": {Kind: policy.KindMovie, Profile: "a"}, "b": {Kind: policy.KindMovie, Profile: "b"}},
		Views:    map[string]*policy.View{"radarr": {Flavour: Radarr, Path: "/radarr", Versions: []string{"a", "b"}, Profiles: map[string][]string{"Both": {"a", "b"}, "B": {"b"}}}},
		Profiles: map[string]*policy.Profile{}, FormatSets: map[string]*policy.FormatSet{},
	}
	for _, key := range []string{"a", "b"} {
		p.Profiles[key] = &policy.Profile{Qualities: []policy.QualityItem{{Name: "WEB", Group: true, Qualities: []string{"WEBDL-1080p", "WEBRip-1080p"}}}, Cutoff: "WEB", MinScore: 10, CutoffScore: 100, FormatSet: key, Formats: map[string]int{"HDR": 50}}
		p.FormatSets[key] = &policy.FormatSet{Flavour: Radarr, Formats: map[string]*policy.FormatRef{"HDR": {File: "format.json"}}}
	}
	// The second scope has a different spec with the same display name and local id.
	p.FormatSets["b"].Formats["HDR"] = &policy.FormatRef{Specification: json.RawMessage(`{"name":"HDR","specifications":[{"name":"DV","implementation":"ReleaseTitleSpecification","fields":[{"name":"value","value":"DV"}]}]}`)}
	build := func(pol *policy.Policy) *Service {
		t.Helper()
		engine, err := decide.New(pol, decide.Options{})
		if err != nil {
			t.Fatal(err)
		}
		s, err := New(context.Background(), Config{Store: e.st, Auth: e.au, Policy: pol, Engine: engine, Getenv: func(string) string { return "" }})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	old := build(p)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	read := func(s *Service, score int) {
		t.Helper()
		e.mux = http.NewServeMux()
		s.Mount(e.mux)
		var formats []format.FormatJSON
		e.call("GET", "/radarr/api/v3/customformat", nil, 200, &formats)
		if len(formats) != 2 || len(formats[0].Specifications) != 1 || formats[0].ID == formats[1].ID {
			t.Fatalf("scoped runtime formats: %+v", formats)
		}
		var fields []struct {
			Name  string
			Value string
		}
		if err := json.Unmarshal(formats[0].Specifications[0].Fields, &fields); err != nil || len(fields) != 1 || fields[0].Value != "HDR" {
			t.Fatalf("arr field resources: %s / %v", formats[0].Specifications[0].Fields, err)
		}
		var profiles []profile.ProfileJSON
		e.call("GET", "/radarr/api/v3/qualityprofile", nil, 200, &profiles)
		if len(profiles) != 2 {
			t.Fatal(profiles)
		}
		for _, qp := range profiles {
			if qp.MinFormatScore != score || qp.CutoffFormatScore != 100 || len(qp.RawFormatItems) != 1 || qp.RawFormatItems[0].Score != 50 {
				t.Fatalf("runtime profile: %+v", qp)
			}
			index := 0
			if qp.Name == "B" {
				index = 1
			}
			if qp.RawFormatItems[0].Format != formats[index].ID {
				t.Fatalf("format namespace: %+v / %+v", qp, formats)
			}
			found := false
			for _, item := range qp.Items {
				if item.Name == "WEB" {
					found = item.Allowed && len(item.Items) == 2 && qp.Cutoff == item.ID
				}
			}
			if !found {
				t.Fatalf("lost quality group: %+v", qp)
			}
		}
		e.call("GET", "/radarr/api/v3/customformat/1", nil, 200, nil)
		e.call("GET", "/radarr/api/v3/customformat/999", nil, 404, nil)
		e.call("POST", "/radarr/api/v3/customformat", map[string]any{"name": "write"}, 404, nil)
	}
	read(old, 10)
	next, err := p.Clone()
	if err != nil {
		t.Fatal(err)
	}
	next.FormatSets["a"].Formats["HDR"] = &policy.FormatRef{Specification: raw}
	for _, pr := range next.Profiles {
		pr.MinScore = 20
	}
	read(build(next), 20)
	read(old, 10)
}
