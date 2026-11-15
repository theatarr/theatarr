package store_test

import (
	"context"
	"testing"

	"github.com/theatarr/theatarr/internal/store"
)

// 00131: a database at 130 with ceiling rows upgrades cleanly: the five tables and every ceiling.compute job go, the
// title, its version and other jobs stay, and the foreign keys check clean.
func TestMigrationDropCeilings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.OpenAt(t, 130)
	db := st.Writer()
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'Alpha', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (1, 1, 'movie', 'filled', 0, 0)`,
		`INSERT INTO source_ceiling (title_id, season, tier, status) VALUES (1, -1, 'web_2160', 'known')`,
		`INSERT INTO source_sighting (title_id, season, tier, source, source_kind, positive_only, first_seen_at, last_seen_at)
			VALUES (1, -1, 'web_2160', 'search:pub', 'torznab', 0, 1, 1)`,
		`INSERT INTO ceiling_search (id, title_id, season, version_key, searched_at, source) VALUES (1, 1, -1, 'movie', 1, 'search')`,
		`INSERT INTO ceiling_search_indexer (search_id, indexer, source_kind, positive_only, complete) VALUES (1, 'pub', 'torznab', 0, 1)`,
		`INSERT INTO version_ceiling (version_id, season, effective_cutoff, ceiling, computed_at) VALUES (1, -1, 'WEB 2160p', '{}', 1)`,
		`INSERT INTO job (kind, payload, state, run_at, created_at, updated_at, finished_at) VALUES
			('ceiling.compute', '{}', 'done', 0, 0, 0, 1), ('ceiling.compute', '{"title_id":1}', 'queued', 0, 0, 0, NULL),
			('db.nightly', '{}', 'queued', 0, 0, 0, NULL)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := st.UpTo(ctx, 131); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]string{
		`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('source_ceiling', 'source_sighting', 'ceiling_search',
			'ceiling_search_indexer', 'version_ceiling', 'source_ceiling_next')`: "0",
		`SELECT GROUP_CONCAT(kind) FROM job`:                                        "db.nightly",
		`SELECT name || ' ' || (SELECT state FROM version WHERE id = 1) FROM title`: "Alpha filled",
		`SELECT COUNT(*) FROM pragma_foreign_key_check`:                             "0",
	} {
		var got string
		if err := db.QueryRowContext(ctx, q).Scan(&got); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", q, got, err, want)
		}
	}
}
