package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/arrview"
	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/tvdb"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/web"
)

func seriesPolicy(anime bool) func(*policy.Policy) {
	return func(p *policy.Policy) {
		v := p.Versions["movie"]
		v.Kind, v.Anime = policy.KindSeries, &anime
		v.Naming = policy.Naming{Folder: "{Series CleanTitleWithoutYear} {(Series Year)}", Season: "Season {season:00}", File: "{Series CleanTitleWithoutYear} - S{season:00}E{episode:00} - {Episode CleanTitle:90} {[Quality Full]}{-Release Group}"}
		p.Versions["tv"] = v
		delete(p.Versions, "movie")
		p.Library.Versions["tv"] = p.Library.Versions["movie"]
		delete(p.Library.Versions, "movie")
		p.Profiles["movie"].Qualities = []policy.QualityItem{{Name: "Bluray-2160p Remux"}, {Name: "WEBDL-2160p"}, {Name: "HDTV-1080p"}}
		p.Profiles["movie"].Cutoff = "Bluray-2160p Remux"
	}
}

func TestSeries(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, seriesPolicy(false))
	// Reuse tvrefresh's recorded TVDB responses, trimmed to the four episodes exercised here.
	for _, suffix := range []string{"series", "default_0", "absolute_0"} {
		b, err := os.ReadFile("../metadata/tvrefresh/testdata/371980_" + suffix + ".json")
		must(t, err)
		path := "/series/371980/extended"
		if suffix != "series" {
			var doc struct {
				Data struct {
					Episodes []tvdb.Episode `json:"episodes"`
				} `json:"data"`
			}
			must(t, json.Unmarshal(b, &doc))
			var eps []tvdb.Episode
			for _, ep := range doc.Data.Episodes {
				if ep.SeasonNumber == 1 && ep.Number <= 3 || ep.SeasonNumber == 2 && ep.Number == 1 {
					eps = append(eps, ep)
				}
			}
			b = episodeResponse(t, eps)
			path = "/series/371980/episodes/" + strings.TrimSuffix(suffix, "_0") + "?page=0"
		}
		e.edge.tvdb[path] = b
	}
	pack := release{name: "Severance.S01.2160p.WEB.H265-GRP", hash: fmt.Sprintf("%040x", 10), season: 1,
		files: []string{"Severance.S01E01E02.2160p.WEB.H265-GRP.mkv", "Severance.S01E03.2160p.WEB.H265-GRP.mkv"}}
	single := release{name: "Severance.S02E01.2160p.WEB.H265-GRP", hash: fmt.Sprintf("%040x", 11), season: 2,
		files: []string{"Severance.S02E01.2160p.WEB.H265-GRP.mkv"}}
	e.edge.set(pack, single)
	e.mountRelease(pack)
	e.mountRelease(single)
	id := e.addSeries(371980, "Severance", "standard", 1, 2)
	e.refreshBeforeSearch(id, 4)
	e.drain()
	e.finish(2)
	files := e.seriesAvailable(id, 4, 3)
	if files["S01E01"] != files["S01E02"] || files["S01E01"] == files["S01E03"] || files["S01E03"] == files["S02E01"] {
		t.Fatalf("episode/file mapping: %v", files)
	}
	if n := e.count(`SELECT count(*) FROM link_episode`); n != 4 {
		t.Fatalf("episode links=%d, want 4", n)
	}
	adds, scans, _ := e.edge.snapshot()
	if len(adds) != 2 || len(scans) != 2 {
		t.Fatalf("series adds=%v scans=%v", adds, scans)
	}
	high := release{name: "Severance.S01E01E02.2160p.UHD.BluRay.REMUX.HEVC-GRP", hash: fmt.Sprintf("%040x", 13), season: 1,
		files: []string{"Severance.S01E01E02.2160p.UHD.BluRay.REMUX.HEVC-GRP.mkv"}}
	e.edge.set(high)
	e.mountRelease(high)
	e.now = e.now.Add(25 * time.Hour)
	e.wanted()
	e.assertUpgradeDecision(id, "tv", high, "WEBDL-2160p", "Bluray-2160p Remux")
	e.finish(1)
	e.seriesAvailable(id, 4, 3)
	if n := e.count(`SELECT count(*) FROM link_trash`); n != 1 {
		t.Fatalf("episode upgrade did not replace the old multi-episode link: %d trash rows", n)
	}
}

