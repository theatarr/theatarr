package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// User is an account as the users screen shows it. Nothing secret: no password hash, no Plex token (none is kept).
type User struct {
	ID          int64    `json:"id"`
	Username    string   `json:"username"`
	DisplayName string   `json:"display_name"`
	Email       string   `json:"email,omitempty"`
	AvatarURL   string   `json:"avatar_url,omitempty"`
	Role        string   `json:"role"`
	Status      string   `json:"status"`
	Local       bool     `json:"local"`
	PlexID      *int64   `json:"plex_id,omitempty"`
	PlexName    string   `json:"plex_username,omitempty"`
	ExtraPerms  []string `json:"extra_permissions"`
	Permissions []string `json:"permissions"`
	AutoApprove bool     `json:"auto_approve"`
	// RequestVersions: the version keys this user's requests ask for; nil is every version that fits the title.
	RequestVersions []string `json:"request_versions"`
	// DiscordID is the account's Discord user id (digits; "" none): request-ready messages to a Discord target
	// @mention it.
	DiscordID   string     `json:"discord_id"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	DisabledAt  *time.Time `json:"disabled_at,omitempty"`
	Sessions    int        `json:"sessions"`
	Keys        int        `json:"keys"`
}

// Errors from user management.
var (
	ErrUserNotFound = errors.New("auth: no such user")
	ErrLastAdmin    = errors.New("auth: the last active admin cannot lose the role or be disabled")
	ErrNotLocal     = errors.New("auth: this account signs in with Plex and has no password")
	ErrWrongPass    = errors.New("auth: the current password is wrong")
)

const userSelect = `SELECT u.id, u.username, u.display_name, u.email, u.avatar_url, u.role, u.status,
	u.password_hash != '', u.plex_id, u.plex_username, u.permissions, u.auto_approve, u.request_versions, u.created_at,
	u.last_login_at, u.disabled_at, u.discord_id,
	(SELECT COUNT(*) FROM web_session s WHERE s.user_id = u.id),
	(SELECT COUNT(*) FROM api_key k WHERE k.created_by = u.id AND k.revoked_at IS NULL)
	FROM web_user u`

func scanUserRow(sc interface{ Scan(...any) error }) (User, error) {
	var u User
	var plexID, login, disabled sql.NullInt64
	var extra, created int64
	var rv sql.NullString
	err := sc.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.AvatarURL, &u.Role, &u.Status, &u.Local, &plexID,
		&u.PlexName, &extra, &u.AutoApprove, &rv, &created, &login, &disabled, &u.DiscordID, &u.Sessions, &u.Keys)
	if err != nil {
		return u, err
	}
	if plexID.Valid {
		u.PlexID = &plexID.Int64
	}
	u.CreatedAt = store.FromMillis(created)
	if login.Valid {
		t := store.FromMillis(login.Int64)
		u.LastLoginAt = &t
	}
	if disabled.Valid {
		t := store.FromMillis(disabled.Int64)
		u.DisabledAt = &t
	}
	if rv.Valid && rv.String != "" {
		if err := json.Unmarshal([]byte(rv.String), &u.RequestVersions); err != nil {
			return u, fmt.Errorf("user %d request_versions: %w", u.ID, err)
		}
	}
	u.ExtraPerms = (Perm(extra) & OptionalUserPerms).Names()
	u.Permissions = Effective(u.Role, Perm(extra), u.AutoApprove).Names()
	return u, nil
}

// ListUsers lists every account: pending first, then by name.
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.st.Reader().QueryContext(ctx, userSelect+` ORDER BY u.status != 'pending', lower(u.username)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUserRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// GetUser reads one account.
func (s *Service) GetUser(ctx context.Context, id int64) (*User, error) {
	u, err := scanUserRow(s.st.Reader().QueryRowContext(ctx, userSelect+` WHERE u.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// UserPatch is a change to an account; nil fields stay as they are.
type UserPatch struct {
	Role        *string
	Status      *string // active, pending or disabled
	ExtraPerms  *Perm
	AutoApprove *bool
	DisplayName *string
	// RequestVersions: a pointer to nil resets to every version that fits.
	RequestVersions *[]string
	// DiscordID: digits (a Discord user id), or "" for none.
	DiscordID *string
}

// UpdateUser applies p. Disabling ends the account's sessions; its keys stop working while it is disabled. The last
// active admin keeps the role and stays active.
func (s *Service) UpdateUser(ctx context.Context, id int64, p UserPatch) error {
	if p.Role != nil && !ValidRole(*p.Role) {
		return fmt.Errorf("role must be admin, manager or user")
	}
	if p.Status != nil && *p.Status != StatusActive && *p.Status != StatusPending && *p.Status != StatusDisabled {
		return fmt.Errorf("status must be active, pending or disabled")
	}
	if p.DiscordID != nil && !ValidDiscordID(strings.TrimSpace(*p.DiscordID)) {
		return fmt.Errorf("discord_id must be a Discord user id (17 to 20 digits) or empty")
	}
	now := store.Millis(s.now())
	return s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var role, status string
		err := tx.QueryRowContext(ctx, `SELECT role, status FROM web_user WHERE id = ?`, id).Scan(&role, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUserNotFound
		}
		if err != nil {
			return err
		}
		losesAdmin := role == RoleAdmin && status == StatusActive &&
			((p.Role != nil && *p.Role != RoleAdmin) || (p.Status != nil && *p.Status != StatusActive))
		if losesAdmin {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM web_user WHERE role = 'admin' AND status = 'active'
				AND id != ?`, id).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return ErrLastAdmin
			}
		}
		set := []string{"updated_at = ?"}
		args := []any{now}
		if p.Role != nil {
			set, args = append(set, "role = ?"), append(args, *p.Role)
		}
		if p.Status != nil && *p.Status != status {
			set, args = append(set, "status = ?"), append(args, *p.Status)
			if *p.Status == StatusDisabled {
				set, args = append(set, "disabled_at = ?"), append(args, now)
			} else {
				set = append(set, "disabled_at = NULL")
			}
		}
		if p.ExtraPerms != nil {
			set, args = append(set, "permissions = ?"), append(args, int64(*p.ExtraPerms&OptionalUserPerms))
		}
		if p.AutoApprove != nil {
			set, args = append(set, "auto_approve = ?"), append(args, *p.AutoApprove)
		}
		if p.DisplayName != nil {
			set, args = append(set, "display_name = ?"), append(args, strings.TrimSpace(*p.DisplayName))
		}
		if p.DiscordID != nil {
			set, args = append(set, "discord_id = ?"), append(args, strings.TrimSpace(*p.DiscordID))
		}
		if p.RequestVersions != nil {
			if *p.RequestVersions == nil {
				set = append(set, "request_versions = NULL")
			} else {
				b, _ := json.Marshal(*p.RequestVersions)
				set, args = append(set, "request_versions = ?"), append(args, string(b))
			}
		}
		args = append(args, id)
		if _, err := tx.ExecContext(ctx, `UPDATE web_user SET `+strings.Join(set, ", ")+` WHERE id = ?`, args...); err != nil {
			return err
		}
		if p.Status != nil && *p.Status != StatusActive {
			if _, err := tx.ExecContext(ctx, `DELETE FROM web_session WHERE user_id = ?`, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// ValidDiscordID reports whether v is "" or a Discord user id (a snowflake: 17 to 20 digits).
func ValidDiscordID(v string) bool {
	if v == "" {
		return true
	}
	if len(v) < 17 || len(v) > 20 {
		return false
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// EndSessions signs an account out everywhere.
func (s *Service) EndSessions(ctx context.Context, userID int64) (int64, error) {
	res, err := s.st.Writer().ExecContext(ctx, `DELETE FROM web_session WHERE user_id = ?`, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ChangePassword sets a local account's own password after checking the current one, and ends its other sessions
// (keep is the session token to keep, or "").
func (s *Service) ChangePassword(ctx context.Context, userID int64, current, next, keep string) error {
	if len(next) < MinPasswordLen {
		return fmt.Errorf("password too short: at least %d characters", MinPasswordLen)
	}
	var hash string
	if err := s.st.Reader().QueryRowContext(ctx, `SELECT password_hash FROM web_user WHERE id = ?`, userID).
		Scan(&hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUserNotFound
		}
		return err
	}
	if hash == "" {
		return ErrNotLocal
	}
	// The check takes a slot like a login (maxVerifying); a signed-in user waits for one instead of being refused.
	select {
	case s.verifying <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	ok, _ := VerifyPassword(hash, current)
	<-s.verifying
	if !ok {
		return ErrWrongPass
	}
	nh, err := HashPassword(next)
	if err != nil {
		return err
	}
	now := store.Millis(s.now())
	return s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE web_user SET password_hash = ?, password_changed_at = ?, updated_at = ?
			WHERE id = ?`, nh, now, now, userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM web_session WHERE user_id = ? AND token_hash != ?`, userID, sha(keep))
		return err
	})
}

// PlexAccount is what plex.tv says about a signed-in account. The token that proved it is not part of it.
type PlexAccount struct {
	ID       int64
	UUID     string
	Username string
	Title    string // the friendly name
	Email    string
	Thumb    string
}

// Settings the web layer keeps in web_setting.
const (
	// SettingPlexNewUsers is what a first Plex sign-in gets: "pending" (an admin enables it) or "active".
	SettingPlexNewUsers = "plex_new_users"
	// SettingPlexClientID is this install's X-Plex-Client-Identifier.
	SettingPlexClientID = "plex_client_id"
)

// Setting reads a web_setting value; def when unset.
func (s *Service) Setting(ctx context.Context, name, def string) (string, error) {
	var v string
	err := s.st.Reader().QueryRowContext(ctx, `SELECT value FROM web_setting WHERE name = ?`, name).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return def, nil
	}
	return v, err
}

// SetSetting writes a web_setting value.
func (s *Service) SetSetting(ctx context.Context, name, value string) error {
	now := store.Millis(s.now())
	_, err := s.st.Writer().ExecContext(ctx, `INSERT INTO web_setting (name, value, created_at, updated_at)
		VALUES (?, ?, ?, ?) ON CONFLICT (name) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		name, value, now, now)
	return err
}

// PlexSignIn finds or creates the account of a Plex user who has access to our server. A new account gets the
// user role and the status the plex_new_users setting says (pending unless set to active). The profile (name, email,
// avatar) is refreshed on every sign-in. While no account exists it creates none and returns ErrNoAdminYet. It
// returns the account's id and status; only an active account may get a session.
func (s *Service) PlexSignIn(ctx context.Context, a PlexAccount) (int64, string, bool, error) {
	if a.ID <= 0 {
		return 0, "", false, errors.New("auth: Plex account without an id")
	}
	newStatus, err := s.Setting(ctx, SettingPlexNewUsers, StatusPending)
	if err != nil {
		return 0, "", false, err
	}
	if newStatus != StatusActive {
		newStatus = StatusPending
	}
	display := strings.TrimSpace(a.Title)
	if display == "" {
		display = a.Username
	}
	now := store.Millis(s.now())
	var id int64
	var status string
	created := false
	err = s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT id, status FROM web_user WHERE plex_id = ?`, a.ID).Scan(&id, &status)
		switch {
		case err == nil:
			_, err = tx.ExecContext(ctx, `UPDATE web_user SET plex_username = ?, email = ?, avatar_url = ?,
				display_name = CASE WHEN display_name = '' THEN ? ELSE display_name END, updated_at = ? WHERE id = ?`,
				a.Username, a.Email, a.Thumb, display, now, id)
			return err
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		// The first account is the admin, made on the create page; Plex cannot make it.
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM web_user)`).Scan(&exists); err != nil {
			return err
		} else if !exists {
			return ErrNoAdminYet
		}
		username, err := freeUsername(ctx, tx, a)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO web_user (username, password_hash, created_at, updated_at,
			password_changed_at, role, status, display_name, email, avatar_url, plex_id, plex_username)
			VALUES (?, '', ?, ?, ?, 'user', ?, ?, ?, ?, ?, ?)`, username, now, now, now, newStatus, display, a.Email,
			a.Thumb, a.ID, a.Username)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		status, created = newStatus, true
		return nil
	})
	if err != nil {
		return 0, "", false, err
	}
	if created {
		s.log.Info("plex account created", "user", id, "plex_id", a.ID, "status", status)
	}
	return id, status, created, nil
}

// freeUsername picks a unique username for a new Plex account: its Plex username, else "plex-<id>", with the Plex id
// appended when the name is taken (the local admin keeps "admin").
func freeUsername(ctx context.Context, tx *sql.Tx, a PlexAccount) (string, error) {
	base := strings.TrimSpace(a.Username)
	if base == "" {
		base = "plex-" + strconv.FormatInt(a.ID, 10)
	}
	for _, name := range []string{base, base + "-" + strconv.FormatInt(a.ID, 10)} {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM web_user WHERE lower(username) = lower(?)`, name).
			Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return name, nil
		}
	}
	return "", fmt.Errorf("auth: no free username for Plex account %d", a.ID)
}
