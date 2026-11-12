package settings

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

func sectionConfig(snap *Snapshot, key string) string {
	for _, s := range snap.Records.Sections {
		if s.Key == key {
			return string(s.Config)
		}
	}
	return ""
}

// TestFirstWriteTurnsCatalogOn: serve's first write over a fresh database (empty policy, no revision) stores the
// catalog on, at the default repository.
func TestFirstWriteTurnsCatalogOn(t *testing.T) {
	st, _, _ := setup(t)
	ctx := context.Background()
	base := &policy.Policy{}
	initial, err := st.Load(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := st.Save(ctx, SaveRequest{Base: base, Actor: "setup", Records: initial.Records})
	if err != nil {
		t.Fatal(err)
	}
	if got := sectionConfig(saved, "cardigann"); got != `{"definitions":{"repo":"Prowlarr/Indexers"}}` {
		t.Fatalf("cardigann section %s", got)
	}
	if c := saved.Policy.Cardigann; c == nil || c.Definitions == nil || c.Definitions.Repo != policy.DefaultCardigannRepo {
		t.Fatalf("catalog not on: %+v", c)
	}
}

// TestCatalogDefaultOnlyOnFirstWrite: a first write that sets the section keeps its value, and a later write over a
// head whose catalog is off keeps it off.
func TestCatalogDefaultOnlyOnFirstWrite(t *testing.T) {
	st, _, _ := setup(t)
	ctx := context.Background()
	base := &policy.Policy{}
	initial, err := st.Load(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	records := initial.Records
	records.Sections = []Section{{Record: Record{Key: "cardigann"}, Config: json.RawMessage("null")}}
	saved, err := st.Save(ctx, SaveRequest{Base: base, Actor: "setup", Records: records})
	if err != nil {
		t.Fatal(err)
	}
	if got := sectionConfig(saved, "cardigann"); got != "null" || saved.Policy.Cardigann != nil {
		t.Fatalf("first write's own null replaced: %s", got)
	}
	again, err := st.Save(ctx, SaveRequest{Base: base, Actor: "setup", ExpectedGeneration: saved.Generation, Records: saved.Records})
	if err != nil {
		t.Fatal(err)
	}
	if got := sectionConfig(again, "cardigann"); got != "null" || again.Policy.Cardigann != nil {
		t.Fatalf("later write turned the catalog on: %s", got)
	}
}
