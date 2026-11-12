package catalog

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

func execAll(t *testing.T, st *store.Store, qs ...string) {
	t.Helper()
	for _, q := range qs {
		if _, err := st.Writer().Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func importTitle(t *testing.T, s *MovieIntake, key string, it ArrImportTitle, dryRun bool) (string, int64) {
	t.Helper()
	change, id, err := s.ImportArrTitle(context.Background(), key, it, dryRun)
	if err != nil {
		t.Fatal(err)
	}
	return change, id
}

// An import adds the copy unmonitored until the caller releases it with the instance's flag; a movie takes the
// instance's minimum availability; a series on a TVDB episode list takes its monitor-new choice and seasons. No
// search is ever started.
func TestImportArrTitleCopiesMonitoring(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, st := newIntake(t, intakeSource{})
	s.Policy.Versions["tv"] = &policy.VersionPolicy{Kind: policy.KindSeries, Mode: policy.ModeActive}
	s.OnAdd = func(context.Context, int64, []string) error { return errors.New("OnAdd called") }

	change, id := importTitle(t, s, "movie-web", ArrImportTitle{Kind: policy.KindMovie, TmdbID: 601, Name: "Off",
		Monitored: false, MinimumAvailability: "inCinemas"}, false)
	if change != "add_title" || id == 0 {
		t.Fatalf("movie: %s %d", change, id)
	}
	if countRows(t, st, `SELECT count(*) FROM version WHERE title_id=? AND policy_key='movie-web' AND monitored=0`, id) != 1 ||
		countRows(t, st, `SELECT count(*) FROM arrview_title WHERE title_id=? AND view='v/movie-web'
			AND minimum_availability='inCinemas'`, id) != 1 {
		t.Error("movie copy not unmonitored with the instance's minimum availability")
	}
	if err := s.SetImportedMonitored(ctx, id, "movie-web", false); err != nil {
		t.Fatal(err)
	}
	_, on := importTitle(t, s, "movie-web", ArrImportTitle{Kind: policy.KindMovie, TmdbID: 602, Name: "On",
		Monitored: true}, false)
	if countRows(t, st, `SELECT count(*) FROM version WHERE title_id=? AND monitored=0`, on) != 1 {
		t.Error("a monitored movie's copy is eligible before its linking pass")
	}
	if err := s.SetImportedMonitored(ctx, on, "movie-web", true); err != nil {
		t.Fatal(err)
	}
	if countRows(t, st, `SELECT count(*) FROM version v JOIN title t ON t.id=v.title_id WHERE t.id=? AND v.monitored=1
		AND t.monitored=1`, on) != 1 || countRows(t, st, `SELECT count(*) FROM version WHERE title_id=? AND monitored=0`, id) != 1 {
		t.Error("released copies do not carry the instance's monitored flag")
	}

	it := ArrImportTitle{Kind: policy.KindSeries, TvdbID: 701, Name: "Show", Monitored: true, MonitorNew: "none",
		Seasons: []Season{{Number: 1, Monitored: false}, {Number: 2, Monitored: true}}}
	change, sid := importTitle(t, s, "tv", it, false)
	if change != "add_title" || sid == 0 {
		t.Fatalf("series: %s %d", change, sid)
	}
	// The episode list as a TVDB refresh leaves it.
	execAll(t, st, `UPDATE series_meta SET source='tvdb' WHERE title_id=`+strconv.FormatInt(sid, 10),
		`INSERT INTO episode(title_id,season,number,air_date_utc_at,created_at,updated_at) VALUES
			(`+strconv.FormatInt(sid, 10)+`,1,1,0,0,0),(`+strconv.FormatInt(sid, 10)+`,2,1,0,0,0)`)
	applied, err := s.ImportEpisodeMonitoring(ctx, sid, "tv", it, nil)
	if err != nil || !applied {
		t.Fatalf("episode monitoring %v %v", applied, err)
	}
	if countRows(t, st, `SELECT count(*) FROM series_monitor m JOIN version v ON v.id=m.version_id WHERE v.title_id=?
		AND m.monitor_new='none' AND m.seasons='{"1":false,"2":true}'`, sid) != 1 {
		t.Error("series_monitor does not hold the instance's choices")
	}
	if countRows(t, st, `SELECT count(*) FROM episode WHERE title_id=? AND monitored=(season=2)`, sid) != 2 {
		t.Error("episode flags do not follow the seasons")
	}
}

// A library row in any state is a choice already made: it reads present and stays as it is; an excluded movie reads
// excluded; a second import of a title it added changes nothing.
func TestImportArrTitleLeavesExistingAlone(t *testing.T) {
	t.Parallel()
	s, st := newIntake(t, intakeSource{})
	execAll(t, st,
		`INSERT INTO movie_exclusion(tmdb_id,created_at) VALUES(302,0)`,
		`INSERT INTO title(id,kind,name,tmdb_id,created_at,updated_at) VALUES(53,'movie','Unmonitored',303,0,0)`,
		`INSERT INTO version(id,title_id,policy_key,monitored,state,created_at,updated_at) VALUES(63,53,'movie-web',0,'wanted',0,0)`)
	if change, id := importTitle(t, s, "movie-web", ArrImportTitle{Kind: policy.KindMovie, TmdbID: 303, Monitored: true,
		MinimumAvailability: "announced"}, false); change != "present" || id != 53 {
		t.Errorf("existing: %s %d", change, id)
	}
	if countRows(t, st, `SELECT count(*) FROM version WHERE title_id=53 AND monitored=0 AND updated_at=0`) != 1 ||
		countRows(t, st, `SELECT count(*) FROM arrview_title WHERE title_id=53`) != 0 {
		t.Error("an existing library row was changed")
	}
	if change, id := importTitle(t, s, "movie-web", ArrImportTitle{Kind: policy.KindMovie, TmdbID: 302}, false); change != "excluded" || id != 0 {
		t.Errorf("excluded: %s %d", change, id)
	}
	first, id := importTitle(t, s, "movie-web", ArrImportTitle{Kind: policy.KindMovie, TmdbID: 304}, false)
	second, again := importTitle(t, s, "movie-web", ArrImportTitle{Kind: policy.KindMovie, TmdbID: 304}, false)
	if first != "add_title" || second != "present" || id != again {
		t.Errorf("twice: %s %s %d %d", first, second, id, again)
	}
	if countRows(t, st, `SELECT count(*) FROM title WHERE tmdb_id IN (302,304)`) != 1 {
		t.Error("excluded movie added, or a movie added twice")
	}
}

// A dry run writes nothing, and decides a new movie's fit to an anime-flagged library from TMDb as the real run does.
func TestImportArrTitleDryRun(t *testing.T) {
	t.Parallel()
	s, st := newIntake(t, intakeSource{movies: map[int]*tmdb.Movie{
		801: intakeFixture[tmdb.Movie](t, `{"id":801,"title":"Anime","release_date":"2020-01-01","original_language":"ja",
			"genres":[{"id":16,"name":"Animation"}]}`),
	}})
	anime := true
	s.Policy.Versions["anime-movie"] = &policy.VersionPolicy{Kind: policy.KindMovie, Anime: &anime, Mode: policy.ModeActive}
	if change, id := importTitle(t, s, "movie-web", ArrImportTitle{Kind: policy.KindMovie, TmdbID: 800}, true); change != "add_title" || id != 0 {
		t.Errorf("new movie: %s %d", change, id)
	}
	if change, _ := importTitle(t, s, "anime-movie", ArrImportTitle{Kind: policy.KindMovie, TmdbID: 801}, true); change != "add_title" {
		t.Errorf("anime movie for the anime library: %s", change)
	}
	if change, _ := importTitle(t, s, "anime-movie", ArrImportTitle{Kind: policy.KindMovie, TmdbID: 802}, true); change != "skipped" {
		t.Errorf("plain movie for the anime library: %s", change)
	}
	if change, _ := importTitle(t, s, "anime-movie", ArrImportTitle{Kind: policy.KindMovie, TmdbID: 802}, false); change != "skipped" {
		t.Errorf("plain movie for the anime library, real run: %s", change)
	}
	if countRows(t, st, `SELECT count(*) FROM title`) != 0 || countRows(t, st, `SELECT count(*) FROM version`) != 0 {
		t.Error("a dry run or a skipped title wrote rows")
	}
	if change, _ := importTitle(t, s, "anime-movie", ArrImportTitle{Kind: policy.KindMovie, TmdbID: 801}, false); change != "add_title" {
		t.Errorf("anime movie, real run: %s", change)
	}
}

// Episode monitoring goes to the copy's episode states and the episodes' own flag, and the copy's state is
// recomputed; a series whose episode list is from TMDb gets nothing. ImportNeedsEpisodes asks for a refresh with no
// episodes, or a list not from TVDB once TVDB is configured.
func TestImportEpisodeMonitoring(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, st := newIntake(t, intakeSource{})
	s.Policy.Versions["tv"] = &policy.VersionPolicy{Kind: policy.KindSeries, Mode: policy.ModeActive}
	execAll(t, st,
		`INSERT INTO title(id,kind,name,tvdb_id,created_at,updated_at) VALUES(90,'series','Show',901,0,0),(91,'series','Other',902,0,0)`,
		`INSERT INTO version(id,title_id,policy_key,monitored,state,created_at,updated_at) VALUES(95,90,'tv',0,'wanted',0,0),(96,91,'tv',0,'wanted',0,0)`)
	if got, err := s.ImportNeedsEpisodes(ctx, 90, false); err != nil || !got {
		t.Errorf("needs episodes, none yet: %v %v", got, err)
	}
	execAll(t, st,
		`INSERT INTO series_meta(title_id,source) VALUES(90,'tvdb'),(91,'tmdb')`,
		`INSERT INTO episode(id,title_id,season,number,air_date_utc_at,created_at,updated_at) VALUES
			(1,90,1,1,0,0,0),(2,90,1,2,0,0,0),(3,91,1,1,0,0,0)`,
		// E01 already has its file in the copy, both start monitored as a refresh leaves them.
		`INSERT INTO episode_version_state(version_id,episode_id,monitored,state,updated_at) VALUES
			(95,1,1,'filled',0),(95,2,1,'wanted',0),(96,3,1,'wanted',0)`)
	for _, c := range []struct {
		id         int64
		configured bool
		want       bool
	}{{90, true, false}, {91, false, false}, {91, true, true}} {
		if got, err := s.ImportNeedsEpisodes(ctx, c.id, c.configured); err != nil || got != c.want {
			t.Errorf("needs episodes %d (TVDB %v): %v %v", c.id, c.configured, got, err)
		}
	}
	eps := []EpisodeMonitor{{Season: 1, Number: 1, Monitored: false}, {Season: 1, Number: 2, Monitored: true}}
	for id, want := range map[int64]bool{90: true, 91: false} {
		got, err := s.ImportEpisodeMonitoring(ctx, id, "tv", ArrImportTitle{Kind: policy.KindSeries}, eps)
		if err != nil || got != want {
			t.Errorf("title %d: applied %v %v", id, got, err)
		}
	}
	if countRows(t, st, `SELECT count(*) FROM episode_version_state WHERE version_id=95 AND monitored=(episode_id=2)`) != 2 ||
		countRows(t, st, `SELECT count(*) FROM episode WHERE title_id=90 AND monitored=(id=2)`) != 2 {
		t.Error("episode flags not written in both places")
	}
	// E02 aired and is wanted and monitored: the copy is wanted; E01's own file does not make it filled.
	if countRows(t, st, `SELECT count(*) FROM version WHERE id=95 AND state='wanted'`) != 1 {
		t.Error("copy state not recomputed")
	}
	if countRows(t, st, `SELECT count(*) FROM episode_version_state WHERE version_id=96 AND monitored=1`) != 1 ||
		countRows(t, st, `SELECT count(*) FROM episode WHERE id=3 AND monitored=1`) != 1 {
		t.Error("a series on a TMDb episode list was changed")
	}
	// Unmonitoring the only wanted episode leaves the copy filled by E01's file.
	if _, err := s.ImportEpisodeMonitoring(ctx, 90, "tv", ArrImportTitle{Kind: policy.KindSeries},
		[]EpisodeMonitor{{Season: 1, Number: 2, Monitored: false}}); err != nil {
		t.Fatal(err)
	}
	if countRows(t, st, `SELECT count(*) FROM version WHERE id=95 AND state='filled'`) != 1 {
		t.Error("copy state not recomputed after unmonitoring")
	}
}
