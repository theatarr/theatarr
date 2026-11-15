package store_test

import (
	"context"
	"testing"

	"github.com/theatarr/theatarr/internal/store"
)

// 00136: web_user is rebuilt with the invited status and the two invite columns. Accounts, their sessions and the
// rows that reference them survive, the id counter is kept, foreign keys check clean, and Down drops the invites.
func TestMigrationUserInvite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.OpenAt(t, 135)
	db := st.Writer()
	for _, q := range []string{
		`INSERT INTO web_user (id, username, password_hash, created_at, updated_at, password_changed_at, role, status,
			plex_id, discord_id) VALUES (1, 'admin', 'h', 1, 1, 2, 'admin', 'active', NULL, ''),
			(7, 'ann', '', 1, 1, 1, 'user', 'pending', 41, '123456789012345678')`,
		`DELETE FROM web_user WHERE id = 7`,
		`INSERT INTO web_user (id, username, password_hash, created_at, updated_at, password_changed_at, role, status,
			plex_id) VALUES (5, 'bea', '', 1, 1, 1, 'user', 'active', 42)`,
		`INSERT INTO web_session (token_hash, user_id, created_at, last_seen_at, expires_at) VALUES ('s', 5, 1, 1, 9)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpTo(ctx, 136); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&n); err != nil || n != 0 {
		t.Errorf("foreign key check: %d %v", n, err)
	}
	for q, want := range map[string]string{
		`SELECT group_concat(username || ':' || status, ' ') FROM (SELECT * FROM web_user ORDER BY id)`: "admin:active bea:active",
		`SELECT CAST(seq AS TEXT) FROM sqlite_sequence WHERE name = 'web_user'`:                         "7",
		`SELECT CAST(COUNT(*) AS TEXT) FROM web_session WHERE user_id = 5`:                              "1",
	} {
		var got string
		if err := db.QueryRowContext(ctx, q).Scan(&got); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", q, got, err, want)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO web_user (username, password_hash, created_at, updated_at,
		password_changed_at, role, status, invite_token_hash, invite_expires_at) VALUES ('invite-1', '', 1, 1, 1, 'user',
		'invited', 'h1', 9)`); err != nil {
		t.Fatal(err)
	}
	// Deleting an account still cascades to its sessions: the references point at the new table.
	if _, err := db.ExecContext(ctx, `DELETE FROM web_user WHERE id = 5`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM web_session`).Scan(&n); err != nil || n != 0 {
		t.Errorf("sessions after deleting their account: %d %v", n, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO web_user (username, password_hash, created_at, updated_at,
		password_changed_at, plex_id) VALUES ('dup', '', 1, 1, 1, 42), ('dup2', '', 1, 1, 1, 42)`); err == nil {
		t.Error("two accounts with one Plex id")
	}
	if err := st.DownTo(ctx, 135); err != nil {
		t.Fatal(err)
	}
	var users string
	if err := db.QueryRowContext(ctx, `SELECT group_concat(username, ' ') FROM web_user`).Scan(&users); err != nil ||
		users != "admin" {
		t.Errorf("after Down: %q %v", users, err)
	}
}
