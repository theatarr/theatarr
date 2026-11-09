package store

import (
	"context"
	"testing"
)

func TestMetafreshMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openAt(t, 52)
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 51); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'Movie', 0, 0), (2, 'series', 'Series', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 2, 'series', 0, 0)`,
		`INSERT INTO series_monitor (version_id, updated_at) VALUES (1, 0)`,
		`INSERT INTO title_meta (title_id, refreshed_at, attempted_at, refresh_after) VALUES (1, 1000, 1000, 259201000), (2, 1000, 1000, 259201000)`,
	} {
		if _, err := st.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := p.UpTo(ctx, 52); err != nil {
			t.Fatal(err)
		}
		var mode string
		if err := st.Reader().QueryRow(`SELECT pending_monitor FROM series_monitor WHERE version_id = 1`).Scan(&mode); err != nil || mode != "" {
			t.Fatalf("existing monitor mode %q: %v", mode, err)
		}
		for _, tc := range []struct{ id, want int64 }{{1, 43201000}, {2, 259201000}} {
			var after int64
			if err := st.Reader().QueryRow(`SELECT refresh_after FROM title_meta WHERE title_id = ?`, tc.id).Scan(&after); err != nil || after != tc.want {
				t.Fatalf("title %d refresh_after %d, want %d: %v", tc.id, after, tc.want, err)
			}
		}
		if _, err := st.Writer().Exec(`INSERT INTO scene_refresh VALUES ('thexem', 1000, '{}'); UPDATE series_monitor SET pending_monitor = 'pilot'`); err != nil {
			t.Fatal(err)
		}
		if _, err := p.DownTo(ctx, 51); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Writer().Exec(`SELECT * FROM scene_refresh`); err == nil {
			t.Fatal("scene cache survived rollback")
		}
		if _, err := st.Writer().Exec(`SELECT pending_monitor FROM series_monitor`); err == nil {
			t.Fatal("pending monitor survived rollback")
		}
	}
}
