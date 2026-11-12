package web

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/errinbox"
	"github.com/theatarr/theatarr/internal/settings"
	"go.yaml.in/yaml/v3"
)

type mcpTool struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema"`
	perm         auth.Perm
	write        bool
	method       string
	path         string
	handler      http.HandlerFunc
}

func mcpObject(properties map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

func mcpString() map[string]any { return map[string]any{"type": "string"} }
func mcpStrings() map[string]any {
	return map[string]any{"type": "array", "items": mcpString()}
}
func mcpMap(value any) map[string]any {
	return map[string]any{"type": "object", "additionalProperties": value}
}
func mcpInteger(minimum int) map[string]any {
	return map[string]any{"type": "integer", "minimum": minimum, "maximum": 9007199254740991}
}

// mcpConfirmTools are the write tools whose changes can need confirmation: they take confirmation_token, the token
// of the 422 answer they returned.
var mcpConfirmTools = []string{"settings_section_update"}

// mcpConfirmNote ends the description of an mcpConfirmTools tool.
const mcpConfirmNote = " A change that needs confirmation returns its effects and a token: show the effects to the user, then call again with the same arguments and confirmation_token to make it."

func (w *Web) mcpTools() []mcpTool {
	empty := func() map[string]any { return mcpObject(map[string]any{}) }
	id := func() map[string]any { return mcpObject(map[string]any{"id": mcpInteger(1)}, "id") }
	tools := []mcpTool{}
	add := func(name, description, method, path string, schema map[string]any, perm auth.Perm, write bool, handler http.HandlerFunc) {
		if slices.Contains(mcpConfirmTools, name) {
			schema["properties"].(map[string]any)["confirmation_token"] = mcpString()
			description += mcpConfirmNote
		}
		tools = append(tools, mcpTool{Name: name, Description: description, InputSchema: schema,
			OutputSchema: mcpObject(map[string]any{"status": mcpInteger(100), "data": map[string]any{}}, "status", "data"),
			perm:         perm, write: write, method: method, path: path, handler: handler})
	}
	admin, browse := auth.PermAdmin, auth.PermBrowse
	add("health", "Read health checks.", "GET", "/health", empty(), admin, false, w.apiHealth)
	add("status", "Read service status and build version.", "GET", "/status", empty(), admin, false, w.apiStatus)
	add("setup", "Read setup checks and missing configuration.", "GET", "/setup", empty(), admin, false, w.apiSetup)
	add("versions", "List library versions.", "GET", "/versions", empty(), browse, false, w.apiVersions)
	add("indexers", "List indexers with credential set markers.", "GET", "/settings/indexers", empty(), admin, false, w.apiSettingsList(entityIndexers))
	add("providers", "List providers with credential set markers.", "GET", "/settings/providers", empty(), admin, false, w.apiSettingsList(entityProviders))
	add("settings_sections", "Read settings sections with credential set markers.", "GET", "/settings/sections", empty(), admin, false, w.apiSettingsSections)
	add("settings_views", "Read view identities and bindings.", "GET", "/settings/view-bindings", empty(), admin, false, w.apiSettingsViewBindings)
	add("journal", "Read recent decisions and grab journal entries.", "GET", "/journal", mcpObject(map[string]any{
		"title": mcpString(), "version": mcpString(), "since": mcpString(), "cursor": mcpString(),
		"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 500},
	}), admin, false, w.apiJournal)
	add("activity", "Read queue and download activity.", "GET", "/activity", empty(), browse, false, w.apiActivity)
	add("library_search", "Search library titles by name.", "GET", "/titles", mcpObject(map[string]any{
		"q": mcpString(), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 500}, "offset": mcpInteger(0),
	}, "q"), browse, false, w.apiTitles)
	add("errors_list", "List the error inbox: unexpected failures grouped by fingerprint, newest sighting first. status: new, seen, resolved, ignored or open (new and seen); since: a duration such as 24h or an RFC 3339 time.", "GET", "/errors", mcpObject(map[string]any{
		"status": map[string]any{"type": "string", "enum": append([]string{"open"}, errinbox.Statuses...)},
		"source": map[string]any{"type": "string", "enum": errinbox.Sources}, "since": mcpString(),
		"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 500}, "offset": mcpInteger(0),
	}), admin, false, w.apiErrors)
	add("errors_get", "Read one error inbox entry with its context and stack.", "GET", "/errors/{id}", id(), admin, false, w.apiError)
	add("errors_resolve", "Mark an error inbox entry resolved, with an optional note (a fix branch, say). It comes back as new if it happens again.", "POST", "/errors/{id}",
		mcpObject(map[string]any{"id": mcpInteger(1), "note": mcpString()}, "id"), admin, false, w.apiErrorUpdate(errinbox.StatusResolved))
	add("preflight", "Run read-only install preflight checks.", "POST", "/preflight", mcpObject(map[string]any{
		"sources": map[string]any{"type": "boolean"},
	}), admin, false, w.apiPreflight)

	keys := mcpMap(mcpString())
	sectionKeys := []string{}
	for _, spec := range settings.Sections {
		sectionKeys = append(sectionKeys, spec.Key)
	}
	add("settings_section_update", "Replace a settings section with API validation.", "PUT", "/settings/sections/{key}",
		mcpObject(map[string]any{
			"key": map[string]any{"type": "string", "enum": sectionKeys}, "generation": mcpInteger(0),
			"config": map[string]any{"type": []string{"object", "null"}}, "yaml": mcpString(),
			"secrets": keys, "clear_secrets": mcpStrings(),
		}, "key", "generation"), admin, true, w.apiSettingsSectionWrite)
	view := func() map[string]any {
		return map[string]any{"key": mcpString(), "generation": mcpInteger(0),
			"flavour": map[string]any{"type": "string", "enum": []string{"radarr", "sonarr"}}, "path": mcpString(),
			"alias_from": mcpStrings(), "versions": mcpStrings(), "profiles": mcpMap(mcpStrings()),
			"api_key": mcpString(), "clear_api_key": map[string]any{"type": "boolean"}}
	}
	add("settings_view_create", "Create a view.", "POST", "/settings/view-bindings",
		mcpObject(view(), "key", "generation", "flavour", "path", "api_key"), admin, true, w.apiSettingsViewCreate)
	add("settings_view_update", "Edit a view identity or binding.", "PUT", "/settings/view-bindings/{key}",
		mcpObject(view(), "key", "generation"), admin, true, w.apiSettingsViewUpdate)
	add("api_request", "Call any Theatarr API route as this caller: the same permissions, validation, confirmations and audit as the API (GET /api/theatarr/v1/openapi.json lists them). path starts with /api/theatarr/v1/ and may carry a query; body is the JSON request body; confirmation_token is sent as the X-Confirmation-Token header (routes that take the token in the body, such as torrent delete or restore, read it from body).",
		"", "", mcpObject(map[string]any{
			"method": map[string]any{"type": "string", "enum": []string{"GET", "POST", "PUT", "PATCH", "DELETE"}},
			"path":   mcpString(), "body": map[string]any{}, "confirmation_token": mcpString(),
		}, "method", "path"), browse, true, nil)
	return tools
}

