package main

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
)

// examplePolicy copies the example policy and points check_url at a local echo server that answers the caller's
// address, with vpnIP as the expected exit.
func examplePolicy(t *testing.T, vpnIP string) string {
	t.Helper()
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		io.WriteString(w, host)
	}))
	t.Cleanup(echo.Close)
	src := filepath.Join("..", "..", "examples", "policy")
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if rel == "egress.yaml" {
			// The example ships the guard commented out (off); these tests turn it on.
			s := string(b)
			i := strings.Index(s, "# egress:")
			if i < 0 {
				return errors.New("egress.yaml has no commented egress section")
			}
			on := ""
			for _, line := range strings.SplitAfter(s[i:], "\n") {
				on += strings.TrimPrefix(strings.TrimPrefix(line, "#"), " ")
			}
			on = strings.Replace(on, "https://api.ipify.org", echo.URL, 1)
			b = []byte(s[:i] + strings.Replace(on, "198.51.100.7", vpnIP, 1))
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestEgressCheckCommand(t *testing.T) {
	t.Parallel()
	if err := runEgressCheck(commandSettingsArgs(t, []string{"-policy", examplePolicy(t, "127.0.0.1")})); err != nil {
		t.Fatalf("on the VPN: %v", err)
	}
	err := runEgressCheck(commandSettingsArgs(t, []string{"-policy", examplePolicy(t, "192.0.2.1")}))
	if err == nil || !strings.Contains(err.Error(), "1 of 1 egress path(s) not on the VPN") {
		t.Fatalf("off the VPN: %v", err)
	}
}

func TestPrintEgress(t *testing.T) {
	t.Parallel()
	pol := &policy.Policy{Egress: &policy.Egress{VPNIPs: []string{"198.51.100.1"}, VPNCIDRs: []string{"10.8.0.0/16"}}}
	var b strings.Builder
	err := printEgress(&b, pol, []gateway.EgressState{
		{Path: "direct", OK: true, IP: "198.51.100.1"},
		{Path: "http://gluetun:8888", Error: "dial tcp: refused"},
	})
	if err == nil {
		t.Error("a failed path must fail the command")
	}
	out := b.String()
	for _, w := range []string{"vpn set: 198.51.100.1, 10.8.0.0/16", "direct", "VPN OK", "NOT VPN: dial tcp: refused"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
}

func TestEgressCheckOptional(t *testing.T) {
	t.Parallel()
	for _, classes := range []string{"off", "[]", "[providers]"} {
		t.Run(classes, func(t *testing.T) {
			dir := examplePolicy(t, "192.0.2.1")
			path := filepath.Join(dir, "egress.yaml")
			if classes == "off" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(strings.Replace(string(b), "require_vpn_for: [indexers]", "require_vpn_for: "+classes, 1)), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			err := runEgressCheck(commandSettingsArgs(t, []string{"-policy", dir}))
			if classes == "[providers]" {
				if err == nil || !strings.Contains(err.Error(), "requests for providers") || strings.Contains(err.Error(), "indexer") {
					t.Fatalf("provider requirement: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			pol, err := policy.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			err = printEgress(&out, pol, []gateway.EgressState{{Path: "direct", Error: "not on VPN"}})
			if classes == "off" && (err != nil || !strings.Contains(out.String(), "VPN guard off")) {
				t.Fatalf("off: %s, %v", out.String(), err)
			}
			if classes == "[]" && (err != nil || !strings.Contains(out.String(), "monitor-only") || !strings.Contains(out.String(), "NOT VPN")) {
				t.Fatalf("monitor: %s, %v", out.String(), err)
			}
		})
	}
}

func TestEgressReadinessGuardOff(t *testing.T) {
	t.Parallel()
	gw, err := gateway.New(context.Background(), &policy.Policy{}, gateway.Options{})
	if err != nil {
		t.Fatal(err)
	}
	base := func(context.Context) (map[string]any, error) { return nil, nil }
	detail, err := egressReadiness(base, gw)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := detail["egress"].(map[string]any)
	if s["mode"] != "off" || s["type"] != "warning" || s["message"] != "VPN guard off" {
		t.Fatalf("readiness: %v", detail)
	}
}

func TestEgressReadiness(t *testing.T) {
	t.Parallel()
	pol, err := policy.Load(examplePolicy(t, "127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	gw, err := gateway.New(context.Background(), pol, gateway.Options{})
	if err != nil {
		t.Fatal(err)
	}
	base := func(context.Context) (map[string]any, error) { return nil, errors.New("db down") }
	detail, err := egressReadiness(base, gw)(context.Background())
	if err == nil || detail["egress"] == nil {
		t.Fatalf("detail %v err %v", detail, err)
	}
	if detail["egress"].(map[string]any)["vpn_ok"] != false {
		t.Error("vpn_ok before any check")
	}
	gw.CheckEgress(context.Background())
	detail, _ = egressReadiness(base, gw)(context.Background())
	if detail["egress"].(map[string]any)["vpn_ok"] != true {
		t.Errorf("vpn_ok after a good check: %v", detail["egress"])
	}
}
