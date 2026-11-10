package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/settings"
)

const mcpRevision = "2025-06-18"

type mcpContextKey struct{}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func (w *Web) mountMCP(mux *http.ServeMux) {
	w.handle(mux, "POST "+APIBase+"/mcp", SignedIn, auth.PermBrowse, w.apiMCP)
}

// apiMCP is stateless Streamable HTTP: each request answers JSON and notifications answer 202 without a body.
func (w *Web) apiMCP(rw http.ResponseWriter, r *http.Request) {
	if !mcpOrigin(r) {
		writeJSON(rw, http.StatusForbidden, apiError{Error: "MCP requires a same-origin request"})
		return
	}
	if !jsonRequest(r) {
		writeJSON(rw, http.StatusUnsupportedMediaType, apiError{Error: "MCP requires application/json"})
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, maxMigrateBody))
	if err != nil {
		badRequest(rw, "MCP request too large or unreadable")
		return
	}
	replyError := func(id json.RawMessage, code int, msg string) {
		if len(id) == 0 {
			id = json.RawMessage("null")
		}
		writeJSON(rw, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id,
			"error": map[string]any{"code": code, "message": msg}})
	}
	if !json.Valid(b) {
		replyError(nil, -32700, "parse error")
		return
	}
	var req mcpRequest
	if json.Unmarshal(b, &req) != nil || req.JSONRPC != "2.0" || req.Method == "" || !mcpID(req.ID) {
		replyError(nil, -32600, "invalid request")
		return
	}
	// Initialization negotiates the version; only subsequent requests must use our supported revision.
	if rev := r.Header.Get("MCP-Protocol-Version"); req.Method != "initialize" && rev != "" && rev != mcpRevision {
		badRequest(rw, "unsupported MCP protocol version")
		return
	}
	if len(req.ID) == 0 {
		rw.WriteHeader(http.StatusAccepted)
		return
	}
	var result any
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string          `json:"protocolVersion"`
			Capabilities    map[string]any  `json:"capabilities"`
			ClientInfo      json.RawMessage `json:"clientInfo"`
			Meta            map[string]any  `json:"_meta,omitempty"`
		}
		if !mcpDecode(req.Params, &p) || strings.TrimSpace(p.ProtocolVersion) == "" || p.Capabilities == nil || !mcpClientInfo(p.ClientInfo) {
			replyError(req.ID, -32602, "invalid initialize params")
			return
		}
		instructions := "A change that needs confirmation is refused with its effects and a token: show the effects to the user and, once they agree, call the tool again with the same arguments and confirmation_token. api_request reaches every other API route with the same permissions and confirmations."
		result = map[string]any{"protocolVersion": mcpRevision, "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo":   map[string]any{"name": "Theatarr", "version": w.version},
			"instructions": instructions}
	case "ping", "tools/list":
		var p struct {
			Meta map[string]any `json:"_meta,omitempty"`
		}
		if len(req.Params) > 0 && !mcpDecode(req.Params, &p) {
			replyError(req.ID, -32602, "invalid params")
			return
		}
		result = map[string]any{}
		if req.Method == "tools/list" {
			list := []mcpTool{}
			for _, tool := range w.mcpTools() {
				if w.perms(auth.FromContext(r.Context())).Has(tool.perm) {
					list = append(list, tool)
				}
			}
			result = map[string]any{"tools": list}
		}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Meta      map[string]any  `json:"_meta,omitempty"`
		}
		if !mcpDecode(req.Params, &p) || p.Name == "" {
			replyError(req.ID, -32602, "invalid tools/call params")
			return
		}
		var found *mcpTool
		for _, tool := range w.mcpTools() {
			if tool.Name == p.Name {
				found = &tool
				break
			}
		}
		if found == nil {
			replyError(req.ID, -32602, "unknown tool; see tools/list, and api_request for any other API route")
			return
		}
		if !w.perms(auth.FromContext(r.Context())).Has(found.perm) {
			result = mcpResult(http.StatusForbidden, apiError{Error: "this tool requires admin permission"})
			break
		}
		if len(p.Arguments) == 0 {
			p.Arguments = json.RawMessage("{}")
		}
		// The caller confirms as it does over the API (its token is bound to it); the marker makes the audit say "via MCP".
		r = r.WithContext(context.WithValue(r.Context(), mcpContextKey{}, true))
		write := found.write
		if found.Name == "api_request" {
			// A GET through api_request is a read: no audit, and it works in YAML settings mode too.
			var call struct{ Method any }
			_ = json.Unmarshal(p.Arguments, &call)
			write = call.Method != "GET"
		}
		if write {
			if w.settings == nil {
				result = mcpResult(http.StatusConflict, apiError{Error: "MCP writes require database settings"})
				break
			}
			// Audit attempts before validating arguments. Never persist arguments, which may contain credentials:
			// api_request records only its method and path (no query, no body).
			var detail any
			if found.Name == "api_request" {
				var call struct{ Method, Path any }
				_ = json.Unmarshal(p.Arguments, &call)
				method, _ := call.Method.(string)
				path, _ := call.Path.(string)
				path, _, _ = strings.Cut(path, "?")
				detail = map[string]string{"method": method, "path": path}
			}
			// Before the first save there is no generation to audit at; the call carries on (a settings save writes
			// its own audit).
			if err := w.settings.RecordAction(r.Context(), settingsActor(r), "mcp", found.Name, "call", detail); err != nil &&
				!errors.Is(err, settings.ErrUninitialized) {
				result = mcpResult(http.StatusInternalServerError, apiError{Error: "could not audit MCP write"})
				break
			}
		}
		var args map[string]any
		if json.Unmarshal(p.Arguments, &args) != nil || !mcpValidate(found.InputSchema, args) {
			replyError(req.ID, -32602, "invalid tool arguments; see tools/list inputSchema")
			return
		}
		result = w.mcpCall(r, *found, args)
	default:
		replyError(req.ID, -32601, "method not found")
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}

func mcpOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	// As the browser sees it: https behind a proxy that says X-Forwarded-Proto: https (requestOrigin).
	scheme, _, _ := strings.Cut(requestOrigin(r), "://")
	return err == nil && u.Scheme == scheme && strings.EqualFold(u.Host, r.Host) && u.User == nil &&
		u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}

func mcpID(id json.RawMessage) bool {
	if len(id) == 0 {
		return true
	}
	var v any
	if json.Unmarshal(id, &v) != nil {
		return false
	}
	switch v.(type) {
	case string, float64:
		return true
	}
	return false
}

func mcpClientInfo(raw json.RawMessage) bool {
	var v struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	return json.Unmarshal(raw, &v) == nil && v.Name != "" && v.Version != ""
}

func mcpDecode(raw []byte, v any) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(v) == nil
}

func mcpResult(status int, data any) map[string]any {
	structured := map[string]any{"status": status, "data": data}
	b, _ := json.Marshal(structured)
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": string(b)}},
		"structuredContent": structured, "isError": status >= 400}
}
