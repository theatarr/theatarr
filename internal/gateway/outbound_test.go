package gateway

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// outboundRE matches the ways Go code makes HTTP requests without the gateway.
var outboundRE = regexp.MustCompile(`http\.(DefaultClient|DefaultTransport|Get\(|Post\(|PostForm\(|Head\()|http\.(Client|Transport)\{`)

// allowedOutbound lists files (or directories, ending in /) that may build their own client, and why. Everything
// here talks to the local machine or the LAN only, never to an indexer, provider or metadata source, except the
// cardigann engine, whose clients send through the gateway.
var allowedOutbound = map[string]string{
	"internal/gateway/":     "the gateway itself",
	"cmd/theatarr/serve.go": "theatarr healthcheck asks the local service's /readyz on loopback",
	"cmd/theatarr/mcp.go": "theatarr mcp forwards to the running Theatarr on loopback or the LAN; each resolved dial " +
		"address is checked before connecting, with no proxy or redirects",
	"internal/arr/": "read-only client for the LAN arr instances (parity tooling, theatarr import-arr); dials LAN addresses only (lan.go)",
	"internal/qbit/qbittest/qbittest.go": "test helper only: its Doer sends tests' requests to their httptest servers " +
		"on loopback",
	"internal/indexer/cardigann/engine.go": "the cardigann engine's clients (cookie jar, one hop at a time) only wrap the indexer's Transport and " +
		"Solver, which are the gateway; a missing one is refused, never replaced by the default transport",
}

// TestNoOtherOutboundClients keeps the gateway the single outbound HTTP path.
func TestNoOtherOutboundClients(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && rel != "." || d.Name() == "testdata" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		for p := range allowedOutbound {
			if rel == p || (strings.HasSuffix(p, "/") && strings.HasPrefix(rel, p)) {
				return nil
			}
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if outboundRE.MatchString(line) {
				t.Errorf("%s:%d: outbound HTTP outside internal/gateway: %s", rel, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
