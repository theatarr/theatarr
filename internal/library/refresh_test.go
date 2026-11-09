package library

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
)

// withScans gives the parallel versions Plex sections (22 movies, 24 series) and rebuilds the library on them.
func (e *parallelEnv) withScans() {
	e.t.Helper()
	e.pol.Library.Versions["movie-theatarr"].PlexSection = 22
	e.pol.Library.Versions["tv-theatarr"].PlexSection = 24
	engine, err := decide.New(e.pol, decide.Options{Now: func() time.Time { return e.now }})
	if err != nil {
		e.t.Fatal(err)
	}
	e.lib = New(Options{Store: e.st, Policy: e.pol, Engine: engine, Queue: e.queue, Now: func() time.Time { return e.now }})
}

// arrUpgrade is the arr replacing its file id old by a new one (the arr import drops the old row): the arr's old
// link and row go, the old release is deleted from the mount when gone, and the new file is linked as arrFile does.
func (e *parallelEnv) arrUpgrade(old int64, gone string, id int64, instance string, versionID, titleID int64, p, target,
	quality string, episodes ...int64) {
	e.t.Helper()
	var oldPath string
	if err := e.st.Reader().QueryRowContext(e.ctx, `SELECT path FROM version_file WHERE id = ?`, old).Scan(&oldPath); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Remove(oldPath); err != nil {
		e.t.Fatal(err)
	}
	e.exec(`DELETE FROM version_file_episode WHERE version_file_id = ?`, old)
	e.exec(`DELETE FROM version_file WHERE id = ?`, old)
	if gone != "" {
		if err := os.RemoveAll(filepath.Join(e.mount, "__all__", gone)); err != nil {
			e.t.Fatal(err)
		}
	}
	e.arrFile(id, instance, versionID, titleID, p, target, quality, episodes...)
}

