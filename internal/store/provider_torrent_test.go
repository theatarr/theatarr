package store_test

import (
	"context"
	"testing"

	"github.com/theatarr/theatarr/internal/store"
)

// 00129: the record of the accounts is two new tables; two torrents of one hash on one account are two rows, and
// content_provider is left as it was.
func TestMigrationProviderTorrent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.OpenAt(t, 128)
	db := st.Writer()
	const h = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, q := range []string{
		`INSERT INTO content (id, info_hash, name, state, first_seen_at, last_seen_at) VALUES (1, '` + h + `', 'X', 'unreferenced', 1, 1)`,
		`INSERT INTO content_provider (content_id, provider, torrent_id, folder_name, seen_at, state, duplicates)
			VALUES (1, 'rd', 'R1', 'X', 5, 'present', 1)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := st.UpTo(ctx, 129); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO provider_torrent (provider, torrent_id, info_hash, name) VALUES ('rd', 'R1', '` + h + `', 'X'),
			('rd', 'R2', '` + h + `', 'X')`,
		`INSERT INTO provider_torrent_read (provider, read_at, torrents) VALUES ('rd', 10, 2)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO provider_torrent (provider, torrent_id) VALUES ('rd', 'R1')`); err == nil {
		t.Fatal("one row per provider and torrent id")
	}
	for q, want := range map[string]string{
		`SELECT count(*) || ' ' || min(files) || ' ' || min(links) || ' ' || COALESCE(max(added_at), 'null') FROM provider_torrent`: "2 -1 -1 null",
		`SELECT torrent_id || ' ' || seen_at || ' ' || state || ' ' || duplicates FROM content_provider`:                            "R1 5 present 1",
		`SELECT state FROM content`: "unreferenced",
	} {
		var got string
		if err := db.QueryRowContext(ctx, q).Scan(&got); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", q, got, err, want)
		}
	}
}
