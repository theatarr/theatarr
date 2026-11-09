// Port of Mediarium internal/indexers/cardigann_otherhost_test.go @ f7d1e26 (github.com/rdborg/Mediarium, AGPL-3.0,
// Copyright (C) Ryan Borg and the Mediarium contributors). Changes are listed in the package doc.

package cardigann

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// A public site with no sign-in may run its search on another public host
// (the movie site whose API lives on a separate name). A site that sends a
// login, cookie or key keeps to its own addresses.
const apiHostDefinition = `---
id: apihost
name: Api Host
description: "A public site whose search runs on a different host"
language: en-US
type: public
encoding: UTF-8
links:
  - https://apihost.fixture.test/

caps:
  categorymappings:
    - {id: 1, cat: Movies, desc: "Movies"}
  modes:
    search: [q]

settings:
  - name: apikey
    type: text
    label: API key

search:
  paths:
    - path: APIHOST/list.json
      response:
        type: json
  inputs:
    query_term: "{{ .Keywords }}"
  rows:
    selector: data
  fields:
    title:
      selector: title
    category:
      text: 1
    details:
      text: "{{ .Config.sitelink }}movie"
    download:
      selector: url
    size:
      selector: size
    seeders:
      selector: seeders
    leechers:
      selector: leechers
    downloadvolumefactor:
      text: 0
    uploadvolumefactor:
      text: 1
`

func apiHostManager(t *testing.T, apiAddress string) *Manager {
	t.Helper()
	def := strings.ReplaceAll(apiHostDefinition, "APIHOST", apiAddress)
	m := NewManager(memDefs{"apihost": []byte(def)}, nil)
	m.DefaultDelay = 0
	return m
}

