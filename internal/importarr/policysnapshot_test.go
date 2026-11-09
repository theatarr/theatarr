package importarr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/arr"
)

func TestPolicySnapshotPersistsCompleteInputs(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	fake := newFake("/movies")
	bodies := map[string][]byte{}
	for _, ep := range []string{"qualityprofile", "customformat", "qualitydefinition"} {
		b, err := os.ReadFile("../decide/testdata/arr/radarr_4k_debrid/snapshot/" + ep + ".json")
		if err != nil {
			t.Fatal(err)
		}
		bodies[ep] = b
	}
	bodies["config/mediamanagement"] = []byte(`{"downloadPropersAndRepacks":"doNotPrefer","unrelated":"not retained"}`)
	bodies["config/indexer"] = []byte(`{"maximumSize":123,"preferIndexerFlags":true,"unrelated":"not retained"}`)
	failed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ep := strings.TrimPrefix(r.URL.Path, "/api/v3/")
		if failed && ep == "customformat" {
			http.Error(w, "unavailable", 400)
			return
		}
		if b, ok := bodies[ep]; ok {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(b)
			return
		}
		fake.ServeHTTP(w, r)
	}))
	defer srv.Close()
	im := &Importer{Store: st, Now: fixedNow}
	src := Source{Instance: "radarr_a", Flavour: "radarr", Version: "movie-remux", Library: "/movies"}
	runImport(t, im, src, srv)
	var snapshot map[string]json.RawMessage
	read := func() string {
		return one[string](t, st, `SELECT body FROM arr_policy_snapshot WHERE instance='radarr_a'`)
	}
	if err := json.Unmarshal([]byte(read()), &snapshot); err != nil {
		t.Fatal(err)
	}
	for _, ep := range []string{"qualityprofile", "customformat", "qualitydefinition"} {
		var a, b any
		_ = json.Unmarshal(bodies[ep], &a)
		_ = json.Unmarshal(snapshot[ep], &b)
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		if string(ja) != string(jb) {
			t.Fatalf("lost raw %s fields", ep)
		}
	}
	if strings.Contains(read(), "unrelated") || !strings.Contains(string(snapshot["config/indexer"]), `"maximumSize":123`) {
		t.Fatal("snapshot did not retain just the decision settings")
	}
	im.Now = func() time.Time { return fixedNow().Add(time.Hour) }
	bodies["config/indexer"] = []byte(`{"maximumSize":0,"preferIndexerFlags":false}`)
	runImport(t, im, src, srv)
	if at := one[int64](t, st, `SELECT fetched_at FROM arr_policy_snapshot`); at != im.Now().UnixMilli() {
		t.Fatalf("stale timestamp %d", at)
	}
	before := read()
	failed = true
	g, err := arr.New(srv.URL, testKey, arr.Options{MinInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := im.Run(context.Background(), src, g); err == nil {
		t.Fatal("incomplete fetch succeeded")
	}
	if read() != before {
		t.Fatal("failed fetch replaced the complete snapshot")
	}
}
