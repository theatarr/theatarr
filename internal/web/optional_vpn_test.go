package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestDashboardOptionalVPN(t *testing.T) {
	for _, mode := range []string{"off", "monitor-only"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnvWith(t, func(c *Config) {
				if mode == "off" {
					c.Policy.Egress = nil
				} else {
					// The example ships the guard off; monitor-only is a guard requiring nothing.
					c.Policy.Egress = &policy.Egress{CheckURL: "https://api.ipify.org", VPNIPs: []string{"198.51.100.7"},
						RequireVPNFor: []string{}}
				}
				gw, err := gateway.New(context.Background(), c.Policy, gateway.Options{Store: c.Store, Logger: c.Logger})
				if err != nil {
					t.Fatal(err)
				}
				// No egress probe: an unverified optional VPN must still allow these local reads.
				c.Egress = gw
			})
			key := e.apiKey()
			get := func(path string, out any) {
				t.Helper()
				r := httptest.NewRequest(http.MethodGet, APIBase+path, nil)
				r.Header.Set(auth.APIKeyHeader, key)
				rec := e.do(r)
				if rec.Code != 200 {
					t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
				}
				if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
					t.Fatal(err)
				}
			}
			var status Status
			get("/status", &status)
			if status.Version != "test" || status.Counts["title"] != 3 || status.Schema["current"] != float64(112) {
				t.Fatalf("status: %+v", status)
			}
			var state GatewayState
			get("/gateway", &state)
			if state.Egress["mode"] != mode || state.Egress["vpn_ok"] != false || state.Budgets == nil || state.Backoffs == nil {
				t.Fatalf("gateway: %+v", state)
			}
			rec := e.get("/admin", e.login())
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<title>spa</title>") {
				t.Fatalf("admin: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}
