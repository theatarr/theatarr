package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatSetsValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, body, want string }{
		{"unknown set", "profiles:\n  scoped:\n    format_set: missing\n    qualities: [WEBDL-2160p]\n    cutoff: WEBDL-2160p\n", "unknown format set"},
		{"mixed flavours", "format_sets:\n  scoped:\n    flavour: sonarr\n    formats:\n      WEB: {ref: 'WEB Tier 01'}\n", "cannot mix flavours"},
		{"unknown definition", "format_sets:\n  scoped:\n    flavour: sonarr\n    formats:\n      WEB: {ref: missing}\n", "unknown global format"},
		{"duplicate display name", "format_sets:\n  scoped:\n    flavour: sonarr\n    formats:\n      WEB: {ref: 'WEB Tier 01'}\n      WEB: {ref: 'WEB Tier 02'}\n", "already defined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := copyExample(t)
			if err := os.WriteFile(filepath.Join(dir, "sets.yaml"), []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
}
