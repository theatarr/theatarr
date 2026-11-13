package tvrefresh

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestPeriodicSceneRefresh(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	id := e.add(t, catalog.Input{TvdbID: 371980, Title: "Severance"})
	e.refresh(t, id)
	fate := e.add(t, catalog.Input{TvdbID: 79151, Title: "Fate/Zero"})
	clock := now
	e.svc.now = func() time.Time { return clock }
	alias, sceneEpisode, fail, empty := "Season Alias", 11, false, false
	emptyIDs := false
	calls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		if r.Header.Get("Authorization") != "" {
			t.Error("TVDB credentials sent to scene provider")
		}
		if fail {
			http.Error(w, "down", 500)
			return
		}
		switch r.URL.Path {
		case "/v1/scenemapping":
			if empty {
				fmt.Fprint(w, `[]`)
				return
			}
			fmt.Fprintf(w, `[{"tvdbId":371980,"title":%q,"searchTitle":%q,"season":1,"sceneSeasonNumber":2,"sceneOrigin":"mixed"}]`, alias, alias)
		case "/map/allNames":
			if r.URL.Query().Get("seasonNumbers") != "true" || r.URL.Query().Get("origin") != "tvdb" {
				t.Error("bad XEM names query")
			}
			fmt.Fprint(w, `{"result":"success","data":{"371980":[{"XEM Seasonal":"2"},{"XEM All":-1}],"79151":[{"Ignored":2},{"Fate Alias":1}]}}`)
		case "/map/havemap":
			if emptyIDs {
				fmt.Fprint(w, `{"result":"success","data":[]}`)
				return
			}
			fmt.Fprint(w, `{"result":"success","data":["371980"]}`)
		case "/map/all":
			if empty {
				fmt.Fprint(w, `{"result":"success","data":[]}`)
				return
			}
			fmt.Fprintf(w, `{"result":"success","data":[{"tvdb":{"season":1,"episode":1},"scene":{"season":2,"episode":%d,"absolute":21}},{"tvdb":{"season":0,"episode":1},"scene":null}]}`, sceneEpisode)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	e.pol.Metadata.Sources = map[string]*policy.MetadataSource{
		"thexem": {URL: srv.URL, Budget: "scene-test"}, "sonarr": {URL: srv.URL + "/v1", Budget: "scene-test"},
	}
	e.pol.Budgets["scene-test"] = &policy.Budget{Requests: 1000, Per: policy.Duration(time.Second), Burst: 1000}
	gw, err := gateway.New(ctx, e.pol, gateway.Options{Store: e.st, Now: e.svc.now})
	if err != nil {
		t.Fatal(err)
	}
	e.svc.sceneDoer = gw
	refresh := func() {
		t.Helper()
		if err := e.svc.RefreshScene(ctx); err != nil {
			t.Fatal(err)
		}
	}
	refresh()
	if e.count(t, `SELECT COUNT(*) FROM title_scene_mapping WHERE title_id=? AND name='Ignored'`, fate) != 0 ||
		e.count(t, `SELECT COUNT(*) FROM title_scene_mapping WHERE title_id=? AND name='Fate Alias' AND scene_season_number=1`, fate) != 1 {
		t.Fatal("Fate/Zero XEM season exception")
	}
	if e.count(t, `SELECT COUNT(*) FROM title_scene_mapping WHERE title_id = ? AND source = 'tvrefresh:sonarr' AND season_number = 1 AND scene_season_number = 2`, id) != 1 {
		t.Fatal("season-specific Sonarr name missing")
	}
	if e.count(t, `SELECT COUNT(*) FROM title_scene_mapping WHERE title_id = ? AND source = 'tvrefresh:thexem' AND scene_season_number IN (2,-1)`, id) != 2 {
		t.Fatal("XEM names missing")
	}
	check := func(want int) {
		t.Helper()
		if e.count(t, `SELECT COUNT(*) FROM episode_number n JOIN episode e ON e.id=n.episode_id WHERE e.title_id=? AND e.season=1 AND e.number=1 AND n.scheme='scene' AND n.season=2 AND n.number=?`, id, want) != 1 {
			t.Fatal("wrong scene number")
		}
	}
	check(11)
	// Sonarr extrapolates S01E02, then shifts the next TVDB season when scene seasons differ.
	if e.count(t, `SELECT COUNT(*) FROM episode_number n JOIN episode e ON e.id=n.episode_id WHERE e.title_id=? AND e.season=1 AND e.number=2 AND n.scheme='scene' AND n.number=12`, id) != 1 {
		t.Fatal("missing extrapolation")
	}
	ti, err := decide.LoadTitle(ctx, e.st.Reader(), id)
	if err != nil {
		t.Fatal(err)
	}
	var vid int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM version WHERE title_id=?`, id).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	if err := decide.LoadSeriesVersion(ctx, e.st.Reader(), &ti, vid); err != nil {
		t.Fatal(err)
	}
	if !ti.UseSceneNumbering || len(ti.SceneMappings) < 3 {
		t.Fatalf("decide did not load mappings: %+v", ti)
	}
	alias, sceneEpisode = "Corrected Alias", 31
	refresh()
	if calls["/v1/scenemapping"] != 1 || calls["/map/all"] != 1 {
		t.Fatal("three-hour response cache ignored")
	}
	clock = clock.Add(sceneAge)
	emptyIDs = true
	refresh()
	check(31)
	if e.count(t, `SELECT COUNT(*) FROM scene_refresh WHERE source='thexem/havemap?origin=tvdb' AND CAST(data AS TEXT) LIKE '%371980%'`) != 1 {
		t.Fatal("empty XEM id list replaced previous ids")
	}
	if e.count(t, `SELECT COUNT(*) FROM title_scene_mapping WHERE title_id=? AND name='Season Alias'`, id) != 0 {
		t.Fatal("old alias retained")
	}
	// A failed read retains both aliases and numbers and retries without advancing freshness.
	clock = clock.Add(sceneAge)
	fail = true
	if err := e.svc.RefreshScene(ctx); err == nil {
		t.Fatal("failed providers succeeded")
	}
	check(31)
	if e.count(t, `SELECT COUNT(*) FROM title_scene_mapping WHERE title_id=? AND name='Corrected Alias'`, id) != 1 {
		t.Fatal("failed refresh lost alias")
	}
	fail = false
	empty = true
	refresh()
	if e.count(t, `SELECT COUNT(*) FROM title_scene_mapping WHERE title_id=? AND name='Corrected Alias'`, id) != 1 {
		t.Fatal("empty provider list lost alias")
	}
	if e.count(t, `SELECT COUNT(*) FROM episode_number WHERE title_id=? AND scheme='scene'`, id) != 0 {
		t.Fatal("empty XEM numbering did not clear prior mappings")
	}
	// Imported scene numbers are replaced by the next refresh; imported names are never deleted.
	empty = false
	clock = clock.Add(sceneAge)
	if _, err := e.st.Writer().Exec(`INSERT INTO episode_number(episode_id,title_id,scheme,season,number,source) SELECT id,title_id,'scene',9,99,'sonarr_4k_debrid' FROM episode WHERE title_id=? AND season=1 AND number=1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer().Exec(`INSERT INTO title_scene_mapping(title_id,source,name) VALUES(?,'sonarr_4k_debrid','Imported Alias')`, id); err != nil {
		t.Fatal(err)
	}
	refresh()
	check(31)
	if e.count(t, `SELECT COUNT(*) FROM episode_number WHERE title_id=? AND source='sonarr_4k_debrid'`, id) != 0 {
		t.Fatal("stale imported numbers")
	}
	if e.count(t, `SELECT COUNT(*) FROM title_scene_mapping WHERE title_id=? AND name='Imported Alias'`, id) != 1 {
		t.Fatal("imported alias deleted")
	}
}

func TestFirstRefreshMonitor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode string
		want int
	}{
		{"all", 19}, {"future", 0}, {"pilot", 1}, {"recent", 0}, {"missing", 19}, {"existing", 0}, {"firstSeason", 9}, {"lastSeason", 10}, {"none", 0},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			e := setup(t)
			id := e.add(t, catalog.Input{TvdbID: 371980, Monitor: tc.mode, SeriesType: "daily"})
			if e.count(t, `SELECT COUNT(*) FROM series_monitor m JOIN version v ON v.id=m.version_id WHERE v.title_id=? AND pending_monitor=?`, id, tc.mode) != 1 {
				t.Fatal("add mode not persisted")
			}
			e.refresh(t, id)
			if n := e.count(t, `SELECT COUNT(*) FROM episode_version_state s JOIN episode e ON e.id=s.episode_id WHERE e.title_id=? AND s.monitored=1`, id); n != tc.want {
				t.Fatalf("monitored %d, want %d", n, tc.want)
			}
			if e.count(t, `SELECT COUNT(*) FROM series_monitor m JOIN version v ON v.id=m.version_id WHERE v.title_id=? AND pending_monitor<>''`, id) != 0 {
				t.Fatal("mode not consumed")
			}
			// User changes after first refresh survive subsequent refreshes.
			if _, err := e.st.Writer().Exec(`UPDATE episode_version_state SET monitored=1 WHERE episode_id IN (SELECT id FROM episode WHERE title_id=? AND season=1 AND number=2)`, id); err != nil {
				t.Fatal(err)
			}
			e.refresh(t, id)
			if e.state(t, id, "tv-2160p", e.ep(t, id, 1, 2).id) != 1 {
				t.Fatal("add mode reapplied")
			}
		})
	}
}