// TestRefreshMovieUpgrade: the arr upgraded the movie and deleted the old release (The Dog Stars: POKE to BYNDR), so
// the adopted link dangles. A dry run plans the swap and writes nothing; the refresh links the new file under Theatarr's
// naming, moves the old link to the trash, retires its row, records the actions, queues a Plex scan and leaves the
// arr's link alone; a second run is a no-op.
func TestRefreshMovieUpgrade(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	e.withScans()
	old := "Amelie.2001.1080p.WEB-DL-POKE"
	e.release(old, map[string]float64{"movie.mkv": 7000})
	arrDir := filepath.Join(e.movies, "Amelie The Movie (2001)")
	e.arrFile(1, "radarr", 10, 1, filepath.Join(arrDir, "Amelie.WEB.mkv"), old+"/movie.mkv", "WEBDL-1080p")
	if rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr"}); err != nil || rep.Linked != 1 {
		t.Fatalf("adopt %+v %v", rep, err)
	}
	folder := filepath.Join(e.theatarrMovies, "Amelie The Movie (2001)")
	adopted := filepath.Join(folder, "Amelie The Movie (2001) {imdb-tt0211915} - [WEBDL-1080p]-GRP.mkv")
	oldTarget := filepath.Join(e.mount, "__all__", old, "movie.mkv")
	if got := e.one(`SELECT state FROM version WHERE id = 11`); got != "filled" {
		t.Fatalf("state after adopt %s", got)
	}
	e.exec(`DELETE FROM job`)

	upgraded := "Amelie.2001.2160p.Remux-BYNDR"
	e.release(upgraded, map[string]float64{"movie.mkv": 7000})
	arrNew := filepath.Join(arrDir, "Amelie.Remux.mkv")
	e.arrUpgrade(1, old, 2, "radarr", 10, 1, arrNew, upgraded+"/movie.mkv", "Remux-2160p")
	newTarget := filepath.Join(e.mount, "__all__", upgraded, "movie.mkv")
	want := filepath.Join(folder, "Amelie The Movie (2001) {imdb-tt0211915} - [Remux-2160p]-GRP.mkv")

	// Plain adopt keeps the parallel version's file: that is the gap refresh closes.
	if rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr"}); err != nil || rep.Linked != 0 || rep.HasFile != 1 {
		t.Fatalf("adopt after the upgrade %+v %v", rep, err)
	}

	before := strings.Join(tree(t, e.dir), "|")
	rep, err := e.lib.Refresh(e.ctx, RefreshOptions{Version: "movie-theatarr", DryRun: true})
	if err != nil || rep.Links != 1 || rep.Swapped != 1 || len(rep.Swaps) != 1 || rep.Swaps[0].Path != want ||
		rep.Swaps[0].Old != adopted || rep.Swaps[0].Target != newTarget {
		t.Fatalf("dry run %+v %v", rep, err)
	}
	if after := strings.Join(tree(t, e.dir), "|"); after != before {
		t.Fatal("dry run wrote to disk")
	}
	if got := e.one(`SELECT COUNT(*) FROM link WHERE state = 'active'`) + " " + e.one(`SELECT COUNT(*) FROM link_trash`) +
		" " + e.one(`SELECT COUNT(*) FROM job`); got != "1 0 0" {
		t.Fatalf("dry run recorded: links, trash, jobs %s", got)
	}

	rep, err = e.lib.Refresh(e.ctx, RefreshOptions{Version: "movie-theatarr"})
	if err != nil || rep.Swapped != 1 || rep.Current != 0 || len(rep.Skipped) != 0 {
		t.Fatalf("refresh %+v %v", rep, err)
	}
	if tg, err := os.Readlink(want); err != nil || tg != newTarget {
		t.Fatalf("new link -> %q %v", tg, err)
	}
	if _, err := os.Lstat(adopted); !os.IsNotExist(err) {
		t.Fatalf("the old link is still in the root: %v", err)
	}
	tp := filepath.Join(e.trash, "movie-theatarr", "2026-10-26", "Amelie The Movie (2001)", filepath.Base(adopted))
	if tg, err := os.Readlink(tp); err != nil || tg != oldTarget || rep.Swaps[0].TrashPath != tp {
		t.Fatalf("trash copy %s -> %q %v (%+v)", tp, tg, err, rep.Swaps)
	}
	if tg, err := os.Readlink(arrNew); err != nil || tg != newTarget {
		t.Fatalf("the arr's link changed: %q %v", tg, err)
	}
	if got := e.one(`SELECT GROUP_CONCAT(state || ' ' || quality, ', ') FROM (SELECT * FROM link ORDER BY id)`); got !=
		"unlinked WEBDL-1080p, active Remux-2160p" {
		t.Errorf("link rows %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM link_trash WHERE version_key = 'movie-theatarr' AND path = ? AND trash_path = ?
		AND target = ? AND link_id = 1`, adopted, tp, oldTarget); got != "1" {
		t.Errorf("link_trash rows %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM library_action WHERE kind = 'link' AND mode = 'applied' AND link_id = 2
		AND detail LIKE 'adopted from radarr ' || ? || ' (refresh: replaces %'`, arrNew); got != "1" {
		t.Errorf("new link action %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM library_action WHERE kind = 'replace' AND mode = 'applied' AND link_id = 1
		AND detail LIKE 'refresh: the arr''s file is now %moved aside to %'`); got != "1" {
		t.Errorf("replace action %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM job WHERE kind = 'plex.scan' AND json_extract(payload, '$.path') = ?
		AND json_extract(payload, '$.section') = 22`, folder); got != "1" {
		t.Errorf("plex scans %s", got)
	}
	if got := e.one(`SELECT state FROM version WHERE id = 11`); got != "cutoff_met" {
		t.Errorf("state %s", got)
	}
	if f, err := decide.LoadExisting(e.ctx, e.st.Reader(), 11); err != nil || f == nil || f.Quality != "Remux-2160p" {
		t.Fatalf("existing %+v %v", f, err)
	}

	// Twice: nothing to do.
	before = strings.Join(tree(t, e.dir), "|")
	actions := e.one(`SELECT COUNT(*) FROM library_action`)
	rep, err = e.lib.Refresh(e.ctx, RefreshOptions{Version: "movie-theatarr"})
	if err != nil || rep.Links != 1 || rep.Current != 1 || rep.Swapped != 0 || len(rep.Skipped) != 0 {
		t.Fatalf("second run %+v %v", rep, err)
	}
	if after := strings.Join(tree(t, e.dir), "|"); after != before {
		t.Fatal("second run changed the tree")
	}
	if got := e.one(`SELECT COUNT(*) FROM library_action`); got != actions {
		t.Fatalf("second run recorded actions: %s, was %s", got, actions)
	}
	// Adopt agrees: the link is in place.
	if rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr"}); err != nil || rep.Linked != 0 || rep.Existing != 1 {
		t.Fatalf("adopt after refresh %+v %v", rep, err)
	}
}

