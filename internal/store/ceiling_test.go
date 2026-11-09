package store

import (
	"context"
	"testing"
)

func TestCeilingMigrationUpDown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := Open(ctx, migratedPath(t, 27), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 27); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 28); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"source_ceiling", "source_sighting", "ceiling_search", "ceiling_search_indexer", "version_ceiling"} {
		var n int
		if err := st.Reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.DownTo(ctx, 27); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name IN ('source_ceiling','source_sighting','ceiling_search','ceiling_search_indexer','version_ceiling')`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("tables left %d: %v", n, err)
	}
	if _, err := p.UpTo(ctx, 28); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().ExecContext(ctx, `SELECT quality_items FROM arr_quality_profile; SELECT fetched_at FROM title_release_date`); err != nil {
		t.Fatal(err)
	}
}
