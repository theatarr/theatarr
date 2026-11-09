package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/settings"
)

func seededSwitchesEnv(t *testing.T, edit func(*Config)) *settingsEnv {
	t.Helper()
	return newSettingsEnv(t, nil, func(c *Config) {
		st, err := settings.New(c.Store)
		if err != nil {
			t.Fatal(err)
		}
		initial, err := st.BootstrapSwitches(context.Background(), c.Policy, "test", nil)
		if err != nil {
			t.Fatal(err)
		}
		c.Settings, err = settings.NewRuntimeCoordinator(st, c.Policy, initial, fakeRuntime{},
			func(context.Context, *settings.Snapshot, *settings.Runtime) (any, error) { return fakeRuntime{}, nil })
		if err != nil {
			t.Fatal(err)
		}
		if edit != nil {
			edit(c)
		}
	})
}

func TestSettingsGlobalSwitchesAPI(t *testing.T) {
	t.Parallel()
	se := seededSwitchesEnv(t, nil)
	unauthenticated := se.do(httptest.NewRequest(http.MethodGet, APIBase+"/settings/switches", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated read: %d", unauthenticated.Code)
	}
	noCSRF := httptest.NewRequest(http.MethodPut, APIBase+"/settings/switches", strings.NewReader(`{"generation":0,"router_enabled":true}`))
	noCSRF.AddCookie(se.cookie)
	noCSRF.Header.Set("Content-Type", "application/json")
	if response := se.do(noCSRF); response.Code != http.StatusForbidden {
		t.Fatalf("write without CSRF: %d", response.Code)
	}
	read := se.want(se.call("GET", "/switches", nil), 200, "read")
	gen := int64(read["generation"].(float64))
	body := map[string]any{"generation": gen, "router_enabled": true}
	refused := se.want(se.call("PUT", "/switches", body), 422, "enable needs proof")
	if !strings.Contains(refused["effects"].([]any)[0].(map[string]any)["effect"].(string), "router.enabled") {
		t.Fatal("effect does not name switch")
	}
	proof := se.want(se.call("POST", "/switches/confirm", body), 200, "confirm")
	body["confirmation_token"] = proof["token"]
	saved := se.want(se.call("PUT", "/switches", body), 200, "enable confirmed")
	if int64(saved["generation"].(float64)) != gen+1 {
		t.Fatal("no generation")
	}
	read = se.want(se.call("GET", "/switches", nil), 200, "live")
	live := read["switches"].(map[string]any)
	if live["router_enabled"] != true || live["sources"].(map[string]any)["router"] != "db" {
		t.Fatalf("not live: %+v", read)
	}
	se.want(se.call("PUT", "/switches", map[string]any{"generation": gen + 1, "router_enabled": false}), 200, "off needs no proof")
	se.want(se.call("PUT", "/switches", body), 409, "stale generation")
	se.want(se.call("PUT", "/switches", map[string]any{"generation": gen + 2, "enabled": true}), 400, "unknown field")
}

func TestSettingsGlobalSwitchesForcedSources(t *testing.T) {
	t.Run("forced", func(t *testing.T) {
		t.Setenv("THEATARR_FORCE_OFF", "router")
		se := seededSwitchesEnv(t, nil)
		read := se.want(se.call("GET", "/switches", nil), 200, "forced")
		if read["switches"].(map[string]any)["sources"].(map[string]any)["router"] != "forced off by environment" {
			t.Fatalf("forced source: %+v", read)
		}
	})
}

// A user's API key confirms like a session (docs/design/settings.md "Who confirms"), with tokens bound to the key: a
// session's token does not work for the key, nor the key's for the session.
func TestSettingsAPIKeyConfirms(t *testing.T) {
	t.Parallel()
	se := seededSwitchesEnv(t, nil)
	gen := se.generation()
	body := map[string]any{"generation": gen, "router_enabled": true}
	apiKey := se.apiKey()
	key := func(method, path string, body any, status int) map[string]any {
		r := httptest.NewRequest(method, APIBase+"/settings"+path, strings.NewReader(mustJSON(t, body)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(auth.APIKeyHeader, apiKey)
		return se.want(se.do(r), status, "API key "+method+" "+path)
	}
	refused := key("PUT", "/switches", body, 422)
	if refused["confirm_in_ui"] == true || confirmRefusal(t, refused, "router.enabled") == "" {
		t.Fatalf("API key refusal: %v", refused)
	}
	keyProof := key("POST", "/switches/confirm", body, 200)
	sessionProof := se.want(se.call("POST", "/switches/confirm", body), 200, "session confirm")
	body["confirmation_token"] = sessionProof["token"]
	if out := key("PUT", "/switches", body, 422); confirmRefusal(t, out, "router.enabled") == "" || se.generation() != gen {
		t.Fatalf("API key redeemed a session token: %v", out)
	}
	body["confirmation_token"] = keyProof["token"]
	se.want(se.call("PUT", "/switches", body), 422, "session redeems a key token")
	if se.generation() != gen {
		t.Fatal("session redeemed the key's token")
	}
	keyProof = key("POST", "/switches/confirm", map[string]any{"generation": gen, "router_enabled": true}, 200)
	body["confirmation_token"] = keyProof["token"]
	key("PUT", "/switches", body, 200)
	if se.generation() != gen+1 {
		t.Fatal("key's own token did not apply")
	}
}
