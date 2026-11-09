package profile

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/format"
)

// The profile editor's Size limits show the arr defaults from a copy of this table; it must match the engine's.
func TestWebSizeDefaultsMatchEngine(t *testing.T) {
	raw, err := os.ReadFile("../../../web/src/pages/admin/settings/profiles/size-defaults.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows map[string]map[string][]*float64
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	// Each row is exactly [min, max, preferred]; a short row would decode as nil (unlimited) and still compare equal.
	web := map[string]map[string][3]*float64{}
	for fl, qs := range rows {
		web[fl] = map[string][3]*float64{}
		for q, row := range qs {
			if len(row) != 3 {
				t.Fatalf("%s %s: %d values, want [min, max, preferred]", fl, q, len(row))
			}
			web[fl][q] = [3]*float64{row[0], row[1], row[2]}
		}
	}
	for _, fl := range []format.Flavour{format.Radarr, format.Sonarr} {
		want := map[string][3]*float64{}
		for _, d := range defaultDefs(fl) {
			want[d.Quality.Name] = [3]*float64{d.MinSize, d.MaxSize, d.PreferredSize}
		}
		if !reflect.DeepEqual(web[string(fl)], want) {
			t.Errorf("%s: web size defaults differ from DefaultDefinitions; regenerate size-defaults.json as [min, max, preferred]", fl)
		}
	}
}