// TestLaterSeasonsOffKeepsFirstSeasons: the add drawer's "monitor seasons that appear later" off sends monitor all
// with monitor_new none, so "all" is held for the first refresh and the seasons it finds are monitored; monitor new
// none then governs a season that appears afterwards.
func TestLaterSeasonsOffKeepsFirstSeasons(t *testing.T) {
	t.Parallel()
	e := setup(t)
	id := e.add(t, catalog.Input{TvdbID: 371980, MonitorNew: catalog.MonitorNone, Monitor: catalog.MonitorTypeAll})
	monitored := func() int {
		return e.count(t, `SELECT COUNT(*) FROM episode_version_state s JOIN episode e ON e.id=s.episode_id WHERE e.title_id=? AND s.monitored=1`, id)
	}
	e.refresh(t, id)
	// 19 episodes in seasons 1 and 2; the specials stay off, as for any add.
	if n := monitored(); n != 19 {
		t.Fatalf("first refresh: %d monitored, want 19", n)
	}
	e.tvdb.edit("/series/371980/episodes/default?page=0", func(eps []map[string]any) []map[string]any {
		return append(eps, map[string]any{"id": 99000001, "seriesId": 371980, "name": "Later", "aired": "2027-01-10",
			"runtime": 50, "seasonNumber": 3, "number": 1})
	})
	e.refresh(t, id)
	e3 := e.ep(t, id, 3, 1)
	if e3 == nil {
		t.Fatal("season 3 did not arrive")
	}
	if e.state(t, id, "tv-2160p", e3.id) != 0 || monitored() != 19 {
		t.Fatalf("a season that appeared later is monitored: state %d, %d monitored", e.state(t, id, "tv-2160p", e3.id), monitored())
	}
	// A new episode of a season the first refresh found still follows that season.
	e.tvdb.edit("/series/371980/episodes/default?page=0", func(eps []map[string]any) []map[string]any {
		return append(eps, map[string]any{"id": 99000002, "seriesId": 371980, "name": "More", "aired": "2027-01-17",
			"runtime": 50, "seasonNumber": 2, "number": 11})
	})
	e.refresh(t, id)
	if n := monitored(); n != 20 {
		t.Fatalf("a new episode of a first season: %d monitored, want 20", n)
	}
}

