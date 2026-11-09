package cardigann

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type sessionRoundTripFunc func(*http.Request) (*http.Response, error)

func (f sessionRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestQueuedGenerationCannotReplaceActiveTransports(t *testing.T) {
	t.Parallel()
	m := NewManager(memDefs{"two": []byte(`id: two
name: Two
links: [https://site.example/]
search:
  paths:
    - path: first
    - path: second
  rows:
    selector: tr.result
  fields:
    title: {selector: a}
    download: {selector: a, attribute: href}
`)}, nil)
	m.DefaultDelay = 0
	entered, release := make(chan struct{}), make(chan struct{})
	var aCalls, bCalls atomic.Int32
	response := func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("<html></html>")), Request: r}, nil
	}
	a := sessionRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if aCalls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return response(r)
	})
	b := sessionRoundTripFunc(func(r *http.Request) (*http.Response, error) { bCalls.Add(1); return response(r) })
	inst := Instance{Key: "same", DefinitionID: "two", BaseURL: "https://site.example/", Transport: a}
	done := make(chan error, 1)
	go func() { _, err := m.Search(context.Background(), inst, "test", nil); done <- err }()
	<-entered
	inst.Transport = b
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := m.Search(ctx, inst, "test", nil)
	close(release)
	firstErr := <-done
	if !errors.Is(err, context.Canceled) || firstErr != nil {
		t.Fatalf("queued %v, active %v", err, firstErr)
	}
	if aCalls.Load() != 2 || bCalls.Load() != 0 {
		t.Fatalf("active flow crossed generations: A=%d B=%d", aCalls.Load(), bCalls.Load())
	}
}

func TestCleanMasksMixedCasePasswordType(t *testing.T) {
	t.Parallel()
	s := &cgSession{def: &Definition{Settings: []SettingField{{Name: "credential", Type: "Password"}}}, settings: map[string]string{"credential": "secret/value here"}}
	err := s.clean(errors.New("failed with secret%2Fvalue%20here"))
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("credential in error: %v", err)
	}
}

func TestSearchRedirectLimitIsAnError(t *testing.T) {
	t.Parallel()
	m := fixtureManager(t, "fixturepublic")
	inst := Instance{Key: "loop", DefinitionID: "fixturepublic", BaseURL: "https://site.example/", Transport: sessionRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"/loop"}}, Body: io.NopCloser(strings.NewReader("<html></html>")), Request: r}, nil
	})}
	if _, err := m.Search(context.Background(), inst, "test", nil); err == nil {
		t.Fatal("redirect loop reported as an empty successful search")
	}
}

func TestLoginMissingFormDoesNotQuoteURL(t *testing.T) {
	t.Parallel()
	m := fixtureManager(t, "fixtureprivate")
	inst := Instance{Key: "login", DefinitionID: "fixtureprivate", BaseURL: "https://site.example/private-address/", Transport: sessionRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("<html></html>")), Request: r}, nil
	})}
	_, err := m.Search(context.Background(), inst, "test", nil)
	if err == nil || strings.Contains(err.Error(), "private-address") {
		t.Fatalf("login error quotes its address: %v", err)
	}
}
