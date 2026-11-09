package indexer

import (
	"encoding/json"
	"testing"
)

func TestStoredFeaturesKeepCandidateCoverage(t *testing.T) {
	var snapshot SeaDexSnapshot
	if err := json.Unmarshal([]byte(`{"anilist":1,"torrents":[{"info_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":100000000,"facts":{"files":[{"name":"Show.S01E01.mkv","length":100000000},{"name":"credits.mkv","length":1000}]}}]}`), &snapshot); err != nil {
		t.Fatal(err)
	}
	stored := storedFacts(snapshot)
	names := stored.Torrents[0].Facts.Files
	if len(names) != 1 || names[0].Name != "Show.S01E01.mkv" {
		t.Fatalf("stored feature evidence: %v", names)
	}
	stored.Torrents[0].Facts.Files[0].Name = "changed"
	if snapshot.Torrents[0].Facts.Files[0].Name == "changed" {
		t.Fatal("stored facts alias source files")
	}
}
