package store

import (
	"context"
	"strings"
	"testing"
)

// 00069: a routed offer whose route made no download of the target (the router gate was shut,
// the route only a plan) becomes planned, so it is offered again once grabbing is on; one with a download stays.
func TestOfferGateRetryMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openAt(t, 69)
	p, err := s.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 68); err != nil {
		t.Fatal(err)
	}
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'Movie', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (10, 1, 'web', 0, 0)`,
		`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state, created_at,
			updated_at) VALUES (1, 10, 'web', '` + strings.ToUpper(b) + `', 'x', 'dmm', 'imported', 0, 0)`,
		`INSERT INTO router_offer (version_key, title_id, info_hash, from_version, release_title, outcome, reasons,
			attempts, first_at, updated_at) VALUES ('web', 1, '` + a + `', 'remux', 'x', 'routed', '[]', 1, 0, 0),
			('web', 1, '` + b + `', 'remux', 'x', 'routed', '[]', 1, 0, 0),
			('extra', 1, '` + a + `', 'remux', 'x', 'declined', '["private_indexer"]', 1, 0, 0)`,
	} {
		if _, err := s.Writer().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := p.UpTo(ctx, 69); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Reader().QueryContext(ctx, `SELECT version_key || '/' || substr(info_hash, 1, 1) || '=' || outcome
		FROM router_offer ORDER BY version_key, info_hash`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	if strings.Join(got, " ") != "extra/a=declined web/a=planned web/b=routed" {
		t.Errorf("offers %v", got)
	}
}
