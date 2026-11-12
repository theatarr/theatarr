package arrimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/importarr"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/library/probe"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/metadata/tvrefresh"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

var testNow = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

// fakeArr answers the instance's GETs from endpoint → JSON; per series episode lists are keyed "episode?seriesId=N".
type fakeArr struct {
	answers map[string]any
	gets    []string
}

func (f *fakeArr) GetJSON(_ context.Context, endpoint string, q url.Values, v any) error {
	key := endpoint
	if id := q.Get("seriesId"); id != "" {
		key += "?seriesId=" + id
	}
	f.gets = append(f.gets, key)
	a, ok := f.answers[key]
	if !ok {
		a = []any{}
	}
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

type fakeTMDb struct{}

func (fakeTMDb) Collection(context.Context, int) (*tmdb.Collection, error) {
	return nil, errors.New("none")
}
func (fakeTMDb) List(context.Context, int) (*tmdb.List, error) { return nil, errors.New("none") }
func (fakeTMDb) MovieDetails(_ context.Context, id int) (*tmdb.Movie, error) {
	return &tmdb.Movie{ID: id, Title: fmt.Sprintf("Film %d", id), ReleaseDate: "2001-04-25", Runtime: 120}, nil
}

// fakeSeries stands in for the series refresh: it writes the episode list of a series from source.
type fakeSeries struct {
	st      *store.Store
	source  string
	refresh []int64
}

func (f *fakeSeries) RefreshTitle(ctx context.Context, id int64, _ bool) (*tvrefresh.Result, error) {
	f.refresh = append(f.refresh, id)
	w := f.st.Writer()
	if _, err := w.ExecContext(ctx, `INSERT INTO series_meta(title_id, source) VALUES(?, ?)
		ON CONFLICT(title_id) DO UPDATE SET source = excluded.source`, id, f.source); err != nil {
		return nil, err
	}
	aired, future := store.Millis(testNow.AddDate(-1, 0, 0)), store.Millis(testNow.AddDate(0, 1, 0))
	for n, at := range []int64{aired, aired, future} {
		if _, err := w.ExecContext(ctx, `INSERT INTO episode(title_id, season, number, name, air_date_utc_at,
			created_at, updated_at) VALUES(?, 1, ?, ?, ?, 0, 0) ON CONFLICT DO NOTHING`, id, n+1,
			fmt.Sprintf("Episode %d", n+1), at); err != nil {
			return nil, err
		}
	}
	if _, err := w.ExecContext(ctx, `INSERT INTO episode_version_state(version_id, episode_id, monitored, state,
		updated_at) SELECT v.id, e.id, 1, 'wanted', 0 FROM version v JOIN episode e ON e.title_id = v.title_id
		WHERE v.title_id = ? ON CONFLICT DO NOTHING`, id); err != nil {
		return nil, err
	}
	return &tvrefresh.Result{TitleID: id, Source: f.source, Episodes: 3}, nil
}

// orderLib records, at each link pass, whether the library's copy was still unmonitored.
type orderLib struct {
	*library.Library
	st         *store.Store
	monitored  []bool
	adoptCalls int
}

func (o *orderLib) AdoptImported(ctx context.Context, version string, files []library.ImportFile) (
	*library.AdoptReport, error) {
	o.adoptCalls++
	var m bool
	if err := o.st.Reader().QueryRowContext(ctx, `SELECT monitored FROM version WHERE title_id = ? AND policy_key = ?`,
		files[0].TitleID, version).Scan(&m); err != nil {
		return nil, err
	}
	o.monitored = append(o.monitored, m)
	return o.Library.AdoptImported(ctx, version, files)
}

type env struct {
	t                    *testing.T
	ctx                  context.Context
	st                   *store.Store
	pol                  *policy.Policy
	mount, movies, shows string
	radarrDir, sonarrDir string
	lib                  *orderLib
	series               *fakeSeries
	intake               *catalog.MovieIntake
	probes               map[string]*probe.Result
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{t: t, ctx: context.Background(), mount: filepath.Join(dir, "zurg"),
		movies: filepath.Join(dir, "media", "movies"), shows: filepath.Join(dir, "media", "shows"),
		radarrDir: filepath.Join(dir, "radarr"), sonarrDir: filepath.Join(dir, "sonarr"),
		probes: map[string]*probe.Result{}}
	for _, d := range []string{filepath.Join(e.mount, "__all__"), e.movies, e.shows, e.radarrDir, e.sonarrDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(e.ctx, storetest.Seed(t, filepath.Join(dir, "theatarr.db")), store.Options{})
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
			"movies": {Mode: policy.ModeActive, Kind: policy.KindMovie, Profile: "movie", Library: e.movies,
				Naming: policy.Naming{Folder: "{Movie CleanTitle} ({Release Year})",
					File: "{Movie CleanTitle} ({Release Year}) {[Quality Full]}{-Release Group}"}},
			"shows": {Mode: policy.ModeActive, Kind: policy.KindSeries, Profile: "tv", Library: e.shows,
				Naming: policy.Naming{Folder: "{Series CleanTitleWithoutYear}", Season: "Season {season:00}",
					File: "{Series CleanTitleWithoutYear} - S{season:00}E{episode:00} {[Quality Full]}"}},
		},
		Profiles: map[string]*policy.Profile{
			"movie": {Qualities: []policy.QualityItem{{Name: "Remux-2160p"}, {Name: "WEBDL-1080p"}}, Cutoff: "Remux-2160p"},
			"tv":    {Qualities: []policy.QualityItem{{Name: "Bluray-2160p"}, {Name: "WEBDL-1080p"}}, Cutoff: "Bluray-2160p"},
		},
		Library: &policy.Library{Enabled: true, Mount: policy.LibraryMount{Path: e.mount},
			Trash: policy.LibraryTrash{Dir: filepath.Join(dir, "media", ".trash")},
			Versions: map[string]*policy.LibraryVersion{
				"movies": {Roots: []string{e.movies}}, "shows": {Roots: []string{e.shows}}}},
	}
	now := func() time.Time { return testNow }
	e.lib = e.newLib()
	e.series = &fakeSeries{st: st, source: "tvdb"}
	e.intake = &catalog.MovieIntake{Store: st, Policy: e.pol, TMDb: fakeTMDb{}, Now: now}
	return e
}

