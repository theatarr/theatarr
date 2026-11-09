package catalog

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
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
	// The example policy: every version in shadow, router off.
	active, skipped := Searchable(pol, keys)
	if len(active) != 0 || len(skipped) != 3 || !strings.Contains(skipped[0].Reason, "shadow") ||
		!strings.Contains(skipped[2].Reason, "not in the policy") {
		t.Fatalf("all shadow: %v %v", active, skipped)
	}
	// Active but the kill switch off: still nothing.
	pol.Versions["movie-web"].Mode = policy.ModeActive
	active, skipped = Searchable(pol, keys)
	if len(active) != 0 || skipped[1].Version != "movie-web" || !strings.Contains(skipped[1].Reason, "router.enabled") {
		t.Fatalf("router off: %v %v", active, skipped)
	}
	// Router and version grab on: the active version only.
	pol.Versions["movie-web"].Grab = true
	pol.Router = &policy.Router{Enabled: true}
	active, skipped = Searchable(pol, keys)
	if !slices.Equal(active, []string{"movie-web"}) || len(skipped) != 2 {
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

func TestSearchableHonorsVersionGrab(t *testing.T) {
	t.Parallel()
	_, p := setup(t)
	p.Router = &policy.Router{Enabled: true}
	p.Versions["movie-web"].Mode = policy.ModeActive
	p.Versions["movie-web"].Grab = false
	active, skipped := Searchable(p, []string{"movie-web"})
	if len(active) != 0 || len(skipped) != 1 {
		t.Fatalf("grab off: %v %v", active, skipped)
	}
	p.Versions["movie-web"].Grab = true
	p.ForcedOff = []string{"router"}
	active, _ = Searchable(p, []string{"movie-web"})
	if len(active) != 0 {
		t.Fatalf("forced off: %v", active)
	}
}
