package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// Invites (docs/design/users.md). An admin invites a person: the invite is a web_user row with status invited that
// carries the role, auto-approve, an optional display name, the SHA-256 of a single-use token and an expiry. The
// token is shown once, in the link; it is never stored, logged or listed. Opening the link, the person chooses a
// username and password or signs in with Plex: the row becomes an active account with the invited role, the token is
// cleared in the same statement that activates it, and a session starts. An expired invite stays listed until the
// admin revokes it (deletes the row). An existing username or Plex account is never taken over: the invite stays
// unused and the person is told. Attempts are throttled per client address like the sign-in.

// Invite errors.
var (
	ErrInviteUnknown = errors.New("auth: the invite link is not valid or was already used")
	ErrInviteExpired = errors.New("auth: the invite link has expired")
	ErrPlexTaken     = errors.New("auth: this Plex account already has an account")
)

// DefaultInviteTTL is an invite's life when the admin picks none.
const DefaultInviteTTL = 7 * 24 * time.Hour

// NewInvite is what an admin sets on an invite.
type NewInvite struct {
	Role        string
	AutoApprove bool
	// Name is the display name the account starts with ("" none).
	Name string
	TTL  time.Duration
}

// Invite is what the invite page shows: the name the admin gave and when the link expires.
type Invite struct {
	Name      string    `json:"name"`
	ExpiresAt time.Time `json:"expires_at"`
}

// CreateInvite adds an invited row and returns its id and the token for the link (the only time it exists in clear).
func (s *Service) CreateInvite(ctx context.Context, in NewInvite) (int64, string, error) {
	if !ValidRole(in.Role) {
		return 0, "", fmt.Errorf("role must be admin, manager or user")
	}
	in.Name = strings.TrimSpace(in.Name)
	if len(in.Name) > maxUsernameLen {
		return 0, "", fmt.Errorf("name: at most %d characters", maxUsernameLen)
	}
	if in.TTL <= 0 {
		in.TTL = DefaultInviteTTL
	}
	token, err := randomToken()
	if err != nil {
		return 0, "", err
	}
	// The row needs a unique username until it is redeemed; the person picks the real one.
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return 0, "", err
	}
	now := s.now()
	ms := store.Millis(now)
	res, err := s.st.Writer().ExecContext(ctx, `INSERT INTO web_user (username, password_hash, created_at, updated_at,
		password_changed_at, role, status, display_name, auto_approve, invite_token_hash, invite_expires_at)
		VALUES (?, '', ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "invite-"+hex.EncodeToString(b), ms, ms, ms, in.Role, StatusInvited,
		in.Name, in.AutoApprove, sha(token), store.Millis(now.Add(in.TTL)))
	if err != nil {
		return 0, "", err
	}
	id, err := res.LastInsertId()
	return id, token, err
}

// DeleteInvite revokes an invite that is not redeemed yet; anything else is ErrUserNotFound.
func (s *Service) DeleteInvite(ctx context.Context, id int64) error {
	res, err := s.st.Writer().ExecContext(ctx, `DELETE FROM web_user WHERE id = ? AND status = ?`, id, StatusInvited)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// inviteGate counts an invite attempt from addr like a failed login (Throttle.Reserve) and finds the invite behind
// token. A valid, unexpired invite clears the count. It returns the row id, ErrInviteUnknown, ErrInviteExpired or
// *Throttled.
func (s *Service) inviteGate(ctx context.Context, token, addr string) (int64, Invite, error) {
	now := s.now()
	key := "addr:" + addr
	if until, ok := s.thr.Reserve(now, key); !ok {
		return 0, Invite{}, &Throttled{Until: until}
	}
	h := sha(token)
	var id, expires int64
	var stored, name string
	err := s.st.Reader().QueryRowContext(ctx, `SELECT id, invite_token_hash, invite_expires_at, display_name
		FROM web_user WHERE status = ? AND invite_token_hash = ?`, StatusInvited, h).Scan(&id, &stored, &expires, &name)
	if errors.Is(err, sql.ErrNoRows) || err == nil && subtle.ConstantTimeCompare([]byte(stored), []byte(h)) != 1 {
		return 0, Invite{}, ErrInviteUnknown
	}
	if err != nil {
		return 0, Invite{}, err
	}
	inv := Invite{Name: name, ExpiresAt: store.FromMillis(expires)}
	if !now.Before(inv.ExpiresAt) {
		return 0, Invite{}, ErrInviteExpired
	}
	s.thr.Succeed(key)
	return id, inv, nil
}

// LookupInvite is what the invite page shows for token, or ErrInviteUnknown, ErrInviteExpired or *Throttled.
func (s *Service) LookupInvite(ctx context.Context, token, addr string) (Invite, error) {
	_, inv, err := s.inviteGate(ctx, token, addr)
	return inv, err
}

// redeem activates invite row id inside tx with the extra SET clauses and arguments, only while it is still that
// unused, unexpired invite; anything else is ErrInviteUnknown. This one statement is what makes the token single use.
func (s *Service) redeem(ctx context.Context, tx *sql.Tx, id int64, token string, set string, args ...any) error {
	now := store.Millis(s.now())
	args = append(args, now, StatusActive, id, StatusInvited, sha(token), now)
	res, err := tx.ExecContext(ctx, `UPDATE web_user SET `+set+`, updated_at = ?, status = ?,
		invite_token_hash = NULL, invite_expires_at = NULL
		WHERE id = ? AND status = ? AND invite_token_hash = ? AND invite_expires_at > ?`, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrInviteUnknown
	}
	return nil
}

// AcceptInvite redeems token with a chosen username and password and starts a session (the token returned is the
// cookie value). A username already in use is refused (InvalidAccount) and the invite stays unused.
func (s *Service) AcceptInvite(ctx context.Context, token, username, password, addr, userAgent string) (string,
	error) {
	username, err := checkNewAccount(username, password)
	if err != nil {
		return "", err
	}
	id, _, err := s.inviteGate(ctx, token, addr)
	if err != nil {
		return "", err
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
	// password_changed_at after created_at: the person chose this password (see CreateFirstAdmin).
	changed := store.Millis(s.now()) + 1
	err = s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var taken bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM web_user WHERE lower(username) = lower(?)
			AND id != ?)`, username, id).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return InvalidAccount("That username is taken. Choose another.")
		}
		return s.redeem(ctx, tx, id, token, `username = ?, password_hash = ?, password_changed_at = ?`, username, hash,
			changed)
	})
	if err != nil {
		return "", err
	}
	s.log.Info("invite accepted", "user", id, "username", username, "via", "password", "remote", addr)
	return s.StartSession(ctx, id, addr, userAgent)
}

