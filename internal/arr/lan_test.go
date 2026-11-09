package arr

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestLANAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1": true, "127.8.9.10": true, "::1": true, "10.1.2.3": true, "172.16.0.1": true,
		"172.31.255.255": true, "192.168.1.5": true, "fd00::5": true, "fc12::1": true, "::ffff:10.1.2.3": true,
		"172.32.0.1": false, "8.8.8.8": false, "203.0.113.9": false, "100.64.0.1": false, "169.254.1.1": false,
		"0.0.0.0": false, "2001:db8::1": false, "fe80::1": false, "::ffff:8.8.8.8": false,
	} {
		if got := LANAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: %v, want %v", addr, got, want)
		}
	}
	for h, ok := range map[string]bool{"10.1.2.4": true, "[fd00::1]": true, "radarr.test": true, "8.8.8.8": false,
		"[2001:db8::1]": false} {
		if err := CheckLiteral(h); (err == nil) != ok {
			t.Errorf("CheckLiteral(%s): %v", h, err)
		}
	}
	ctx := context.Background()
	for _, h := range []string{"10.1.2.4", "localhost", "[::1]"} {
		if err := CheckHost(ctx, h); err != nil {
			t.Errorf("%s: %v", h, err)
		}
	}
	for _, h := range []string{"8.8.8.8", "[2001:db8::1]"} {
		if err := CheckHost(ctx, h); !errors.Is(err, ErrNotLAN) {
			t.Errorf("%s: %v, want ErrNotLAN", h, err)
		}
	}
}

// New refuses an IP-literal host off the LAN; the default transport refuses to dial one (a name that resolves
// there), before any byte, and so before the key, is sent.
func TestOnlyLANHosts(t *testing.T) {
	for _, u := range []string{"http://8.8.8.8:7878", "https://[2001:db8::1]", "http://203.0.113.9/radarr"} {
		if _, err := New(u, "SECRETKEY", Options{}); !errors.Is(err, ErrNotLAN) {
			t.Errorf("%s: %v, want ErrNotLAN", u, err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"appName":"Radarr","version":"6"}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "SECRETKEY", Options{Retries: -1})
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := c.GetJSON(context.Background(), "system/status", nil, &st); err != nil || st["appName"] != "Radarr" {
		t.Errorf("loopback through the default transport: %v", err)
	}
	// The dialer itself: a public address is refused at connect time, whatever name led there.
	if err := lanControl("tcp", "8.8.8.8:80", nil); !errors.Is(err, ErrNotLAN) {
		t.Errorf("dial 8.8.8.8: %v", err)
	}
	if err := lanControl("tcp6", "[2001:db8::1]:443", nil); !errors.Is(err, ErrNotLAN) {
		t.Errorf("dial 2001:db8::1: %v", err)
	}
	if err := lanControl("tcp", "10.1.2.3:7878", nil); err != nil {
		t.Errorf("dial 10.1.2.3: %v", err)
	}
	if tr := lanTransport(); tr.Proxy != nil || tr.DialContext == nil {
		t.Error("the transport uses a proxy or the default dialer")
	}
	if !strings.Contains(ErrNotLAN.Error(), "RFC 1918") {
		t.Error(ErrNotLAN)
	}
}
