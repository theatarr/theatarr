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
	for i := 0; i < 2; i++ {
		if _, err := p.UpTo(ctx, 129); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM library_action WHERE release_title IS NULL
			AND quality IS NULL AND score IS NULL`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("old action: %d %v", n, err)
		}
		if _, err := st.Writer().ExecContext(ctx, `INSERT INTO provider_copy_event (at, info_hash, provider, state)
			VALUES (1, 'h', 'rd', 'added')`); err != nil {
			t.Fatal(err)
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
