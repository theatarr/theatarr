package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestLinkMetadataMigration(t *testing.T) {
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
	exec := func(q string) {
		t.Helper()
		if _, err := st.Writer().ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'Movie', 0, 0)`)
	exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'movie', 0, 0)`)
	exec(`INSERT INTO release (id, indexer, guid, origin, privacy, info_hash, title, size_bytes, parsed, indexer_flags,
		first_seen_at, last_seen_at) VALUES (1, 'idx', 'one', 'search', 'public', 'abc', 'Movie.2020.REPACK.1080p.WEB-DL-GRP',
		3000, '{"languages":["French",{"id":1,"name":"English"}]}', '["internal"]', 0, 0)`)
	exec(`INSERT INTO release (id, indexer, guid, origin, privacy, info_hash, title, size_bytes, parsed,
		first_seen_at, last_seen_at) VALUES (2, 'other', 'two', 'search', 'public', 'abc', 'Other.Name',
		4000, '{"languages":["German"]}', 0, 999)`)
	exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		score, size_bytes, formats, state, created_at, updated_at) VALUES (1, 1, 1, 'movie', 'abc',
		'Movie.2020.REPACK.1080p.WEB-DL-GRP', 'idx', 'WEBDL-1080p', 50, 2000,
		'[{"name":"French","score":50}]', 'imported', 0, 0)`)
	exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (1, 'abc', 'present', 0, 0)`)
	exec(`INSERT INTO content_file (id, content_id, path, size_bytes) VALUES (1, 1, 'file.mkv', 1000)`)
	exec(`INSERT INTO link (id, title_id, download_id, content_file_id, path, target, created_at, updated_at)
		VALUES (1, 1, 1, 1, '/renamed.mkv', '/mount/file.mkv', 0, 0),
		(2, 1, NULL, NULL, '/adopted.mkv', '/mount/adopted.mkv', 0, 0)`)
	check := func() {
		t.Helper()
		var raw, title, quality string
		var score int
		if err := st.Reader().QueryRowContext(ctx, `SELECT file_metadata, release_title, quality, score FROM link
			WHERE id = 1`).Scan(&raw, &title, &quality, &score); err != nil {
			t.Fatal(err)
		}
		var f struct {
			SceneName, Quality, InfoHash string
			Size                         int
			Languages, Flags             []string
			Formats                      []struct {
				Name  string
				Score int
			}
		}
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			t.Fatal(err)
		}
		if f.SceneName != title || f.Quality != quality || quality != "WEBDL-1080p" || score != 50 || f.Size != 1000 ||
			f.InfoHash != "abc" || len(f.Languages) != 2 || f.Languages[0] != "French" || f.Languages[1] != "English" || len(f.Flags) != 1 ||
			len(f.Formats) != 1 || f.Formats[0].Score != 50 {
			t.Fatalf("backfill: %s, title %q, quality %q, score %d", raw, title, quality, score)
		}
		var valid bool
		if err := st.Reader().QueryRowContext(ctx, `SELECT json_valid(file_metadata) FROM link WHERE id = 2`).Scan(&valid); err != nil || !valid {
			t.Fatalf("link without download: valid %v, %v", valid, err)
		}
	}
	if _, err := p.UpTo(ctx, 38); err != nil {
		t.Fatal(err)
	}
	check()
	if _, err := p.DownTo(ctx, 37); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Reader().QueryContext(ctx, `SELECT file_metadata FROM link`); err == nil {
		t.Fatal("file metadata survived Down")
	}
	if _, err := st.Reader().QueryContext(ctx, `SELECT release_data FROM download`); err == nil {
		t.Fatal("release data survived Down")
	}
	if _, err := p.UpTo(ctx, 38); err != nil {
		t.Fatal(err)
	}
	check()
}
