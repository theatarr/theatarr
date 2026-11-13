package store_test

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// arrFixture is an install at 00124 shaped like the owner's, plus what other installs can hold:
//
//   - Live libraries movie-mm (own profile mm-prof) and tv-mm, a Watch-only mirror movie-mirror (named under
//     import.arr, file records, no links), a Live arr-fed movie-fed whose profile (fed-prof) and naming are taken from
//     the arr radarr_fed, and an unused profile taken from an arr (arr-mirror);
//   - rows of movie-old and tv-old, libraries no longer in the settings: a link held only by movie-old, a link shared
//     with movie-mm that points at a movie-old download, offers, wanted rows, tags, view rows, a collection key;
//   - arr rows: instances, snapshots, titles (minimum availability of the fed and mirror copies), history pointing at
//     a gone version, the import section with its credential, a view's alias_from, arr.import jobs and their card.
//
// Titles: 1 Shared (11 movie-mm, 12 movie-mirror, 13 movie-old), 2 Fed (21 movie-fed, 22 movie-mm), 3 Show (31
// tv-mm, 32 tv-old), 4 Old (41 movie-old).
var arrFixture = []string{
	`INSERT INTO settings_generation (generation, revision, created_at) VALUES (1, 'r', 0)`,
	`INSERT INTO setting_version (key, config, created_at, updated_at) VALUES
		('movie-mm', '{"config":{"mode":"active","kind":"movie","profile":"mm-prof"}}', 1, 1),
		('movie-mirror', '{"config":{"mode":"shadow","kind":"movie","profile":"mm-prof"}}', 1, 1),
		('movie-fed', '{"config":{"mode":"active","kind":"movie","profile":"fed-prof","minimum_availability":"announced"}}', 1, 1),
		('tv-mm', '{"config":{"mode":"active","kind":"series","profile":"tv-prof"}}', 1, 1)`,
	`INSERT INTO setting_quality_profile (key, config, created_at, updated_at) VALUES
		('mm-prof', '{"Cutoff":"Remux-2160p","FromArr":""}', 1, 1), ('tv-prof', '{"Cutoff":"WEBDL-2160p"}', 1, 1),
		('fed-prof', '{"FromArr":"radarr_fed/HD-1080p"}', 1, 1), ('arr-mirror', '{"FromArr":"radarr_mirror/Any"}', 1, 1)`,
	`INSERT INTO setting_version_naming (key, config, created_at, updated_at) VALUES
		('movie-mm', '{"Folder":"{Movie Title}","FromArr":""}', 1, 1), ('movie-fed', '{"FromArr":"radarr_fed"}', 1, 1)`,
	`INSERT INTO setting_secret (id, value, created_at, updated_at) VALUES (7, 'arr-keys', 1, 1), (8, 'notify-key', 1, 1)`,
	`INSERT INTO setting_section (key, config, secret_id, created_at, updated_at) VALUES
		('import', '{"arr":{"radarr_mirror":{"flavour":"radarr","url":"http://m","version":"movie-mirror"},
			"radarr_fed":{"flavour":"radarr","url":"http://f","version":"movie-fed"}}}', 7, 1, 1),
		('notify', '{}', 8, 1, 1)`,
	`INSERT INTO setting_view_binding (key, config, created_at, updated_at) VALUES
		('radarr', '{"identity":{"alias_from":["radarr_mirror"],"name":"Radarr"}}', 1, 1)`,
	`INSERT INTO title (id, kind, name, created_at, updated_at, monitored) VALUES (1, 'movie', 'Shared', 0, 0, 1),
		(2, 'movie', 'Fed', 0, 0, 1), (3, 'series', 'Show', 0, 0, 1), (4, 'movie', 'Old', 0, 0, 1),
		(5, 'series', 'OldShow', 0, 0, 1)`,
	`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
		(11, 1, 'movie-mm', 0, 'filled', 0, 0), (12, 1, 'movie-mirror', 1, 'filled', 0, 0),
		(13, 1, 'movie-old', 1, 'wanted', 0, 0), (21, 2, 'movie-fed', 1, 'wanted', 0, 0),
		(22, 2, 'movie-mm', 1, 'wanted', 0, 0), (31, 3, 'tv-mm', 0, 'wanted', 0, 0),
		(32, 3, 'tv-old', 1, 'wanted', 0, 0), (41, 4, 'movie-old', 1, 'wanted', 0, 0),
		(52, 5, 'tv-old', 1, 'wanted', 0, 0)`,
	`INSERT INTO episode (id, title_id, season, number, monitored, created_at, updated_at) VALUES
		(301, 3, 1, 1, 1, 0, 0), (302, 3, 1, 2, 1, 0, 0), (303, 3, 1, 3, 1, 0, 0), (501, 5, 1, 1, 1, 0, 0)`,
	`INSERT INTO episode_version_state (version_id, episode_id, monitored, updated_at) VALUES
		(31, 301, 0, 0), (32, 301, 1, 0), (31, 302, 1, 0), (32, 302, 0, 0), (32, 303, 0, 0), (52, 501, 1, 0)`,
	`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state, created_at,
		updated_at) VALUES (500, 1, 11, 'movie-mm', 'h500', 'Shared.2160p', 'ix', 'imported', 0, 0),
		(501, 1, 13, 'movie-old', 'h501', 'Shared.1080p', 'ix', 'imported', 0, 0),
		(502, 4, 41, 'movie-old', 'h502', 'Old.1080p', 'ix', 'downloading', 0, 0)`,
	`INSERT INTO link (id, path, target, title_id, download_id, created_at, updated_at) VALUES
		(100, '/mm/Shared.mkv', '/mnt/a', 1, 500, 0, 0), (101, '/mm/Shared-1080p.mkv', '/mnt/b', 1, 501, 0, 0),
		(102, '/old/Old.mkv', '/mnt/c', 4, 502, 0, 0)`,
	`INSERT INTO link_version (link_id, version_id) VALUES (100, 11), (101, 11), (101, 13), (102, 41)`,
	`INSERT INTO version_source (version_id, instance, arr_id) VALUES (12, 'radarr_mirror', 1), (21, 'radarr_fed', 1)`,
	`INSERT INTO version_file (version_id, title_id, instance, arr_file_id, path) VALUES
		(12, 1, 'radarr_mirror', 1, '/arr/Shared.mkv'), (21, 2, 'radarr_fed', 2, '/fed/Fed.mkv')`,
	`INSERT INTO router_offer (version_key, title_id, info_hash, from_version, outcome, first_at, updated_at) VALUES
		('movie-mm', 1, 'h1', 'movie-old', 'pending', 0, 0), ('movie-old', 4, 'h2', 'movie-mm', 'pending', 0, 0),
		('movie-mm', 2, 'h3', 'movie-fed', 'routed', 0, 0)`,
	`INSERT INTO search_request (title_id, version_key, reason, created_at) VALUES (1, 'movie-old', 'missing', 0),
		(2, 'movie-mm', 'missing', 0)`,
	`INSERT INTO arrview_tag (id, label, created_at) VALUES (1, 'kids', 0)`,
	`INSERT INTO arrview_title_tag (version, title_id, tag_id) VALUES ('movie-old', 1, 1), ('movie-mm', 1, 1)`,
	// A blank list-synced row of the mirror, a choice of movie-mm, the gone library's row.
	`INSERT INTO arrview_title (view, title_id, minimum_availability, added_at, updated_at) VALUES
		('v/movie-mirror', 1, '', 1, 9), ('v/movie-mm', 2, 'tba', 1, 3), ('v/movie-old', 1, 'announced', 1, 4)`,
	`INSERT INTO movie_collection (tmdb_id, versions, updated_at) VALUES (900, '["movie-mm","movie-old"]', 0),
		(901, '["movie-mm"]', 0)`,
	`INSERT INTO arr_instance (name, flavour, policy_key, created_at, updated_at) VALUES
		('radarr_mirror', 'radarr', 'movie-mirror', 0, 0), ('radarr_fed', 'radarr', 'movie-fed', 0, 0),
		('radarr_other', 'radarr', 'movie-mm', 0, 0)`,
	`INSERT INTO arr_policy_snapshot (instance, body, fetched_at) VALUES ('radarr_mirror', '{"qualityprofile":[]}', 0),
		('radarr_fed', '{"qualityprofile":[{"name":"HD-1080p"}]}', 0)`,
	`INSERT INTO arr_title (instance, arr_id, kind, title_id, version_id, monitored, minimum_availability) VALUES
		('radarr_fed', 1, 'movie', 2, 21, 1, 'inCinemas'), ('radarr_mirror', 1, 'movie', 1, 12, 1, 'announced'),
		('radarr_other', 5, 'movie', 2, 22, 1, 'inCinemas'), ('radarr_other', 6, 'movie', 1, 11, 1, 'released'),
		('radarr_other', 7, 'movie', 1, 13, 1, 'inCinemas'), ('radarr_other', 8, 'series', 3, 31, 1, '')`,
	`INSERT INTO arr_history (instance, arr_id, event_type, date_at, title_id, version_id) VALUES
		('radarr_other', 1, 'grabbed', 0, 1, 13)`,
	`INSERT INTO arr_tag (instance, arr_id, label) VALUES ('radarr_other', 1, 'kids')`,
	`INSERT INTO arr_blocklist (instance, version_key, info_hash, release_title) VALUES ('radarr_other', 'movie-mm', 'h9', 'x')`,
	`INSERT INTO arr_alias (instance, kind, arr_id, target_id, created_at) VALUES ('radarr_other', 'movie', 1, 1, 0)`,
	`INSERT INTO import_run (instance, started_at, finished_at) VALUES ('radarr_other', 0, 0)`,
	`INSERT INTO library_naming (instance, naming, fetched_at) VALUES ('radarr_fed', '{}', 0)`,
	`INSERT INTO job (kind, state, run_at, created_at, updated_at) VALUES ('arr.import', 'queued', 0, 0, 0),
		('arr.import', 'failed', 0, 0, 0), ('plex.scan', 'queued', 0, 0, 0)`,
	`INSERT INTO error_event (fingerprint, source, component, message, first_seen, last_seen, updated_at) VALUES
		('f1', 'job', 'arr.import', 'Import from Sonarr/Radarr failed', 0, 0, 0), ('f2', 'job', 'plex.scan', 'x', 0, 0, 0)`,
}

