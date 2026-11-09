package auth

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"

	"github.com/theatarr/theatarr/internal/store"
)

// Migration 00026 keeps existing accounts admins, makes key names unique per owner, and goes down and up cleanly.
func TestMigration26(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "theatarr.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sub, err := fs.Sub(os.DirFS("../store"), "migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(database.DialectSQLite3, st.Writer(), sub, goose.WithDisableGlobalRegistry(true),
		goose.WithTableName(store.MigrationTable), goose.WithAllowOutofOrder(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 25); err != nil {
		t.Fatal(err)
	}
	w := st.Writer()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := w.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO web_user (id, username, password_hash, created_at, updated_at, password_changed_at)
		VALUES (1, 'admin', 'x', 0, 0, 0)`)
	exec(`INSERT INTO api_key (name, prefix, key_hash, created_by, created_at) VALUES ('seerr', 'theatarr_a', 'h1', 1, 0)`)
	for range 2 {
		if _, err := p.UpTo(ctx, 26); err != nil {
			t.Fatal(err)
		}
		var role, status string
		if err := w.QueryRowContext(ctx, `SELECT role, status FROM web_user WHERE id = 1`).Scan(&role, &status); err != nil ||
			role != RoleAdmin || status != StatusActive {
			t.Fatalf("existing account: %q %q %v", role, status, err)
		}
		exec(`INSERT INTO web_user (id, username, password_hash, created_at, updated_at, password_changed_at, role,
			plex_id) VALUES (2, 'ann', '', 0, 0, 0, 'user', 7)`)
		// Another owner may use the same key name; the same owner may not.
		exec(`INSERT INTO api_key (name, prefix, key_hash, created_by, created_at) VALUES ('seerr', 'theatarr_b', 'h2', 2, 0)`)
		if _, err := w.ExecContext(ctx, `INSERT INTO api_key (name, prefix, key_hash, created_by, created_at)
			VALUES ('seerr', 'theatarr_c', 'h3', 2, 0)`); err == nil {
			t.Error("duplicate key name for one owner")
		}
		if _, err := w.ExecContext(ctx, `UPDATE web_user SET role = 'owner' WHERE id = 2`); err == nil {
			t.Error("role CHECK missing")
		}
		exec(`INSERT INTO request (user_id, kind, tmdb_id, name, status, created_at, updated_at)
			VALUES (2, 'movie', 105, 'Epsilon', 'pending', 0, 0)`)
		exec(`INSERT INTO request_user (request_id, user_id, created_at) VALUES (1, 1, 0)`)
		if _, err := w.ExecContext(ctx, `UPDATE request SET status = 'lost'`); err == nil {
			t.Error("request status CHECK missing")
		}
		exec(`DELETE FROM api_key WHERE created_by = 2`)
		exec(`DELETE FROM web_user WHERE id = 2`)
		if _, err := p.DownTo(ctx, 25); err != nil {
			t.Fatal(err)
		}
		if _, err := w.ExecContext(ctx, `SELECT role FROM web_user`); err == nil {
			t.Error("down: role still there")
		}
		if _, err := w.ExecContext(ctx, `SELECT 1 FROM request`); err == nil {
			t.Error("down: request still there")
		}
	}
}
