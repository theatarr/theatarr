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
	"github.com/theatarr/theatarr/internal/indexer"
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

// mergedShow serves a three-season ended series of two aired episodes a season (TVDB 434343) and adds it: its title
// search runs once the first refresh is done.
func (e *environment) mergedShow(t *testing.T) (int64, []tvdb.Episode) {
	var eps []tvdb.Episode
	for s := 1; s <= 3; s++ {
		for n := 1; n <= 2; n++ {
			name, aired, runtime := fmt.Sprintf("Episode %d", n), fmt.Sprintf("%d-03-%02d", 2018+s, n), 50
			eps = append(eps, tvdb.Episode{ID: 960000 + 10*s + n, SeriesID: 434343, Name: &name, Aired: &aired,
				Runtime: &runtime, SeasonNumber: s, Number: n})
		}
	}
	e.edge.tvdb["/series/434343/extended"] = encode(t, map[string]any{"status": "success", "data": tvdb.Series{ID: 434343,
		Name: "Merged Show", Year: "2019", OriginalLanguage: "eng", Status: tvdb.Status{Name: "Ended"}}})
	e.edge.tvdb["/series/434343/episodes/default?page=0"] = episodeResponse(t, eps)
	e.edge.tvdb["/series/434343/episodes/absolute?page=0"] = episodeResponse(t, eps)
	return e.addSeries(434343, "Merged Show", "standard", 1, 2, 3), eps
}

func seasonFiles(name string, eps []tvdb.Episode, seasons ...int) []string {
	var out []string
	for _, ep := range eps {
		if len(seasons) == 0 || slices.Contains(seasons, ep.SeasonNumber) {
			out = append(out, fmt.Sprintf("%s.S%02dE%02d.%s.mkv", "Merged.Show", ep.SeasonNumber, ep.Number, name))
		}
	}
	return out
}

// A title search of a three-season series is one merged search: its seasons and the query without a season are
// asked together, and the pack of all three seasons is grabbed and imported; no season pack is downloaded besides.
func TestSeriesMergedSearchTakesTheSpan(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, seriesPolicy(false))
	id, eps := e.mergedShow(t)
	span := release{name: "Merged.Show.S01-S03.2160p.WEB.H265-GRP", hash: fmt.Sprintf("%040x", 41), season: 2,
		files: seasonFiles("2160p.WEB.H265-GRP", eps)}
	one := release{name: "Merged.Show.S01.2160p.WEB.H265-GRP", hash: fmt.Sprintf("%040x", 42), season: 1,
		files: seasonFiles("2160p.WEB.H265-GRP", eps, 1)}
	three := release{name: "Merged.Show.S03.2160p.WEB.H265-GRP", hash: fmt.Sprintf("%040x", 43), season: 3,
		files: seasonFiles("2160p.WEB.H265-GRP", eps, 3)}
	e.edge.set(span, one, three)
	for _, r := range []release{span, one, three} {
		e.mountRelease(r)
	}
	e.refreshBeforeSearch(id, 6)
	e.drain()
	if n := e.count(`SELECT count(*) FROM acquire_run WHERE title_id=? AND search_run IS NULL`, id); n != 1 ||
		e.count(`SELECT count(*) FROM acquire_run WHERE title_id=?`, id) != 3 {
		t.Fatalf("searches: %d roots\n%s", n, e.logs.String())
	}
	if n := e.count(`SELECT count(*) FROM download`); n != 1 || e.count(`SELECT count(*) FROM download WHERE release_title=?`, span.name) != 1 {
		var got string
		_ = e.st.Reader().QueryRow(`SELECT group_concat(release_title || ' ' || episodes, '; ') FROM download`).Scan(&got)
		t.Fatalf("downloads=%d (%s), want the span only\n%s", n, got, e.logs.String())
	}
	e.finish(1)
	e.seriesAvailable(id, 6, 6)
}