func TestRefreshedSeriesIdentity(t *testing.T) {
	t.Parallel()
	e := setup(t)
	id := e.add(t, catalog.Input{TvdbID: 371980, Title: "Old name", Year: 2000, SeriesType: "daily"})
	e.refresh(t, id)
	// The current title/year are read directly by decide and catalog text searches.
	ti, err := decide.LoadTitle(context.Background(), e.st.Reader(), id)
	if err != nil {
		t.Fatal(err)
	}
	if ti.Name != "Severance" || ti.Year != 2022 {
		t.Fatalf("stale decide identity: %+v", ti)
	}
	var sortName, typ string
	if err := e.st.Reader().QueryRow(`SELECT sort_name,series_type FROM title WHERE id=?`, id).Scan(&sortName, &typ); err != nil {
		t.Fatal(err)
	}
	if sortName != "severance" || typ != "daily" {
		t.Fatalf("identity %q %q", sortName, typ)
	}
	// Verify a second refresh overwrites nonempty fields as well.
	e.tvdb.fix["/series/371980/extended"] = []byte(strings.ReplaceAll(strings.ReplaceAll(string(e.tvdb.fix["/series/371980/extended"]), `"Severance"`, `"New Name"`), `"2022"`, `"2023"`))
	e.refresh(t, id)
	ti, err = decide.LoadTitle(context.Background(), e.st.Reader(), id)
	if err != nil {
		t.Fatal(err)
	}
	if ti.Name != "New Name" || ti.Year != 2023 {
		t.Fatalf("second refresh: %+v", ti)
	}
	if e.count(t, `SELECT COUNT(*) FROM title WHERE name LIKE '%New Name%' AND year=2023`) != 1 {
		t.Fatal("text search still uses stale identity")
	}
}

func TestPendingMonitorWaitsForEpisodes(t *testing.T) {
	t.Parallel()
	e := setup(t)
	id := e.add(t, catalog.Input{TvdbID: 371980, Monitor: "pilot"})
	ctx := context.Background()
	if err := e.st.WriteTx(ctx, func(tx *sql.Tx) error { return catalog.ApplyPendingMonitor(ctx, tx, id, "tv-2160p", "continuing", now) }); err != nil {
		t.Fatal(err)
	}
	if e.count(t, `SELECT COUNT(*) FROM series_monitor WHERE pending_monitor='pilot'`) != 1 {
		t.Fatal("empty refresh consumed intent")
	}
	e.refresh(t, id)
	if e.state(t, id, "tv-2160p", e.ep(t, id, 1, 1).id) != 1 {
		t.Fatal("pilot not monitored")
	}
}

