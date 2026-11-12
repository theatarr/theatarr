package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/policy"
)

// keyCall sends a settings request with an API key, as an agent does.
func (se *settingsEnv) keyCall(key, method, path string, body any) *httptest.ResponseRecorder {
	se.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			se.t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, APIBase+"/settings"+path, &buf)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(auth.APIKeyHeader, key)
	rec := se.do(r)
	se.bodies = append(se.bodies, rec.Body.String())
	return rec
}

func (se *settingsEnv) lastActor(entity string) string {
	se.t.Helper()
	var actor string
	if err := se.st.Reader().QueryRow(`SELECT actor FROM settings_audit WHERE entity = ? ORDER BY id DESC LIMIT 1`, entity).Scan(&actor); err != nil {
		se.t.Fatal(err)
	}
	return actor
}

// An admin's key creates, activates and turns grab on for a version through the same 422/preview and token round
// trips as the UI; its saves are audited as the key, confirmed by the key.
func TestAgentKeyActivatesVersion(t *testing.T) {
	t.Parallel()
	area := t.TempDir()
	se := newQualityEnvBase(t, func(p *policy.Policy) { p.Library.Dangling.Roots = []string{area} })
	key := se.apiKeyNamed("agent")
	config := se.want(se.keyCall(key, "GET", "/versions/movie-web", nil), 200, "get")["record"].(map[string]any)["config"].(map[string]any)
	root := filepath.Join(area, "test-version")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	config["library"], config["mode"] = root, "shadow"
	body := map[string]any{"generation": se.generation(), "key": "test-version", "config": config, "roots": []string{root}}
	se.want(se.keyCall(key, "POST", "/versions", body), 200, "shadow create")

	config["mode"] = "active"
	body["generation"] = se.generation()
	gen := se.generation()
	token := confirmRefusal(t, se.want(se.keyCall(key, "PUT", "/versions/test-version", body), 422, "activation"), root)
	body["confirmation_token"] = token
	se.want(se.keyCall(key, "PUT", "/versions/test-version", body), 200, "key-confirmed activation")
	if se.generation() != gen+1 {
		t.Fatal("activation not saved")
	}
	if got := se.lastActor("versions"); got != "admin (api key agent), confirmed by api key agent" {
		t.Fatalf("audit actor %q", got)
	}

	got := se.want(se.keyCall(key, "GET", "/versions/test-version", nil), 200, "read")["record"].(map[string]any)["config"].(map[string]any)
	if got["mode"] != "active" {
		t.Fatalf("version not active: %v", got)
	}
}