func TestSeriesImportUpdatesEpisodeStateAndWantedSet(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, seriesPolicy(false))
	for _, suffix := range []string{"series", "default_0", "absolute_0"} {
		b, err := os.ReadFile("../metadata/tvrefresh/testdata/371980_" + suffix + ".json")
		must(t, err)
		path := "/series/371980/extended"
		if suffix != "series" {
			var doc struct {
				Data struct {
					Episodes []tvdb.Episode `json:"episodes"`
				} `json:"data"`
			}
			must(t, json.Unmarshal(b, &doc))
			var eps []tvdb.Episode
			for _, ep := range doc.Data.Episodes {
				if ep.SeasonNumber == 1 && ep.Number <= 3 || ep.SeasonNumber == 2 && ep.Number == 1 {
					eps = append(eps, ep)
				}
			}
			b = episodeResponse(t, eps)
			path = "/series/371980/episodes/" + strings.TrimSuffix(suffix, "_0") + "?page=0"
		}
		e.edge.tvdb[path] = b
	}
	file := "Severance.S01E01.2160p.UHD.BluRay.REMUX.HEVC-GRP.mkv"
	rel := release{name: strings.TrimSuffix(file, ".mkv"), hash: fmt.Sprintf("%040x", 15), files: []string{file}, season: 1}
	e.edge.set(rel)
	e.mountRelease(rel)
	id := e.addSeries(371980, "Severance", "standard", 1, 2)
	e.refreshBeforeSearch(id, 4)
	e.drain()
	e.finish(1)
	files := e.seriesAvailable(id, 1, 1)
	if files["S01E01"] == 0 {
		t.Fatalf("Sonarr does not report the imported episode as a file: %v", files)
	}
	var episodeID int64
	if err := e.st.Reader().QueryRowContext(e.ctx, `SELECT id FROM episode WHERE title_id=? AND season=1 AND number=1`, id).Scan(&episodeID); err != nil {
		t.Fatal(err)
	}
	if state := e.text(`SELECT state FROM episode_version_state WHERE version_id=(SELECT id FROM version WHERE title_id=? AND policy_key='tv') AND episode_id=?`, id, episodeID); state != "cutoff_met" {
		t.Fatalf("episode state=%s, want cutoff_met", state)
	}
	e.now = e.now.Add(48 * time.Hour)
	sum, err := e.ac.Round(e.ctx, nil)
	must(t, err)
	if sum.Queued == 0 {
		t.Fatalf("wanted loop did not search remaining episodes: %+v", sum)
	}
	var episodesJSON string
	if err := e.st.Reader().QueryRowContext(e.ctx, `SELECT episodes FROM acquire_run
		WHERE source='wanted' AND season=1 AND reason='missing' ORDER BY id DESC LIMIT 1`).Scan(&episodesJSON); err != nil {
		t.Fatal(err)
	}
	var episodes []int64
	must(t, json.Unmarshal([]byte(episodesJSON), &episodes))
	if len(episodes) != 2 || episodes[0] == episodeID || episodes[1] == episodeID {
		t.Fatalf("season missing search still includes imported episode %d: %v", episodeID, episodes)
	}
}