// mcpValidate covers the schema vocabulary used above. Policy-shaped config is validated by the API handler.
func mcpValidate(s map[string]any, v any) bool {
	if e, ok := s["enum"].([]string); ok {
		str, ok := v.(string)
		if !ok {
			return false
		}
		found := false
		for _, allowed := range e {
			found = found || str == allowed
		}
		if !found {
			return false
		}
	}
	types, _ := s["type"].([]string)
	if typ, ok := s["type"].(string); ok {
		types = []string{typ}
	}
	if len(types) == 0 {
		return true
	}
	for _, typ := range types {
		switch typ {
		case "null":
			if v == nil {
				return true
			}
		case "boolean":
			if _, ok := v.(bool); ok {
				return true
			}
		case "string":
			if _, ok := v.(string); ok {
				return true
			}
		case "integer":
			if n, ok := v.(float64); ok && math.Trunc(n) == n {
				lo, hasLo := s["minimum"].(int)
				hi, hasHi := s["maximum"].(int)
				return (!hasLo || n >= float64(lo)) && (!hasHi || n <= float64(hi))
			}
		case "array":
			if list, ok := v.([]any); ok {
				minimum, _ := s["minItems"].(int)
				if len(list) < minimum {
					return false
				}
				for _, item := range list {
					if !mcpValidate(s["items"].(map[string]any), item) {
						return false
					}
				}
				return true
			}
		case "object":
			if obj, ok := v.(map[string]any); ok && obj != nil {
				props, _ := s["properties"].(map[string]any)
				required, _ := s["required"].([]string)
				for _, key := range required {
					if _, ok := obj[key]; !ok {
						return false
					}
				}
				for key, child := range obj {
					if prop, ok := props[key].(map[string]any); ok {
						if !mcpValidate(prop, child) {
							return false
						}
					} else if s["additionalProperties"] == false {
						return false
					} else if prop, ok := s["additionalProperties"].(map[string]any); ok && !mcpValidate(prop, child) {
						return false
					}
				}
				return true
			}
		}
	}
	return false
}

