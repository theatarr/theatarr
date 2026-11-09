package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// KeyInfo describes an API key. The key itself is never stored or listed.
type KeyInfo struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Prefix    string `json:"prefix"`
	CreatedBy string `json:"created_by,omitempty"`
	// OwnerID is the owning account (the creator); the key carries its permissions.
	OwnerID    int64      `json:"owner_id,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// Errors from key management.
var (
	ErrKeyName     = errors.New("auth: key name must be 1-64 characters of letters, digits, '-', '_' or '.'")
	ErrKeyExists   = errors.New("auth: you have an active key with that name")
	ErrKeyNotFound = errors.New("auth: no active key with that id")
)

func validKeyName(n string) bool {
	if n == "" || len(n) > 64 {
		return false
	}
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.", r)) {
			return false
		}
	}
	return true
}

// CreateKey makes a new API key for a client called name and returns it. The returned key is the only copy.
func (s *Service) CreateKey(ctx context.Context, name string, by int64) (string, KeyInfo, error) {
	if !validKeyName(name) {
		return "", KeyInfo{}, ErrKeyName
	}
	tok, err := randomToken()
	if err != nil {
		return "", KeyInfo{}, err
	}
	key := "tha_" + tok
	now := s.now()
	info := KeyInfo{Name: name, Prefix: key[:10], CreatedAt: now.UTC(), OwnerID: by}
	var creator any
	if by > 0 {
		creator = by
	}
	res, err := s.st.Writer().ExecContext(ctx, `INSERT INTO api_key (name, prefix, key_hash, created_by, created_at)
		VALUES (?, ?, ?, ?, ?)`, name, info.Prefix, sha(key), creator, store.Millis(now))
	if store.IsUniqueViolation(err) {
		return "", KeyInfo{}, ErrKeyExists
	}
	if err != nil {
		return "", KeyInfo{}, err
	}
	info.ID, _ = res.LastInsertId()
	s.log.Info("api key created", "name", name, "id", info.ID)
	return key, info, nil
}

// CreateUserKey is CreateKey for the account called username, with only the store (`theatarr admin create-key`).
func CreateUserKey(ctx context.Context, st *store.Store, username, name string) (string, KeyInfo, error) {
	var id int64
	err := st.Reader().QueryRowContext(ctx, `SELECT id FROM web_user WHERE username = ?`, username).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", KeyInfo{}, ErrUserNotFound
	}
	if err != nil {
		return "", KeyInfo{}, err
	}
	s := &Service{st: st, log: slog.Default().With("component", "auth"), now: time.Now}
	return s.CreateKey(ctx, name, id)
}

// ListKeys lists keys, newest first, revoked ones included: every key when owner is 0, else that account's.
func (s *Service) ListKeys(ctx context.Context, owner int64) ([]KeyInfo, error) {
	q := `SELECT k.id, k.name, k.prefix, COALESCE(u.username, ''), COALESCE(k.created_by, 0), k.created_at,
		k.last_used_at, k.revoked_at FROM api_key k LEFT JOIN web_user u ON u.id = k.created_by`
	var args []any
	if owner > 0 {
		q += ` WHERE k.created_by = ?`
		args = append(args, owner)
	}
	rows, err := s.st.Reader().QueryContext(ctx, q+` ORDER BY k.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KeyInfo{}
	for rows.Next() {
		var k KeyInfo
		var created int64
		var used, revoked sql.NullInt64
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.CreatedBy, &k.OwnerID, &created, &used, &revoked); err != nil {
			return nil, err
		}
		k.CreatedAt = store.FromMillis(created)
		if used.Valid {
			t := store.FromMillis(used.Int64)
			k.LastUsedAt = &t
		}
		if revoked.Valid {
			t := store.FromMillis(revoked.Int64)
			k.RevokedAt = &t
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// RevokeKey revokes an active key: any key when owner is 0, else only that account's. It takes effect on the next
// request.
func (s *Service) RevokeKey(ctx context.Context, id, owner int64) error {
	q := `UPDATE api_key SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`
	args := []any{store.Millis(s.now()), id}
	if owner > 0 {
		q += ` AND created_by = ?`
		args = append(args, owner)
	}
	res, err := s.st.Writer().ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrKeyNotFound
	}
	s.log.Info("api key revoked", "id", id)
	return nil
}

// String masks a key for messages: its prefix only.
func (k KeyInfo) String() string { return fmt.Sprintf("%s (%s…)", k.Name, k.Prefix) }
