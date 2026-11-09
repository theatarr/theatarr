package plex

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/gateway"
)

type fakeDoer struct {
	req *http.Request
	t   gateway.Target
}

func (f *fakeDoer) Do(_ context.Context, t gateway.Target, req *http.Request) (*http.Response, error) {
	f.req, f.t = req, t
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
}

func TestScan(t *testing.T) {
	d := &fakeDoer{}
	c, err := New(d, "http://plex:32400/", "secret-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Scan(context.Background(), 18, "/media/movies/A (2001)"); err != nil {
		t.Fatal(err)
	}
	u := d.req.URL
	if d.req.Method != http.MethodGet || u.Path != "/library/sections/18/refresh" ||
		u.Query().Get("path") != "/media/movies/A (2001)" || d.t != Target {
		t.Errorf("request %s %s target %v", d.req.Method, u, d.t)
	}
	if strings.Contains(u.String(), "secret") || d.req.Header.Get("X-Plex-Token") != "secret-token" {
		t.Errorf("token handling: url %s", u)
	}
	if _, err := New(d, "http://x", ""); err == nil {
		t.Error("no token accepted")
	}
	if err := c.Scan(context.Background(), 0, "/x"); err == nil {
		t.Error("section 0 accepted")
	}
}
