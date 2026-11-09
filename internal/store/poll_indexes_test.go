package store

import (
	"context"
	"strings"
	"testing"
)

func TestPollingIndexes(t *testing.T) {
	t.Parallel()
	s := openTest(t, Options{})
	for _, tc := range []struct{ query, index string }{
		{`SELECT MAX(updated_at) FROM download`, "download_updated"},
		{`SELECT download_id FROM library_import WHERE updated_at >= 100`, "library_import_updated"},
		{`SELECT id FROM job WHERE updated_at > 100 ORDER BY updated_at, id LIMIT 50`, "job_updated"},
		{`SELECT title_id FROM title_meta WHERE refreshed_at > 100 ORDER BY refreshed_at LIMIT 500`, "title_meta_refreshed"},
		{`SELECT title_id FROM version WHERE updated_at > 100`, "version_updated"},
		{`SELECT version_id, target, MAX(created_at) FROM acquire_run WHERE infra = 0 GROUP BY version_id, target`, "COVERING INDEX acquire_run_target"},
		{`SELECT version_id, target, COALESCE(MAX(CASE WHEN infra = 0 THEN created_at END), 0), COALESCE(MAX(CASE WHEN infra = 2 THEN created_at END), 0), MAX(created_at) FROM acquire_run GROUP BY version_id, target`, "COVERING INDEX acquire_run_target"},
	} {
		rows, err := s.Reader().QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+tc.query)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan.WriteString(detail + "\n")
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if !strings.Contains(plan.String(), tc.index) || strings.Contains(plan.String(), "TEMP B-TREE") {
			t.Errorf("%s: want %s without sorting, got %s", tc.query, tc.index, plan.String())
		}
	}
}

func TestReadIndexesMigrationRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := Open(ctx, migratedPath(t, 87), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := s.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 87); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 84); err != nil {
		t.Fatal(err)
	}
	var definition string
	if err := s.Reader().QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'acquire_run_target'`).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(definition, "infra") {
		t.Fatalf("rollback kept infra in the index: %s", definition)
	}
	if _, err := p.UpTo(ctx, 87); err != nil {
		t.Fatal(err)
	}
}

// TestAcquirePartialBackfill (00100): runs recorded as infra = 1 for a partial answer (routed, or failed
// with "search incomplete") become 2; those with no answer stay 1.
func TestAcquirePartialBackfill(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := Open(ctx, migratedPath(t, 99), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := s.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 99); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'M', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (1, 1, 'movie', 'wanted', 0, 0)`,
		`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, state, message, infra, created_at, updated_at) VALUES
			(1, 'wanted', 1, 1, 'movie', 'movie', 'routed', '1 of 2 results accepted; not counted as searched: retried soon', 1, 0, 0),
			(2, 'wanted', 1, 1, 'movie', 'movie', 'failed', 'search incomplete (dmm: partial answer); not counted as searched: retried soon', 1, 0, 0),
			(3, 'wanted', 1, 1, 'movie', 'movie', 'failed', 'search failed: no indexer answered (x)', 1, 0, 0),
			(4, 'wanted', 1, 1, 'movie', 'movie', 'failed', 'search failed: acquire: vpn required', 1, 0, 0),
			(5, 'wanted', 1, 1, 'movie', 'movie', 'nothing', 'no accepted release among 0 results', 0, 0, 0)`,
	} {
		if _, err := s.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.UpTo(ctx, 100); err != nil {
		t.Fatal(err)
	}
	want := map[int]int{1: 2, 2: 2, 3: 1, 4: 1, 5: 0}
	for id, infra := range want {
		var got int
		if err := s.Reader().QueryRow(`SELECT infra FROM acquire_run WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != infra {
			t.Errorf("run %d: infra %d, want %d", id, got, infra)
		}
	}
	if _, err := p.DownTo(ctx, 99); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.Reader().QueryRow(`SELECT COUNT(*) FROM acquire_run WHERE infra = 2`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rollback left %d partial rows (%v)", n, err)
	}
}
