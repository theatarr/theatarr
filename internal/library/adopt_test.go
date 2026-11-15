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
	arr                           []arrRec // the arr's files (arrFile), in order
}

// arrRec is one arr file the test's import hands to AdoptImported.
type arrRec struct {
	versionID int64
	file      ImportFile
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
	e.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES
		(11, 1, 'movie-theatarr', 'wanted', 0, 0), (21, 2, 'tv-theatarr', 'wanted', 0, 0)`)
	return e
}

// arrFile makes the arr's link at p to target (relative targets are under the zurg release root) and records the
// file the import reads from the arr.
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
	e.arr = append(e.arr, arrRec{versionID: versionID, file: ImportFile{TitleID: titleID, Path: p,
		RelativePath: filepath.Base(p), Quality: quality, Revision: 1, ReleaseGroup: "GRP",
		SceneName: strings.TrimSuffix(filepath.Base(target), filepath.Ext(target)), Languages: []string{"English"},
		MediaInfo: "{}", Size: 1000, Source: instance + " " + p, Episodes: episodes}})
}

// adopt links the arr's files (arrFile) of the library beside version (movie-remux for movie-theatarr, tv for
// tv-theatarr) into version, as the one-time import does.
func (e *parallelEnv) adopt(version string) (*AdoptReport, error) {
	e.t.Helper()
	from := map[string]int64{"movie-theatarr": 10, "tv-theatarr": 20}[version]
	var files []ImportFile
	for _, a := range e.arr {
		if a.versionID == from {
			files = append(files, a.file)
		}
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
