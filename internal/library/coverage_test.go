package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// TestCoverageReport: a follower missing one title row, one arr file not yet adopted and one adopted link whose arr
// file changed is not complete (1/1/1), and the check writes nothing; after adopt and refresh it is complete. An arr
// file that dangles is an expected exception; a path conflict fails; a grabbing library is not checked for refresh.
func TestCoverageReport(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	movie := func(id int64, name string, follower bool) {
		e.exec(`INSERT INTO title (id, kind, name, year, tmdb_id, created_at, updated_at) VALUES (?, 'movie', ?, 2010, ?, 0, 0)`,
			id, name, id)
		e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (?, ?, 'movie-remux', 0, 0)`, id*10, id)
		e.exec(`INSERT INTO version_source (version_id, instance, arr_id) VALUES (?, 'radarr', ?)`, id*10, id)
		if follower {
			e.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (?, ?, 'movie-theatarr', 'wanted', 0, 0)`, id*10+1, id)
		}
	}
	old := "Amelie.2001.1080p.WEB-DL-POKE"
	e.release(old, map[string]float64{"movie.mkv": 7000})
	arrDir := filepath.Join(e.movies, "Amelie The Movie (2001)")
	e.arrFile(1, "radarr", 10, 1, filepath.Join(arrDir, "Amelie.WEB.mkv"), old+"/movie.mkv", "WEBDL-1080p")
	if rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr"}); err != nil || rep.Linked != 1 {
		t.Fatalf("adopt %+v %v", rep, err)
	}
	upgraded := "Amelie.2001.2160p.Remux-BYNDR"
	e.release(upgraded, map[string]float64{"movie.mkv": 7000})
	e.arrUpgrade(1, old, 2, "radarr", 10, 1, filepath.Join(arrDir, "Amelie.Remux.mkv"), upgraded+"/movie.mkv", "Remux-2160p")
	movie(3, "Third", true)
	e.release("Third.2010.1080p.WEB-DL-GRP", map[string]float64{"t.mkv": 7000})
	e.arrFile(3, "radarr", 30, 3, filepath.Join(e.movies, "Third (2010)", "Third.mkv"), "Third.2010.1080p.WEB-DL-GRP/t.mkv", "WEBDL-1080p")
	movie(4, "Fourth", false)

	before := strings.Join(tree(t, e.dir), "|")
	links := e.one(`SELECT COUNT(*) FROM link`)
	rep, err := e.lib.Coverage(e.ctx, "movie-theatarr")
	if err != nil {
		t.Fatal(err)
	}
	if rep.MissingTitles != 1 || len(rep.Missing) != 1 || rep.Missing[0].Name != "Fourth" || rep.Adopt.Linked != 1 ||
		rep.Refresh == nil || rep.Refresh.Swapped != 1 || rep.Complete || rep.Outcome != CoverageFailed ||
		len(rep.Failures) != 3 || len(rep.Exceptions) != 0 {
		t.Fatalf("coverage %+v", rep)
	}
	if after := strings.Join(tree(t, e.dir), "|"); after != before || e.one(`SELECT COUNT(*) FROM link`) != links {
		t.Fatal("coverage wrote")
	}

	// After the title row, adopt and refresh: complete.
	e.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (41, 4, 'movie-theatarr', 'wanted', 0, 0)`)
	if _, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.lib.Refresh(e.ctx, RefreshOptions{Version: "movie-theatarr"}); err != nil {
		t.Fatal(err)
	}
	if rep, err = e.lib.Coverage(e.ctx, "movie-theatarr"); err != nil || !rep.Complete || rep.Outcome != CoverageComplete ||
		len(rep.Failures)+len(rep.Exceptions) != 0 {
		t.Fatalf("after adopt and refresh %+v %v", rep, err)
	}

	// An arr file that dangles is an expected exception, named with its reason.
	movie(5, "Gone", true)
	e.arrFile(5, "radarr", 50, 5, filepath.Join(e.movies, "Gone (2010)", "Gone.mkv"), "Missing.Release/m.mkv", "WEBDL-1080p")
	if rep, err = e.lib.Coverage(e.ctx, "movie-theatarr"); err != nil || rep.Complete || rep.Outcome != CoverageExceptions ||
		len(rep.Failures) != 0 || len(rep.Exceptions) != 1 || rep.Exceptions[0].Reason != AdoptDangling ||
		rep.Exceptions[0].Count != 1 || rep.Exceptions[0].Note == "" {
		t.Fatalf("dangling arr file %+v %v", rep, err)
	}

	// Something else at the path the link would take fails the check.
	movie(6, "Sixth", true)
	e.release("Sixth.2010.1080p.WEB-DL-GRP", map[string]float64{"s.mkv": 7000})
	e.arrFile(6, "radarr", 60, 6, filepath.Join(e.movies, "Sixth (2010)", "Sixth.mkv"), "Sixth.2010.1080p.WEB-DL-GRP/s.mkv", "WEBDL-1080p")
	plan, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr", DryRun: true})
	if err != nil || len(plan.Links) != 1 {
		t.Fatalf("plan %+v %v", plan, err)
	}
	taken := plan.Links[0].Path
	if err := os.MkdirAll(filepath.Dir(taken), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taken, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rep, err = e.lib.Coverage(e.ctx, "movie-theatarr"); err != nil || rep.Outcome != CoverageFailed ||
		len(rep.Failures) != 1 || rep.Failures[0].Reason != AdoptConflict {
		t.Fatalf("path conflict %+v %v", rep, err)
	}

	// A grabbing library: the refresh is not checked, and the check is not complete.
	e.pol.Router = &policy.Router{Enabled: true}
	e.pol.Versions["movie-theatarr"].Grab = true
	if rep, err = e.lib.Coverage(e.ctx, "movie-theatarr"); err != nil || rep.Refresh != nil || rep.RefreshSkipped == "" ||
		rep.Outcome != CoverageFailed {
		t.Fatalf("grabbing %+v %v", rep, err)
	}

	// A library that follows none is refused.
	if _, err := e.lib.Coverage(e.ctx, "movie-remux"); err == nil {
		t.Fatal("coverage of a library that follows none")
	}
}
