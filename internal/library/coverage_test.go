package library

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// TestCoverageReport: a follower missing one title row, one arr file not yet adopted and one adopted link whose arr
// file changed is not complete (1/1/1), each listed, and the check writes nothing; after adopt and refresh it is
// complete. Any skipped file makes it not complete: one the mount refuses to stat is listed with the error's text; a
// path conflict is listed; 201 skipped files are all listed. A grabbing library is not checked for refresh.
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
	// As theatarr library coverage runs it: any write through the writer fails.
	e.exec(`PRAGMA query_only = 1`)
	rep, err := e.lib.Coverage(e.ctx, "movie-theatarr")
	if err != nil {
		t.Fatal(err)
	}
	e.exec(`PRAGMA query_only = 0`)
	if rep.MissingTitles != 1 || rep.Adopt.Linked != 1 || rep.Refresh == nil || rep.Refresh.Swapped != 1 || rep.Complete ||
		len(rep.Items) != 3 || rep.Counts["titles missing_title"] != 1 || rep.Counts["adopt to_link"] != 1 ||
		rep.Counts["refresh to_move"] != 1 || !strings.Contains(rep.Items[0].Title, "Fourth") {
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
	if rep, err = e.lib.Coverage(e.ctx, "movie-theatarr"); err != nil || !rep.Complete || len(rep.Items) != 0 {
		t.Fatalf("after adopt and refresh %+v %v", rep, err)
	}

	// An arr file whose target the mount refuses to stat: not complete, listed with the error's text.
	movie(5, "Locked", true)
	locked := filepath.Join(e.mount, "__all__", "Locked.Release", "m.mkv")
	e.arrFile(5, "radarr", 50, 5, filepath.Join(e.movies, "Locked (2010)", "Locked.mkv"), locked, "WEBDL-1080p")
	e.lib.mount.fs = permFS{path: locked}
	if rep, err = e.lib.Coverage(e.ctx, "movie-theatarr"); err != nil || rep.Complete || len(rep.Items) != 1 ||
		rep.Items[0].Step != "adopt" || rep.Items[0].Reason != AdoptDangling ||
		!strings.Contains(rep.Items[0].Detail, "permission denied") {
		t.Fatalf("unreadable arr file %+v %v", rep, err)
	}
	e.lib.mount.fs = nil
	e.exec(`DELETE FROM version_file WHERE id = 5`)

	// An arr file that is gone from the arr's root: listed with the lstat error.
	movie(7, "Absent", true)
	e.arrFile(7, "radarr", 70, 7, filepath.Join(e.movies, "Absent (2010)", "Absent.mkv"), "", "WEBDL-1080p")
	if rep, err = e.lib.Coverage(e.ctx, "movie-theatarr"); err != nil || rep.Complete || len(rep.Items) != 1 ||
		rep.Items[0].Reason != AdoptMissing || !strings.Contains(rep.Items[0].Detail, "no such file or directory") {
		t.Fatalf("absent arr file %+v %v", rep, err)
	}
	e.exec(`DELETE FROM version_file WHERE id = 7`)

	// Something else at the path the link would take: listed.
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
	if rep, err = e.lib.Coverage(e.ctx, "movie-theatarr"); err != nil || rep.Complete || len(rep.Items) != 1 ||
		rep.Items[0].Reason != AdoptConflict || rep.Items[0].Path == "" {
		t.Fatalf("path conflict %+v %v", rep, err)
	}
	if err := os.Remove(taken); err != nil {
		t.Fatal(err)
	}
	e.exec(`DELETE FROM version_file WHERE id = 6`)

	// 201 arr files that dangle: every one is listed (adopt alone lists 200).
	for i := int64(0); i < 201; i++ {
		id := 1000 + i
		movie(id, fmt.Sprintf("Gone %d", i), true)
		e.arrFile(id, "radarr", id*10, id, filepath.Join(e.movies, fmt.Sprintf("Gone %d", i), "g.mkv"),
			fmt.Sprintf("Missing.%d/m.mkv", i), "WEBDL-1080p")
	}
	if rep, err = e.lib.Coverage(e.ctx, "movie-theatarr"); err != nil || rep.Complete || len(rep.Items) != 201 ||
		rep.Counts["adopt dangling"] != 201 {
		t.Fatalf("201 dangling: %d items, counts %v, %v", len(rep.Items), rep.Counts, err)
	}
	if plan, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr", DryRun: true}); err != nil || len(plan.Problems) != 200 {
		t.Fatalf("adopt lists %d problems, want its cap of 200 (%v)", len(plan.Problems), err)
	}

	// A grabbing library: the refresh is not checked, and the check is not complete.
	e.pol.Router = &policy.Router{Enabled: true}
	e.pol.Versions["movie-theatarr"].Grab = true
	if rep, err = e.lib.Coverage(e.ctx, "movie-theatarr"); err != nil || rep.Refresh != nil || rep.RefreshSkipped == "" ||
		rep.Complete || rep.Counts["refresh not_checked"] != 1 {
		t.Fatalf("grabbing %+v %v", rep, err)
	}

	// A library that follows none is refused.
	if _, err := e.lib.Coverage(e.ctx, "movie-remux"); err == nil {
		t.Fatal("coverage of a library that follows none")
	}
}

