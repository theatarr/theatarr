package plexauth_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/plexauth"
	"github.com/theatarr/theatarr/internal/plexauth/plexauthtest"
)

const server = "machine-ours"

func newService(t *testing.T, f *plexauthtest.Fake, now func() time.Time) *plexauth.Service {
	t.Helper()
	s, err := plexauth.New(plexauth.Config{Doer: f, BaseURL: "https://plex.tv", Product: "theatarr", Now: now,
		ClientID: func(context.Context) (string, error) { return "theatarr-test-client", nil },
		ServerID: func(context.Context) (string, error) { return server, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSignInWithAccess(t *testing.T) {
	ctx := context.Background()
	f := plexauthtest.New()
	s := newService(t, f, nil)
	st, err := s.Start(ctx, "192.0.2.1", "http://theatarr.lan/login/plex/done")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(st.AuthURL)
	if err != nil || u.Host != "app.plex.tv" {
		t.Fatalf("auth url %q", st.AuthURL)
	}
	frag, _ := url.ParseQuery(strings.TrimPrefix(u.Fragment, "?"))
	if frag.Get("clientID") != "theatarr-test-client" || frag.Get("code") == "" || frag.Get("forwardUrl") != "http://theatarr.lan/login/plex/done" {
		t.Errorf("auth url fragment %v", frag)
	}
	// Not confirmed yet.
	if a, err := s.Poll(ctx, st.ID); a != nil || err != nil {
		t.Fatalf("poll before confirm: %v %v", a, err)
	}
	if err := f.Confirm(frag.Get("code"), plexauthtest.Account{ID: 7, Username: "viewer", Title: "Viewer",
		Servers: []string{"someone-else", server}}); err != nil {
		t.Fatal(err)
	}
	a, err := s.Poll(ctx, st.ID)
	if err != nil || a == nil || a.ID != 7 || a.Username != "viewer" || a.Title != "Viewer" {
		t.Fatalf("poll after confirm: %+v %v", a, err)
	}
	// One use: the handle is gone.
	if _, err := s.Poll(ctx, st.ID); !errors.Is(err, plexauth.ErrUnknownPin) {
		t.Errorf("second poll: %v", err)
	}
	if len(f.Leaks) > 0 {
		t.Errorf("token in a URL: %v", f.Leaks)
	}
	if f.Calls["GET /api/v2/user"] != 1 || f.Calls["GET /api/v2/resources"] != 1 {
		t.Errorf("calls %v", f.Calls)
	}
}

func TestSignInWithoutAccess(t *testing.T) {
	ctx := context.Background()
	f := plexauthtest.New()
	s := newService(t, f, nil)
	st, err := s.Start(ctx, "192.0.2.1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.ConfirmLatest(plexauthtest.Account{ID: 8, Username: "stranger", Servers: []string{"another"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Poll(ctx, st.ID); !errors.Is(err, plexauth.ErrNoAccess) {
		t.Errorf("poll: %v, want ErrNoAccess", err)
	}
}

func TestPinsExpireAndAreLimited(t *testing.T) {
	ctx := context.Background()
	f := plexauthtest.New()
	now := time.Date(2026, 10, 26, 12, 30, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	s := newService(t, f, clock)
	st, err := s.Start(ctx, "192.0.2.1", "")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(31 * time.Minute)
	if _, err := s.Poll(ctx, st.ID); !errors.Is(err, plexauth.ErrUnknownPin) {
		t.Errorf("expired pin: %v", err)
	}
	if _, err := s.Poll(ctx, "made-up"); !errors.Is(err, plexauth.ErrUnknownPin) {
		t.Errorf("unknown handle: %v", err)
	}
	for i := range 20 {
		_, err := s.Start(ctx, "192.0.2.9", "")
		if i < 10 && err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		if i >= 10 && !errors.Is(err, plexauth.ErrTooMany) {
			t.Fatalf("start %d: %v, want ErrTooMany", i, err)
		}
	}
	// Another address is not held up.
	if _, err := s.Start(ctx, "192.0.2.10", ""); err != nil {
		t.Errorf("other address: %v", err)
	}
}
