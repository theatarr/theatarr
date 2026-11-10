// Package auth is the web layer's authentication (docs/design/web.md): on by default, with no switch to turn it off.
//
// # First start
//
// [New] makes sure the database holds a signing secret and at least one user. On the first start it generates the
// secret (32 random bytes) and an account named "admin" with a random 24-character password, and stores only the
// password's argon2id hash. The password itself goes to <data dir>/initial-admin-password (mode 0600). theatarr
// serve also prints it, with the user name, in a first-start block on stderr (the container log) at every start
// while that file exists ([FirstStartBanner]); it never goes through the logger, so the log files, the Logs page
// and the error inbox do not hold it. The file is deleted at the first successful login and by
// `theatarr admin reset-password` for admin, which sets a new password (and ends every session of that user).
//
// # Credentials
//
//   - Sessions, for the browser. POST /login with username and password sets the theatarr_session cookie: a 256-bit
//     random token (HttpOnly, SameSite=Lax, Path=/, Secure when the request came over TLS or through a proxy that
//     says X-Forwarded-Proto: https). The database keeps only its SHA-256. A session ends after SessionIdle without
//     use or SessionMax after login, whichever is first, at logout, or on a password reset.
//   - API keys, per client. "tha_" plus 256 random bits, shown once at creation, stored as SHA-256, revocable. Sent as
//     the X-Api-Key header. The apikey query parameter is not accepted here; the arr-compatible views will
//     accept it on their routes only.
//
// # CSRF
//
// Requests authenticated by a session cookie that change state (anything but GET, HEAD and OPTIONS) must carry a
// CSRF token, as the form field "csrf" or the X-CSRF-Token header. The token is HMAC-SHA256(secret, session hash), so
// it is bound to the session and needs no storage. The login form, which has no session yet, binds its token to a
// random theatarr_csrf cookie instead. An Origin header, when present, must match the request's host. API-key requests
// carry no ambient credentials and need no token.
//
// # Throttling
//
// See [Throttle]: failed logins are counted per client address and per username.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// Cookie and header names.
const (
	SessionCookie = "theatarr_session"
	LoginCSRF     = "theatarr_csrf"
	CSRFField     = "csrf"
	CSRFHeader    = "X-CSRF-Token"
	APIKeyHeader  = "X-Api-Key"
	AdminUsername = "admin"
)

// Defaults for Options.
const (
	DefaultSessionIdle = 7 * 24 * time.Hour
	DefaultSessionMax  = 30 * 24 * time.Hour
	// touchEvery limits how often a session's last_seen_at or a key's last_used_at is written.
	touchEvery = time.Minute
)

// CookieSecure decides the Secure flag on cookies.
type CookieSecure string

const (
	// SecureAuto sets Secure when the request came over TLS or X-Forwarded-Proto is https.
	SecureAuto   CookieSecure = "auto"
	SecureAlways CookieSecure = "always"
	SecureNever  CookieSecure = "never"
)

// ParseCookieSecure parses auto, always or never.
func ParseCookieSecure(s string) (CookieSecure, error) {
	switch c := CookieSecure(s); c {
	case SecureAuto, SecureAlways, SecureNever:
		return c, nil
	}
	return "", fmt.Errorf("cookie secure %q: want auto, always or never", s)
}

// Options tune New. The zero value is fine.
type Options struct {
	Logger       *slog.Logger
	Now          func() time.Time
	SessionIdle  time.Duration
	SessionMax   time.Duration
	CookieSecure CookieSecure
	Throttle     *Throttle
	// DataDir is where the first start writes InitialPasswordFile (the database's directory). Without it the
	// generated password is written nowhere: use `theatarr admin reset-password`.
	DataDir string
}

// InitialPasswordFile is the file in the data directory that holds the first start's admin password, mode 0600,
// until the first successful login or `theatarr admin reset-password`.
const InitialPasswordFile = "initial-admin-password"