// newLib builds the library service on the current policy.
func (e *env) newLib() *orderLib {
	e.t.Helper()
	now := func() time.Time { return testNow }
	engine, err := decide.New(e.pol, decide.Options{Now: now})
	if err != nil {
		e.t.Fatal(err)
	}
	lib := library.New(library.Options{Store: e.st, Policy: e.pol, Engine: engine, Now: now,
		Probe: func(p string, _ int64) (*probe.Result, error) {
			if r, ok := e.probes[p]; ok {
				return r, nil
			}
			return nil, probe.ErrUnknownFormat
		},
		Formats: func(context.Context, string, int64, string) []string { return nil }})
	return &orderLib{Library: lib, st: e.st}
}

func (e *env) deps(g *fakeArr) Deps {
	return Deps{Store: e.st, Policy: e.pol, Titles: e.intake, Series: e.series, TVDB: true, Library: e.lib, Now: func() time.Time { return testNow },
		Arr: func(u, key string) (importarr.Getter, error) {
			if key != "secret" {
				return nil, errors.New("bad key")
			}
			return g, nil
		}}
}

// release puts a file on the mount and makes the instance's file at p a symlink to it.
func (e *env) release(p, rel string) {
	e.t.Helper()
	target := filepath.Join(e.mount, "__all__", rel)
	for _, d := range []string{filepath.Dir(target), filepath.Dir(p)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			e.t.Fatal(err)
		}
	}
	if err := os.WriteFile(target, []byte("video"), 0o644); err != nil {
		e.t.Fatal(err)
	}
	e.probes[target] = &probe.Result{Container: "matroska", RunTimeSeconds: 7000,
		Video: &probe.Video{Codec: "hevc", Width: 3840, Height: 2160, BitDepth: 10},
		Audio: []probe.Audio{{Codec: "truehd", Channels: 8, Language: "eng"}}}
	if err := os.Symlink(target, p); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) one(q string, args ...any) string {
	e.t.Helper()
	var v any
	if err := e.st.Reader().QueryRowContext(e.ctx, q, args...).Scan(&v); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return fmt.Sprint(v)
}

func quality(name string) map[string]any {
	return map[string]any{"quality": map[string]any{"id": 1, "name": name}, "revision": map[string]any{"version": 1}}
}

