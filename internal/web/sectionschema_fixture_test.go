package web

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

var updateSectionFixture = flag.Bool("update-section-fixture", false, "update the real settings schema web fixture")

func TestSectionSchemaFixture(t *testing.T) {
	rw := httptest.NewRecorder()
	w := &Web{pol: &policy.Policy{}}
	w.apiSettingsSchema(rw, httptest.NewRequest("GET", APIBase+"/settings/schema", nil))
	var schema SettingsSchema
	if err := json.Unmarshal(rw.Body.Bytes(), &schema); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := "../../web/src/components/settings/testdata/schema.json"
	if *updateSectionFixture {
		if err := os.MkdirAll("../../web/src/components/settings/testdata", 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, stored) {
		t.Fatal("stale section fixture: go test ./internal/web -run TestSectionSchemaFixture -update-section-fixture")
	}
}
func TestSectionDefaultTrue(t *testing.T) {
	p := &policy.Policy{Library: &policy.Library{Plex: &policy.Plex{}}}
	defaults := map[string]bool{
		"metadata.series.enabled":                    p.SeriesMetaSettings().Enabled,
		"metadata.anime.auto_build":                  p.AnimeBuildSettings().AutoBuild,
		"library.sample.resolution":                  p.LibrarySettings().Resolution,
		"library.sample.mislabel":                    p.LibrarySettings().Mislabel,
		"library.plex.now_playing":                   p.LibrarySettings().NowPlaying,
		"library.plex.prewarm":                       p.LibrarySettings().Prewarm,
		"shadow.rss":                                 p.ShadowSettings().RSS,
		"playlists.routes":                           p.PlaylistSettings().Routes,
		"router.cached_only":                         p.RouterSettings().CachedOnly,
		"router.wanted.missing":                      p.WantedSettings().Missing,
		"router.wanted.retry_new":                    p.WantedSettings().RetryNew,
		"router.cache_evidence.torbox_check.enabled": p.CacheEvidenceSettings().CheckEnabled,
	}
	var walk func(string, []SchemaField)
	walk = func(path string, fields []SchemaField) {
		for _, f := range fields {
			full := path + "." + f.Name
			if want, ok := defaults[full]; ok {
				if !want || f.Default != "true" {
					t.Errorf("%s: runtime %v schema %q", full, want, f.Default)
				}
				delete(defaults, full)
			} else if f.Type == "boolean" && f.Default == "true" {
				t.Errorf("uncovered default true: %s", full)
			}
			walk(full, f.Fields)
		}
	}
	for key, fields := range sectionSchemas() {
		walk(key, fields)
	}
	if len(defaults) > 0 {
		t.Fatal(defaults)
	}
}
func TestSectionHelpHasNoGoFieldNames(t *testing.T) {
	names := map[string]bool{}
	visited := map[reflect.Type]bool{}
	var visit func(reflect.Type)
	visit = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || visited[typ] {
			return
		}
		visited[typ] = true
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.IsExported() {
				names[f.Name] = true
				visit(f.Type)
			}
		}
	}
	typ := reflect.TypeOf(policy.Policy{})
	for _, spec := range settings.Sections {
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if strings.Split(f.Tag.Get("yaml"), ",")[0] == spec.Key {
				visit(f.Type)
			}
		}
	}
	patterns := map[string]*regexp.Regexp{}
	for name := range names {
		patterns[name] = regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
	}
	var check func(string, []SchemaField)
	check = func(path string, fields []SchemaField) {
		for _, f := range fields {
			for name, pattern := range patterns {
				if pattern.MatchString(f.Help) {
					t.Errorf("%s.%s help contains %s: %s", path, f.Name, name, f.Help)
				}
			}
			check(path+"."+f.Name, f.Fields)
		}
	}
	for key, fields := range sectionSchemas() {
		check(key, fields)
	}
}

func TestWantedSchemaDefaultFollowsActiveVersions(t *testing.T) {
	for _, active := range []bool{false, true} {
		p := &policy.Policy{}
		if active {
			p.Versions = map[string]*policy.VersionPolicy{"main": {Mode: policy.ModeActive}}
		}
		schema := settingsSchemaForPolicy(p)
		for _, f := range schema.Sections["router"] {
			if f.Name == "wanted" {
				for _, child := range f.Fields {
					if child.Name == "enabled" && (child.Default == "true") != p.WantedSettings().Enabled {
						t.Errorf("active %v: %s", active, child.Default)
					}
				}
			}
		}
	}
}

func TestSectionPlainLabels(t *testing.T) {
	want := map[string]string{
		"egress.vpn_ips": "VPN exit IPs", "egress.vpn_cidrs": "VPN address ranges",
		"ui.images.dir": "Folder", "ui.images.max_bytes": "Maximum size",
		"router.alldebrid_delete_uncached":       "Delete uncached AllDebrid adds",
		"router.cache_evidence.torbox_check":     "Check TorBox cache",
		"router.cache_evidence.torbox_check.top": "Maximum releases checked per search",
		"cardigann.flaresolverr_url":             "FlareSolverr address",
		"library.mount.rclone_rc":                "rclone remote control", "notify.targets.*.native": "Send Theatarr events",
		"router.stalled_after": "Stalled download timeout", "router.downloading_timeout": "No progress timeout",
	}
	var walk func(string, []SchemaField)
	walk = func(path string, fields []SchemaField) {
		for _, f := range fields {
			full := path
			if f.Name != "" {
				full += "." + f.Name
			}
			if label, ok := want[full]; ok {
				if f.Label != label {
					t.Errorf("%s: %q, want %q", full, f.Label, label)
				}
				delete(want, full)
			}
			if f.Type == "keyed" {
				full += ".*"
			}
			walk(full, f.Fields)
		}
	}
	for key, fields := range sectionSchemas() {
		walk(key, fields)
	}
	if len(want) != 0 {
		t.Fatal(want)
	}
}