// RemoveInitialPassword deletes dir's InitialPasswordFile; a missing file is fine. It reports whether one was
// removed.
func RemoveInitialPassword(dir string) (bool, error) {
	if dir == "" {
		return false, nil
	}
	err := os.Remove(filepath.Join(dir, InitialPasswordFile))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// FirstStartBanner is the block theatarr serve prints to stderr (the container log, not the logger) while the first
// start's admin password is pending, that is while dir's InitialPasswordFile exists: the address to open, the user
// and the password read from the file. It is "" once the file is gone (the first login removes it) or unreadable.
func FirstStartBanner(dir, address string) string {
	if dir == "" {
		return ""
	}
	path := filepath.Join(dir, InitialPasswordFile)
	b, err := os.ReadFile(path)
	password := strings.TrimSpace(string(b))
	if err != nil || password == "" {
		return ""
	}
	line := strings.Repeat("=", 64)
	return line + "\n" +
		"  Theatarr first start: sign in at " + address + "\n" +
		"    user      " + AdminUsername + "\n" +
		"    password  " + password + "\n" +
		"  The password is also in " + path + ".\n" +
		"  That file is deleted at the first sign-in, and this message stops with it.\n" +
		line + "\n"
}

// writeInitialPassword writes the password to dir's InitialPasswordFile, mode 0600, replacing any older one.
func writeInitialPassword(dir, password string) (string, error) {
	path := filepath.Join(dir, InitialPasswordFile)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(password + "\n"); err != nil {
		f.Close()
		return "", err
	}
	return path, f.Close()
}

// Service checks credentials. It is safe for concurrent use.
type Service struct {
	st     *store.Store
	log    *slog.Logger
	now    func() time.Time
	idle   time.Duration
	maxAge time.Duration
	secure CookieSecure
	secret []byte
	thr    *Throttle
	dummy  string // a hash verified for unknown users, so timing does not reveal which usernames exist
	// verifying holds a slot per password check in progress (maxVerifying).
	verifying chan struct{}

	mu      sync.Mutex
	touched map[string]time.Time

	// dataDir holds InitialPasswordFile; pwPending is true while it may still exist (removed at the first login).
	dataDir   string
	pwPending atomic.Bool
}

// Errors from Login.
var (
	ErrInvalid = errors.New("auth: wrong username or password")
)

// Throttled is returned by Login while the client or username is locked.
type Throttled struct{ Until time.Time }

func (t *Throttled) Error() string {
	return "auth: too many failed logins, try again after " + t.Until.UTC().Format(time.RFC3339)
}

// New loads the signing secret and makes sure an account exists, creating both on the first start (see the
// package doc). The generated admin password is written to o.DataDir's InitialPasswordFile, never passed to the
// logger, and returned so callers and tests can use it; it is empty on every later start.
func New(ctx context.Context, st *store.Store, o Options) (*Service, string, error) {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.SessionIdle <= 0 {
		o.SessionIdle = DefaultSessionIdle
	}
	if o.SessionMax <= 0 {
		o.SessionMax = DefaultSessionMax
	}
	if o.CookieSecure == "" {
		o.CookieSecure = SecureAuto
	}
	if o.Throttle == nil {
		o.Throttle = NewThrottle()
	}
	s := &Service{st: st, log: o.Logger.With("component", "auth"), now: o.Now, idle: o.SessionIdle,
		maxAge: o.SessionMax, secure: o.CookieSecure, thr: o.Throttle, touched: map[string]time.Time{},
		dataDir: o.DataDir, verifying: make(chan struct{}, maxVerifying)}
	var err error
	if s.dummy, err = HashPassword("not a password"); err != nil {
		return nil, "", err
	}
	var password, pwPath string
	err = st.WriteTx(ctx, func(tx *sql.Tx) error {
		now := store.Millis(s.now())
		var hexSecret string
		err := tx.QueryRowContext(ctx, `SELECT value FROM web_setting WHERE name = 'signing_secret'`).Scan(&hexSecret)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			b := make([]byte, 32)
			if _, err := rand.Read(b); err != nil {
				return err
			}
			hexSecret = hex.EncodeToString(b)
			if _, err := tx.ExecContext(ctx, `INSERT INTO web_setting (name, value, created_at, updated_at)
				VALUES ('signing_secret', ?, ?, ?)`, hexSecret, now, now); err != nil {
				return err
			}
		case err != nil:
			return err
		}
		if s.secret, err = hex.DecodeString(hexSecret); err != nil || len(s.secret) < 32 {
			return errors.New("auth: signing secret in web_setting is malformed")
		}
		var users int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM web_user`).Scan(&users); err != nil {
			return err
		}
		if users > 0 {
			password = ""
			return nil
		}
		if password, err = RandomPassword(); err != nil {
			return err
		}
		hash, err := HashPassword(password)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO web_user (username, password_hash, created_at, updated_at,
			password_changed_at) VALUES (?, ?, ?, ?, ?)`, AdminUsername, hash, now, now, now)
		if err != nil || o.DataDir == "" {
			return err
		}
		// Before the commit: when the file cannot be written, the account is not created either.
		pwPath, err = writeInitialPassword(o.DataDir, password)
		return err
	})
	if err != nil {
		return nil, "", fmt.Errorf("auth: init: %w", err)
	}
	switch {
	case password != "" && pwPath != "":
		// Never the password itself: the logger feeds the log files, GET /logs and the error inbox. theatarr serve
		// prints it on stderr only (FirstStartBanner).
		s.log.Warn("first start: created the admin account; its password is in the file (mode 0600), which is "+
			"deleted at the first login; or set one with 'theatarr admin reset-password'", "username", AdminUsername,
			"file", pwPath)
	case password != "":
		s.log.Warn("first start: created the admin account; no data directory, so its password was written "+
			"nowhere: set one with 'theatarr admin reset-password'", "username", AdminUsername)
	}
	if o.DataDir != "" {
		if _, err := os.Stat(filepath.Join(o.DataDir, InitialPasswordFile)); err == nil {
			s.pwPending.Store(true)
		}
	}
	return s, password, nil
}

