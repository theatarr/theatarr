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
	if def.RSSInterval != 15*time.Minute || !def.RSS {
		t.Errorf("defaults %+v", def)
	}
	for _, tc := range []struct {
		yaml, err string
	}{
		{"shadow:\n  rss_interval: 20m\n  rss: false\n", ""},
		{"shadow:\n  rss_interval: 5m\n", "shadow.rss_interval"},
		{"shadow:\n  search: false\n", "search"},
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
			if s.RSSInterval != 20*time.Minute || s.RSS {
				t.Errorf("settings %+v", s)
			}
		}
	}
}