// TestNewSeriesChosenSeasons: a new series added from the add sheet with chosen seasons (season 1 on, season 2 and
// the specials off, no preset), whose seasons the sheet took from the series' own seasons, gets on its first refresh
// (TVDB numbering, seasons 0, 1 and 2) season 1's episodes monitored and season 2's and the specials' not; the held
// search looks for season 1 only.
func TestNewSeriesChosenSeasons(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, seriesPolicy(false))
	severanceFixtures(t, e)
	// The add sheet's options: the series' own seasons, by the TVDB id TMDb names.
	var opts web.AddOptions
	e.request("GET", web.APIBase+"/add/options?kind=series&tmdb_id=95396", nil, &opts)
	var nums []int
	for _, s := range opts.Seasons {
		nums = append(nums, s.Number)
	}
	if opts.EpisodesKnown || !slices.Equal(nums, []int{0, 1, 2}) {
		t.Fatalf("add options: %+v", opts)
	}
	// What the sheet sends with chosen seasons: every shown season's choice and no preset.
	var res web.AddResult
	e.request("POST", web.APIBase+"/titles", map[string]any{"kind": "series", "tmdb_id": 95396, "title": "Severance",
		"year": 2022, "anime": false, "versions": []string{"tv"}, "search": true, "seasons": []map[string]any{
			{"season": 0, "monitored": false}, {"season": 1, "monitored": true}, {"season": 2, "monitored": false}}}, &res)
	id := res.Title.ID
	e.refreshBeforeSearch(id, 5)
	e.drain()
	for season, want := range map[int]int{0: 0, 1: 2, 2: 0} {
		if n := e.count(`SELECT count(*) FROM episode_version_state s JOIN episode ep ON ep.id = s.episode_id
			WHERE ep.title_id = ? AND ep.season = ? AND s.monitored = 1`, id, season); n != want {
			t.Errorf("season %d: %d episodes monitored, want %d", season, n, want)
		}
	}
	if n := e.count(`SELECT count(*) FROM acquire_run WHERE title_id = ?`, id); n == 0 {
		t.Fatalf("the held search did not run\n%s", e.logs.String())
	}
	if n := e.count(`SELECT count(*) FROM acquire_run WHERE title_id = ? AND COALESCE(season, -1) <> 1`, id); n != 0 {
		t.Errorf("%d searches beyond season 1", n)
	}
}

// TestSeasonRequestWithoutSeasonList: a request for season 1 of a new series whose seasons cannot be read at approval
// (TVDB answers an error, or too slowly: "slow" answers after 2.5 s, past the 2 s bound of the season lookup, which
// cancels the request) still gets only season 1 monitored and searched on the first refresh, which brings seasons 0, 1
// and 2: the copy leaves the seasons the request does not name unmonitored.
func TestSeasonRequestWithoutSeasonList(t *testing.T) {
	t.Parallel()
	for _, down := range []string{"error", "slow"} {
		t.Run(down, func(t *testing.T) {
			t.Parallel()
			e := newEnvironment(t, seriesPolicy(false))
			severanceFixtures(t, e)
			e.edge.mu.Lock()
			e.edge.tvdbDown = down
			e.edge.mu.Unlock()
			var cr web.CreateResult
			e.request("POST", web.APIBase+"/requests", map[string]any{"kind": "series", "tmdb_id": 95396, "name": "Severance",
				"seasons": []int{1}}, &cr)
			if !cr.Approved || cr.Request.TitleID == nil {
				t.Fatalf("request: %+v", cr)
			}
			e.edge.mu.Lock()
			e.edge.tvdbDown = ""
			e.edge.mu.Unlock()
			id := *cr.Request.TitleID
			e.refreshBeforeSearch(id, 5)
			e.drain()
			for season, want := range map[int]int{0: 0, 1: 2, 2: 0} {
				if n := e.count(`SELECT count(*) FROM episode_version_state s JOIN episode ep ON ep.id = s.episode_id
					WHERE ep.title_id = ? AND ep.season = ? AND s.monitored = 1`, id, season); n != want {
					t.Errorf("season %d: %d episodes monitored, want %d", season, n, want)
				}
			}
			if n := e.count(`SELECT count(*) FROM acquire_run WHERE title_id = ?`, id); n == 0 {
				t.Fatalf("the held search did not run\n%s", e.logs.String())
			}
			if n := e.count(`SELECT count(*) FROM acquire_run WHERE title_id = ? AND COALESCE(season, -1) <> 1`, id); n != 0 {
				t.Errorf("%d searches beyond season 1", n)
			}
		})
	}
}