// Identity is who a request is from.
type Identity struct {
	// Kind is "session" or "apikey".
	Kind     string
	UserID   int64
	Username string
	// KeyID and KeyName are set for API keys.
	KeyID   int64
	KeyName string
	// Role, Perms and DisplayName are the account's (for a key, its owner's). Perms is the effective set: the
	// role's preset, the extra bits and auto-approve.
	Role        string
	Perms       Perm
	DisplayName string
	// Local: the account has a password (the break-glass admin and other local accounts); Plex accounts have none.
	Local bool
	// sessionHash is set for sessions; the CSRF token derives from it.
	sessionHash string
}

// Session reports whether the identity came from a session cookie (and so needs CSRF tokens).
func (id *Identity) Session() bool { return id != nil && id.Kind == "session" }

type ctxKey struct{}

// WithIdentity returns ctx carrying id.
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext returns the request's identity, or nil.
func FromContext(ctx context.Context) *Identity {
	id, _ := ctx.Value(ctxKey{}).(*Identity)
	return id
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ClientAddr is the request's peer address without the port. Forwarded headers are not trusted.
func ClientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Authenticate returns the identity behind r's API key or session cookie, or nil when it has neither or they are
// not valid. Only the X-Api-Key header is read, never the query string.
func (s *Service) Authenticate(r *http.Request) (*Identity, error) {
	ctx := r.Context()
	if key := r.Header.Get(APIKeyHeader); key != "" {
		return s.checkKey(ctx, key)
	}
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	return s.checkSession(ctx, c.Value)
}

// CheckAPIKey returns the identity behind an API key however the client sent it, or nil when the key is not an
// active key. It is for the arr-compatible views (internal/arrview), which take the key from the X-Api-Key header,
// the apikey query parameter or a bearer token as the arrs do; [Service.Authenticate] reads the header only.
func (s *Service) CheckAPIKey(ctx context.Context, key string) (*Identity, error) {
	if key == "" {
		return nil, nil
	}
	return s.checkKey(ctx, key)
}

// userCols are the account columns an identity carries, for scanUser.
const userCols = `u.username, u.role, u.permissions, u.auto_approve, u.status, u.display_name, u.password_hash != ''`

// scanUser fills id's account fields and reports whether the account may sign in (status active).
func scanUser(id *Identity, extra int64, auto bool, status string) bool {
	id.Perms = Effective(id.Role, Perm(extra), auto)
	return status == StatusActive
}

func (s *Service) checkKey(ctx context.Context, key string) (*Identity, error) {
	h := sha(key)
	id := &Identity{Kind: "apikey"}
	var user sql.NullInt64
	var username, role, status, display sql.NullString
	var extra sql.NullInt64
	var auto, local sql.NullBool
	err := s.st.Reader().QueryRowContext(ctx, `SELECT k.id, k.name, k.created_by, `+userCols+` FROM api_key k
		LEFT JOIN web_user u ON u.id = k.created_by WHERE k.key_hash = ? AND k.revoked_at IS NULL`, h).
		Scan(&id.KeyID, &id.KeyName, &user, &username, &role, &extra, &auto, &status, &display, &local)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// A key carries its owner's permissions; a key whose owner is gone, pending or disabled is refused.
	if !user.Valid || !username.Valid {
		return nil, nil
	}
	id.UserID, id.Username, id.Role, id.DisplayName, id.Local = user.Int64, username.String, role.String,
		display.String, local.Bool
	if !scanUser(id, extra.Int64, auto.Bool, status.String) {
		return nil, nil
	}
	if s.shouldTouch("k" + h) {
		if _, err := s.st.Writer().ExecContext(ctx, `UPDATE api_key SET last_used_at = ? WHERE id = ?`,
			store.Millis(s.now()), id.KeyID); err != nil {
			s.log.Warn("api key last use", "err", err)
		}
	}
	return id, nil
}

func (s *Service) checkSession(ctx context.Context, token string) (*Identity, error) {
	h := sha(token)
	id := &Identity{Kind: "session", sessionHash: h}
	var lastSeen, expires, extra int64
	var auto bool
	var status string
	err := s.st.Reader().QueryRowContext(ctx, `SELECT s.user_id, s.last_seen_at, s.expires_at, `+userCols+`
		FROM web_session s JOIN web_user u ON u.id = s.user_id WHERE s.token_hash = ?`, h).
		Scan(&id.UserID, &lastSeen, &expires, &id.Username, &id.Role, &extra, &auto, &status, &id.DisplayName,
			&id.Local)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := s.now()
	active := scanUser(id, extra, auto, status)
	if !active || !now.Before(store.FromMillis(expires)) || now.Sub(store.FromMillis(lastSeen)) >= s.idle {
		if _, err := s.st.Writer().ExecContext(ctx, `DELETE FROM web_session WHERE token_hash = ?`, h); err != nil {
			s.log.Warn("expired session delete", "err", err)
		}
		return nil, nil
	}
	if s.shouldTouch("s" + h) {
		if _, err := s.st.Writer().ExecContext(ctx, `UPDATE web_session SET last_seen_at = ? WHERE token_hash = ?`,
			store.Millis(now), h); err != nil {
			s.log.Warn("session last seen", "err", err)
		}
	}
	return id, nil
}

func (s *Service) shouldTouch(k string) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.touched[k]; ok && now.Sub(t) < touchEvery {
		return false
	}
	if len(s.touched) > 10000 {
		clear(s.touched)
	}
	s.touched[k] = now
	return true
}

