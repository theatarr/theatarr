package library

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
)

// parallelEnv is newEnv with the arr versions in shadow (their roots hold the arr's links), parallel versions
// movie-theatarr and tv-theatarr following them on roots of their own.
type parallelEnv struct {
	*env
	theatarrMovies, theatarrShows string
}

func newParallelEnv(t *testing.T, enabled bool) *parallelEnv {
	t.Helper()
	e := &parallelEnv{env: newEnv(t, enabled, policy.ModeShadow)}
	e.theatarrMovies = filepath.Join(e.dir, "media", "theatarr", "movies")
	e.theatarrShows = filepath.Join(e.dir, "media", "theatarr", "tv")
	for _, d := range []string{e.theatarrMovies, e.theatarrShows} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	movie := *e.pol.Versions["movie-remux"]
	movie.Mode, movie.Follows, movie.Library = policy.ModeActive, "movie-remux", e.theatarrMovies
	movie.Naming = policy.Naming{FromVersion: "movie-remux"}
	tv := *e.pol.Versions["tv"]
	tv.Mode, tv.Follows, tv.Library = policy.ModeActive, "tv", e.theatarrShows
	tv.Naming = policy.Naming{FromVersion: "tv"}
	e.pol.Versions["movie-theatarr"], e.pol.Versions["tv-theatarr"] = &movie, &tv
	e.pol.Library.Versions["movie-theatarr"] = &policy.LibraryVersion{Roots: []string{e.theatarrMovies}}
	e.pol.Library.Versions["tv-theatarr"] = &policy.LibraryVersion{Roots: []string{e.theatarrShows}}
	engine, err := decide.New(e.pol, decide.Options{Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	e.lib = New(Options{Store: e.st, Policy: e.pol, Engine: engine, Queue: e.queue, Now: func() time.Time { return e.now }})
	e.exec(`INSERT INTO version_source (version_id, instance, arr_id) VALUES (10, 'radarr', 1), (20, 'sonarr', 2)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES
		(11, 1, 'movie-theatarr', 'wanted', 0, 0), (21, 2, 'tv-theatarr', 'wanted', 0, 0)`)
	e.exec(`INSERT INTO arr_instance (name, flavour, policy_key, created_at, updated_at) VALUES
		('radarr', 'radarr', 'movie-remux', 0, 0), ('sonarr', 'sonarr', 'tv', 0, 0)`)
	e.exec(`INSERT INTO arr_title (instance, arr_id, kind, title_id, version_id, monitored, path) VALUES
		('radarr', 1, 'movie', 1, 10, 1, ?), ('sonarr', 2, 'series', 2, 20, 1, ?)`,
		filepath.Join(e.movies, "Amelie The Movie (2001)"), filepath.Join(e.shows, "Show (2020)"))
	return e
}

// arrFile makes the arr's link at p to target (relative targets are under the zurg release root) and its
// version_file row.
func (e *parallelEnv) arrFile(id int64, instance string, versionID, titleID int64, p, target, quality string, episodes ...int64) {
	e.t.Helper()
	if target != "" {
		if !filepath.IsAbs(target) {
			target = filepath.Join(e.mount, "__all__", target)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			e.t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			e.t.Fatal(err)
		}
	}
	e.exec(`INSERT INTO version_file (id, instance, arr_file_id, version_id, title_id, path, relative_path, size_bytes,
		quality, quality_revision, release_group, scene_name, languages, media_info, custom_format_score)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1000, ?, 1, 'GRP', ?, '["English"]', '{}', 50)`, id, instance, id, versionID,
		titleID, p, filepath.Base(p), quality, strings.TrimSuffix(filepath.Base(target), filepath.Ext(target)))
	for _, ep := range episodes {
		e.exec(`INSERT INTO version_file_episode (version_file_id, episode_id) VALUES (?, ?)`, id, ep)
	}
}

func TestAdoptMovies(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	arrLink := filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie.mkv")
	e.arrFile(1, "radarr", 10, 1, arrLink, movieRelease+"/movie.mkv", "Remux-2160p")
	// Other titles of the arr: a dangling link, a regular file, a link off the mount, a WEB file.
	for id, name := range map[int64]string{3: "Gone", 4: "Plain", 5: "Off", 6: "Alt"} {
		e.exec(`INSERT INTO title (id, kind, name, year, tmdb_id, created_at, updated_at) VALUES (?, 'movie', ?, 2010, ?, 0, 0)`,
			id, name, id)
		e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (?, ?, 'movie-remux', 0, 0)`, id*10, id)
		e.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (?, ?, 'movie-theatarr', 'wanted', 0, 0)`, id*10+1, id)
		e.exec(`INSERT INTO version_source (version_id, instance, arr_id) VALUES (?, 'radarr', ?)`, id*10, id)
	}
	e.arrFile(3, "radarr", 30, 3, filepath.Join(e.movies, "Gone (2010)", "Gone.mkv"), "Missing.Release/m.mkv", "Remux-2160p")
	plain := filepath.Join(e.movies, "Plain (2010)", "Plain.mkv")
	os.MkdirAll(filepath.Dir(plain), 0o755)
	os.WriteFile(plain, []byte("x"), 0o644)
	e.arrFile(4, "radarr", 40, 4, plain, "", "Remux-2160p")
	e.arrFile(5, "radarr", 50, 5, filepath.Join(e.movies, "Off (2010)", "Off.mkv"), filepath.Join(e.outside, "o.mkv"), "Remux-2160p")
	e.release("Alt.2010.1080p.WEB-DL-GRP", map[string]float64{"alt.mkv": 7000})
	alt := filepath.Join(e.mount, "__all__", "Alt.2010.1080p.WEB-DL-GRP", "alt.mkv")
	e.arrFile(6, "radarr", 60, 6, filepath.Join(e.movies, "Alt (2010)", "Alt.mkv"), alt, "WEBDL-1080p")

	// A dry run plans and writes nothing.
	before := tree(t, e.dir)
	rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Linked != 2 || rep.Files != 5 {
		t.Fatalf("dry run %+v", rep)
	}
	if after := tree(t, e.dir); strings.Join(before, "|") != strings.Join(after, "|") {
		t.Fatal("dry run wrote to disk")
	}
	if n := e.one(`SELECT COUNT(*) FROM link`); n != "0" {
		t.Fatalf("dry run recorded %s links", n)
	}

	rep, err = e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Linked != 2 || rep.Skipped[AdoptDangling] != 1 || rep.Skipped[AdoptNotLink] != 1 ||
		rep.Skipped[AdoptOffMount] != 1 {
		t.Fatalf("report %+v", rep)
	}
	want := filepath.Join(e.theatarrMovies, "Amelie The Movie (2001)",
		"Amelie The Movie (2001) {imdb-tt0211915} - [Remux-2160p]-GRP.mkv")
	if tg, err := os.Readlink(want); err != nil || tg != filepath.Join(e.mount, "__all__", movieRelease, "movie.mkv") {
		t.Fatalf("link %s -> %q %v", want, tg, err)
	}
	if tg, err := os.Readlink(filepath.Join(e.theatarrMovies, "Alt (2010)", "Alt (2010) - [WEBDL-1080p]-GRP.mkv")); err != nil || tg != alt {
		t.Fatalf("WEB link -> %q %v", tg, err)
	}
	if tg, _ := os.Readlink(arrLink); tg != filepath.Join(e.mount, "__all__", movieRelease, "movie.mkv") {
		t.Fatal("the arr's link changed")
	}
	if got := e.one(`SELECT l.quality || ' ' || l.score || ' ' || l.adopted || ' ' || COALESCE(l.download_id, 0)
		FROM link l JOIN link_version lv ON lv.link_id = l.id WHERE lv.version_id = 11`); got != "Remux-2160p 50 0 0" {
		t.Errorf("link row %s", got)
	}
	if got := e.one(`SELECT json_extract(file_metadata, '$.SceneName') || ' ' || json_extract(file_metadata, '$.Revision')
		FROM link WHERE path = ?`, want); got != "movie 1" {
		t.Errorf("file metadata %s", got)
	}
	// decide sees the link as the parallel version's file; the followed version still has only the arr's.
	if f, err := decide.LoadExisting(e.ctx, e.st.Reader(), 11); err != nil || f == nil || f.Quality != "Remux-2160p" ||
		f.ReleaseGroup != "GRP" {
		t.Fatalf("existing %+v %v", f, err)
	}
	if got := e.one(`SELECT state FROM version WHERE id = 11`) + " " + e.one(`SELECT state FROM version WHERE id = 61`); got != "cutoff_met filled" {
		t.Errorf("states %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM library_action WHERE kind = 'link' AND mode = 'applied' AND version_key = 'movie-theatarr'
		AND detail LIKE 'adopted from radarr %'`); got != "2" {
		t.Errorf("journal %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM link l JOIN link_version lv ON lv.link_id = l.id WHERE lv.version_id = 10`); got != "0" {
		t.Errorf("the followed version got links: %s", got)
	}

	// Idempotent.
	before = tree(t, e.dir)
	rep, err = e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr"})
	if err != nil || rep.Linked != 0 || rep.Existing != 2 {
		t.Fatalf("second run %+v %v", rep, err)
	}
	if after := tree(t, e.dir); strings.Join(before, "|") != strings.Join(after, "|") {
		t.Fatal("second run changed the tree")
	}
}

func TestAdoptRefusals(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, false)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.arrFile(1, "radarr", 10, 1, filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie.mkv"),
		movieRelease+"/movie.mkv", "Remux-2160p")
	if _, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr"}); !errors.Is(err, ErrRefused) {
		t.Fatalf("closed gate: %v", err)
	}
	if _, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-remux", DryRun: true}); err == nil {
		t.Fatal("adopt into a version that follows nothing")
	}
	// The path is taken by something else: nothing is overwritten.
	e2 := newParallelEnv(t, true)
	e2.release(movieRelease, map[string]float64{"movie.mkv": 7000, "other.mkv": 7000})
	e2.arrFile(1, "radarr", 10, 1, filepath.Join(e2.movies, "Amelie The Movie (2001)", "Amelie.mkv"),
		movieRelease+"/movie.mkv", "Remux-2160p")
	taken := filepath.Join(e2.theatarrMovies, "Amelie The Movie (2001)", "Amelie The Movie (2001) {imdb-tt0211915} - [Remux-2160p]-GRP.mkv")
	os.MkdirAll(filepath.Dir(taken), 0o755)
	os.Symlink(filepath.Join(e2.mount, "__all__", movieRelease, "other.mkv"), taken)
	rep, err := e2.lib.Adopt(e2.ctx, AdoptOptions{Version: "movie-theatarr"})
	if err != nil || rep.Linked != 0 || rep.Skipped[AdoptConflict] != 1 {
		t.Fatalf("taken path %+v %v", rep, err)
	}
	if tg, _ := os.Readlink(taken); !strings.HasSuffix(tg, "other.mkv") {
		t.Fatal("overwritten")
	}
	// The limit stops after N new links.
	e3 := newParallelEnv(t, true)
	e3.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e3.arrFile(1, "radarr", 10, 1, filepath.Join(e3.movies, "Amelie The Movie (2001)", "Amelie.mkv"),
		movieRelease+"/movie.mkv", "Remux-2160p")
	if rep, err := e3.lib.Adopt(e3.ctx, AdoptOptions{Version: "movie-theatarr", Limit: 1}); err != nil || rep.Linked != 1 {
		t.Fatalf("limit %+v %v", rep, err)
	}
}

