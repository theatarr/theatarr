package store

import (
	"context"
	"testing"
)

// 00129 adds History's facts: release columns on library_action (old rows keep NULL) and provider_copy_event; it
// rolls back cleanly and applies again.
func TestHistoryFactsMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := Open(ctx, migratedPath(t, 128), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 128); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO library_action (at, kind, mode, path) VALUES (1, 'replace',
		'applied', '/a')`); err != nil {
		t.Fatal(err)
	}
	// Finished copies are seeded as events with what they kept; unfinished and failed ones are not.
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO title (id, kind, name, created_at, updated_at)
		VALUES (1, 'movie', 'M', 0, 0);
		INSERT INTO provider_copy (info_hash, name, title_id, provider, state, message, created_at, updated_at) VALUES
		('h1', 'R1', 1, 'tb', 'added', 'Added to TB', 5, 6), ('h1', 'R1', 1, 'ad', 'added', 'Already on AD', 5, 7),
		('h1', 'R1', NULL, 'rd', 'not_cached', 'Not cached on RD', 5, 8), ('h2', 'R2', 1, 'tb', 'queued', '', 5, 9),
		('h2', 'R2', 1, 'ad', 'failed', 'x', 5, 9)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := p.UpTo(ctx, 129); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM library_action WHERE release_title IS NULL
			AND quality IS NULL AND score IS NULL`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("old action: %d %v", n, err)
		}
		var seeded string
		if err := st.Reader().QueryRowContext(ctx, `SELECT group_concat(at || ' ' || provider || ' ' || state || ' ' ||
			COALESCE(title_id, '-') || ' ' || name, ', ') FROM (SELECT * FROM provider_copy_event ORDER BY id)`).
			Scan(&seeded); err != nil || seeded != "6 tb added 1 R1, 7 ad already 1 R1, 8 rd not_cached - R1" {
			t.Fatalf("seeded events %q %v", seeded, err)
		}
		if _, err := p.DownTo(ctx, 128); err != nil {
			t.Fatal(err)
		}
		if err := st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'provider_copy_event'`).
			Scan(&n); err != nil || n != 0 {
			t.Fatalf("table left %d: %v", n, err)
		}
	}
}
