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
	"time"

	"github.com/theatarr/theatarr/internal/arr"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/migration"
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
	if json.Unmarshal(list.Body.Bytes(), &rpc) != nil || len(rpc.Result.Tools) != 25 {
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
		{"missing argument", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"migration_apply","arguments":{"id":1}}}`, `"code":-32602`},
		{"empty steps", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"migration_apply","arguments":{"id":1,"steps":[]}}}`, `"code":-32602`},
		{"imprecise id", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"migration_plan_show","arguments":{"id":9007199254740992}}}`, `"code":-32602`},
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
	for _, name := range []string{"health", "settings_section_update", "migration_apply", "settings_view_create", "settings_view_update", "migration_plan_create"} {
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
	// A risky change comes back with its effects and a token bound to the caller; the same call with the token makes it.
	refusal := se.mcpTool("settings_section_update", body, 422).(map[string]any)
	if se.generation() != gen || refusal["confirm_in_ui"] != nil ||
		!strings.Contains(fmt.Sprint(refusal["effects"]), "library.mount.link_path") {
		t.Fatalf("MCP refusal: %v", refusal)
	}
	body["confirmation_token"] = confirmRefusal(t, refusal, "library.mount.link_path")
	se.mcpTool("settings_section_update", body, 200)
	if se.generation() != gen+1 {
		t.Fatal("MCP confirmation did not apply")
	}
	// Writes that need no confirmation apply at once.
	se.mcpTool("settings_section_update", map[string]any{"key": "library", "generation": se.generation(),
		"yaml":    findSection(t, se.mcpTool("settings_sections", map[string]any{}, 200).(map[string]any), "library")["yaml"],
		"secrets": map[string]string{"plex.token": "other-" + testSecret}}, 200)

	create := map[string]any{"generation": se.generation(), "key": "mcp-radarr", "flavour": "radarr", "path": "/mcp-radarr",
		"versions": []string{"movie-web"}, "api_key": testSecret}
	refusal = se.mcpTool("settings_view_create", create, 422).(map[string]any)
	create["confirmation_token"] = confirmRefusal(t, refusal, "Create view")
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

func TestMCPMigrationTools(t *testing.T) {
	t.Parallel()
	se := newQualityEnv(t)
	radarr := fakeRadarr(t)
	se.web.migration = &Migration{Checkpoint: func(context.Context) (migration.Checkpoint, error) {
		return migration.Checkpoint{At: testNow, Backup: "/backups/test", Generation: se.generation()}, nil
	}, Dial: migration.NewDialer(arr.Options{Timeout: time.Second, MinInterval: time.Millisecond, Retries: 1})}
	args := map[string]any{"sources": migration.Sources{Arr: []migration.ArrSource{{Name: "radarr_uhd", Flavour: "radarr", URL: radarr.URL, Key: "RADARR_KEY"}}},
		"keys": map[string]string{"RADARR_KEY": migrateArrKey}}
	se.mcpTool("migration_inspect", args, 200)
	gen := se.generation()
	plan := se.mcpTool("migration_plan_create", args, 201).(map[string]any)["plan"].(map[string]any)
	if se.generation() != gen {
		t.Fatal("plan creation changed settings")
	}
	id := plan["id"]
	se.mcpTool("migration_plan_list", map[string]any{}, 200)
	se.mcpTool("migration_plan_show", map[string]any{"id": id}, 200)
	se.mcpTool("migration_verify", map[string]any{"id": id}, 200)
	var quality string
	for _, raw := range plan["steps"].([]any) {
		step := raw.(map[string]any)
		if step["kind"] == migration.KindQuality {
			quality = step["id"].(string)
		}
	}
	if quality == "" {
		t.Fatal("no quality step")
	}
	se.mcpTool("migration_apply", map[string]any{"id": id, "steps": []string{quality}}, 200)
	var n int
	if err := se.st.Reader().QueryRow(`SELECT count(*) FROM settings_audit WHERE entity = 'mcp' AND key IN ('migration_plan_create', 'migration_apply') AND actor LIKE '%via MCP%'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("migration audit %d: %v", n, err)
	}
	if strings.Contains(strings.Join(se.bodies, "\n")+se.logs.String(), migrateArrKey) {
		t.Fatal("migration returned a credential")
	}
}

