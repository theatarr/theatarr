package library

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

type namingGetter struct {
	calls []string
	fail  bool
}

func (g *namingGetter) GetJSON(_ context.Context, endpoint string, _ url.Values, v any) error {
	g.calls = append(g.calls, endpoint)
	if g.fail {
		return fmt.Errorf("unavailable")
	}
	body := `{"renameEpisodes":true,"seriesFolderFormat":"{Series Title}"}`
	if endpoint == "qualitydefinition" {
		body = `[{"quality":{"name":"WEBDL-2160p"},"title":"WEB"}]`
	}
	return json.Unmarshal([]byte(body), v)
}

func TestSyncNamingAllImporters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	pol := &policy.Policy{
		Versions: map[string]*policy.VersionPolicy{"native": {Naming: policy.Naming{Folder: "Explicit"}}, "defaults": {}},
		Import:   &policy.Import{Arr: map[string]*policy.ArrSource{"radarr": {}, "sonarr": {}, "missing": {}}},
	}
	a, b := &namingGetter{}, &namingGetter{}
	getters := map[string]ArrGetter{"radarr": a, "sonarr": b}
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	done, err := SyncNaming(ctx, st, pol, getters, now)
	if err != nil || !reflect.DeepEqual(done, []string{"radarr", "sonarr"}) {
		t.Fatalf("done %v: %v", done, err)
	}
	for _, g := range []*namingGetter{a, b} {
		if !reflect.DeepEqual(g.calls, []string{"config/naming", "qualitydefinition"}) {
			t.Fatalf("unexpected reads: %v", g.calls)
		}
	}
	b.fail = true
	done, err = SyncNaming(ctx, st, pol, getters, now.Add(time.Hour))
	if err == nil || !reflect.DeepEqual(done, []string{"radarr"}) {
		t.Fatalf("done %v: %v", done, err)
	}
	var at int64
	var titles string
	if err := st.Reader().QueryRow(`SELECT fetched_at, qualities FROM library_naming WHERE instance='sonarr'`).Scan(&at, &titles); err != nil {
		t.Fatal(err)
	}
	if at != now.UnixMilli() || titles != `{"WEBDL-2160p":"WEB"}` {
		t.Fatal("failed refresh changed stored naming")
	}
}