// TestSeasonRequestWithoutSeasonListMixed: a request for season 1 in two libraries, one of which the series got
// before the approval (added meanwhile, not refreshed yet): when the seasons cannot be read at approval, the copy the
// approval adds monitors and searches season 1 only after the first refresh, and the copy that was there keeps its own
// monitoring of new seasons.
func TestSeasonRequestWithoutSeasonListMixed(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, func(p *policy.Policy) {
		seriesPolicy(false)(p)
		v := *p.Versions["tv"]
		lv := *p.Library.Versions["tv"]
		v.Library = filepath.Join(filepath.Dir(v.Library), "tv2")
		lv.Roots = []string{v.Library}
		p.Versions["tv2"], p.Library.Versions["tv2"] = &v, &lv
	})
	severanceFixtures(t, e)
	now := e.now.UnixMilli()
	res, err := e.st.Writer().ExecContext(e.ctx, `INSERT INTO request (user_id, kind, tmdb_id, tvdb_id, name, year, anime,
		poster_path, versions, seasons, note, status, created_at, updated_at) VALUES (1, 'series', 95396, 371980,
		'Severance', 2022, 0, '', '["tv","tv2"]', '[1]', '', 'pending', ?, ?)`, now, now)
	must(t, err)
	rid, _ := res.LastInsertId()
	var added web.AddResult
	e.request("POST", web.APIBase+"/titles", map[string]any{"kind": "series", "tmdb_id": 95396, "title": "Severance",
		"year": 2022, "anime": false, "versions": []string{"tv"}}, &added)
	id := added.Title.ID
	e.edge.mu.Lock()
	e.edge.tvdbDown = "error"
	e.edge.mu.Unlock()
	e.request("POST", fmt.Sprintf("%s/requests/%d/approve", web.APIBase, rid), map[string]any{}, nil)
	e.edge.mu.Lock()
	e.edge.tvdbDown = ""
	e.edge.mu.Unlock()
	newItems := func(key string) string {
		return e.text(`SELECT m.monitor_new FROM series_monitor m JOIN version v ON v.id = m.version_id
			WHERE v.title_id = ? AND v.policy_key = ?`, id, key)
	}
	if newItems("tv") != "all" || newItems("tv2") != "none" {
		t.Fatalf("monitor new items: tv %q, tv2 %q", newItems("tv"), newItems("tv2"))
	}
	e.refreshBeforeSearch(id, 5)
	e.drain()
	monitored := func(key string, season int) int {
		return e.count(`SELECT count(*) FROM episode_version_state s JOIN episode ep ON ep.id = s.episode_id
			JOIN version v ON v.id = s.version_id WHERE ep.title_id = ? AND v.policy_key = ? AND ep.season = ?
			AND s.monitored = 1`, id, key, season)
	}
	if monitored("tv2", 1) != 2 || monitored("tv2", 2) != 0 || monitored("tv2", 0) != 0 {
		t.Errorf("new copy: season 1 %d, season 2 %d, specials %d monitored", monitored("tv2", 1), monitored("tv2", 2),
			monitored("tv2", 0))
	}
	if monitored("tv", 1) != 2 || monitored("tv", 2) != 2 {
		t.Errorf("the copy that was there: season 1 %d, season 2 %d monitored", monitored("tv", 1), monitored("tv", 2))
	}
	if n := e.count(`SELECT count(*) FROM acquire_run r JOIN version v ON v.id = r.version_id WHERE r.title_id = ?
		AND v.policy_key = 'tv2'`, id); n == 0 {
		t.Fatalf("the new copy was not searched\n%s", e.logs.String())
	}
	if n := e.count(`SELECT count(*) FROM acquire_run r JOIN version v ON v.id = r.version_id WHERE r.title_id = ?
		AND v.policy_key = 'tv2' AND COALESCE(r.season, -1) <> 1`, id); n != 0 {
		t.Errorf("%d searches of the new copy beyond season 1", n)
	}
}

