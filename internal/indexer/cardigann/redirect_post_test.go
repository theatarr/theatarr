package cardigann

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type postRedirectRT struct{ to string }

func (p postRedirectRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/takelogin.php" && r.URL.Host != p.to {
		return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": {"http://" + p.to + "/takelogin.php"}},
			Body: io.NopCloser(http.NoBody), Request: r}, nil
	}
	return direct.RoundTrip(r)
}

// A login form answered with a 307 to another host must not have its body (the password) sent there again.
func TestLoginPostNotResentToAnotherHost(t *testing.T) {
	t.Parallel()
	site := newPrivateSite(t)
	var got atomic.Value
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.Store(r.Method + " " + string(b))
	}))
	defer other.Close()
	inst := Instance{Key: "ix7", Transport: postRedirectRT{to: other.Listener.Addr().String()}, Name: "Fixture Private",
		DefinitionID: "fixtureprivate", BaseURL: site.URL + "/", Settings: map[string]string{"username": "alice", "password": "s3cret"}}
	_, err := fixtureManager(t, "fixtureprivate").Search(context.Background(), inst, "private movie", []int{2000})
	if v, _ := got.Load().(string); v != "" {
		t.Fatalf("the login form went to the other host: %q (err %v)", v, err)
	}
	if err == nil {
		t.Fatal("a login redirected to another host should fail")
	}
}