type mcpResponse struct {
	header http.Header
	status int
	bytes.Buffer
}

func (r *mcpResponse) Header() http.Header  { return r.header }
func (r *mcpResponse) WriteHeader(code int) { r.status = code }

func (w *Web) mcpCall(r *http.Request, tool mcpTool, args map[string]any) any {
	if tool.Name == "api_request" {
		return w.mcpAPIRequest(r, args)
	}
	if tool.Name == "settings_section_update" {
		key, _ := args["key"].(string)
		if key == "router" || key == "library" || key == "notify" {
			// These switches belong to the UI, including attempts hidden in YAML or an otherwise invalid config.
			var cfg map[string]any
			if raw, ok := args["config"].(map[string]any); ok {
				cfg = raw
			}
			if _, ok := cfg["enabled"]; ok || mcpYAMLSwitch(args["yaml"]) {
				return mcpResult(http.StatusForbidden, apiError{Error: "global switches are not section settings: use api_request PUT /api/theatarr/v1/settings/switches"})
			}
		}
	}
	path := tool.path
	query := url.Values{}
	body := map[string]any{}
	for key, value := range args {
		if strings.Contains(path, "{"+key+"}") {
			str, _ := value.(string)
			if n, ok := value.(float64); ok {
				str = strconv.FormatInt(int64(n), 10)
			}
			path = strings.ReplaceAll(path, "{"+key+"}", url.PathEscape(str))
			continue
		}
		if tool.method == "GET" {
			b, _ := json.Marshal(value)
			if str, ok := value.(string); ok {
				query.Set(key, str)
			} else {
				query.Set(key, string(b))
			}
		} else {
			body[key] = value
		}
	}
	b, _ := json.Marshal(body)
	req := r.Clone(r.Context())
	req.Method = tool.method
	req.URL = &url.URL{Path: APIBase + path, RawQuery: query.Encode()}
	req.Body = io.NopCloser(bytes.NewReader(b))
	for _, key := range []string{"id", "key"} {
		if str, ok := args[key].(string); ok {
			req.SetPathValue(key, str)
		} else if n, ok := args[key].(float64); ok {
			req.SetPathValue(key, strconv.FormatInt(int64(n), 10))
		}
	}
	res := &mcpResponse{header: http.Header{}, status: http.StatusOK}
	tool.handler(res, req)
	var data any
	if json.Unmarshal(res.Bytes(), &data) != nil {
		return mcpResult(http.StatusInternalServerError, apiError{Error: "invalid API response"})
	}
	// API handlers return write-only credential markers. Also scrub supplied values from upstream errors and
	// arbitrary inventory strings, including escaped and short credentials.
	secrets := []string{r.Header.Get("X-Api-Key")}
	for _, key := range []string{"keys", "secrets"} {
		if values, ok := args[key].(map[string]any); ok {
			for _, value := range values {
				if value, ok := value.(string); ok && value != "" {
					secrets = append(secrets, value)
				}
			}
		}
	}
	if key, ok := args["api_key"].(string); ok && key != "" {
		secrets = append(secrets, key)
	}
	return mcpResult(res.status, mcpScrub(data, secrets))
}