// liveState is everything of the Live libraries (movie-mm, movie-fed, tv-mm) the migration must leave alone: their
// versions, wanted and episode states, links (but the download a gone library's download held), downloads, offers
// and wanted rows between them, their own view rows, profiles and naming.
func liveState(t *testing.T, db *sql.DB) string {
	t.Helper()
	live := `('movie-mm', 'movie-fed', 'tv-mm')`
	var out []string
	for _, q := range []string{
		`SELECT id || policy_key || monitored || state FROM version WHERE policy_key IN ` + live,
		`SELECT s.version_id || ':' || s.episode_id || s.monitored || s.state FROM episode_version_state s
			JOIN version v ON v.id = s.version_id WHERE v.policy_key IN ` + live,
		`SELECT l.id || l.path || l.target || l.state || l.title_id || ':' || lv.version_id FROM link l
			JOIN link_version lv ON lv.link_id = l.id JOIN version v ON v.id = lv.version_id WHERE v.policy_key IN ` + live,
		`SELECT id || version_key || state FROM download WHERE version_key IN ` + live,
		`SELECT version_key || title_id || info_hash || from_version || outcome FROM router_offer
			WHERE version_key IN ` + live + ` AND from_version IN ` + live,
		`SELECT title_id || version_key || reason FROM search_request WHERE version_key IN ` + live,
		`SELECT version || title_id FROM arrview_title_tag WHERE version IN ` + live,
		`SELECT view || title_id || minimum_availability || updated_at FROM arrview_title WHERE view IN
			('v/movie-mm', 'v/movie-fed', 'v/tv-mm') AND title_id <> 2`,
		`SELECT key || config FROM setting_version WHERE key IN ` + live,
		`SELECT key || config FROM setting_quality_profile WHERE key IN ('mm-prof', 'tv-prof')`,
		`SELECT key || config FROM setting_version_naming WHERE key = 'movie-mm'`,
		`SELECT version_id || path FROM version_file`,
	} {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		rows.Close()
	}
	slices.Sort(out)
	return strings.Join(out, "\n")
}

