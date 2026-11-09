package policy

import (
	"strings"
	"testing"
)

// Settings stored before comparison imports were removed carry an empty role and sink_path on every import.arr
// instance: they still load, role: compare is refused, and the instance stays strictly decoded.
func TestImportLegacyRoleFields(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, entry, want string }{
		{"stored", `{"flavour":"radarr","url":"http://127.0.0.1:7878","api_key_env":"TEST_ARR_KEY","version":"movie","role":"","sink_path":""}`, ""},
		{"import", "{flavour: radarr, url: 'http://127.0.0.1:7878', api_key_env: TEST_ARR_KEY, version: movie, role: import}", ""},
		{"compare", "{flavour: radarr, url: 'http://127.0.0.1:7878', api_key_env: TEST_ARR_KEY, version: movie, role: compare, sink_path: /sink}", "comparison imports were removed"},
		{"sink", "{flavour: radarr, url: 'http://127.0.0.1:7878', api_key_env: TEST_ARR_KEY, version: movie, sink_path: /sink}", "comparison imports were removed"},
		{"unknown", "{flavour: radarr, url: 'http://127.0.0.1:7878', api_key_env: TEST_ARR_KEY, version: movie, role: '', colour: red}", "field colour not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p Policy
			err := decodeBytes([]byte("import:\n  arr:\n    radarr_x: "+tc.entry+"\n"), &p)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if a := p.Import.Arr["radarr_x"]; a == nil || a.Flavour != "radarr" || a.Version != "movie" {
					t.Fatalf("decoded %+v", p.Import.Arr)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}
