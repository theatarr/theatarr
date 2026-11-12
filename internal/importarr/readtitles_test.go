package importarr

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/arr"
)

func readTitles(t *testing.T, srv *httptest.Server, flavour string, progress func(done, total int)) *Snapshot {
	t.Helper()
	g, err := arr.New(srv.URL, testKey, arr.Options{MinInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	s, err := ReadTitles(context.Background(), g, flavour, progress)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// endpoints is the list of endpoints requested, without queries.
func endpoints(reqs []string) []string {
	var out []string
	for _, r := range reqs {
		ep, _, _ := strings.Cut(r, "?")
		out = append(out, ep)
	}
	return out
}

func TestReadTitlesRadarr(t *testing.T) {
	t.Parallel()
	fake := newFake("/mnt/movies-a")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	calls := 0
	s := readTitles(t, srv, "radarr", func(done, total int) {
		calls++
		if done != 4 || total != 4 {
			t.Errorf("progress %d/%d", done, total)
		}
	})
	if calls != 1 {
		t.Errorf("progress called %d times", calls)
	}
	if s.AppVersion != "6.4.4.10685" || len(s.RootFolders) != 1 || s.RootFolders[0] != "/mnt/movies-a" {
		t.Errorf("version %q roots %v", s.AppVersion, s.RootFolders)
	}
	if len(s.Profiles) != 1 || s.Profiles[0].ArrID != 7 || s.Profiles[0].Name != "Remux + WEB 2160p" {
		t.Errorf("profiles %+v", s.Profiles)
	}
	if len(s.Titles) != 4 {
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
	if got := endpoints(fake.requests); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("requests %v, want %v", got, want)
	}
}

func TestReadTitlesSonarr(t *testing.T) {
	t.Parallel()
	fake := newFakeSonarr("/mnt/shows")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	var progress []int
	s := readTitles(t, srv, "sonarr", func(done, total int) {
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
	if show.TvdbID != 1001 || len(show.Episodes) != 4 || len(show.Files) != 1 {
		t.Fatalf("show %+v", show)
	}
	for _, e := range show.Episodes {
		if (e.Season == 0) == e.Monitored {
			t.Errorf("episode S%02dE%02d monitored %v", e.Season, e.Number, e.Monitored)
		}
	}
	if ids := show.Files[0].EpisodeArrIDs; len(ids) != 2 || ids[0] != 101 || ids[1] != 102 {
		t.Errorf("file episodes %v", ids)
	}
	for _, ep := range endpoints(fake.requests) {
		switch ep {
		case "system/status", "rootfolder", "qualityprofile", "series", "episode", "episodefile":
		default:
			t.Errorf("unexpected request %s", ep)
		}
	}
}
