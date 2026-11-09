package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
)

// The migration CLI checks that -service is a LAN host, but its client follows redirects (forwarding the custom
// X-Api-Key header to the new host) and honours HTTP(S)_PROXY: the admin key can leave for a host that was never
// checked.
func TestMigrationCLIKeyFollowsRedirect(t *testing.T) {
	var mu sync.Mutex
	var gotKey, gotHost string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotKey, gotHost = r.Header.Get(auth.APIKeyHeader), r.Host
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer other.Close()
	// "elsewhere": a different host name for the same loopback server (stands in for a public host).
	target := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer svc.Close()
	t.Setenv("ZZ_KEY", "admin-key-secret-123")
	f := newMigrationFlags("verify")
	*f.service = svc.URL
	*f.apiKeyEnv = "ZZ_KEY"
	var into map[string]any
	if err := f.call(context.Background(), http.MethodPost, "/plans/1/verify", nil, &into); err == nil {
		t.Error("redirect accepted")
	}
	mu.Lock()
	defer mu.Unlock()
	if gotKey != "" {
		t.Errorf("admin API key forwarded across a redirect to %s: %q", gotHost, gotKey)
	}
}

func TestMigrationCLIIgnoresAmbientProxy(t *testing.T) {
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyCalls.Add(1); w.Write([]byte(`{}`)) }))
	defer proxy.Close()
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"direct":true}`)) }))
	defer svc.Close()
	// Model the default transport's proxy setting without reading the process environment.
	proxyURL, _ := url.Parse(proxy.URL)
	old := http.DefaultTransport
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = old; transport.CloseIdleConnections() }()
	t.Setenv("MIGRATE_TEST_KEY", "fixture-admin-key")
	f := newMigrationFlags("verify")
	*f.service = svc.URL
	*f.apiKeyEnv = "MIGRATE_TEST_KEY"
	var got struct{ Direct bool }
	if err := f.call(context.Background(), http.MethodGet, "/plans/1/verify", nil, &got); err != nil || !got.Direct || proxyCalls.Load() != 0 {
		t.Fatalf("proxy intercepted request: direct=%t calls=%d err=%v", got.Direct, proxyCalls.Load(), err)
	}
}
