package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"github.com/theatarr/theatarr/internal/store"
)

// First account (package doc, "First start"): while web_user is empty, the web UI offers "Create your admin account".

// ErrClaimed is returned by CreateFirstAdmin once any account exists.
var ErrClaimed = errors.New("auth: this install already has an account")

// ErrNoAdminYet is returned by PlexSignIn while no account exists: the first account is the admin, made on the
// create page.
var ErrNoAdminYet = errors.New("auth: no admin account exists yet")

// InvalidAccount is a username or password CreateFirstAdmin refuses; its text is for the person filling the form.
type InvalidAccount string

func (e InvalidAccount) Error() string { return string(e) }

// maxUsernameLen bounds a username chosen on the create page.
const maxUsernameLen = 64

// NeedsFirstAccount reports whether the user table is empty. It reads the database each time: `theatarr admin
// reset-password` can create the account from another process.
func (s *Service) NeedsFirstAccount(ctx context.Context) (bool, error) {
	var exists bool
	err := s.st.Reader().QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM web_user)`).Scan(&exists)
	return !exists, err
}

// PasswordPending reports whether an older version's generated admin password file is still there unused (it is
// removed at the first successful login, or by `theatarr admin reset-password` from another process).
func (s *Service) PasswordPending() bool {
	if !s.pwPending.Load() {
		return false
	}
	_, err := os.Stat(filepath.Join(s.dataDir, InitialPasswordFile))
	return err == nil
}

// CreateFirstAdmin creates the first account, an active admin with a password, and starts its session (the token
// is the cookie value). It works only while the user table is empty: the check and the insert are one statement,
// so of two requests at once only one creates an account; every later call gets ErrClaimed. The password is never
// logged.
func (s *Service) CreateFirstAdmin(ctx context.Context, username, password, addr, userAgent string) (string, error) {
	username = strings.TrimSpace(username)
	switch {
	case username == "":
		return "", InvalidAccount("Enter a username.")
	case len(username) > maxUsernameLen:
		return "", InvalidAccount("The username is too long.")
	case strings.IndexFunc(username, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0:
		return "", InvalidAccount("The username cannot contain spaces.")
	case len(password) < MinPasswordLen:
		return "", InvalidAccount(fmt.Sprintf("The password must be at least %d characters.", MinPasswordLen))
	}
	// Refused before the hash, so a claimed install spends nothing on this public route.
	if need, err := s.NeedsFirstAccount(ctx); err != nil {
		return "", err
	} else if !need {
		return "", ErrClaimed
	}
	select {
	case s.verifying <- struct{}{}:
	default:
		return "", &Throttled{Until: s.now().Add(busyRetry)}
	}
	release := sync.OnceFunc(func() { <-s.verifying })
	defer release()
	hash, err := HashPassword(password)
	release()
	if err != nil {
		return "", err
	}
	now := s.now()
	// password_changed_at differs from created_at: Setup reads equal times on admin as "still the generated
	// password", and this one was chosen by the person.
	created, changed := store.Millis(now), store.Millis(now)+1
	var id int64
	err = s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO web_user (username, password_hash, created_at, updated_at,
			password_changed_at, role, status) SELECT ?, ?, ?, ?, ?, ?, ?
			WHERE NOT EXISTS (SELECT 1 FROM web_user)`, username, hash, created, created, changed, RoleAdmin,
			StatusActive)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrClaimed
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return "", err
	}
	s.log.Warn("created the first admin account from the web page", "username", username, "remote", addr)
	return s.StartSession(ctx, id, addr, userAgent)
}
