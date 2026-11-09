package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProviderStalledAfter(t *testing.T) {
	t.Parallel()
	d := func(v time.Duration) *Duration { x := Duration(v); return &x }
	for _, tc := range []struct {
		name   string
		pv     *Provider
		global time.Duration
		want   time.Duration
	}{
		{"rd takes the router's", &Provider{Kind: "realdebrid"}, 10 * time.Minute, 10 * time.Minute},
		{"tb takes the router's", &Provider{Kind: "torbox"}, 10 * time.Minute, 10 * time.Minute},
		{"ad at least 25m", &Provider{Kind: "alldebrid"}, 10 * time.Minute, 25 * time.Minute},
		{"ad keeps a longer router value", &Provider{Kind: "alldebrid"}, time.Hour, time.Hour},
		{"ad off with the router off", &Provider{Kind: "alldebrid"}, 0, 0},
		{"override", &Provider{Kind: "alldebrid", StalledAfter: d(40 * time.Minute)}, 10 * time.Minute, 40 * time.Minute},
		{"override off", &Provider{Kind: "realdebrid", StalledAfter: d(0)}, 10 * time.Minute, 0},
		{"override with the router off", &Provider{Kind: "torbox", StalledAfter: d(30 * time.Minute)}, 0, 30 * time.Minute},
		{"unknown provider", nil, 10 * time.Minute, 10 * time.Minute},
	} {
		if got := tc.pv.EffectiveStalledAfter(tc.global); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	p := &Policy{Providers: map[string]*Provider{"ad": {Kind: "alldebrid"}}}
	if got := p.ProviderStalledAfter("ad"); got != 25*time.Minute {
		t.Errorf("policy default for ad: %v", got)
	}
	if got := p.ProviderStalledAfter("nope"); got != DefaultRouterStalledAfter {
		t.Errorf("unknown key: %v", got)
	}
}

func TestProviderPacingValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		yaml, want string
	}{
		{"pacing: {min_gap: 20s}", ""},
		{"pacing: {min_gap: 0s}", ""},
		{"pacing: {min_gap: 11m}", "providers[rd].pacing.min_gap"},
		{"pacing: {min_gap: -1s}", "providers[rd].pacing.min_gap"},
		{"stalled_after: 25m", ""},
		{"stalled_after: 0s", ""},
		{"stalled_after: 30s", "providers[rd].stalled_after"},
		{"stalled_after: 25h", "providers[rd].stalled_after"},
	} {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "providers.yaml"), "providers:\n  rd:\n    kind: realdebrid\n    order: 1\n"+
			"    url: https://api.real-debrid.com/rest/1.0\n    api_key_env: THEATARR_RD_KEY\n    "+tc.yaml+"\n")
		_, err := Load(dir)
		switch {
		case tc.want == "" && err != nil && strings.Contains(err.Error(), "providers[rd]"):
			t.Errorf("%s: %v", tc.yaml, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: want an error at %s, got %v", tc.yaml, tc.want, err)
		}
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProviderMagnetLimit(t *testing.T) {
	t.Parallel()
	n := func(v int) *int { return &v }
	for _, tc := range []struct {
		pv   *Provider
		want int
	}{
		{&Provider{Kind: "alldebrid"}, DefaultAllDebridMagnetLimit},
		{&Provider{Kind: "alldebrid", MagnetLimit: n(1000)}, 1000},
		{&Provider{Kind: "realdebrid"}, 0},
		{&Provider{Kind: "torbox"}, 0},
		{nil, 0},
	} {
		if got := tc.pv.TorrentLimit(); got != tc.want {
			t.Errorf("%+v: %d, want %d", tc.pv, got, tc.want)
		}
	}
	if NearTorrentLimit(4499, 5000) || !NearTorrentLimit(4500, 5000) || NearTorrentLimit(10, 0) {
		t.Error("NearTorrentLimit is not 90% of the limit")
	}
	for _, tc := range []struct {
		yaml, want string
	}{
		{"kind: alldebrid\n    magnet_limit: 1000", ""},
		{"kind: alldebrid\n    magnet_limit: 0", "providers[ad].magnet_limit"},
		{"kind: alldebrid\n    magnet_limit: 1000001", "providers[ad].magnet_limit"},
		{"kind: realdebrid\n    magnet_limit: 1000", "providers[ad].magnet_limit"},
	} {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "providers.yaml"), "providers:\n  ad:\n    "+tc.yaml+"\n    order: 1\n"+
			"    url: https://api.alldebrid.com/v4\n    api_key_env: THEATARR_AD_KEY\n")
		_, err := Load(dir)
		switch {
		case tc.want == "" && err != nil && strings.Contains(err.Error(), "magnet_limit"):
			t.Errorf("%s: %v", tc.yaml, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: want an error at %s, got %v", tc.yaml, tc.want, err)
		}
	}
}
