package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/arrview"
	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/tvdb"
	"github.com/theatarr/theatarr/internal/policy"
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
	sum, err := e.ac.Wanted(e.ctx)
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
