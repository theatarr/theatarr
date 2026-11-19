package rss

import (
	"fmt"
	"strings"
	"testing"

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
