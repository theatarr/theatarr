package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func init() { SetFastHashing() }

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}

// testAdminPassword is the admin password newService sets.
const testAdminPassword = "test admin password"

// newService starts the service and, when no account exists, creates admin with testAdminPassword (the first start
// creates none).
func newService(t *testing.T, st *store.Store, c *clock) (*Service, string) {
	t.Helper()
	ctx := context.Background()
	s, _, err := New(ctx, st, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	if need, err := s.NeedsFirstAccount(ctx); err != nil {
		t.Fatal(err)
	} else if need {
		if err := ResetPassword(ctx, st, AdminUsername, testAdminPassword); err != nil {
			t.Fatal(err)
		}
	}
	return s, testAdminPassword
}

func TestPasswordHash(t *testing.T) {
	for _, fast := range []bool{true, false} {
		if !fast {
			activeArgon = defaultArgon
		}
		h, err := HashPassword("correct horse")
		activeArgon = testArgon
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(h, "$argon2id$v=19$") {
			t.Errorf("hash %q", h)
		}
		if ok, err := VerifyPassword(h, "correct horse"); !ok || err != nil {
			t.Errorf("verify right password: %v %v", ok, err)
		}
		if ok, _ := VerifyPassword(h, "correct horsE"); ok {
			t.Error("verify wrong password: ok")
		}
	}
	for _, bad := range []string{"", "plain", "$argon2id$v=19$m=0,t=1,p=1$AAAA$AAAA", "$bcrypt$x$y$z$w"} {
		if _, err := VerifyPassword(bad, "x"); err == nil {
			t.Errorf("VerifyPassword(%q): want an error", bad)
		}
	}
}

// The first start makes the signing secret and no account; it logs that no admin exists yet. A later start keeps the
// secret.
func TestFirstStartCreatesNoAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	dir := t.TempDir()
	st := openStore(t)
	c := &clock{t: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	s, pw, err := New(ctx, st, Options{Logger: log, Now: c.now, DataDir: dir})
	if err != nil || pw != "" {
		t.Fatal(pw, err)
	}
	var users int
	if err := st.Reader().QueryRow(`SELECT COUNT(*) FROM web_user`).Scan(&users); err != nil || users != 0 {
		t.Fatalf("users %d %v", users, err)
	}
	if _, err := os.Stat(filepath.Join(dir, InitialPasswordFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("password file on a new install: %v", err)
	}
	if need, err := s.NeedsFirstAccount(ctx); !need || err != nil || s.PasswordPending() {
		t.Errorf("needs %v %v, pending %v", need, err, s.PasswordPending())
	}
	if n := strings.Count(logs.String(), "no admin account exists yet"); n != 1 {
		t.Errorf("%d start-up lines about the missing admin: %s", n, logs.String())
	}
	s2, _, err := New(ctx, st, Options{Logger: log, Now: c.now, DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if string(s2.secret) != string(s.secret) || len(s.secret) < 32 {
		t.Error("signing secret changed between starts")
	}
	// Once an account exists, a start says nothing about it.
	if err := ResetPassword(ctx, st, AdminUsername, testAdminPassword); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	if _, _, err := New(ctx, st, Options{Logger: log, Now: c.now, DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "no admin account") {
		t.Errorf("start with an account: %s", logs.String())
	}
}

// An install made by an older version whose generated password was never used keeps its file and its normal login:
// the file stays across restarts and failed logins, the first successful login removes it, and the create page is
// refused.
func TestPendingInitialPasswordFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	dir := t.TempDir()
	st := openStore(t)
	c := &clock{t: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	// What an older first start left: the admin with a generated password and the file holding it.
	pw := "generated-by-an-older-start"
	if err := ResetPassword(ctx, st, AdminUsername, pw); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, InitialPasswordFile)
	if err := os.WriteFile(path, []byte(pw+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _, err := New(ctx, st, Options{Logger: log, Now: c.now, DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if need, err := s.NeedsFirstAccount(ctx); need || err != nil || !s.PasswordPending() {
		t.Fatalf("needs %v %v, pending %v", need, err, s.PasswordPending())
	}
	if _, err := s.CreateFirstAdmin(ctx, "bob", "a long enough password", "10.1.2.3", ""); !errors.Is(err, ErrClaimed) {
		t.Fatalf("create on a pending install: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("start removed the file: %v", err)
	}
	// reset-password (another process) removing the file ends the pending state too.
	if err := os.Rename(path, path+".moved"); err != nil {
		t.Fatal(err)
	}
	if s.PasswordPending() {
		t.Error("pending without the file")
	}
	if err := os.Rename(path+".moved", path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, "admin", "wrong", "10.1.2.3", ""); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("a failed login removed the file")
	}
	if _, err := s.Login(ctx, "admin", pw, "10.1.2.3", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("file after the first login: %v", err)
	}
	if s.PasswordPending() {
		t.Error("still pending after the first login")
	}
	if strings.Contains(logs.String(), pw) || strings.Contains(logs.String(), "no admin account") {
		t.Errorf("log: %s", logs.String())
	}
	// RemoveInitialPassword: a missing file is fine.
	if removed, err := RemoveInitialPassword(dir); removed || err != nil {
		t.Errorf("remove missing: %v %v", removed, err)
	}
}

func req(token string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	if token != "" {
		r.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
	}
	return r
}

func TestLoginAndSessionExpiry(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	c := &clock{t: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	s, pw := newService(t, st, c)
	ctx := context.Background()

	if _, err := s.Login(ctx, "admin", "wrong", "10.1.2.3", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := s.Login(ctx, "nobody", pw, "10.1.2.3", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown user: %v", err)
	}
	tok, err := s.Login(ctx, "admin", pw, "10.1.2.3", "test")
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Authenticate(req(tok))
	if err != nil || id == nil || id.Username != "admin" || !id.Session() {
		t.Fatalf("authenticate: %+v %v", id, err)
	}
	if id, _ := s.Authenticate(req(tok + "x")); id != nil {
		t.Error("tampered token accepted")
	}

	// Idle: used every 6 days, the session lives on; then the 30-day absolute limit ends it.
	for range 4 {
		c.add(6 * 24 * time.Hour)
		if id, _ := s.Authenticate(req(tok)); id == nil {
			t.Fatalf("session ended early at %v", c.now())
		}
	}
	c.add(6 * 24 * time.Hour) // day 30
	if id, _ := s.Authenticate(req(tok)); id != nil {
		t.Error("session outlived SessionMax")
	}

	// Idle timeout: 7 days without use.
	tok2, err := s.Login(ctx, "admin", pw, "10.1.2.3", "")
	if err != nil {
		t.Fatal(err)
	}
	c.add(7*24*time.Hour + time.Minute)
	if id, _ := s.Authenticate(req(tok2)); id != nil {
		t.Error("idle session still valid")
	}

	// Logout.
	tok3, _ := s.Login(ctx, "admin", pw, "10.1.2.3", "")
	if err := s.Logout(ctx, tok3); err != nil {
		t.Fatal(err)
	}
	if id, _ := s.Authenticate(req(tok3)); id != nil {
		t.Error("session valid after logout")
	}
}

func TestLoginThrottle(t *testing.T) {
	st := openStore(t)
	c := &clock{t: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	s, pw := newService(t, st, c)
	ctx := context.Background()
	for i := range 5 {
		if _, err := s.Login(ctx, "admin", "guess", "192.0.2.1", ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("failure %d: %v", i+1, err)
		}
	}
	// Sixth failure locks; then even the right password is refused, from that address and for that username.
	s.Login(ctx, "admin", "guess", "192.0.2.1", "")
	var thr *Throttled
	if _, err := s.Login(ctx, "admin", pw, "192.0.2.1", ""); !errors.As(err, &thr) {
		t.Fatalf("locked login: %v", err)
	}
	if _, err := s.Login(ctx, "admin", pw, "192.0.2.99", ""); !errors.As(err, &thr) {
		t.Fatalf("username lock from another address: %v", err)
	}
	if _, err := s.Login(ctx, "someone", "x", "192.0.2.1", ""); !errors.As(err, &thr) {
		t.Fatalf("address lock for another username: %v", err)
	}
	// Locks grow: the next failure after the lock doubles it.
	first := thr.Until.Sub(c.now())
	c.add(first + time.Millisecond)
	s.Login(ctx, "admin", "guess", "192.0.2.1", "")
	_, err := s.Login(ctx, "admin", pw, "192.0.2.1", "")
	if !errors.As(err, &thr) || thr.Until.Sub(c.now()) <= first {
		t.Fatalf("second lock %v not longer than the first %v", err, first)
	}
	// After the lock, the right password works and clears the count.
	c.add(time.Hour)
	if _, err := s.Login(ctx, "admin", pw, "192.0.2.1", ""); err != nil {
		t.Fatalf("after lock: %v", err)
	}
	if _, err := s.Login(ctx, "admin", "guess", "192.0.2.1", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("count not cleared: %v", err)
	}
}

func TestThrottleCap(t *testing.T) {
	t.Parallel()
	th := NewThrottle()
	now := time.Now()
	for range 100 {
		th.Fail(now, "k")
	}
	until, ok := th.Locked(now, "k")
	if !ok || until.Sub(now) > th.MaxLock {
		t.Errorf("lock %v beyond MaxLock", until.Sub(now))
	}
}

// The table is bounded: a flood of unseen keys never grows it past MaxEntries, and a locked key under attack is
// the last to be evicted.
func TestThrottleBounded(t *testing.T) {
	th := NewThrottle()
	th.MaxEntries = 100
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	for range 12 { // locked for 128 s, past the flood below
		th.Fail(now, "user:admin")
	}
	if _, ok := th.Locked(now, "user:admin"); !ok {
		t.Fatal("admin not locked")
	}
	// 10,000 failures from fresh addresses within the window (nothing expires), plus some locked keys.
	for i := range 10000 {
		now = now.Add(time.Millisecond)
		th.Fail(now, fmt.Sprintf("addr:192.0.2.%d/%d", i%250, i))
		if n := th.size(); n > th.MaxEntries {
			t.Fatalf("after %d failures: %d entries, cap %d", i+1, n, th.MaxEntries)
		}
	}
	if _, ok := th.Locked(now, "user:admin"); !ok {
		t.Error("the locked admin key was evicted by unlocked ones")
	}
	// The newest key is kept: eviction takes the least recently failed.
	last := "addr:192.0.2.249/9999"
	th.mu.Lock()
	_, kept := th.entries[last]
	th.mu.Unlock()
	if !kept {
		t.Error("the newest key was evicted")
	}
	// A huge username is stored hashed, and still counts.
	long := "user:" + strings.Repeat("x", 1<<16)
	for range 6 {
		th.Fail(now, long)
	}
	if _, ok := th.Locked(now, long); !ok {
		t.Error("long key not locked")
	}
	th.mu.Lock()
	for k := range th.entries {
		if len(k) > maxThrottleKey {
			t.Errorf("key of %d bytes stored", len(k))
		}
	}
	th.mu.Unlock()
	// Default cap.
	if NewThrottle().MaxEntries != 10000 {
		t.Error("default cap")
	}
}

func TestAPIKeys(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	c := &clock{t: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	s, _ := newService(t, st, c)
	ctx := context.Background()
	key, info, err := s.CreateKey(ctx, "homepage", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "tha_") || len(key) < 40 || info.Prefix != key[:10] {
		t.Fatalf("key %q info %+v", key, info)
	}
	if _, _, err := s.CreateKey(ctx, "homepage", 1); !errors.Is(err, ErrKeyExists) {
		t.Errorf("duplicate name: %v", err)
	}
	for _, bad := range []string{"", "has space", strings.Repeat("a", 65), "semi;colon"} {
		if _, _, err := s.CreateKey(ctx, bad, 1); !errors.Is(err, ErrKeyName) {
			t.Errorf("name %q: %v", bad, err)
		}
	}
	var stored int
	st.Reader().QueryRow(`SELECT COUNT(*) FROM api_key WHERE key_hash = ? OR prefix = ?`, key, key).Scan(&stored)
	if stored != 0 {
		t.Error("key stored in clear")
	}

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set(APIKeyHeader, key)
	id, err := s.Authenticate(r)
	if err != nil || id == nil || id.Kind != "apikey" || id.KeyName != "homepage" || id.Session() {
		t.Fatalf("authenticate with key: %+v %v", id, err)
	}
	// The query string is never read.
	q := httptest.NewRequest("GET", "/?apikey="+url.QueryEscape(key), nil)
	if id, _ := s.Authenticate(q); id != nil {
		t.Error("apikey query parameter accepted")
	}
	keys, err := s.ListKeys(ctx, 0)
	if err != nil || len(keys) != 1 || keys[0].LastUsedAt == nil {
		t.Fatalf("list: %+v %v", keys, err)
	}
	if err := s.RevokeKey(ctx, info.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeKey(ctx, info.ID, 0); !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("second revoke: %v", err)
	}
	if id, _ := s.Authenticate(r); id != nil {
		t.Error("revoked key accepted")
	}
	// The name is free again once revoked.
	if _, _, err := s.CreateKey(ctx, "homepage", 1); err != nil {
		t.Errorf("recreate after revoke: %v", err)
	}
}

func TestResetPassword(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	c := &clock{t: time.Now()}
	s, pw := newService(t, st, c)
	ctx := context.Background()
	tok, err := s.Login(ctx, "admin", pw, "10.1.2.3", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := ResetPassword(ctx, st, "admin", "short"); err == nil {
		t.Error("short password accepted")
	}
	if err := ResetPassword(ctx, st, "admin", "a new long password"); err != nil {
		t.Fatal(err)
	}
	if id, _ := s.Authenticate(req(tok)); id != nil {
		t.Error("session survived a password reset")
	}
	if _, err := s.Login(ctx, "admin", pw, "10.1.2.3", ""); !errors.Is(err, ErrInvalid) {
		t.Error("old password still works")
	}
	if _, err := s.Login(ctx, "admin", "a new long password", "10.1.2.3", ""); err != nil {
		t.Errorf("new password: %v", err)
	}
}

func TestCSRF(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	c := &clock{t: time.Now()}
	s, pw := newService(t, st, c)
	tok, _ := s.Login(context.Background(), "admin", pw, "10.1.2.3", "")
	id, _ := s.Authenticate(req(tok))

	post := func(token, origin string, sess *Identity) *http.Request {
		r := httptest.NewRequest("POST", "http://theatarr.lan:7700/x", strings.NewReader(url.Values{CSRFField: {token}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if sess != nil {
			r = r.WithContext(WithIdentity(r.Context(), sess))
		}
		return r
	}
	good := s.SessionCSRF(tok)
	if !s.CheckCSRF(post(good, "", id)) {
		t.Error("valid token refused")
	}
	if !s.CheckCSRF(post(good, "http://theatarr.lan:7700", id)) {
		t.Error("same origin refused")
	}
	if s.CheckCSRF(post(good, "http://evil.example", id)) {
		t.Error("foreign origin accepted")
	}
	if s.CheckCSRF(post("", "", id)) || s.CheckCSRF(post(good+"x", "", id)) {
		t.Error("missing or wrong token accepted")
	}
	// Another session's token does not fit.
	tok2, _ := s.Login(context.Background(), "admin", pw, "10.1.2.3", "")
	if s.CheckCSRF(post(s.SessionCSRF(tok2), "", id)) {
		t.Error("token of another session accepted")
	}
	// Login form: token bound to the theatarr_csrf cookie.
	rec := httptest.NewRecorder()
	get := httptest.NewRequest("GET", "/login", nil)
	ft := s.CSRFToken(rec, get)
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != LoginCSRF || !cookies[0].HttpOnly {
		t.Fatalf("login csrf cookie: %+v", cookies)
	}
	p := post(ft, "", nil)
	p.AddCookie(cookies[0])
	if !s.CheckCSRF(p) {
		t.Error("login form token refused")
	}
	p = post(ft, "", nil)
	if s.CheckCSRF(p) {
		t.Error("login form token accepted without its cookie")
	}
}

func TestSessionCookieFlags(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	s, _ := newService(t, st, &clock{t: time.Now()})
	for _, c := range []struct {
		proto  string
		secure bool
	}{{"", false}, {"https", true}} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/login", nil)
		if c.proto != "" {
			r.Header.Set("X-Forwarded-Proto", c.proto)
		}
		s.SetSessionCookie(rec, r, "tok")
		ck := rec.Result().Cookies()[0]
		if !ck.HttpOnly || ck.SameSite != http.SameSiteLaxMode || ck.Secure != c.secure || ck.Path != "/" {
			t.Errorf("proto %q: cookie %+v", c.proto, ck)
		}
	}
}
