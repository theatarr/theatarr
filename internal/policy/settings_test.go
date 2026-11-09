package policy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSettingsKeepsYAMLOwnedFieldsStrict(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, document     string
		quality, wantError bool
	}{
		{"managed", "profiles: invalid\nformats: invalid\nformat_sets: invalid\nindexers: invalid\nproviders: invalid\nbudgets: invalid\nversions:\n  movie:\n    mode: shadow\n    naming: invalid\nrouter:\n  enabled: false\nlibrary:\n  enabled: false\n", true, false},
		{"pre-upgrade", "profiles: invalid\n", false, true},
		{"unknown version field", "versions:\n  movie:\n    unexpected: true\n", true, true},
		{"unknown root field", "unexpected: true\n", true, true},
		{"unknown router field", "router:\n  unexpected: true\n", true, true},
		{"extra document", "router:\n  enabled: false\n---\nrouter:\n  enabled: true\n", true, true},
		{"duplicate version", "versions:\n  movie: {}\n  movie: {}\n", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte(tc.document), 0600); err != nil {
				t.Fatal(err)
			}
			p, err := ReadSettings(dir, tc.quality)
			if (err != nil) != tc.wantError {
				t.Fatalf("ReadSettings err=%v, wantError=%v", err, tc.wantError)
			}
			if tc.name == "managed" && (p.Versions["movie"].Mode != ModeShadow || p.Router == nil || p.Router.Enabled || p.Library == nil || p.Library.Enabled || len(p.Profiles) != 0) {
				t.Fatal("filter changed YAML-owned settings")
			}
		})
	}
}
