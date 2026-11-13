package settings

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// Removing a library removes its rows ("Stop managing library", the owner's way to drop his Watch-only mirrors): a
// Watch-only movie-web with versions, file records, offers, wanted rows, tags and its own view rows, next to a Live
// movie-remux that shares its titles and one link. The Live library's links, monitoring and wanted state stay as they
// were; the shared link loses only the removed library and its download; title monitoring is recomputed, and so is
// episode monitoring for a removed series library.
func TestDeleteLibraryRemovesRows(t *testing.T) {
	t.Parallel()
	s, db, p, initial := versionFixture(t, func(p *policy.Policy) {
		p.Versions["movie-remux"].Mode = policy.ModeActive
		p.Versions["movie-web"].Mode = policy.ModeShadow
		p.Versions["tv-2160p"].Mode = policy.ModeActive
	})
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, monitored, created_at, updated_at) VALUES (1, 'movie', 'Shared', 1, 0, 0),
			(2, 'movie', 'WebOnly', 1, 0, 0), (3, 'series', 'Show', 1, 0, 0), (4, 'series', 'AnimeOnly', 1, 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
			(11, 1, 'movie-remux', 0, 'filled', 0, 0), (12, 1, 'movie-web', 1, 'wanted', 0, 0),
			(22, 2, 'movie-web', 1, 'filled', 0, 0), (31, 3, 'tv-2160p', 1, 'wanted', 0, 0),
			(32, 3, 'anime-tv', 1, 'wanted', 0, 0), (42, 4, 'anime-tv', 1, 'wanted', 0, 0)`,
		`INSERT INTO episode (id, title_id, season, number, monitored, created_at, updated_at) VALUES
			(301, 3, 1, 1, 1, 0, 0), (302, 3, 1, 2, 1, 0, 0), (401, 4, 1, 1, 1, 0, 0)`,
		`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at) VALUES
			(31, 301, 0, 'wanted', 0), (32, 301, 1, 'wanted', 0), (31, 302, 1, 'filled', 0), (32, 302, 0, 'wanted', 0),
			(42, 401, 1, 'wanted', 0)`,
		`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
			created_at, updated_at) VALUES (500, 1, 11, 'movie-remux', 'h500', 'Shared.Remux', 'ix', 'imported', 0, 0),
			(501, 1, 12, 'movie-web', 'h501', 'Shared.WEB', 'ix', 'imported', 0, 0),
			(502, 2, 22, 'movie-web', 'h502', 'WebOnly.WEB', 'ix', 'downloading', 0, 0),
			(503, 2, 22, 'movie-web', 'h503', 'WebOnly.2160p', 'ix', 'queued', 0, 0)`,
		`INSERT INTO link (id, path, target, title_id, download_id, created_at, updated_at) VALUES
			(100, '/remux/Shared.mkv', '/mnt/a', 1, 500, 0, 0), (101, '/remux/Shared-web.mkv', '/mnt/b', 1, 501, 0, 0),
			(102, '/web/WebOnly.mkv', '/mnt/c', 2, 502, 0, 0)`,
		`INSERT INTO link_version (link_id, version_id) VALUES (100, 11), (101, 11), (101, 12), (102, 22)`,
		`INSERT INTO version_file (version_id, title_id, instance, arr_file_id, path) VALUES
			(12, 1, 'radarr_web', 1, '/arr/Shared.mkv'), (22, 2, 'radarr_web', 2, '/arr/WebOnly.mkv')`,
		`INSERT INTO router_offer (version_key, title_id, info_hash, from_version, outcome, first_at, updated_at) VALUES
			('movie-remux', 1, 'h1', 'movie-web', 'pending', 0, 0), ('movie-web', 1, 'h2', 'movie-remux', 'pending', 0, 0),
			('movie-remux', 1, 'h3', 'movie-remux', 'same', 0, 0)`,
		`INSERT INTO search_request (title_id, version_key, reason, created_at) VALUES (1, 'movie-web', 'missing', 0),
			(1, 'movie-remux', 'cutoff', 0)`,
		`INSERT INTO arrview_tag (id, label, created_at) VALUES (1, 'kids', 0)`,
		`INSERT INTO arrview_title_tag (version, title_id, tag_id) VALUES ('movie-web', 1, 1), ('movie-remux', 1, 1)`,
		`INSERT INTO arrview_title (view, title_id, minimum_availability, added_at, updated_at) VALUES
			('v/movie-web', 1, 'announced', 1, 1), ('v/movie-remux', 1, 'inCinemas', 1, 1)`,
		`INSERT INTO movie_collection (tmdb_id, versions, updated_at) VALUES (900, '["movie-remux","movie-web"]', 0)`,
	} {
		if _, err := db.Writer().Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	live := func() string {
		t.Helper()
		var out []string
		for _, q := range []string{
			`SELECT id || policy_key || monitored || state FROM version WHERE policy_key = 'movie-remux'`,
			`SELECT l.id || l.path || l.target || l.state || ':' || lv.version_id FROM link l JOIN link_version lv
				ON lv.link_id = l.id WHERE lv.version_id = 11`,
			`SELECT id || state FROM download WHERE version_key = 'movie-remux'`,
			`SELECT version_key || from_version || info_hash || outcome FROM router_offer WHERE version_key = 'movie-remux'
				AND from_version = 'movie-remux'`,
			`SELECT version_key || reason FROM search_request WHERE version_key = 'movie-remux'`,
			`SELECT view || minimum_availability FROM arrview_title WHERE view = 'v/movie-remux'`,
			`SELECT version || title_id FROM arrview_title_tag WHERE version = 'movie-remux'`,
		} {
			rows, err := db.Reader().Query(q)
			if err != nil {
				t.Fatal(err)
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
	before := live()

	req := saveRequest(p, initial)
	req.Records.Versions = slices.DeleteFunc(req.Records.Versions, func(v Version) bool { return v.Key == "movie-web" })
	for i, v := range req.Records.Views {
		if slices.Contains(v.Versions, "movie-web") {
			v.Versions = slices.DeleteFunc(slices.Clone(v.Versions), func(k string) bool { return k == "movie-web" })
			v.Profiles = nil
			req.Records.Views[i] = v
		}
	}
	m := versionCoordinator(t, s, p, initial)
	proof, err := m.Confirm(ctx, req)
	if err != nil || len(proof.Effects) != 1 || proof.Effects[0].Effect != "Library movie-web is removed with its "+
		"entries on every title (monitoring, wanted state, history). 1 linked files stay on disk and are no longer "+
		"tracked. 1 downloads in progress stay at the provider or client and are no longer tracked. 1 queued "+
		"downloads that were not sent yet are dropped." {
		t.Fatalf("confirmation: %+v %v", proof, err)
	}
	req.ConfirmationToken = proof.Token
	if _, err := m.SaveAndApply(ctx, req); err != nil {
		t.Fatal(err)
	}
	if after := live(); after != before {
		t.Errorf("the Live library changed:\nbefore\n%s\nafter\n%s", before, after)
	}
	// A series library removed the same way (the store call): a shared series' episodes follow the remaining Live
	// library; a series that had only the removed library ends unmonitored, episodes included.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error { return removeLibraryRows(ctx, tx, "anime-tv", 9) }); err != nil {
		t.Fatal(err)
	}
	str := func(q string) string {
		t.Helper()
		var s sql.NullString
		if err := db.Reader().QueryRow(q).Scan(&s); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return s.String
	}
	for q, want := range map[string]string{
		`SELECT group_concat(id, ' ') FROM (SELECT id FROM version ORDER BY id)`:                                                                 "11 31",
		`SELECT group_concat(id || ':' || COALESCE(download_id, '-'), ' ') FROM (SELECT * FROM link ORDER BY id)`:                                "100:500 101:-",
		`SELECT group_concat(link_id || ':' || version_id, ' ') FROM link_version`:                                                               "100:11 101:11",
		`SELECT group_concat(id, ' ') FROM download`:                                                                                             "500",
		`SELECT COUNT(*) FROM version_file`:                                                                                                      "0",
		`SELECT group_concat(info_hash, ' ') FROM router_offer`:                                                                                  "h3",
		`SELECT group_concat(version_key, ' ') FROM search_request`:                                                                              "movie-remux",
		`SELECT group_concat(version, ' ') FROM arrview_title_tag`:                                                                               "movie-remux",
		`SELECT group_concat(view, ' ') FROM arrview_title`:                                                                                      "v/movie-remux",
		`SELECT versions FROM movie_collection`:                                                                                                  `["movie-remux"]`,
		`SELECT group_concat(id || ':' || monitored, ' ') FROM (SELECT * FROM title ORDER BY id)`:                                                "1:0 2:0 3:1 4:0",
		`SELECT group_concat(id || ':' || monitored, ' ') FROM (SELECT * FROM episode ORDER BY id)`:                                              "301:0 302:1 401:0",
		`SELECT group_concat(version_id || ':' || episode_id || ':' || monitored, ' ') FROM (SELECT * FROM episode_version_state ORDER BY 1, 2)`: "31:301:0 31:302:1",
		`SELECT COUNT(*) FROM pragma_foreign_key_check`:                                                                                          "0",
	} {
		if got := str(q); got != want {
			t.Errorf("%s\n got %s\nwant %s", q, got, want)
		}
	}
}