// One key's token is not another key's; a revoked key's token goes nowhere; a key without admin is still refused.
func TestAgentKeyTokensAreBoundToTheKey(t *testing.T) {
	t.Parallel()
	se := seededSwitchesEnv(t, nil)
	gen := se.generation()
	body := map[string]any{"generation": gen, "paused": false}
	first, other := se.apiKeyNamed("first"), se.apiKeyNamed("other")
	token := confirmRefusal(t, se.want(se.keyCall(first, "PUT", "/switches", body), 422, "first key"), "Turn Pause off")
	body["confirmation_token"] = token
	if out := se.want(se.keyCall(other, "PUT", "/switches", body), 422, "other key"); confirmRefusal(t, out, "Turn Pause off") == token {
		t.Fatal("same token for another key")
	}
	if se.generation() != gen {
		t.Fatal("another key redeemed the token")
	}

	// Revoke the first key (its owner's session): its pending token cannot be redeemed.
	var id int64
	if err := se.st.Reader().QueryRow(`SELECT id FROM api_key WHERE name = 'first'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("DELETE", fmt.Sprintf("%s/keys/%d", APIBase, id), nil)
	r.AddCookie(se.cookie)
	r.Header.Set(auth.CSRFHeader, se.csrf)
	if rec := se.do(r); rec.Code != 200 && rec.Code != 204 {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	se.want(se.keyCall(first, "PUT", "/switches", body), 401, "revoked key")
	if se.generation() != gen {
		t.Fatal("revoked key's token applied")
	}

	uid := se.account("helper", auth.RoleManager, auth.StatusActive, false)
	manager, _, err := se.auth.CreateKey(t.Context(), "helper-key", uid)
	if err != nil {
		t.Fatal(err)
	}
	se.want(se.keyCall(manager, "PUT", "/switches", map[string]any{"generation": gen, "paused": true}), 403, "manager key on an admin route")
	se.want(se.keyCall(manager, "POST", "/switches/confirm", map[string]any{"generation": gen, "paused": false}), 403, "manager key confirm")
}

// api_request runs any API route through the mux as the caller: permissions, confirmations and the audit all apply,
// and it stays inside the API.
func TestMCPAPIRequest(t *testing.T) {
	t.Parallel()
	se := seededSwitchesEnv(t, nil)
	se.mcpKey = se.apiKeyNamed("agent")
	read := se.mcpTool("api_request", map[string]any{"method": "GET", "path": APIBase + "/settings/switches?x=1"}, 200).(map[string]any)
	if read["switches"] == nil {
		t.Fatalf("read: %v", read)
	}
	// A formerly session-only route: the agent makes a key for its owner.
	made := se.mcpTool("api_request", map[string]any{"method": "POST", "path": APIBase + "/keys", "body": map[string]any{"name": "made-by-agent"}}, 201).(map[string]any)
	if made["key"] == nil {
		t.Fatalf("key create: %v", made)
	}

	gen := se.generation()
	args := map[string]any{"method": "PUT", "path": APIBase + "/settings/switches", "body": map[string]any{"generation": gen, "paused": false}}
	token := confirmRefusal(t, se.mcpTool("api_request", args, 422).(map[string]any), "Turn Pause off")
	if se.generation() != gen {
		t.Fatal("unconfirmed save")
	}
	args["confirmation_token"] = token
	se.mcpTool("api_request", args, 200)
	if se.generation() != gen+1 {
		t.Fatal("confirmed save did not apply")
	}
	if got := se.lastActor("switches"); got != "admin (api key agent) via MCP, confirmed by api key agent" {
		t.Fatalf("audit actor %q", got)
	}
	var after string
	if err := se.st.Reader().QueryRow(`SELECT after FROM settings_audit WHERE entity = 'mcp' AND key = 'api_request' ORDER BY id DESC LIMIT 1`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(after, `"method":"PUT"`) || !strings.Contains(after, `"path":"`+APIBase+`/settings/switches"`) || strings.Contains(after, "paused") {
		t.Fatalf("api_request audit: %s", after)
	}

	for _, path := range []string{"/api/other/v1/titles", "http://example.test" + APIBase + "/titles", APIBase + "/../other/v1/titles",
		APIBase + "/auth/logout", APIBase + "/%61uth/logout", APIBase + "/mcp", APIBase + "/events", APIBase + "/", "/healthz"} {
		out := se.mcpTool("api_request", map[string]any{"method": "POST", "path": path}, 400).(map[string]any)
		if !strings.Contains(out["error"].(string), "under "+APIBase) {
			t.Fatalf("%s: %v", path, out)
		}
	}

	// MCP on a signed-in session: the cookie and CSRF token carry through, and the session confirms as itself.
	se.mcpKey = ""
	if read := se.mcpTool("api_request", map[string]any{"method": "GET", "path": APIBase + "/settings/switches"}, 200).(map[string]any); read["switches"] == nil {
		t.Fatalf("session read: %v", read)
	}
	se.mcpTool("api_request", map[string]any{"method": "PUT", "path": APIBase + "/settings/switches", "body": map[string]any{"generation": gen + 1, "paused": true}}, 200)
	args = map[string]any{"method": "PUT", "path": APIBase + "/settings/switches", "body": map[string]any{"generation": gen + 2, "paused": false}}
	args["confirmation_token"] = confirmRefusal(t, se.mcpTool("api_request", args, 422).(map[string]any), "Turn Pause off")
	se.mcpTool("api_request", args, 200)
	if se.generation() != gen+3 {
		t.Fatal("session-confirmed save did not apply")
	}

	uid := se.account("helper", auth.RoleManager, auth.StatusActive, false)
	manager, _, err := se.auth.CreateKey(t.Context(), "helper-key", uid)
	if err != nil {
		t.Fatal(err)
	}
	se.mcpKey = manager
	se.mcpTool("api_request", map[string]any{"method": "PUT", "path": APIBase + "/settings/switches", "body": map[string]any{"generation": gen + 3, "paused": true}}, 403)
}

// Without database settings (YAML mode), api_request still reads; its writes are refused like the other write tools.
func TestMCPAPIRequestYAMLMode(t *testing.T) {
	t.Parallel()
	se := &settingsEnv{env: newEnv(t)}
	se.mcpKey = se.apiKey()
	se.mcpTool("api_request", map[string]any{"method": "GET", "path": APIBase + "/status"}, 200)
	se.mcpTool("api_request", map[string]any{"method": "POST", "path": APIBase + "/keys", "body": map[string]any{"name": "x"}}, 409)
}
