package web

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/plexauth"
	"github.com/theatarr/theatarr/internal/plexauth/plexauthtest"
)

// A new install nobody has opened: the status says it needs its first account, the create endpoint makes an admin
// and signs it in (Setup's account step is done), and afterwards the endpoint refuses for good. The password is
// never logged.
func TestFirstAccountCreate(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	e := newEnvAccount(t, func(c *Config) {
		c.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}, false)
	var st FirstAccountStatus
	if code := e.jsonCall("GET", APIBase+"/auth/first-account", nil, nil, "", &st); code != 200 || !st.NeedsAccount ||
		st.PasswordPending {
		t.Fatalf("status: %d %+v", code, st)
	}
	// The status is the whole answer: nothing else rides on it.
	rec := e.get(APIBase+"/auth/first-account", nil)
	var raw map[string]any
	json.Unmarshal(rec.Body.Bytes(), &raw)
	if len(raw) != 2 {
		t.Errorf("status fields: %v", raw)
	}
	const pw = "the owner's password"
	// Nothing but username and password: a role (or anything else) is refused.
	if code := e.jsonCall("POST", APIBase+"/auth/first-account", map[string]any{"username": "admin", "password": pw,
		"role": "user"}, nil, "", nil); code != 400 {
		t.Errorf("extra field: %d", code)
	}
	var bad apiError
	if code := e.jsonCall("POST", APIBase+"/auth/first-account", map[string]any{"username": "admin",
		"password": "short"}, nil, "", &bad); code != 400 || !strings.Contains(bad.Error, "12") {
		t.Errorf("short password: %d %+v", code, bad)
	}
	// A form post or a foreign origin is refused like the JSON login.
	r := httptest.NewRequest("POST", APIBase+"/auth/first-account", strings.NewReader(`{"username":"admin","password":"`+pw+`"}`))
	r.Header.Set("Content-Type", "text/plain")
	if rec := e.do(r); rec.Code != 403 {
		t.Errorf("form post: %d", rec.Code)
	}
	r = httptest.NewRequest("POST", APIBase+"/auth/first-account", strings.NewReader(`{"username":"admin","password":"`+pw+`"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://evil.example")
	if rec := e.do(r); rec.Code != 403 {
		t.Errorf("foreign origin: %d", rec.Code)
	}

	var out plexPollOut // any JSON with a session cookie
	if code := e.jsonCall("POST", APIBase+"/auth/first-account", map[string]any{"username": "admin", "password": pw},
		nil, "", &out); code != 200 || out.CSRFToken == "" || out.cookie == nil {
		t.Fatalf("create: %d %+v", code, out)
	}
	var me struct {
		Username    string   `json:"username"`
		Kind        string   `json:"kind"`
		Permissions []string `json:"permissions"`
	}
	if rec := e.get(APIBase+"/auth/me", out.cookie); rec.Code != 200 {
		t.Fatalf("me: %d", rec.Code)
	} else if json.Unmarshal(rec.Body.Bytes(), &me); me.Username != "admin" || me.Kind != "session" ||
		!strings.Contains(strings.Join(me.Permissions, ","), "admin") {
		t.Fatalf("me: %s", rec.Body.String())
	}
	var setup SetupState
	if rec := e.get(APIBase+"/setup", out.cookie); rec.Code != 200 {
		t.Fatalf("setup: %d", rec.Code)
	} else if json.Unmarshal(rec.Body.Bytes(), &setup); setup.AdminPasswordGenerated {
		t.Error("Setup asks to replace a generated password")
	}
	if code := e.jsonCall("POST", APIBase+"/auth/first-account", map[string]any{"username": "second",
		"password": "another long password"}, nil, "", nil); code != 409 {
		t.Errorf("second create: %d", code)
	}
	if code := e.jsonCall("GET", APIBase+"/auth/first-account", nil, nil, "", &st); code != 200 || st.NeedsAccount ||
		st.PasswordPending {
		t.Errorf("status after create: %+v", st)
	}
	if strings.Contains(logs.String(), pw) {
		t.Errorf("the log holds the password: %s", logs.String())
	}
}

// An install with an account: the create endpoint refuses and the status says only that no account is needed.
func TestFirstAccountRefusedWithAdmin(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var st FirstAccountStatus
	if code := e.jsonCall("GET", APIBase+"/auth/first-account", nil, nil, "", &st); code != 200 || st.NeedsAccount ||
		st.PasswordPending {
		t.Fatalf("status: %d %+v", code, st)
	}
	if code := e.jsonCall("POST", APIBase+"/auth/first-account", map[string]any{"username": "admin",
		"password": "a takeover password"}, nil, "", nil); code != 409 {
		t.Errorf("create: %d", code)
	}
	e.login() // the admin's password is unchanged
}

// While unclaimed, every route that is not public still refuses a request without credentials: an empty user table
// opens nothing but the status and the create endpoint (and the public set TestEveryRouteNeedsAuth pins).
func TestUnclaimedOpensNothingElse(t *testing.T) {
	t.Parallel()
	e := newEnvAccount(t, nil, false)
	for _, rt := range e.web.Routes() {
		if rt.Access == Public {
			continue
		}
		method, path := concrete(rt.Pattern)
		rec := e.do(httptest.NewRequest(method, path, nil))
		switch {
		case rt.API && rec.Code != http.StatusUnauthorized:
			t.Errorf("%s %s: %d, want 401", method, path, rec.Code)
		case !rt.API && (rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login")):
			t.Errorf("%s %s: %d, want a redirect to /login", method, path, rec.Code)
		}
	}
}

// Plex sign-in cannot make the first account while the table is empty.
func TestPlexSignInBeforeFirstAccount(t *testing.T) {
	t.Parallel()
	fake := plexauthtest.New()
	svc, err := plexauth.New(plexauth.Config{Doer: fake, BaseURL: "https://plex.tv",
		ClientID: func(context.Context) (string, error) { return "theatarr-test", nil },
		ServerID: func(context.Context) (string, error) { return "our-machine", nil }})
	if err != nil {
		t.Fatal(err)
	}
	e := newEnvAccount(t, func(c *Config) { c.Plex = svc }, false)
	var started plexauth.Started
	if code := e.jsonCall("POST", APIBase+"/auth/plex/start", map[string]any{}, nil, "", &started); code != 200 {
		t.Fatalf("start: %d", code)
	}
	if err := fake.ConfirmLatest(plexauthtest.Account{ID: 41000009, Username: "early", Servers: []string{"our-machine"}}); err != nil {
		t.Fatal(err)
	}
	var p plexPollOut
	if code := e.jsonCall("POST", APIBase+"/auth/plex/poll", map[string]any{"id": started.ID}, nil, "", &p); code != 409 ||
		p.cookie != nil || p.Status != "error" {
		t.Fatalf("poll: %d %+v", code, p)
	}
	if need, err := e.auth.NeedsFirstAccount(context.Background()); !need || err != nil {
		t.Fatalf("plex made an account: %v", err)
	}
}
