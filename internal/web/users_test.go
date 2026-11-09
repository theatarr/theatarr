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

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/plexauth"
	"github.com/theatarr/theatarr/internal/plexauth/plexauthtest"
)

func (e *env) jsonCall(method, path string, body any, c *http.Cookie, csrf string, out any) int {
	e.t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	if c != nil {
		r.AddCookie(c)
	}
	if csrf != "" {
		r.Header.Set(auth.CSRFHeader, csrf)
	}
	rec := e.do(r)
	if out != nil && rec.Code/100 == 2 || out != nil && rec.Code >= 400 && strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		json.Unmarshal(rec.Body.Bytes(), out)
	}
	if c == nil {
		if sc := findCookie(rec, auth.SessionCookie); sc != nil && out != nil {
			if m, ok := out.(*plexPollOut); ok {
				m.cookie = sc
			}
		}
	}
	return rec.Code
}

type plexPollOut struct {
	PlexPoll
	cookie *http.Cookie
}

// TestPlexSignIn: through the native API against a fake plex.tv. A first sign-in creates a pending user (the
// default); an admin enables it; the next sign-in gets a session with the user role. An account without access to
// our server is refused and gets no account. The Plex token is never stored or logged.
func TestPlexSignIn(t *testing.T) {
	t.Parallel()
	fake := plexauthtest.New()
	var logs bytes.Buffer
	svc, err := plexauth.New(plexauth.Config{Doer: fake, BaseURL: "https://plex.tv",
		ClientID: func(context.Context) (string, error) { return "theatarr-test", nil },
		ServerID: func(context.Context) (string, error) { return "our-machine", nil }})
	if err != nil {
		t.Fatal(err)
	}
	e := newEnvWith(t, func(c *Config) {
		c.Plex = svc
		c.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	ctx := context.Background()
	viewer := plexauthtest.Account{ID: 41000001, Username: "householdviewer", Title: "Household Viewer",
		Email: "viewer@example.invalid", Servers: []string{"our-machine"}}

	signIn := func(a plexauthtest.Account) (int, plexPollOut) {
		var st plexauth.Started
		if code := e.jsonCall("POST", APIBase+"/auth/plex/start", map[string]any{}, nil, "", &st); code != 200 {
			t.Fatalf("start: %d", code)
		}
		if !strings.HasPrefix(st.AuthURL, plexauth.AppAuthURL+"#?") || !strings.Contains(st.AuthURL, "forwardUrl=") {
			t.Errorf("auth url %s", st.AuthURL)
		}
		var p plexPollOut
		if code := e.jsonCall("POST", APIBase+"/auth/plex/poll", map[string]any{"id": st.ID}, nil, "", &p); code != 200 ||
			p.Status != "waiting" {
			t.Fatalf("poll before confirm: %d %+v", code, p)
		}
		if err := fake.ConfirmLatest(a); err != nil {
			t.Fatal(err)
		}
		p = plexPollOut{}
		code := e.jsonCall("POST", APIBase+"/auth/plex/poll", map[string]any{"id": st.ID}, nil, "", &p)
		return code, p
	}

	var info map[string]any
	e.jsonCall("GET", APIBase+"/auth/plex", nil, nil, "", &info)
	if info["enabled"] != true {
		t.Errorf("auth/plex: %v", info)
	}
	// A form post or a foreign origin is refused like the JSON login.
	r := httptest.NewRequest("POST", APIBase+"/auth/plex/start", strings.NewReader("{}"))
	r.Header.Set("Content-Type", "text/plain")
	if rec := e.do(r); rec.Code != 403 {
		t.Errorf("start without JSON: %d", rec.Code)
	}

	// First sign-in: pending, no session.
	code, p := signIn(viewer)
	if code != 200 || p.Status != "pending" || p.cookie != nil {
		t.Fatalf("first sign-in: %d %+v", code, p)
	}
	us, err := e.auth.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var u auth.User
	for _, x := range us {
		if x.PlexID != nil && *x.PlexID == viewer.ID {
			u = x
		}
	}
	if u.ID == 0 || u.Role != auth.RoleUser || u.Status != auth.StatusPending || u.Local || u.Username != "householdviewer" ||
		u.DisplayName != "Household Viewer" {
		t.Fatalf("new account: %+v", u)
	}
	// A pending account cannot sign in with a password either (it has none).
	if _, err := e.auth.Login(ctx, "householdviewer", "", "192.0.2.1", "t"); err == nil {
		t.Error("password login for a Plex account")
	}
	// The admin enables it; the next sign-in gets a session as a user.
	c := e.login()
	var me Me
	e.jsonCall("GET", APIBase+"/auth/me", nil, c, "", &me)
	var patched auth.User
	if code := e.jsonCall("PATCH", APIBase+"/users/"+itoa(u.ID), map[string]any{"status": "active"}, c, me.CSRFToken,
		&patched); code != 200 || patched.Status != auth.StatusActive {
		t.Fatalf("enable: %d %+v", code, patched)
	}
	code, p = signIn(viewer)
	if code != 200 || p.Status != "signed_in" || p.cookie == nil || p.CSRFToken == "" {
		t.Fatalf("second sign-in: %d %+v", code, p)
	}
	var vme Me
	e.jsonCall("GET", APIBase+"/auth/me", nil, p.cookie, "", &vme)
	if vme.Role != auth.RoleUser || !vme.Plex || vme.Local || strings.Join(vme.Permissions, ",") != "browse,request" {
		t.Errorf("signed-in me: %+v", vme)
	}
	// The user cannot reach the admin panel or the users screen.
	if code := e.jsonCall("GET", APIBase+"/jobs", nil, p.cookie, "", nil); code != 403 {
		t.Errorf("user on /jobs: %d", code)
	}
	if code := e.jsonCall("GET", APIBase+"/users", nil, p.cookie, "", nil); code != 403 {
		t.Errorf("user on /users: %d", code)
	}
	// A Plex account has no password to change.
	if code := e.jsonCall("POST", APIBase+"/me/password", map[string]any{"current": "x", "new": "long-enough-pass"},
		p.cookie, p.CSRFToken, nil); code != 409 {
		t.Errorf("plex password change: %d", code)
	}

	// No access to our server: refused, no account.
	code, p = signIn(plexauthtest.Account{ID: 99, Username: "stranger", Servers: []string{"elsewhere"}})
	if code != 403 || p.Status != "denied" || p.cookie != nil {
		t.Errorf("stranger: %d %+v", code, p)
	}
	if us, _ := e.auth.ListUsers(ctx); len(us) != 2 {
		t.Errorf("accounts after a refused sign-in: %d", len(us))
	}
	// Disabled: refused, sessions gone.
	dis := auth.StatusDisabled
	if err := e.auth.UpdateUser(ctx, u.ID, auth.UserPatch{Status: &dis}); err != nil {
		t.Fatal(err)
	}
	if code := e.jsonCall("GET", APIBase+"/auth/me", nil, p.cookie, "", nil); code != 401 {
		t.Errorf("disabled account's old session: %d", code)
	}
	code, p = signIn(viewer)
	if code != 403 || p.Status != "disabled" {
		t.Errorf("disabled sign-in: %d %+v", code, p)
	}
	// New Plex users allowed at once: a new account is active and signed in.
	if code := e.jsonCall("PUT", APIBase+"/users/settings", map[string]any{"plex_new_users": "active"}, c, me.CSRFToken,
		nil); code != 200 {
		t.Fatalf("settings: %d", code)
	}
	code, p = signIn(plexauthtest.Account{ID: 42, Username: "admin", Title: "Another", Servers: []string{"our-machine"}})
	if code != 200 || p.Status != "signed_in" {
		t.Fatalf("open sign-in: %d %+v", code, p)
	}
	e.jsonCall("GET", APIBase+"/auth/me", nil, p.cookie, "", &vme)
	if vme.Username != "admin-42" || vme.Role != auth.RoleUser {
		t.Errorf("name clash with the local admin: %+v", vme)
	}

	// Tokens: never in the database or the log.
	for _, table := range []string{"web_user", "web_setting", "web_session", "api_key"} {
		rows, err := e.st.Reader().Query(`SELECT * FROM ` + table)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			rows.Scan(ptrs...)
			for _, v := range vals {
				if s, ok := v.(string); ok && strings.Contains(s, "tok-") {
					t.Errorf("%s holds a Plex token", table)
				}
			}
		}
		rows.Close()
	}
	if strings.Contains(logs.String(), "tok-") {
		t.Error("the log holds a Plex token")
	}
	if len(fake.Leaks) > 0 {
		t.Errorf("token in a URL: %v", fake.Leaks)
	}
}

// TestUsersAdmin: roles, extra permissions, request versions, the last admin, and keys per user.
func TestUsersAdmin(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	c := e.login()
	var me Me
	e.jsonCall("GET", APIBase+"/auth/me", nil, c, "", &me)
	csrf := me.CSRFToken
	uid := e.account("ann", auth.RoleUser, auth.StatusActive, true)

	var page UsersPage
	if code := e.jsonCall("GET", APIBase+"/users", nil, c, "", &page); code != 200 || len(page.Items) != 2 ||
		page.Settings.PlexNewUsers != "pending" || page.Settings.PlexEnabled || len(page.Versions) == 0 {
		t.Fatalf("users: %d %+v", code, page)
	}
	var u auth.User
	code := e.jsonCall("PATCH", APIBase+"/users/"+itoa(uid), map[string]any{"extra_permissions": []string{"own_keys"},
		"auto_approve": true, "request_versions": []string{"movie-web"}}, c, csrf, &u)
	if code != 200 || !u.AutoApprove || strings.Join(u.RequestVersions, ",") != "movie-web" ||
		strings.Join(u.Permissions, ",") != "browse,request,auto_approve,own_keys" {
		t.Fatalf("patch: %d %+v", code, u)
	}
	if code := e.jsonCall("PATCH", APIBase+"/users/"+itoa(uid), map[string]any{"request_versions": nil}, c, csrf, &u); code != 200 ||
		u.RequestVersions != nil {
		t.Errorf("reset request versions: %d %+v", code, u)
	}
	// A Discord user id: digits, cleared with "".
	if code := e.jsonCall("PATCH", APIBase+"/users/"+itoa(uid), map[string]any{"discord_id": " 123456789012345678 "}, c, csrf, &u); code != 200 ||
		u.DiscordID != "123456789012345678" {
		t.Errorf("discord id: %d %+v", code, u)
	}
	if code := e.jsonCall("PATCH", APIBase+"/users/"+itoa(uid), map[string]any{"discord_id": ""}, c, csrf, &u); code != 200 ||
		u.DiscordID != "" {
		t.Errorf("clear discord id: %d %+v", code, u)
	}
	for _, bad := range []map[string]any{{"role": "owner"}, {"request_versions": []string{"nope"}},
		{"extra_permissions": []string{"root"}}, {"request_versions": []string{}}, {"discord_id": "@ann"},
		{"discord_id": "12345"}} {
		if code := e.jsonCall("PATCH", APIBase+"/users/"+itoa(uid), bad, c, csrf, nil); code != 400 {
			t.Errorf("patch %v: %d", bad, code)
		}
	}
	// The last admin keeps the role.
	if code := e.jsonCall("PATCH", APIBase+"/users/1", map[string]any{"role": "user"}, c, csrf, nil); code != 409 {
		t.Errorf("demote the last admin: %d", code)
	}
	if code := e.jsonCall("PATCH", APIBase+"/users/1", map[string]any{"status": "disabled"}, c, csrf, nil); code != 400 {
		t.Errorf("disable yourself: %d", code)
	}
	// Keys: the admin makes one for Ann; Ann (own_keys) sees hers only and cannot touch the admin's.
	var made struct {
		Key  string       `json:"key"`
		Info auth.KeyInfo `json:"info"`
	}
	if code := e.jsonCall("POST", APIBase+"/keys", map[string]any{"name": "phone", "user_id": uid}, c, csrf, &made); code != 201 ||
		made.Info.OwnerID != uid {
		t.Fatalf("key for ann: %d %+v", code, made)
	}
	var am Me
	if code := e.getKey(APIBase+"/auth/me", made.Key, &am); code != 200 || am.Username != "ann" || am.Role != auth.RoleUser {
		t.Errorf("ann's key: %d %+v", code, am)
	}
	if code := e.getKey(APIBase+"/jobs", made.Key, nil); code != 403 {
		t.Errorf("ann's key on /jobs: %d", code)
	}
	adminKey, info, _ := e.auth.CreateKey(ctx, "admin-key", 1)
	_ = adminKey
	ac, acsrf := e.session(uid)
	var keys struct {
		Items []auth.KeyInfo `json:"items"`
	}
	if code := e.jsonCall("GET", APIBase+"/keys", nil, ac, "", &keys); code != 200 || len(keys.Items) != 1 ||
		keys.Items[0].Name != "phone" {
		t.Errorf("ann's keys: %d %+v", code, keys)
	}
	if code := e.jsonCall("GET", APIBase+"/keys?user=all", nil, ac, "", nil); code != 403 {
		t.Errorf("ann lists all keys: %d", code)
	}
	if code := e.jsonCall("DELETE", APIBase+"/keys/"+itoa(info.ID), nil, ac, acsrf, nil); code != 404 {
		t.Errorf("ann revokes the admin's key: %d", code)
	}
	if code := e.jsonCall("POST", APIBase+"/keys", map[string]any{"name": "x", "user_id": 1}, ac, acsrf, nil); code != 403 {
		t.Errorf("ann makes a key for the admin: %d", code)
	}
	// Without own_keys a user has no keys routes.
	none := auth.Perm(0)
	e.auth.UpdateUser(ctx, uid, auth.UserPatch{ExtraPerms: &none})
	if code := e.jsonCall("GET", APIBase+"/keys", nil, ac, "", nil); code != 403 {
		t.Errorf("keys without own_keys: %d", code)
	}
	// Disabling ends the sessions and stops the keys.
	if code := e.jsonCall("PATCH", APIBase+"/users/"+itoa(uid), map[string]any{"status": "disabled"}, c, csrf, &u); code != 200 {
		t.Fatalf("disable: %d", code)
	}
	if code := e.getKey(APIBase+"/auth/me", made.Key, nil); code != 401 {
		t.Errorf("disabled account's key: %d", code)
	}
	if code := e.jsonCall("GET", APIBase+"/auth/me", nil, ac, "", nil); code != 401 {
		t.Errorf("disabled account's session: %d", code)
	}
	// A local account changes its own password; the current one must match.
	if code := e.jsonCall("POST", APIBase+"/me/password", map[string]any{"current": "wrong", "new": "a-new-password-1"}, c,
		csrf, nil); code != 403 {
		t.Errorf("wrong current password: %d", code)
	}
	if code := e.jsonCall("POST", APIBase+"/me/password", map[string]any{"current": e.pw, "new": "a-new-password-1"}, c,
		csrf, nil); code != 204 {
		t.Errorf("change password: %d", code)
	}
	if code := e.jsonCall("GET", APIBase+"/auth/me", nil, c, "", nil); code != 200 {
		t.Errorf("own session after a password change: %d", code)
	}
}
