package web

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// The settings form offers every indexer kind the policy accepts, and every config field it describes is a policy
// field (the write path decodes config strictly into policy.Indexer).
func TestSettingsSchemaIndexerFieldsArePolicyFields(t *testing.T) {
	t.Parallel()
	s := settingsSchema()
	if !slices.Contains(s.Indexers.Kinds, "cardigann") {
		t.Fatalf("indexer kinds %v lack cardigann", s.Indexers.Kinds)
	}
	tags := map[string]bool{}
	rt := reflect.TypeOf(policy.Indexer{})
	for i := 0; i < rt.NumField(); i++ {
		tags[strings.Split(rt.Field(i).Tag.Get("yaml"), ",")[0]] = true
	}
	byName := map[string]SchemaField{}
	for _, f := range s.Indexers.Config {
		if !tags[f.Name] {
			t.Errorf("schema field %q is not a policy indexer field", f.Name)
		}
		byName[f.Name] = f
	}
	for _, name := range []string{"definition", "settings", "hosts"} {
		f, ok := byName[name]
		if !ok || !reflect.DeepEqual(f.Kinds, []string{"cardigann"}) {
			t.Errorf("%s: %+v, want a cardigann-only field", name, f)
		}
	}
	if byName["settings"].Type != "string_map" {
		t.Errorf("settings type %q, want string_map (the policy holds strings)", byName["settings"].Type)
	}
}

func TestSettingsSchemaOptionalVPN(t *testing.T) {
	t.Parallel()
	rules := strings.Join(settingsSchema().Rules, " ")
	for _, want := range []string{"No egress section", "require_vpn_for: [] monitors only", "defaults to [indexers]"} {
		if !strings.Contains(rules, want) {
			t.Errorf("rules lack %q: %s", want, rules)
		}
	}
}