// maxVerifying bounds the password checks running at once. Each argon2id check holds 64 MiB (defaultArgon) for
// 50-100 ms, so 2 cap it at 128 MiB, whatever a burst of requests from spread addresses and names does. A household
// signs in a few times a day: two at once is already rare, and a refused login is answered like a throttled one
// (429, retry in a second).
const maxVerifying = 2

// busyRetry is the Retry-After of a login refused because maxVerifying checks are running.
const busyRetry = time.Second

// Login checks a username and password from addr and, on success, creates a session and returns its token (the
// cookie value). Failures return ErrInvalid, or *Throttled while locked or while maxVerifying checks are running.
// Known and unknown names take the same steps: a slot, the attempt counted (Throttle.Reserve), one hash.
func (s *Service) Login(ctx context.Context, username, password, addr, userAgent string) (string, error) {
	now := s.now()
	keys := []string{"addr:" + addr, "user:" + strings.ToLower(username)}
	select {
	case s.verifying <- struct{}{}:
	default:
		return "", &Throttled{Until: now.Add(busyRetry)}
	}
	release := sync.OnceFunc(func() { <-s.verifying })
	defer release()
	if until, ok := s.thr.Reserve(now, keys...); !ok {
		return "", &Throttled{Until: until}
	}
	var userID int64
	var hash, status string
	err := s.st.Reader().QueryRowContext(ctx, `SELECT id, password_hash, status FROM web_user WHERE username = ?`,
		username).Scan(&userID, &hash, &status)
	known := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	// A Plex account has no password: it signs in through Plex only, and costs the same hash as an unknown name.
	if !known || hash == "" {
		known, hash = false, s.dummy
	}
	ok, verr := VerifyPassword(hash, password)
	release()
	if verr != nil {
		s.log.Error("password hash unreadable", "username", username, "err", verr)
	}
	// A failure was counted by Reserve.
	if !known || !ok {
		s.log.Warn("login failed", "username", username, "remote", addr)
		return "", ErrInvalid
	}
	if status != StatusActive {
		s.log.Warn("login refused: account not active", "username", username, "status", status, "remote", addr)
		return "", ErrInvalid
	}
	s.thr.Succeed(keys...)
	token, err := s.StartSession(ctx, userID, addr, userAgent)
	if err != nil {
		return "", err
	}
	if s.pwPending.CompareAndSwap(true, false) {
		if removed, err := RemoveInitialPassword(s.dataDir); err != nil {
			s.log.Error("removing the initial admin password file", "err", err)
		} else if removed {
			s.log.Info("first login: removed the initial admin password file", "file",
				filepath.Join(s.dataDir, InitialPasswordFile))
		}
	}
	s.log.Info("login", "username", username, "remote", addr)
	return token, nil
}