// AcceptInvitePlex redeems token for a Plex account that has proven itself (and has access to our server) and
// returns the account id; the caller starts the session. A Plex account that already has an account here is
// ErrPlexTaken, and the invite stays unused.
func (s *Service) AcceptInvitePlex(ctx context.Context, token string, a PlexAccount, addr string) (int64, error) {
	if a.ID <= 0 {
		return 0, errors.New("auth: Plex account without an id")
	}
	id, _, err := s.inviteGate(ctx, token, addr)
	if err != nil {
		return 0, err
	}
	display := strings.TrimSpace(a.Title)
	if display == "" {
		display = a.Username
	}
	err = s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var taken bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM web_user WHERE plex_id = ?)`, a.ID).
			Scan(&taken); err != nil {
			return err
		}
		if taken {
			return ErrPlexTaken
		}
		username, err := freeUsername(ctx, tx, a)
		if err != nil {
			return err
		}
		return s.redeem(ctx, tx, id, token, `username = ?, plex_id = ?, plex_username = ?, email = ?, avatar_url = ?,
			display_name = CASE WHEN display_name = '' THEN ? ELSE display_name END`, username, a.ID, a.Username,
			a.Email, a.Thumb, display)
	})
	if err != nil {
		return 0, err
	}
	s.log.Info("invite accepted", "user", id, "plex_id", a.ID, "via", "plex", "remote", addr)
	return id, nil
}