// radarr is an instance at /movies (read by Theatarr at radarrDir): a monitored film with a file on the mount, a
// monitored film without a file, and a film whose quality profile is not ticked.
func (e *env) radarr() *fakeArr {
	movie := func(id, tmdbID int, title string, profile int, hasFile bool) map[string]any {
		return map[string]any{"id": id, "title": title, "year": 2001, "tmdbId": tmdbID, "monitored": true,
			"hasFile": hasFile, "qualityProfileId": profile, "minimumAvailability": "released", "status": "released",
			"path": "/movies/" + title, "rootFolderPath": "/movies", "added": "2024-01-02T03:04:05Z"}
	}
	e.release(filepath.Join(e.radarrDir, "Amelie", "Amelie.mkv"), "Amelie.2001.2160p.REMUX-GRP/movie.mkv")
	return &fakeArr{answers: map[string]any{
		"system/status":  map[string]any{"appName": "Radarr", "version": "5.14.0"},
		"rootfolder":     []map[string]any{{"id": 1, "path": "/movies"}},
		"qualityprofile": []map[string]any{{"id": 7, "name": "UHD"}, {"id": 8, "name": "SD"}},
		"movie": []map[string]any{movie(1, 194, "Amelie", 7, true), movie(2, 195, "Brazil", 7, false),
			movie(3, 196, "Cinema", 8, false)},
		"moviefile": []map[string]any{{"id": 11, "movieId": 1, "relativePath": "Amelie.mkv",
			"path": "/movies/Amelie/Amelie.mkv", "size": 1000, "quality": quality("Remux-2160p"),
			"releaseGroup": "GRP", "sceneName": "Amelie.2001.2160p.REMUX-GRP", "dateAdded": "2024-02-01T00:00:00Z"}},
	}}
}

// sonarr is an instance at /tv with one series: season 1 monitored, E01 (with a file on the mount) and the future
// E03 monitored, E02 not.
func (e *env) sonarr() *fakeArr {
	e.release(filepath.Join(e.sonarrDir, "Show", "Season 01", "Show.S01E01.mkv"), "Show.S01.2160p-GRP/e01.mkv")
	return &fakeArr{answers: map[string]any{
		"system/status":  map[string]any{"appName": "Sonarr", "version": "4.0.20"},
		"rootfolder":     []map[string]any{{"id": 1, "path": "/tv"}},
		"qualityprofile": []map[string]any{{"id": 7, "name": "UHD"}},
		"series": []map[string]any{{"id": 1, "title": "Show", "year": 2020, "tvdbId": 5, "tmdbId": 50005,
			"monitored": true, "qualityProfileId": 7, "status": "continuing", "path": "/tv/Show",
			"rootFolderPath": "/tv", "added": "2024-01-02T03:04:05Z", "seriesType": "standard",
			"monitorNewItems": "none", "seasons": []map[string]any{{"seasonNumber": 1, "monitored": true}}}},
		"episode?seriesId=1": []map[string]any{
			{"id": 101, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 1, "monitored": true, "episodeFileId": 501, "hasFile": true},
			{"id": 102, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 2, "monitored": false},
			{"id": 103, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 3, "monitored": true}},
		"episodefile?seriesId=1": []map[string]any{{"id": 501, "seriesId": 1, "seasonNumber": 1,
			"relativePath": "Season 01/Show.S01E01.mkv", "path": "/tv/Show/Season 01/Show.S01E01.mkv", "size": 1000,
			"quality": quality("Bluray-2160p"), "releaseGroup": "GRP", "dateAdded": "2024-02-01T00:00:00Z"}},
	}}
}

