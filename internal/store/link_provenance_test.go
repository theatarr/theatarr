package store

import (
	"context"
	"testing"
)

// 00077 backfills link.adopted_from from the latest applied link action of each link without a download, and only
// when that action is an adoption; it rolls back cleanly.
func TestLinkProvenanceMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := Open(ctx, migratedPath(t, 76), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 76); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'M', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'v', 0, 0)`,
		`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state, created_at, updated_at)
			VALUES (9, 1, 1, 'v', 'h', 'r', 'i', 'imported', 0, 0)`,
		`INSERT INTO link (id, path, target, title_id, download_id, created_at, updated_at) VALUES
			(1, '/a', '/t/a', 1, NULL, 0, 0), (2, '/b', '/t/b', 1, NULL, 0, 0), (3, '/c', '/t/c', 1, 9, 0, 0),
			(4, '/d', '/t/d', 1, NULL, 0, 0), (5, '/e', '/t/e', 1, NULL, 0, 0)`,
		`INSERT INTO library_action (at, kind, mode, link_id, detail) VALUES
			(0, 'link', 'applied', 1, 'adopted from radarr /arr/a b.mkv'),
			(0, 'link', 'applied', 2, 'adopted from radarr /arr/b.mkv'),
			(1, 'link', 'applied', 2, 'adopted from radarr /arr/b2.mkv (refresh: replaces /b -> /t/old)'),
			(0, 'link', 'applied', 3, 'adopted from radarr /arr/c.mkv'),
			(0, 'link', 'applied', 4, 'adopted from radarr /arr/d.mkv'),
			(1, 'link', 'applied', 4, ''),
			(0, 'link', 'dry_run', 5, 'adopted from radarr /arr/e.mkv')`,
	} {
		if _, err := st.Writer().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := p.UpTo(ctx, 77); err != nil {
			t.Fatal(err)
		}
		want := map[int64]string{1: "radarr /arr/a b.mkv", 2: "radarr /arr/b2.mkv", 3: "-", 4: "-", 5: "-"}
		for id, w := range want {
			var got string
			if err := st.Reader().QueryRowContext(ctx, `SELECT COALESCE(adopted_from, '-') FROM link WHERE id = ?`, id).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != w {
				t.Errorf("link %d adopted_from %q, want %q", id, got, w)
			}
		}
		if _, err := p.DownTo(ctx, 76); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Writer().ExecContext(ctx, `SELECT adopted_from FROM link`); err == nil {
			t.Fatal("adopted_from left after rollback")
		}
	}
}
