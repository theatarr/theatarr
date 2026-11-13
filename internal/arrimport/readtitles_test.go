package arrimport

import (
	"context"
	"strings"
	"testing"
)

func readTitles(t *testing.T, g *fakeArr, flavour string, progress func(done, total int)) *Snapshot {
	t.Helper()
	s, err := ReadTitles(context.Background(), g, flavour, progress)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestReadTitlesRadarr(t *testing.T) {
	t.Parallel()
	g := &fakeArr{answers: map[string]any{
		"system/status": map[string]any{"appName": "Radarr", "version": "6.4.4.10685"},
		"rootfolder":    []map[string]any{{"id": 1, "path": "/mnt/movies-a"}},
		"qualityprofile": []map[string]any{{"id": 7, "name": "Remux + WEB 2160p", "upgradeAllowed": true, "cutoff": 31,
			"items": []map[string]any{{"quality": map[string]any{"id": 31, "name": "Remux-2160p"}}}}},
		"movie": []map[string]any{
			{"id": 1, "title": "Alpha", "tmdbId": 100, "monitored": true, "hasFile": true, "qualityProfileId": 7,
				"minimumAvailability": "released", "path": "/mnt/movies-a/Alpha"},
			{"id": 2, "title": "Beta", "tmdbId": 200, "qualityProfileId": 7, "path": "/mnt/movies-a/Beta"}},
		"moviefile": []map[string]any{{"id": 11, "movieId": 1, "relativePath": "Alpha (2001) [Remux-2160p]-GRP.mkv",
			"quality": quality("Remux-2160p"), "mediaInfo": map[string]any{"videoCodec": "HEVC"}}},
	}}
	calls := 0
	s := readTitles(t, g, "radarr", func(done, total int) {
		calls++
		if done != 2 || total != 2 {
			t.Errorf("progress %d/%d", done, total)
		}
	})
	if calls != 1 {
		t.Errorf("progress called %d times", calls)
	}
	if s.AppVersion != "6.4.4.10685" || len(s.RootFolders) != 1 || s.RootFolders[0] != "/mnt/movies-a" {
		t.Errorf("version %q roots %v", s.AppVersion, s.RootFolders)
	}
	if len(s.Profiles) != 1 || s.Profiles[0].ArrID != 7 || s.Profiles[0].Cutoff != "Remux-2160p" {
		t.Errorf("profiles %+v", s.Profiles)
	}
	if len(s.Titles) != 2 {
		t.Fatalf("titles %d", len(s.Titles))
	}
	a := s.Titles[0]
	if a.Name != "Alpha" || a.TmdbID != 100 || !a.Monitored || a.MinimumAvailability != "released" || a.QualityProfileID != 7 {
		t.Errorf("alpha %+v", a)
	}
	if a.File == nil || a.File.Path != "/mnt/movies-a/Alpha/Alpha (2001) [Remux-2160p]-GRP.mkv" ||
		a.File.Quality != "Remux-2160p" || !strings.Contains(string(a.File.MediaInfo), "HEVC") {
		t.Errorf("alpha file %+v", a.File)
	}
	if s.Titles[1].Monitored || s.Titles[1].File != nil {
		t.Errorf("beta %+v", s.Titles[1])
	}
	want := []string{"system/status", "rootfolder", "qualityprofile", "movie", "moviefile"}
	if got := strings.Join(g.gets, ","); got != strings.Join(want, ",") {
		t.Errorf("requests %v, want %v", got, want)
	}
}

func TestReadTitlesSonarr(t *testing.T) {
	t.Parallel()
	series := func(id, tvdb int) map[string]any {
		return map[string]any{"id": id, "title": "Show", "tvdbId": tvdb, "monitored": true, "path": "/mnt/shows/Show"}
	}
	g := &fakeArr{answers: map[string]any{
		"system/status":  map[string]any{"appName": "Sonarr", "version": "4.0.20"},
		"rootfolder":     []map[string]any{{"id": 1, "path": "/mnt/shows"}},
		"qualityprofile": []map[string]any{{"id": 7, "name": "UHD"}},
		"series":         []map[string]any{series(1, 1001), series(2, 0)},
		"episode?seriesId=1": []map[string]any{
			{"id": 101, "seasonNumber": 1, "episodeNumber": 1, "monitored": true, "episodeFileId": 501, "hasFile": true},
			{"id": 102, "seasonNumber": 1, "episodeNumber": 2, "monitored": true, "episodeFileId": 501, "hasFile": true},
			{"id": 100, "seasonNumber": 0, "episodeNumber": 1}},
		"episodefile?seriesId=1": []map[string]any{{"id": 501, "relativePath": "Season 01/Show.S01E01-E02.mkv",
			"quality": quality("Bluray-2160p")}},
	}}
	var progress []int
	s := readTitles(t, g, "sonarr", func(done, total int) {
		if total != 2 {
			t.Errorf("progress total %d", total)
		}
		progress = append(progress, done)
	})
	if len(progress) != 2 || progress[0] != 1 || progress[1] != 2 {
		t.Errorf("progress %v", progress)
	}
	if len(s.Titles) != 2 {
		t.Fatalf("titles %d", len(s.Titles))
	}
	show := s.Titles[0]
	if show.TvdbID != 1001 || len(show.Episodes) != 3 || len(show.Files) != 1 {
		t.Fatalf("show %+v", show)
	}
	if e := show.Episodes[0]; e.Season != 0 || e.Monitored {
		t.Errorf("first episode %+v", e)
	}
	if f := show.Files[0]; f.Path != "/mnt/shows/Show/Season 01/Show.S01E01-E02.mkv" || len(f.EpisodeArrIDs) != 2 ||
		f.EpisodeArrIDs[0] != 101 || f.EpisodeArrIDs[1] != 102 {
		t.Errorf("file %+v", f)
	}
	if len(s.Titles[1].Episodes) != 0 {
		t.Errorf("a series without a TVDB id read episodes")
	}
}