// The mixed case: a better pack for season 2 comes first, so the pack of all three seasons is opened for seasons 1
// and 3 only; both are imported, the span's season-2 files left alone.
func TestSeriesMergedSearchMixed(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, seriesPolicy(false))
	id, eps := e.mergedShow(t)
	span := release{name: "Merged.Show.S01-S03.2160p.WEB.H265-GRP", hash: fmt.Sprintf("%040x", 51), season: 2,
		files: seasonFiles("2160p.WEB.H265-GRP", eps)}
	two := release{name: "Merged.Show.S02.2160p.UHD.BluRay.REMUX.HEVC-GRP", hash: fmt.Sprintf("%040x", 52), season: 2,
		files: seasonFiles("2160p.UHD.BluRay.REMUX.HEVC-GRP", eps, 2)}
	e.edge.set(span, two)
	for _, r := range []release{span, two} {
		e.mountRelease(r)
	}
	e.refreshBeforeSearch(id, 6)
	e.drain()
	if n := e.count(`SELECT count(*) FROM download`); n != 2 {
		t.Fatalf("downloads=%d, want the season-2 pack and the span\n%s", n, e.logs.String())
	}
	var episodesJSON string
	must(t, e.st.Reader().QueryRowContext(e.ctx, `SELECT episodes FROM download WHERE release_title=?`, span.name).Scan(&episodesJSON))
	var ids []int64
	must(t, json.Unmarshal([]byte(episodesJSON), &ids))
	if len(ids) != 4 {
		t.Fatalf("span download episodes=%v, want seasons 1 and 3", ids)
	}
	_, err := e.rt.Poll(e.ctx)
	must(t, err)
	folders := map[string]int{}
	for _, r := range []release{two, span} {
		var downloadID int64
		must(t, e.st.Reader().QueryRowContext(e.ctx, `SELECT id FROM download WHERE release_title=? AND state='ready'`, r.name).Scan(&downloadID))
		plan, err := e.lib.Import(e.ctx, downloadID, false)
		must(t, err)
		if r.name == span.name && (len(plan.Unmatched) != 2 || !strings.Contains(plan.Unmatched[0], "S02E01")) {
			t.Fatalf("span import unmatched=%v", plan.Unmatched)
		}
		for _, l := range plan.Links {
			if !l.Done {
				t.Fatalf("link not imported: %+v", l)
			}
			folders[filepath.Base(filepath.Dir(l.Path))]++
		}
	}
	e.now = e.now.Add(2 * time.Second)
	e.drain()
	if folders["Season 01"] != 2 || folders["Season 02"] != 2 || folders["Season 03"] != 2 {
		t.Fatalf("links by folder=%v", folders)
	}
	e.seriesAvailable(id, 6, 6)
}

// animeShow is an ended anime series of three seasons of two episodes, absolute 1 to 6, added with every season.
func (e *environment) animeShow(t *testing.T) (int64, []tvdb.Episode) {
	var eps []tvdb.Episode
	for s := 1; s <= 3; s++ {
		for n := 1; n <= 2; n++ {
			name, aired, runtime, abs := fmt.Sprintf("Episode %d", n), fmt.Sprintf("%d-03-%02d", 2018+s, n), 24, 2*(s-1)+n
			eps = append(eps, tvdb.Episode{ID: 970000 + 10*s + n, SeriesID: 454545, Name: &name, Aired: &aired,
				Runtime: &runtime, SeasonNumber: s, Number: n, AbsoluteNumber: &abs})
		}
	}
	e.edge.tvdb["/series/454545/extended"] = encode(t, map[string]any{"status": "success", "data": tvdb.Series{ID: 454545,
		Name: "Kaiju Tale", Year: "2019", OriginalLanguage: "jpn", Status: tvdb.Status{Name: "Ended"}}})
	e.edge.tvdb["/series/454545/episodes/default?page=0"] = episodeResponse(t, eps)
	e.edge.tvdb["/series/454545/episodes/absolute?page=0"] = episodeResponse(t, eps)
	return e.addSeries(454545, "Kaiju Tale", "anime", 1, 2, 3), eps
}