// oldMinimum is the minimum availability of a movie copy as Theatarr read it before 00125 (internal/availability
// Rules with the arr's value): the explicit choice, else the first arr record's value (the owning instance first),
// else the library's default, else released.
func oldMinimum(t *testing.T, db *sql.DB, pol *policy.Policy, titleID, versionID int64, key string) string {
	t.Helper()
	minimum := "released"
	if vp := pol.Versions[key]; vp != nil && vp.MinimumAvailability != "" {
		minimum = vp.MinimumAvailability
	}
	var imported string
	err := db.QueryRow(`SELECT a.minimum_availability FROM arr_title a LEFT JOIN version_source vs
		ON vs.version_id = a.version_id AND vs.instance = a.instance AND vs.arr_id = a.arr_id
		WHERE a.version_id = ? ORDER BY (vs.instance IS NOT NULL) DESC, a.instance LIMIT 1`, versionID).Scan(&imported)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if imported != "" {
		minimum = imported
	}
	var choice string
	err = db.QueryRow(`SELECT minimum_availability FROM arrview_title WHERE title_id = ? AND view = ?
		AND minimum_availability <> '' ORDER BY updated_at DESC LIMIT 1`, titleID, "v/"+key).Scan(&choice)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if choice != "" {
		minimum = choice
	}
	return minimum
}