// severanceFixtures serves TVDB 371980 trimmed to S00E11, S01E01-02 and S02E01-02.
func severanceFixtures(t *testing.T, e *environment) {
	t.Helper()
	for _, suffix := range []string{"series", "default_0", "absolute_0"} {
		b, err := os.ReadFile("../metadata/tvrefresh/testdata/371980_" + suffix + ".json")
		must(t, err)
		path := "/series/371980/extended"
		if suffix != "series" {
			var doc struct {
				Data struct {
					Episodes []tvdb.Episode `json:"episodes"`
				} `json:"data"`
			}
			must(t, json.Unmarshal(b, &doc))
			var eps []tvdb.Episode
			for _, ep := range doc.Data.Episodes {
				if ep.SeasonNumber == 0 && ep.Number == 11 || ep.SeasonNumber == 1 && ep.Number <= 2 ||
					ep.SeasonNumber == 2 && ep.Number <= 2 {
					eps = append(eps, ep)
				}
			}
			b = episodeResponse(t, eps)
			path = "/series/371980/episodes/" + strings.TrimSuffix(suffix, "_0") + "?page=0"
		}
		e.edge.tvdb[path] = b
	}
}

func TestSeriesSeasonPackFanout(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, func(p *policy.Policy) {
		seriesPolicy(false)(p)
		v := *p.Versions["tv"]
		v.Library = filepath.Join(filepath.Dir(v.Library), "tv-web")
		v.Profile = "tv-web"
		p.Versions["tv-web"] = &v
		profile := *p.Profiles["movie"]
		p.Profiles["tv-web"] = &profile
		p.Library.Versions["tv-web"] = &policy.LibraryVersion{Roots: []string{v.Library}, PlexSection: 18}
	})
	for _, suffix := range []string{"series", "default_0", "absolute_0"} {
		b, err := os.ReadFile("../metadata/tvrefresh/testdata/371980_" + suffix + ".json")
		must(t, err)
		path := "/series/371980/extended"
		if suffix != "series" {
			var doc struct {
				Data struct {
					Episodes []tvdb.Episode `json:"episodes"`
				} `json:"data"`
			}
			must(t, json.Unmarshal(b, &doc))
			var eps []tvdb.Episode
			for _, ep := range doc.Data.Episodes {
				if ep.SeasonNumber == 1 && ep.Number <= 2 {
					eps = append(eps, ep)
				}
			}
			b = episodeResponse(t, eps)
			path = "/series/371980/episodes/" + strings.TrimSuffix(suffix, "_0") + "?page=0"
		}
		e.edge.tvdb[path] = b
	}
	pack := release{name: "Severance.S01.2160p.WEB.H265-GRP", hash: fmt.Sprintf("%040x", 20), season: 1,
		files: []string{"Severance.S01E01.2160p.WEB.H265-GRP.mkv", "Severance.S01E02.2160p.WEB.H265-GRP.mkv"}}
	e.edge.set(pack)
	e.mountRelease(pack)
	id := e.addSeries(371980, "Severance", "standard", 1)
	if _, err := e.st.Writer().ExecContext(e.ctx, `INSERT INTO version (title_id, policy_key, monitored, state,
		created_at, updated_at) SELECT title_id, 'tv-web', monitored, state, created_at, updated_at FROM version
		WHERE title_id = ? AND policy_key = 'tv'`, id); err != nil {
		t.Fatal(err)
	}
	e.refreshBeforeSearch(id, 2)
	e.drain()
	e.finish(2)
	e.seriesAvailable(id, 2, 2)
	if n := e.count(`SELECT count(*) FROM link_episode le JOIN link_version lv ON lv.link_id=le.link_id
		JOIN version v ON v.id=lv.version_id WHERE v.policy_key='tv-web'`); n != 2 {
		t.Fatalf("tv-web episode links=%d, want 2", n)
	}
	adds, _, _ := e.edge.snapshot()
	if len(adds) != 1 || e.count(`SELECT count(*) FROM download WHERE reused=1`) != 1 {
		t.Fatalf("season pack was not reused: adds=%v", adds)
	}
	if searches := e.edge.searchCount(); searches != 1 {
		t.Fatalf("series title searches=%d, want 1", searches)
	}
}

