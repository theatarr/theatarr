package store

import (
	"context"
	"testing"
)

func TestAcquisitionMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := Open(ctx, migratedPath(t, 37), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 37); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO title (id,kind,name,created_at,updated_at) VALUES (1,'movie','Movie',0,0),(2,'series','Series',0,0)`,
		`INSERT INTO title_meta (title_id,attempted_at,refresh_after) VALUES (1,1,999),(2,1,999)`,
		`INSERT INTO shadow_rss_seen (indexer,item_key,seen_at) VALUES ('fixture','h:rejected',1)`,
	} {
		if _, err := st.Writer().ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.UpTo(ctx, 41); err != nil {
		t.Fatal(err)
	}
	var seen, movieDue, seriesDue int
	if err := st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM shadow_rss_seen`).Scan(&seen); err != nil || seen != 0 {
		t.Fatalf("seen=%d: %v", seen, err)
	}
	if err := st.Reader().QueryRowContext(ctx, `SELECT refresh_after FROM title_meta WHERE title_id = 1 AND movie_dates_at IS NULL`).Scan(&movieDue); err != nil || movieDue != 0 {
		t.Fatalf("movie due=%d: %v", movieDue, err)
	}
	if err := st.Reader().QueryRowContext(ctx, `SELECT refresh_after FROM title_meta WHERE title_id = 2`).Scan(&seriesDue); err != nil || seriesDue != 999 {
		t.Fatalf("series due=%d: %v", seriesDue, err)
	}
	if _, err := p.DownTo(ctx, 37); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 41); err != nil {
		t.Fatal(err)
	}
}
