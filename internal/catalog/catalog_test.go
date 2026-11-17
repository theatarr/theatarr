package catalog

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

var now = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func setup(t *testing.T) (*store.Store, *policy.Policy) {
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
	pol, err := policy.Load(filepath.Join("..", "..", "examples", "policy"))
	if err != nil {
		t.Fatal(err)
	}
	return st, pol
}

func add(t *testing.T, st *store.Store, in Input) *Result {
	t.Helper()
	var res *Result
	err := st.WriteTx(context.Background(), func(tx *sql.Tx) error {
		var err error
		res, err = Add(context.Background(), tx, in, now, slog.New(slog.NewTextHandler(io.Discard, nil)))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func fixed(keys ...string) func(bool) ([]string, error) {
	return func(bool) ([]string, error) { return keys, nil }
}

func TestDefaultVersions(t *testing.T) {
	t.Parallel()
	_, pol := setup(t)
	for _, c := range []struct {
		kind  policy.Kind
		anime bool
		want  string
	}{
		{policy.KindMovie, false, "movie-remux movie-web"},
		{policy.KindMovie, true, "anime-movie"},
		{policy.KindSeries, false, "tv-2160p"},
		{policy.KindSeries, true, "anime-tv"},
	} {
		if got := strings.Join(DefaultVersions(pol, c.kind, c.anime), " "); got != c.want {
			t.Errorf("%s anime=%v: %q, want %q", c.kind, c.anime, got, c.want)
		}
	}
}

func TestSearchable(t *testing.T) {
	t.Parallel()
	_, pol := setup(t)
	keys := []string{"movie-remux", "movie-web", "nope"}
	// The example policy: router off, nothing is searched.
	active, skipped := Searchable(pol, keys)
	if len(active) != 0 || len(skipped) != 3 || skipped[1].Version != "movie-web" ||
		!strings.Contains(skipped[1].Reason, "Paused") || !strings.Contains(skipped[2].Reason, "not in the policy") {
		t.Fatalf("router off: %v %v", active, skipped)
	}
	// Pause off: every library in the policy.
	pol.Router = &policy.Router{Enabled: true}
	active, skipped = Searchable(pol, keys)
	if !slices.Equal(active, []string{"movie-remux", "movie-web"}) || len(skipped) != 1 {
		t.Fatalf("router on: %v %v", active, skipped)
	}
}

func TestAddCreatesFindsAndRestores(t *testing.T) {
	t.Parallel()
	st, _ := setup(t)
	yes := true
	r := add(t, st, Input{Kind: policy.KindSeries, TmdbID: 900, TvdbID: 901, Title: "Kaiju", Year: 2024, Anime: &yes,
		Versions: fixed("anime-tv"), Monitored: true, Source: "test"})
	if !r.Created || !r.Anime || !slices.Equal(r.New, []string{"anime-tv"}) {
		t.Fatalf("create: %+v", r)
	}
	var name, sortName string
	var tmdb, tvdb int64
	var anime, mon bool
	if err := st.Reader().QueryRow(`SELECT name, sort_name, tmdb_id, tvdb_id, anime, monitored FROM title WHERE id = ?`,
		r.TitleID).Scan(&name, &sortName, &tmdb, &tvdb, &anime, &mon); err != nil {
		t.Fatal(err)
	}
	if name != "Kaiju" || sortName != "kaiju" || tmdb != 900 || tvdb != 901 || !anime || !mon {
		t.Errorf("title %s %s %d %d %v %v", name, sortName, tmdb, tvdb, anime, mon)
	}
	// Found again by TMDb id alone: no new title; the chooser sees the stored anime flag.
	var sawAnime bool
	r2 := add(t, st, Input{Kind: policy.KindSeries, TmdbID: 900, Monitored: true,
		Versions: func(a bool) ([]string, error) { sawAnime = a; return []string{"anime-tv"}, nil }})
	if r2.Created || r2.TitleID != r.TitleID || !sawAnime || len(r2.New) != 0 {
		t.Errorf("second add %+v anime %v", r2, sawAnime)
	}
	// A removed version is restored and monitored again; the title follows.
	if _, err := st.Writer().Exec(`UPDATE version SET monitored = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().Exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at)
		SELECT id, 'sonarr', 0 FROM version`); err != nil {
		t.Fatal(err)
	}
	add(t, st, Input{Kind: policy.KindSeries, TvdbID: 901, Versions: fixed("anime-tv"), Monitored: true})
	var removed int
	if err := st.Reader().QueryRow(`SELECT COUNT(*) FROM arrview_removed_version`).Scan(&removed); err != nil || removed != 0 {
		t.Errorf("removed rows %d %v", removed, err)
	}
	// No id at all is refused; a found title gets the ids it lacked.
	err := st.WriteTx(context.Background(), func(tx *sql.Tx) error {
		_, err := Add(context.Background(), tx, Input{Kind: policy.KindMovie, Versions: fixed("movie-web")}, now, nil)
		return err
	})
	if err != ErrNoID {
		t.Errorf("no id: %v", err)
	}
	m := add(t, st, Input{Kind: policy.KindMovie, TmdbID: 77, Versions: fixed("movie-web"), Monitored: false})
	var mname string
	var mmon bool
	if err := st.Reader().QueryRow(`SELECT name, monitored FROM title WHERE id = ?`, m.TitleID).Scan(&mname, &mmon); err != nil ||
		mname != "Tmdb 77" || mmon {
		t.Errorf("unnamed movie %q monitored %v %v", mname, mmon, err)
	}
	add(t, st, Input{Kind: policy.KindMovie, TmdbID: 77, ImdbID: "tt0077", Versions: fixed("movie-web"), Monitored: true})
	var imdb string
	if err := st.Reader().QueryRow(`SELECT imdb_id FROM title WHERE id = ?`, m.TitleID).Scan(&imdb); err != nil || imdb != "tt0077" {
		t.Errorf("imdb %q %v", imdb, err)
	}
}

// A series add keeps its monitoring as durable intent (monitor new items, season choices) for episodes a metadata
// refresh adds later, and marks the series' metadata pending.
func TestSeriesIntent(t *testing.T) {
	t.Parallel()
	st, _ := setup(t)
	ctx := context.Background()
	res := add(t, st, Input{Kind: policy.KindSeries, TvdbID: 81189, Title: "Show", Monitored: true,
		Versions: fixed("tv-2160p"), MonitorNew: MonitorNone,
		Seasons: []Season{{Number: 1, Monitored: true}, {Number: 2, Monitored: false}}})
	var vid int64
	if err := st.Reader().QueryRow(`SELECT id FROM version WHERE title_id = ?`, res.TitleID).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	m, err := LoadMonitor(ctx, st.Reader(), vid)
	if err != nil || m.New != MonitorNone || len(m.Seasons) != 2 || !m.Seasons[1] || m.Seasons[2] {
		t.Fatalf("intent %+v %v", m, err)
	}
	var added sql.NullInt64
	if err := st.Reader().QueryRow(`SELECT added_at FROM series_meta WHERE title_id = ?`, res.TitleID).Scan(&added); err != nil ||
		added.Int64 != now.UnixMilli() {
		t.Fatalf("pending %v %v", added, err)
	}
	// A later season toggle updates the intent; an add naming no monitorNewItems keeps the version's.
	if err := st.WriteTx(ctx, func(tx *sql.Tx) error {
		return SetSeasonMonitored(ctx, tx, res.TitleID, []string{"tv-2160p"}, 2, true, now)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().Exec(`UPDATE series_meta SET added_at = NULL`); err != nil {
		t.Fatal(err)
	}
	add(t, st, Input{Kind: policy.KindSeries, TvdbID: 81189, Monitored: true, Versions: fixed("tv-2160p")})
	if m, err = LoadMonitor(ctx, st.Reader(), vid); err != nil || m.New != MonitorNone || !m.Seasons[2] {
		t.Fatalf("after %+v %v", m, err)
	}
	// Nothing new turned on: the series is not pending again.
	if err := st.Reader().QueryRow(`SELECT added_at FROM series_meta WHERE title_id = ?`, res.TitleID).Scan(&added); err != nil ||
		added.Valid {
		t.Fatalf("pending again %v %v", added, err)
	}
	if err := st.WriteTx(ctx, func(tx *sql.Tx) error {
		return SetMonitorNew(ctx, tx, res.TitleID, []string{"tv-2160p"}, "some", now)
	}); err == nil {
		t.Error("a bad monitorNewItems accepted")
	}
}

func TestAddMethod(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"", "list", "collection"} {
		t.Run(method, func(t *testing.T) {
			st, _ := setup(t)
			in := Input{Kind: policy.KindMovie, TmdbID: 123, Title: "Movie", Versions: fixed("movie-web"), AddMethod: method}
			res := add(t, st, in)
			want := method
			if want == "" {
				want = "manual"
			}
			var got string
			if err := st.Reader().QueryRow(`SELECT add_method FROM version WHERE title_id=?`, res.TitleID).Scan(&got); err != nil || got != want {
				t.Fatalf("method %q want %q: %v", got, want, err)
			}
			in.AddMethod = "manual"
			add(t, st, in)
			if err := st.Reader().QueryRow(`SELECT add_method FROM version WHERE title_id=?`, res.TitleID).Scan(&got); err != nil || got != want {
				t.Fatalf("repeat changed method to %q: %v", got, err)
			}
		})
	}
}

// syncPolicy is the example policy with a third movie library and a second series library, kept in sync as
// {movie-remux, movie-web, movie-hd} and {tv-2160p, tv-1080p}; the policy validates with them.
func syncPolicy(t *testing.T) *policy.Policy {
	t.Helper()
	_, pol := setup(t)
	hd := *pol.Versions["movie-web"]
	hd.Library = "/media/movies-hd"
	pol.Versions["movie-hd"] = &hd
	tv := *pol.Versions["tv-2160p"]
	tv.Library = "/media/tv-1080p"
	pol.Versions["tv-1080p"] = &tv
	pol.Library.SyncGroups = [][]string{{"movie-remux", "movie-web", "movie-hd"}, {"tv-2160p", "tv-1080p"}}
	if err := pol.Validate(); err != nil {
		t.Fatal(err)
	}
	return pol
}

// copies are the title's versions as "key:monitored[:removed]", sorted.
func copies(t *testing.T, st *store.Store, titleID int64) string {
	t.Helper()
	rows, err := st.Reader().Query(`SELECT v.policy_key, v.monitored, EXISTS (SELECT 1 FROM arrview_removed_version r
		WHERE r.version_id = v.id) FROM version v WHERE v.title_id = ? ORDER BY v.policy_key`, titleID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		var mon, removed bool
		if err := rows.Scan(&k, &mon, &removed); err != nil {
			t.Fatal(err)
		}
		s := k + ":" + map[bool]string{true: "on", false: "off"}[mon]
		if removed {
			s += ":removed"
		}
		out = append(out, s)
	}
	return strings.Join(out, " ")
}

// The sync groups' rule, one route at a time through catalog.Add and TurnOnSynced: a library in sync gets a copy
// only when the add did not name it and the title has no copy of it, or a person's add finds it removed (restored),
// and only when the named copy is new or restored.
func TestSyncGroupsAdd(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, _ := setup(t)
	pol := syncPolicy(t)
	movie := func(tmdb int64, keys ...string) *Result {
		return add(t, st, Input{Kind: policy.KindMovie, TmdbID: tmdb, Title: "M", Monitored: true, Versions: fixed(keys...),
			Policy: pol, Source: "test"})
	}
	// held gives title id the copies, as "key" (monitored), "key-" (unmonitored) or "key!" (removed).
	held := func(id int64, kind string, cs ...string) {
		t.Helper()
		execAll(t, st, `INSERT INTO title (id, kind, name, tmdb_id, created_at, updated_at) VALUES (`+
			strconv.FormatInt(id, 10)+`, '`+kind+`', 'H', `+strconv.FormatInt(id, 10)+`, 0, 0)`)
		for _, c := range cs {
			k, mon := strings.TrimRight(c, "-!"), 1
			if strings.HasSuffix(c, "-") || strings.HasSuffix(c, "!") {
				mon = 0
			}
			execAll(t, st, `INSERT INTO version (title_id, policy_key, monitored, state, created_at, updated_at) VALUES (`+
				strconv.FormatInt(id, 10)+`, '`+k+`', `+strconv.Itoa(mon)+`, 'wanted', 0, 0)`)
			if strings.HasSuffix(c, "!") {
				execAll(t, st, `INSERT INTO arrview_removed_version (version_id, view, removed_at) SELECT id, 'ui', 0 FROM version
					WHERE title_id = `+strconv.FormatInt(id, 10)+` AND policy_key = '`+k+`'`)
			}
		}
	}

	// An add to one library of a group gives the others a copy, reported as new and as synced.
	r := movie(1001, "movie-remux")
	if got := copies(t, st, r.TitleID); got != "movie-hd:on movie-remux:on movie-web:on" {
		t.Errorf("new title: %s", got)
	}
	if !slices.Equal(r.Synced, []string{"movie-hd", "movie-web"}) || len(r.New) != 3 || len(r.Versions) != 3 {
		t.Errorf("result %+v", r)
	}
	// Unmonitored add: the partners are unmonitored too.
	r = add(t, st, Input{Kind: policy.KindMovie, TmdbID: 1002, Monitored: false, Versions: fixed("movie-web"), Policy: pol})
	if got := copies(t, st, r.TitleID); got != "movie-hd:off movie-remux:off movie-web:off" {
		t.Errorf("unmonitored add: %s", got)
	}
	// A person's add restores a removed copy in a partner; the third library gets one.
	held(2001, "movie", "movie-web!")
	r = add(t, st, Input{Kind: policy.KindMovie, TitleID: 2001, Monitored: true, Versions: fixed("movie-remux"), Policy: pol})
	if got := copies(t, st, 2001); got != "movie-hd:on movie-remux:on movie-web:on" ||
		!slices.Equal(r.Synced, []string{"movie-hd", "movie-web"}) {
		t.Errorf("removed partner: %s %+v", got, r)
	}
	// An import list's or a collection's add leaves it removed.
	for i, method := range []string{"list", "collection"} {
		id := int64(2006 + i)
		held(id, "movie", "movie-web!")
		r = add(t, st, Input{Kind: policy.KindMovie, TitleID: id, Monitored: true, Versions: fixed("movie-remux"),
			Policy: pol, AddMethod: method})
		if got := copies(t, st, id); got != "movie-hd:on movie-remux:on movie-web:off:removed" ||
			!slices.Equal(r.Synced, []string{"movie-hd"}) {
			t.Errorf("%s with a removed partner: %s %+v", method, got, r)
		}
	}
	// An unmonitored copy in a partner stays unmonitored.
	held(2002, "movie", "movie-web-")
	movie(2002, "movie-remux")
	if got := copies(t, st, 2002); got != "movie-hd:on movie-remux:on movie-web:off" {
		t.Errorf("unmonitored partner: %s", got)
	}
	// Naming the removed library restores it.
	held(2003, "movie", "movie-web!")
	movie(2003, "movie-remux", "movie-web")
	if got := copies(t, st, 2003); got != "movie-hd:on movie-remux:on movie-web:on" {
		t.Errorf("named removed: %s", got)
	}
	// Restoring a removed copy brings the partners it lacks.
	held(2004, "movie", "movie-remux!")
	movie(2004, "movie-remux")
	if got := copies(t, st, 2004); got != "movie-hd:on movie-remux:on movie-web:on" {
		t.Errorf("restored: %s", got)
	}
	// Monitoring an existing unmonitored copy again creates nothing, through Add or TurnOnSynced.
	held(2005, "movie", "movie-remux-")
	if r := movie(2005, "movie-remux"); len(r.Synced) != 0 {
		t.Errorf("monitored again: %+v", r)
	}
	if got := copies(t, st, 2005); got != "movie-remux:on" {
		t.Errorf("monitored again: %s", got)
	}
	execAll(t, st, `UPDATE version SET monitored = 0 WHERE title_id = 2005`)
	if err := st.WriteTx(ctx, func(tx *sql.Tx) error {
		_, partners, err := TurnOnSynced(ctx, tx, pol, 2005, []string{"movie-remux"}, true, true, now)
		if len(partners) != 0 {
			t.Errorf("TurnOnSynced on an existing copy: %v", partners)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// An anime movie named into a library of a non-anime group: no copy where it does not fit.
	yes := true
	r = add(t, st, Input{Kind: policy.KindMovie, TmdbID: 1003, Anime: &yes, Monitored: true, Versions: fixed("movie-remux"),
		Policy: pol})
	if got := copies(t, st, r.TitleID); got != "movie-remux:on" {
		t.Errorf("anime title: %s", got)
	}
	// A library outside every group widens nothing; no policy widens nothing.
	r = movie(1004, "anime-movie")
	if got := copies(t, st, r.TitleID); got != "anime-movie:on" {
		t.Errorf("ungrouped: %s", got)
	}
	r = add(t, st, Input{Kind: policy.KindMovie, TmdbID: 1005, Monitored: true, Versions: fixed("movie-remux")})
	if got := copies(t, st, r.TitleID); got != "movie-remux:on" || r.Synced != nil {
		t.Errorf("no policy: %s %+v", got, r)
	}
}

// A series added to one library with seasons 1 and 2 gets the same seasons, and its monitor-new choice, in the
// library in sync; also when the add's season scope is a compatible view's own library.
func TestSyncGroupsSeriesSeasons(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, _ := setup(t)
	pol := syncPolicy(t)
	for i, scope := range [][]string{nil, {"tv-2160p"}} {
		id := int64(3001 + i)
		ids := strconv.FormatInt(id, 10)
		execAll(t, st, `INSERT INTO title (id, kind, name, tvdb_id, created_at, updated_at) VALUES (`+ids+`, 'series', 'S', `+ids+`, 0, 0)`,
			`INSERT INTO episode (title_id, season, number, air_date_utc_at, created_at, updated_at) VALUES (`+ids+`, 1, 1, 0, 0, 0),
				(`+ids+`, 2, 1, 0, 0, 0), (`+ids+`, 3, 1, 0, 0, 0)`)
		r := add(t, st, Input{Kind: policy.KindSeries, TitleID: id, Monitored: true, Versions: fixed("tv-2160p"),
			SeasonVersions: scope, MonitorNew: MonitorNone, Policy: pol,
			Seasons: []Season{{Number: 1, Monitored: true}, {Number: 2, Monitored: true}, {Number: 3, Monitored: false}}})
		if !slices.Equal(r.Synced, []string{"tv-1080p"}) {
			t.Fatalf("scope %v: %+v", scope, r)
		}
		for _, k := range []string{"tv-2160p", "tv-1080p"} {
			var seasons string
			if err := st.Reader().QueryRow(`SELECT group_concat(e.season, ',') FROM episode_version_state s
				JOIN version v ON v.id = s.version_id JOIN episode e ON e.id = s.episode_id
				WHERE v.title_id = ? AND v.policy_key = ? AND s.monitored = 1 ORDER BY e.season`, id, k).Scan(&seasons); err != nil {
				t.Fatal(err)
			}
			var vid int64
			if err := st.Reader().QueryRow(`SELECT id FROM version WHERE title_id = ? AND policy_key = ?`, id, k).Scan(&vid); err != nil {
				t.Fatal(err)
			}
			m, err := LoadMonitor(ctx, st.Reader(), vid)
			if err != nil {
				t.Fatal(err)
			}
			if seasons != "1,2" || m.New != MonitorNone || !m.Seasons[1] || !m.Seasons[2] || m.Seasons[3] {
				t.Errorf("scope %v %s: monitored seasons %q, intent %+v", scope, k, seasons, m)
			}
		}
	}
}
