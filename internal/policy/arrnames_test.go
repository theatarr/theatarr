package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Arr names that normalized name matching cannot resolve resolve through indexers.K.arr_names
// (case-insensitive), which loads from YAML instead of being dropped.
func TestIndexerByNameArrNames(t *testing.T) {
	t.Parallel()
	live := map[string][]string{
		"prowlarr-tpb":      {"The Pirate Bay AD (Prowlarr)"},
		"prowlarr-therarbg": {"TheRARBG RD (Prowlarr)"},
		"prowlarr-nyaa":     {"Nyaa.si (Prowlarr)"},
		"dmm":               {"DMM RD (Prowlarr)"},
		"torrentio":         {"Torrentio RD v2 (Prowlarr)"},
		"zilean":            {"Zilean RD (Prowlarr)"},
		"seadex":            {"SeaDex Anime (Prowlarr)"},
	}
	p := &Policy{Indexers: map[string]*Indexer{}}
	for k, names := range live {
		p.Indexers[k] = &Indexer{ArrNames: names}
	}
	for k, names := range live {
		if got := p.IndexerByName(strings.ToLower(names[0])); got != k {
			t.Errorf("%q -> %q, want %s", names[0], got, k)
		}
	}

	dir := copyExample(t)
	b, err := os.ReadFile(filepath.Join(dir, "indexers.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(string(b), "    privacy: private\n", "    privacy: private\n    arr_names: [\"X Tracker (Prowlarr)\"]\n", 1)
	if err := os.WriteFile(filepath.Join(dir, "indexers.yaml"), []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Ignored) != 0 || loaded.IndexerByName("x tracker (prowlarr)") != "tracker-example" {
		t.Fatalf("arr_names not loaded: ignored %q, match %q", loaded.Ignored, loaded.IndexerByName("x tracker (prowlarr)"))
	}
	loaded.Indexers["dmm"].ArrNames = []string{"x tracker (PROWLARR)"}
	if err := loaded.Validate(); err == nil || !strings.Contains(err.Error(), "already names indexer") {
		t.Fatalf("duplicate alias accepted: %v", err)
	}
	// A settings database indexer record (policy.Indexer as JSON) keeps them too.
	var ix Indexer
	if err := json.Unmarshal([]byte(`{"kind":"dmm","ArrNames":["DMM RD (Prowlarr)"]}`), &ix); err != nil || len(ix.ArrNames) != 1 {
		t.Fatalf("record arr names: %v %v", ix.ArrNames, err)
	}
}
