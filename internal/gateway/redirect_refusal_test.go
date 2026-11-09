package gateway

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type extraRedirectTransport struct {
	requests              int
	redirectedHasResponse bool
}

func (x *extraRedirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	x.requests++
	if x.requests == 1 {
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"http://second.test/path"}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	}
	x.redirectedHasResponse = req.Response != nil
	return nil, &Refused{Reason: ReasonVPN, Detail: "VPN proof revoked while dialing redirected hop"}
}
func TestRedirectDialRefusalCountsOriginal(t *testing.T) {
	raw := &extraRedirectTransport{}
	g := &Gateway{allow: allowlist{"first.test": true, "second.test": true}}
	client := &http.Client{Transport: &guard{base: raw, g: g}}
	_, err := client.Get("http://first.test/")
	var r *Refused
	if !errors.As(err, &r) {
		t.Fatalf("wanted refusal, got %v", err)
	}
	if !raw.redirectedHasResponse {
		t.Fatal("fixture did not establish redirect")
	}
	if !r.Sent {
		t.Fatalf("original request produced redirect, but transport's refusal has Sent=false after %d RoundTrips", raw.requests)
	}
}
