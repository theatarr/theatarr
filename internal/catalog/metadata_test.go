package catalog

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

type intakeSource struct {
	collection *tmdb.Collection
	list       *tmdb.List
	movies     map[int]*tmdb.Movie
}

func (f intakeSource) Collection(context.Context, int) (*tmdb.Collection, error) {
	return f.collection, nil
}
func (f intakeSource) List(context.Context, int) (*tmdb.List, error) { return f.list, nil }

func (f intakeSource) MovieDetails(_ context.Context, id int) (*tmdb.Movie, error) {
	if m := f.movies[id]; m != nil {
		return m, nil
	}
	return &tmdb.Movie{ID: id, Title: "Movie " + strconv.Itoa(id), ReleaseDate: "2020-01-01"}, nil
}

func intakeFixture[T any](t *testing.T, v string) *T {
	t.Helper()
	var out T
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		t.Fatal(err)
	}
	return &out
}

func newIntake(t *testing.T, src intakeSource) (*MovieIntake, *store.Store) {
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
	pol := &policy.Policy{Versions: map[string]*policy.VersionPolicy{
		"movie-web": {Kind: policy.KindMovie, Enable: "default", Mode: policy.ModeActive},
	}}
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	return &MovieIntake{Store: st, Policy: pol, TMDb: src, Now: func() time.Time { return now }}, st
}

func TestCollectionRefreshAddsOnceAndHonoursExclusionsAndMonitoring(t *testing.T) {
	t.Parallel()
	src := intakeSource{collection: intakeFixture[tmdb.Collection](t, `{"id":7,"name":"Collection","parts":[{"id":101,"title":"Old","release_date":"2020-01-01"},{"id":102,"title":"Excluded","release_date":"2020-01-01"}]}`)}
	s, st := newIntake(t, src)
	if _, err := st.Writer().Exec(`INSERT INTO movie_collection(tmdb_id,name,monitored,versions,minimum_availability,updated_at) VALUES(7,'Collection',1,'["movie-web"]','released',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().Exec(`INSERT INTO movie_exclusion(tmdb_id,created_at) VALUES(102,0)`); err != nil {
		t.Fatal(err)
	}
	got, err := s.RefreshCollections(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Added != 1 {
		t.Fatalf("added=%d, want 1", got.Added)
	}
	var method string
	if err := st.Reader().QueryRow(`SELECT add_method FROM version`).Scan(&method); err != nil || method != "collection" {
		t.Fatalf("collection add method %q: %v", method, err)
	}
	got, err = s.RefreshCollections(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Added != 0 {
		t.Fatalf("second added=%d, want 0", got.Added)
	}
	var n int
	if err := st.Reader().QueryRow(`SELECT count(*) FROM title`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("titles=%d, want 1", n)
	}
	if _, err := st.Writer().Exec(`UPDATE movie_collection SET monitored=0`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().Exec(`DELETE FROM movie_exclusion`); err != nil {
		t.Fatal(err)
	}
	got, err = s.RefreshCollections(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Added != 0 {
		t.Fatalf("unmonitored added=%d, want 0", got.Added)
	}
}

func TestListSyncIsIdempotent(t *testing.T) {
	t.Parallel()
	src := intakeSource{list: intakeFixture[tmdb.List](t, `{"id":9,"items":[{"id":201,"title":"Movie","release_date":"2020-01-01"}]}`)}
	s, st := newIntake(t, src)
	if _, err := st.Writer().Exec(`INSERT INTO movie_import_list(name,list_type,tmdb_id,enabled,enable_auto,monitor,versions,updated_at,created_at) VALUES('list','tmdb',9,1,1,'movieOnly','["movie-web"]',0,0)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		got, err := s.SyncLists(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && got.Added != 1 {
			t.Fatalf("first added=%d", got.Added)
		}
		if i == 1 && got.Added != 0 {
			t.Fatalf("second added=%d", got.Added)
		}
	}
	var method string
	if err := st.Reader().QueryRow(`SELECT add_method FROM version`).Scan(&method); err != nil || method != "list" {
		t.Fatalf("list add method %q: %v", method, err)
	}
}

func TestIntakeAvailabilityAndSearchOnAdd(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, minimum, cinema, digital string
		search                         bool
	}{
		{"announced without dates", "announced", "", "", true},
		{"future cinema", "inCinemas", "2026-11-01", "", false},
		{"in cinemas", "inCinemas", "2026-10-01", "2026-11-01", true},
		{"digital not yet released", "released", "2026-10-01", "2026-11-01", false},
		{"digital released", "released", "2026-10-01", "2026-10-26", true},
		{"cinema ninety days", "released", "2026-07-01", "", true},
		{"unknown dates", "released", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &tmdb.Movie{ID: 201, Title: "Movie", ReleaseDate: tc.cinema}
			if tc.digital != "" {
				date, err := time.Parse(time.DateOnly, tc.digital)
				if err != nil {
					t.Fatal(err)
				}
				m.ReleaseDates.Results = []tmdb.CountryReleases{{Country: "US", Releases: []tmdb.Release{{Type: 4, Date: date}}}}
			}
			s, st := newIntake(t, intakeSource{list: &tmdb.List{Items: []tmdb.MovieSummary{{ID: 201}}}, movies: map[int]*tmdb.Movie{201: m}})
			if _, err := st.Writer().Exec(`INSERT INTO movie_import_list(name,tmdb_id,versions,minimum_availability,search_on_add,created_at,updated_at) VALUES('list',9,'["movie-web"]',?,1,0,0)`, tc.minimum); err != nil {
				t.Fatal(err)
			}
			searched := 0
			s.OnAdd = func(_ context.Context, id int64, keys []string) error {
				searched++
				if len(keys) != 1 || keys[0] != "movie-web" {
					t.Fatalf("keys=%v", keys)
				}
				return nil
			}
			for i := 0; i < 2; i++ {
				got, err := s.SyncLists(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if i == 0 && got.Added != 1 {
					t.Fatalf("future and released titles must enter catalog: %+v", got)
				}
			}
			want := 0
			if tc.search {
				want = 1
			}
			if searched != want {
				t.Fatalf("searches=%d want=%d", searched, want)
			}
			var titleID, versionID int64
			if err := st.Reader().QueryRow(`SELECT t.id,v.id FROM title t JOIN version v ON v.title_id=t.id`).Scan(&titleID, &versionID); err != nil {
				t.Fatal(err)
			}
			available, err := availability.Movie(context.Background(), st.Reader(), s.Policy, titleID, versionID, "movie-web", s.Now())
			if err != nil {
				t.Fatal(err)
			}
			if available != tc.search {
				t.Fatalf("stored availability=%v want=%v", available, tc.search)
			}
		})
	}
}

