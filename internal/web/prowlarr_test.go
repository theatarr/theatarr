package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

// fakeProwlarr answers GET /api/v1/indexer with two linkable torrent indexers, one already linked (feed 7) and a
// Usenet one, and records each request with the key it carried.
func fakeProwlarr(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("X-Api-Key"))
		mu.Unlock()
		w.Write([]byte(`[{"id":1,"name":"Public One","protocol":"torrent","privacy":"public","enable":true},
			{"id":2,"name":"Private Two","protocol":"torrent","privacy":"private","enable":true},
			{"id":7,"name":"Linked","protocol":"torrent","privacy":"public","enable":true},
			{"id":9,"name":"News","protocol":"usenet","privacy":"private","enable":true}]`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(seen) }
}

// prowlarrEnv is a settings env with the catalog off and Prowlarr at srv; key, when set, is stored as its API key.
func prowlarrEnv(t *testing.T, srv *httptest.Server, key string) *settingsEnv {
	t.Helper()
	se := seededSectionsEnvBase(t, func(p *policy.Policy) {
		p.Cardigann = &policy.Cardigann{Prowlarr: &policy.CardigannProwlarr{URL: srv.URL, APIKeyEnv: "THEATARR_TEST_UNSET_PROWLARR_KEY"}}
	})
	// Prowlarr's feed 7 is here already, spelled differently.
	se.want(se.call("POST", "/indexers", map[string]any{"generation": se.generation(), "key": "already", "enabled": false,
		"config": map[string]any{"kind": "torznab", "url": strings.ToUpper(srv.URL[:4]) + srv.URL[4:] + "/7/api/", "budget": "prowlarr"}}),
		200, "existing feed")
	if key != "" {
		se.want(se.call("PUT", "/sections/cardigann", map[string]any{"generation": se.generation(),
			"secrets": map[string]string{"prowlarr.api_key": key}}), 200, "store the Prowlarr key")
	}
	return se
}

func TestProwlarrFeedsWithoutCatalog(t *testing.T) {
	t.Parallel()
	srv, seen := fakeProwlarr(t)
	se := prowlarrEnv(t, srv, testSecret)
	got := se.want(se.call("GET", "/prowlarr/feeds", nil), 200, "feeds")
	if got["configured"] != true || got["url"] != srv.URL {
		t.Fatalf("feeds: %v", got)
	}
	var status []string
	for _, f := range got["feeds"].([]any) {
		m := f.(map[string]any)
		key, _ := m["key"].(string)
		linked, _ := m["linked"].(string)
		status = append(status, m["status"].(string)+" "+key+linked)
	}
	if !slices.Equal(status, []string{"linkable prowlarr-public-one", "linkable prowlarr-private-two", "linked already",
		"usenet "}) {
		t.Fatalf("statuses %v", status)
	}
	if s := seen(); len(s) != 1 || s[0] != "GET /api/v1/indexer "+testSecret {
		t.Fatalf("Prowlarr saw %v", s)
	}
	se.noSecretAnywhere()

	off := prowlarrEnv(t, srv, "")
	if got := off.want(off.call("GET", "/prowlarr/feeds", nil), 200, "no key"); got["configured"] != false || got["feeds"] != nil {
		t.Fatalf("not configured: %v", got)
	}
}

func TestLinkProwlarrFeeds(t *testing.T) {
	t.Parallel()
	srv, _ := fakeProwlarr(t)
	se := prowlarrEnv(t, srv, testSecret)
	gen := se.generation()
	for _, ids := range [][]int{{1, 7}, {1, 3}} {
		se.want(se.call("POST", "/prowlarr/feeds", map[string]any{"generation": gen, "ids": ids}), 400, "not linkable")
	}
	if se.generation() != gen {
		t.Fatal("refused link saved")
	}
	se.want(se.call("POST", "/prowlarr/feeds", map[string]any{"generation": gen, "ids": []int{1, 2},
		"versions": []string{"movie-remux"}}), 200, "link")
	snap, err := se.coord.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Generation != gen+1 {
		t.Fatalf("generation %d after %d", snap.Generation, gen)
	}
	for _, key := range []string{"prowlarr-public-one", "prowlarr-private-two"} {
		i := slices.IndexFunc(snap.Records.Indexers, func(r settings.Indexer) bool { return r.Key == key })
		if i < 0 {
			t.Fatalf("no indexer %s", key)
		}
		r := snap.Records.Indexers[i]
		if !r.Enabled || r.Config.Kind != "torznab" || r.Config.Budget != "prowlarr" || !strings.HasPrefix(r.Config.URL, srv.URL+"/") {
			t.Fatalf("indexer %s: %+v", key, r)
		}
		if se.storedSecret(entityIndexers, key) != testSecret {
			t.Fatalf("indexer %s has no key", key)
		}
	}
	budgets := 0
	for _, b := range snap.Records.Budgets {
		if b.Key == "prowlarr" {
			budgets++
		}
	}
	if budgets != 1 {
		t.Fatalf("%d prowlarr budgets", budgets)
	}
	for _, v := range snap.Records.Versions {
		has := slices.Contains(v.Config.Indexers, "prowlarr-public-one") && slices.Contains(v.Config.Indexers, "prowlarr-private-two")
		if has != (v.Key == "movie-remux") {
			t.Fatalf("library %s: %v", v.Key, v.Config.Indexers)
		}
	}
	se.noSecretAnywhere()
}
