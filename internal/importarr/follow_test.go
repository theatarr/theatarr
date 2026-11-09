package importarr

import (
	"context"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
)

type followSearchRecorder struct {
	targets []FollowSearchTarget
}

func (r *followSearchRecorder) EnqueueFollowSearch(_ context.Context, targets []FollowSearchTarget) error {
	r.targets = append(r.targets, targets...)
	return nil
}

func followSnapshots() (*Snapshot, *Snapshot) {
	aired := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	later := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	movies := &Snapshot{Instance: "radarr_a", Flavour: "radarr", Titles: []Title{
		{ArrID: 1, Kind: "movie", Name: "Arrival", Year: 2016, TmdbID: 329865, Monitored: true,
			File: &File{ArrID: 11, Path: "/arr/movies/Arrival (2016)/Arrival.mkv", Quality: "Remux-2160p"}},
		{ArrID: 2, Kind: "movie", Name: "Dune", Year: 2021, TmdbID: 438631, Monitored: false},
	}}
	series := &Snapshot{Instance: "sonarr_a", Flavour: "sonarr", Titles: []Title{{
		ArrID: 5, Kind: "series", Name: "Show", Year: 2020, TvdbID: 1001, Monitored: true, SeriesType: "standard",
		MonitorNewItems: "none", SeasonsMonitored: map[int]bool{0: false, 1: true},
		Episodes: []Episode{
			{ArrID: 50, Season: 1, Number: 1, AirDateUTC: &aired, Monitored: true, FileArrID: 500},
			{ArrID: 51, Season: 1, Number: 2, AirDateUTC: &aired, Monitored: true},
			{ArrID: 52, Season: 1, Number: 3, AirDateUTC: &later, Monitored: true},
			{ArrID: 53, Season: 0, Number: 1, AirDateUTC: &aired, Monitored: false},
		},
		Files: []*File{{ArrID: 500, Path: "/arr/shows/Show/Season 01/Show - S01E01.mkv", Quality: "WEBDL-2160p",
			EpisodeArrIDs: []int{50}}},
	}}}
	return movies, series
}

func TestFollowerSourceSettings(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	ctx := context.Background()
	im := &Importer{Store: st, Now: fixedNow}
	movies, series := followSnapshots()
	if _, err := im.Write(ctx, Source{Instance: "sonarr_a", Version: "standard"}, series); err != nil {
		t.Fatal(err)
	}
	series.Instance = "sonarr_b"
	series.Titles[0].SeriesType = "anime"
	if _, err := im.Write(ctx, Source{Instance: "sonarr_b", Version: "anime", Followers: []string{"anime-theatarr"}}, series); err != nil {
		t.Fatal(err)
	}
	tid := one[int64](t, st, `SELECT id FROM title WHERE kind = 'series'`)
	vid := one[int64](t, st, `SELECT id FROM version WHERE policy_key = 'anime-theatarr'`)
	title, err := decide.LoadTitle(ctx, st.Reader(), tid)
	if err != nil {
		t.Fatal(err)
	}
	if err := decide.LoadSeriesVersion(ctx, st.Reader(), &title, vid); err != nil {
		t.Fatal(err)
	}
	if title.SeriesType != "anime" {
		t.Errorf("follower series type %q, want anime", title.SeriesType)
	}
	movies.Titles[0].MinimumAvailability = "announced"
	if _, err := im.Write(ctx, Source{Instance: "radarr_a", Version: "movie", Followers: []string{"movie-theatarr"}}, movies); err != nil {
		t.Fatal(err)
	}
	tid = one[int64](t, st, `SELECT id FROM title WHERE name = 'Arrival'`)
	vid = one[int64](t, st, `SELECT id FROM version WHERE title_id = ? AND policy_key = 'movie-theatarr'`, tid)
	pol := &policy.Policy{Versions: map[string]*policy.VersionPolicy{"movie-theatarr": {Follows: "movie"}}}
	ok, err := availability.Movie(ctx, st.Reader(), pol, tid, vid, "movie-theatarr", fixedNow())
	if err != nil || !ok {
		t.Errorf("announced follower available=%v, err=%v", ok, err)
	}
}

