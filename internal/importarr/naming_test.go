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

func TestImportStoresNaming(t *testing.T) {
	t.Parallel()
	for _, flavour := range []string{"radarr", "sonarr"} {
		t.Run(flavour, func(t *testing.T) {
			st := openStore(t)
			var fake http.Handler = newFake("/movies")
			src := Source{Instance: flavour + "_all", Flavour: flavour, Version: "version", Library: "/movies"}
			if flavour == "sonarr" {
				fake = &fakeSonarr{root: "/series"}
				src.Library = "/series"
			}
			b, err := os.ReadFile("../library/naming/testdata/golden/" + flavour + "_4k_debrid.json")
			if err != nil {
				t.Fatal(err)
			}
			var fixture struct{ Naming json.RawMessage }
			if err := json.Unmarshal(b, &fixture); err != nil {
				t.Fatal(err)
			}
			body := fixture.Naming
			failed := false
			reads := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("naming refresh must be GET only")
					http.Error(w, "method", http.StatusMethodNotAllowed)
					return
				}
				switch strings.TrimPrefix(r.URL.Path, "/api/v3/") {
				case "config/naming":
					reads++
					if failed {
						http.Error(w, "unavailable", http.StatusBadRequest)
						return
					}
					_, _ = w.Write(body)
				case "qualitydefinition":
					_, _ = w.Write([]byte(`[{"quality":{"name":"WEBDL-2160p"},"title":"Custom WEB"}]`))
				default:
					fake.ServeHTTP(w, r)
				}
			}))
			defer srv.Close()
			im := &Importer{Store: st, Now: fixedNow}
			runImport(t, im, src, srv)
			if got := one[string](t, st, `SELECT naming FROM library_naming`); got != string(body) {
				t.Fatal("lost naming fields")
			}
			if got := one[string](t, st, `SELECT qualities FROM library_naming`); got != `{"WEBDL-2160p":"Custom WEB"}` {
				t.Fatalf("lost quality titles: %s", got)
			}
			im.Now = func() time.Time { return fixedNow().Add(time.Hour) }
			body = json.RawMessage(`{"renameEpisodes":true,"seriesFolderFormat":"Changed"}`)
			runImport(t, im, src, srv)
			before := one[string](t, st, `SELECT body FROM arr_policy_snapshot`)
			if got := one[string](t, st, `SELECT naming FROM library_naming`); got != string(body) {
				t.Fatal("naming was not refreshed")
			}
			if got := one[int64](t, st, `SELECT fetched_at FROM library_naming`); got != im.Now().UnixMilli() {
				t.Fatal("naming timestamp was not refreshed")
			}
			failed = true
			g, err := arr.New(srv.URL, testKey, arr.Options{MinInterval: time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := im.Run(context.Background(), src, g); err == nil {
				t.Fatal("failed naming fetch accepted")
			}
			if reads != 3 || one[string](t, st, `SELECT naming FROM library_naming`) != string(body) || one[string](t, st, `SELECT body FROM arr_policy_snapshot`) != before {
				t.Fatal("failed naming fetch changed snapshots")
			}
		})
	}
}
