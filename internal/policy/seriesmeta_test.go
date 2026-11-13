package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSeriesMetaSettings(t *testing.T) {
	t.Parallel()
	p, err := Load(exampleDir)
	if err != nil {
		t.Fatal(err)
	}
	if s := p.SeriesMetaSettings(); !s.Enabled || s.Interval != time.Hour || s.PerRun != 50 {
		t.Errorf("example %+v", s)
	}
	if s := (&Policy{}).SeriesMetaSettings(); !s.Enabled || s.Interval != DefaultSeriesMetaInterval ||
		s.PerRun != DefaultSeriesMetaPerRun {
		t.Errorf("defaults %+v", s)
	}
	for _, tc := range []struct{ name, old, new, want string }{
		{"per_run", "per_run: 50", "per_run: 9000", "metadata.series.per_run"},
		{"interval", "interval: 1h", "interval: 1m", "metadata.series.interval"},
		{"off", "enabled: true", "enabled: false", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := copyExample(t)
			path := filepath.Join(dir, "metadata.yaml")
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), tc.old) {
				t.Fatalf("metadata.yaml lacks %q", tc.old)
			}
			if err := os.WriteFile(path, []byte(strings.Replace(string(b), tc.old, tc.new, 1)), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err = Load(dir)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("want valid, got %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("want %q, got %v", tc.want, err)
			}
		})
	}
}