func TestFollowerMissingEpisodes(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	ctx := context.Background()
	im := &Importer{Store: st, Now: fixedNow}
	_, snap := followSnapshots()
	src := Source{Instance: "sonarr_a", Version: "tv", Followers: []string{"tv-theatarr"}}
	if _, err := im.Write(ctx, src, snap); err != nil {
		t.Fatal(err)
	}
	// Metadata ids retain the disappeared episode; the follower's link and state must survive too.
	eid := one[int64](t, st, `SELECT id FROM episode WHERE season = 1 AND number = 2`)
	vid := one[int64](t, st, `SELECT id FROM version WHERE policy_key = 'tv-theatarr'`)
	for _, q := range []string{
		`UPDATE episode SET tvdb_episode_id = 12345 WHERE season = 1 AND number = 2`,
		`INSERT INTO link (id, title_id, path, target, state, created_at, updated_at)
		 VALUES (99, (SELECT id FROM title WHERE kind = 'series'), '/theatarr/episode.mkv', '/mount/episode.mkv', 'active', 0, 0)`,
	} {
		if _, err := st.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Writer().Exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (99, ?)`, eid); err != nil {
		t.Fatal(err)
	}
	snap.Titles[0].Episodes = append(snap.Titles[0].Episodes[:1], snap.Titles[0].Episodes[2:]...)
	if _, err := im.Write(ctx, src, snap); err != nil {
		t.Fatal(err)
	}
	if n := one[int](t, st, `SELECT monitored FROM episode_version_state WHERE version_id = ? AND episode_id = ?`, vid, eid); n != 0 {
		t.Error("disappeared episode still monitored")
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM link_episode WHERE link_id = 99 AND episode_id = ?`, eid); n != 1 {
		t.Error("disappeared episode lost its link")
	}
	if sum, err := im.Write(ctx, src, snap); err != nil || sum.Changes.Total() != 0 {
		t.Errorf("repeat import: %+v, %v", sum, err)
	}
}

func TestImportFollowers(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	ctx := context.Background()
	im := &Importer{Store: st, Now: fixedNow}
	movies, series := followSnapshots()
	msrc := Source{Instance: "radarr_a", Flavour: "radarr", Version: "movie-remux", Followers: []string{"movie-remux-theatarr"}}
	ssrc := Source{Instance: "sonarr_a", Flavour: "sonarr", Version: "tv-2160p", Followers: []string{"tv-theatarr"}}
	sum, err := im.Write(ctx, msrc, movies)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Followed != (FollowCounts{Titles: 2, Created: 2}) {
		t.Errorf("followed %+v", sum.Followed)
	}
	if _, err := im.Write(ctx, ssrc, series); err != nil {
		t.Fatal(err)
	}
	if s := one[string](t, st, `SELECT GROUP_CONCAT(t.name || ':' || v.monitored || ':' || v.state, ' ') FROM
		(SELECT * FROM version WHERE policy_key = 'movie-remux-theatarr' ORDER BY id) v JOIN title t ON t.id = v.title_id`); s != "Arrival:1:wanted Dune:0:wanted" {
		t.Errorf("movie followers %q", s)
	}
	// The arr's files are the followed version's only: no file, source or arr record names a follower.
	if n := one[int](t, st, `SELECT COUNT(*) FROM version_file vf JOIN version v ON v.id = vf.version_id
		WHERE v.policy_key IN ('movie-remux-theatarr', 'tv-theatarr')`); n != 0 {
		t.Errorf("follower files %d", n)
	}
	if n := one[int](t, st, `SELECT (SELECT COUNT(*) FROM version_source vs JOIN version v ON v.id = vs.version_id
		WHERE v.policy_key LIKE '%theatarr') + (SELECT COUNT(*) FROM arr_title a JOIN version v ON v.id = a.version_id
		WHERE v.policy_key LIKE '%theatarr')`); n != 0 {
		t.Errorf("follower ownership rows %d", n)
	}
	if s := one[string](t, st, `SELECT GROUP_CONCAT(e.season || 'x' || e.number || '=' || s.monitored || s.state, ' ') FROM
		(SELECT * FROM episode_version_state WHERE version_id = (SELECT id FROM version WHERE policy_key = 'tv-theatarr')
		ORDER BY episode_id) s JOIN episode e ON e.id = s.episode_id`); s != "1x1=1wanted 1x2=1wanted 1x3=1wanted 0x1=0wanted" {
		t.Errorf("follower episodes %q", s)
	}
	if s := one[string](t, st, `SELECT monitor_new || ' ' || seasons FROM series_monitor WHERE version_id =
		(SELECT id FROM version WHERE policy_key = 'tv-theatarr')`); s != `none {"0":false,"1":true}` {
		t.Errorf("follower intent %q", s)
	}
	if s := one[string](t, st, `SELECT state FROM version WHERE policy_key = 'tv-theatarr'`); s != "wanted" {
		t.Errorf("follower series state %q", s)
	}

	// A second run with nothing new changes nothing.
	sum, err = im.Write(ctx, ssrc, series)
	if err != nil {
		t.Fatal(err)
	}
	if n := sum.Changes.Total(); n != 0 {
		t.Errorf("re-run changed %s", sum.Changes)
	}

	// The arr unmonitors an episode and later drops the movie: the follower does the same.
	series.Titles[0].Episodes[1].Monitored = false
	if _, err := im.Write(ctx, ssrc, series); err != nil {
		t.Fatal(err)
	}
	if n := one[int](t, st, `SELECT s.monitored FROM episode_version_state s JOIN episode e ON e.id = s.episode_id
		WHERE s.version_id = (SELECT id FROM version WHERE policy_key = 'tv-theatarr') AND e.season = 1 AND e.number = 2`); n != 0 {
		t.Errorf("episode still monitored in the follower")
	}
	movies.Titles = movies.Titles[1:]
	sum, err = im.Write(ctx, msrc, movies)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Followed.Unmonitored != 1 {
		t.Errorf("unmonitored %+v", sum.Followed)
	}
	if n := one[int](t, st, `SELECT v.monitored FROM version v JOIN title t ON t.id = v.title_id
		WHERE v.policy_key = 'movie-remux-theatarr' AND t.name = 'Arrival'`); n != 0 {
		t.Errorf("dropped movie's follower still monitored")
	}
}