// TestRunWholeSequence imports a film and a series against real catalogue and library services: each copy is added
// unmonitored, gets its monitoring and its linked file, and only then the instance's monitored flag; a second run
// adds nothing and finds the links in place.
func TestRunWholeSequence(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	radarr := e.radarr()
	req := Request{Flavour: "radarr", URL: "http://radarr:7878", APIKey: "secret", Library: "movies", Profiles: []int{7},
		Paths: map[string]string{"/movies": e.radarrDir}}

	// Preview writes nothing and counts the linkable file.
	preview := req
	preview.DryRun = true
	rep, err := Run(e.ctx, e.deps(radarr), preview, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.AddedTitle != 2 || rep.Skipped.Profile != 1 || rep.Linkable != 1 || !rep.Writable || rep.Files != 1 {
		t.Fatalf("preview %+v", rep)
	}
	if n := e.one(`SELECT COUNT(*) FROM title`); n != "0" {
		t.Fatalf("preview wrote %s titles", n)
	}

	rep, err = Run(e.ctx, e.deps(radarr), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.AddedTitle != 2 || rep.Linked != 1 || rep.MissingMonitored != 1 || rep.Skipped.Profile != 1 || rep.App != "Radarr" {
		t.Fatalf("import %+v", rep)
	}
	if len(e.lib.monitored) != 1 || e.lib.monitored[0] {
		t.Fatalf("the film's copy was monitored while linking: %v", e.lib.monitored)
	}
	if got := e.one(`SELECT v.monitored FROM version v JOIN title t ON t.id = v.title_id WHERE t.tmdb_id = 194`); got != "1" {
		t.Fatalf("film not released to monitored: %s", got)
	}
	p := e.one(`SELECT path FROM link`)
	if !strings.HasPrefix(p, e.movies+"/") {
		t.Fatalf("link at %s", p)
	}
	if target, err := os.Readlink(p); err != nil || !strings.HasPrefix(target, e.mount) {
		t.Fatalf("link %s -> %s %v", p, target, err)
	}
	if src := e.one(`SELECT adopted_from FROM link`); src != "radarr@radarr:7878 /movies/Amelie/Amelie.mkv" {
		t.Fatalf("adopted_from %q", src)
	}
	for _, g := range radarr.gets {
		if g == "tag" || g == "history" || g == "customformat" {
			t.Fatalf("read %s", g)
		}
	}

	again, err := Run(e.ctx, e.deps(radarr), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again.AddedTitle+again.AddedLibrary != 0 || again.Present != 2 || again.Existing != 1 || again.Linked != 0 {
		t.Fatalf("second run %+v", again)
	}
	if n := e.one(`SELECT COUNT(*) FROM link`); n != "1" {
		t.Fatalf("links after second run: %s", n)
	}

	// The series: refresh, Sonarr's monitoring, the file matched by season and number, then released.
	sonarr := e.sonarr()
	sreq := Request{Flavour: "sonarr", URL: "http://sonarr:8989", APIKey: "secret", Library: "shows", Profiles: []int{7},
		Paths: map[string]string{"/tv": e.sonarrDir}}
	e.lib.monitored = nil
	rep, err = Run(e.ctx, e.deps(sonarr), sreq, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.AddedTitle != 1 || rep.Linked != 1 || len(e.series.refresh) != 1 || rep.MissingMonitored != 0 {
		t.Fatalf("series import %+v (refreshes %v)", rep, e.series.refresh)
	}
	if len(e.lib.monitored) != 1 || e.lib.monitored[0] {
		t.Fatalf("the series' copy was monitored while linking: %v", e.lib.monitored)
	}
	sid := e.one(`SELECT id FROM title WHERE tvdb_id = 5`)
	if got := e.one(`SELECT monitored FROM version WHERE title_id = ?`, sid); got != "1" {
		t.Fatalf("series not released: %s", got)
	}
	if got := e.one(`SELECT group_concat(x, ',') FROM (SELECT e.number || ':' || es.monitored AS x FROM
		episode_version_state es JOIN episode e ON e.id = es.episode_id WHERE e.title_id = ? ORDER BY e.number)`,
		sid); got != "1:1,2:0,3:1" {
		t.Fatalf("episode monitoring %s", got)
	}
	if got := e.one(`SELECT e.number FROM link_episode le JOIN episode e ON e.id = le.episode_id`); got != "1" {
		t.Fatalf("linked episode %s", got)
	}
	again, err = Run(e.ctx, e.deps(sonarr), sreq, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again.AddedTitle != 0 || again.Present != 1 || again.Existing != 1 || len(e.series.refresh) != 1 {
		t.Fatalf("second series run %+v", again)
	}
}

// TestRunSonarrNumbering: a series whose episode list is from TMDb gets no monitoring copied and no file linked;
// without a TVDB key the Preview says so and promises no linkable file.
func TestRunSonarrNumbering(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	sonarr := e.sonarr()
	req := Request{Flavour: "sonarr", URL: "http://sonarr:8989", APIKey: "secret", Library: "shows", Profiles: []int{7},
		Paths: map[string]string{"/tv": e.sonarrDir}, DryRun: true}
	d := e.deps(sonarr)
	d.TVDB = false
	rep, err := Run(e.ctx, d, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.TVDBMissing || rep.Linkable != 0 || rep.NotLinked[ReasonNumbering] != 1 {
		t.Fatalf("preview without TVDB %+v", rep)
	}
	e.series.source = "tmdb"
	req.DryRun = false
	if rep, err = Run(e.ctx, d, req, nil); err != nil {
		t.Fatal(err)
	}
	if rep.AddedTitle != 1 || rep.Linked != 0 || rep.NotLinked[ReasonNumbering] != 1 || e.lib.adoptCalls != 0 {
		t.Fatalf("tmdb numbering %+v", rep)
	}
	if got := e.one(`SELECT COUNT(*) FROM episode_version_state WHERE monitored = 0`); got != "0" {
		t.Fatalf("monitoring copied onto TMDb numbering: %s unmonitored", got)
	}
	if got := e.one(`SELECT monitored FROM version`); got != "1" {
		t.Fatalf("series not released: %s", got)
	}

	// A TVDB key added later: the re-run refreshes onto TVDB, brings the monitoring over and links the file.
	e.series.source = "tvdb"
	d.TVDB = true
	if rep, err = Run(e.ctx, d, req, nil); err != nil {
		t.Fatal(err)
	}
	if rep.Present != 1 || rep.Linked != 1 || len(e.series.refresh) != 2 {
		t.Fatalf("re-run with TVDB %+v", rep)
	}
	if got := e.one(`SELECT COUNT(*) FROM episode_version_state WHERE monitored = 0`); got != "1" {
		t.Fatalf("monitoring not brought over on the re-run: %s unmonitored", got)
	}
}

func TestRunRefusals(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	radarr := e.radarr()
	base := Request{Flavour: "radarr", URL: "http://radarr:7878", APIKey: "secret", Library: "movies", Profiles: []int{7}}
	var ref *Refusal
	var in *InputError

	// A library folder inside an instance root folder: refused before any read.
	inside := base
	inside.Paths = map[string]string{"/movies": filepath.Dir(e.movies)}
	if _, err := Run(e.ctx, e.deps(radarr), inside, nil); !errors.As(err, &ref) || len(radarr.gets) != 0 {
		t.Fatalf("overlap: %v, gets %v", err, radarr.gets)
	}
	none := base
	none.Profiles = nil
	if _, err := Run(e.ctx, e.deps(radarr), none, nil); !errors.As(err, &in) {
		t.Fatalf("no profile ticked: %v", err)
	}
	d := e.deps(radarr)
	d.Titles = nil
	if _, err := Run(e.ctx, d, base, nil); !errors.As(err, &ref) || !strings.Contains(ref.Msg, "TMDb") {
		t.Fatalf("no TMDb: %v", err)
	}

	// A Watch-only library: an Import is refused, a Preview reports it.
	e.pol.Versions["movies"].Mode = policy.ModeShadow
	d = e.deps(radarr)
	d.Library = e.newLib()
	if _, err := Run(e.ctx, d, base, nil); !errors.As(err, &ref) || !strings.Contains(ref.Msg, "Watch only") {
		t.Fatalf("watch only: %v", err)
	}
	preview := base
	preview.DryRun = true
	rep, err := Run(e.ctx, d, preview, nil)
	if err != nil || rep.Writable || rep.WriteBlock != "it is Watch only" {
		t.Fatalf("watch-only preview %+v %v", rep, err)
	}
}

// TestRunFolderMapping: the overlap check uses where Theatarr reads an instance folder. An instance folder with the
// same name as the library's own folder is fine when it is mapped elsewhere; a library folder equal to or inside the
// mapped folder, or equal to an unmapped instance folder, is refused.
func TestRunFolderMapping(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	radarr := e.radarr()
	// The instance's root folder has the library's own path (both containers call it the same).
	radarr.answers["rootfolder"] = []map[string]any{{"id": 1, "path": e.movies}}
	base := Request{Flavour: "radarr", URL: "http://radarr:7878", APIKey: "secret", Library: "movies",
		Profiles: []int{7}, DryRun: true}
	var ref *Refusal

	mapped := base
	mapped.Paths = map[string]string{e.movies: e.radarrDir}
	if _, err := Run(e.ctx, e.deps(radarr), mapped, nil); err != nil {
		t.Fatalf("mapped elsewhere: %v", err)
	}
	for _, to := range []string{e.movies, filepath.Dir(e.movies)} {
		bad := base
		bad.Paths = map[string]string{"/old-movies": to}
		if _, err := Run(e.ctx, e.deps(radarr), bad, nil); !errors.As(err, &ref) {
			t.Fatalf("library inside the mapped folder %s: %v", to, err)
		}
	}
	if _, err := Run(e.ctx, e.deps(radarr), base, nil); !errors.As(err, &ref) {
		t.Fatalf("unmapped instance folder equal to the library's: %v", err)
	}
}