func TestAdoptSeries(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	e.exec(`UPDATE episode SET air_date_utc_at = 0`)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES (200, 2, 0, 1, 'Special', 0, 0)`)
	for _, id := range []int64{200, 201, 202} {
		e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at) VALUES (21, ?, 1, 'wanted', 0)`, id)
	}
	rel := "Show.S01.1080p.WEB-DL-GRP"
	e.release(rel, map[string]float64{"Show.S01E01E02.mkv": 5400, "Show.S00E01.mkv": 2700})
	e.arrFile(1, "sonarr", 20, 2, filepath.Join(e.shows, "Show (2020)", "Season 01", "Show - S01E01-E02.mkv"),
		rel+"/Show.S01E01E02.mkv", "WEBDL-1080p", 201, 202)
	e.arrFile(2, "sonarr", 20, 2, filepath.Join(e.shows, "Show (2020)", "Specials", "Show - S00E01.mkv"),
		rel+"/Show.S00E01.mkv", "Bluray-2160p Remux", 200)
	rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "tv-theatarr"})
	if err != nil || rep.Linked != 2 {
		t.Fatalf("%+v %v", rep, err)
	}
	multi := filepath.Join(e.theatarrShows, "Show (2020)", "Season 01",
		"Show (2020) - S01E01-E02 - Pilot and Second Part [WEBDL-1080p]-GRP.mkv")
	if _, err := os.Readlink(multi); err != nil {
		t.Fatalf("multi-episode link: %v (%+v)", err, rep.Links)
	}
	if got := e.one(`SELECT GROUP_CONCAT(episode_id || '=' || state, ' ') FROM (SELECT * FROM episode_version_state
		WHERE version_id = 21 ORDER BY episode_id)`); got != "200=cutoff_met 201=filled 202=filled" {
		t.Errorf("episode states %s", got)
	}
	if got := e.one(`SELECT state FROM version WHERE id = 21`); got != "filled" {
		t.Errorf("series state %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM link_episode le JOIN link_version lv ON lv.link_id = le.link_id WHERE lv.version_id = 21`); got != "3" {
		t.Errorf("link episodes %s", got)
	}
}