// A newly added anime series is one merged search; a pack named as seasons 1 to 3 is taken for all three, proved
// at import and every file linked to its own episode; nothing else is downloaded.
func TestAnimeMergedSearchTakesTheSpan(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, seriesPolicy(true))
	id, eps := e.animeShow(t)
	span := release{name: "[GRP] Kaiju Tale S01-S03 [WEB 2160p]", hash: fmt.Sprintf("%040x", 61), season: 2, size: 8 << 30}
	for _, ep := range eps {
		span.files = append(span.files, fmt.Sprintf("Season %d/[GRP] Kaiju Tale - %02d [WEB 2160p].mkv", ep.SeasonNumber, ep.Number))
	}
	span.files = append(span.files, "[GRP] Kaiju Tale - NCOP1 [WEB 2160p].mkv")
	e.edge.set(span)
	e.mountRelease(span)
	e.refreshBeforeSearch(id, 6)
	e.drain()
	if n := e.count(`SELECT count(*) FROM acquire_run WHERE title_id=? AND search_run IS NULL`, id); n != 1 {
		t.Fatalf("searches: %d roots\n%s", n, e.logs.String())
	}
	if n := e.count(`SELECT count(*) FROM download`); n != 1 || !strings.Contains(e.text(`SELECT COALESCE(episode_map,'') FROM download`), `"mode":"seasons"`) {
		t.Fatalf("downloads=%d, want the span with its claim\n%s", n, e.logs.String())
	}
	_, err := e.rt.Poll(e.ctx)
	must(t, err)
	plan, err := e.lib.Import(e.ctx, int64(e.count(`SELECT id FROM download`)), false)
	must(t, err)
	if len(plan.Links) != 6 {
		t.Fatalf("links %d, why %q, unresolved %v", len(plan.Links), plan.Why, plan.Unresolved)
	}
	for _, l := range plan.Links {
		var season, number int
		if _, err := fmt.Sscanf(l.File, "Season %d/[GRP] Kaiju Tale - %d", &season, &number); err != nil ||
			!strings.Contains(filepath.Base(l.Path), fmt.Sprintf("S%02dE%02d", season, number)) {
			t.Errorf("link %s <- %s", l.Path, l.File)
		}
	}
	e.now = e.now.Add(2 * time.Second)
	e.drain()
	e.seriesAvailable(id, 6, 6)
}

// A pack whose numbering cannot be trusted (two files on one episode) is rejected at import: nothing linked, the
// download failed and blocklisted; each season is then searched by itself, takes its own season pack, and the bad
// pack is not taken again.
func TestAnimeMergedPackRejectedThenSeasonsAlone(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, seriesPolicy(true))
	id, eps := e.animeShow(t)
	bad := release{name: "[GRP] Kaiju Tale (01-06) [WEB 2160p]", hash: fmt.Sprintf("%040x", 71), season: 2, size: 8 << 30}
	for i := range eps {
		bad.files = append(bad.files, fmt.Sprintf("[GRP] Kaiju Tale - %02d [WEB 2160p].mkv", i+1))
	}
	bad.files = append(bad.files, "[GRP] Kaiju Tale - 04 [WEB 2160p] (v2).mkv")
	e.edge.set(bad)
	e.mountRelease(bad)
	e.refreshBeforeSearch(id, 6)
	e.drain()
	if n := e.count(`SELECT count(*) FROM download`); n != 1 {
		t.Fatalf("downloads=%d\n%s", n, e.logs.String())
	}
	_, err := e.rt.Poll(e.ctx)
	must(t, err)
	plan, err := e.lib.Import(e.ctx, int64(e.count(`SELECT id FROM download`)), false)
	must(t, err)
	if len(plan.Links) != 0 || !strings.HasPrefix(plan.Why, "pack numbering: two files on S02E02") {
		t.Fatalf("links %d, why %q", len(plan.Links), plan.Why)
	}
	if got := e.text(`SELECT state FROM download`); got != "failed" || e.count(`SELECT count(*) FROM router_blocklist WHERE source='library'`) != 1 {
		t.Fatalf("download %s, blocklist %d", got, e.count(`SELECT count(*) FROM router_blocklist`))
	}
	var packs []release
	for s := 1; s <= 3; s++ {
		p := release{name: fmt.Sprintf("[GRP] Kaiju Tale S%d [WEB 2160p]", s), hash: fmt.Sprintf("%040x", 71+s), season: s, size: 3 << 30}
		for n := 1; n <= 2; n++ {
			p.files = append(p.files, fmt.Sprintf("[GRP] Kaiju Tale S%d - %02d [WEB 2160p].mkv", s, n))
		}
		packs = append(packs, p)
		e.mountRelease(p)
	}
	e.edge.set(append([]release{bad}, packs...)...)
	roots := e.count(`SELECT count(*) FROM acquire_run WHERE title_id=? AND search_run IS NULL`, id)
	_, err = e.rt.Poll(e.ctx) // the blocklist's re-search
	must(t, err)
	e.drain()
	if n := e.count(`SELECT count(*) FROM acquire_run WHERE title_id=? AND search_run IS NULL`, id) - roots; n != 3 {
		t.Fatalf("re-search: %d roots, want three seasons one by one\n%s", n, e.logs.String())
	}
	if n := e.count(`SELECT count(*) FROM download WHERE info_hash=?`, bad.hash); n != 1 {
		t.Fatalf("the rejected pack was taken again: %d downloads", n)
	}
	if n := e.count(`SELECT count(*) FROM download WHERE state<>'failed'`); n != 3 {
		var got string
		_ = e.st.Reader().QueryRow(`SELECT group_concat(release_title || ' ' || state, '; ') FROM download`).Scan(&got)
		t.Fatalf("season packs: %s", got)
	}
}

