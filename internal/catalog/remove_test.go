package catalog

import (
	"context"
	"database/sql"
	"slices"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// TestRemoveAndRestore: Remove hides and unmonitors copies (rows kept), drops the arr view records only when no copy
// is left, and an add restores a removed copy.
func TestRemoveAndRestore(t *testing.T) {
	t.Parallel()
	st, _ := setup(t)
	ctx := context.Background()
	r := add(t, st, Input{Kind: policy.KindMovie, TmdbID: 500, Title: "Omega", Year: 2025,
		Versions: fixed("movie-remux", "movie-web"), Monitored: true, Source: "test"})
	if _, err := st.Writer().Exec(`INSERT INTO arrview_title (view, title_id, quality_profile_id, added_at,
		updated_at) VALUES ('radarr', ?, 1, 0, 0)`, r.TitleID); err != nil {
		t.Fatal(err)
	}
	remove := func(keys ...string) []string {
		t.Helper()
		var out []string
		err := st.WriteTx(ctx, func(tx *sql.Tx) error {
			var err error
			out, err = Remove(ctx, tx, r.TitleID, keys, "ui", now)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	count := func(q string) int {
		t.Helper()
		var n int
		if err := st.Reader().QueryRow(q, r.TitleID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := remove("movie-web", "tv-2160p"); !slices.Equal(got, []string{"movie-web"}) {
		t.Fatalf("removed %v", got)
	}
	if count(`SELECT COUNT(*) FROM arrview_title WHERE title_id = ?`) != 1 ||
		count(`SELECT COUNT(*) FROM version WHERE title_id = ? AND monitored = 1`) != 1 ||
		count(`SELECT monitored FROM title WHERE id = ?`) != 1 {
		t.Error("one copy left: the title stays monitored with its view records")
	}
	if got := remove("movie-web"); len(got) != 0 {
		t.Errorf("removed twice: %v", got)
	}
	remove("movie-remux")
	if count(`SELECT COUNT(*) FROM arrview_title WHERE title_id = ?`) != 0 ||
		count(`SELECT monitored FROM title WHERE id = ?`) != 0 ||
		count(`SELECT COUNT(*) FROM version WHERE title_id = ?`) != 2 {
		t.Error("every copy gone: view records dropped, title unmonitored, rows kept")
	}
	add(t, st, Input{Kind: policy.KindMovie, TmdbID: 500, Versions: fixed("movie-web"), Monitored: true})
	if count(`SELECT COUNT(*) FROM arrview_removed_version r JOIN version v ON v.id = r.version_id WHERE v.title_id = ?`) != 1 {
		t.Error("an add restores the copy it names only")
	}
}