func TestMCPMigrationConfirmation(t *testing.T) {
	t.Parallel()
	se, _ := activeTestVersion(t)
	snap, err := se.web.settings.Snapshot(t.Context())
	if err != nil || len(snap.Records.Views) == 0 {
		t.Fatalf("snapshot: %v", err)
	}
	const sid = "view/test"
	p := &migration.Plan{SchemaVersion: migration.SchemaVersion, CreatedAt: testNow, UpdatedAt: testNow,
		Steps: []migration.Step{{ID: sid, Kind: migration.KindView, Automatic: true, Status: migration.StatusPending,
			Payload: &migration.Payload{Key: snap.Records.Views[0].Key, Versions: []string{"test-version"}}}}}
	if err := migration.Save(t.Context(), se.st, p, &migration.Inventory{SchemaVersion: migration.SchemaVersion}); err != nil {
		t.Fatal(err)
	}
	checkpoints := 0
	se.web.migration = &Migration{Checkpoint: func(context.Context) (migration.Checkpoint, error) {
		checkpoints++
		return migration.Checkpoint{At: testNow, Backup: "/backups/test"}, nil
	}}
	args := map[string]any{"id": p.ID, "steps": []string{sid}}
	gen := se.generation()
	// The user applies the step in the UI, which stores the session's token in the plan.
	ui := se.want(se.migrateCall("POST", fmt.Sprintf("/plans/%v/apply", p.ID), map[string]any{"steps": []string{sid}}), 200, "UI apply")
	step := ui["steps"].([]any)[0].(map[string]any)
	sessionToken, _ := step["confirmation"].(map[string]any)["token"].(string)
	if step["status"] != migration.StatusConfirmation || sessionToken == "" || se.generation() != gen {
		t.Fatalf("UI apply: %v", step)
	}
	// MCP on an API key: the session's token is not the key's. The step stays unapplied with a token for the key.
	key, _, err := se.auth.CreateKey(t.Context(), "migrate-key", 1)
	if err != nil {
		t.Fatal(err)
	}
	se.mcpKey = key
	args["confirmation_token"] = sessionToken
	step = se.mcpTool("migration_apply", args, 200).(map[string]any)["steps"].([]any)[0].(map[string]any)
	keyToken, _ := step["confirmation"].(map[string]any)["token"].(string)
	if step["status"] != migration.StatusConfirmation || keyToken == "" || keyToken == sessionToken || se.generation() != gen {
		t.Fatalf("key redeemed the session's token: %v", step)
	}
	// One token confirms one step.
	if out := se.mcpTool("migration_apply", map[string]any{"id": p.ID, "steps": []string{sid, "other"}, "confirmation_token": keyToken}, 400).(map[string]any); !strings.Contains(out["error"].(string), "exactly one step") {
		t.Fatalf("two steps: %v", out)
	}
	args["confirmation_token"] = keyToken
	done := se.mcpTool("migration_apply", args, 200).(map[string]any)
	if done["steps"].([]any)[0].(map[string]any)["status"] != migration.StatusDone || se.generation() == gen {
		t.Fatalf("key-confirmed migration: %v", done)
	}
	var actor string
	if err := se.st.Reader().QueryRow(`SELECT actor FROM settings_audit WHERE entity != 'mcp' ORDER BY id DESC LIMIT 1`).Scan(&actor); err != nil ||
		actor != "migrate:admin (api key migrate-key) via MCP, confirmed by api key migrate-key" {
		t.Fatalf("audit actor %q: %v", actor, err)
	}
	if checkpoints != 3 {
		t.Fatalf("checkpoints %d", checkpoints)
	}
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

// A migrated version is created in shadow with grab off: apply refuses the step itself, through MCP as in the UI.
func TestMCPMigrationActivationLimits(t *testing.T) {
	t.Parallel()
	se := newQualityEnv(t)
	se.web.migration = &Migration{Checkpoint: func(context.Context) (migration.Checkpoint, error) {
		return migration.Checkpoint{}, nil
	}}
	gen := se.generation()
	for _, config := range []string{`{"mode":"active","grab":false}`, `{"mode":"shadow","grab":true}`} {
		p := &migration.Plan{SchemaVersion: migration.SchemaVersion, CreatedAt: testNow, UpdatedAt: testNow,
			Steps: []migration.Step{{ID: "version/unsafe", Kind: migration.KindVersion, Automatic: true, Status: migration.StatusPending,
				Payload: &migration.Payload{Key: "unsafe", Config: json.RawMessage(config)}}}}
		if err := migration.Save(t.Context(), se.st, p, &migration.Inventory{SchemaVersion: migration.SchemaVersion}); err != nil {
			t.Fatal(err)
		}
		out := se.mcpTool("migration_apply", map[string]any{"id": p.ID, "steps": []string{"version/unsafe"}}, 200).(map[string]any)
		step := out["steps"].([]any)[0].(map[string]any)
		if step["status"] != migration.StatusFailed || !strings.Contains(step["error"].(string), "Watch only") || se.generation() != gen {
			t.Fatalf("activation step: %v", step)
		}
	}
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