func TestPendingMonitorIncludesEmptySeasons(t *testing.T) {
	t.Parallel()
	e := setup(t)
	id := e.add(t, catalog.Input{TvdbID: 371980, Monitor: "lastSeason",
		Seasons: []catalog.Season{{Number: 1, Monitored: true}, {Number: 2, Monitored: true}, {Number: 3, Monitored: true}}})
	e.refresh(t, id)
	if e.count(t, `SELECT COUNT(*) FROM episode_version_state s JOIN episode e ON e.id=s.episode_id WHERE e.title_id=? AND s.monitored=1`, id) != 0 {
		t.Fatal("lastSeason ignored the requested season without episodes")
	}
}

func TestSceneExtrapolation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		mappings []xemMapping
		slot     slot
		want     *xemNumber
	}{
		{"hole before later mapping", []xemMapping{
			{TVDB: xemNumber{Season: 1, Episode: 1}, Scene: &xemNumber{Season: 2, Episode: 11}},
			{TVDB: xemNumber{Season: 1, Episode: 3}, Scene: &xemNumber{Season: 2, Episode: 13}},
		}, slot{1, 2}, nil},
		{"special at scene tail", []xemMapping{
			{TVDB: xemNumber{Season: 1, Episode: 1}, Scene: &xemNumber{Season: 2, Episode: 11}},
			{TVDB: xemNumber{Season: 0, Episode: 32}, Scene: &xemNumber{Season: 2, Episode: 12}},
		}, slot{1, 2}, nil},
		{"mapped to scene specials", []xemMapping{
			{TVDB: xemNumber{Season: 1, Episode: 1}, Scene: &xemNumber{Season: 3, Episode: 1}},
			{TVDB: xemNumber{Season: 2, Episode: 1}, Scene: &xemNumber{Season: 0, Episode: 4}},
		}, slot{2, 2}, &xemNumber{Season: 0, Episode: 5, Absolute: 1}},
		{"new season shift", []xemMapping{
			{TVDB: xemNumber{Season: 1, Episode: 1}, Scene: &xemNumber{Season: 3, Episode: 1}},
		}, slot{2, 2}, &xemNumber{Season: 4, Episode: 2}},
		{"explicit zero absolute", []xemMapping{
			{TVDB: xemNumber{Season: 1, Episode: 1}, Scene: &xemNumber{Season: 3, Episode: 1}},
		}, slot{1, 1}, &xemNumber{Season: 3, Episode: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			id := e.add(t, catalog.Input{TvdbID: 371980})
			e.refresh(t, id)
			if err := e.svc.sceneNumbers(context.Background(), id, tc.mappings); err != nil {
				t.Fatal(err)
			}
			ep := e.ep(t, id, tc.slot.season, tc.slot.number)
			var got xemNumber
			var absolute sql.NullInt64
			err := e.st.Reader().QueryRow(`SELECT s.season,s.number,a.number FROM episode_number s LEFT JOIN episode_number a ON a.episode_id=s.episode_id AND a.scheme='scene_absolute' WHERE s.episode_id=? AND s.scheme='scene'`, ep.id).Scan(&got.Season, &got.Episode, &absolute)
			got.Absolute = int(absolute.Int64)
			if tc.want == nil {
				if err != sql.ErrNoRows {
					t.Fatalf("unexpected extrapolation %+v: %v", got, err)
				}
			} else if err != nil || got != *tc.want {
				t.Fatalf("mapping %+v, want %+v: %v", got, *tc.want, err)
			} else if absolute.Valid != (tc.name != "new season shift") {
				t.Fatalf("absolute number presence = %v", absolute.Valid)
			}
		})
	}
}

func TestSceneSchedule(t *testing.T) {
	e := setup(t)
	var offset atomic.Int64
	e.svc.now = func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	e.svc.poll, e.svc.delay = 5*time.Millisecond, time.Millisecond
	e.svc.set.Interval = 24 * time.Hour
	e.svc.q = jobs.NewQueue(e.st)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.svc.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	wait := func() {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if e.count(t, `SELECT COUNT(*) FROM job WHERE kind=?`, sceneJob) == 1 {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("scene refresh not scheduled")
	}
	wait()
	if _, err := e.st.Writer().Exec(`DELETE FROM job WHERE kind=?`, sceneJob); err != nil {
		t.Fatal(err)
	}
	offset.Store(int64(sceneAge - time.Minute))
	// A normal series poll does not queue scene reads before three hours have passed.
	time.Sleep(25 * time.Millisecond)
	if e.count(t, `SELECT COUNT(*) FROM job WHERE kind=?`, sceneJob) != 0 {
		t.Fatal("scene refresh scheduled early")
	}
	offset.Store(int64(sceneAge))
	wait()
}
