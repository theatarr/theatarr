package store_test

import (
	"context"
	"testing"

	"github.com/theatarr/theatarr/internal/store"
)

// 00134: a database with leftover arr-file rows upgrades cleanly. The owner's current file of each library becomes a
// link record holding its title, library, episodes and quality; another instance's file does not; a library action
// keeps the title it found through that file; a profile still taken from an arr becomes the minimal native profile
// and its library Watch only with one message. The seven tables are gone and the foreign keys check clean.
func TestMigrationDropArrFiles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.OpenAt(t, 131)
	db := st.Writer()
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'Alpha', 0, 0),
			(2, 'series', 'Show', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES
			(1, 1, 'movie', 'filled', 0, 0), (2, 2, 'tv', 'filled', 0, 0)`,
		`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES
			(21, 2, 1, 1, 'a', 0, 0), (22, 2, 1, 2, 'b', 0, 0)`,
		`INSERT INTO version_source (version_id, instance, arr_id) VALUES (1, 'radarr', 7)`,
		`INSERT INTO content (id, info_hash, name, state, first_seen_at, last_seen_at) VALUES
			(5, 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'Alpha', 'present', 0, 0)`,
		`INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path, size_bytes, quality,
			scene_name, languages, media_info, added_at, content_id, custom_format_score) VALUES
			(1, 1, 1, 'radarr', 1, '/movies/Alpha/Alpha.mkv', 9000, 'Remux-2160p', 'Alpha.2020.REMUX', '["English"]',
			 '{"videoCodec":"x265"}', 1000, 5, 1950),
			(2, 1, 1, 'radarr_old', 2, '/old/Alpha.mkv', 1, 'DVD', '', '[]', '{}', 10, NULL, 0),
			(3, 2, 2, 'sonarr', 3, '/tv/Show/S01E01E02.mkv', 2000, 'WEBDL-1080p', '', '[]', '{}', 2000, NULL, NULL)`,
		`INSERT INTO version_file_episode (version_file_id, episode_id) VALUES (3, 21), (3, 22)`,
		`INSERT INTO content_version (content_id, version_id, source) VALUES (5, 1, 'arr_history')`,
		`INSERT INTO arr_frozen_input (instance, flavour, body) VALUES ('radarr', 'radarr', '{}')`,
		`INSERT INTO library_action (at, kind, mode, path, detail) VALUES (1, 'link', 'applied', '/tv/Show/S01E01E02.mkv', '')`,
		`INSERT INTO setting_quality_profile (key, config, created_at, updated_at) VALUES ('arrp', '{"FromArr":"radarr:HD"}', 0, 0)`,
		`INSERT INTO setting_version (key, config, created_at, updated_at) VALUES
			('movie', '{"config":{"profile":"arrp","mode":"active"}}', 0, 0)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := st.UpTo(ctx, 134); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]string{
		`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('version_file', 'version_file_episode', 'version_source',
			'content_version', 'arr_frozen_input', 'release', 'decision')`: "0",
		`SELECT group_concat(path, ' ') FROM (SELECT path FROM link ORDER BY path)`: "/movies/Alpha/Alpha.mkv /tv/Show/S01E01E02.mkv",
		`SELECT l.kind || ' ' || l.quality || ' ' || l.state || ' ' || lv.version_id || ' ' || l.adopted_from || ' ' ||
			json_extract(l.file_metadata, '$.InfoHash') || ' ' || json_extract(l.file_metadata, '$.Size') || ' ' ||
			json_extract(l.adopted_media, '$.videoCodec') || ' ' || l.created_at || ' ' || l.score
			FROM link l JOIN link_version lv ON lv.link_id = l.id WHERE l.title_id = 1`: "copy Remux-2160p active 1 radarr /movies/Alpha/Alpha.mkv aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 9000 x265 1000 1950",
		`SELECT COALESCE(score, 'null') FROM link WHERE title_id = 2`: "null",
		`SELECT group_concat(le.episode_id, ' ') FROM link l JOIN link_episode le ON le.link_id = l.id
			JOIN link_version lv ON lv.link_id = l.id AND lv.version_id = 2 WHERE l.title_id = 2`: "21 22",
		`SELECT title_id FROM library_action`: "2",
		`SELECT json_extract(config, '$.Cutoff') || ' ' || COALESCE(json_extract(config, '$.FromArr'), '-')
			FROM setting_quality_profile WHERE key = 'arrp'`: "WEBDL-1080p -",
		`SELECT json_extract(config, '$.config.mode') FROM setting_version WHERE key = 'movie'`: "shadow",
		`SELECT COUNT(*) FROM error_event WHERE fingerprint = 'arr-freeze:movie'`:               "1",
		`SELECT COUNT(*) FROM pragma_foreign_key_check`:                                         "0",
	} {
		var got string
		if err := db.QueryRowContext(ctx, q).Scan(&got); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", q, got, err, want)
		}
	}
}
