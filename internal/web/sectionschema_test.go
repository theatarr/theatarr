package web

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

// Walk the policy independently from schema construction. Populate every parent,
// including one named record, to check all credential paths against the save API.
func TestSectionSchemaCoverage(t *testing.T) {
	schemas := sectionSchemas()
	pt := reflect.TypeOf(policy.Policy{})
	for _, spec := range settings.Sections {
		t.Run(spec.Key, func(t *testing.T) {
			var typ reflect.Type
			for i := 0; i < pt.NumField(); i++ {
				if strings.Split(pt.Field(i).Tag.Get("yaml"), ",")[0] == spec.Key {
					typ = pt.Field(i).Type
				}
			}
			var check func(reflect.Type, []SchemaField, string) any
			var secrets []string
			check = func(typ reflect.Type, fields []SchemaField, path string) any {
				for typ.Kind() == reflect.Pointer {
					typ = typ.Elem()
				}
				if typ.Kind() == reflect.Map {
					return map[string]any{"example": check(typ.Elem(), fields[0].Fields, path+".example")}
				}
				config := map[string]any{}
				for i := 0; i < typ.NumField(); i++ {
					f := typ.Field(i)
					name := strings.Split(f.Tag.Get("yaml"), ",")[0]
					if name == "" || name == "-" {
						continue
					}
					full := path + "." + name
					owned := full == "library.enabled" || full == "notify.enabled" || full == "router.enabled" || full == "library.versions.example.roots"
					n := slices.IndexFunc(fields, func(sf SchemaField) bool { return sf.Name == name })
					if owned {
						if n >= 0 {
							t.Errorf("owned %s appears", full)
						}
						continue
					}
					if n < 0 {
						t.Errorf("missing %s", full)
						continue
					}
					sf := fields[n]
					if sf.Help == "" {
						t.Errorf("no help %s", full)
					}
					ft := f.Type
					for ft.Kind() == reflect.Pointer {
						ft = ft.Elem()
					}
					switch sf.Type {
					case "object":
						config[name] = check(ft, sf.Fields, full)
					case "keyed":
						config[name] = map[string]any{"example": check(ft.Elem(), sf.Fields, full+".example")}
					case "duration":
						config[name] = "0s"
					case "string", "enum":
						config[name] = ""
					case "boolean":
						config[name] = false
					case "integer", "bytes", "number":
						config[name] = 0
					case "list":
						config[name] = []string{}
					default:
						t.Errorf("uncovered type %s at %s", sf.Type, full)
					}
					if strings.HasSuffix(name, "_env") {
						sn := strings.TrimSuffix(name, "_env")
						idx := slices.IndexFunc(fields, func(sf SchemaField) bool { return sf.Name == sn && sf.Type == "secret" })
						if idx < 0 {
							t.Errorf("no secret %s", full)
						} else {
							sp := strings.ReplaceAll(fields[idx].Path, "*", "example")
							if sp != strings.TrimSuffix(full, "_env") {
								t.Errorf("secret path %s", sp)
							}
							secrets = append(secrets, strings.TrimPrefix(sp, spec.Key+"."))
						}
					}
				}
				return config
			}
			config := check(typ, schemas[spec.Key], spec.Key)
			b, _ := json.Marshal(config)
			paths, err := settings.SectionSecretPaths(spec.Key, b)
			if err != nil {
				t.Fatal(err)
			}
			slices.Sort(secrets)
			if !slices.Equal(paths, secrets) {
				t.Fatalf("paths %v, schema %v", paths, secrets)
			}
		})
	}
}
func TestTorrentSchemaLimits(t *testing.T) {
	fields := sectionSchemas()["torrents"][1].Fields
	if *fields[0].Min != policy.MinTorrentBackupEvery.Seconds() || !fields[0].ZeroAllowed {
		t.Fatal(fields[0])
	}
	if *fields[1].Min != 1 || *fields[1].Max != policy.MaxTorrentBackupKeep || fields[1].Default != "30" {
		t.Fatal(fields[1])
	}
}
