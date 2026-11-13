package arr

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "s3cr3t-KEY+/=value"

func fastOpts() Options {
	return Options{Timeout: 2 * time.Second, MinInterval: time.Millisecond, Retries: 2, Backoff: time.Millisecond}
}

func TestGetSendsKeyHeaderAndQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method %s", r.Method)
		}
		if r.URL.Path != "/base/api/v3/movie" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("X-Api-Key") != testKey {
			t.Errorf("key header missing")
		}
		if strings.Contains(r.URL.RawQuery, "s3cr3t") {
			t.Errorf("key in query")
		}
		if got := r.URL.Query().Get("title"); got != "A.Movie.2020.1080p&x=1" {
			t.Errorf("title %q", got)
		}
		w.Write([]byte(`{"title":"ok"}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL+"/base/", testKey, fastOpts())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Get(context.Background(), "movie", url.Values{"title": {"A.Movie.2020.1080p&x=1"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 || string(resp.Body) != `{"title":"ok"}` {
		t.Fatalf("got %d %s", resp.Status, resp.Body)
	}
}

// The exported API has no way to choose a method: no exported func or method takes a method string or a request.
func TestNoMethodParameterInAPI(t *testing.T) {
	typ := reflect.TypeOf(&Client{})
	for i := 0; i < typ.NumMethod(); i++ {
		m := typ.Method(i)
		for j := 1; j < m.Type.NumIn(); j++ {
			in := m.Type.In(j)
			if in == reflect.TypeOf(&http.Request{}) || in == reflect.TypeOf(http.Request{}) {
				t.Errorf("Client.%s takes an *http.Request", m.Name)
			}
		}
		if strings.Contains(strings.ToLower(m.Name), "post") || strings.Contains(strings.ToLower(m.Name), "put") ||
			strings.Contains(strings.ToLower(m.Name), "delete") || m.Name == "Do" {
			t.Errorf("Client has method %s", m.Name)
		}
	}
}

// Even if a non-GET request reached the client's transport, the guard refuses it before the network.
func TestNonGETIsImpossible(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	c, err := New(srv.URL, testKey, fastOpts())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodHead} {
		req, _ := http.NewRequest(m, srv.URL+"/api/v3/movie", strings.NewReader("{}"))
		_, err := c.hc.Do(req)
		if !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%s: err = %v, want ErrNotAllowed", m, err)
		}
	}
	// A GET with a body is refused too.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v3/movie", strings.NewReader("{}"))
	if _, err := c.hc.Do(req); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("GET with body: %v", err)
	}
	// Endpoints off the allowlist (command, release, a movie by id) are refused at both layers.
	for _, ep := range []string{"command", "release", "movie/1", "movie/lookup", "../api/v3/command", "movie/../command", "history", "parse", "tag"} {
		if _, err := c.Get(context.Background(), ep, nil); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("Get(%q): %v", ep, err)
		}
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v3/"+ep, nil)
		if _, err := c.hc.Do(req); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("transport %q: %v", ep, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("server saw %d requests", hits.Load())
	}
}

func TestRedirectRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/api/v3/command", http.StatusFound)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, testKey, fastOpts())
	if _, err := c.Get(context.Background(), "system/status", nil); err == nil {
		t.Fatal("redirect followed")
	}
}

func TestKeyNeverInErrors(t *testing.T) {
	// The server echoes the key in its error body and status; every error path must mask it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k := r.Header.Get("X-Api-Key")
		switch r.URL.Path {
		case "/api/v3/system/status":
			http.Error(w, "bad key "+k+" "+url.QueryEscape(k), http.StatusInternalServerError)
		case "/api/v3/rootfolder":
			http.Error(w, "unauthorised "+k, http.StatusUnauthorized)
		case "/api/v3/qualityprofile":
			w.Write([]byte("not json " + k))
		}
	}))
	defer srv.Close()
	c, _ := New(srv.URL, testKey, fastOpts())
	ctx := context.Background()
	var v any
	errs := []error{
		c.GetJSON(ctx, "system/status", nil, &v),
		c.GetJSON(ctx, "rootfolder", nil, &v),
		c.GetJSON(ctx, "qualityprofile", nil, &v),
	}
	// Network error: closed server.
	dead, _ := New("http://127.0.0.1:1", testKey, fastOpts())
	errs = append(errs, dead.GetJSON(ctx, "system/status", nil, &v))
	for i, err := range errs {
		if err == nil {
			t.Errorf("case %d: no error", i)
			continue
		}
		s := err.Error()
		if strings.Contains(s, "s3cr3t") || strings.Contains(s, url.QueryEscape(testKey)) {
			t.Errorf("case %d: key in error: %s", i, s)
		}
	}
}

func TestRetriesOn5xxThenSucceeds(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, testKey, fastOpts())
	resp, err := c.Get(context.Background(), "system/status", nil)
	if err != nil || resp.Status != 200 {
		t.Fatalf("resp %v err %v", resp, err)
	}
	if n.Load() != 3 {
		t.Fatalf("attempts %d", n.Load())
	}
}

func TestNoRetryOn4xx(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, testKey, fastOpts())
	resp, err := c.Get(context.Background(), "movie", nil)
	if err != nil || resp.Status != 400 || n.Load() != 1 {
		t.Fatalf("resp %v err %v attempts %d", resp, err, n.Load())
	}
}

func TestRetriesOnTimeout(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			time.Sleep(300 * time.Millisecond)
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	o := fastOpts()
	o.Timeout = 100 * time.Millisecond
	c, _ := New(srv.URL, testKey, o)
	if _, err := c.Get(context.Background(), "system/status", nil); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 2 {
		t.Fatalf("attempts %d", n.Load())
	}
}

func TestGivesUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, testKey, fastOpts())
	_, err := c.Get(context.Background(), "system/status", nil)
	var se *StatusError
	if !errors.As(err, &se) && !strings.Contains(err.Error(), "503") {
		t.Fatalf("err %v", err)
	}
}

func TestRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) }))
	defer srv.Close()
	o := fastOpts()
	o.MinInterval = 50 * time.Millisecond
	c, _ := New(srv.URL, testKey, o)
	start := time.Now()
	for i := 0; i < 5; i++ {
		if _, err := c.Get(context.Background(), "system/status", nil); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d < 200*time.Millisecond {
		t.Fatalf("5 requests in %v, want >= 200ms", d)
	}
}

func TestContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	o := fastOpts()
	o.Backoff = time.Hour
	c, _ := New(srv.URL, testKey, o)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Get(ctx, "system/status", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
}

func TestNewRejectsBadInput(t *testing.T) {
	for _, u := range []string{"", "ftp://x", "http://", "http://h/?apikey=1", "http://u:p@h"} {
		if _, err := New(u, testKey, Options{}); err == nil {
			t.Errorf("New(%q) accepted", u)
		}
	}
	if _, err := New("http://h", "", Options{}); err == nil {
		t.Error("empty key accepted")
	}
}

// A Prowlarr client reads /api/v1/indexer and nothing else: not the v3 endpoints, not Prowlarr's search or commands.
func TestProwlarrClientReadsIndexersOnly(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.Header.Get("X-Api-Key") != testKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`[{"id":1,"definitionName":"thepiratebay"}]`))
	}))
	defer srv.Close()
	o := fastOpts()
	o.Prowlarr = true
	c, err := New(srv.URL+"/prowlarr", testKey, o)
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := c.GetJSON(context.Background(), "indexer", nil, &got); err != nil || len(got) != 1 {
		t.Fatalf("indexer: %v %v", got, err)
	}
	for _, ep := range []string{"search", "command", "indexer/1/newznab", "movie", "system/status", "applications"} {
		if _, err := c.Get(context.Background(), ep, nil); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("Get(%q): %v", ep, err)
		}
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/prowlarr/api/v1/"+ep, nil)
		if _, err := c.hc.Do(req); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("transport %q: %v", ep, err)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/prowlarr/api/v3/indexer", nil)
	if _, err := c.hc.Do(req); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("v3 path: %v", err)
	}
	if len(paths) != 1 || paths[0] != "GET /prowlarr/api/v1/indexer" {
		t.Fatalf("server saw %v", paths)
	}
}
