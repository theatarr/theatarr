package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/theatarr/theatarr/internal/arr"
	"github.com/theatarr/theatarr/internal/web"
)

func init() {
	register(command{name: "mcp", summary: "serve MCP over stdio, forwarding to Theatarr (docs/mcp.md)",
		run: func(args []string) error { return runMCP(args, os.Stdin, os.Stdout) }})
}

func runMCP(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	// Flag errors can contain supplied values. Nothing except protocol messages belongs on stdout.
	fs.SetOutput(io.Discard)
	address := fs.String("url", "http://localhost:7700", "Theatarr base URL")
	keyEnv := fs.String("key-env", "", "name of the environment variable holding the API key")
	keyFile := fs.String("key-file", "", "file holding the API key")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errors.New("usage: theatarr mcp --url URL (--key-env NAME | --key-file PATH)")
	}
	if (*keyEnv == "") == (*keyFile == "") {
		return errors.New("give exactly one of --key-env or --key-file; literal keys are not accepted")
	}
	u, err := url.Parse(*address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return errors.New("the Theatarr URL must be HTTP or HTTPS, without credentials, query or fragment")
	}
	if arr.CheckLiteral(u.Hostname()) != nil {
		return errors.New("MCP bridge sends its API key only to loopback or the LAN")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(u.Path, web.APIBase+"/mcp") {
		u.Path += web.APIBase + "/mcp"
	}
	u.RawPath = ""
	var key string
	if *keyEnv != "" {
		key = os.Getenv(*keyEnv)
	} else {
		f, err := os.Open(*keyFile)
		if err != nil {
			return errors.New("could not read API key file")
		}
		b, err := io.ReadAll(io.LimitReader(f, 65537))
		f.Close()
		if err != nil || len(b) > 65536 {
			return errors.New("could not read API key file (maximum 64 KiB)")
		}
		key = string(b)
	}
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, "\r\n") {
		return errors.New("API key source is empty or invalid")
	}
	// Like the migration CLI, the bridge talks to Theatarr on this machine or the LAN. Check each resolved address
	// before connecting, and never send a credential through a proxy or redirect.
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: mcpLANControl}
	transport := &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second, ForceAttemptHTTP2: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 32<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		req, err := http.NewRequest(http.MethodPost, u.String(), bytes.NewReader(line))
		if err != nil {
			return errors.New("could not build MCP request")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		req.Header.Set("X-Api-Key", key)
		res, err := client.Do(req)
		if err != nil {
			return errors.New("MCP connection failed")
		}
		b, readErr := io.ReadAll(io.LimitReader(res.Body, (32<<20)+1))
		res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			// One failed answer (a 500 during a settings swap, a proxy's 502) fails only its request: the client
			// gets a JSON-RPC error for it and the bridge keeps reading. Only a transport failure ends the bridge.
			if err := mcpHTTPError(out, line, res.StatusCode); err != nil {
				return err
			}
			continue
		}
		if readErr != nil || len(b) > 32<<20 {
			return errors.New("MCP response too large or unreadable")
		}
		if res.StatusCode == http.StatusAccepted || len(bytes.TrimSpace(b)) == 0 {
			continue
		}
		var response map[string]any
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		if dec.Decode(&response) != nil || response == nil || dec.Decode(new(any)) != io.EOF {
			return errors.New("invalid MCP JSON response")
		}
		if err := json.NewEncoder(out).Encode(mcpRedact(response, key)); err != nil {
			return errors.New("could not write MCP response")
		}
	}
	if scanner.Err() != nil {
		return errors.New("could not read MCP input (maximum 32 MiB per message)")
	}
	return nil
}

// mcpHTTPError answers a request whose HTTP exchange failed with a JSON-RPC internal error naming the status. A
// notification (no id) gets no answer. The response body is never copied: it may echo anything.
func mcpHTTPError(out io.Writer, line []byte, status int) error {
	var req struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(line, &req) != nil || len(req.ID) == 0 || string(req.ID) == "null" {
		return nil
	}
	answer := map[string]any{"jsonrpc": "2.0", "id": req.ID,
		"error": map[string]any{"code": -32603, "message": fmt.Sprintf("Theatarr answered HTTP %d", status)}}
	if err := json.NewEncoder(out).Encode(answer); err != nil {
		return errors.New("could not write MCP response")
	}
	return nil
}

func mcpLANControl(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || !arr.LANAddr(ap.Addr()) {
		return errors.New("MCP bridge sends its API key only to loopback or the LAN")
	}
	return nil
}

func mcpRedact(v any, key string) any {
	switch v := v.(type) {
	case string:
		return strings.ReplaceAll(v, key, "***")
	case map[string]any:
		out := map[string]any{}
		for name, child := range v {
			out[strings.ReplaceAll(name, key, "***")] = mcpRedact(child, key)
		}
		return out
	case []any:
		for i, child := range v {
			v[i] = mcpRedact(child, key)
		}
	}
	return v
}
