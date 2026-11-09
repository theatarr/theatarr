package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPStdioForwarding(t *testing.T) {
	const key = "invented-mcp-credential"
	file := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(file, []byte(key+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("THEATARR_TEST_MCP_KEY", key)
	for _, source := range [][]string{{"--key-file", file}, {"--key-env", "THEATARR_TEST_MCP_KEY"}} {
		t.Run(source[0], func(t *testing.T) {
			var methods []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/api/theatarr/v1/mcp" || r.Header.Get("X-Api-Key") != key ||
					r.Header.Get("MCP-Protocol-Version") != "2025-06-18" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("incorrect forwarded request")
				}
				var req struct {
					ID     any    `json:"id"`
					Method string `json:"method"`
				}
				dec := json.NewDecoder(r.Body)
				dec.UseNumber()
				if dec.Decode(&req) != nil {
					t.Error("invalid forwarded body")
				}
				methods = append(methods, req.Method)
				if req.ID == nil {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				result := map[string]any{}
				if req.Method == "initialize" {
					result["protocolVersion"] = "2025-06-18"
				} else {
					// Even a misbehaving upstream must not echo our key into stdout.
					result["echo"] = key
				}
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			}))
			defer srv.Close()
			in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}` + "\n" +
				`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" + `{"jsonrpc":"2.0","id":9007199254740993,"method":"ping"}` + "\n")
			var out bytes.Buffer
			args := append([]string{"--url", srv.URL}, source...)
			if err := runMCP(args, in, &out); err != nil {
				t.Fatal(err)
			}
			if strings.Join(methods, ",") != "initialize,notifications/initialized,ping" ||
				strings.Count(out.String(), "\n") != 2 || strings.Contains(out.String(), key) || !strings.Contains(out.String(), "2025-06-18") ||
				!strings.Contains(out.String(), "9007199254740993") {
				t.Fatalf("forwarding output: %s", out.String())
			}
		})
	}
}

func TestMCPStdioFailuresKeepKeyPrivate(t *testing.T) {
	const key = "invented-mcp-error-credential"
	t.Setenv("THEATARR_TEST_MCP_ERROR_KEY", key)
	for _, status := range []int{301, 401, 403, 500} {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Location", "/leak?key="+key)
			w.WriteHeader(status)
			w.Write([]byte(key))
		}))
		var out bytes.Buffer
		// A failed answer fails only its request: a request gets a JSON-RPC error, a notification nothing, and the
		// bridge keeps reading.
		in := `{"jsonrpc":"2.0","id":7,"method":"tools/call"}` + "\n" + `{"jsonrpc":"2.0","method":"notifications/x"}` + "\n"
		err := runMCP([]string{"--url", srv.URL, "--key-env", "THEATARR_TEST_MCP_ERROR_KEY"}, strings.NewReader(in), &out)
		srv.Close()
		want := fmt.Sprintf(`{"error":{"code":-32603,"message":"Theatarr answered HTTP %d"},"id":7,"jsonrpc":"2.0"}`+"\n", status)
		if err != nil || strings.Contains(out.String(), key) || out.String() != want || calls != 2 {
			t.Fatalf("status %d: err %v, %d calls, output %q", status, err, calls, out.String())
		}
	}
	for _, args := range [][]string{
		{"--key", key}, {"--key-env", "THEATARR_TEST_MCP_ERROR_KEY", "--key-file", "unused"},
		{"--key-file", "/nonexistent/" + key}, {"--url", "http://user:" + key + "@localhost", "--key-env", "THEATARR_TEST_MCP_ERROR_KEY"},
		{"--url", "http://203.0.113.1", "--key-env", "THEATARR_TEST_MCP_ERROR_KEY"},
	} {
		var out bytes.Buffer
		err := runMCP(args, strings.NewReader(""), &out)
		if err == nil || strings.Contains(err.Error()+out.String(), key) {
			t.Fatal("invalid flags succeeded or leaked credentials")
		}
	}
}

func TestMCPBridgeLANOnly(t *testing.T) {
	t.Parallel()
	for _, address := range []string{"127.0.0.1:7700", "192.168.1.2:7700", "10.1.2.3:7700", "172.16.0.1:7700", "[::1]:7700", "[fd00::1]:7700"} {
		if err := mcpLANControl("tcp", address, nil); err != nil {
			t.Errorf("LAN address %s refused", address)
		}
	}
	for _, address := range []string{"203.0.113.1:7700", "[2001:db8::1]:7700", "[fe80::1]:7700", "169.254.169.254:80", "bad"} {
		if err := mcpLANControl("tcp", address, nil); err == nil {
			t.Errorf("non-LAN address %s accepted", address)
		}
	}
}