// mcpAPIRequest is the api_request tool: the request goes through the mounted mux as a fresh request with the
// caller's headers (its key, or its session cookie and CSRF token), so the route's guard authenticates and authorises
// it again and the handler confirms and audits as for the API. The MCP marker stays on the context ("via MCP").
func (w *Web) mcpAPIRequest(r *http.Request, args map[string]any) any {
	method, _ := args["method"].(string)
	raw, _ := args["path"].(string)
	path, query, _ := strings.Cut(raw, "?")
	if !mcpAPIPath(path) {
		return mcpResult(http.StatusBadRequest, apiError{Error: "path must be an API route under " + APIBase + "/ (not /auth, /mcp or /events; no .., // or escapes)"})
	}
	var body io.Reader = http.NoBody
	if v, ok := args["body"]; ok {
		b, _ := json.Marshal(v)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(r.Context(), method, path, body)
	if err != nil {
		return mcpResult(http.StatusBadRequest, apiError{Error: "invalid path"})
	}
	req.URL.RawQuery = query
	req.RemoteAddr, req.Host, req.TLS = r.RemoteAddr, r.Host, r.TLS
	for _, h := range []string{auth.APIKeyHeader, auth.CSRFHeader, "Cookie", "Origin", "User-Agent"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	if token, _ := args["confirmation_token"].(string); token != "" {
		req.Header.Set(ConfirmationHeader, token)
	}
	res := &mcpResponse{header: http.Header{}, status: http.StatusOK}
	w.mux.ServeHTTP(res, req)
	var data any
	if res.Len() > 0 && json.Unmarshal(res.Bytes(), &data) != nil {
		data = res.String()
	}
	return mcpResult(res.status, mcpScrub(data, []string{r.Header.Get(auth.APIKeyHeader)}))
}

// mcpAPIPath reports whether api_request may call path: a clean path under APIBase, not sign-in, not MCP itself and
// not the event stream (which never ends). No escapes: the route checked is the route served.
func mcpAPIPath(path string) bool {
	rest, ok := strings.CutPrefix(path, APIBase+"/")
	if !ok || rest == "" || strings.ContainsAny(path, "\\%#") || strings.Contains(path, "..") || strings.Contains(path, "//") {
		return false
	}
	first, _, _ := strings.Cut(rest, "/")
	return first != "auth" && first != "mcp" && first != "events"
}

func mcpYAMLSwitch(value any) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	var config map[string]any
	if yaml.Unmarshal([]byte(text), &config) != nil {
		return false
	}
	_, found := config["enabled"]
	return found
}

func mcpScrub(v any, secrets []string) any {
	scrub := func(s string) string {
		for _, secret := range secrets {
			if secret != "" {
				s = strings.ReplaceAll(s, secret, "***")
			}
		}
		return s
	}
	switch v := v.(type) {
	case string:
		return scrub(v)
	case map[string]any:
		out := map[string]any{}
		for key, child := range v {
			out[scrub(key)] = mcpScrub(child, secrets)
		}
		return out
	case []any:
		for i, child := range v {
			v[i] = mcpScrub(child, secrets)
		}
	}
	return v
}
