package store

import (
	"context"
	"testing"
)

func TestOfferSourcesMigration(t *testing.T) {
	t.Parallel()
	st := openAt(t, 64)
	ctx := context.Background()
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 63); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'Movie', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'source', 0, 0)`,
		`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
			created_at, updated_at) VALUES (1, 1, 1, 'source', 'abc', 'Movie', 'public', 'imported', 0, 0)`,
		`INSERT INTO router_offer (version_key, title_id, info_hash, from_version, outcome, first_at, updated_at)
			VALUES ('target', 1, 'abc', 'source', 'gave_up', 0, 1000)`,
	} {
		if _, err := st.Writer().ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.UpTo(ctx, 64); err != nil {
		t.Fatal(err)
	}
	var kind string
	var source, retries, next int64
	if err := st.Reader().QueryRowContext(ctx, `SELECT source_kind, source_id, retry_attempts, next_at
		FROM router_offer`).Scan(&kind, &source, &retries, &next); err != nil {
		t.Fatal(err)
	}
	if kind != "download" || source != 1 || retries != 0 || next != 86401000 {
		t.Fatalf("migrated offer: %s %d %d %d", kind, source, retries, next)
	}
	if _, err := p.DownTo(ctx, 63); err != nil {
		t.Fatal(err)
	}
	var final bool
	if err := st.Reader().QueryRowContext(ctx, `SELECT next_at IS NULL FROM router_offer`).Scan(&final); err != nil || !final {
		t.Fatalf("down migration: final %t, %v", final, err)
	}
	if _, err := p.UpTo(ctx, 64); err != nil {
		t.Fatal(err)
	}
}