// StartSession creates a session for an account that has proven who it is (a password, or Plex) and returns its
// token, the cookie value. It does not check the account's status; callers do.
func (s *Service) StartSession(ctx context.Context, userID int64, addr, userAgent string) (string, error) {
	now := s.now()
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	if len(userAgent) > 200 {
		userAgent = userAgent[:200]
	}
	ms := store.Millis(now)
	_, err = s.st.Writer().ExecContext(ctx, `INSERT INTO web_session (token_hash, user_id, created_at, last_seen_at,
		expires_at, remote, user_agent) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sha(token), userID, ms, ms, store.Millis(now.Add(s.maxAge)), addr, userAgent)
	if err != nil {
		return "", err
	}
	if _, err := s.st.Writer().ExecContext(ctx, `UPDATE web_user SET last_login_at = ? WHERE id = ?`, ms,
		userID); err != nil {
		s.log.Warn("last login", "err", err)
	}
	// Housekeeping: drop sessions past their absolute or idle limit.
	if _, err := s.st.Writer().ExecContext(ctx, `DELETE FROM web_session WHERE expires_at <= ? OR last_seen_at <= ?`,
		ms, store.Millis(now.Add(-s.idle))); err != nil {
		s.log.Warn("session cleanup", "err", err)
	}
	return token, nil
}

// Logout ends the session behind token.
func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.st.Writer().ExecContext(ctx, `DELETE FROM web_session WHERE token_hash = ?`, sha(token))
	return err
}

// SetSessionCookie writes the session cookie on w.
func (s *Service) SetSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.isSecure(r), MaxAge: int(s.maxAge.Seconds())})
}

// ClearSessionCookie removes the session cookie.
func (s *Service) ClearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.isSecure(r), MaxAge: -1})
}

func (s *Service) isSecure(r *http.Request) bool {
	switch s.secure {
	case SecureAlways:
		return true
	case SecureNever:
		return false
	}
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Service) mac(parts ...string) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(strings.Join(parts, "|")))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// CSRFToken returns the CSRF token for r's session. Without a session (the login form) it binds the token to the
// theatarr_csrf cookie, setting one on w if r has none.
func (s *Service) CSRFToken(w http.ResponseWriter, r *http.Request) string {
	if id := FromContext(r.Context()); id.Session() {
		return s.mac("csrf", id.sessionHash)
	}
	if c, err := r.Cookie(LoginCSRF); err == nil && len(c.Value) >= 32 {
		return s.mac("login", c.Value)
	}
	v, err := randomToken()
	if err != nil {
		return ""
	}
	http.SetCookie(w, &http.Cookie{Name: LoginCSRF, Value: v, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.isSecure(r)})
	// Later reads in this request see the new cookie.
	r.AddCookie(&http.Cookie{Name: LoginCSRF, Value: v})
	return s.mac("login", v)
}

// SessionCSRF is the CSRF token of the session whose cookie value is token (for the login response).
func (s *Service) SessionCSRF(token string) string { return s.mac("csrf", sha(token)) }

// CheckCSRF reports whether a state-changing request carries a valid token (form field or header) and, when the
// browser sent an Origin, comes from this host. For a session the token must match the session; otherwise it must
// match the theatarr_csrf cookie.
func (s *Service) CheckCSRF(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			return false
		}
	}
	got := r.Header.Get(CSRFHeader)
	if got == "" {
		got = r.PostFormValue(CSRFField)
	}
	if got == "" {
		return false
	}
	var want string
	if id := FromContext(r.Context()); id.Session() {
		want = s.mac("csrf", id.sessionHash)
	} else if c, err := r.Cookie(LoginCSRF); err == nil && len(c.Value) >= 32 {
		want = s.mac("login", c.Value)
	} else {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// ResetPassword sets username's password, creating the account if it does not exist, and ends its sessions. It
// works on the database alone, so `theatarr admin reset-password` can run while the service is stopped.
func ResetPassword(ctx context.Context, st *store.Store, username, password string) error {
	if len(password) < MinPasswordLen {
		return fmt.Errorf("password too short: at least %d characters", MinPasswordLen)
	}
	if username == "" {
		return errors.New("empty username")
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	now := store.Millis(time.Now())
	return st.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE web_user SET password_hash = ?, updated_at = ?, password_changed_at = ?
			WHERE username = ?`, hash, now, now, username)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO web_user (username, password_hash, created_at, updated_at,
				password_changed_at) VALUES (?, ?, ?, ?, ?)`, username, hash, now, now, now); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM web_session WHERE user_id = (SELECT id FROM web_user WHERE username = ?)`,
			username)
		return err
	})
}
