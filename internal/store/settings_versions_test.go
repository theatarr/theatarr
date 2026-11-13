package store

import (
	"context"
	"strings"
	"testing"
)

func TestSettingsVersionsMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openAt(t, 68)
	p, err := s.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 66); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := p.UpTo(ctx, 68); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"setting_version", "setting_view_binding"} {
			if _, err := s.Writer().ExecContext(ctx, "INSERT INTO "+table+" (key,config,created_at,updated_at) VALUES ('fixture','{}',0,0)"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Writer().ExecContext(ctx, "UPDATE "+table+" SET config='invalid'"); err == nil {
				t.Fatal("accepted invalid JSON")
			}
		}
		if _, err := p.DownTo(ctx, 66); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"setting_version", "setting_view_binding", "settings_e3_bootstrap"} {
			if _, err := s.Reader().QueryContext(ctx, "SELECT * FROM "+table); err == nil {
				t.Fatalf("%s survived migration down", table)
			}
		}
	}
}

// 00120: the guided migration's plans go, and a library row loses only its golive_done.
func TestDropGuidedMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openAt(t, 119)
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 119); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO setting_version(key,config,created_at,updated_at) VALUES
			('movies','{"config":{"mode":"active","golive_done":["homepage"]},"roots":["/media/movies"]}',1,1),
			('tv','{"config":{"mode":"shadow"}}',1,1)`,
		`INSERT INTO migration_plan(name,inventory,plan,created_at,updated_at) VALUES('old','{}','{}',1,1)`,
	} {
		if _, err := st.Writer().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := p.UpTo(ctx, 120); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"movies": `{"config":{"mode":"active"},"roots":["/media/movies"]}`,
		"tv":     `{"config":{"mode":"shadow"}}`,
	} {
		var config string
		if err := st.Reader().QueryRowContext(ctx, `SELECT config FROM setting_version WHERE key=?`, key).Scan(&config); err != nil || config != want {
			t.Fatalf("%s config %s, %v", key, config, err)
		}
	}
	var n int
	if err := st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name='migration_plan'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("migration_plan still there: %d %v", n, err)
	}
}

// 00121: following goes. Inherited naming and minimum availability are copied to the former follower's own rows, an
// existing choice still wins, a series without a type takes its arr's, queued adopt jobs and both follow tables go.
func TestMigrateDetach(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openAt(t, 120)
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, series_type, created_at, updated_at) VALUES (1, 'movie', 'New', NULL, 0, 0),
			(2, 'series', 'Untyped', NULL, 0, 0), (3, 'movie', 'Chosen', NULL, 0, 0), (4, 'movie', 'Listed', NULL, 0, 0),
			(5, 'movie', 'Default', NULL, 0, 0), (6, 'series', 'Typed', 'standard', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES
			(10, 1, 'movie-remux', 0, 0), (11, 1, 'movie-copy', 0, 0), (30, 3, 'movie-remux', 0, 0), (31, 3, 'movie-copy', 0, 0),
			(40, 4, 'movie-remux', 0, 0), (41, 4, 'movie-copy', 0, 0), (50, 5, 'movie-remux', 0, 0), (51, 5, 'movie-copy', 0, 0),
			(20, 2, 'tv', 0, 0), (60, 6, 'tv', 0, 0)`,
		`INSERT INTO version_follow (version_id, follows_id) VALUES (11, 10), (31, 30), (41, 40), (51, 50)`,
		`INSERT INTO arr_instance (name, flavour, policy_key, created_at, updated_at) VALUES
			('aaa', 'radarr', 'movie-remux', 0, 0), ('radarr', 'radarr', 'movie-remux', 0, 0), ('sonarr', 'sonarr', 'tv', 0, 0)`,
		// Title 1: the owner's record wins over another instance's that sorts first.
		`INSERT INTO version_source (version_id, instance, arr_id) VALUES (10, 'radarr', 1), (30, 'radarr', 3),
			(40, 'radarr', 4), (50, 'radarr', 5)`,
		`INSERT INTO arr_title (instance, arr_id, kind, title_id, version_id, monitored, minimum_availability, series_type)
			VALUES ('radarr', 1, 'movie', 1, 10, 1, 'announced', ''), ('aaa', 1, 'movie', 1, 10, 1, 'inCinemas', ''),
			('radarr', 3, 'movie', 3, 30, 1, 'announced', ''), ('radarr', 4, 'movie', 4, 40, 1, 'announced', ''),
			('radarr', 5, 'movie', 5, 50, 1, 'released', ''),
			('sonarr', 2, 'series', 2, 20, 1, '', 'anime'), ('aaa', 2, 'series', 2, 20, 1, '', ''),
			('sonarr', 6, 'series', 6, 60, 1, '', 'anime')`,
		// Title 3: an explicit choice on the library's own row and a newer one on a view holding it. Title 4: a list
		// sync's row without a value.
		`INSERT INTO arrview_title (view, title_id, minimum_availability, added_at, updated_at) VALUES
			('v/movie-copy', 3, 'inCinemas', 1, 5), ('radarr', 1, 'inCinemas', 1, 7), ('v/movie-copy', 4, '', 1, 9)`,
		// As the settings store writes them: library rows hold the YAML fields under config, naming rows policy.Naming
		// with its Go field names.
		`INSERT INTO setting_version (key, config, created_at, updated_at) VALUES
			('movie-remux', '{"config":{"mode":"shadow","kind":"movie","follows":"","minimum_availability":""},"roots":["/m"]}', 1, 1),
			('movie-copy', '{"config":{"mode":"active","kind":"movie","follows":"movie-remux","minimum_availability":""},"roots":["/c"]}', 1, 1),
			('tv-copy', '{"config":{"mode":"active","kind":"series","follows":"tv"},"roots":["/t"]}', 1, 1)`,
		`INSERT INTO setting_version_naming (key, config, created_at, updated_at) VALUES
			('movie-remux', '{"Folder":"{Movie Title}","File":"","Colon":"","FromArr":"","FromVersion":""}', 1, 1),
			('movie-copy', '{"Folder":"","File":"","Colon":"","FromArr":"","FromVersion":"movie-remux"}', 1, 1),
			('tv-copy', '{"Folder":"","File":"","Colon":"","FromArr":"","FromVersion":"tv"}', 1, 1)`,
		`INSERT INTO job (kind, state, run_at, created_at, updated_at) VALUES ('library.adopt', 'queued', 0, 0, 0),
			('library.adopt', 'done', 0, 0, 0), ('plex.scan', 'queued', 0, 0, 0)`,
		`INSERT INTO follow_search_intent (title_id, version_key, all_items, created_at, updated_at) VALUES (1, 'movie-copy', 1, 0, 0)`,
	} {
		if _, err := st.Writer().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := p.UpTo(ctx, 121); err != nil {
		t.Fatal(err)
	}
	str := func(q string) string {
		t.Helper()
		var s string
		if err := st.Reader().QueryRowContext(ctx, q).Scan(&s); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return s
	}
	for q, want := range map[string]string{
		`SELECT config FROM setting_version_naming WHERE key = 'movie-remux'`:                         `{"Folder":"{Movie Title}","File":"","Colon":"","FromArr":""}`,
		`SELECT config FROM setting_version_naming WHERE key = 'movie-copy'`:                          `{"Folder":"{Movie Title}","File":"","Colon":"","FromArr":""}`,
		`SELECT config FROM setting_version_naming WHERE key = 'tv-copy'`:                             `{}`,
		`SELECT group_concat(json_type(config, '$.config.follows') IS NULL, '') FROM setting_version`: "111",
		`SELECT json_extract(config, '$.config.mode') FROM setting_version WHERE key = 'movie-copy'`:  "active",
		// The inherited value, behind any newer choice.
		`SELECT minimum_availability || ' ' || updated_at FROM arrview_title WHERE view = 'v/movie-copy' AND title_id = 1`: "announced 0",
		`SELECT minimum_availability || ' ' || updated_at FROM arrview_title WHERE view = 'radarr' AND title_id = 1`:       "inCinemas 7",
		// An explicit choice is kept; a blank list-synced row takes the value with updated_at 0.
		`SELECT minimum_availability || ' ' || updated_at FROM arrview_title WHERE view = 'v/movie-copy' AND title_id = 3`: "inCinemas 5",
		`SELECT minimum_availability || ' ' || updated_at FROM arrview_title WHERE view = 'v/movie-copy' AND title_id = 4`: "announced 0",
		// The library's own default needs no row.
		`SELECT COUNT(*) FROM arrview_title WHERE title_id = 5`:                                                        "0",
		`SELECT group_concat(series_type, ' ') FROM (SELECT series_type FROM title WHERE kind = 'series' ORDER BY id)`: "anime standard",
		`SELECT group_concat(kind || ':' || state, ' ') FROM (SELECT kind, state FROM job ORDER BY id)`:                "library.adopt:done plex.scan:queued",
		`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('version_follow', 'follow_search_intent')`:                  "0",
	} {
		if got := str(q); got != want {
			t.Errorf("%s\n got %s, want %s", q, got, want)
		}
	}
}

