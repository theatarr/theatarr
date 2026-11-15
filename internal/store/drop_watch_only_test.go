package store_test

import (
	"context"
	"testing"

	"github.com/theatarr/theatarr/internal/store"
)

// 00137: Watch only goes. A library stored in shadow mode becomes Live with every one of its titles not monitored
// (a title held by a Live library too stays monitored), enable request, Pause on and one card; a Live library keeps
// everything but loses the mode key. Lists, collections and accounts' request libraries no longer target the
// converted library: its key leaves their targets, one left with none is turned off, and one without targets (the
// defaults) gets the Live libraries of its kind, or is turned off when there are none. The dry-run plans, their verdicts and the RSS run log are dropped and the foreign
// keys check clean.
func TestMigrationDropWatchOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.OpenAt(t, 134)
	db := st.Writer()
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, monitored, created_at, updated_at) VALUES (1, 'movie', 'Alpha', 1, 0, 0),
			(2, 'movie', 'Both', 1, 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, state, monitored, created_at, updated_at) VALUES
			(1, 1, 'watch', 'wanted', 1, 0, 0), (2, 2, 'watch', 'wanted', 1, 0, 0), (3, 2, 'live', 'wanted', 1, 0, 0)`,
		`INSERT INTO setting_version (key, config, created_at, updated_at) VALUES
			('watch', '{"config":{"mode":"shadow","enable":"default","kind":"movie"},"roots":["/w"]}', 0, 0),
			('live', '{"config":{"mode":"active","enable":"default","kind":"movie"},"roots":["/l"]}', 0, 5)`,
		`INSERT INTO setting_global_switches (key, config, created_at, updated_at) VALUES ('global', '{"paused":false}', 0, 0)`,
		`INSERT INTO error_event (fingerprint, source, component, level, message, first_seen, last_seen, updated_at)
			VALUES ('arr-freeze:watch', 'log', 'settings', 'error', 'Library watch used a profile or naming taken from'
			|| ' Sonarr/Radarr that could not be copied. It is now Watch only. Check its profile and naming in Settings ›'
			|| ' Libraries, then set it Live.', 0, 0, 0)`,
		`INSERT INTO router_plan (id, title_id, version_key, mode, outcome, created_at) VALUES (1, 1, 'watch', 'shadow', 'none', 0)`,
		`INSERT INTO journal_verdict (entry_id, verdict, reason, action, created_at) VALUES ('plan:1', 'ok', 'x', 'recorded', 0),
			('dl:1', 'ok', 'y', 'recorded', 0)`,
		`INSERT INTO shadow_run (kind, started_at, finished_at) VALUES ('shadow.rss', 0, 0)`,
		`INSERT INTO import_list (id, name, list_type, tmdb_id, url, versions, created_at, updated_at) VALUES
			(1, 'both', 'tmdb', 1, '', '["watch","live"]', 0, 0), (2, 'watch only', 'tmdb', 2, '', '["watch"]', 0, 0),
			(3, 'defaults', 'tmdb', 3, '', '[]', 0, 0), (4, 'series', 'sonarr', 0, 'http://sonarr', '[]', 0, 0),
			(5, 'live', 'tmdb', 5, '', '["live"]', 0, 7)`,
		`INSERT INTO movie_collection (tmdb_id, monitored, versions, updated_at) VALUES (1, 1, '["watch"]', 0),
			(2, 1, '[]', 0), (3, 1, '["live","watch"]', 0)`,
		`INSERT INTO web_user (id, username, password_hash, created_at, updated_at, password_changed_at, request_versions)
			VALUES (1, 'a', 'x', 0, 0, 0, '["watch","live"]'), (2, 'b', 'x', 0, 0, 0, NULL),
			(3, 'c', 'x', 0, 0, 0, '["watch"]'), (4, 'd', 'x', 0, 0, 0, '[]')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := st.UpTo(ctx, 137); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]string{
		`SELECT group_concat(id || '=' || monitored, ' ') FROM (SELECT id, monitored FROM version ORDER BY id)`: "1=0 2=0 3=1",
		`SELECT group_concat(id || '=' || monitored, ' ') FROM (SELECT id, monitored FROM title ORDER BY id)`:   "1=0 2=1",
		`SELECT json_extract(config, '$.config.enable') || ' ' || (json_type(config, '$.config.mode') IS NULL)
			FROM setting_version WHERE key = 'watch'`: "request 1",
		`SELECT json_extract(config, '$.config.enable') || ' ' || (json_type(config, '$.config.mode') IS NULL) || ' ' ||
			updated_at FROM setting_version WHERE key = 'live'`: "default 1 5",
		`SELECT json_extract(config, '$.paused') FROM setting_global_switches WHERE key = 'global'`: "1",
		`SELECT message FROM error_event WHERE fingerprint = 'watch-only'`: "Watch only is gone: every library is Live. " +
			"Library watch was Watch only, so its titles are now not monitored, arr clients no longer add to it, and Pause " +
			"is on. Monitor what should download, check Settings › Libraries, then turn Pause off.",
		`SELECT message FROM error_event WHERE fingerprint = 'arr-freeze:watch'`: "Library watch used a profile or naming " +
			"taken from Sonarr/Radarr that could not be copied. Check its profile and naming in Settings › Libraries.",
		`SELECT group_concat(id || '=' || enabled || versions || (updated_at > 7), ' ') FROM (SELECT * FROM import_list ORDER BY id)`: `1=1["live"]1 2=0[]1 3=1["live"]1 4=0[]1 5=1["live"]0`,
		`SELECT group_concat(tmdb_id || '=' || monitored || versions, ' ') FROM (SELECT * FROM movie_collection ORDER BY tmdb_id)`:    `1=0[] 2=1["live"] 3=1["live"]`,
		`SELECT group_concat(id || '=' || COALESCE(request_versions, '-'), ' ') FROM (SELECT * FROM web_user ORDER BY id)`:            `1=["live"] 2=["live"] 3=["live"] 4=["live"]`,
		`SELECT group_concat(entry_id) FROM journal_verdict`:                                                                          "dl:1",
		`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('router_plan', 'shadow_run', 'k6_watch_only')`:                             "0",
		`SELECT COUNT(*) FROM pragma_foreign_key_check`:                                                                               "0",
	} {
		var got string
		if err := db.QueryRowContext(ctx, q).Scan(&got); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", q, got, err, want)
		}
	}
}

