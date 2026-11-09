package policy

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestMinimumSeeders(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"torznab", "torrentio", "dmm", "zilean", "seadex"} {
		for _, tc := range []struct {
			field string
			want  int
		}{{"", 1}, {"min_seeders: 0", 0}, {"min_seeders: 5", 5}} {
			var ix Indexer
			if err := yaml.Unmarshal([]byte("kind: "+kind+"\n"+tc.field), &ix); err != nil {
				t.Fatal(err)
			}
			if got := ix.MinimumSeeders(); got != tc.want {
				t.Fatalf("%s %s: %d, want %d", kind, tc.field, got, tc.want)
			}
		}
	}
	p, err := Load(exampleDir)
	if err != nil {
		t.Fatal(err)
	}
	p.Indexers["torrentio"].MinSeeders = new(-1)
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "indexers[torrentio].min_seeders") {
		t.Fatalf("negative minimum: %v", err)
	}
}