// 00122: the comparison goes. The stored shadow section keeps only rss and rss_interval, queued shadow.grab jobs and
// the Watch-only dry-run plans (source shadow.*) go with the six comparison tables; RSS's seen marks, cursors, runs,
// queued shadow.rss jobs and every other plan stay.
func TestMigrationDropsComparison(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openAt(t, 121)
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'Film', 0, 0)`,
		`INSERT INTO setting_section (key, config, created_at, updated_at) VALUES ('shadow', '{"import_interval":"10m",
			"rss_interval":"20m","rss":false,"search":true,"searches_per_hour":5,"search_reuse":"1h","keep_results":3,
			"link_window":"2h"}', 1, 1), ('router', '{"enabled":true}', 1, 1)`,
		`INSERT INTO job (kind, state, run_at, created_at, updated_at) VALUES ('shadow.grab', 'queued', 0, 0, 0),
			('shadow.rss', 'queued', 0, 0, 0), ('arr.import', 'queued', 0, 0, 0)`,
		`INSERT INTO router_plan (title_id, version_key, mode, outcome, source, created_at) VALUES
			(1, 'movie-web', 'shadow', 'would_add', 'shadow.rss', 0), (1, 'movie-web', 'shadow', 'none', 'shadow.grab', 0),
			(1, 'movie-web', 'disabled', 'would_add', 'rss', 0), (1, 'movie-web', 'shadow', 'would_add', 'acquire:1', 0)`,
		`INSERT INTO shadow_grab (id, instance, history_id, title_id, version_key, grabbed_at, release_title, state,
			created_at, updated_at) VALUES (1, 'radarr', 1, 1, 'movie-web', 0, 'Film.2020', 'done', 0, 0)`,
		`INSERT INTO shadow_rss_pick (id, indexer, release_title, title_id, version_key, seen_at, grab_id) VALUES
			(1, 'ix', 'Film.2020', 1, 'movie-web', 0, 1)`,
		`INSERT INTO shadow_diff (at, title_id, version_key, bucket, reason, grab_id, rss_pick_id, created_at) VALUES
			(0, 1, 'movie-web', 'agree', 'same', 1, 1, 0)`,
		`INSERT INTO shadow_cursor (instance, history_id, updated_at) VALUES ('radarr', 1, 0)`,
		`INSERT INTO shadow_compare_source (instance, started_at) VALUES ('radarr', 0)`,
		`INSERT INTO shadow_compare_grab (instance, history_id, arr_title_id, title_id, at, event) VALUES
			('radarr', 1, 1, 1, 0, 'grabbed')`,
		`INSERT INTO shadow_rss_seen (indexer, item_key, seen_at) VALUES ('ix', 'a', 0)`,
		`INSERT INTO shadow_rss_cursor (indexer, item_key, updated_at) VALUES ('ix', 'a', 0)`,
		`INSERT INTO shadow_run (kind, started_at, finished_at) VALUES ('rss', 0, 0)`,
	} {
		if _, err := st.Writer().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := p.UpTo(ctx, 122); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]string{
		`SELECT config FROM setting_section WHERE key = 'shadow'`:                            `{"rss_interval":"20m","rss":false}`,
		`SELECT config FROM setting_section WHERE key = 'router'`:                            `{"enabled":true}`,
		`SELECT group_concat(kind, ' ') FROM (SELECT kind FROM job ORDER BY id)`:             "shadow.rss arr.import",
		`SELECT group_concat(source, ' ') FROM (SELECT source FROM router_plan ORDER BY id)`: "rss acquire:1",
		`SELECT COUNT(*) FROM shadow_rss_seen`:                                               "1",
		`SELECT COUNT(*) FROM shadow_rss_cursor`:                                             "1",
		`SELECT COUNT(*) FROM shadow_run`:                                                    "1",
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('shadow_diff', 'shadow_rss_pick',
			'shadow_grab', 'shadow_cursor', 'shadow_compare_grab', 'shadow_compare_source')`: "0",
	} {
		var got string
		if err := st.Reader().QueryRowContext(ctx, q).Scan(&got); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if got != want {
			t.Errorf("%s\n got %s, want %s", q, got, want)
		}
	}
}

// 00123: one Pause, Watch only or Live, notifications in the notify section (the plan's table, row by row).
func TestMigrateControls(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	type result struct{ switches, notify, versions string }
	run := func(t *testing.T, switches, notify string, versions map[string]string) result {
		t.Helper()
		st := openAt(t, 122)
		p, err := st.provider(false)
		if err != nil {
			t.Fatal(err)
		}
		qs := []string{`INSERT INTO setting_global_switches (key, config, created_at, updated_at) VALUES ('global', '` + switches + `', 1, 1)`}
		if notify != "" {
			qs = append(qs, `INSERT INTO setting_section (key, config, created_at, updated_at) VALUES ('notify', '`+notify+`', 1, 1)`)
		}
		for key, config := range versions {
			qs = append(qs, `INSERT INTO setting_version (key, config, created_at, updated_at) VALUES ('`+key+`', '`+config+`', 1, 1)`)
		}
		for _, q := range qs {
			if _, err := st.Writer().ExecContext(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		if _, err := p.UpTo(ctx, 123); err != nil {
			t.Fatal(err)
		}
		var out result
		for q, into := range map[string]*string{
			`SELECT config FROM setting_global_switches`:                                                                     &out.switches,
			`SELECT COALESCE((SELECT config FROM setting_section WHERE key = 'notify'), '(none)')`:                           &out.notify,
			`SELECT COALESCE(group_concat(key || '=' || config, ' '), '') FROM (SELECT * FROM setting_version ORDER BY key)`: &out.versions,
		} {
			if err := st.Reader().QueryRowContext(ctx, q).Scan(into); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		return out
	}
	for _, c := range []struct {
		name, switches, notify, wantSwitches, wantNotify string
	}{
		{"both on", `{"router_enabled":true,"library_enabled":true,"notify_enabled":false}`, `{"poll":"15s"}`, `{"paused":false}`, `{"poll":"15s"}`},
		{"router off", `{"router_enabled":false,"library_enabled":true,"notify_enabled":false}`, `null`, `{"paused":true}`, `null`},
		{"library off", `{"router_enabled":true,"library_enabled":false,"notify_enabled":false}`, `null`, `{"paused":true}`, `null`},
		{"both off", `{"router_enabled":false,"library_enabled":false,"notify_enabled":false}`, `null`, `{"paused":true}`, `null`},
		{"keys missing", `{}`, `null`, `{"paused":true}`, `null`},
		{"notify on", `{"router_enabled":true,"library_enabled":true,"notify_enabled":true}`, `{"poll":"15s"}`, `{"paused":false}`, `{"poll":"15s","enabled":true}`},
		{"notify on, null section", `{"router_enabled":true,"library_enabled":true,"notify_enabled":true}`, `null`, `{"paused":false}`, `{"enabled":true}`},
		{"notify on, no section", `{"router_enabled":true,"library_enabled":true,"notify_enabled":true}`, ``, `{"paused":false}`, `{"enabled":true}`},
		{"notify off removes enabled", `{"router_enabled":true,"library_enabled":true,"notify_enabled":false}`, `{"enabled":true,"poll":"15s"}`, `{"paused":false}`, `{"poll":"15s"}`},
		{"notify off, no section", `{"router_enabled":true,"library_enabled":true,"notify_enabled":false}`, ``, `{"paused":false}`, `(none)`},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := run(t, c.switches, c.notify, nil)
			if got.switches != c.wantSwitches || got.notify != c.wantNotify {
				t.Errorf("switches %s notify %s, want %s %s", got.switches, got.notify, c.wantSwitches, c.wantNotify)
			}
		})
	}
	t.Run("libraries", func(t *testing.T) {
		t.Parallel()
		got := run(t, `{"router_enabled":true,"library_enabled":true,"notify_enabled":false}`, `null`, map[string]string{
			"a-watch-grab":   `{"config":{"mode":"shadow","grab":true},"roots":["/a"]}`,
			"b-watch":        `{"config":{"mode":"shadow"}}`,
			"c-live-grab":    `{"config":{"mode":"active","grab":true},"roots":["/c"]}`,
			"d-live-no-grab": `{"config":{"mode":"active","grab":false}}`,
			// The old field was omitted when false and had no default: a Live row without it had downloads off.
			"e-live-missing": `{"config":{"mode":"active","kind":"movie"}}`,
		})
		want := `a-watch-grab={"config":{"mode":"shadow"},"roots":["/a"]} b-watch={"config":{"mode":"shadow"}} ` +
			`c-live-grab={"config":{"mode":"active"},"roots":["/c"]} d-live-no-grab={"config":{"mode":"shadow"}} ` +
			`e-live-missing={"config":{"mode":"shadow","kind":"movie"}}`
		if got.versions != want {
			t.Errorf("libraries\n got %s\nwant %s", got.versions, want)
		}
	})
	// The owner's rows: both switches on, notifications off, five Live libraries with grab on, five Watch only.
	t.Run("owner", func(t *testing.T) {
		t.Parallel()
		versions := map[string]string{}
		for _, k := range []string{"m1", "m2", "m3", "m4", "m5"} {
			versions[k+"-mm"] = `{"config":{"mode":"active","grab":true,"kind":"movie"},"roots":["/x"]}`
			versions[k] = `{"config":{"mode":"shadow","kind":"movie"},"roots":["/y"]}`
		}
		got := run(t, `{"router_enabled":true,"library_enabled":true,"notify_enabled":false}`, `{"targets":{}}`, versions)
		if got.switches != `{"paused":false}` || got.notify != `{"targets":{}}` ||
			strings.Count(got.versions, `"mode":"active"`) != 5 || strings.Count(got.versions, `"mode":"shadow"`) != 5 ||
			strings.Contains(got.versions, "grab") {
			t.Errorf("owner: %+v", got)
		}
	})
}

// 00124: every series library parses with v4plus and takes packs by any_upgradable, so the stored library settings
// sonarr, packs and packs_threshold go; other keys and roots stay.
func TestMigrationStripsParserAndPackFields(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openAt(t, 122)
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO setting_version(key,config,created_at,updated_at) VALUES
		('tv','{"config":{"mode":"active","kind":"series","packs":"threshold","packs_threshold":60,"sonarr":"v5","upgrades":"auto"},"roots":["/media/tv"]}',1,1),
		('anime','{"config":{"kind":"series","packs":"any_upgradable","sonarr":"v4plus"}}',1,1),
		('movies','{"config":{"kind":"movie","mode":"shadow"},"roots":["/media/movies"]}',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 124); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"tv":     `{"config":{"mode":"active","kind":"series","upgrades":"auto"},"roots":["/media/tv"]}`,
		"anime":  `{"config":{"kind":"series"}}`,
		"movies": `{"config":{"kind":"movie","mode":"shadow"},"roots":["/media/movies"]}`,
	} {
		var config string
		if err := st.Reader().QueryRowContext(ctx, `SELECT config FROM setting_version WHERE key=?`, key).Scan(&config); err != nil || config != want {
			t.Errorf("%s config %s, %v; want %s", key, config, err, want)
		}
	}
}