// TestRefreshLeavesLinks: an arr file removed with no replacement and an arr file whose target is missing on the
// mount (a zurg hiccup) both leave the adopted link as it is, reported; a closed gate refuses a real run.
func TestRefreshLeavesLinks(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) (*parallelEnv, string, string) {
		e := newParallelEnv(t, true)
		old := "Amelie.2001.1080p.WEB-DL-POKE"
		e.release(old, map[string]float64{"movie.mkv": 7000})
		e.arrFile(1, "radarr", 10, 1, filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie.WEB.mkv"),
			old+"/movie.mkv", "WEBDL-1080p")
		if rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr"}); err != nil || rep.Linked != 1 {
			t.Fatalf("adopt %+v %v", rep, err)
		}
		adopted := filepath.Join(e.theatarrMovies, "Amelie The Movie (2001)", "Amelie The Movie (2001) {imdb-tt0211915} - [WEBDL-1080p]-GRP.mkv")
		return e, adopted, filepath.Join(e.mount, "__all__", old, "movie.mkv")
	}
	check := func(t *testing.T, e *parallelEnv, adopted, target, reason string) {
		t.Helper()
		before := strings.Join(tree(t, e.dir), "|")
		for range 2 {
			rep, err := e.lib.Refresh(e.ctx, RefreshOptions{Version: "movie-theatarr"})
			if err != nil || rep.Swapped != 0 || rep.Skipped[reason] != 1 || len(rep.Problems) != 1 ||
				rep.Problems[0].Path != adopted {
				t.Fatalf("refresh %+v %v", rep, err)
			}
		}
		if after := strings.Join(tree(t, e.dir), "|"); after != before {
			t.Fatal("refresh changed the tree")
		}
		if tg, err := os.Readlink(adopted); err != nil || tg != target {
			t.Fatalf("link -> %q %v", tg, err)
		}
		if got := e.one(`SELECT state FROM link WHERE path = ?`, adopted) + " " + e.one(`SELECT COUNT(*) FROM link_trash`); got != "active 0" {
			t.Fatalf("link row, trash %s", got)
		}
	}

	t.Run("removed without replacement", func(t *testing.T) {
		e, adopted, target := setup(t)
		// The account holds other releases: a listing with nothing in it reads as a mount that is down.
		e.release("Unrelated.Release", map[string]float64{"other.mkv": 7000})
		os.Remove(filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie.WEB.mkv"))
		os.RemoveAll(filepath.Dir(target))
		e.exec(`DELETE FROM version_file WHERE id = 1`)
		check(t, e, adopted, target, RefreshNoArrFile)
	})
	t.Run("target missing on the mount", func(t *testing.T) {
		e, adopted, target := setup(t)
		// The arr already links the new release, which the mount does not show (yet).
		e.arrUpgrade(1, "", 2, "radarr", 10, 1, filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie.Remux.mkv"),
			"Amelie.2001.2160p.Remux-BYNDR/movie.mkv", "Remux-2160p")
		check(t, e, adopted, target, RefreshTargetMissing)
	})
	t.Run("closed gate", func(t *testing.T) {
		e, adopted, target := setup(t)
		e.release("Amelie.2001.2160p.Remux-BYNDR", map[string]float64{"movie.mkv": 7000})
		e.arrUpgrade(1, "", 2, "radarr", 10, 1, filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie.Remux.mkv"),
			"Amelie.2001.2160p.Remux-BYNDR/movie.mkv", "Remux-2160p")
		e.pol.Library.Enabled = false
		e.lib = New(Options{Store: e.st, Policy: e.pol, Queue: e.queue, Now: func() time.Time { return e.now }})
		if _, err := e.lib.Refresh(e.ctx, RefreshOptions{Version: "movie-theatarr"}); !errors.Is(err, ErrRefused) {
			t.Fatalf("closed gate: %v", err)
		}
		// The automatic refresh skips the version.
		if err := e.lib.RefreshFollowers(e.ctx, "movie-remux", []int64{1}); err != nil {
			t.Fatal(err)
		}
		if tg, err := os.Readlink(adopted); err != nil || tg != target {
			t.Fatalf("link -> %q %v", tg, err)
		}
		if rep, err := e.lib.Refresh(e.ctx, RefreshOptions{Version: "movie-theatarr", DryRun: true}); err != nil || rep.Swapped != 1 {
			t.Fatalf("dry run with the gate closed %+v %v", rep, err)
		}
	})
}

