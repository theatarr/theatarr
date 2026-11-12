package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
)

// parallelEnv is newEnv with the arr versions in shadow (their roots hold the arr's links) and Live libraries
// movie-theatarr and tv-theatarr beside them on roots of their own, with the same naming.
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
	movie.Mode, movie.Library = policy.ModeActive, e.theatarrMovies
	tv := *e.pol.Versions["tv"]
	tv.Mode, tv.Library = policy.ModeActive, e.theatarrShows
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

// adopt links the arr's files (arrFile) of the library beside version (movie-remux for movie-theatarr, tv for
// tv-theatarr) into version, as the one-time import does.
func (e *parallelEnv) adopt(version string) (*AdoptReport, error) {
	e.t.Helper()
	from := map[string]string{"movie-theatarr": "movie-remux", "tv-theatarr": "tv"}[version]
	rows, err := e.st.Reader().QueryContext(e.ctx, `SELECT vf.id, vf.instance, vf.title_id, vf.path, vf.relative_path,
		vf.quality, vf.quality_revision, vf.release_group, vf.scene_name, vf.languages, vf.media_info, vf.size_bytes
		FROM version_file vf JOIN version v ON v.id = vf.version_id WHERE v.policy_key = ? ORDER BY vf.id`, from)
	if err != nil {
		e.t.Fatal(err)
	}
	var files []ImportFile
	var ids []int64
	for rows.Next() {
		var f ImportFile
		var id int64
		var instance, langs string
		if err := rows.Scan(&id, &instance, &f.TitleID, &f.Path, &f.RelativePath, &f.Quality, &f.Revision,
			&f.ReleaseGroup, &f.SceneName, &langs, &f.MediaInfo, &f.Size); err != nil {
			e.t.Fatal(err)
		}
		f.Source = instance + " " + f.Path
		_ = json.Unmarshal([]byte(langs), &f.Languages)
		files, ids = append(files, f), append(ids, id)
	}
	if err := rows.Close(); err != nil {
		e.t.Fatal(err)
	}
	for i, id := range ids {
		eps, err := e.st.Reader().QueryContext(e.ctx, `SELECT episode_id FROM version_file_episode
			WHERE version_file_id = ? ORDER BY episode_id`, id)
		if err != nil {
			e.t.Fatal(err)
		}
		for eps.Next() {
			var ep int64
			if err := eps.Scan(&ep); err != nil {
				e.t.Fatal(err)
			}
			files[i].Episodes = append(files[i].Episodes, ep)
		}
		eps.Close()
	}
	return e.lib.AdoptImported(e.ctx, version, files)
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
			{
				rep, err := e.adopt("tv-theatarr")
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