func TestAdoptPreservesOwnFilesAndReportsCoverage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name             string
		full, dangling   bool
		missing, partial int
	}{
		{"partial pack", false, false, 1, 1},
		{"full across links", true, false, 0, 0},
		{"dangling own file", false, true, 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newParallelEnv(t, true)
			rel := "Show.S01.1080p.WEB-DL-GRP"
			e.release(rel, map[string]float64{"Show.S01E01E02.mkv": 5400, "own.mkv": 2700})
			arrPath := filepath.Join(e.shows, "Show (2020)", "Season 01", "Show.S01E01E02.mkv")
			e.arrFile(1, "sonarr", 20, 2, arrPath, rel+"/Show.S01E01E02.mkv", "WEBDL-1080p", 201, 202)
			target := filepath.Join(e.mount, "__all__", rel, "own.mkv")
			if tc.dangling {
				target = filepath.Join(e.mount, "__all__", rel, "gone.mkv")
			}
			n := 1
			if tc.full {
				n = 2
			}
			var paths []string
			for i := 1; i <= n; i++ {
				p := filepath.Join(e.theatarrShows, "Show (2020)", "Season 01", "own"+string(rune('0'+i))+".mkv")
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, p); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, p)
				e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at) VALUES (?, ?, ?, 2, 'active', 0, 0)`, i, p, target)
				e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (?, 21)`, i)
				e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (?, ?)`, i, 200+i)
			}
			before := strings.Join(tree(t, e.dir), "|")
			for _, dryRun := range []bool{true, false} {
				rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "tv-theatarr", DryRun: dryRun})
				if err != nil {
					t.Fatal(err)
				}
				if rep.Linked != 0 || rep.HasFile != 1 || rep.MissingEpisodes != tc.missing || rep.PartialPacks != tc.partial {
					t.Fatalf("report: %+v", rep)
				}
				if tc.missing > 0 {
					if len(rep.Problems) != 1 || rep.Problems[0].Reason != AdoptHasFile ||
						len(rep.Problems[0].MissingEpisodes) != tc.missing || rep.Problems[0].MissingEpisodes[tc.missing-1] != 202 {
						t.Fatalf("missing episodes: %+v", rep.Problems)
					}
					data, err := json.Marshal(rep)
					if err != nil || !strings.Contains(string(data), `"missing_episodes":`) ||
						!strings.Contains(string(data), "202") {
						t.Fatalf("JSON %s: %v", data, err)
					}
				} else if len(rep.Problems) != 0 {
					t.Fatalf("fully covered: %+v", rep.Problems)
				}
			}
			if after := strings.Join(tree(t, e.dir), "|"); before != after {
				t.Fatal("adopt changed files while preserving own links")
			}
			for _, p := range paths {
				if got, err := os.Readlink(p); err != nil || got != target {
					t.Fatalf("own link changed: %s %v", got, err)
				}
			}
			if got := e.one(`SELECT COUNT(*) FROM link_version WHERE version_id = 21`); got != string(rune('0'+n)) {
				t.Fatalf("own links changed: %s", got)
			}
		})
	}
}

func TestAdoptAnimeAbsolute(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	tv := e.pol.Versions["tv"]
	tv.Naming.Anime = "{Series CleanTitleWithoutYear} - S{season:00}E{episode:00} - {absolute:000} - {Episode CleanTitle} {[Quality Full]}"
	e.exec(`UPDATE title SET series_type = 'anime' WHERE id = 2`)
	e.exec(`INSERT INTO episode_number (episode_id, title_id, scheme, number, source) VALUES (202, 2, 'absolute_tvdb', 14, 'sonarr')`)
	rel := "[Group] Show - 14 [1080p]"
	e.release(rel, map[string]float64{"Show - 14.mkv": 1400})
	e.arrFile(1, "sonarr", 20, 2, filepath.Join(e.shows, "Show (2020)", "Season 01", "Show - 14.mkv"),
		rel+"/Show - 14.mkv", "WEBDL-1080p", 202)
	rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "tv-theatarr"})
	if err != nil || rep.Linked != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	want := filepath.Join(e.theatarrShows, "Show (2020)", "Season 01", "Show - S01E02 - 014 - Second Part [WEBDL-1080p].mkv")
	if rep.Links[0].Path != want {
		t.Fatalf("path %s, want %s", rep.Links[0].Path, want)
	}
}

// TestFollowerUpgradeMovesOnlyItsLink: an import for the parallel version replaces its own adopted link (moved to
// the trash) and never touches the arr's link or the followed version's file.
func TestFollowerUpgradeMovesOnlyItsLink(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	old := "Amelie.2001.1080p.WEB-DL-GRP"
	e.release(old, map[string]float64{"movie.mkv": 7000})
	arrLink := filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie.mkv")
	e.arrFile(1, "radarr", 10, 1, arrLink, old+"/movie.mkv", "WEBDL-1080p")
	if rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr"}); err != nil || rep.Linked != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	adopted := filepath.Join(e.theatarrMovies, "Amelie The Movie (2001)", "Amelie The Movie (2001) {imdb-tt0211915} - [WEBDL-1080p]-GRP.mkv")
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-theatarr", 1, 11, movieRelease, movieRelease, "[]")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 1 || len(plan.Links[0].Replaces) != 1 || plan.Links[0].Replaces[0] != adopted {
		t.Fatalf("plan %+v", plan.Links)
	}
	if _, err := os.Lstat(adopted); !os.IsNotExist(err) {
		t.Fatal("the adopted link is still there")
	}
	if tg, err := os.Readlink(arrLink); err != nil || !strings.Contains(tg, old) {
		t.Fatal("the arr's link was touched")
	}
	if got := e.one(`SELECT COUNT(*) FROM link_trash WHERE version_key = 'movie-theatarr' AND path = ?`, adopted); got != "1" {
		t.Errorf("trash rows %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM version_file WHERE version_id = 10`); got != "1" {
		t.Errorf("the arr's file row went: %s", got)
	}
	if got := e.one(`SELECT state FROM version WHERE id = 11`); got != "cutoff_met" {
		t.Errorf("state %s", got)
	}
}

// TestShadowFollowedNeverWritten: the followed version is shadow, so an import for it stays a dry run even while its
// parallel version may write.
func TestShadowFollowedNeverWritten(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	before := tree(t, e.dir)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.DryRun {
		t.Fatalf("plan %+v", plan)
	}
	if after := tree(t, e.dir); strings.Join(before, "|") != strings.Join(after, "|") {
		t.Fatal("the shadow followed version wrote")
	}
	if !e.lib.Gate().Active("movie-theatarr") || e.lib.Gate().Active("movie-remux") {
		t.Fatal("gate")
	}
}
