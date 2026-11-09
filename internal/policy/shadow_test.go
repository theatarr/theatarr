package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShadowSettings(t *testing.T) {
	t.Parallel()
	p, err := Load(exampleDir)
	if err != nil {
		t.Fatal(err)
	}
	def := (&Policy{}).ShadowSettings()
	if got := p.ShadowSettings(); got != def {
		t.Errorf("example %+v, defaults %+v", got, def)
	}
	if def.ImportInterval != 15*time.Minute || def.RSSInterval != 15*time.Minute || !def.RSS || !def.Search ||
		def.SearchesPerHour != 20 || def.KeepResults != 10 || def.LinkWindow != 24*time.Hour || def.SearchReuse != 6*time.Hour {
		t.Errorf("defaults %+v", def)
	}
	for _, tc := range []struct {
		yaml, err string
	}{
		{"shadow:\n  import_interval: 5m\n  rss: false\n  search: false\n  searches_per_hour: 200\n", ""},
		{"shadow:\n  import_interval: 4m\n", "shadow.import_interval"},
		{"shadow:\n  rss_interval: 5m\n", "shadow.rss_interval"},
		{"shadow:\n  searches_per_hour: 201\n", "shadow.searches_per_hour"},
		{"shadow:\n  keep_results: -1\n", "shadow.keep_results"},
		{"shadow:\n  link_window: 30m\n", "shadow.link_window"},
		{"shadow:\n  search_reuse: 48h\n", "shadow.search_reuse"},
		{"shadow:\n  bogus: 1\n", "bogus"},
	} {
		dir := copyExample(t)
		if err := os.WriteFile(filepath.Join(dir, "shadow.yaml"), []byte(tc.yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		p, err := Load(dir)
		switch {
		case tc.err == "" && err != nil:
			t.Errorf("%q: %v", tc.yaml, err)
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("%q: want an error about %s, got %v", tc.yaml, tc.err, err)
		case tc.err == "":
			s := p.ShadowSettings()
			if s.ImportInterval != 5*time.Minute || s.RSS || s.Search || s.SearchesPerHour != 200 || s.KeepResults != 10 {
				t.Errorf("settings %+v", s)
			}
		}
	}
}
