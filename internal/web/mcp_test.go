package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
)

func (se *settingsEnv) mcpRPC(method string, params any) *httptest.ResponseRecorder {
	se.t.Helper()
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		se.t.Fatal(err)
	}
	r := httptest.NewRequest("POST", APIBase+"/mcp", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	if se.mcpKey != "" {
		r.Header.Set(auth.APIKeyHeader, se.mcpKey)
	} else {
		r.AddCookie(se.cookie)
		r.Header.Set(auth.CSRFHeader, se.csrf)
	}
	rec := se.do(r)
	se.bodies = append(se.bodies, rec.Body.String())
	return rec
}

func (se *settingsEnv) mcpTool(name string, args any, status int) any {
	se.t.Helper()
	rec := se.mcpRPC("tools/call", map[string]any{"name": name, "arguments": args})
	var rpc struct {
		Error  any `json:"error"`
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Structured struct {
				Status int `json:"status"`
				Data   any `json:"data"`
			} `json:"structuredContent"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rpc); err != nil || rpc.Error != nil || rec.Code != 200 {
		se.t.Fatalf("%s RPC: %d %s (%v)", name, rec.Code, rec.Body.String(), err)
	}
	if rpc.Result.Structured.Status != status || rpc.Result.IsError != (status >= 400) || len(rpc.Result.Content) != 1 {
		se.t.Fatalf("%s result: %s", name, rec.Body.String())
	}
	var content any
	if rpc.Result.Content[0].Type != "text" || json.Unmarshal([]byte(rpc.Result.Content[0].Text), &content) != nil {
		se.t.Fatalf("%s content: %s", name, rec.Body.String())
	}
	return rpc.Result.Structured.Data
}

func TestMCPProtocol(t *testing.T) {
	t.Parallel()
	se := seededSectionsEnv(t)
	init := se.mcpRPC("initialize", map[string]any{"protocolVersion": mcpRevision,
		"capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	if !strings.Contains(init.Body.String(), `"protocolVersion":"2025-06-18"`) ||
		!strings.Contains(init.Body.String(), `"serverInfo"`) || !strings.Contains(init.Body.String(), `"tools":{}`) {
		t.Fatalf("initialize: %s", init.Body.String())
	}
	list := se.mcpRPC("tools/list", map[string]any{})
	var rpc struct {
		Result struct {
			Tools []mcpTool `json:"tools"`
		} `json:"result"`
	}
	if json.Unmarshal(list.Body.Bytes(), &rpc) != nil || len(rpc.Result.Tools) != 19 {
		t.Fatalf("tools/list: %s", list.Body.String())
	}
	// MCP confirms like the API (docs/mcp.md): the write tools that can need confirmation, and api_request, take the
	// token back; no other tool does.
	if !strings.Contains(init.Body.String(), "confirmation_token") {
		t.Fatalf("initialize does not explain the token: %s", init.Body.String())
	}
	for _, tool := range rpc.Result.Tools {
		_, takes := tool.InputSchema["properties"].(map[string]any)["confirmation_token"]
		if want := slices.Contains(mcpConfirmTools, tool.Name) || tool.Name == "api_request"; takes != want {
			t.Errorf("%s takes confirmation_token: %v, want %v", tool.Name, takes, want)
		}
	}
	for _, tool := range rpc.Result.Tools {
		if tool.InputSchema["type"] != "object" || tool.InputSchema["additionalProperties"] != false ||
			tool.InputSchema["required"] == nil || tool.OutputSchema["type"] != "object" {
			t.Errorf("invalid schema for %s", tool.Name)
		}
	}
	for _, tc := range []struct {
		name, raw string
		code      string
	}{
		{"unknown", `{"jsonrpc":"2.0","id":"x","method":"nope"}`, `"code":-32601`},
		{"bad initialize", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, `"code":-32602`},
		{"empty revision", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`, `"code":-32602`},
		{"bad call", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":[]}`, `"code":-32602`},
		{"bad arguments", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"library_search","arguments":{"q":1}}}`, `"code":-32602`},
		{"unknown argument", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"health","arguments":{"secret":"hidden"}}}`, `"code":-32602`},
		{"missing argument", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"errors_get","arguments":{}}}`, `"code":-32602`},
		{"imprecise id", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"errors_get","arguments":{"id":9007199254740992}}}`, `"code":-32602`},
		{"batch", `[]`, `"code":-32600`},
		{"bad version", `{"jsonrpc":"1.0","id":1,"method":"ping"}`, `"code":-32600`},
		{"bad id", `{"jsonrpc":"2.0","id":{},"method":"ping"}`, `"code":-32600`},
		{"parse", `{`, `"code":-32700`},
		{"ping", `{"jsonrpc":"2.0","id":2,"method":"ping"}`, `"result":{}`},
		{"initialized", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, ""},
		{"unknown notification", `{"jsonrpc":"2.0","method":"unknown"}`, ""},
		{"write notification", `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"settings_view_create","arguments":{}}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", APIBase+"/mcp", strings.NewReader(tc.raw))
			r.Header.Set("Content-Type", "application/json")
			r.AddCookie(se.cookie)
			r.Header.Set(auth.CSRFHeader, se.csrf)
			rec := se.do(r)
			if tc.code == "" {
				if rec.Code != 202 || rec.Body.Len() != 0 {
					t.Fatalf("notification: %d %s", rec.Code, rec.Body.String())
				}
			} else if rec.Code != 200 || !strings.Contains(rec.Body.String(), tc.code) {
				t.Fatalf("response: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestMCPAuthAndOrigin(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key, _, err := e.auth.CreateKey(t.Context(), "mcp-admin", 1)
	if err != nil {
		t.Fatal(err)
	}
	uid := e.account("viewer", auth.RoleUser, auth.StatusActive, false)
	viewer, _, err := e.auth.CreateKey(t.Context(), "mcp-viewer", uid)
	if err != nil {
		t.Fatal(err)
	}
	call := func(key, origin, raw string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", APIBase+"/mcp", strings.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Api-Key", key)
		r.Header.Set("Origin", origin)
		return e.do(r)
	}
	const ping = `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	if rec := call("", "", ping); rec.Code != 401 {
		t.Fatalf("no key: %d", rec.Code)
	}
	for _, origin := range []string{"https://evil.test", "null", "https://example.com", "http://example.com/path", "http://user@example.com"} {
		if rec := call(key, origin, ping); rec.Code != 403 {
			t.Errorf("origin %q: %d", origin, rec.Code)
		}
	}
	if rec := call(key, "http://example.com", ping); rec.Code != 200 {
		t.Fatalf("same origin: %d", rec.Code)
	}
	// Behind a proxy that ends TLS and says so, the browser's origin is https.
	r := httptest.NewRequest("POST", APIBase+"/mcp", strings.NewReader(ping))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Api-Key", key)
	r.Header.Set("Origin", "https://example.com")
	r.Header.Set("X-Forwarded-Proto", "https")
	if rec := e.do(r); rec.Code != 200 {
		t.Fatalf("https origin behind a proxy: %d", rec.Code)
	}
	list := call(viewer, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if strings.Contains(list.Body.String(), `"name":"settings_section_update"`) || !strings.Contains(list.Body.String(), `"name":"library_search"`) {
		t.Fatalf("viewer tools: %s", list.Body.String())
	}
	for _, name := range []string{"health", "settings_section_update", "settings_view_create", "settings_view_update"} {
		rec := call(viewer, "", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":{}}}`)
		if !strings.Contains(rec.Body.String(), `"status":403`) {
			t.Errorf("viewer %s: %s", name, rec.Body.String())
		}
	}
	cookie, _ := e.session(1)
	r = httptest.NewRequest("POST", APIBase+"/mcp", strings.NewReader(ping))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(cookie)
	if rec := e.do(r); rec.Code != 403 {
		t.Fatalf("session without CSRF: %d", rec.Code)
	}
}

func TestMCPSettingsConfirmationsAndLimits(t *testing.T) {
	t.Parallel()
	se := seededSectionsEnv(t)
	read := se.mcpTool("settings_sections", map[string]any{}, 200).(map[string]any)
	library := findSection(t, read, "library")
	body := map[string]any{"key": "library", "generation": se.generation(),
		"yaml":    strings.Replace(library["yaml"].(string), "link_path: /mnt/debrid", "link_path: /mnt/other", 1),
		"secrets": map[string]string{"plex.token": testSecret}}
	gen := se.generation()
	// An address or folder change applies at once (only going Live, downloads on, Pause off and deletes ask).
	se.mcpTool("settings_section_update", body, 200)
	if se.generation() != gen+1 {
		t.Fatal("MCP section update did not apply")
	}
	se.mcpTool("settings_section_update", map[string]any{"key": "library", "generation": se.generation(),
		"yaml":    findSection(t, se.mcpTool("settings_sections", map[string]any{}, 200).(map[string]any), "library")["yaml"],
		"secrets": map[string]string{"plex.token": "other-" + testSecret}}, 200)

	create := map[string]any{"generation": se.generation(), "key": "mcp-radarr", "flavour": "radarr", "path": "/mcp-radarr",
		"versions": []string{"movie-web"}, "api_key": testSecret}
	se.mcpTool("settings_view_create", create, 200)
	gen = se.generation()
	for _, section := range []string{"router", "library", "notify"} {
		for _, config := range []map[string]any{{"config": map[string]any{"enabled": true}}, {"yaml": "enabled: true\n"}, {"yaml": "{enabled: true}"}} {
			config["key"], config["generation"] = section, gen
			out := se.mcpTool("settings_section_update", config, 403).(map[string]any)
			if !strings.Contains(out["error"].(string), "/settings/switches") {
				t.Fatalf("limit message: %v", out)
			}
		}
	}
	for _, name := range []string{"settings_version_update", "grab", "torrent_delete", "settings_switches_update"} {
		rec := se.mcpRPC("tools/call", map[string]any{"name": name, "arguments": map[string]any{"mode": "active", "grab": true}})
		if !strings.Contains(rec.Body.String(), "api_request") || !strings.Contains(rec.Body.String(), `"code":-32602`) {
			t.Fatalf("unsafe tool: %s", rec.Body.String())
		}
	}
	if se.generation() != gen {
		t.Fatal("hard limit changed settings")
	}
	for _, name := range []string{"health", "status", "setup", "versions", "indexers", "providers", "settings_sections", "settings_views", "journal", "activity"} {
		se.mcpTool(name, map[string]any{}, 200)
	}
	se.mcpTool("library_search", map[string]any{"q": "test"}, 200)
	se.mcpTool("preflight", map[string]any{"sources": false}, 200)
	var actor string
	if err := se.st.Reader().QueryRow(`SELECT actor FROM settings_audit WHERE entity = 'mcp' ORDER BY id DESC LIMIT 1`).Scan(&actor); err != nil || !strings.Contains(actor, "admin via MCP") {
		t.Fatalf("audit actor %q: %v", actor, err)
	}
	var applied int
	if err := se.st.Reader().QueryRow(`SELECT count(*) FROM settings_audit WHERE entity != 'mcp' AND actor LIKE '%via MCP%'`).Scan(&applied); err != nil || applied < 1 {
		t.Fatalf("settings audit count %d: %v", applied, err)
	}
	se.noSecretAnywhere()
}

func TestMCPRedactsSuppliedSecrets(t *testing.T) {
	t.Parallel()
	for _, secret := range []string{testSecret, "x", "quoted-\"-\\-\n-secret"} {
		t.Run(secret[:1], func(t *testing.T) {
			se := seededSectionsEnv(t)
			// An invalid credential path is echoed by validation, including when it contains a supplied secret.
			out := se.mcpTool("settings_section_update", map[string]any{"key": "library", "generation": se.generation(),
				"secrets": map[string]string{"invalid." + secret: secret}}, 422)
			if mcpHasSecret(out, secret) {
				t.Fatal("tool returned supplied credential")
			}
		})
	}
}

func mcpHasSecret(value any, secret string) bool {
	switch value := value.(type) {
	case string:
		return strings.Contains(value, secret)
	case map[string]any:
		for key, child := range value {
			if strings.Contains(key, secret) || mcpHasSecret(child, secret) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if mcpHasSecret(child, secret) {
				return true
			}
		}
	}
	return false
}

type mcpAuditFailure struct{ SettingsAdmin }

func (mcpAuditFailure) RecordAction(context.Context, string, string, string, string, any) error {
	return errors.New(testSecret)
}

func TestMCPAuditCallerAndFailure(t *testing.T) {
	t.Parallel()
	se := seededSectionsEnv(t)
	key, _, err := se.auth.CreateKey(t.Context(), "mcp-audit", 1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "settings_section_update", "arguments": map[string]any{
			"key": "library", "generation": se.generation(), "secrets": map[string]string{"plex.token": testSecret}}}})
	r := httptest.NewRequest("POST", APIBase+"/mcp", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Api-Key", key)
	rec := se.do(r)
	se.bodies = append(se.bodies, rec.Body.String())
	if !strings.Contains(rec.Body.String(), `"status":200`) || strings.Contains(rec.Body.String(), key) {
		t.Fatalf("API key write: %s", rec.Body.String())
	}
	var actor string
	if err := se.st.Reader().QueryRow(`SELECT actor FROM settings_audit WHERE entity = 'mcp' ORDER BY id DESC LIMIT 1`).Scan(&actor); err != nil ||
		actor != "admin (api key mcp-audit) via MCP" {
		t.Fatalf("API key audit actor %q: %v", actor, err)
	}
	gen := se.generation()
	se.web.settings = mcpAuditFailure{SettingsAdmin: se.web.settings}
	se.mcpTool("settings_section_update", map[string]any{"key": "library", "generation": gen,
		"secrets": map[string]string{"plex.token": "changed-" + testSecret}}, 500)
	if se.generation() != gen {
		t.Fatal("write proceeded without audit")
	}
	se.noSecretAnywhere()
}

// MCP's first write on a fresh database works as the same call to the API does: there is no generation to audit
// the call at, so it goes ahead without the MCP row (the section save writes its own audit).
func TestMCPFirstWriteOnUninitializedSettings(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"settings_section_update", "api_request"} {
		se := newImportEnv(t, nil)
		if se.generation() != 0 {
			t.Fatalf("fixture generation %d", se.generation())
		}
		args := map[string]any{"key": "provider_sync", "generation": 0, "config": map[string]any{"interval": "6h"}}
		if tool == "api_request" {
			args = map[string]any{"method": "PUT", "path": APIBase + "/settings/sections/provider_sync",
				"body": map[string]any{"generation": 0, "config": map[string]any{"interval": "6h"}}}
		}
		se.mcpTool(tool, args, 200)
		if se.generation() != 1 {
			t.Fatalf("%s: generation %d after the first save", tool, se.generation())
		}
	}
}