func TestFollowerAddSearchHook(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	rec := &followSearchRecorder{}
	RegisterFollowSearcher(st, rec)
	t.Cleanup(func() { followSearchers.Delete(st) })
	movies, _ := followSnapshots()
	if _, err := (&Importer{Store: st, Now: fixedNow}).Write(context.Background(), Source{Instance: "radarr_a", Flavour: "radarr", Version: "movie", Followers: []string{"movie-theatarr"}}, movies); err != nil {
		t.Fatal(err)
	}
	if len(rec.targets) != 1 || !rec.targets[0].All || rec.targets[0].VersionKey != "movie-theatarr" {
		t.Fatalf("follow searches %+v", rec.targets)
	}
}

func TestFollowerAddSearchHookDisabledRecorder(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	rec := &followSearchRecorder{}
	RegisterFollowSearcher(st, rec)
	t.Cleanup(func() { followSearchers.Delete(st) })
	movies, _ := followSnapshots()
	if _, err := (&Importer{Store: st, Now: fixedNow}).Write(context.Background(), Source{Instance: "radarr_a", Flavour: "radarr", Version: "movie", Followers: []string{"movie-theatarr"}}, movies); err != nil {
		t.Fatal(err)
	}
	// The importer records the event; the registered acquirer owns the router gate.
	if len(rec.targets) != 1 {
		t.Fatalf("follow searches %+v", rec.targets)
	}
}

