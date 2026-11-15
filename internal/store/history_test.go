package store

import (
	"context"
	"testing"
)

// 00140 creates history and backfills it once from the sources (a grab, its failure and the blocklist entry here);
// it rolls back cleanly and applies again. The rows themselves are compared with the derived History in
// internal/journal (TestStoredHistoryMatchesDemo).
func TestHistoryMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := Open(ctx, migratedPath(t, 138), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO title (id, kind, name, created_at, updated_at)
		VALUES (1, 'movie', 'M', 0, 0);
		INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'movie-remux', 0, 0);
		INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state, error,
			created_at, updated_at, added_at) VALUES (7, 1, 1, 'movie-remux', 'h7', 'M.2020.Remux', 'x', 'failed',
			'stalled at 0%', 1, 5, 2);
		INSERT INTO router_blocklist (version_key, info_hash, title_id, release_title, reason, source, download_id,
			created_at) VALUES ('movie-remux', 'h7', 1, 'M.2020.Remux', 'stalled', 'stalled', 7, 6)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := p.UpTo(ctx, 140); err != nil {
			t.Fatal(err)
		}
		var rows string
		if err := st.Reader().QueryRowContext(ctx, `SELECT group_concat(ref || ' ' || at || ' ' || event || ' ' || kind,
			', ') FROM (SELECT * FROM history ORDER BY ref)`).Scan(&rows); err != nil ||
			rows != "bl:1 6 blocked blocklist, dl:7 2 grabbed grab, dl:7:fail 5 failed stall" {
			t.Fatalf("backfilled %q %v", rows, err)
		}
		if _, err := p.DownTo(ctx, 138); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'history'`).
			Scan(&n); err != nil || n != 0 {
			t.Fatalf("table left %d: %v", n, err)
		}
	}
}