func TestMCPNegotiatesVersionAndValidatesLaterRequests(t *testing.T) {
	t.Parallel()
	se := seededSectionsEnv(t)
	for _, offered := range []string{"2025-11-25", "2024-11-05", mcpRevision} {
		for _, header := range []string{"", offered} {
			body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":%q,"capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`, offered)
			r := httptest.NewRequest("POST", APIBase+"/mcp", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("MCP-Protocol-Version", header)
			r.Header.Set(auth.CSRFHeader, se.csrf)
			r.AddCookie(se.cookie)
			rec := se.do(r)
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"protocolVersion":"`+mcpRevision+`"`) {
				t.Errorf("initialize %s (%s): %d %s", offered, header, rec.Code, rec.Body.String())
			}
		}
	}
	for _, method := range []string{"ping", "tools/list", "notifications/initialized"} {
		for _, header := range []string{mcpRevision, "2025-11-25"} {
			id := `,"id":2`
			want := 200
			if method == "notifications/initialized" {
				id = ""
				want = 202
			}
			if header != mcpRevision {
				want = 400
			}
			r := httptest.NewRequest("POST", APIBase+"/mcp", strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0"%s,"method":%q}`, id, method)))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("MCP-Protocol-Version", header)
			r.Header.Set(auth.CSRFHeader, se.csrf)
			r.AddCookie(se.cookie)
			if rec := se.do(r); rec.Code != want {
				t.Errorf("%s (%s): %d %s", method, header, rec.Code, rec.Body.String())
			}
		}
	}
}
