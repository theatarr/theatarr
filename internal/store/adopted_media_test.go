package store

import (
	"context"
	"testing"
)

// TestAdoptedMediaBackfill: 00117 gives an adopted link its arr file's media_info; a link Theatarr imported, and an
// adopted link whose arr file is no longer recorded, stay NULL.
func TestAdoptedMediaBackfill(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openAt(t, 114)
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'Alpha', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (10, 1, 'movie-remux', 0, 0)`,
		`INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path, size_bytes, quality, media_info)
			VALUES (1, 10, 1, 'radarr', 1, '/movies/Alpha (2020)/Alpha.mkv', 1000, 'Remux-2160p', '{"videoCodec":"x265"}')`,
		`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, size_bytes, state,
			created_at, updated_at) VALUES (1, 1, 10, 'movie-remux', 'aa', 'Alpha', 'dmm', 1000, 'imported', 0, 0)`,
		`INSERT INTO link (id, path, target, title_id, adopted_from, created_at, updated_at) VALUES
			(1, '/theatarr/Alpha.mkv', '/mount/a.mkv', 1, 'radarr /movies/Alpha (2020)/Alpha.mkv', 0, 0),
			(2, '/theatarr/Gone.mkv', '/mount/g.mkv', 1, 'radarr /movies/Gone.mkv', 0, 0)`,
		`INSERT INTO link (id, path, target, title_id, download_id, created_at, updated_at) VALUES
			(3, '/theatarr/Own.mkv', '/mount/o.mkv', 1, 1, 0, 0)`,
	} {
		if _, err := st.Writer().Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 117); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := st.Reader().QueryRow(`SELECT GROUP_CONCAT(id || ' ' || COALESCE(adopted_media, 'NULL'), ', ') FROM
		(SELECT * FROM link ORDER BY id)`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != `1 {"videoCodec":"x265"}, 2 NULL, 3 NULL` {
		t.Fatalf("adopted media: %s", got)
	}
}
