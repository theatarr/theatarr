package naming

import (
	"strings"
	"testing"
	"time"

	"github.com/dlclark/regexp2"
)

// forceTimeout makes every match of re time out until the test ends. Not parallel: the regexes are package vars.
func forceTimeout(t *testing.T, re *regexp2.Regexp) {
	old := re.MatchTimeout
	re.MatchTimeout = -time.Hour
	t.Cleanup(func() { re.MatchTimeout = old })
}

// A regex match timeout (a starved CPU) fails the naming with an error; it never yields a wrong or empty name
// ("Season 01/.mkv").
func TestNamingMatchTimeoutFails(t *testing.T) {
	scfg := DefaultSeriesConfig()
	scfg.Rename = true
	s := &Series{Title: "Show", Year: 2020, Type: SeriesStandard}
	eps := []Episode{{Season: 1, Number: 1, Title: "Pilot"}}
	ef := &EpisodeFile{ID: 1, Quality: Quality{Title: "WEBDL-1080p", Version: 1}, ReleaseGroup: "GRP"}
	mcfg := DefaultMovieConfig()
	mcfg.Rename = true
	m := &Movie{Title: "The Movie", Year: 2001}
	mf := &MovieFile{Quality: Quality{Title: "Remux-2160p", Version: 1}, ReleaseGroup: "GRP"}
	wantSeries, err := SeriesFileName(&scfg, s, eps, ef, ".mkv", "/tv/Show (2020)/Season 01")
	if err != nil {
		t.Fatal(err)
	}
	wantMovie, err := MovieFileName(&mcfg, m, mf)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		re   *regexp2.Regexp
	}{
		{"sonarr tokens", sonarrTitleRegex},
		{"radarr tokens", radarrTitleRegex},
		{"cleanup", fileNameCleanupRegex},
		{"trim separators", trimSeparatorsRegex},
		{"season", seasonRegex},
		{"episode", episodeRegex},
		{"season episode pattern", seasonEpisodePatternRegex},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forceTimeout(t, tc.re)
			if got, err := SeriesFileName(&scfg, s, eps, ef, ".mkv", "/tv/Show (2020)/Season 01"); err == nil && got != wantSeries {
				t.Errorf("series file name %q without an error", got)
			} else if err != nil && !strings.Contains(err.Error(), "match timeout") {
				t.Errorf("series error %v", err)
			}
			if got, err := MovieFileName(&mcfg, m, mf); err == nil && got != wantMovie {
				t.Errorf("movie file name %q without an error", got)
			} else if err != nil && !strings.Contains(err.Error(), "match timeout") {
				t.Errorf("movie error %v", err)
			}
		})
	}
}

// The folders fail the same way.
func TestFolderMatchTimeoutFails(t *testing.T) {
	scfg, mcfg := DefaultSeriesConfig(), DefaultMovieConfig()
	forceTimeout(t, sonarrTitleRegex)
	forceTimeout(t, radarrTitleRegex)
	if got, err := SeriesFolder(&scfg, &Series{Title: "Show"}); err == nil {
		t.Errorf("series folder %q without an error", got)
	}
	if got, err := SeasonFolder(&scfg, &Series{Title: "Show"}, 1); err == nil {
		t.Errorf("season folder %q without an error", got)
	}
	if got, err := MovieFolder(&mcfg, &Movie{Title: "Movie", Year: 2001}); err == nil {
		t.Errorf("movie folder %q without an error", got)
	}
}

// A format that renders nothing for the file is refused, not named ".mkv".
func TestEmptyFileNameRefused(t *testing.T) {
	t.Parallel()
	mcfg := DefaultMovieConfig()
	mcfg.Rename = true
	mcfg.StandardMovieFormat = "{Edition Tags}"
	if got, err := MovieFileName(&mcfg, &Movie{Title: "Movie", Year: 2001}, &MovieFile{}); err == nil {
		t.Errorf("movie file name %q", got)
	}
	// An empty final component is not dropped for the folder before it (Radarr would name the file "Movie").
	mcfg.StandardMovieFormat = "{Movie Title}/{Edition Tags}"
	if got, err := MovieFileName(&mcfg, &Movie{Title: "Movie", Year: 2001}, &MovieFile{}); err == nil {
		t.Errorf("movie file name with an empty last component %q", got)
	}
	if got, err := MovieFileName(&mcfg, &Movie{Title: "Movie", Year: 2001}, &MovieFile{Edition: "Director's Cut"}); err != nil || got != "Movie/Director's Cut" {
		t.Errorf("movie file name with an edition %q %v", got, err)
	}
	scfg := DefaultSeriesConfig()
	scfg.Rename = true
	scfg.StandardEpisodeFormat = "Season {season:00}/{Release Hash}"
	eps := []Episode{{Season: 1, Number: 1}}
	if got, err := SeriesFileName(&scfg, &Series{Title: "Show", Type: SeriesStandard}, eps, &EpisodeFile{}, ".mkv", "/tv"); err == nil {
		t.Errorf("series file name %q", got)
	}
	scfg.StandardEpisodeFormat = "{Release Hash}"
	if got, err := SeriesFileName(&scfg, &Series{Title: "Show", Type: SeriesStandard}, eps, &EpisodeFile{}, ".mkv", "/tv"); err == nil {
		t.Errorf("series file name %q", got)
	}
}

// A CI failure: the token regex matches once, then the next match times out. regexp2's ReplaceFunc returns ""
// and no error there; the naming must fail instead.
func TestNamingTimeoutAfterFirstMatch(t *testing.T) {
	cfg := DefaultSeriesConfig()
	cfg.Rename = true
	s := &Series{Title: "Show", Type: SeriesStandard}
	f := &EpisodeFile{ID: 1, Quality: Quality{Title: "WEBDL-1080p", Version: 1}}
	eps := []Episode{{Season: 1, Number: 1, Title: "Pilot"}}
	forceTimeout(t, sonarrTitleRegex)
	sonarrTitleRegex.MatchTimeout = time.Minute
	h := handlers{}
	h.set("{Series Title}", func(*tokenMatch) string {
		sonarrTitleRegex.MatchTimeout = -time.Hour // the next match times out
		return s.Title
	})
	var err error
	got := func() string {
		defer catch(&err)
		return sonarrReplaceTokens("{Series Title} - {Episode Title}", h, &cfg, false)
	}()
	if err == nil {
		t.Fatalf("rendered %q without an error", got)
	}
	sonarrTitleRegex.MatchTimeout = -time.Hour
	if name, err := SeriesFileName(&cfg, s, eps, f, ".mkv", "/tv/Show/Season 01"); err == nil {
		t.Errorf("file name %q without an error", name)
	}
}
