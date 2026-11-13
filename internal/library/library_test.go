package library

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/library/probe"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

type env struct {
	t                                *testing.T
	ctx                              context.Context
	st                               *store.Store
	pol                              *policy.Policy
	dir, mount, movies, shows, trash string
	outside                          string
	now                              time.Time
	probes                           map[string]*probe.Result
	lib                              *Library
	queue                            *jobs.Queue
}

const hashA = "abcdef0123456789abcdef0123456789abcdef01"

func newEnv(t *testing.T, enabled bool, mode policy.Mode) *env {
	t.Helper()
	e := &env{t: t, ctx: context.Background(), dir: t.TempDir(), now: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC),
		probes: map[string]*probe.Result{}}
	e.mount = filepath.Join(e.dir, "zurg")
	e.movies = filepath.Join(e.dir, "media", "movies")
	e.shows = filepath.Join(e.dir, "media", "shows")
	e.trash = filepath.Join(e.dir, "media", ".trash")
	e.outside = filepath.Join(e.dir, "elsewhere")
	for _, d := range []string{filepath.Join(e.mount, "__all__"), e.movies, e.shows, e.outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(e.ctx, storetest.Seed(t, filepath.Join(e.dir, "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.st = st
	e.pol = &policy.Policy{
		Versions: map[string]*policy.VersionPolicy{
			"movie-remux": {Mode: mode, Kind: policy.KindMovie, Profile: "movie-state", Library: e.movies, Naming: policy.Naming{
				Folder: "{Movie CleanTitle} ({Release Year})",
				File:   "{Movie CleanTitle} {(Release Year)} {imdb-{ImdbId}} - {[Quality Full]}{[Mediainfo AudioCodec}{ Mediainfo AudioChannels]}{[MediaInfo VideoDynamicRangeType]}{[Mediainfo VideoCodec]}{-Release Group}",
				Colon:  "delete"}},
			"tv": {Mode: mode, Kind: policy.KindSeries, Profile: "tv-state", Library: e.shows, Naming: policy.Naming{
				Folder: "{Series CleanTitleWithoutYear} {(Series Year)}", Season: "Season {season:00}",
				File: "{Series CleanTitleWithoutYear} {(Series Year)} - S{season:00}E{episode:00} - {Episode CleanTitle:90} {[Quality Full]}{-Release Group}"}},
		},
		Profiles: map[string]*policy.Profile{
			"movie-state": {Qualities: []policy.QualityItem{{Name: "Remux-2160p"}, {Name: "WEBDL-1080p"}}, Cutoff: "Remux-2160p"},
			"tv-state":    {Qualities: []policy.QualityItem{{Name: "Bluray-2160p Remux"}, {Name: "WEBDL-1080p"}}, Cutoff: "Bluray-2160p Remux"},
		},
		Library: &policy.Library{Enabled: enabled, Mount: policy.LibraryMount{Path: e.mount},
			Trash: policy.LibraryTrash{Dir: e.trash},
			Plex:  &policy.Plex{URL: "http://plex:32400", TokenEnv: "THEATARR_PLEX_TOKEN"},
			Versions: map[string]*policy.LibraryVersion{
				"movie-remux": {Roots: []string{e.movies}, PlexSection: 18},
				"tv":          {Roots: []string{e.shows}},
			}},
	}
	e.exec(`INSERT INTO title (id, kind, name, year, imdb_id, tmdb_id, runtime_minutes, created_at, updated_at)
		VALUES (1, 'movie', 'Amélie: The Movie', 2001, 'tt0211915', 194, 120, 0, 0)`)
	e.exec(`INSERT INTO title (id, kind, name, year, tvdb_id, series_type, runtime_minutes, created_at, updated_at)
		VALUES (2, 'series', 'Show', 2020, 5, 'standard', 45, 0, 0)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (10, 1, 'movie-remux', 0, 0)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (20, 2, 'tv', 0, 0)`)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES (201, 2, 1, 1, 'Pilot', 0, 0)`)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES (202, 2, 1, 2, 'Second: Part', 0, 0)`)
	e.queue = jobs.NewQueue(st)
	e.queue.SetClock(func() time.Time { return e.now })
	engine, err := decide.New(e.pol, decide.Options{Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	e.lib = New(Options{Store: st, Policy: e.pol, Engine: engine, Queue: e.queue, Now: func() time.Time { return e.now },
		Probe: func(p string, _ int64) (*probe.Result, error) {
			if r, ok := e.probes[p]; ok {
				return r, nil
			}
			return nil, probe.ErrUnknownFormat
		},
		Formats: func(context.Context, string, int64, string) []string { return nil }})
	t.Cleanup(e.lib.mount.closeHelpers)
	return e
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.st.Writer().ExecContext(e.ctx, q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *env) one(q string, args ...any) string {
	e.t.Helper()
	var s any
	if err := e.st.Reader().QueryRowContext(e.ctx, q, args...).Scan(&s); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return fmt.Sprint(s)
}

// release puts a release folder with files on the mount.
func (e *env) release(folder string, files map[string]float64) {
	for name, runtime := range files {
		p := filepath.Join(e.mount, "__all__", folder, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			e.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("video"), 0o644); err != nil {
			e.t.Fatal(err)
		}
		e.probes[p] = &probe.Result{Container: "matroska", RunTimeSeconds: runtime,
			Video: &probe.Video{Codec: "hevc", Width: 3840, Height: 2160, BitDepth: 10, DoviCompat: -1,
				Primaries: "bt2020", Transfer: "smpte2084", Mastering: true},
			Audio: []probe.Audio{{Codec: "truehd", Tag: "thd+", Channels: 8, Language: "eng"}}}
	}
}

// videoSize sets the probed frame size of every file of a release folder (release probes 3840x2160).
func (e *env) videoSize(folder string, w, h int) {
	prefix := filepath.Join(e.mount, "__all__", folder) + string(filepath.Separator)
	for p, r := range e.probes {
		if strings.HasPrefix(p, prefix) && r.Video != nil {
			r.Video.Width, r.Video.Height = w, h
		}
	}
}

func (e *env) download(id int64, version string, title, versionID int64, release, name, episodes string) {
	quality := "Remux-2160p"
	if version == "tv" {
		quality = "Bluray-2160p Remux"
	}
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality, score,
		episodes, provider, name, state, created_at, updated_at, ready_at)
		VALUES (?, ?, ?, ?, ?, ?, 'idx', ?, 100, ?, 'rd', ?, 'ready', 0, 0, 0)`,
		id, title, versionID, version, hashA, release, quality, episodes, name)
}

// tree snapshots every path under dir except the database: kind, and link target.
func tree(t *testing.T, dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || strings.HasPrefix(filepath.Base(p), "theatarr.db") {
			return nil
		}
		s := p + " " + d.Type().String()
		if d.Type()&fs.ModeSymlink != 0 {
			tg, _ := os.Readlink(p)
			s += " -> " + tg
		}
		out = append(out, s)
		return nil
	})
	sort.Strings(out)
	return out
}

const movieRelease = "Amelie.2001.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-GRP"

func TestImportMovie(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000, "sample.mkv": 30})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(e.movies, "Amelie The Movie (2001)",
		"Amelie The Movie (2001) {imdb-tt0211915} - [Remux-2160p][TrueHD Atmos 7.1][HDR10][HEVC]-GRP.mkv")
	if len(plan.Links) != 1 || plan.Links[0].Path != want {
		t.Fatalf("plan %+v", plan.Links)
	}
	target, err := os.Readlink(want)
	if err != nil || target != filepath.Join(e.mount, "__all__", movieRelease, "movie.mkv") {
		t.Fatalf("link: %q %v", target, err)
	}
	if len(plan.Rejected) != 1 || !strings.Contains(plan.Rejected[0].Reason, "sample") {
		t.Errorf("sample not rejected: %+v", plan.Rejected)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "imported" {
		t.Errorf("download state %s", got)
	}
	if got := e.one(`SELECT count(*) FROM link l JOIN link_version lv ON lv.link_id = l.id JOIN content_file cf ON cf.id = l.content_file_id
		WHERE lv.version_id = 10 AND l.state = 'active' AND cf.probe IS NOT NULL`); got != "1" {
		t.Errorf("link rows %s", got)
	}
	if got := e.one(`SELECT mount_folder FROM content WHERE info_hash = ?`, hashA); got != movieRelease {
		t.Errorf("content mount folder %s", got)
	}
	// A Plex scan of the folder is queued, debounced.
	if got := e.one(`SELECT count(*) FROM job WHERE kind = 'plex.scan' AND run_at > ?`, store.Millis(e.now)); got != "1" {
		t.Errorf("scan jobs %s", got)
	}
	// Idempotent: importing again changes nothing on disk.
	before := tree(t, e.dir)
	e.exec(`UPDATE download SET state = 'ready' WHERE id = 1`)
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if after := tree(t, e.dir); strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Errorf("second import changed the tree")
	}
}

// TestGateBlocksWrites: with library.enabled false, or the version in shadow mode, an import writes nothing to the
// filesystem, leaves the download ready, and records what it would have linked.
func TestGateBlocksWrites(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		enabled bool
		mode    policy.Mode
		reason  string
	}{{"disabled", false, policy.ModeActive, "disabled"}, {"shadow", true, policy.ModeShadow, "shadow"}} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, c.enabled, c.mode)
			e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
			e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
			// An old link the upgrade would move aside.
			old := filepath.Join(e.movies, "Amelie The Movie (2001)", "old.mkv")
			if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/somewhere/old.mkv", old); err != nil {
				t.Fatal(err)
			}
			e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at) VALUES (5, ?, '/somewhere/old.mkv', 1, 'active', 0, 0)`, old)
			e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (5, 10)`)
			before := tree(t, e.dir)
			plan, err := e.lib.Import(e.ctx, 1, false)
			if err != nil {
				t.Fatal(err)
			}
			if !plan.DryRun || !strings.Contains(plan.Refused, c.reason) || len(plan.Links) != 1 || len(plan.Links[0].Replaces) != 1 {
				t.Errorf("plan %+v", plan)
			}
			if after := tree(t, e.dir); strings.Join(before, "\n") != strings.Join(after, "\n") {
				t.Errorf("filesystem changed while gated:\n%s\n---\n%s", strings.Join(before, "\n"), strings.Join(after, "\n"))
			}
			if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "ready" {
				t.Errorf("download state %s", got)
			}
			if got := e.one(`SELECT count(*) FROM library_action WHERE mode = 'dry_run' AND kind IN ('link', 'replace')`); got != "2" {
				t.Errorf("dry run actions %s", got)
			}
			if got := e.one(`SELECT state FROM library_import WHERE download_id = 1`); got != "dry_run" {
				t.Errorf("library_import %s", got)
			}
			// Relink and purge write nothing either, and no Plex call can be made.
			if _, err := e.lib.Relink(e.ctx, "", false); err != nil {
				t.Fatal(err)
			}
			e.now = e.now.Add(400 * 24 * time.Hour)
			if _, err := e.lib.PurgeTrash(e.ctx); err != nil {
				t.Fatal(err)
			}
			if after := tree(t, e.dir); strings.Join(before, "\n") != strings.Join(after, "\n") {
				t.Errorf("relink or purge changed the filesystem while gated")
			}
			if c.name == "disabled" {
				if err := e.lib.Gate().CheckPlex(); !errors.Is(err, ErrRefused) {
					t.Errorf("plex allowed while disabled")
				}
			}
		})
	}
}

func TestUpgradeMovesAside(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.release("Old.Release", map[string]float64{"old.mkv": 7000})
	oldTarget := filepath.Join(e.mount, "__all__", "Old.Release", "old.mkv")
	folder := filepath.Join(e.movies, "Amelie The Movie (2001)")
	old := filepath.Join(folder, "Amelie The Movie (2001) - old.mkv")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(oldTarget, old); err != nil {
		t.Fatal(err)
	}
	// The arr's file (no link row yet): an upgrade replaces it as well.
	e.exec(`INSERT INTO version_file (version_id, title_id, instance, arr_file_id, path) VALUES (10, 1, 'radarr', 1, ?)`, old)
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old link still in the library: %v", err)
	}
	tp := e.one(`SELECT trash_path FROM link_trash`)
	if !strings.HasPrefix(tp, e.trash) {
		t.Errorf("trash path %s", tp)
	}
	if tg, _ := os.Readlink(tp); tg != oldTarget {
		t.Errorf("trash copy points at %q", tg)
	}
	if _, err := os.Stat(oldTarget); err != nil {
		t.Errorf("debrid content touched: %v", err)
	}
	if _, err := os.Readlink(plan.Links[0].Path); err != nil {
		t.Error(err)
	}
	// After keep_days, the trash copy goes, and still only the link.
	e.now = e.now.Add(31 * 24 * time.Hour)
	if n, err := e.lib.PurgeTrash(e.ctx); err != nil || n != 1 {
		t.Fatalf("purge %d %v", n, err)
	}
	if _, err := os.Lstat(tp); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("trash copy remains")
	}
	if _, err := os.Stat(oldTarget); err != nil {
		t.Errorf("debrid content touched by purge: %v", err)
	}
}

func TestSeriesPack(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	rel := "Show.S01.2160p.WEB-DL.DDP5.1.H.265-GRP"
	e.release(rel, map[string]float64{"Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2700,
		"Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2650, "Show.S01E03.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2600})
	e.download(1, "tv", 2, 20, rel, rel, "[201, 202]")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, l := range plan.Links {
		paths = append(paths, strings.TrimPrefix(l.Path, e.shows+"/"))
	}
	sort.Strings(paths)
	// Each file is named as what it is (its own aggregated quality), not as the download's decision says.
	want := []string{"Show (2020)/Season 01/Show (2020) - S01E01 - Pilot [WEBDL-2160p]-GRP.mkv",
		"Show (2020)/Season 01/Show (2020) - S01E02 - Second Part [WEBDL-2160p]-GRP.mkv"}
	if strings.Join(paths, "|") != strings.Join(want, "|") {
		t.Errorf("links\n got %q\nwant %q", paths, want)
	}
	if len(plan.Unmatched) != 1 {
		t.Errorf("E03 is not in the download: %v", plan.Unmatched)
	}
	if got := e.one(`SELECT count(*) FROM link_episode`); got != "2" {
		t.Errorf("link_episode %s", got)
	}
}

func TestSampleGuardRejects(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 40 * 60}) // 40 min for a 120 min film
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	before := tree(t, e.movies)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 0 || len(plan.Rejected) != 1 {
		t.Fatalf("plan %+v", plan)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "failed" {
		t.Errorf("state %s", got)
	}
	if after := tree(t, e.movies); strings.Join(before, "|") != strings.Join(after, "|") {
		t.Error("rejected import wrote")
	}
	// Resolution that contradicts the release.
	f := &PlannedFile{Path: "/x.mkv", Probe: &probe.Result{RunTimeSeconds: 7000, Video: &probe.Video{Width: 1280, Height: 720}}}
	if r := sampleCheck(0.5, true, "movie", 120, false, f, 2160); !strings.Contains(r, "resolution") {
		t.Errorf("resolution guard: %q", r)
	}
	f.Probe.Video.Width, f.Probe.Video.Height = 3840, 1600 // scope 2160p
	if r := sampleCheck(0.5, true, "movie", 120, false, f, 2160); r != "" {
		t.Errorf("scope film rejected: %q", r)
	}
}

func TestWaitingAndMountDown(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil || !plan.Waiting {
		t.Fatalf("waiting: %+v %v", plan, err)
	}
	e.now = e.now.Add(7 * time.Minute)
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT count(*) FROM library_action WHERE kind = 'alert'`); got != "1" {
		t.Errorf("alerts %s", got)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "ready" {
		t.Errorf("a timeout must not fail the download: %s", got)
	}
	// Mount gone: nothing is read as missing.
	if err := os.Rename(filepath.Join(e.mount, "__all__"), filepath.Join(e.mount, "x")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.lib.Import(e.ctx, 1, false); !errors.Is(err, ErrMountDown) {
		t.Errorf("import on a dead mount: %v", err)
	}
	if _, err := e.lib.Relink(e.ctx, "", false); !errors.Is(err, ErrMountDown) {
		t.Errorf("relink on a dead mount: %v", err)
	}
}

// TestFreshWaiting: theatarr serve re-checks soon only while a download that turned ready within the window waits for
// its release to show on the mount; past the window, or waiting for another reason, the poll interval applies.
func TestFreshWaiting(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	if e.lib.FreshWaiting(e.ctx) {
		t.Error("fresh before any import")
	}
	e.exec(`UPDATE download SET ready_at = ? WHERE id = 1`, store.Millis(e.now))
	if plan, err := e.lib.Import(e.ctx, 1, false); err != nil || !plan.Waiting {
		t.Fatalf("waiting: %+v %v", plan, err)
	}
	if !e.lib.FreshWaiting(e.ctx) {
		t.Error("a just-ready download waiting for the mount is not fresh")
	}
	e.exec(`UPDATE library_import SET last_error = ? WHERE download_id = 1`, UnreadablePrefix+"EIO")
	if e.lib.FreshWaiting(e.ctx) {
		t.Error("an unreadable download is re-checked soon")
	}
	e.exec(`UPDATE library_import SET last_error = ? WHERE download_id = 1`, notOnMount)
	e.exec(`UPDATE download SET ready_at = ? WHERE id = 1`, store.Millis(e.now.Add(time.Second)))
	if e.lib.FreshWaiting(e.ctx) {
		t.Error("a ready time in the future is fresh")
	}
	e.exec(`UPDATE download SET ready_at = NULL WHERE id = 1`)
	if e.lib.FreshWaiting(e.ctx) {
		t.Error("a download without a ready time is fresh")
	}
	e.exec(`UPDATE download SET ready_at = ? WHERE id = 1`, store.Millis(e.now))
	e.now = e.now.Add(ReadyRecheckWindow)
	if e.lib.FreshWaiting(e.ctx) {
		t.Error("fresh past the window")
	}

	// A version the gate refuses (library off) only plans its import: no quick re-check for it.
	g := newEnv(t, false, policy.ModeActive)
	g.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	g.exec(`UPDATE download SET ready_at = ? WHERE id = 1`, store.Millis(g.now))
	if plan, err := g.lib.Import(g.ctx, 1, false); err != nil || !plan.Waiting {
		t.Fatalf("gated waiting: %+v %v", plan, err)
	}
	if g.lib.FreshWaiting(g.ctx) {
		t.Error("a gated version's wait is re-checked soon")
	}
}

func TestRelink(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	// zurg renamed "Movie.Release" to "Movie.Release {abcdef}" when another hash took the bare name.
	e.release("Movie.Release {abcdef}", map[string]float64{"movie.mkv": 7000})
	e.exec(`INSERT INTO content (id, info_hash, name, state, first_seen_at, last_seen_at) VALUES (1, ?, 'Movie.Release', 'present', 0, 0)`, hashA)
	e.exec(`INSERT INTO content (id, info_hash, name, state, first_seen_at, last_seen_at) VALUES (2, ?, 'Gone.Release', 'absent', 0, 0)`,
		"1111111111111111111111111111111111111111")
	dangling := filepath.Join(e.movies, "Movie (2001)", "Movie (2001).mkv")
	gone := filepath.Join(e.movies, "Gone (1999)", "Gone (1999).mkv")
	for p, tg := range map[string]string{dangling: "Movie.Release/movie.mkv", gone: "Gone.Release/gone.mkv"} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(e.mount, "__all__", tg), p); err != nil {
			t.Fatal(err)
		}
	}
	// Verify is read-only and sees both.
	before := tree(t, e.dir)
	scans, err := e.lib.Verify(e.ctx, "movie-remux")
	if err != nil {
		t.Fatal(err)
	}
	if len(scans) != 1 || len(scans[0].Dangling) != 2 || scans[0].Causes["renamed"] != 1 {
		t.Fatalf("verify %+v", scans[0])
	}
	if after := tree(t, e.dir); strings.Join(before, "|") != strings.Join(after, "|") {
		t.Error("verify wrote")
	}
	scans, err = e.lib.Relink(e.ctx, "", false)
	if err != nil {
		t.Fatal(err)
	}
	var movieScan *LinkScan
	for _, s := range scans {
		if s.Version == "movie-remux" {
			movieScan = s
		}
	}
	if len(movieScan.Relinked) != 1 || len(movieScan.Dangling) != 1 || movieScan.Dangling[0].Cause != "gone" {
		t.Fatalf("relink %+v", movieScan)
	}
	tg, _ := os.Readlink(dangling)
	if tg != filepath.Join(e.mount, "__all__", "Movie.Release {abcdef}", "movie.mkv") {
		t.Errorf("relinked to %q", tg)
	}
	if tg, _ := os.Readlink(gone); !strings.Contains(tg, "Gone.Release") {
		t.Errorf("unfixable link changed: %q", tg)
	}
}

func TestGatePaths(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	g := e.lib.Gate()
	// A directory inside the root that is a symlink to somewhere else.
	if err := os.Symlink(e.outside, filepath.Join(e.movies, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		version, path string
		ok            bool
	}{
		{"movie-remux", filepath.Join(e.movies, "A (2001)", "a.mkv"), true},
		{"movie-remux", e.movies, false},
		{"movie-remux", filepath.Join(e.shows, "x.mkv"), false},
		{"movie-remux", filepath.Join(e.outside, "x.mkv"), false},
		{"movie-remux", e.movies + "/../elsewhere/x.mkv", false},
		{"movie-remux", filepath.Join(e.movies, "escape", "x.mkv"), false},
		{"movie-remux", filepath.Join(e.mount, "__all__", "x"), false},
		{"movie-remux", "relative/x.mkv", false},
		{"nope", filepath.Join(e.movies, "x.mkv"), false},
	} {
		err := g.CheckPath(c.version, c.path)
		if (err == nil) != c.ok {
			t.Errorf("%s %s: %v", c.version, c.path, err)
		}
	}
	if err := g.CheckTrash("movie-remux", filepath.Join(e.trash, "x")); err != nil {
		t.Errorf("trash: %v", err)
	}
	if err := g.CheckTrash("movie-remux", filepath.Join(e.movies, "x")); err == nil {
		t.Error("trash check allowed a library path")
	}
	// The writer refuses to replace a regular file.
	file := filepath.Join(e.movies, "real.mkv")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.lib.w.symlink("movie-remux", "/t", file); !errors.Is(err, ErrRefused) {
		t.Errorf("replaced a regular file: %v", err)
	}
	if err := e.lib.w.removeLink("movie-remux", file); !errors.Is(err, ErrRefused) {
		t.Errorf("removed a regular file: %v", err)
	}
}

// TestWritesOnlyInFS: fs.go's writer is the only code in the package that changes the filesystem, so the gate
// sees every write.
func TestWritesOnlyInFS(t *testing.T) {
	t.Parallel()
	files, _ := filepath.Glob("*.go")
	calls := []string{"os.Symlink", "os.Remove", "os.Rename", "os.Mkdir", "os.WriteFile", "os.Create", "os.OpenFile",
		"os.Chmod", "os.Chown", "os.Lchown", "os.Link", "os.Truncate", "os.Chtimes"}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "fs.go" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range calls {
			if strings.Contains(string(b), c+"(") || strings.Contains(string(b), c+"All(") {
				t.Errorf("%s calls %s outside fs.go", f, c)
			}
		}
	}
}

type fakeScanner struct{ calls []string }

func (f *fakeScanner) Scan(_ context.Context, section int, path string) error {
	f.calls = append(f.calls, fmt.Sprintf("%d %s", section, path))
	return nil
}

func TestPlexScanJob(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	w := jobs.NewWorker(e.queue, jobs.WorkerOptions{Owner: "t"})
	fs := &fakeScanner{}
	e.lib.RegisterJobs(w, fs)
	if err := e.lib.EnqueueScan(e.ctx, "movie-remux", filepath.Join(e.movies, "A")); err != nil {
		t.Fatal(err)
	}
	if err := e.lib.EnqueueScan(e.ctx, "movie-remux", filepath.Join(e.movies, "A")); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Minute)
	for {
		ran, err := w.RunOne(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			break
		}
	}
	if len(fs.calls) != 1 || fs.calls[0] != "18 "+filepath.Join(e.movies, "A") {
		t.Errorf("scans %v", fs.calls)
	}
	// Disabled: the handler refuses even a queued job.
	e2 := newEnv(t, false, policy.ModeActive)
	w2 := jobs.NewWorker(e2.queue, jobs.WorkerOptions{Owner: "t"})
	fs2 := &fakeScanner{}
	e2.lib.RegisterJobs(w2, fs2)
	if _, _, err := e2.queue.Enqueue(e2.ctx, ScanJobKind, scanPayload{Version: "movie-remux", Section: 18, Path: "/x"}, jobs.EnqueueOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := w2.RunOne(e2.ctx); err != nil {
		t.Fatal(err)
	}
	if len(fs2.calls) != 0 {
		t.Errorf("plex called while disabled: %v", fs2.calls)
	}
}

// Sonarr v5's import check (DownloadedEpisodesImportService.CheckEmptyResultForIssue, RejectedFileExtensionService):
// a download with no video file but a potentially dangerous or executable one fails with that reason and is
// blocklisted; without one it remains held for manual resolution.
func TestImportRefusesDangerousFiles(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv.exe": 7000, "readme.txt": 0})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	want := "Caution: Found executable file with extension: .exe"
	if got := e.one(`SELECT error FROM download WHERE id = 1 AND state = 'failed'`); got != "library: "+want {
		t.Errorf("download error %q", got)
	}
	if got := e.one(`SELECT reason || '|' || source FROM router_blocklist WHERE version_key = 'movie-remux' AND info_hash = ?`, hashA); got != want+"|library" {
		t.Errorf("blocklist %q", got)
	}
	if got := e.one(`SELECT count(*) FROM link`); got != "0" {
		t.Errorf("%s links", got)
	}

	e2 := newEnv(t, true, policy.ModeActive)
	e2.release(movieRelease, map[string]float64{"Movie/payload.lnk": 0, "Movie/run.bat": 0})
	e2.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	if _, err := e2.lib.Import(e2.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if got := e2.one(`SELECT error FROM download WHERE id = 1`); got != "library: Caution: Found potentially dangerous file with extension: .lnk" {
		t.Errorf("dangerous first: %q", got)
	}

	e3 := newEnv(t, true, policy.ModeActive)
	e3.release(movieRelease, map[string]float64{"info.nfo": 0})
	e3.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	if _, err := e3.lib.Import(e3.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if got := e3.one(`SELECT state || '/' || COALESCE(error, '') FROM download WHERE id = 1`); got != "ready/" {
		t.Errorf("no video download: %q", got)
	}
	if got := e3.one(`SELECT state || '/' || last_error FROM library_import WHERE download_id = 1`); !strings.HasPrefix(got, "rejected/no video files in ") {
		t.Errorf("no video import: %q", got)
	}
	if got := e3.one(`SELECT COUNT(*) FROM router_blocklist WHERE download_id = 1`); got != "0" {
		t.Errorf("no video blocklist rows: %q", got)
	}

	e4 := newEnv(t, true, policy.ModeActive)
	e4.release(movieRelease, map[string]float64{"movie.mkv.exe": 7000, "readme.txt": 0})
	e4.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	e4.exec(`INSERT INTO download_file (download_id, rel, episodes, source, updated_at)
		VALUES (1, 'movie.mkv.exe', '[]', 'manual', 0)`)
	if _, err := e4.lib.Import(e4.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if got := e4.one(`SELECT state FROM download WHERE id = 1`); got != "failed" {
		t.Errorf("selected dangerous file state: %q", got)
	}
	if got := e4.one(`SELECT COUNT(*) FROM router_blocklist WHERE download_id = 1`); got != "1" {
		t.Errorf("selected dangerous file blocklist rows: %q", got)
	}
}

// cpFake is a provider whose list is fixed, for the provider sync after an import.
type cpFake struct {
	provider.Provider
	list []provider.Torrent
}

func (p *cpFake) Key() string                                          { return "rd" }
func (p *cpFake) Kind() string                                         { return provider.KindRealDebrid }
func (p *cpFake) Torrents(context.Context) ([]provider.Torrent, error) { return p.list, nil }

// cpRow is the download's content_provider row for rd: state, progress, torrent id, folder, size, seen_at.
func (e *env) cpRow() string {
	e.t.Helper()
	return e.one(`SELECT group_concat(cp.state || '|' || COALESCE(cp.progress, '') || '|' || COALESCE(cp.torrent_id, '') ||
		'|' || COALESCE(cp.folder_name, '') || '|' || COALESCE(cp.size_bytes, '') || '|' || cp.seen_at, ';')
		FROM content_provider cp JOIN content c ON c.id = cp.content_id WHERE c.info_hash = ? AND cp.provider = 'rd'`, hashA)
}

// TestImportRecordsProvider: importing a debrid provider's download records that the provider holds the content
// (content_provider present, progress 100), so the link's file shows Cached on at once, before any provider sync. A
// download whose provider is not a configured debrid provider records nothing.
func TestImportRecordsProvider(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Providers = map[string]*policy.Provider{"rd": {Kind: "realdebrid", Order: 1}}
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	e.exec(`UPDATE download SET provider_torrent_id = 'T1' WHERE id = 1`)
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("present|100.0|T1|%s||%d", movieRelease, store.Millis(e.now))
	if got := e.cpRow(); got != want {
		t.Fatalf("content_provider %q, want %q", got, want)
	}
	// The link's content satisfies the read side's providerHolds (internal/web/library.go) for rd.
	if got := e.one(`SELECT count(*) FROM link l JOIN content_file cf ON cf.id = l.content_file_id
		JOIN content_provider cp ON cp.content_id = cf.content_id
		WHERE l.download_id = 1 AND cp.provider = 'rd' AND cp.state = 'present' AND cp.progress >= 100`); got != "1" {
		t.Errorf("links held by rd: %s", got)
	}

	f := newEnv(t, true, policy.ModeActive)
	f.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	f.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	if _, err := f.lib.Import(f.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if got := f.one(`SELECT count(*) FROM content_provider`); got != "0" {
		t.Errorf("unconfigured provider recorded: %s rows", got)
	}
}

// TestImportProviderRowAndSync: the provider sync after an import keeps the import's row present and fills in the
// rest, in the one row; an import never degrades a row the sync wrote, and brings an absent one back to present.
func TestImportProviderRowAndSync(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Providers = map[string]*policy.Provider{"rd": {Kind: "realdebrid", Order: 1}}
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	e.exec(`UPDATE download SET provider_torrent_id = 'T1' WHERE id = 1`)
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	fp := &cpFake{list: []provider.Torrent{{ID: "T1", Hash: hashA, Name: movieRelease, Bytes: 5000,
		Status: "downloaded", State: provider.StateDownloaded, Progress: 100, Links: 1}}}
	syncAt := e.now.Add(time.Hour)
	s := &provider.Syncer{Store: e.st, Providers: map[string]provider.Provider{"rd": fp}, Configured: []string{"rd"},
		Now: func() time.Time { return syncAt }}
	if _, err := s.Run(e.ctx, provider.SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	synced := fmt.Sprintf("present|100.0|T1|%s|5000|%d", movieRelease, store.Millis(e.now))
	if got := e.cpRow(); got != synced {
		t.Fatalf("after sync %q, want %q", got, synced)
	}
	if got := e.one(`SELECT status FROM content_provider WHERE provider = 'rd'`); got != "downloaded" {
		t.Errorf("status %q", got)
	}

	// Importing again (another torrent id on the download) leaves the sync's complete row as it is.
	e.now = e.now.Add(2 * time.Hour)
	e.exec(`UPDATE download SET state = 'ready', provider_torrent_id = 'T9' WHERE id = 1`)
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if got := e.cpRow(); got != synced {
		t.Errorf("import changed the synced row: %q, want %q", got, synced)
	}

	// Gone from the account: the sync marks it absent; a later import of it brings it back present.
	fp.list = nil
	if _, err := s.Run(e.ctx, provider.SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT state FROM content_provider WHERE provider = 'rd'`); got != "absent" {
		t.Fatalf("state after empty list %s", got)
	}
	e.now = e.now.Add(time.Hour)
	e.exec(`UPDATE download SET state = 'ready' WHERE id = 1`)
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	back := fmt.Sprintf("present|100.0|T9|%s|5000|%d", movieRelease, store.Millis(e.now))
	if got := e.cpRow(); got != back {
		t.Errorf("after re-import %q, want %q", got, back)
	}
	if got := e.one(`SELECT absent_at IS NULL FROM content_provider WHERE provider = 'rd'`); got != "1" {
		t.Errorf("absent_at kept")
	}
}