// 00137 on an install whose libraries are all Live (the owner's): nothing but the mode key changes; Pause stays off
// and no card is written.
func TestMigrationDropWatchOnlyAllLive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.OpenAt(t, 134)
	db := st.Writer()
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, monitored, created_at, updated_at) VALUES (1, 'movie', 'Alpha', 1, 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, state, monitored, created_at, updated_at) VALUES
			(1, 1, 'live', 'wanted', 1, 0, 7)`,
		`INSERT INTO setting_version (key, config, created_at, updated_at) VALUES
			('live', '{"config":{"mode":"active","enable":"default","kind":"movie"},"roots":["/l"]}', 0, 5)`,
		`INSERT INTO setting_global_switches (key, config, created_at, updated_at) VALUES ('global', '{"paused":false}', 0, 3)`,
		`INSERT INTO import_list (id, name, list_type, tmdb_id, url, versions, created_at, updated_at) VALUES
			(1, 'defaults', 'tmdb', 1, '', '[]', 0, 4)`,
		`INSERT INTO movie_collection (tmdb_id, monitored, versions, updated_at) VALUES (1, 1, '[]', 4)`,
		`INSERT INTO web_user (id, username, password_hash, created_at, updated_at, password_changed_at, request_versions)
			VALUES (1, 'a', 'x', 0, 0, 0, NULL), (2, 'b', 'x', 0, 0, 0, '[]')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := st.UpTo(ctx, 137); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]string{
		`SELECT monitored || ' ' || updated_at FROM version`:                                                               "1 7",
		`SELECT config || ' ' || updated_at FROM setting_version`:                                                          `{"config":{"enable":"default","kind":"movie"},"roots":["/l"]} 5`,
		`SELECT config || ' ' || updated_at FROM setting_global_switches`:                                                  `{"paused":false} 3`,
		`SELECT COUNT(*) FROM error_event`:                                                                                 "0",
		`SELECT enabled || versions || updated_at FROM import_list`:                                                        "1[]4",
		`SELECT monitored || versions || updated_at FROM movie_collection`:                                                 "1[]4",
		`SELECT group_concat(id || '=' || COALESCE(request_versions, '-'), ' ') FROM (SELECT * FROM web_user ORDER BY id)`: "1=- 2=[]",
		`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('router_plan', 'shadow_run')`:                                   "0",
	} {
		var got string
		if err := db.QueryRowContext(ctx, q).Scan(&got); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", q, got, err, want)
		}
	}
}

// 00137 on an install whose libraries were all Watch only: no Live library remains, so accounts' request libraries
// stay as they were (Pause is on and the card names the libraries).
func TestMigrationDropWatchOnlyNoLive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.OpenAt(t, 136)
	db := st.Writer()
	for _, q := range []string{
		`INSERT INTO setting_version (key, config, created_at, updated_at) VALUES
			('watch', '{"config":{"mode":"shadow","kind":"movie"},"roots":["/w"]}', 0, 0)`,
		`INSERT INTO web_user (id, username, password_hash, created_at, updated_at, password_changed_at, request_versions)
			VALUES (1, 'a', 'x', 0, 0, 0, NULL), (2, 'b', 'x', 0, 0, 0, '["watch"]')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := st.UpTo(ctx, 137); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]string{
		`SELECT group_concat(id || '=' || COALESCE(request_versions, '-'), ' ') FROM (SELECT * FROM web_user ORDER BY id)`: `1=- 2=["watch"]`,
		`SELECT json_extract(config, '$.paused') FROM setting_global_switches WHERE key = 'global'`:                        "1",
	} {
		var got string
		if err := db.QueryRowContext(ctx, q).Scan(&got); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", q, got, err, want)
		}
	}
}
