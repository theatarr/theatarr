// Port of Mediarium internal/indexers/cardigann_foreign_test.go @ f7d1e26 (github.com/rdborg/Mediarium, AGPL-3.0,
// Copyright (C) Ryan Borg and the Mediarium contributors). Changes are listed in the package doc.

package cardigann

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func publicSiteSession(t *testing.T, def Definition, settings map[string]string) *cgSession {
	t.Helper()
	base, _ := url.Parse("https://www.yts.example.org/")
	return &cgSession{name: "Fixture", base: base, def: &def, settings: settings, hosts: map[string]bool{"www.yts.example.org": true}}
}

// Mediarium resolved names and refused those pointing inside; Theatarr reaches only the site and the indexer's
// configured hosts (and the gateway checks its allowlist again), so a name that resolves inside is never asked for.
// A link a page hands out that names the home network is still refused when the site is on the internet.
func TestPageLinksToTheHomeNetworkAreRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		host string
		ok   bool
	}{
		{"files.mirror.example.net", true},
		{"192.168.1.5", false},
		{"127.0.0.1:8080", false},
		{"169.254.169.254", false},
		{"[::1]", false},
		{"100.64.1.1", false},
		{"router", false},
		{"nas.local", false},
	}
	for _, tt := range tests {
		s := publicSiteSession(t, Definition{}, nil)
		err := s.checkPageAddress("https://" + tt.host + "/dl/1")
		if tt.ok != (err == nil) {
			t.Errorf("%s: checkPageAddress = %v, want ok=%v", tt.host, err, tt.ok)
		}
		if err != nil && !strings.Contains(err.Error(), "own network") {
			t.Errorf("%s: the refusal should say why: %v", tt.host, err)
		}
	}
}

// A request for a host that is neither the site's nor one of the indexer's hosts never reaches the transport, and
// neither does a redirect to one.
func TestRequestToAnUnlistedHostIsNeverSent(t *testing.T) {
	t.Parallel()
	sent := 0
	ok := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sent++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
	})
	def := &Definition{ID: "x", Name: "X", Links: []string{"https://www.example.org/"}}
	m := NewManager(nil, nil)
	s, err := newSession(m, def, Instance{Name: "X", BaseURL: "https://www.example.org/", Hosts: []string{"files.mirror.example.net"}, Transport: ok})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.doOnce(context.Background(), cgRequest{url: "https://rebind.attacker.example/x"}); err == nil || !strings.Contains(err.Error(), "not one of the indexer's hosts") {
		t.Fatalf("got %v, want the request refused", err)
	}
	if sent != 0 {
		t.Fatalf("the request was sent %d times", sent)
	}
	for _, u := range []string{"https://www.example.org/search", "https://files.mirror.example.net/a.torrent"} {
		if _, err := s.doOnce(context.Background(), cgRequest{url: u}); err != nil {
			t.Fatalf("%s: %v", u, err)
		}
	}
	if sent != 2 {
		t.Fatalf("sent = %d, want 2", sent)
	}
	redirect := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sent++
		h := http.Header{"Location": []string{"https://rebind.attacker.example/in"}}
		return &http.Response{StatusCode: http.StatusFound, Header: h, Body: http.NoBody, Request: r}, nil
	})
	s, _ = newSession(m, def, Instance{Name: "X", BaseURL: "https://www.example.org/", Transport: redirect})
	sent = 0
	if _, err := s.doOnce(context.Background(), cgRequest{url: "https://www.example.org/go"}); err == nil || !strings.Contains(err.Error(), "not one of the indexer's hosts") {
		t.Fatalf("got %v, want the redirect refused", err)
	}
	if sent != 1 {
		t.Fatalf("sent = %d, want only the first request", sent)
	}
}

// The site the person set up on their own network keeps working.
func TestSiteOnTheHomeNetworkStillWorks(t *testing.T) {
	t.Parallel()
	base, _ := url.Parse("http://192.168.1.10:9117/")
	s := &cgSession{name: "Fixture", base: base, def: &Definition{}}
	if _, err := s.resolvePath("api/v2.0/search"); err != nil {
		t.Fatalf("own path: %v", err)
	}
	if err := s.checkPageAddress("http://192.168.1.10:9117/dl/1"); err != nil {
		t.Fatalf("own link: %v", err)
	}
	tr := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
	})
	sess, err := newSession(NewManager(nil, nil), &Definition{ID: "x", Name: "X"}, Instance{Name: "X", BaseURL: "http://192.168.1.10:9117/", Transport: tr})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.doOnce(context.Background(), cgRequest{url: "http://192.168.1.10:9117/api"}); err != nil {
		t.Fatalf("a request to the site's own address: %v", err)
	}
}

func TestSendsCredentialsRecognisesUnusualSettingsAndHeaders(t *testing.T) {
	t.Parallel()
	text := func(name string) Definition { return Definition{Settings: []SettingField{{Name: name, Type: "text"}}} }
	sel := Definition{Settings: []SettingField{{Name: "sessionid", Type: "select"}, {Name: "freeleech", Type: "checkbox"}}}
	header := func(k string) Definition {
		return Definition{Search: SearchBlock{Headers: map[string][]string{k: {"x"}}}}
	}
	tests := []struct {
		name     string
		def      Definition
		settings map[string]string
		want     bool
	}{
		{"a session id", text("sessionid"), map[string]string{"sessionid": "abc"}, true},
		{"sid", text("sid"), map[string]string{"sid": "abc"}, true},
		{"auth", text("auth"), map[string]string{"auth": "abc"}, true},
		{"hash", text("hash"), map[string]string{"hash": "abc"}, true},
		{"pwd", text("pwd"), map[string]string{"pwd": "abc"}, true},
		{"rss key", text("rss_key"), map[string]string{"rss_key": "abc"}, true},
		{"bearer", text("bearer"), map[string]string{"bearer": "abc"}, true},
		{"a text setting left empty", text("sessionid"), map[string]string{"sessionid": " "}, false},
		{"user agent", text("useragent"), map[string]string{"useragent": "Mozilla/5.0"}, false},
		{"language", text("language"), map[string]string{"language": "en"}, false},
		{"sort", text("sort"), map[string]string{"sort": "seeders"}, false},
		{"a choice", sel, map[string]string{"sessionid": "a", "freeleech": "true"}, false},
		{"x-auth-token header", header("X-Auth-Token"), nil, true},
		{"api-key header", header("Api-Key"), nil, true},
		{"x-session header", header("X-Session"), nil, true},
		{"cookie header", header("Cookie"), nil, true},
		{"accept-language header", header("Accept-Language"), nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := publicSiteSession(t, tt.def, tt.settings)
			if got := s.sendsCredentials(); got != tt.want {
				t.Fatalf("sendsCredentials = %v, want %v", got, tt.want)
			}
		})
	}
	// and so it refuses another host
	s := publicSiteSession(t, text("sessionid"), map[string]string{"sessionid": "abc"})
	if _, err := s.resolvePath("https://movies-api.example.net/x"); err == nil || !strings.Contains(err.Error(), "not one of the site's own addresses") {
		t.Fatalf("got %v, want the other host refused", err)
	}
}
