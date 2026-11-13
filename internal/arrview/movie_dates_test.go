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