func seedArr(t *testing.T, st *store.Store, bootstrapped bool) {
	t.Helper()
	qs := arrFixture
	if bootstrapped {
		qs = append(slices.Clone(qs), `INSERT INTO settings_e3_bootstrap (id, generation) VALUES (1, 1)`)
	}
	for _, q := range qs {
		if _, err := st.Writer().Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func query(t *testing.T, db *sql.DB, q string) string {
	t.Helper()
	var s sql.NullString
	if err := db.QueryRow(q).Scan(&s); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return s.String
}

// 00125 on the owner's shape with libraries stored in the database: every step of the migration, nothing of a Live
// library changes, and every movie copy keeps its minimum availability.
func TestMigrationArrConnections(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.OpenAt(t, 124)
	seedArr(t, st, true)
	db := st.Reader()
	before := liveState(t, db)
	pol := &policy.Policy{Versions: map[string]*policy.VersionPolicy{"movie-mm": {}, "movie-mirror": {},
		"movie-fed": {MinimumAvailability: "announced"}}}
	copies := map[int64][2]any{11: {int64(1), "movie-mm"}, 12: {int64(1), "movie-mirror"},
		21: {int64(2), "movie-fed"}, 22: {int64(2), "movie-mm"}}
	wantMinimum := map[int64]string{}
	for id, c := range copies {
		wantMinimum[id] = oldMinimum(t, db, pol, c[0].(int64), id, c[1].(string))
	}
	if err := st.UpTo(ctx, 125); err != nil {
		t.Fatal(err)
	}
	if after := liveState(t, db); after != before {
		t.Errorf("Live libraries changed:\nbefore\n%s\nafter\n%s", before, after)
	}
	rules, err := availability.Rules(ctx, db, pol, []int64{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range wantMinimum {
		if rules[id].Minimum != want {
			t.Errorf("version %d: minimum availability %q, was %q", id, rules[id].Minimum, want)
		}
	}
	for q, want := range map[string]string{
		// 1, 2: the jobs and their card go; one card for the Watch-only mirror, none for the Live arr-fed library.
		`SELECT group_concat(kind || ':' || state, ' ') FROM job`:                                                        "plex.scan:queued",
		`SELECT group_concat(fingerprint, ' ') FROM (SELECT fingerprint FROM error_event ORDER BY fingerprint)`:          "arr-mirror:movie-mirror f2",
		`SELECT source || ' ' || level || ' ' || message FROM error_event WHERE fingerprint = 'arr-mirror:movie-mirror'`: "log error Library movie-mirror copied a Sonarr/Radarr instance. That copy is no longer updated. Delete the library when you no longer need it.",
		// 3: the section and its credential go, another section's stays; alias_from goes.
		`SELECT group_concat(key, ' ') FROM setting_section`:           "notify",
		`SELECT group_concat(id, ' ') FROM setting_secret`:             "8",
		`SELECT config FROM setting_view_binding WHERE key = 'radarr'`: `{"identity":{"name":"Radarr"}}`,
		// 4: the arr's value becomes the copy's own choice: fed (differs from its default), the mirror's blank row
		// filled; movie-mm's own choice kept; a value equal to the default needs no row; the gone library's row goes.
		`SELECT group_concat(view || ':' || title_id || ':' || minimum_availability || ':' || updated_at, ' ') FROM
			(SELECT * FROM arrview_title ORDER BY view, title_id)`: "v/movie-fed:2:inCinemas:0 v/movie-mirror:1:announced:0 v/movie-mm:2:tba:3",
		// 5, 6: the unused profile from an arr goes; the used one waits for FreezeStored with its instance's snapshot.
		`SELECT group_concat(key, ' ') FROM (SELECT key FROM setting_quality_profile ORDER BY key)`: "fed-prof mm-prof tv-prof",
		`SELECT group_concat(instance || ':' || flavour || ':' || body, ' ') FROM arr_frozen_input`: `radarr_fed:radarr:{"qualityprofile":[{"name":"HD-1080p"}]}`,
		// 7.
		`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('arr_tag', 'arr_quality_profile', 'arr_policy_snapshot',
			'arr_blocklist', 'arr_history', 'arr_alias', 'arr_title', 'arr_instance', 'import_run', 'library_naming')`: "0",
		// 8: the gone libraries' rows; the shared link stays for movie-mm without the gone download.
		`SELECT group_concat(id, ' ') FROM (SELECT id FROM version ORDER BY id)`:                                  "11 12 21 22 31",
		`SELECT group_concat(id || ':' || COALESCE(download_id, '-'), ' ') FROM (SELECT * FROM link ORDER BY id)`: "100:500 101:-",
		`SELECT group_concat(id, ' ') FROM download`:                                                              "500",
		`SELECT group_concat(version_key || '<' || from_version, ' ') FROM router_offer`:                          "movie-mm<movie-fed",
		`SELECT group_concat(version_key, ' ') FROM search_request`:                                               "movie-mm",
		`SELECT group_concat(version, ' ') FROM arrview_title_tag`:                                                "movie-mm",
		`SELECT group_concat(tmdb_id || versions, ' ') FROM movie_collection`:                                     `900["movie-mm"] 901["movie-mm"]`,
		`SELECT group_concat(id || ':' || monitored, ' ') FROM (SELECT * FROM title ORDER BY id)`:                 "1:1 2:1 3:0 4:0 5:0",
		`SELECT group_concat(id || ':' || monitored, ' ') FROM (SELECT * FROM episode ORDER BY id)`:               "301:0 302:1 303:1 501:0",
		`SELECT COUNT(*) FROM pragma_foreign_key_check`:                                                           "0",
	} {
		if got := query(t, db, q); got != want {
			t.Errorf("%s\n got %s\nwant %s", q, got, want)
		}
	}
}

// Before the libraries are stored in the database (no settings_e3_bootstrap) nothing of a library is deleted and no
// profile is taken for unused, so every profile from an arr keeps its snapshot. An empty database migrates too.
func TestMigrationArrConnectionsBeforeBootstrap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	empty := store.OpenAt(t, 124)
	if err := empty.UpTo(ctx, 125); err != nil {
		t.Fatal(err)
	}
	if got := query(t, empty.Reader(), `SELECT COUNT(*) FROM arr_frozen_input`); got != "0" {
		t.Fatalf("empty database: %s frozen inputs", got)
	}
	st := store.OpenAt(t, 124)
	seedArr(t, st, false)
	if err := st.UpTo(ctx, 125); err != nil {
		t.Fatal(err)
	}
	db := st.Reader()
	for q, want := range map[string]string{
		`SELECT group_concat(id, ' ') FROM (SELECT id FROM version ORDER BY id)`:                                  "11 12 13 21 22 31 32 41 52",
		`SELECT group_concat(id || ':' || COALESCE(download_id, '-'), ' ') FROM (SELECT * FROM link ORDER BY id)`: "100:500 101:501 102:502",
		`SELECT COUNT(*) FROM router_offer`:                                                                 "3",
		`SELECT group_concat(tmdb_id || versions, ' ') FROM movie_collection`:                               `900["movie-mm","movie-old"] 901["movie-mm"]`,
		`SELECT group_concat(key, ' ') FROM (SELECT key FROM setting_quality_profile ORDER BY key)`:         "arr-mirror fed-prof mm-prof tv-prof",
		`SELECT group_concat(instance, ' ') FROM (SELECT instance FROM arr_frozen_input ORDER BY instance)`: "radarr_fed radarr_mirror",
		`SELECT group_concat(id || ':' || monitored, ' ') FROM title`:                                       "1:1 2:1 3:1 4:1 5:1",
	} {
		if got := query(t, db, q); got != want {
			t.Errorf("%s\n got %s\nwant %s", q, got, want)
		}
	}
}
