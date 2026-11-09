package cardigann

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// The engine's sessions outlive settings generations, keyed by definition, address and settings, but
// not by the indexer's hosts. An admin who adds the API host the error asks for (the documented TPB setup: apibay.org
// in hosts) keeps getting "not one of the indexer's hosts" until Theatarr restarts.
func TestAddedHostReachesTheRunningSession(t *testing.T) {
	t.Parallel()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"title":"Other.Host.Movie.2020.1080p.BluRay","url":"magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01","size":1073741824,"seeders":5,"leechers":1}]}`)
	}))
	defer api.Close()
	site := httptest.NewServer(http.NotFoundHandler())
	defer site.Close()
	apiURL, _ := url.Parse(api.URL)
	m := apiHostManager(t, "http://localhost:"+apiURL.Port())

	// Generation 1: the API host is missing; the search is refused (as it should be).
	inst := Instance{Key: "tpb", Transport: direct, DefinitionID: "apihost", BaseURL: site.URL}
	if _, err := m.Search(context.Background(), inst, "movie", nil); err == nil {
		t.Fatal("generation 1 searched a host that is not configured")
	}
	// Generation 2: the admin adds the host, nothing else changes. The shared manager keeps the session.
	inst.Hosts = []string{"localhost"}
	res, err := m.Search(context.Background(), inst, "movie", nil)
	if err != nil || len(res) != 1 {
		t.Fatalf("after adding the host: %v (%d results); the running session still has the old hosts", err, len(res))
	}
}

func TestSessionKeyTreatsHostsAsASet(t *testing.T) {
	t.Parallel()
	a := Instance{Hosts: []string{"API.EXAMPLE.", "mirror.example:443"}}
	b := Instance{Hosts: []string{"mirror.example", "api.example", "api.example"}}
	if sessionKey("definition", a) != sessionKey("definition", b) {
		t.Fatal("host order/case/duplicates changed the session key")
	}
	b.Hosts = []string{"mirror.example"}
	if sessionKey("definition", a) == sessionKey("definition", b) {
		t.Fatal("host removal kept the old session key")
	}
}
