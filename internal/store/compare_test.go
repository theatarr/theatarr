package store

import (
	"context"
	"testing"
)

func TestCompareMigrationUpDown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := Open(ctx, migratedPath(t, 32), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 32); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := p.UpTo(ctx, 35); err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{`SELECT started_at FROM shadow_compare_source`, `SELECT event FROM shadow_compare_grab`, `SELECT compare_instance, compare_key FROM shadow_diff`} {
			if _, err := st.Writer().ExecContext(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := p.DownTo(ctx, 34); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name IN ('shadow_compare_source','shadow_compare_grab','shadow_diff_compare')`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("comparison objects left %d: %v", n, err)
		}
		if _, err := st.Writer().ExecContext(ctx, `SELECT compare_key FROM shadow_diff`); err == nil {
			t.Fatal("comparison column left after rollback")
		}
	}
}