// A grab by hand from the whole-series window, with override, is decided with that window's criterion (the pack is
// read as seasons 1 to 3 and claims them) and its import is proved like any other: a duplicate number rejects it.
func TestAnimeWholeSeriesGrabByHandIsProved(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, seriesPolicy(true))
	id, _ := e.animeShow(t)
	e.refreshBeforeSearch(id, 6)
	e.drain()
	pack := release{name: "[GRP] Kaiju Tale Seasons 1-3 [WEB 2160p]", hash: fmt.Sprintf("%040x", 81), size: 1 << 30}
	for s := 1; s <= 3; s++ {
		for n := 1; n <= 2; n++ {
			pack.files = append(pack.files, fmt.Sprintf("Season %d/[GRP] Kaiju Tale - %02d [WEB 2160p].mkv", s, n))
		}
	}
	pack.files = append(pack.files, "Season 3/[GRP] Kaiju Tale - 02 [WEB 2160p] (v2).mkv")
	e.edge.set(pack)
	e.mountRelease(pack)
	rel := indexer.Release{Title: pack.name, InfoHash: pack.hash, Indexer: "public", Kind: "torznab", Privacy: "public",
		Size: pack.size, Seeders: 20, Peers: 20}
	g, err := e.av.GrabWholeRelease(e.ctx, id, "tv", rel, true, "", "ui:grab:key:e2e")
	must(t, err)
	if !g.Overridden || len(g.Decision.EpisodeIDs) != 6 || g.Decision.Pack == nil || !g.Decision.Pack.Span() {
		t.Fatalf("grab: overridden %v, episodes %v, claim %+v (%v)", g.Overridden, g.Decision.Episodes, g.Decision.Pack,
			g.Decision.Rejections)
	}
	e.drain()
	_, err = e.rt.Poll(e.ctx)
	must(t, err)
	if c := e.text(`SELECT COALESCE(release_data, '') FROM download WHERE info_hash = ?`, pack.hash); !strings.Contains(c, `"Kind":"series"`) {
		t.Errorf("the candidate lost the whole-series criterion: %s", c)
	}
	plan, err := e.lib.Import(e.ctx, int64(e.count(`SELECT id FROM download WHERE info_hash = ?`, pack.hash)), false)
	must(t, err)
	if len(plan.Links) != 0 || !strings.HasPrefix(plan.Why, "pack numbering: two files on S03E02") {
		t.Fatalf("links %d, why %q", len(plan.Links), plan.Why)
	}
	if got := e.text(`SELECT state FROM download WHERE info_hash = ?`, pack.hash); got != "failed" {
		t.Errorf("download %s", got)
	}
}