func TestAnime(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, seriesPolicy(true))
	// Reuse anime's recorded SkyHook fixture as the TVDB edge, and the absolute-release syntax from
	// decide.TestAnimeDecisions. Keep S01 and S02E01 so absolute 26 must cross a season boundary.
	b, err := os.ReadFile("../metadata/anime/testdata/offline/cache/skyhook/17c6f705719c320941ab.json")
	must(t, err)
	var recorded struct {
		Response anime.Show `json:"response"`
	}
	must(t, json.Unmarshal(b, &recorded))
	show := recorded.Response
	if show.Title == nil || show.TVDBID != 305089 {
		t.Fatal("unexpected anime fixture")
	}
	var eps []tvdb.Episode
	abs := 0
	for i, ep := range show.Episodes {
		if ep.SeasonNumber != 1 && !(ep.SeasonNumber == 2 && ep.EpisodeNumber == 1) {
			continue
		}
		eps = append(eps, tvdb.Episode{ID: 900000 + i, SeriesID: show.TVDBID, Name: ep.Title, Aired: ep.AirDate,
			Runtime: ep.Runtime, SeasonNumber: ep.SeasonNumber, Number: ep.EpisodeNumber, AbsoluteNumber: ep.AbsoluteEpisodeNumber})
		if ep.SeasonNumber == 2 && ep.AbsoluteEpisodeNumber != nil {
			abs = *ep.AbsoluteEpisodeNumber
		}
	}
	if abs != 26 {
		t.Fatalf("fixture's first S02 absolute=%d, want 26", abs)
	}
	e.edge.tvdb["/series/305089/extended"] = encode(t, map[string]any{"status": "success", "data": tvdb.Series{ID: show.TVDBID, Name: *show.Title, Year: "2016", OriginalLanguage: "eng", Status: tvdb.Status{Name: "Continuing"}}})
	e.edge.tvdb["/series/305089/episodes/default?page=0"] = episodeResponse(t, eps)
	name := fmt.Sprintf("[SubsPlease] %s - %02d (1080p) [ABCD1234]", *show.Title, abs)
	rel := release{name: name, hash: fmt.Sprintf("%040x", 12), files: []string{name + ".mkv"}, season: 2, size: 1 << 30}
	e.edge.set(rel)
	e.mountRelease(rel)
	id := e.addSeries(show.TVDBID, *show.Title, "anime", 2)
	e.refreshBeforeSearch(id, len(eps))
	e.drain()
	e.finish(1)
	files := e.seriesAvailable(id, 1, 1)
	if files["S02E01"] == 0 {
		t.Fatalf("absolute release mapped to wrong episode: %v", files)
	}
	if n := e.count(`SELECT count(*) FROM link_episode le JOIN episode e ON e.id=le.episode_id
		JOIN episode_number n ON n.episode_id=e.id WHERE e.season=2 AND e.number=1 AND n.scheme='absolute_tvdb' AND n.number=26`); n != 1 {
		t.Fatalf("absolute mapping count=%d", n)
	}
}

func encode(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	must(t, err)
	return b
}

func episodeResponse(t *testing.T, eps []tvdb.Episode) []byte {
	t.Helper()
	return encode(t, map[string]any{"status": "success", "data": map[string]any{"episodes": eps}, "links": map[string]any{"next": nil}})
}

func (e *environment) addSeries(tvdbID int, title, typ string, seasons ...int) int64 {
	e.t.Helper()
	var ss []map[string]any
	for _, s := range seasons {
		ss = append(ss, map[string]any{"seasonNumber": s, "monitored": true})
	}
	var result struct{ ID int64 }
	e.request("POST", "/v/tv/sonarr/api/v3/series", map[string]any{"tvdbId": tvdbID, "title": title, "monitored": true,
		"seasons": ss, "monitorNewItems": "none", "seriesType": typ, "seasonFolder": true,
		"addOptions": map[string]any{"searchForMissingEpisodes": true}}, &result)
	return result.ID - arrview.IDBase
}

