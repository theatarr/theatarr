package store

import (
	"context"
	"testing"
)

// 00105 makes title tags per version: each existing (title, tag) is copied to every version the title has, a title
// with no version keeps none (no view showed it), arr_title gains tags_copied '[]'; it rolls back to one row per
// (title, tag).
func TestVersionTagsMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := Open(ctx, migratedPath(t, 104), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 104); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'A', 0, 0), (2, 'movie', 'B', 0, 0),
			(3, 'movie', 'C', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'movie-remux', 0, 0),
			(2, 1, 'movie-web-theatarr', 0, 0), (3, 2, 'movie-web', 0, 0)`,
		`INSERT INTO arrview_tag (id, label, created_at) VALUES (1, 'best-available', 0), (2, '1-alice', 0)`,
		`INSERT INTO arrview_title_tag (title_id, tag_id) VALUES (1, 1), (1, 2), (2, 2), (3, 1)`,
		`INSERT INTO arr_instance (name, flavour, policy_key, created_at, updated_at) VALUES ('radarr', 'radarr', 'movie-remux', 0, 0)`,
		`INSERT INTO arr_title (instance, arr_id, kind, title_id, version_id, monitored, tags)
			VALUES ('radarr', 1, 'movie', 1, 1, 1, '["best-available"]')`,
	} {
		if _, err := st.Writer().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	rows := func(q string) string {
		var s string
		if err := st.Reader().QueryRowContext(ctx, `SELECT COALESCE(group_concat(r, ' '), '') FROM (`+q+`)`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	for i := 0; i < 2; i++ {
		if _, err := p.UpTo(ctx, 105); err != nil {
			t.Fatal(err)
		}
		if got, want := rows(`SELECT version || ':' || title_id || ':' || tag_id AS r FROM arrview_title_tag
			ORDER BY version, title_id, tag_id`), "movie-remux:1:1 movie-remux:1:2 movie-web:2:2 movie-web-theatarr:1:1 movie-web-theatarr:1:2"; got != want {
			t.Errorf("up: %s, want %s", got, want)
		}
		if got := rows(`SELECT tags_copied AS r FROM arr_title`); got != "[]" {
			t.Errorf("tags_copied %s", got)
		}
		// Deleting a label still cascades to every version.
		if i == 1 {
			if _, err := st.Writer().ExecContext(ctx, `DELETE FROM arrview_tag WHERE id = 1`); err != nil {
				t.Fatal(err)
			}
			if got := rows(`SELECT version || ':' || title_id || ':' || tag_id AS r FROM arrview_title_tag WHERE tag_id = 1`); got != "" {
				t.Errorf("label delete left %s", got)
			}
			break
		}
		if _, err := p.DownTo(ctx, 104); err != nil {
			t.Fatal(err)
		}
		if got, want := rows(`SELECT title_id || ':' || tag_id AS r FROM arrview_title_tag ORDER BY title_id, tag_id`),
			"1:1 1:2 2:2"; got != want {
			t.Errorf("down: %s, want %s", got, want)
		}
		if _, err := st.Writer().ExecContext(ctx, `SELECT tags_copied FROM arr_title`); err == nil {
			t.Error("tags_copied left after rollback")
		}
	}
}