// TestRefreshNeverTouchesOwnLinks: a link Theatarr's own import made (with a download) is never refreshed, even when the
// arr's file differs; and a version that grabs (router.enabled, mode: active) refuses the refresh and is skipped by
// the automatic one.
func TestRefreshNeverTouchesOwnLinks(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	old := "Amelie.2001.1080p.WEB-DL-GRP"
	e.release(old, map[string]float64{"movie.mkv": 7000})
	e.arrFile(1, "radarr", 10, 1, filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie.mkv"), old+"/movie.mkv", "WEBDL-1080p")
	if rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr"}); err != nil || rep.Linked != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	// Theatarr's own import upgrades the parallel version (moving the adopted link aside).
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-theatarr", 1, 11, movieRelease, movieRelease, "[]")
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	own := e.one(`SELECT path FROM link WHERE state = 'active' AND download_id = 1`)
	ownTarget, _ := os.Readlink(own)
	// The arr upgrades too, to something else.
	e.release("Amelie.2001.2160p.Remux-BYNDR", map[string]float64{"movie.mkv": 7000})
	e.arrUpgrade(1, "", 2, "radarr", 10, 1, filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie.Remux.mkv"),
		"Amelie.2001.2160p.Remux-BYNDR/movie.mkv", "Remux-2160p")
	before := strings.Join(tree(t, e.dir), "|")
	rep, err := e.lib.Refresh(e.ctx, RefreshOptions{Version: "movie-theatarr"})
	if err != nil || rep.Links != 0 || rep.Swapped != 0 {
		t.Fatalf("refresh %+v %v", rep, err)
	}
	if tg, _ := os.Readlink(own); tg != ownTarget {
		t.Fatalf("own link changed: %s", tg)
	}

	// An adopted link in a grabbing version is left too.
	e2 := newParallelEnv(t, true)
	e2.release(old, map[string]float64{"movie.mkv": 7000})
	e2.arrFile(1, "radarr", 10, 1, filepath.Join(e2.movies, "Amelie The Movie (2001)", "Amelie.mkv"), old+"/movie.mkv", "WEBDL-1080p")
	if rep, err := e2.lib.Adopt(e2.ctx, AdoptOptions{Version: "movie-theatarr"}); err != nil || rep.Linked != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	e2.release("Amelie.2001.2160p.Remux-BYNDR", map[string]float64{"movie.mkv": 7000})
	e2.arrUpgrade(1, "", 2, "radarr", 10, 1, filepath.Join(e2.movies, "Amelie The Movie (2001)", "Amelie.Remux.mkv"),
		"Amelie.2001.2160p.Remux-BYNDR/movie.mkv", "Remux-2160p")
	e2.pol.Router = &policy.Router{Enabled: true}
	// router.enabled alone does not make a follower grab; its grab switch does.
	if FollowerGrabs(e2.pol, "movie-theatarr") {
		t.Fatal("a follower without grab counted as grabbing")
	}
	e2.pol.Versions["movie-theatarr"].Grab = true
	if !FollowerGrabs(e2.pol, "movie-theatarr") || FollowerGrabs(e2.pol, "movie-remux") {
		t.Fatal("FollowerGrabs")
	}
	before2 := strings.Join(tree(t, e2.dir), "|")
	if _, err := e2.lib.Refresh(e2.ctx, RefreshOptions{Version: "movie-theatarr", DryRun: true}); !errors.Is(err, ErrGrabbing) {
		t.Fatalf("grabbing version: %v", err)
	}
	if err := e2.lib.RefreshFollowers(e2.ctx, "movie-remux", []int64{1}); err != nil {
		t.Fatal(err)
	}
	if after := strings.Join(tree(t, e2.dir), "|"); after != before2 {
		t.Fatal("a grabbing version's link was refreshed")
	}
	if after := strings.Join(tree(t, e.dir), "|"); after != before {
		t.Fatal("refresh changed the tree")
	}
}

// TestRefreshSeriesPerEpisode: each episode link follows its own arr file. Of two single-episode files only the
// upgraded one moves; a multi-episode file upgraded to a multi-episode file moves; a multi-episode file the arr
// replaced with separate files is left and reported.
func TestRefreshSeriesPerEpisode(t *testing.T) {
	t.Parallel()
	rel := "Show.S01.1080p.WEB-DL-GRP"
	up := "Show.S01.2160p.Remux-BYNDR"
	season := func(e *parallelEnv) string { return filepath.Join(e.shows, "Show (2020)", "Season 01") }
	theatarrSeason := func(e *parallelEnv) string { return filepath.Join(e.theatarrShows, "Show (2020)", "Season 01") }

	t.Run("single episodes", func(t *testing.T) {
		e := newParallelEnv(t, true)
		e.withScans()
		e.release(rel, map[string]float64{"Show.S01E01.mkv": 2700, "Show.S01E02.mkv": 2700})
		e.arrFile(1, "sonarr", 20, 2, filepath.Join(season(e), "Show - S01E01.mkv"), rel+"/Show.S01E01.mkv", "WEBDL-1080p", 201)
		e.arrFile(2, "sonarr", 20, 2, filepath.Join(season(e), "Show - S01E02.mkv"), rel+"/Show.S01E02.mkv", "WEBDL-1080p", 202)
		if rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "tv-theatarr"}); err != nil || rep.Linked != 2 {
			t.Fatalf("adopt %+v %v", rep, err)
		}
		e1 := filepath.Join(theatarrSeason(e), "Show (2020) - S01E01 - Pilot [WEBDL-1080p]-GRP.mkv")
		e1Target, err := os.Readlink(e1)
		if err != nil {
			t.Fatal(err)
		}
		e.exec(`DELETE FROM job`)
		e.release(up, map[string]float64{"Show.S01E02.mkv": 2700})
		e.arrUpgrade(2, "", 3, "sonarr", 20, 2, filepath.Join(season(e), "Show - S01E02 Remux.mkv"), up+"/Show.S01E02.mkv",
			"Bluray-2160p Remux", 202)
		rep, err := e.lib.Refresh(e.ctx, RefreshOptions{Version: "tv-theatarr", Titles: []int64{2}})
		if err != nil || rep.Links != 2 || rep.Current != 1 || rep.Swapped != 1 || len(rep.Skipped) != 0 {
			t.Fatalf("refresh %+v %v", rep, err)
		}
		e2 := filepath.Join(theatarrSeason(e), "Show (2020) - S01E02 - Second Part [Bluray-2160p Remux]-GRP.mkv")
		if tg, err := os.Readlink(e2); err != nil || tg != filepath.Join(e.mount, "__all__", up, "Show.S01E02.mkv") {
			t.Fatalf("E02 -> %q %v", tg, err)
		}
		if tg, err := os.Readlink(e1); err != nil || tg != e1Target {
			t.Fatalf("E01 changed: %q %v", tg, err)
		}
		if got := e.one(`SELECT GROUP_CONCAT(le.episode_id || '=' || l.state, ' ') FROM (SELECT * FROM link ORDER BY id) l
			JOIN link_episode le ON le.link_id = l.id`); got != "201=active 202=unlinked 202=active" {
			t.Errorf("links %s", got)
		}
		if got := e.one(`SELECT GROUP_CONCAT(episode_id || '=' || state, ' ') FROM (SELECT * FROM episode_version_state
			WHERE version_id = 21 ORDER BY episode_id)`); got != "201=filled 202=cutoff_met" {
			t.Errorf("episode states %s", got)
		}
		if got := e.one(`SELECT COUNT(*) FROM job WHERE kind = 'plex.scan' AND json_extract(payload, '$.section') = 24`); got != "1" {
			t.Errorf("plex scans %s", got)
		}
		// A title the import did not change is not looked at.
		if rep, err := e.lib.Refresh(e.ctx, RefreshOptions{Version: "tv-theatarr", Titles: []int64{1}}); err != nil || rep.Links != 0 {
			t.Fatalf("other titles %+v %v", rep, err)
		}
		if rep, err := e.lib.Refresh(e.ctx, RefreshOptions{Version: "tv-theatarr", Titles: []int64{}}); err != nil || rep.Links != 0 {
			t.Fatalf("no titles %+v %v", rep, err)
		}
		if rep, err := e.lib.Refresh(e.ctx, RefreshOptions{Version: "tv-theatarr"}); err != nil || rep.Current != 2 || rep.Swapped != 0 {
			t.Fatalf("second run %+v %v", rep, err)
		}
	})

	for _, split := range []bool{false, true} {
		name := map[bool]string{false: "multi-episode upgraded", true: "multi-episode split"}[split]
		t.Run(name, func(t *testing.T) {
			e := newParallelEnv(t, true)
			e.release(rel, map[string]float64{"Show.S01E01E02.mkv": 5400})
			e.arrFile(1, "sonarr", 20, 2, filepath.Join(season(e), "Show - S01E01-E02.mkv"), rel+"/Show.S01E01E02.mkv",
				"WEBDL-1080p", 201, 202)
			if rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "tv-theatarr"}); err != nil || rep.Linked != 1 {
				t.Fatalf("adopt %+v %v", rep, err)
			}
			multi := filepath.Join(theatarrSeason(e), "Show (2020) - S01E01-E02 - Pilot and Second Part [WEBDL-1080p]-GRP.mkv")
			target, _ := os.Readlink(multi)
			if !split {
				e.release(up, map[string]float64{"Show.S01E01E02.mkv": 5400})
				e.arrUpgrade(1, rel, 2, "sonarr", 20, 2, filepath.Join(season(e), "Show - S01E01-E02 Remux.mkv"),
					up+"/Show.S01E01E02.mkv", "Bluray-2160p Remux", 201, 202)
				rep, err := e.lib.Refresh(e.ctx, RefreshOptions{Version: "tv-theatarr"})
				if err != nil || rep.Swapped != 1 {
					t.Fatalf("refresh %+v %v", rep, err)
				}
				want := filepath.Join(theatarrSeason(e), "Show (2020) - S01E01-E02 - Pilot and Second Part [Bluray-2160p Remux]-GRP.mkv")
				if tg, err := os.Readlink(want); err != nil || tg != filepath.Join(e.mount, "__all__", up, "Show.S01E01E02.mkv") {
					t.Fatalf("multi -> %q %v", tg, err)
				}
				if got := e.one(`SELECT COUNT(*) FROM link_episode le JOIN link l ON l.id = le.link_id WHERE l.state = 'active'
					AND l.path = ?`, want); got != "2" {
					t.Errorf("episodes on the new link %s", got)
				}
				return
			}
			e.release(up, map[string]float64{"Show.S01E01.mkv": 2700, "Show.S01E02.mkv": 2700})
			e.arrUpgrade(1, "", 2, "sonarr", 20, 2, filepath.Join(season(e), "Show - S01E01.mkv"), up+"/Show.S01E01.mkv",
				"Bluray-2160p Remux", 201)
			e.arrFile(3, "sonarr", 20, 2, filepath.Join(season(e), "Show - S01E02.mkv"), up+"/Show.S01E02.mkv",
				"Bluray-2160p Remux", 202)
			before := strings.Join(tree(t, e.dir), "|")
			rep, err := e.lib.Refresh(e.ctx, RefreshOptions{Version: "tv-theatarr"})
			if err != nil || rep.Swapped != 0 || rep.Skipped[RefreshSplit] != 1 {
				t.Fatalf("refresh %+v %v", rep, err)
			}
			if after := strings.Join(tree(t, e.dir), "|"); after != before {
				t.Fatal("a split pack changed the tree")
			}
			if tg, err := os.Readlink(multi); err != nil || tg != target {
				t.Fatalf("multi -> %q %v", tg, err)
			}
		})
	}
}

