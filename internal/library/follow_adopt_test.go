package library

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// An arr import must populate a new follower copy, without adopting unrelated titles or changing the arr link.
func TestFollowerAdoptsNewMovieAfterImport(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	e.withScans()
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	arr := filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie.mkv")
	e.arrFile(1, "radarr", 10, 1, arr, movieRelease+"/movie.mkv", "Remux-2160p")
	e.exec(`INSERT INTO title (id, kind, name, year, tmdb_id, created_at, updated_at)
		VALUES (3, 'movie', 'Other', 2010, 300, 0, 0)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES
		(30, 3, 'movie-remux', 0, 0), (31, 3, 'movie-theatarr', 0, 0)`)
	e.exec(`INSERT INTO version_source (version_id, instance, arr_id) VALUES (30, 'radarr', 3)`)
	e.release("Other.2010.Remux", map[string]float64{"other.mkv": 7000})
	e.arrFile(3, "radarr", 30, 3, filepath.Join(e.movies, "Other (2010)", "Other.mkv"),
		"Other.2010.Remux/other.mkv", "Remux-2160p")
	before, err := os.Readlink(arr)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := e.lib.RefreshFollowers(e.ctx, "movie-remux", []int64{1}); err != nil {
			t.Fatal(err)
		}
		if got := e.one(`SELECT COUNT(*) FROM link WHERE state = 'active' AND title_id = 1`); got != "1" {
			t.Fatalf("new movie not adopted exactly once: %s", got)
		}
		if got := e.one(`SELECT COUNT(*) FROM link WHERE title_id = 3`); got != "0" {
			t.Fatalf("unrelated title adopted: %s", got)
		}
	}
	if got, err := os.Readlink(arr); err != nil || got != before {
		t.Fatalf("arr source changed: %q %v", got, err)
	}
	link := e.one(`SELECT path FROM link WHERE title_id = 1 AND state = 'active'`)
	if got, err := os.Readlink(link); err != nil || got != before {
		t.Fatalf("follower target: %q %v", got, err)
	}
	if got := e.one(`SELECT COUNT(*) FROM job WHERE kind = 'plex.scan'`); got != "1" {
		t.Fatalf("expected one Plex scan, got %s", got)
	}
}

// Refreshing an existing episode must also adopt an episode newly imported by Sonarr.
func TestFollowerAdoptsNewEpisodeAfterImport(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	folder := "Show.S01.1080p.WEB-DL-GRP"
	e.release(folder, map[string]float64{"Show.S01E01.mkv": 1000, "Show.S01E02.mkv": 1000})
	e.arrFile(1, "sonarr", 20, 2, filepath.Join(e.shows, "Show (2020)", "Show.S01E01.mkv"),
		folder+"/Show.S01E01.mkv", "WEBDL-1080p", 201)
	if _, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "tv-theatarr"}); err != nil {
		t.Fatal(err)
	}
	e.arrFile(2, "sonarr", 20, 2, filepath.Join(e.shows, "Show (2020)", "Show.S01E02.mkv"),
		folder+"/Show.S01E02.mkv", "WEBDL-1080p", 202)
	for range 2 {
		if err := e.lib.RefreshFollowers(e.ctx, "tv", []int64{2}); err != nil {
			t.Fatal(err)
		}
		if got := e.one(`SELECT COUNT(*) FROM link l JOIN link_episode le ON le.link_id = l.id
			WHERE l.state = 'active' AND le.episode_id IN (201, 202)`); got != "2" {
			t.Fatalf("new episode not adopted exactly once: %s", got)
		}
	}
}

// An unreadable target may arrive after its one changed-title import. The hourly catch-up must recover it.
func TestFollowerCatchUpAdoptsTargetThatArrivesLater(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	e.release("Available.Release", map[string]float64{"other.mkv": 7000})
	e.arrFile(1, "radarr", 10, 1, filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie.mkv"),
		movieRelease+"/movie.mkv", "Remux-2160p")
	if err := e.lib.RefreshFollowers(e.ctx, "movie-remux", []int64{1}); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT COUNT(*) FROM link`); got != "0" {
		t.Fatalf("unreadable source adopted: %s", got)
	}
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	if err := e.lib.RefreshFollowersAll(e.ctx, "movie-remux"); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT COUNT(*) FROM link WHERE state = 'active'`); got != "1" {
		t.Fatalf("catch-up missed the recovered source: %s", got)
	}
}

func TestFollowerAutomaticAdoptionHonorsGates(t *testing.T) {
	t.Parallel()
	for _, closed := range []string{"library", "grabbing"} {
		t.Run(closed, func(t *testing.T) {
			e := newParallelEnv(t, closed != "library")
			if closed == "grabbing" {
				e.pol.Router = &policy.Router{Enabled: true}
				e.pol.Versions["movie-theatarr"].Grab = true
			}
			e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
			e.arrFile(1, "radarr", 10, 1, filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie.mkv"),
				movieRelease+"/movie.mkv", "Remux-2160p")
			if err := e.lib.RefreshFollowers(e.ctx, "movie-remux", []int64{1}); err != nil {
				t.Fatal(err)
			}
			if err := e.lib.RefreshFollowersAll(e.ctx, "movie-remux"); err != nil {
				t.Fatal(err)
			}
			if got := e.one(`SELECT COUNT(*) FROM link`); got != "0" {
				t.Fatalf("closed %s gate adopted files: %s", closed, got)
			}
		})
	}
}