func (e *environment) refreshBeforeSearch(id int64, episodes int) {
	e.t.Helper()
	if e.count(`SELECT count(*) FROM episode`) != 0 || e.count(`SELECT count(*) FROM acquire_run`) != 0 || e.count(`SELECT count(*) FROM series_search_wait WHERE title_id=?`, id) != 1 {
		e.t.Fatal("series search was not held for first refresh")
	}
	e.edge.mu.Lock()
	requests := append([]string(nil), e.edge.requests...)
	e.edge.mu.Unlock()
	for _, request := range requests {
		if strings.Contains(request, "/indexer") {
			e.t.Fatalf("indexer used before refresh: %v", requests)
		}
	}
	ran, err := e.worker.RunOne(e.ctx)
	must(e.t, err)
	if !ran || e.count(`SELECT count(*) FROM episode`) != episodes || e.count(`SELECT count(*) FROM series_search_wait`) != 0 {
		e.t.Fatalf("metadata did not release held search\n%s", e.logs.String())
	}
}

func (e *environment) seriesAvailable(id int64, wantEpisodes, wantFiles int) map[string]int64 {
	e.t.Helper()
	var eps []struct {
		SeasonNumber, EpisodeNumber int
		EpisodeFileID               int64 `json:"episodeFileId"`
		HasFile                     bool
	}
	e.request("GET", fmt.Sprintf("/v/tv/sonarr/api/v3/episode?seriesId=%d", id+arrview.IDBase), nil, &eps)
	files := map[string]int64{}
	for _, ep := range eps {
		if ep.HasFile {
			if ep.EpisodeFileID == 0 {
				e.t.Fatal("hasFile without episodeFileId")
			}
			files[fmt.Sprintf("S%02dE%02d", ep.SeasonNumber, ep.EpisodeNumber)] = ep.EpisodeFileID
		}
	}
	if len(files) != wantEpisodes {
		e.t.Fatalf("Sonarr episodes with files=%v, want %d", files, wantEpisodes)
	}
	var ff []struct {
		ID   int64
		Path string
	}
	e.request("GET", fmt.Sprintf("/v/tv/sonarr/api/v3/episodefile?seriesId=%d", id+arrview.IDBase), nil, &ff)
	if len(ff) != wantFiles {
		e.t.Fatalf("Sonarr episodefiles=%+v, want %d", ff, wantFiles)
	}
	for _, file := range ff {
		if file.ID == 0 || file.Path == "" {
			e.t.Fatalf("incomplete episodefile: %+v", file)
		}
		_, err := os.Stat(file.Path)
		must(e.t, err)
	}
	return files
}