func TestListMonitoringExclusionsAndEnableFlags(t *testing.T) {
	t.Parallel()
	src := intakeSource{list: &tmdb.List{Items: []tmdb.MovieSummary{{ID: 201}, {ID: 202}, {ID: 203, MediaType: "tv"}}}}
	s, st := newIntake(t, src)
	if _, err := st.Writer().Exec(`INSERT INTO movie_import_list(name,tmdb_id,versions,monitor,search_on_add,enabled,enable_auto,created_at,updated_at)
  VALUES('manual',9,'["movie-web"]','none',1,1,0,0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().Exec(`INSERT INTO movie_exclusion(tmdb_id,created_at) VALUES(202,0)`); err != nil {
		t.Fatal(err)
	}
	searches := 0
	s.OnAdd = func(context.Context, int64, []string) error { searches++; return nil }
	got, err := s.SyncLists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Lists != 0 || got.Added != 0 {
		t.Fatalf("manual list synced: %+v", got)
	}
	if _, err := st.Writer().Exec(`UPDATE movie_import_list SET enable_auto=1,enabled=0`); err != nil {
		t.Fatal(err)
	}
	got, err = s.SyncLists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Lists != 0 {
		t.Fatal("disabled list synced")
	}
	if _, err := st.Writer().Exec(`UPDATE movie_import_list SET enabled=1`); err != nil {
		t.Fatal(err)
	}
	got, err = s.SyncLists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Added != 1 || searches != 0 {
		t.Fatalf("unmonitored list: %+v, searches=%d", got, searches)
	}
	var monitored bool
	if err := st.Reader().QueryRow(`SELECT monitored FROM title WHERE tmdb_id=201`).Scan(&monitored); err != nil {
		t.Fatal(err)
	}
	if monitored {
		t.Fatal("monitor none added a monitored title")
	}
	if _, err := st.Writer().Exec(`UPDATE movie_import_list SET monitor='movieOnly'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncLists(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := st.Reader().QueryRow(`SELECT monitored FROM title WHERE tmdb_id=201`).Scan(&monitored); err != nil {
		t.Fatal(err)
	}
	if monitored {
		t.Fatal("sync changed existing monitoring")
	}
	var count int
	if err := st.Reader().QueryRow(`SELECT count(*) FROM title_external_id WHERE source='tmdb_collection'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("list id used as collection membership")
	}
}

func TestCollectionDiscoveryAndNewMembership(t *testing.T) {
	t.Parallel()
	src := intakeSource{collection: &tmdb.Collection{ID: 7, Name: "Collection", Parts: []tmdb.MovieSummary{{ID: 101}, {ID: 102}}}}
	s, st := newIntake(t, src)
	ctx := context.Background()
	var existing *Result
	if err := st.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		existing, err = Add(ctx, tx, Input{Kind: policy.KindMovie, TmdbID: 101, Versions: func(bool) ([]string, error) { return []string{"movie-web"}, nil }, Monitored: true}, s.Now(), nil)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO title_external_id(title_id,source,external_id) VALUES(?,'tmdb_collection','7')`, existing.TitleID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.RefreshCollections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Collections != 1 || got.Added != 0 {
		t.Fatalf("discovery enabled collection: %+v", got)
	}
	var name, movies, versions string
	if err := st.Reader().QueryRow(`SELECT name,movies,versions FROM movie_collection WHERE tmdb_id=7`).Scan(&name, &movies, &versions); err != nil {
		t.Fatal(err)
	}
	var parts []tmdb.MovieSummary
	if err := json.Unmarshal([]byte(movies), &parts); err != nil {
		t.Fatal(err)
	}
	if name != "Collection" || len(parts) != 2 || versions != `["movie-web"]` {
		t.Fatalf("metadata: %q %s %s", name, movies, versions)
	}
	if _, err := st.Writer().Exec(`UPDATE movie_collection SET monitored=1,search_on_add=1`); err != nil {
		t.Fatal(err)
	}
	searches := 0
	s.OnAdd = func(context.Context, int64, []string) error { searches++; return nil }
	got, err = s.RefreshCollections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Added != 1 || searches != 1 {
		t.Fatalf("new membership: %+v searches=%d", got, searches)
	}
	var membership string
	if err := st.Reader().QueryRow(`SELECT x.external_id FROM title_external_id x JOIN title t ON t.id=x.title_id WHERE t.tmdb_id=102 AND x.source='tmdb_collection'`).Scan(&membership); err != nil {
		t.Fatal(err)
	}
	if membership != "7" {
		t.Fatalf("membership=%s", membership)
	}
	got, err = s.RefreshCollections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Added != 0 || searches != 1 {
		t.Fatalf("refresh repeated add/search: %+v %d", got, searches)
	}
}

func TestMovieAndCollectionListMonitoring(t *testing.T) {
	t.Parallel()
	m := intakeFixture[tmdb.Movie](t, `{"id":201,"title":"Movie","release_date":"2020-01-01","belongs_to_collection":{"id":7,"name":"Collection"}}`)
	s, st := newIntake(t, intakeSource{list: &tmdb.List{Items: []tmdb.MovieSummary{{ID: 201}}}, movies: map[int]*tmdb.Movie{201: m}})
	if _, err := st.Writer().Exec(`INSERT INTO movie_import_list(name,tmdb_id,versions,monitor,created_at,updated_at) VALUES('list',9,'["movie-web"]','movieAndCollection',0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncLists(context.Background()); err != nil {
		t.Fatal(err)
	}
	var monitored bool
	var versions string
	if err := st.Reader().QueryRow(`SELECT monitored,versions FROM movie_collection WHERE tmdb_id=7`).Scan(&monitored, &versions); err != nil {
		t.Fatal(err)
	}
	if !monitored || versions != `["movie-web"]` {
		t.Fatalf("collection monitored=%v versions=%s", monitored, versions)
	}
}

// A monitored collection adds only to its stored targets that are collection targets (active movie versions nothing
// follows): a shadow or followed target is skipped and logged once, and with none left (or an empty list) nothing is
// added. A movieAndCollection list monitors the collection only in such targets.
func TestCollectionTargetsRevalidated(t *testing.T) {
	t.Parallel()
	src := intakeSource{collection: &tmdb.Collection{ID: 7, Name: "Collection", Parts: []tmdb.MovieSummary{{ID: 101}}}}
	s, st := newIntake(t, src)
	var logs bytes.Buffer
	s.Log = slog.New(slog.NewTextHandler(&logs, nil))
	s.Policy.Versions["movie-remux"] = &policy.VersionPolicy{Kind: policy.KindMovie, Mode: policy.ModeShadow}
	s.Policy.Versions["movie-4k"] = &policy.VersionPolicy{Kind: policy.KindMovie, Mode: policy.ModeActive}
	s.Policy.Versions["movie-4k-theatarr"] = &policy.VersionPolicy{Kind: policy.KindMovie, Mode: policy.ModeActive, Follows: "movie-4k"}
	ctx := context.Background()
	for _, versions := range []string{`[]`, `["movie-remux"]`, `["movie-4k"]`} {
		if _, err := st.Writer().Exec(`DELETE FROM movie_collection`); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Writer().Exec(`INSERT INTO movie_collection(tmdb_id,name,monitored,versions,updated_at) VALUES(7,'Collection',1,?,0)`, versions); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			got, err := s.RefreshCollections(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got.Added != 0 {
				t.Fatalf("%s: added %+v", versions, got)
			}
		}
	}
	if n := strings.Count(logs.String(), "targets skipped"); n != 3 {
		t.Errorf("%d skip logs, want 3 (once per collection and targets):\n%s", n, logs.String())
	}
	if _, err := st.Writer().Exec(`UPDATE movie_collection SET versions='["movie-4k-theatarr","movie-remux","movie-web"]'`); err != nil {
		t.Fatal(err)
	}
	if got, err := s.RefreshCollections(ctx); err != nil || got.Added != 1 {
		t.Fatalf("eligible target: %+v %v", got, err)
	}
	var keys string
	if err := st.Reader().QueryRow(`SELECT group_concat(policy_key, ',') FROM (SELECT policy_key FROM version ORDER BY policy_key)`).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if keys != "movie-4k-theatarr,movie-web" {
		t.Errorf("versions %s, want movie-4k-theatarr,movie-web", keys)
	}

	// movieAndCollection: the list's shadow target does not become the collection's.
	m := intakeFixture[tmdb.Movie](t, `{"id":201,"title":"Movie","release_date":"2020-01-01","belongs_to_collection":{"id":8,"name":"Other"}}`)
	s.TMDb = intakeSource{list: &tmdb.List{Items: []tmdb.MovieSummary{{ID: 201}}}, movies: map[int]*tmdb.Movie{201: m}}
	if _, err := st.Writer().Exec(`INSERT INTO movie_import_list(name,tmdb_id,versions,monitor,created_at,updated_at) VALUES('list',9,'["movie-remux","movie-web"]','movieAndCollection',0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncLists(ctx); err != nil {
		t.Fatal(err)
	}
	var versions string
	if err := st.Reader().QueryRow(`SELECT versions FROM movie_collection WHERE tmdb_id=8`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != `["movie-web"]` {
		t.Errorf("list collection versions %s", versions)
	}
}

type scheduledIntakeSource struct {
	intakeSource
	refreshed chan struct{}
}

func (f *scheduledIntakeSource) List(ctx context.Context, id int) (*tmdb.List, error) {
	select {
	case f.refreshed <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return f.list, nil
}

func TestIntakeScheduleAndCancellation(t *testing.T) {
	s, st := newIntake(t, intakeSource{})
	src := &scheduledIntakeSource{intakeSource: intakeSource{list: &tmdb.List{}}, refreshed: make(chan struct{}, 2)}
	s.TMDb = src
	if _, err := st.Writer().Exec(`INSERT INTO movie_import_list(name,tmdb_id,created_at,updated_at) VALUES('List',9,0,0)`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx, time.Millisecond) }()
	for i := 0; i < 2; i++ {
		select {
		case <-src.refreshed:
		case <-time.After(5 * time.Second):
			t.Fatal("scheduled sync did not run")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("intake did not stop")
	}
}

func TestIntakeAnimeTargets(t *testing.T) {
	t.Parallel()
	s, st := newIntake(t, intakeSource{list: &tmdb.List{Items: []tmdb.MovieSummary{{ID: 201}}}})
	anime := true
	s.Policy.Versions["anime-movie"] = &policy.VersionPolicy{Kind: policy.KindMovie, Anime: &anime, Library: "/anime"}
	if _, err := st.Writer().Exec(`INSERT INTO movie_import_list(name,tmdb_id,versions,root_folder_path,created_at,updated_at) VALUES('List',9,'["anime-movie"]','/anime',0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncLists(context.Background()); err != nil {
		t.Fatal(err)
	}
	var isAnime bool
	var key string
	if err := st.Reader().QueryRow(`SELECT t.anime,v.policy_key FROM title t JOIN version v ON v.title_id=t.id WHERE t.tmdb_id=201`).Scan(&isAnime, &key); err != nil {
		t.Fatal(err)
	}
	if !isAnime || key != "anime-movie" {
		t.Fatalf("anime=%v version=%s", isAnime, key)
	}
}

// RefreshCollection is the scheduled refresh for one collection: it stores name and parts, adds a monitored
// collection's missing movies, adds nothing while unmonitored, and leaves other collections alone.
func TestRefreshOneCollection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := intakeSource{collection: intakeFixture[tmdb.Collection](t, `{"id":7,"name":"Collection","parts":[{"id":101,"title":"One","release_date":"2020-01-01"},{"id":102,"title":"Two","release_date":"2021-01-01"}]}`)}
	s, st := newIntake(t, src)
	if _, err := st.Writer().Exec(`INSERT INTO movie_collection(tmdb_id,monitored,versions,updated_at) VALUES(7,0,'["movie-web"]',0),(8,1,'["movie-web"]',0)`); err != nil {
		t.Fatal(err)
	}
	got, err := s.RefreshCollection(ctx, 7)
	if err != nil || got != (IntakeResult{Collections: 1}) {
		t.Fatalf("unmonitored: %+v %v", got, err)
	}
	var name, movies string
	var refreshed sql.NullInt64
	if err := st.Reader().QueryRow(`SELECT name,movies,refreshed_at FROM movie_collection WHERE tmdb_id=7`).Scan(&name, &movies, &refreshed); err != nil ||
		name != "Collection" || !strings.Contains(movies, `"id":102`) || !refreshed.Valid {
		t.Fatalf("stored %q %q %v: %v", name, movies, refreshed, err)
	}
	// The other collection was not touched.
	if err := st.Reader().QueryRow(`SELECT name FROM movie_collection WHERE tmdb_id=8`).Scan(&name); err != nil || name != "" {
		t.Fatalf("collection 8 refreshed: %q %v", name, err)
	}
	if _, err := st.Writer().Exec(`UPDATE movie_collection SET monitored=1 WHERE tmdb_id=7`); err != nil {
		t.Fatal(err)
	}
	if got, err = s.RefreshCollection(ctx, 7); err != nil || got != (IntakeResult{Collections: 1, Added: 2}) {
		t.Fatalf("monitored: %+v %v", got, err)
	}
	if got, err = s.RefreshCollection(ctx, 7); err != nil || got != (IntakeResult{Collections: 1, Skipped: 2}) {
		t.Fatalf("again: %+v %v", got, err)
	}
	if _, err := s.RefreshCollection(ctx, 99); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown collection: %v", err)
	}
}

// SyncList imports one list by hand, enabled or not: a native row (no view, explicit versions) adds its movies to
// those versions, and last_sync_at moves only on a pass without failures.
func TestSyncOneList(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := intakeSource{list: intakeFixture[tmdb.List](t, `{"id":9,"items":[{"id":201,"title":"Movie","release_date":"2020-01-01"},{"id":202,"title":"Show","media_type":"tv"}]}`)}
	s, st := newIntake(t, src)
	if _, err := st.Writer().Exec(`INSERT INTO movie_import_list(id,name,list_type,tmdb_id,enabled,enable_auto,monitor,versions,updated_at,created_at)
		VALUES(1,'off','tmdb',9,0,0,'movieOnly','["movie-web"]',0,0),(2,'other','tmdb',10,1,1,'movieOnly','["movie-web"]',0,0)`); err != nil {
		t.Fatal(err)
	}
	got, err := s.SyncList(ctx, 1)
	if err != nil || got != (IntakeResult{Lists: 1, Added: 1, Skipped: 1}) {
		t.Fatalf("sync: %+v %v", got, err)
	}
	var key, view string
	if err := st.Reader().QueryRow(`SELECT v.policy_key, a.view FROM version v JOIN arrview_title a ON a.title_id=v.title_id`).Scan(&key, &view); err != nil ||
		key != "movie-web" || view != "v/movie-web" {
		t.Fatalf("added to %q in view %q: %v", key, view, err)
	}
	var synced, other sql.NullInt64
	if err := st.Reader().QueryRow(`SELECT (SELECT last_sync_at FROM movie_import_list WHERE id=1), (SELECT last_sync_at FROM movie_import_list WHERE id=2)`).Scan(&synced, &other); err != nil ||
		!synced.Valid || other.Valid {
		t.Fatalf("last_sync_at %v %v: %v", synced, other, err)
	}
	if _, err := s.SyncList(ctx, 99); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown list: %v", err)
	}
	// A failing movie keeps last_sync_at where it was.
	if _, err := st.Writer().Exec(`UPDATE movie_import_list SET last_sync_at=NULL, versions='["nope"]' WHERE id=1; DELETE FROM arrview_title; DELETE FROM version; DELETE FROM title`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncList(ctx, 1); err == nil {
		t.Fatal("a list with no usable version synced cleanly")
	}
	if err := st.Reader().QueryRow(`SELECT last_sync_at FROM movie_import_list WHERE id=1`).Scan(&synced); err != nil || synced.Valid {
		t.Fatalf("last_sync_at after a failed pass %v: %v", synced, err)
	}
}

type failingListSource struct{ intakeSource }

func (failingListSource) List(context.Context, int) (*tmdb.List, error) {
	return nil, errors.New("tmdb: GET /list/9?page=2: HTTP 502")
}

// A list TMDb did not give in full (a later page failed, so the client returns no list) is not a sync: the manual
// and the scheduled pass report it, add nothing and leave last_sync_at alone.
func TestListReadFailureIsNotASync(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, st := newIntake(t, intakeSource{})
	s.TMDb = failingListSource{}
	if _, err := st.Writer().Exec(`INSERT INTO movie_import_list(id,name,list_type,tmdb_id,enabled,enable_auto,monitor,versions,last_sync_at,updated_at,created_at)
		VALUES(1,'long','tmdb',9,1,1,'movieOnly','["movie-web"]',5,0,0)`); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SyncList(ctx, 1); err == nil || got.Added != 0 {
		t.Fatalf("manual sync of an unreadable list: %+v %v", got, err)
	}
	if got, err := s.SyncLists(ctx); err == nil || got.Added != 0 {
		t.Fatalf("scheduled sync of an unreadable list: %+v %v", got, err)
	}
	var synced int64
	if err := st.Reader().QueryRow(`SELECT last_sync_at FROM movie_import_list WHERE id=1`).Scan(&synced); err != nil || synced != 5 {
		t.Fatalf("last_sync_at %d: %v", synced, err)
	}
}
