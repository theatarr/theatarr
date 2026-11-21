package rss

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

func containsReq(reqs []string, part string) bool {
	for _, r := range reqs {
		if strings.Contains(r, part) {
			return true
		}
	}
	return false
}

// An RSS item in a TV category is matched to a series by a scene mapping name and routed with its episodes; the feed
// is asked for movies and TV.
func TestRSSSeriesBySceneName(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	e.seed(t)
	e.seedShow(t)
	// A same-name remake whose alias Sonarr limits to its own releases ("Limited matching"): the name matches only
	// the series with the plain alias.
	at := store.Millis(t0)
	e.exec(t, `INSERT INTO title (id, kind, name, sort_name, year, tvdb_id, imdb_id, monitored, series_type,
		runtime_minutes, created_at, updated_at) VALUES (6, 'series', 'The Show Remake', 'show remake', 2025, 1002,
		'tt1001002', 1, 'standard', 45, ?, ?)`, at, at)
	e.exec(t, `INSERT INTO title_scene_mapping (title_id, source, name, scene_season_number, scene_origin, comment)
		VALUES (6, 'tvrefresh:sonarr', 'Show Alt', -1, '', 'Limited matching')`)
	e.exec(t, `INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at)
		VALUES (10, 6, 'tv', 1, 'wanted', ?, ?)`, at, at)
	e.prow.setRSS(item{title: "Show.Alt.S01E03.2160p.WEB-DL-GRP", hash: hShowE03, size: 8e9, pub: t0, tv: true})
	rs, err := e.r.RSS(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Routed != 1 || len(e.rec.reqs) != 1 || rs.Decided != 1 {
		t.Fatalf("rss %+v", rs)
	}
	if req := e.rec.reqs[0]; req.Version != "tv" || len(req.Candidates[0].Episodes) != 1 {
		t.Errorf("route %+v", req)
	}
	if reqs := e.net.requests("prowlarr.test/1/api"); !containsReq(reqs, "cat=2000%2C5000") {
		t.Errorf("rss categories %v", reqs)
	}
}

// An item with no TVDB id whose name finds no series is matched by its IMDb id when exactly one catalog series has
// it (ids compared normalised: "tt100100" stored, "tt0100100" on the feed), and the decision checks the episode as
// usual. With a second series of that IMDb id, monitored in a library or not, it is matched to neither.
func TestRSSSeriesByIMDbFallback(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, imdb string
		other      string // "": no other series; else the other series' version state
		routed     bool
	}{
		{"unique", "tt1001001", "", true},
		{"shared", "tt1001001", "monitored", false},
		{"shared with an unmonitored series", "tt1001001", "unmonitored", false},
		{"unpadded stored id", "tt0100100", "", true},
	} {
		e := newEnv(t, false)
		e.seed(t)
		e.seedShow(t)
		if tc.imdb == "tt0100100" {
			e.exec(t, `UPDATE title SET imdb_id = 'tt100100' WHERE id = 5`)
		}
		if tc.other != "" {
			at := store.Millis(t0)
			e.exec(t, `INSERT INTO title (id, kind, name, sort_name, year, tvdb_id, imdb_id, monitored, series_type,
				runtime_minutes, created_at, updated_at) VALUES (6, 'series', 'Other Show', 'other show', 2020, 1002,
				'tt1001001', 1, 'standard', 45, ?, ?)`, at, at)
			if tc.other == "monitored" {
				e.exec(t, `INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at)
					VALUES (10, 6, 'tv', 1, 'wanted', ?, ?)`, at, at)
			}
		}
		e.prow.setRSS(item{title: "La.Serie.S01E03.2160p.WEB-DL-GRP", hash: hShowE03, size: 8e9, pub: t0, tv: true,
			imdb: tc.imdb})
		rs, err := e.r.RSS(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !tc.routed {
			if rs.Matched != 0 || len(e.rec.reqs) != 0 {
				t.Errorf("%s: rss %+v", tc.name, rs)
			}
			continue
		}
		if rs.Routed != 1 || len(e.rec.reqs) != 1 {
			t.Errorf("%s: rss %+v", tc.name, rs)
			continue
		}
		if req := e.rec.reqs[0]; req.Version != "tv" || len(req.Candidates[0].Episodes) != 1 {
			t.Errorf("%s: route %+v", tc.name, req)
		}
	}
}

func TestRSSCategoriesIncludeAnimeForConfiguredAnimeVersion(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	anime := true
	e.r.pol.Versions["tv"].Anime = &anime
	if got := e.r.rssCategories("prowlarr"); fmt.Sprint(got) != "[2000 5000 5070]" {
		t.Fatalf("RSS categories %v", got)
	}
}

// An RSS item without ids that names a mapped sequel (an anime_entry name no series or scene mapping has) is matched
// by that name when it points to exactly one series across the catalogue and every anime mapping, and routed only
// when the decision placed it through the franchise: "Show Sequel - 01" is the sequel's first episode, S02E01. The
// name proves the series, not the season: the same name with "S01E02", an entry not approved, and a name another
// catalogue title or another series' mapping also has are dropped, never numbered series-wide.
func TestRSSSeriesByFranchiseName(t *testing.T) {
	t.Parallel()
	const placed = "[GRP] Show Sequel - 01 (2160p WEB-DL)"
	for _, tc := range []struct {
		name, title, status string
		setup               string // extra SQL
		routed              bool
	}{
		{"sequel placed", placed, "auto", "", true},
		{"sequel named with S01E02", "Show.Sequel.S01E02.2160p.WEB-DL-GRP", "auto", "", false},
		{"conflict entry", placed, "conflict", "", false},
		{"rejected entry", placed, "rejected", "", false},
		{"name of another catalogue title", placed, "auto",
			`INSERT INTO title (id, kind, name, sort_name, year, tvdb_id, monitored, series_type, created_at, updated_at)
			VALUES (6, 'series', 'Show Sequel', 'show sequel', 2024, 1002, 0, 'anime', 0, 0)`, false},
		{"scene name of another catalogue title", placed, "auto",
			`INSERT INTO title (id, kind, name, sort_name, year, tvdb_id, monitored, series_type, created_at, updated_at)
			VALUES (6, 'series', 'Another Show', 'another show', 2024, 1002, 0, 'anime', 0, 0);
			INSERT INTO title_scene_mapping (title_id, source, name, scene_season_number, scene_origin, comment)
			VALUES (6, 'test', 'Show Sequel', -1, '', '')`, false},
		{"entry name in another series' mapping", placed, "auto",
			`INSERT INTO title (id, kind, name, sort_name, year, tvdb_id, monitored, series_type, created_at, updated_at)
			VALUES (6, 'series', 'Another Show', 'another show', 2024, 1002, 0, 'anime', 0, 0);
			INSERT INTO anime_franchise (slug, name, seed_anilist, created_at, updated_at) VALUES ('g', 'G', 31, 0, 0);
			INSERT INTO anime_entry (franchise, key, origin, anilist_id, title, tmdb_movie, body)
			VALUES ('g', 'al:31', 'anilist', 31, 'Show Sequel', '[]', '{}');
			INSERT INTO anime_mapping (franchise, entry_key, status, tvdb_series, episodes, updated_at)
			VALUES ('g', 'al:31', 'conflict', 1002, '[[1,1]]', 0)`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, false)
			e.seed(t)
			e.seedShow(t)
			at := store.Millis(t0)
			e.exec(t, `UPDATE title SET series_type = 'anime' WHERE id = 5`)
			for n := 1; n <= 2; n++ {
				air := time.Date(2026, 9, 10+n, 2, 0, 0, 0, time.UTC)
				e.exec(t, `INSERT INTO episode (id, title_id, season, number, name, air_date, air_date_utc_at,
					runtime_minutes, monitored, created_at, updated_at) VALUES (?, 5, 2, ?, ?, ?, ?, 45, 1, ?, ?)`,
					3+n, n, fmt.Sprint("S2 Ep ", n),
					air.Format(time.DateOnly), store.Millis(air), at, at)
				e.exec(t, `INSERT INTO episode_version_state (version_id, episode_id, monitored, state, cutoff_met,
					updated_at) VALUES (9, ?, 1, 'wanted', 0, ?)`, 3+n, at)
			}
			e.exec(t, `INSERT INTO anime_franchise (slug, name, seed_anilist, created_at, updated_at) VALUES ('f', 'F', 21, 0, 0)`)
			e.exec(t, `INSERT INTO anime_entry (franchise, key, origin, anilist_id, title, tmdb_movie, body)
				VALUES ('f', 'al:21', 'anilist', 21, 'Show Sequel', '[]', '{}')`)
			e.exec(t, `INSERT INTO anime_mapping (franchise, entry_key, status, tvdb_series, episodes, updated_at)
				VALUES ('f', 'al:21', ?, 1001, '[[2,1],[2,2]]', 0)`, tc.status)
			for _, q := range strings.Split(tc.setup, ";") {
				if strings.TrimSpace(q) != "" {
					e.exec(t, q)
				}
			}
			e.prow.setRSS(item{title: tc.title, hash: hShowE03, size: 8e9, pub: t0, tv: true})
			rs, err := e.r.RSS(e.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.routed {
				if rs.Routed != 0 || len(e.rec.reqs) != 0 {
					t.Fatalf("rss %+v, reqs %+v", rs, e.rec.reqs)
				}
				return
			}
			if rs.Routed != 1 || len(e.rec.reqs) != 1 {
				t.Fatalf("rss %+v", rs)
			}
			if req := e.rec.reqs[0]; req.Version != "tv" || fmt.Sprint(req.Candidates[0].Episodes) != "[4]" {
				t.Errorf("route %+v, want episode 4 (S02E01)", req.Candidates[0])
			}
		})
	}
}