// TestRefreshFollowersAutomatic: what arr.import calls. Only the given titles of the followers that do not grab are
// refreshed; running it again changes nothing.
func TestRefreshFollowersAutomatic(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	old := "Amelie.2001.1080p.WEB-DL-POKE"
	e.release(old, map[string]float64{"movie.mkv": 7000})
	e.arrFile(1, "radarr", 10, 1, filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie.WEB.mkv"), old+"/movie.mkv", "WEBDL-1080p")
	if rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr"}); err != nil || rep.Linked != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	e.release("Amelie.2001.2160p.Remux-BYNDR", map[string]float64{"movie.mkv": 7000})
	e.arrUpgrade(1, old, 2, "radarr", 10, 1, filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie.Remux.mkv"),
		"Amelie.2001.2160p.Remux-BYNDR/movie.mkv", "Remux-2160p")
	// Another title changed: the movie's link stays.
	if err := e.lib.RefreshFollowers(e.ctx, "movie-remux", []int64{2}); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT COUNT(*) FROM link WHERE state = 'active' AND quality = 'WEBDL-1080p'`); got != "1" {
		t.Fatalf("a title outside the list was refreshed: %s", got)
	}
	for range 2 {
		if err := e.lib.RefreshFollowers(e.ctx, "movie-remux", []int64{1}); err != nil {
			t.Fatal(err)
		}
		if got := e.one(`SELECT GROUP_CONCAT(state || ' ' || quality, ', ') FROM (SELECT * FROM link ORDER BY id)`); got !=
			"unlinked WEBDL-1080p, active Remux-2160p" {
			t.Fatalf("links %s", got)
		}
	}
	if got := e.one(`SELECT COUNT(*) FROM link_trash`); got != "1" {
		t.Fatalf("trash rows %s", got)
	}
}

// TestRefreshSamePath: the arr's new file names the same under Theatarr's naming (same quality and group, another
// release), so the link is replaced in place: the old link is copied to the trash first and the row keeps its id.
func TestRefreshSamePath(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	old := "Amelie.2001.1080p.WEB-DL-GRP"
	e.release(old, map[string]float64{"movie.mkv": 7000})
	e.arrFile(1, "radarr", 10, 1, filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie.mkv"), old+"/movie.mkv", "WEBDL-1080p")
	if rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr"}); err != nil || rep.Linked != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	adopted := filepath.Join(e.theatarrMovies, "Amelie The Movie (2001)", "Amelie The Movie (2001) {imdb-tt0211915} - [WEBDL-1080p]-GRP.mkv")
	other := "Amelie.2001.1080p.AMZN.WEB-DL-GRP"
	e.release(other, map[string]float64{"movie.mkv": 7000})
	e.arrUpgrade(1, old, 2, "radarr", 10, 1, filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie.AMZN.mkv"),
		other+"/movie.mkv", "WEBDL-1080p")
	rep, err := e.lib.Refresh(e.ctx, RefreshOptions{Version: "movie-theatarr"})
	if err != nil || rep.Swapped != 1 || rep.Swaps[0].Path != adopted || rep.Swaps[0].Old != adopted {
		t.Fatalf("refresh %+v %v", rep, err)
	}
	if tg, err := os.Readlink(adopted); err != nil || tg != filepath.Join(e.mount, "__all__", other, "movie.mkv") {
		t.Fatalf("link -> %q %v", tg, err)
	}
	if tg, err := os.Readlink(rep.Swaps[0].TrashPath); err != nil || tg != filepath.Join(e.mount, "__all__", old, "movie.mkv") {
		t.Fatalf("trash -> %q %v", tg, err)
	}
	if got := e.one(`SELECT GROUP_CONCAT(id || ' ' || state || ' ' || target, ', ') FROM link`); got !=
		"1 active "+filepath.Join(e.mount, "__all__", other, "movie.mkv") {
		t.Fatalf("link rows %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM link_trash WHERE link_id = 1 AND path = ?`, adopted); got != "1" {
		t.Fatalf("trash rows %s", got)
	}
	if rep, err := e.lib.Refresh(e.ctx, RefreshOptions{Version: "movie-theatarr"}); err != nil || rep.Current != 1 || rep.Swapped != 0 {
		t.Fatalf("second run %+v %v", rep, err)
	}
}
