package arrview

import (
	"fmt"
	"testing"
)

func TestMovieUsesRefreshedDates(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.exec(`INSERT INTO title_meta (title_id, movie_dates_at, attempted_at, refresh_after) VALUES (3,1,1,1)`)
	e.exec(`INSERT INTO title_release_date (title_id, source, type, date) VALUES
		(3,'tmdb-art','theatrical','2026-09-01'),(3,'tmdb-art','digital','2026-10-01')`)
	var movie map[string]any
	e.call("GET", fmt.Sprintf("/radarr/api/v3/movie/%d", IDBase+3), nil, 200, &movie)
	if movie["status"] != "released" || movie["isAvailable"] != true || movie["digitalRelease"] != "2026-10-01T00:00:00Z" {
		t.Fatalf("refreshed movie: %v", movie)
	}
	e.exec(`DELETE FROM title_release_date WHERE title_id = 3 AND source = 'tmdb-art'`)
	e.call("GET", fmt.Sprintf("/radarr/api/v3/movie/%d", IDBase+3), nil, 200, &movie)
	if movie["status"] != "announced" || movie["isAvailable"] != false {
		t.Fatalf("removed dates: %v", movie)
	}
}

func TestCachedReleaseGrabSkipsAvailability(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	var releases []map[string]any
	e.call("GET", fmt.Sprintf("/radarr/api/v3/release?movieId=%d", IDBase+1), nil, 200, &releases)
	if len(releases) == 0 {
		t.Fatal("no cached release")
	}
	e.exec(`DELETE FROM title_release_date WHERE title_id = 1`)
	e.call("POST", "/radarr/api/v3/release", map[string]any{"guid": releases[0]["guid"], "indexerId": releases[0]["indexerId"]}, 200, nil)
	if n := e.one(`SELECT COUNT(*) FROM job WHERE kind = 'router.route'`); n != 2 {
		t.Fatalf("manual grab queued %d routes", n)
	}
}

// TestViewPathFromFiles: a title's path is the folder a file of it already has under the root (an imported file or
// an active link), not the computed "Title (Year)"; the computed one is used only when no folder is known.
func TestViewPathFromFiles(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var s map[string]any
	e.call("GET", fmt.Sprintf("/sonarr/api/v3/series/%d", IDBase+10), nil, 200, &s)
	if s["path"] != "/media/tv/The Show (2019)" {
		t.Fatalf("no file: path %v", s["path"])
	}
	e.exec(`INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path, relative_path, size_bytes,
		quality) VALUES (3, 10, 10, 'sonarr_4k_debrid', 601, '/media/tv/The Show/Season 01/The Show - S01E01.mkv',
		'Season 01/The Show - S01E01.mkv', 1, 'WEBDL-2160p')`)
	e.call("GET", fmt.Sprintf("/sonarr/api/v3/series/%d", IDBase+10), nil, 200, &s)
	if s["path"] != "/media/tv/The Show" || s["rootFolderPath"] != "/media/tv" {
		t.Fatalf("imported file: path %v root %v", s["path"], s["rootFolderPath"])
	}
	// A movie's active link names its folder too.
	e.exec(`INSERT INTO link (id, path, target, kind, title_id, state, created_at, updated_at) VALUES
		(7, '/media/movies-web/Beta Old Name/Beta.mkv', '/mnt/zurg/Beta.mkv', 'symlink', 2, 'active', 0, 0)`)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (7, 3)`)
	var m map[string]any
	e.call("GET", fmt.Sprintf("/v/movie-web/radarr/api/v3/movie/%d", IDBase+2), nil, 200, &m)
	if m["path"] != "/media/movies-web/Beta Old Name" {
		t.Fatalf("linked movie: path %v root %v", m["path"], m["rootFolderPath"])
	}
}
