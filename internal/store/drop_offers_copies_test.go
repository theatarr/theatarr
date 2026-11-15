package store_test

import (
	"context"
	"testing"

	"github.com/theatarr/theatarr/internal/store"
)

// 00139: provider copies and offers go. A copy past its add gets its History event (ended by the upgrade); a queued
// one ends with nothing; the finished copies' events stay. router_offer and provider_copy are dropped, queued offer
// and copy jobs go, copy_to_providers leaves the stored router section, and the foreign keys check clean.
func TestMigrationDropOffersCopies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.OpenAt(t, 138)
	db := st.Writer()
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, monitored, created_at, updated_at) VALUES (1, 'movie', 'Alpha', 1, 0, 0)`,
		`INSERT INTO provider_copy (info_hash, name, title_id, provider, torrent_id, own_add, state, message, created_at,
			updated_at) VALUES ('aa', 'Alpha.2020', 1, 'tb', '7', 1, 'checking', '', 0, 5),
			('bb', 'Alpha.2020', 1, 'ad', NULL, 0, 'queued', '', 0, 6),
			('cc', 'Alpha.2020', 1, 'rd', '9', 0, 'added', 'Added to RD', 0, 7)`,
		`INSERT INTO provider_copy_event (at, info_hash, name, title_id, provider, state, message)
			VALUES (7, 'cc', 'Alpha.2020', 1, 'rd', 'added', 'Added to RD')`,
		`INSERT INTO router_offer (version_key, title_id, info_hash, from_version, outcome, first_at, updated_at)
			VALUES ('movie-web', 1, 'aa', 'movie-remux', 'declined', 0, 0)`,
		`INSERT INTO job (kind, payload, state, run_at, created_at, updated_at) VALUES
			('router.offer', '{}', 'queued', 0, 0, 0), ('router.copy', '{}', 'running', 0, 0, 0),
			('router.offer', '{}', 'done', 0, 0, 0), ('router.route', '{}', 'queued', 0, 0, 0)`,
		`INSERT INTO setting_section (key, config, created_at, updated_at) VALUES
			('router', '{"enabled":true,"copy_to_providers":true,"cached_only":true}', 0, 3)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := st.UpTo(ctx, 139); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]string{
		`SELECT group_concat(info_hash || ' ' || provider || ' ' || state || ' ' || at || ' ' || title_id, '; ')
			FROM (SELECT * FROM provider_copy_event ORDER BY id)`: "cc rd added 7 1; aa tb added 5 1",
		`SELECT message FROM provider_copy_event WHERE info_hash = 'aa'`: "Added to TB; the copy was ended by an upgrade " +
			"before it settled, its torrent stays on the account",
		`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('provider_copy', 'router_offer')`: "0",
		`SELECT group_concat(kind || ' ' || state, ', ') FROM (SELECT * FROM job ORDER BY id)`: "router.offer done, " +
			"router.route queued",
		`SELECT config || ' ' || updated_at FROM setting_section WHERE key = 'router'`: `{"enabled":true,"cached_only":true} 3`,
		`SELECT COUNT(*) FROM pragma_foreign_key_check`:                                "0",
	} {
		var got string
		if err := db.QueryRowContext(ctx, q).Scan(&got); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", q, got, err, want)
		}
	}
}
