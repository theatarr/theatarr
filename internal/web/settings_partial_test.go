package web

import (
	"encoding/json"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

// A partial record PUT must not write through the stored record's pointers or maps into the live
// snapshot, and a field sent as null takes its default. Fields left out keep their stored values.
func TestPartialConfigPutLeavesSnapshotAlone(t *testing.T) {
	t.Parallel()
	five, hundred := 5, 100
	snap := &settings.Snapshot{Generation: 1, Records: settings.Records{Indexers: []settings.Indexer{{
		Record: settings.Record{ID: 7, Key: "ix"}, Enabled: true,
		Config: policy.Indexer{Kind: "cardigann", URL: "https://ix.invalid", MinSeeders: &five,
			Settings: map[string]string{"sort": "seeders"}, SeaDex: &policy.SeaDexScoring{Best: &hundred}},
	}}}}
	body := settingsBody{Config: json.RawMessage(`{"min_seeders": 3, "settings": {"freeleech": "true"}, "seadex": {"best": 50}}`)}
	req, _, err := applyBody(snap, entityIndexers, 7, body, false)
	if err != nil {
		t.Fatal(err)
	}
	stored := snap.Records.Indexers[0].Config
	if *stored.MinSeeders != 5 || len(stored.Settings) != 1 || *stored.SeaDex.Best != 100 {
		t.Fatalf("the PUT changed the live snapshot: min_seeders %d settings %v seadex %+v", *stored.MinSeeders, stored.Settings, *stored.SeaDex.Best)
	}
	got := req.Records.Indexers[0].Config
	if *got.MinSeeders != 3 || *got.SeaDex.Best != 50 || got.URL != "https://ix.invalid" || got.Kind != "cardigann" {
		t.Fatalf("decoded record: %+v", got)
	}
	if len(got.Settings) != 1 || got.Settings["freeleech"] != "true" {
		t.Errorf("a sent map replaces the stored one: %v", got.Settings)
	}

	body = settingsBody{Config: json.RawMessage(`{"min_seeders": null}`)}
	if req, _, err = applyBody(snap, entityIndexers, 7, body, false); err != nil {
		t.Fatal(err)
	}
	if got := req.Records.Indexers[0].Config; got.MinSeeders != nil || got.MinimumSeeders() != 1 || got.Settings["sort"] != "seeders" {
		t.Errorf("min_seeders null: %v settings %v", got.MinSeeders, got.Settings)
	}
	if *snap.Records.Indexers[0].Config.MinSeeders != 5 {
		t.Error("the null PUT changed the live snapshot")
	}
}

// min_seeders is nullable in the schema, so the UI clears it to its default 1, not to 0 (no check).
func TestMinSeedersSchemaIsNullable(t *testing.T) {
	t.Parallel()
	for _, f := range settingsSchema().Indexers.Config {
		if f.Name == "min_seeders" {
			if !f.Nullable {
				t.Error("min_seeders is not nullable")
			}
			return
		}
	}
	t.Fatal("no min_seeders field")
}