func TestFollowerMonitorSearchHookAiredOnly(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	rec := &followSearchRecorder{}
	RegisterFollowSearcher(st, rec)
	t.Cleanup(func() { followSearchers.Delete(st) })
	_, series := followSnapshots()
	src := Source{Instance: "sonarr_a", Version: "tv", Followers: []string{"tv-theatarr"}}
	series.Titles[0].Monitored = false
	for i := range series.Titles[0].Episodes {
		series.Titles[0].Episodes[i].Monitored = false
	}
	if _, err := (&Importer{Store: st, Now: fixedNow}).Write(context.Background(), src, series); err != nil {
		t.Fatal(err)
	}
	rec.targets = nil
	series.Titles[0].Monitored = true
	series.Titles[0].Episodes[0].Monitored = true
	series.Titles[0].Episodes[1].Monitored = true
	if _, err := (&Importer{Store: st, Now: fixedNow}).Write(context.Background(), src, series); err != nil {
		t.Fatal(err)
	}
	if len(rec.targets) != 1 || !rec.targets[0].All || len(rec.targets[0].EpisodeIDs) != 0 {
		t.Fatalf("follow searches %+v", rec.targets)
	}
}

func TestFollowerEpisodeMonitorSearchHook(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	rec := &followSearchRecorder{}
	RegisterFollowSearcher(st, rec)
	t.Cleanup(func() { followSearchers.Delete(st) })
	_, series := followSnapshots()
	src := Source{Instance: "sonarr_a", Version: "tv", Followers: []string{"tv-theatarr"}}
	series.Titles[0].Episodes[0].Monitored = false
	if _, err := (&Importer{Store: st, Now: fixedNow}).Write(context.Background(), src, series); err != nil {
		t.Fatal(err)
	}
	rec.targets = nil
	series.Titles[0].Episodes[0].Monitored = true
	if _, err := (&Importer{Store: st, Now: fixedNow}).Write(context.Background(), src, series); err != nil {
		t.Fatal(err)
	}
	if len(rec.targets) != 1 || rec.targets[0].All || len(rec.targets[0].EpisodeIDs) != 1 {
		t.Fatalf("follow searches %+v", rec.targets)
	}
}

func TestFollowerStateKeepsOwnLinks(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	ctx := context.Background()
	im := &Importer{Store: st, Now: fixedNow}
	_, series := followSnapshots()
	ssrc := Source{Instance: "sonarr_a", Flavour: "sonarr", Version: "tv-2160p", Followers: []string{"tv-theatarr"}}
	if _, err := im.Write(ctx, ssrc, series); err != nil {
		t.Fatal(err)
	}
	// Theatarr's own link filled the aired episodes of the follower: the next import keeps those states.
	if _, err := st.Writer().Exec(`UPDATE episode_version_state SET state = 'cutoff_met', cutoff_met = 1
		WHERE version_id = (SELECT id FROM version WHERE policy_key = 'tv-theatarr') AND episode_id IN
		(SELECT id FROM episode WHERE season = 1 AND number IN (1, 2))`); err != nil {
		t.Fatal(err)
	}
	if _, err := im.Write(ctx, ssrc, series); err != nil {
		t.Fatal(err)
	}
	if s := one[string](t, st, `SELECT state FROM version WHERE policy_key = 'tv-theatarr'`); s != "cutoff_met" {
		t.Errorf("follower series state %q", s)
	}
}

// The import records follower changes as add-time search intents in its own transaction, with no searcher
// registered too, merged per title and version.
func TestFollowerAddSearchIntentRecorded(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	_, series := followSnapshots()
	src := Source{Instance: "sonarr_a", Version: "tv", Followers: []string{"tv-theatarr"}}
	series.Titles[0].Episodes[0].Monitored = false
	if _, err := (&Importer{Store: st, Now: fixedNow}).Write(context.Background(), src, series); err != nil {
		t.Fatal(err)
	}
	if got := one[string](t, st, `SELECT all_items || ' ' || episodes FROM follow_search_intent
		WHERE version_key = 'tv-theatarr'`); got != "1 []" {
		t.Fatalf("intent %q", got)
	}
	if _, err := st.Writer().Exec(`DELETE FROM follow_search_intent`); err != nil {
		t.Fatal(err)
	}
	series.Titles[0].Episodes[0].Monitored = true
	if _, err := (&Importer{Store: st, Now: fixedNow}).Write(context.Background(), src, series); err != nil {
		t.Fatal(err)
	}
	if got := one[string](t, st, `SELECT all_items || ' ' || json_array_length(episodes) FROM follow_search_intent
		WHERE version_key = 'tv-theatarr'`); got != "0 1" {
		t.Fatalf("episode intent %q", got)
	}
}
