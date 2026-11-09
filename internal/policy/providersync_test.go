package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProviderSyncInterval(t *testing.T) {
	t.Parallel()
	p, err := Load(exampleDir)
	if err != nil {
		t.Fatal(err)
	}
	if d := p.ProviderSyncInterval(); d != 6*time.Hour {
		t.Errorf("example interval %v", d)
	}
	if d := (&Policy{}).ProviderSyncInterval(); d != DefaultProviderSyncInterval {
		t.Errorf("default %v", d)
	}
	for _, tc := range []struct {
		val  string
		want time.Duration // 0: refused
	}{{"15m", 15 * time.Minute}, {"24h", 24 * time.Hour}, {"14m", 0}, {"30s", 0}} {
		dir := copyExample(t)
		path := filepath.Join(dir, "providers.yaml")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		b = []byte(strings.Replace(string(b), "interval: 6h", "interval: "+tc.val, 1))
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		p, err := Load(dir)
		switch {
		case tc.want == 0 && (err == nil || !strings.Contains(err.Error(), "provider_sync.interval")):
			t.Errorf("%s: want a provider_sync.interval error, got %v", tc.val, err)
		case tc.want != 0 && err != nil:
			t.Errorf("%s: %v", tc.val, err)
		case tc.want != 0 && p.ProviderSyncInterval() != tc.want:
			t.Errorf("%s: interval %v", tc.val, p.ProviderSyncInterval())
		}
	}
}