// permFS refuses to stat one path, as a mount does for a file it may not read.
type permFS struct {
	osFS
	path string
}

func (f permFS) Stat(name string) (os.FileInfo, error) {
	if name == f.path {
		return nil, &os.PathError{Op: "stat", Path: name, Err: syscall.EACCES}
	}
	return os.Stat(name)
}

// TestCoverageUncoveredEpisodes: a pack the follower's own file covers only in part is not complete, and the
// uncovered episodes are listed.
func TestCoverageUncoveredEpisodes(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	rel := "Show.S01.1080p.WEB-DL-GRP"
	e.release(rel, map[string]float64{"Show.S01E01E02.mkv": 5400, "own.mkv": 2700})
	e.arrFile(1, "sonarr", 20, 2, filepath.Join(e.shows, "Show (2020)", "Season 01", "Show.S01E01E02.mkv"),
		rel+"/Show.S01E01E02.mkv", "WEBDL-1080p", 201, 202)
	target := filepath.Join(e.mount, "__all__", rel, "own.mkv")
	p := filepath.Join(e.theatarrShows, "Show (2020)", "Season 01", "own1.mkv")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at) VALUES (1, ?, ?, 2, 'active', 0, 0)`, p, target)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 21)`)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (1, 201)`)
	rep, err := e.lib.Coverage(e.ctx, "tv-theatarr")
	if err != nil || rep.Complete || rep.Adopt.MissingEpisodes != 1 || len(rep.Items) != 1 ||
		rep.Items[0].Reason != AdoptHasFile || len(rep.Items[0].Episodes) != 1 || rep.Items[0].Episodes[0] != 202 ||
		rep.OwnFilesKept != 1 {
		t.Fatalf("uncovered episodes %+v %v", rep, err)
	}
}

// TestCoverageMountDownKeepsReport: when adopt and refresh cannot run (the mount does not answer), the titles found
// so far are still reported, each step that could not run is listed as not_checked with why, and the error is
// returned.
func TestCoverageMountDownKeepsReport(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	e.exec(`INSERT INTO title (id, kind, name, year, tmdb_id, created_at, updated_at) VALUES (4, 'movie', 'Fourth', 2010, 4, 0, 0)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (40, 4, 'movie-remux', 0, 0)`)
	e.exec(`INSERT INTO version_source (version_id, instance, arr_id) VALUES (40, 'radarr', 4)`)
	e.lib.mount.fs = permFS{path: e.lib.mount.AllPath()}
	rep, err := e.lib.Coverage(e.ctx, "movie-theatarr")
	if err == nil || rep == nil || rep.Complete || rep.Counts["titles missing_title"] != 1 ||
		rep.Counts["adopt not_checked"] != 1 || rep.Counts["refresh not_checked"] != 1 {
		t.Fatalf("mount down %+v %v", rep, err)
	}
}

// ownLink records an active link of the version (not adopted) at p to target, with its symlink.
func (e *parallelEnv) ownLink(id, titleID, versionID int64, p, target string, episodes ...int64) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		e.t.Fatal(err)
	}
	e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at) VALUES (?, ?, ?, ?, 'active', 0, 0)`,
		id, p, target, titleID)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (?, ?)`, id, versionID)
	for _, ep := range episodes {
		e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (?, ?)`, id, ep)
	}
}