// A multi-season pack found by a season-2 search is grabbed for the seasons searched: seasons 2 and 3 are wanted
// whole and searched together (one merged search), season 1 misses one episode and is searched by that episode
// alone, so the pack is for seasons 2 and 3 only (phase AB: a pack never brings a season nobody searched). The
// other searched season's episodes are in flight meanwhile, the import files each season in its own folder, and
// season 1's files, one of them better, are left alone.
func TestSeriesMultiSeasonPack(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, seriesPolicy(false))
	var eps []tvdb.Episode
	for s := 1; s <= 3; s++ {
		for n := 1; n <= 2; n++ {
			name, aired, runtime := fmt.Sprintf("Episode %d", n), fmt.Sprintf("%d-03-%02d", 2018+s, n), 50
			eps = append(eps, tvdb.Episode{ID: 950000 + 10*s + n, SeriesID: 424242, Name: &name, Aired: &aired,
				Runtime: &runtime, SeasonNumber: s, Number: n})
		}
	}
	e.edge.tvdb["/series/424242/extended"] = encode(t, map[string]any{"status": "success", "data": tvdb.Series{ID: 424242,
		Name: "Ended Show", Year: "2019", OriginalLanguage: "eng", Status: tvdb.Status{Name: "Ended"}}})
	e.edge.tvdb["/series/424242/episodes/default?page=0"] = episodeResponse(t, eps)
	e.edge.tvdb["/series/424242/episodes/absolute?page=0"] = episodeResponse(t, eps)

	remux := release{name: "Ended.Show.S01E01.2160p.UHD.BluRay.REMUX.HEVC-GRP", hash: fmt.Sprintf("%040x", 30), season: 1,
		files: []string{"Ended.Show.S01E01.2160p.UHD.BluRay.REMUX.HEVC-GRP.mkv"}}
	e.edge.set(remux)
	e.mountRelease(remux)
	id := e.addSeries(424242, "Ended Show", "standard", 1, 2, 3)
	e.refreshBeforeSearch(id, 6)
	e.drain()
	e.finish(1)
	before := e.seriesAvailable(id, 1, 1)["S01E01"]

	pack := release{name: "Ended.Show.S01-S03.2160p.WEB.H265-GRP", hash: fmt.Sprintf("%040x", 31), season: 2}
	for _, ep := range eps {
		pack.files = append(pack.files, fmt.Sprintf("Ended.Show.S%02dE%02d.2160p.WEB.H265-GRP.mkv", ep.SeasonNumber, ep.Number))
	}
	e.edge.set(pack) // answered to a season-2 query only
	e.mountRelease(pack)
	e.now = e.now.Add(48 * time.Hour)
	e.wanted()
	var episodesJSON string
	if err := e.st.Reader().QueryRowContext(e.ctx, `SELECT episodes FROM download WHERE release_title=?`, pack.name).Scan(&episodesJSON); err != nil {
		t.Fatalf("pack not grabbed: %v\n%s", err, e.logs.String())
	}
	var ids []int64
	must(t, json.Unmarshal([]byte(episodesJSON), &ids))
	if len(ids) != 4 { // seasons 2 and 3: season 1 was not searched by season
		t.Fatalf("pack download episodes=%v, want the four of seasons 2 and 3", ids)
	}
	var versionID, s3 int64
	must(t, e.st.Reader().QueryRowContext(e.ctx, `SELECT id FROM version WHERE title_id=? AND policy_key='tv'`, id).Scan(&versionID))
	must(t, e.st.Reader().QueryRowContext(e.ctx, `SELECT id FROM episode WHERE title_id=? AND season=3 AND number=2`, id).Scan(&s3))
	if in, err := router.InFlight(e.ctx, e.st.Reader(), id, versionID, []int64{s3}); err != nil || !in {
		t.Fatalf("season 3 not in flight with the pack: %v %v", in, err)
	}
	if _, err := e.ac.Round(e.ctx, nil); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if n := e.count(`SELECT count(*) FROM download`); n != 2 {
		t.Fatalf("downloads=%d, want the remux and the pack only", n)
	}
	_, err := e.rt.Poll(e.ctx)
	must(t, err)
	var downloadID int64
	must(t, e.st.Reader().QueryRowContext(e.ctx, `SELECT id FROM download WHERE release_title=? AND state='ready'`, pack.name).Scan(&downloadID))
	plan, err := e.lib.Import(e.ctx, downloadID, false)
	must(t, err)
	if len(plan.Unmatched) != 2 || plan.Unmatched[0] != pack.files[0] || plan.Unmatched[1] != pack.files[1] {
		// season 1's files are not the download's: left alone
		t.Fatalf("pack import unmatched=%v", plan.Unmatched)
	}
	folders := map[string]int{}
	for _, l := range plan.Links {
		if !l.Done {
			t.Fatalf("link not imported: %+v", l)
		}
		folders[filepath.Base(filepath.Dir(l.Path))]++
	}
	e.now = e.now.Add(2 * time.Second)
	e.drain()
	if folders["Season 01"] != 0 || folders["Season 02"] != 2 || folders["Season 03"] != 2 {
		t.Fatalf("pack links by folder=%v", folders)
	}
	files := e.seriesAvailable(id, 5, 5)
	if files["S01E01"] != before {
		t.Fatalf("S01E01's better file was replaced: %d -> %d", before, files["S01E01"])
	}
}
