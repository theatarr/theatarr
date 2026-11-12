package web

import (
	"context"
	"testing"
)

// TestCreateIndexerJoinsLibraries: a new indexer joins the named libraries in its own generation.
func TestCreateIndexerJoinsLibraries(t *testing.T) {
	t.Parallel()
	se := newSettingsEnv(t, nil, nil)
	body := func(key string, versions ...string) map[string]any {
		return map[string]any{"generation": se.generation(), "key": key, "enabled": true,
			"config": map[string]any{"kind": "torrentio", "url": "https://torrentio.example"}, "versions": versions}
	}
	before := se.generation()
	se.want(se.call("POST", "/indexers", body("joins", "movie-remux", "movie-remux")), 200, "create with versions")
	snap, err := se.coord.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Generation != before+1 {
		t.Fatalf("generation %d after %d", snap.Generation, before)
	}
	for _, v := range snap.Records.Versions {
		n := 0
		for _, k := range v.Config.Indexers {
			if k == "joins" {
				n++
			}
		}
		if want := map[bool]int{true: 1, false: 0}[v.Key == "movie-remux"]; n != want {
			t.Fatalf("library %s lists it %d times: %v", v.Key, n, v.Config.Indexers)
		}
	}
	gen := se.generation()
	se.want(se.call("POST", "/indexers", body("nowhere", "no-such-library")), 400, "unknown library")
	if se.generation() != gen {
		t.Fatal("refused create saved something")
	}
	se.want(se.call("PUT", "/indexers/"+itoa(se.id(entityIndexers, "joins")),
		map[string]any{"generation": gen, "enabled": false, "versions": []string{"movie-remux"}}), 400, "versions on update")
	se.want(se.call("POST", "/budgets", map[string]any{"generation": gen, "key": "b", "versions": []string{"movie-remux"},
		"config": map[string]any{"requests": 1, "per": "1m"}}), 400, "versions on a budget")
	if se.generation() != gen {
		t.Fatal("refused update saved")
	}
}