// TestCoverageFromTheLibrarysLinks: covered means the library has an active link whose target exists. Its own file
// whose target is gone is not covered (listed with the stat error), though the followed library has a good file;
// its own healthy file is covered and counted as kept, also where adopt would report the path taken or the arr's
// file missing.
func TestCoverageFromTheLibrarysLinks(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.arrFile(1, "radarr", 10, 1, filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie.mkv"), movieRelease+"/movie.mkv", "Remux-2160p")
	gone := filepath.Join(e.mount, "__all__", "Gone.Release", "x.mkv")
	e.ownLink(1, 1, 11, filepath.Join(e.theatarrMovies, "Amelie The Movie (2001)", "own.mkv"), gone)
	rep, err := e.lib.Coverage(e.ctx, "movie-theatarr")
	if err != nil || rep.Complete || len(rep.Items) != 1 || !strings.Contains(rep.Items[0].Detail, "no such file or directory") ||
		!strings.Contains(rep.Items[0].Detail, gone) {
		t.Fatalf("own file gone %+v %v", rep, err)
	}

	// A healthy own file at the path adopt would take, and one for a title whose arr file is missing: covered.
	e2 := newParallelEnv(t, true)
	e2.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e2.arrFile(1, "radarr", 10, 1, filepath.Join(e2.movies, "Amelie The Movie (2001)", "Amelie.mkv"), movieRelease+"/movie.mkv", "Remux-2160p")
	plan, err := e2.lib.Adopt(e2.ctx, AdoptOptions{Version: "movie-theatarr", DryRun: true})
	if err != nil || len(plan.Links) != 1 {
		t.Fatalf("plan %+v %v", plan, err)
	}
	e2.release("Own.Release", map[string]float64{"o.mkv": 7000})
	own := filepath.Join(e2.mount, "__all__", "Own.Release", "o.mkv")
	e2.ownLink(1, 1, 11, plan.Links[0].Path, own)
	e2.exec(`INSERT INTO title (id, kind, name, year, tmdb_id, created_at, updated_at) VALUES (3, 'movie', 'Third', 2010, 3, 0, 0)`)
	e2.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (30, 3, 'movie-remux', 0, 0)`)
	e2.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (31, 3, 'movie-theatarr', 'wanted', 0, 0)`)
	e2.exec(`INSERT INTO version_source (version_id, instance, arr_id) VALUES (30, 'radarr', 3)`)
	e2.arrFile(3, "radarr", 30, 3, filepath.Join(e2.movies, "Third (2010)", "Third.mkv"), "", "WEBDL-1080p")
	e2.ownLink(2, 3, 31, filepath.Join(e2.theatarrMovies, "Third (2010)", "own.mkv"), own)
	if rep, err := e2.lib.Coverage(e2.ctx, "movie-theatarr"); err != nil || !rep.Complete || rep.OwnFilesKept != 2 {
		t.Fatalf("healthy own files %+v %v", rep, err)
	}
}

// TestCoverageSeriesTargetError: an own episode file whose target cannot be checked is not covered; the item carries
// the error and the target.
func TestCoverageSeriesTargetError(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	rel := "Show.S01.1080p.WEB-DL-GRP"
	e.release(rel, map[string]float64{"Show.S01E01E02.mkv": 5400, "own.mkv": 2700})
	e.arrFile(1, "sonarr", 20, 2, filepath.Join(e.shows, "Show (2020)", "Season 01", "Show.S01E01E02.mkv"),
		rel+"/Show.S01E01E02.mkv", "WEBDL-1080p", 201, 202)
	target := filepath.Join(e.mount, "__all__", rel, "own.mkv")
	e.ownLink(1, 2, 21, filepath.Join(e.theatarrShows, "Show (2020)", "Season 01", "own.mkv"), target, 201, 202)
	e.lib.mount.fs = permFS{path: target}
	rep, err := e.lib.Coverage(e.ctx, "tv-theatarr")
	if err != nil || rep.Complete || len(rep.Items) == 0 || !strings.Contains(rep.Items[0].Detail, "permission denied") ||
		!strings.Contains(rep.Items[0].Detail, target) {
		t.Fatalf("unreadable own episode file %+v %v", rep, err)
	}
}