func TestSiteWithoutCredentialsMayUseAnotherHost(t *testing.T) {
	t.Parallel()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/list.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"title":"Other.Host.Movie.2020.1080p.BluRay","url":"magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01","size":1073741824,"seeders":5,"leechers":1}]}`)
	}))
	defer api.Close()
	// the site itself is on 127.0.0.1, the API is named localhost: two hosts
	site := httptest.NewServer(http.NotFoundHandler())
	defer site.Close()
	apiURL, _ := url.Parse(api.URL)
	apiAddress := "http://localhost:" + apiURL.Port()

	tests := []struct {
		name     string
		settings map[string]string
		hosts    []string
		want     string // "" = works; otherwise a part of the refusal
	}{
		{"no credentials", nil, []string{"localhost"}, ""},
		{"an empty key is no key", map[string]string{"apikey": "  "}, []string{"localhost"}, ""},
		{"the API host is not one of the indexer's hosts", nil, nil, "not one of the indexer's hosts"},
		{"an api key is filled in", map[string]string{"apikey": "secret-key"}, []string{"localhost"}, "not one of the site's own addresses"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := apiHostManager(t, apiAddress)
			inst := Instance{Key: fmt.Sprint("ix", i+1), Transport: direct, Name: "Api Host", DefinitionID: "apihost", BaseURL: site.URL, Settings: tt.settings, Hosts: tt.hosts}
			res, err := m.Search(context.Background(), inst, "movie", nil)
			if tt.want == "" {
				if err != nil || len(res) != 1 || res[0].Title != "Other.Host.Movie.2020.1080p.BluRay" {
					t.Fatalf("search: %v %+v", err, res)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("search = %v, want an error containing %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "secret-key") {
				t.Fatalf("the error shows the key: %v", err)
			}
		})
	}
}

func TestAddressRulesWithAndWithoutCredentials(t *testing.T) {
	t.Parallel()
	const strange = "not one of the site's own addresses"
	const unlisted = "not one of the indexer's hosts"
	withKey := map[string]string{"apikey": "secret-key"}
	tests := []struct {
		name     string
		def      Definition
		settings map[string]string
		path     string
		want     string // "" = allowed
	}{
		{"public site, other public host", Definition{}, nil, "https://movies-api.example.net/api/list", ""},
		{"public site, same domain", Definition{}, nil, "https://api.yts.example.org/list", ""},
		{"public site, a private address", Definition{}, nil, "http://192.168.1.5/api", unlisted},
		{"public site, the machine itself", Definition{}, nil, "http://127.0.0.1:8080/api", unlisted},
		{"public site, localhost", Definition{}, nil, "http://localhost/api", unlisted},
		{"public site, the metadata address", Definition{}, nil, "http://169.254.169.254/latest/meta-data/", unlisted},
		{"public site, not a web address", Definition{}, nil, "ftp://movies-api.example.net/x", "not a web address"},
		{"login block, other host", Definition{Login: &LoginBlock{}}, nil, "https://movies-api.example.net/api/list", strange},
		{"login block, own domain", Definition{Login: &LoginBlock{}}, nil, "https://api.yts.example.org/list", ""},
		{"login block, private address", Definition{Login: &LoginBlock{}}, nil, "http://192.168.1.5/api", strange},
		{"api key filled, other host", Definition{Settings: []SettingField{{Name: "apikey", Type: "text"}}}, withKey, "https://movies-api.example.net/api/list", strange},
		{"api key not filled, other host", Definition{Settings: []SettingField{{Name: "apikey", Type: "text"}}}, nil, "https://movies-api.example.net/api/list", ""},
		{"password filled, other host", Definition{Settings: []SettingField{{Name: "pw", Type: "password"}}}, map[string]string{"pw": "x"}, "https://movies-api.example.net/api/list", strange},
		{"cookie filled, other host", Definition{Settings: []SettingField{{Name: "cookie", Type: "text"}}}, map[string]string{"cookie": "a=b"}, "https://movies-api.example.net/api/list", strange},
		{"username filled, other host", Definition{Settings: []SettingField{{Name: "username", Type: "text"}}}, map[string]string{"username": "me"}, "https://movies-api.example.net/api/list", strange},
		{"api key filled, private address", Definition{Settings: []SettingField{{Name: "apikey", Type: "text"}}}, withKey, "http://192.168.1.5/api", strange},
		{"authorization header, other host", Definition{Search: SearchBlock{Headers: map[string][]string{"Authorization": {"Bearer x"}}}}, nil, "https://movies-api.example.net/api/list", strange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base, _ := url.Parse("https://www.yts.example.org/")
			def := tt.def
			s := &cgSession{name: "Fixture", base: base, def: &def, settings: tt.settings,
				hosts: map[string]bool{"www.yts.example.org": true, "movies-api.example.net": true}}
			_, err := s.resolvePath(tt.path)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("resolvePath(%q): %v", tt.path, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("resolvePath(%q) = %v, want an error containing %q", tt.path, err, tt.want)
			}
		})
	}
}

// YTS and The Pirate Bay save the API address the definition suggests with
// the form. That is no sign-in, so the site may still use that other host.
func TestSavedDefaultsAreNotCredentials(t *testing.T) {
	t.Parallel()
	def := &Definition{Settings: []SettingField{
		{Name: "apiurl", Type: "text", Default: "movies-api.accel.li"},
		{Name: "uploader", Type: "text"},
		{Name: "top100", Type: "select", Default: "recent"},
	}}
	tests := []struct {
		name     string
		settings map[string]string
		want     bool
	}{
		{"nothing saved", nil, false},
		{"the suggested address saved with the form", map[string]string{"apiurl": "movies-api.accel.li", "top100": "recent"}, false},
		{"an uploader filter", map[string]string{"uploader": "SomeUser"}, false},
		{"a different address typed in", map[string]string{"apiurl": "mirror.example.net"}, false},
	}
	for _, tt := range tests {
		s := &cgSession{def: def, settings: tt.settings}
		if got := s.sendsCredentials(); got != tt.want {
			t.Errorf("%s: sendsCredentials = %v, want %v", tt.name, got, tt.want)
		}
	}
	// A setting named like a sign-in is a credential even when it equals its default.
	login := &Definition{Settings: []SettingField{{Name: "username", Type: "text", Default: "guest"}}}
	if !(&cgSession{def: login, settings: map[string]string{"username": "guest"}}).sendsCredentials() {
		t.Error("a username equal to its default must still count as a credential")
	}
}
