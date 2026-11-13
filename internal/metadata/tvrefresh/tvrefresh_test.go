package tvrefresh

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/metadata/tvdb"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// The TVDB fixtures in testdata were recorded from api4.thetvdb.com on 2026-10-26 for three public series (Severance
// 371980, Slow Horses 372264, Frieren 424536) and trimmed to the fields the client reads (testdata/README.md).

const (
	base    = "https://api4.thetvdb.example/v4"
	testKey = "fixture-key"
)

var now = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

// fakeTVDB answers TVDB v4 requests from fixtures by path (and page). No network.
type fakeTVDB struct {
	t    *testing.T
	mu   sync.Mutex
	fix  map[string][]byte
	reqs []string
	// during, when set, runs (once) on the first fixture request: something writing while the refresh fetches.
	during func()
}

func newFakeTVDB(t *testing.T) *fakeTVDB {
	f := &fakeTVDB{t: t, fix: map[string][]byte{}}
	for _, id := range []string{"371980", "372264", "424536"} {
		f.load("/series/"+id+"/extended", id+"_series.json")
		f.load("/series/"+id+"/episodes/default?page=0", id+"_default_0.json")
		f.load("/series/"+id+"/episodes/absolute?page=0", id+"_absolute_0.json")
	}
	f.load("/series/424536/episodes/default/eng?page=0", "424536_default_eng_0.json")
	return f
}

func (f *fakeTVDB) load(path, file string) {
	b, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		f.t.Fatal(err)
	}
	f.fix[path] = b
}

// edit rewrites the episodes of a listing fixture.
func (f *fakeTVDB) edit(path string, fn func(eps []map[string]any) []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var env map[string]any
	if err := json.Unmarshal(f.fix[path], &env); err != nil {
		f.t.Fatal(err)
	}
	data := env["data"].(map[string]any)
	var eps []map[string]any
	for _, e := range data["episodes"].([]any) {
		eps = append(eps, e.(map[string]any))
	}
	out := make([]any, 0, len(eps))
	for _, e := range fn(eps) {
		out = append(out, e)
	}
	data["episodes"] = out
	b, err := json.Marshal(env)
	if err != nil {
		f.t.Fatal(err)
	}
	f.fix[path] = b
}

func (f *fakeTVDB) Do(_ context.Context, _ gateway.Target, r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	resp := func(code int, body []byte) (*http.Response, error) {
		return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	}
	path := strings.TrimPrefix(r.URL.Path, "/v4")
	if path == "/login" {
		return resp(200, []byte(`{"status":"success","data":{"token":"tok"}}`))
	}
	f.reqs = append(f.reqs, r.URL.RequestURI())
	if f.during != nil {
		f.during()
		f.during = nil
	}
	key := path
	if p := r.URL.Query().Get("page"); p != "" {
		key += "?page=" + p
	}
	if b, ok := f.fix[key]; ok {
		return resp(200, b)
	}
	return resp(404, []byte(`{"status":"failure","message":"NotFoundException"}`))
}

// fakeTMDb serves one hand-written series (id 5000, two seasons).
type fakeTMDb struct {
	calls int
	lang  string // the series' original_language
}

func (f *fakeTMDb) TVShow(_ context.Context, id int) (*tmdb.TVSeries, error) {
	f.calls++
	if id != 5000 {
		return nil, tmdb.ErrNotFound
	}
	var s tmdb.TVSeries
	err := json.Unmarshal([]byte(`{"id": 5000, "name": "Only On TMDb", "status": "Ended", "first_air_date": "2019-03-01",
		"episode_run_time": [30], "origin_country": ["GB"], "seasons": [{"season_number": 0, "episode_count": 0},
		{"season_number": 1, "episode_count": 2}]}`), &s)
	s.OriginalLanguage = f.lang
	return &s, err
}

func (f *fakeTMDb) TVSeasonEpisodes(_ context.Context, id, season int) (*tmdb.TVSeason, error) {
	f.calls++
	rt := 31
	return &tmdb.TVSeason{SeasonNumber: 1, Episodes: []tmdb.TVEpisode{
		{ID: 70001, Name: "First", AirDate: "2019-03-01", EpisodeNumber: 1, SeasonNumber: 1, Runtime: &rt},
		{ID: 70002, Name: "", AirDate: "2019-03-08", EpisodeNumber: 2, SeasonNumber: 1},
	}}, nil
}

