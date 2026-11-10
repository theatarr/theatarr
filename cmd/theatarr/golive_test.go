package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/golive"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func TestParseServiceMetrics(t *testing.T) {
	t.Parallel()
	s, err := parseServiceMetrics(strings.NewReader(`# HELP theatarr_egress_vpn_ok 1 if ...
# TYPE theatarr_egress_vpn_ok gauge
theatarr_egress_vpn_ok{path="direct"} 1
theatarr_egress_vpn_ok{path="gluetun"} 0
theatarr_gateway_backoff_seconds{subject="indexer:dmm"} 42.5
theatarr_gateway_backoff_seconds{subject="budget:tmdb"} 0
theatarr_jobs_ready{kind="meta.art"} 3
`))
	if err != nil {
		t.Fatal(err)
	}
	if !s.VPNOK["direct"] || s.VPNOK["gluetun"] || len(s.VPNOK) != 2 {
		t.Errorf("vpn = %v", s.VPNOK)
	}
	if strings.Join(s.Backoffs, ",") != "indexer:dmm" {
		t.Errorf("backoffs = %v", s.Backoffs)
	}
}

func TestGoliveCheckCommand(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "theatarr.db")
	st, err := store.Open(ctx, storetest.Seed(t, db), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	st.Close()
	t.Setenv("THEATARR_RD_KEY", "rd-secret-value")
	args := []string{"check", "-policy", filepath.Join("..", "..", "examples", "policy"), "-db", db,
		"-backup-dir", "off", "-service", "none", "-no-arr", "-no-engine", "-scan=false", "-version", "movie-web"}
	var out bytes.Buffer
	err = runGolive(commandSettingsArgs(t, args), &out)
	text := out.String()
	// The example roots do not exist here, and there is no shadow evidence: the check fails, saying why.
	if err == nil {
		t.Fatalf("no failure:\n%s", text)
	}
	for _, want := range []string{"== movie-web (movie, mode shadow", "PASS  secrets   THEATARR_RD_KEY",
		"FAIL  shadow    window", "INFO  handover  owner", "-- movie-web:"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "rd-secret-value") {
		t.Fatal("a secret value was printed")
	}

	out.Reset()
	_ = runGolive(commandSettingsArgs(t, append(args, "-json")), &out)
	var reps []golive.Report
	if err := json.Unmarshal(out.Bytes(), &reps); err != nil || len(reps) != 1 || reps[0].Version != "movie-web" {
		t.Fatalf("json: %v %+v", err, reps)
	}

	if err := runGolive(commandSettingsArgs(t, []string{"check", "-policy", filepath.Join("..", "..", "examples", "policy"), "-db", "none",
		"-version", "nope"}), &out); err == nil || !strings.Contains(err.Error(), "no library") {
		t.Errorf("unknown version: %v", err)
	}
}

func TestParseExpiry(t *testing.T) {
	t.Parallel()
	m, err := parseExpiry("tb=2027-01-31, ad=2027-01-02")
	if err != nil || m["tb"].Format("2006-01-02") != "2027-01-31" || len(m) != 2 {
		t.Fatalf("%v %v", m, err)
	}
	if _, err := parseExpiry("tb=soon"); err == nil {
		t.Error("bad date accepted")
	}
}

func TestGoliveFollowedLibraryReportsFollower(t *testing.T) {
	dir := t.TempDir()
	if err := copyPolicyDir(filepath.Join("..", "..", "examples", "policy"), dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "versions.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "versions:\n", `versions:
  movie-remux-theatarr:
    follows: movie-remux
    mode: active
    kind: movie
    anime: false
    profile: remux-2160p
    library: /tmp/theatarr-fixture-follower
    storage: debrid
    indexers: [prowlarr-public]
    providers: [rd]
    client: decypharr
    naming: {from_version: movie-remux}
`, 1))
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, "library.yaml")
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "  versions:\n", "  versions:\n    movie-remux-theatarr: {roots: [/tmp/theatarr-fixture-follower]}\n", 1))
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, selected := range []string{"", "movie-remux"} {
		args := []string{"check", "-policy", dir, "-db", "none", "-service", "none", "-no-arr", "-no-engine", "-scan=false", "-json"}
		if selected != "" {
			args = append(args, "-version", selected)
		}
		var out bytes.Buffer
		// Other readiness failures (missing roots, no DB) remain failures.
		_ = runGolive(commandSettingsArgs(t, args), &out)
		var reports []golive.Report
		if err := json.Unmarshal(out.Bytes(), &reports); err != nil {
			t.Fatalf("no report: %v: %s", err, out.String())
		}
		found := false
		for _, report := range reports {
			if report.Version != "movie-remux" {
				continue
			}
			for _, result := range report.Results {
				if result.Check == "switched policy" {
					t.Fatalf("simulated forbidden switch: %+v", result)
				}
				if result.Check == "cutover" && result.Section == golive.SecPolicy && result.Status == golive.Info && result.Message == "cut over through its follower movie-remux-theatarr (docs/migration.md#5-hand-over-clients-and-plex-retire-the-arr)" {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("missing follower INFO (-version %q): %s", selected, out.String())
		}
	}
}