func (f *fakeTMDb) FindTVByTVDB(_ context.Context, tvdbID int) (int, error) {
	f.calls++
	if tvdbID == 800800 {
		return 5000, nil
	}
	return 0, tmdb.ErrNotFound
}

// releases records ReleaseWaiting calls; at, when set, runs on each (what the released search would read).
type releases struct {
	ids []int64
	at  func(id int64)
}

func (r *releases) ReleaseWaiting(_ context.Context, id int64) error {
	r.ids = append(r.ids, id)
	if r.at != nil {
		r.at(id)
	}
	return nil
}

type env struct {
	st   *store.Store
	pol  *policy.Policy
	tvdb *fakeTVDB
	tmdb *fakeTMDb
	rel  *releases
	svc  *Service
}

func setup(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(filepath.Join("..", "..", "..", "examples", "policy"))
	if err != nil {
		t.Fatal(err)
	}
	// Scene providers have their own gateway fixtures in augmentation_test.go.
	delete(pol.Metadata.Sources, "thexem")
	delete(pol.Metadata.Sources, "sonarr")
	// A second standard series version, for per-version intent.
	v := *pol.Versions["tv-2160p"]
	pol.Versions["tv-1080p"] = &v
	e := &env{st: st, pol: pol, tvdb: newFakeTVDB(t), tmdb: &fakeTMDb{}, rel: &releases{}}
	c, err := tvdb.New(tvdb.Options{BaseURL: base, APIKey: testKey, Doer: e.tvdb, MinInterval: time.Microsecond})
	if err != nil {
		t.Fatal(err)
	}
	e.svc, err = New(Config{Store: st, Policy: pol, TVDB: c, TMDb: e.tmdb, Searches: e.rel,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// add puts a series in through catalog.Add.
func (e *env) add(t *testing.T, in catalog.Input) int64 {
	t.Helper()
	in.Kind = policy.KindSeries
	if in.Versions == nil {
		in.Versions = func(bool) ([]string, error) { return []string{"tv-2160p"}, nil }
	}
	in.Monitored = true
	var id int64
	err := e.st.WriteTx(context.Background(), func(tx *sql.Tx) error {
		res, err := catalog.Add(context.Background(), tx, in, now, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err == nil {
			id = res.TitleID
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *env) refresh(t *testing.T, id int64) *Result {
	t.Helper()
	r, err := e.svc.RefreshTitle(context.Background(), id, false)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

type epRow struct {
	id       int64
	name     string
	air      string
	utc      sql.NullInt64
	ext      sql.NullInt64
	removed  bool
	runtime  sql.NullInt64
	monitor  bool
	absolute sql.NullInt64
}

// ep reads the episode at a slot.
func (e *env) ep(t *testing.T, titleID int64, season, number int) *epRow {
	t.Helper()
	var r epRow
	var air sql.NullString
	err := e.st.Reader().QueryRow(`SELECT id, name, air_date, air_date_utc_at, tvdb_episode_id, removed_at IS NOT NULL,
		runtime_minutes, monitored, (SELECT number FROM episode_number n WHERE n.episode_id = e.id AND
		n.scheme = 'absolute_tvdb') FROM episode e WHERE title_id = ? AND season = ? AND number = ?`, titleID, season,
		number).Scan(&r.id, &r.name, &air, &r.utc, &r.ext, &r.removed, &r.runtime, &r.monitor, &r.absolute)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	r.air = air.String
	return &r
}

// state is a version's state row of an episode: monitored, or -1 for no row.
func (e *env) state(t *testing.T, titleID int64, key string, episodeID int64) int {
	t.Helper()
	var m int
	err := e.st.Reader().QueryRow(`SELECT s.monitored FROM episode_version_state s JOIN version v ON v.id = s.version_id
		WHERE v.title_id = ? AND v.policy_key = ? AND s.episode_id = ?`, titleID, key, episodeID).Scan(&m)
	if errors.Is(err, sql.ErrNoRows) {
		return -1
	}
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (e *env) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.st.Reader().QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func utc(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return store.Millis(t)
}

func TestNewSeriesFromTVDB(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	id := e.add(t, catalog.Input{TvdbID: 371980, Seasons: []catalog.Season{{Number: 1, Monitored: true},
		{Number: 2, Monitored: false}}})
	var added sql.NullInt64
	if err := e.st.Reader().QueryRow(`SELECT added_at FROM series_meta WHERE title_id = ?`, id).Scan(&added); err != nil ||
		!added.Valid {
		t.Fatalf("add did not mark the series pending: %v %v", added, err)
	}
	r := e.refresh(t, id)
	if r.Source != SourceTVDB || r.Added != 40 || r.Episodes != 40 || r.NotFound {
		t.Fatalf("result %+v", r)
	}
	// The placeholder name and the missing year and runtime come from TVDB.
	var name string
	var year, runtime int
	var tmdbID sql.NullInt64
	if err := e.st.Reader().QueryRow(`SELECT name, year, runtime_minutes, tmdb_id FROM title WHERE id = ?`, id).
		Scan(&name, &year, &runtime, &tmdbID); err != nil {
		t.Fatal(err)
	}
	if name != "Severance" || year != 2022 || runtime != 49 || tmdbID.Int64 != 95396 {
		t.Errorf("title %q %d %d %v", name, year, runtime, tmdbID)
	}
	e1 := e.ep(t, id, 1, 1)
	// 21:00 in New York on 2022-02-18 (EST) is 02:00 UTC the next day; E02 aired the same day, one runtime later.
	if e1 == nil || e1.name != "Good News About Hell" || e1.air != "2022-02-18" || e1.utc.Int64 != utc("2022-02-19T02:00:00Z") ||
		e1.ext.Int64 != 8891221 || e1.runtime.Int64 != 57 || !e1.monitor {
		t.Fatalf("S01E01 %+v", e1)
	}
	if e2 := e.ep(t, id, 1, 2); e2.utc.Int64 != utc("2022-02-19T02:49:00Z") {
		t.Errorf("S01E02 air %v", time.UnixMilli(e2.utc.Int64).UTC())
	}
	// Season choices: 1 monitored, 2 not; specials (a new season 0) never.
	s2 := e.ep(t, id, 2, 1)
	s0 := e.ep(t, id, 0, 11)
	if e.state(t, id, "tv-2160p", e1.id) != 1 || e.state(t, id, "tv-2160p", s2.id) != 0 || e.state(t, id, "tv-2160p", s0.id) != 0 {
		t.Errorf("states S1 %d S2 %d S0 %d", e.state(t, id, "tv-2160p", e1.id), e.state(t, id, "tv-2160p", s2.id),
			e.state(t, id, "tv-2160p", s0.id))
	}
	if s2.monitor || s0.monitor {
		t.Error("episodes no version monitors are monitored")
	}
	var src, status, airTime, zone string
	var refreshed sql.NullInt64
	if err := e.st.Reader().QueryRow(`SELECT source, status, air_time, time_zone, refreshed_at, added_at FROM series_meta
		WHERE title_id = ?`, id).Scan(&src, &status, &airTime, &zone, &refreshed, &added); err != nil {
		t.Fatal(err)
	}
	if src != "tvdb" || status != "Continuing" || airTime != "21:00" || zone != "America/New_York" || !refreshed.Valid ||
		added.Valid {
		t.Errorf("series_meta %s %s %s %s %v %v", src, status, airTime, zone, refreshed, added)
	}
	if len(e.rel.ids) != 1 || e.rel.ids[0] != id {
		t.Errorf("held searches not released: %v", e.rel.ids)
	}
	var vid int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM version WHERE title_id = ?`, id).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	m, err := catalog.LoadMonitor(ctx, e.st.Reader(), vid)
	if err != nil || m.New != "all" || len(m.Seasons) != 3 || !m.Seasons[1] || m.Seasons[2] || m.Seasons[0] {
		t.Errorf("intent %+v %v", m, err)
	}
	// A second refresh with nothing new changes nothing.
	r = e.refresh(t, id)
	if r.Added+r.Updated+r.Moved+r.Deleted+r.Removed+r.States != 0 {
		t.Errorf("second refresh %+v", r)
	}
	if len(e.rel.ids) != 1 {
		t.Errorf("released again: %v", e.rel.ids)
	}
}

func TestNewEpisodesFollowIntent(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	path := "/series/372264/episodes/default?page=0"
	full := e.tvdb.fix[path]
	// Before season 6 was announced.
	e.tvdb.edit(path, func(eps []map[string]any) []map[string]any {
		var out []map[string]any
		for _, x := range eps {
			if x["seasonNumber"].(float64) != 6 {
				out = append(out, x)
			}
		}
		return out
	})
	id := e.add(t, catalog.Input{TvdbID: 372264, Title: "Slow Horses",
		Versions: func(bool) ([]string, error) { return []string{"tv-2160p", "tv-1080p"}, nil }})
	if err := e.st.WriteTx(ctx, func(tx *sql.Tx) error {
		return catalog.SetMonitorNew(ctx, tx, id, []string{"tv-1080p"}, catalog.MonitorNone, now)
	}); err != nil {
		t.Fatal(err)
	}
	e.refresh(t, id)
	if e.ep(t, id, 6, 1) != nil {
		t.Fatal("season 6 before it exists")
	}
	if _, err := e.st.Writer().Exec(`UPDATE version SET state = 'cutoff_met'`); err != nil {
		t.Fatal(err)
	}
	e.tvdb.fix[path] = full
	r := e.refresh(t, id)
	if r.Added != 6 {
		t.Fatalf("result %+v", r)
	}
	e1 := e.ep(t, id, 6, 1)
	// 2026-09-16 at 00:00 New York (EDT) is 04:00 UTC.
	if e1 == nil || e1.utc.Int64 != utc("2026-09-16T04:00:00Z") || !e1.monitor {
		t.Fatalf("S06E01 %+v", e1)
	}
	// monitor new items: all in tv-2160p, none in tv-1080p.
	if e.state(t, id, "tv-2160p", e1.id) != 1 || e.state(t, id, "tv-1080p", e1.id) != 0 {
		t.Errorf("states %d %d", e.state(t, id, "tv-2160p", e1.id), e.state(t, id, "tv-1080p", e1.id))
	}
	var st2160, st1080 string
	if err := e.st.Reader().QueryRow(`SELECT (SELECT state FROM version WHERE policy_key = 'tv-2160p'),
		(SELECT state FROM version WHERE policy_key = 'tv-1080p')`).Scan(&st2160, &st1080); err != nil {
		t.Fatal(err)
	}
	if st2160 != "wanted" || st1080 != "cutoff_met" {
		t.Errorf("version states %s %s", st2160, st1080)
	}
}

func TestRenumberAndRemoval(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	id := e.add(t, catalog.Input{TvdbID: 371980, Title: "Severance"})
	e.refresh(t, id)
	e9, e8, s2e9, s2e10 := e.ep(t, id, 1, 9), e.ep(t, id, 1, 8), e.ep(t, id, 2, 9), e.ep(t, id, 2, 10)
	sp := e.ep(t, id, 0, 32)
	// A file on S01E09 and S02E10, a link on S02E09.
	var vid int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM version WHERE title_id = ?`, id).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	if err := e.st.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO version_file (instance, arr_file_id, version_id, title_id, path, relative_path)
			VALUES ('theatarr', 1, ?, ?, '/x/a.mkv', 'a.mkv')`, vid, id)
		if err != nil {
			return err
		}
		vf, _ := res.LastInsertId()
		if _, err := tx.Exec(`INSERT INTO version_file_episode VALUES (?, ?), (?, ?)`, vf, e9.id, vf, s2e10.id); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO link (path, target, title_id, created_at, updated_at) VALUES ('/l/b.mkv', '/m/b.mkv', ?, 0, 0)`,
			id); err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (last_insert_rowid(), ?)`, s2e9.id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// TVDB: S01E09 becomes S01E10 and a new episode takes its place; S01E08 is a new record in the same place;
	// S02E09 is gone and S02E08 takes its number; S02E10 and the special 32 are gone.
	e.tvdb.edit("/series/371980/episodes/default?page=0", func(eps []map[string]any) []map[string]any {
		var out []map[string]any
		for _, x := range eps {
			switch int(x["id"].(float64)) {
			case 8891229:
				x["number"] = 10
			case 8891228:
				x["id"] = 990001
				x["name"] = "Replacement"
			case 10592765:
				x["number"] = 9
			case 10592768, 11046980, 10592766:
				continue
			}
			out = append(out, x)
		}
		return append(out, map[string]any{"id": 990002, "seriesId": 371980, "name": "Inserted", "aired": "2022-04-08",
			"runtime": 40, "seasonNumber": 1, "number": 9})
	})
	r := e.refresh(t, id)
	if r.Moved != 0 || r.Added != 1 || r.Deleted != 2 || r.Removed != 1 {
		t.Fatalf("result %+v", r)
	}
	// Slot identity wins over TVDB identity: the file stays on S01E09.
	if x := e.ep(t, id, 1, 10); x == nil || x.id == e9.id || x.ext.Int64 != 8891229 {
		t.Errorf("S01E10 %+v (was row %d)", x, e9.id)
	}
	if x := e.ep(t, id, 1, 9); x == nil || x.id != e9.id || x.name != "Inserted" || x.ext.Int64 != 990002 {
		t.Errorf("S01E09 %+v", x)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM version_file_episode WHERE episode_id = ?`, e9.id); n != 1 {
		t.Errorf("the file left the renumbered episode")
	}
	// A recreated TVDB record keeps the existing row.
	if x := e.ep(t, id, 1, 8); x == nil || x.id != e8.id || x.name != "Replacement" || x.ext.Int64 != 990001 {
		t.Errorf("S01E08 %+v", x)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM episode WHERE id = ?`, e8.id); n != 1 {
		t.Error("the replaced S01E08 row was lost")
	}
	// The link on S02E09 stays with its slot and takes the refreshed TVDB id.
	if x := e.ep(t, id, 2, 9); x == nil || x.id != s2e9.id || x.ext.Int64 != 10592765 {
		t.Errorf("S02E09 %+v", x)
	}
	if x := e.ep(t, id, 2, int(-s2e9.id)); x != nil {
		t.Errorf("old S02E09 %+v", x)
	}
	// S02E10 has a file: kept where it is, marked removed. The special had nothing: deleted.
	if x := e.ep(t, id, 2, 10); x == nil || x.id != s2e10.id || !x.removed {
		t.Errorf("S02E10 %+v", x)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM episode WHERE id = ?`, sp.id); n != 0 {
		t.Error("the special is still there")
	}
	// TVDB brings S02E10 back: no longer removed.
	e.tvdb.edit("/series/371980/episodes/default?page=0", func(eps []map[string]any) []map[string]any {
		return append(eps, map[string]any{"id": 10592768, "seriesId": 371980, "name": "Cold Harbor",
			"aired": "2025-03-20", "runtime": 76, "seasonNumber": 2, "number": 10})
	})
	e.refresh(t, id)
	if x := e.ep(t, id, 2, 10); x == nil || x.id != s2e10.id || x.removed {
		t.Errorf("S02E10 back %+v", x)
	}
}

// TestSlotReplacementKeepsIdentity: TVDB drops an episode and puts a new record at its number. The old row, with a
// link, keeps its id and link and takes the new TVDB id.
func TestSlotReplacementKeepsIdentity(t *testing.T) {
	t.Parallel()
	e := setup(t)
	id := e.add(t, catalog.Input{TvdbID: 371980, Title: "Severance"})
	e.refresh(t, id)
	e8 := e.ep(t, id, 1, 8)
	if _, err := e.st.Writer().Exec(`INSERT INTO link (path, target, title_id, created_at, updated_at)
		VALUES ('/l/e8.mkv', '/m/e8.mkv', ?, 0, 0)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer().Exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (last_insert_rowid(), ?)`,
		e8.id); err != nil {
		t.Fatal(err)
	}
	e.tvdb.edit("/series/371980/episodes/default?page=0", func(eps []map[string]any) []map[string]any {
		for _, x := range eps {
			if int(x["id"].(float64)) == 8891228 {
				x["id"], x["name"] = 990001, "Replacement"
			}
		}
		return eps
	})
	r := e.refresh(t, id)
	if r.Added != 0 || r.Removed != 0 {
		t.Fatalf("result %+v", r)
	}
	x := e.ep(t, id, 1, 8)
	if x == nil || x.id != e8.id || x.name != "Replacement" || x.ext.Int64 != 990001 {
		t.Errorf("new S01E08 %+v (old row %d)", x, e8.id)
	}
	old := e.ep(t, id, 1, int(-e8.id))
	if old != nil {
		t.Errorf("old S01E08 %+v", old)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM link_episode WHERE episode_id = ?`, e8.id); n != 1 {
		t.Error("the link left the old episode")
	}
}

// TestManualFileMappingKeepsEpisode: an episode a pending manual import names only in its file mappings
// (download_file.episodes, the download's own list empty) is kept as removed, not deleted, when TVDB drops it.
func TestManualFileMappingKeepsEpisode(t *testing.T) {
	t.Parallel()
	e := setup(t)
	id := e.add(t, catalog.Input{TvdbID: 371980, Title: "Severance"})
	e.refresh(t, id)
	e5 := e.ep(t, id, 1, 5)
	var vid int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM version WHERE title_id = ?`, id).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	res, err := e.st.Writer().Exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title,
		indexer, state, episodes, created_at, updated_at) VALUES (?, ?, 'tv-2160p', 'abc', 'Severance.S01', 'manual',
		'ready', '[]', 0, 0)`, id, vid)
	if err != nil {
		t.Fatal(err)
	}
	did, _ := res.LastInsertId()
	if _, err := e.st.Writer().Exec(`INSERT INTO download_file (download_id, rel, episodes, source, updated_at)
		VALUES (?, 'a.mkv', json_array(?), 'manual', 0)`, did, e5.id); err != nil {
		t.Fatal(err)
	}
	e.tvdb.edit("/series/371980/episodes/default?page=0", func(eps []map[string]any) []map[string]any {
		var out []map[string]any
		for _, x := range eps {
			if int(x["id"].(float64)) != int(e5.ext.Int64) {
				out = append(out, x)
			}
		}
		return out
	})
	r := e.refresh(t, id)
	if r.Deleted != 0 || r.Removed != 1 {
		t.Fatalf("result %+v", r)
	}
	if x := e.ep(t, id, 1, 5); x == nil || x.id != e5.id || !x.removed {
		t.Errorf("S01E05 %+v", x)
	}
}

func TestAnimeAbsoluteAndNames(t *testing.T) {
	t.Parallel()
	e := setup(t)
	anime := true
	id := e.add(t, catalog.Input{TvdbID: 424536, Title: "Frieren", Anime: &anime,
		Versions: func(bool) ([]string, error) { return []string{"anime-tv"}, nil }})
	r := e.refresh(t, id)
	if r.Episodes == 0 {
		t.Fatalf("result %+v", r)
	}
	e1 := e.ep(t, id, 1, 1)
	// English names; 23:00 in Tokyo is 14:00 UTC; four episodes on one day are not spread (more than three).
	if e1.name != "The Journey's End" || e1.utc.Int64 != utc("2023-09-29T14:00:00Z") || e1.absolute.Int64 != 1 {
		t.Errorf("S01E01 %+v", e1)
	}
	if e4 := e.ep(t, id, 1, 4); e4.utc.Int64 != e1.utc.Int64 {
		t.Errorf("S01E04 air %v", e4.utc)
	}
	if e := e.ep(t, id, 2, 1); e == nil || e.absolute.Int64 != 29 {
		t.Errorf("S02E01 %+v", e)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM episode_number WHERE title_id = ? AND scheme = 'absolute_tvdb'
		AND source = 'tvdb'`, id); n < 28 {
		t.Errorf("%d absolute numbers", n)
	}
}

func TestTMDbFallback(t *testing.T) {
	t.Parallel()
	e := setup(t)
	id := e.add(t, catalog.Input{TvdbID: 800800, Title: "Only On TMDb"})
	r := e.refresh(t, id)
	if r.Source != SourceTMDb || r.Added != 2 {
		t.Fatalf("result %+v", r)
	}
	var ext sql.NullInt64
	if err := e.st.Reader().QueryRow(`SELECT tmdb_episode_id FROM episode WHERE title_id = ? AND season = 1 AND number = 1`,
		id).Scan(&ext); err != nil || ext.Int64 != 70001 {
		t.Errorf("tmdb id %v %v", ext, err)
	}
	// Midnight in London (GMT in March); the missing name is TBA.
	if x := e.ep(t, id, 1, 1); x.utc.Int64 != utc("2019-03-01T00:00:00Z") || x.runtime.Int64 != 31 {
		t.Errorf("S01E01 %+v", x)
	}
	if x := e.ep(t, id, 1, 2); x.name != "TBA" {
		t.Errorf("S01E02 %+v", x)
	}
	// An anime series is never taken from TMDb.
	anime := true
	calls := e.tmdb.calls
	aid := e.add(t, catalog.Input{TvdbID: 800801, Title: "Anime Not On TVDB", Anime: &anime,
		Versions: func(bool) ([]string, error) { return []string{"anime-tv"}, nil }})
	r = e.refresh(t, aid)
	if !r.NotFound || e.tmdb.calls != calls {
		t.Errorf("anime %+v, %d TMDb calls", r, e.tmdb.calls-calls)
	}
	var added sql.NullInt64
	var msg string
	if err := e.st.Reader().QueryRow(`SELECT added_at, error FROM series_meta WHERE title_id = ?`, aid).Scan(&added,
		&msg); err != nil || added.Valid || !strings.Contains(msg, "anime") {
		t.Errorf("not found recorded %v %q %v", added, msg, err)
	}
	if len(e.rel.ids) != 2 {
		t.Errorf("held searches of a series no source has are released too: %v", e.rel.ids)
	}
}

// TestUnmonitorReadded: a series that had no episodes and was not just added gets its long-aired episodes
// unmonitored (Sonarr's UnmonitorReaddedEpisodes); a fresh add keeps its choices.
func TestUnmonitorReadded(t *testing.T) {
	t.Parallel()
	e := setup(t)
	e.tvdb.edit("/series/372264/episodes/default?page=0", func(eps []map[string]any) []map[string]any {
		return append(eps, map[string]any{"id": 990004, "name": "Next", "aired": "2026-10-28", "seasonNumber": 6,
			"number": 7})
	})
	id := e.add(t, catalog.Input{TvdbID: 372264, Title: "Slow Horses"})
	if _, err := e.st.Writer().Exec(`UPDATE series_meta SET added_at = NULL`); err != nil {
		t.Fatal(err)
	}
	e.refresh(t, id)
	if x := e.ep(t, id, 1, 1); e.state(t, id, "tv-2160p", x.id) != 0 {
		t.Error("an old episode came in monitored")
	}
	if x := e.ep(t, id, 6, 7); x == nil || e.state(t, id, "tv-2160p", x.id) != 1 {
		t.Error("an episode yet to air came in unmonitored")
	}
	// A fresh add keeps its choices.
	e2 := setup(t)
	id = e2.add(t, catalog.Input{TvdbID: 372264, Title: "Slow Horses"})
	e2.refresh(t, id)
	if x := e2.ep(t, id, 1, 1); e2.state(t, id, "tv-2160p", x.id) != 1 {
		t.Error("an added series' old episode came in unmonitored")
	}
}

func TestForceReadsPastTheCache(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := setup(t)
	c, err := tvdb.New(tvdb.Options{BaseURL: base, APIKey: testKey, Doer: e.tvdb, Store: e.st, MinInterval: time.Microsecond})
	if err != nil {
		t.Fatal(err)
	}
	e.svc.tvdb = c
	id := e.add(t, catalog.Input{TvdbID: 371980, Title: "Severance"})
	e.refresh(t, id)
	n := len(e.tvdb.reqs)
	e.tvdb.edit("/series/371980/episodes/default?page=0", func(eps []map[string]any) []map[string]any {
		return append(eps, map[string]any{"id": 990003, "name": "New", "aired": "2026-11-01", "seasonNumber": 3, "number": 1})
	})
	if r := e.refresh(t, id); r.Added != 0 || len(e.tvdb.reqs) != n {
		t.Fatalf("a cached refresh asked TVDB: %+v", r)
	}
	r, err := e.svc.RefreshTitle(ctx, id, true)
	if err != nil || r.Added != 1 {
		t.Fatalf("forced %+v %v", r, err)
	}
}

func TestDue(t *testing.T) {
	t.Parallel()
	ms := func(d time.Duration) sql.NullInt64 {
		return sql.NullInt64{Int64: store.Millis(now.Add(-d)), Valid: true}
	}
	none := sql.NullInt64{}
	for _, c := range []struct {
		name string
		c    candidate
		want bool
	}{
		{"never", candidate{}, true},
		{"never, failed an hour ago", candidate{attempted: ms(time.Hour)}, false},
		{"never, failed 7h ago", candidate{attempted: ms(7 * time.Hour)}, true},
		{"continuing 13h", candidate{status: StatusContinuing, refreshed: ms(13 * time.Hour)}, true},
		{"continuing 11h", candidate{status: StatusContinuing, refreshed: ms(11 * time.Hour)}, false},
		{"ended 13h", candidate{status: StatusEnded, refreshed: ms(13 * time.Hour)}, false},
		{"ended 31d", candidate{status: StatusEnded, refreshed: ms(31 * 24 * time.Hour)}, true},
		{"ended, aired 10d ago", candidate{status: StatusEnded, refreshed: ms(13 * time.Hour), lastAir: ms(240 * time.Hour)}, true},
		{"ended, TBA 7h", candidate{status: StatusEnded, refreshed: ms(7 * time.Hour), tba: true}, true},
		{"ended, TBA 5h", candidate{status: StatusEnded, refreshed: ms(5 * time.Hour), tba: true}, false},
		{"failed after a good one", candidate{status: StatusContinuing, refreshed: ms(20 * time.Hour), attempted: ms(time.Hour)}, false},
		{"upcoming 13h", candidate{status: StatusUpcoming, refreshed: ms(13 * time.Hour), lastAir: none}, true},
	} {
		if got := c.c.due(now); got != c.want {
			t.Errorf("%s: due %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSweep(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.add(t, catalog.Input{TvdbID: 371980, Title: "Severance"})
	b := e.add(t, catalog.Input{TvdbID: 800801, Title: "Nowhere"})
	sum, err := e.svc.Sweep(ctx)
	if err != nil || sum.Due != 2 || sum.Refreshed != 1 || sum.NotFound != 1 {
		t.Fatalf("sweep %+v %v", sum, err)
	}
	// Both were tried: nothing is due now.
	if sum, err = e.svc.Sweep(ctx); err != nil || sum.Due != 0 {
		t.Fatalf("second sweep %+v %v", sum, err)
	}
	_, _ = a, b
}

// TestSeriesOriginalLanguageAtRelease: the searches an add held are scored with the series' original language from
// the refresh that releases them (TVDB's 3-letter code, else TMDb's), before meta.art has written title_meta. TMDb's
// title_meta language still wins once there, an arr's stored name wins over both, and an empty answer keeps the
// language stored earlier.
func TestSeriesOriginalLanguageAtRelease(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	load := func(id int64) decide.Title {
		t.Helper()
		ti, err := decide.LoadTitle(ctx, e.st.Reader(), id)
		if err != nil {
			t.Fatal(err)
		}
		return ti
	}
	var atRelease decide.Title
	e.rel.at = func(id int64) { atRelease = load(id) }
	anime := true
	id := e.add(t, catalog.Input{TvdbID: 424536, Title: "Frieren", Anime: &anime,
		Versions: func(bool) ([]string, error) { return []string{"anime-tv"}, nil }})
	e.refresh(t, id)
	if atRelease.OriginalLanguage != "Japanese" || !atRelease.OriginalLanguageResolved {
		t.Errorf("TVDB jpn at release: %q resolved %v", atRelease.OriginalLanguage, atRelease.OriginalLanguageResolved)
	}
	// TMDb's code in title_meta (meta.art) wins.
	if _, err := e.st.Writer().Exec(`INSERT INTO title_meta (title_id, original_language, attempted_at, refresh_after)
		VALUES (?, 'ko', 0, 0)`, id); err != nil {
		t.Fatal(err)
	}
	if ti := load(id); ti.OriginalLanguage != "Korean" || !ti.OriginalLanguageResolved {
		t.Errorf("title_meta ko: %q resolved %v", ti.OriginalLanguage, ti.OriginalLanguageResolved)
	}
	// An arr's stored name wins over both, as before.
	if _, err := e.st.Writer().Exec(`UPDATE title SET original_language = 'French' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if ti := load(id); ti.OriginalLanguage != "French" || ti.OriginalLanguageResolved {
		t.Errorf("arr name: %q resolved %v", ti.OriginalLanguage, ti.OriginalLanguageResolved)
	}

	// TMDb fallback: its 2-letter code.
	e.tmdb.lang = "ko"
	tid := e.add(t, catalog.Input{TvdbID: 800800, Title: "Only On TMDb"})
	e.refresh(t, tid)
	if atRelease.OriginalLanguage != "Korean" {
		t.Errorf("TMDb ko at release: %q", atRelease.OriginalLanguage)
	}
	// A later answer without a language keeps the stored one.
	e.tmdb.lang = ""
	if _, err := e.svc.RefreshTitle(ctx, tid, true); err != nil {
		t.Fatal(err)
	}
	if ti := load(tid); ti.OriginalLanguage != "Korean" {
		t.Errorf("empty answer: %q", ti.OriginalLanguage)
	}
}
