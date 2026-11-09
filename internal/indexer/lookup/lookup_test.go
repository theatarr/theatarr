package lookup

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func testDB(t *testing.T) *store.Store {
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
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, tvdb_id, imdb_id, created_at, updated_at) VALUES (1, 'series', 'Show', 100, 'tt0000100', 0, 0)`,
		`INSERT INTO title (id, kind, name, tmdb_id, imdb_id, created_at, updated_at) VALUES (2, 'movie', 'Film', 149, 'tt0000149', 0, 0)`,
		`INSERT INTO title (id, kind, name, tvdb_id, created_at, updated_at) VALUES (3, 'series', 'Other', 300, 0, 0)`,
		`INSERT INTO title (id, kind, name, tmdb_id, imdb_id, created_at, updated_at) VALUES (4, 'movie', 'Plain', 500, 'tt0000500', 0, 0)`,
		`INSERT INTO title_external_id (title_id, source, external_id) VALUES (3, 'anilist', '777'), (4, 'anilist', '888')`,
		`INSERT INTO episode (title_id, season, number, created_at, updated_at) VALUES (1, 0, 1, 0, 0), (1, 1, 1, 0, 0),
			(1, 1, 2, 0, 0), (1, 2, 1, 0, 0)`,
		`INSERT INTO anime_franchise (slug, name, seed_anilist, created_at, updated_at) VALUES ('f', 'F', 11, 0, 0)`,
		`INSERT INTO anime_entry (franchise, key, origin, anilist_id, tmdb_movie, body) VALUES
			('f', 'al:11', 'anilist', 11, '[]', '{}'), ('f', 'al:12', 'anilist', 12, '[]', '{}'),
			('f', 'al:13', 'anilist', 13, '[149]', '{}')`,
		`INSERT INTO anime_mapping (franchise, entry_key, status, tvdb_series, episodes, updated_at) VALUES
			('f', 'al:11', 'auto', 100, '[[1,1],[1,2]]', 0), ('f', 'al:12', 'conflict', 100, '[[2,1]]', 0)`,
	} {
		if _, err := st.Writer().Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return st
}

func TestDBAniList(t *testing.T) {
	db := DB{DB: testDB(t).Reader()}
	ctx := context.Background()
	for name, c := range map[string]struct {
		q    indexer.Query
		want []int
	}{
		"season 1":                {indexer.Query{Type: indexer.QuerySeries, TVDB: 100, Season: 1}, []int{11}},
		"season 2 (conflict too)": {indexer.Query{Type: indexer.QuerySeries, TVDB: 100, Season: 2}, []int{12}},
		"unmapped season":         {indexer.Query{Type: indexer.QuerySeries, TVDB: 100, Season: 3}, nil},
		"series' own id":          {indexer.Query{Type: indexer.QuerySeries, TVDB: 300, Season: 4}, []int{777}},
		"movie by tmdb":           {indexer.Query{Type: indexer.QueryMovie, TMDb: 149}, []int{13}},
		"movie by imdb":           {indexer.Query{Type: indexer.QueryMovie, IMDb: "tt0000149"}, []int{13}},
		"movie's own id":          {indexer.Query{Type: indexer.QueryMovie, TMDb: 500}, []int{888}},
		"unknown":                 {indexer.Query{Type: indexer.QueryMovie, TMDb: 1}, nil},
		"no tvdb":                 {indexer.Query{Type: indexer.QuerySeries, IMDb: "tt0000100", Season: 1}, nil},
	} {
		got, err := db.AniListIDs(ctx, c.q)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s: %v %v, want %v", name, got, err, c.want)
		}
	}
}

func TestTorrentNamesUseProviderInventoryForExactHash(t *testing.T) {
	st := testDB(t)
	hash := strings.Repeat("a", 40)
	if _, err := st.Writer().Exec(`INSERT INTO content(id,info_hash,name,first_seen_at,last_seen_at)
		VALUES(99,?,'Synthetic 1080p remux release',0,0)`, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().Exec(`INSERT INTO content_provider(content_id,provider,torrent_id,folder_name,state,seen_at,progress)
		VALUES(99,'ad','a','[Arid] Show BDRip 1280x720','present',0,100),
		(99,'tb','b','Show BD 720p','present',0,100),(99,'rd','c','Old name 1080p','absent',0,100),(99,'pending','d','Show BD 1080p','present',0,0)`); err != nil {
		t.Fatal(err)
	}
	db := DB{DB: st.Reader()}
	names, err := db.TorrentNames(context.Background(), strings.ToUpper(hash))
	if err != nil || !slices.Equal(names, []string{"Show BD 720p", "[Arid] Show BDRip 1280x720"}) {
		t.Fatalf("provider names: %v %v", names, err)
	}
	if names, err := db.TorrentNames(context.Background(), strings.Repeat("b", 40)); err != nil || len(names) != 0 {
		t.Fatalf("another hash borrowed quality evidence: %v %v", names, err)
	}
}

func TestDBEpisodeCounts(t *testing.T) {
	db := DB{DB: testDB(t).Reader()}
	ctx := context.Background()
	for _, q := range []indexer.Query{{Type: indexer.QuerySeries, TVDB: 100}, {Type: indexer.QuerySeries, IMDb: "tt0000100"}} {
		m, err := db.EpisodeCounts(ctx, q)
		if err != nil || len(m) != 3 || m[0] != 1 || m[1] != 2 || m[2] != 1 {
			t.Errorf("%+v: %v %v", q, m, err)
		}
	}
	if m, _ := db.EpisodeCounts(ctx, indexer.Query{Type: indexer.QuerySeries, TVDB: 300}); len(m) != 0 {
		t.Errorf("no episodes: %v", m)
	}
}

func TestSeasonAniList(t *testing.T) {
	am := animemap.New(100, []*animemap.Entry{
		{Key: "al:11", Episodes: []anime.SE{{1, 1}, {1, 12}}},
		{Key: "al:12#2", Episodes: []anime.SE{{1, 13}, {2, 1}}},
		{Key: "adb:99", Episodes: []anime.SE{{1, 5}}},
		{Key: "al:13", Episodes: []anime.SE{{0, 1}}},
		{Key: "al:11", Episodes: []anime.SE{{1, 2}}},
	}, nil)
	if got := SeasonAniList(am, 1); !slices.Equal(got, []int{11, 12}) {
		t.Errorf("season 1: %v", got)
	}
	if got := SeasonAniList(am, 2); !slices.Equal(got, []int{12}) {
		t.Errorf("season 2: %v", got)
	}
	if SeasonAniList(am, 0) != nil || SeasonAniList(nil, 1) != nil || SeasonAniList(am, 3) != nil {
		t.Error("specials, no map or an unmapped season must give no ids")
	}
}

func TestAnibridge(t *testing.T) {
	l := &anime.Lists{}
	err := l.ParseAnibridge(strings.NewReader(`{"$meta": {"version": 3},
		"anilist:154587": {"tvdb_show:424536:s1": {"1-28": "1-28"}, "tmdb_show:209867:s1": {"1-28": "1-28"}},
		"anilist:182255": {"tvdb_show:424536:s2": {"1-10": "1-10"}},
		"anilist:999": {"tvdb_show:424536:s1": {"29-": "29-"}},
		"anilist:47": {"tmdb_movie:149": {"1": "1"}},
		"anidb:1": {"tvdb_show:5:s1": {"1": "1"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	a := NewAnibridge(l)
	ctx := context.Background()
	for _, c := range []struct {
		q    indexer.Query
		want []int
	}{
		{indexer.Query{Type: indexer.QuerySeries, TVDB: 424536, Season: 1}, []int{999, 154587}},
		{indexer.Query{Type: indexer.QuerySeries, TVDB: 424536, Season: 2}, []int{182255}},
		{indexer.Query{Type: indexer.QuerySeries, TVDB: 424536, Season: 0}, nil},
		{indexer.Query{Type: indexer.QueryMovie, TMDb: 149}, []int{47}},
		{indexer.Query{Type: indexer.QuerySeries, TVDB: 5, Season: 1}, nil},
	} {
		if got, _ := a.AniListIDs(ctx, c.q); !slices.Equal(got, c.want) {
			t.Errorf("%+v: %v, want %v", c.q, got, c.want)
		}
	}
	// The other way round, for SeaDex's recent feed.
	for al, want := range map[int][]indexer.AniListTarget{
		154587: {{TVDB: 424536, Season: 1}},
		182255: {{TVDB: 424536, Season: 2}},
		47:     {{TMDb: 149}},
		1:      nil,
	} {
		if got, _ := a.AniListTargets(ctx, al); !slices.Equal(got, want) {
			t.Errorf("targets of %d: %v, want %v", al, got, want)
		}
	}
}

// AniList ids map back to the seasons and movies they are filed under, and our titles have names.
func TestDBAniListTargetsAndNames(t *testing.T) {
	db := DB{DB: testDB(t).Reader()}
	ctx := context.Background()
	for al, want := range map[int][]indexer.AniListTarget{
		11:  {{TVDB: 100, Season: 1, Exact: true}}, // exact episode mappings summarized by season
		12:  {{TVDB: 100, Season: 2, Exact: true}},
		13:  {{TMDb: 149}},
		777: {{TVDB: 300, Season: 1}}, // a series' own id
		888: {{TMDb: 500}},
		5:   nil,
	} {
		if got, err := db.AniListTargets(ctx, al); err != nil || !slices.Equal(got, want) {
			t.Errorf("targets of %d: %v %v, want %v", al, got, err, want)
		}
	}
	for _, c := range []struct {
		q    indexer.Query
		name string
		ok   bool
	}{
		{indexer.Query{Type: indexer.QuerySeries, TVDB: 100, Season: 1}, "Show", true},
		{indexer.Query{Type: indexer.QueryMovie, TMDb: 149}, "Film", true},
		{indexer.Query{Type: indexer.QueryMovie, IMDb: "tt0000500"}, "Plain", true},
		{indexer.Query{Type: indexer.QuerySeries, TVDB: 9}, "", false},
		{indexer.Query{Type: indexer.QueryMovie, TMDb: 100}, "", false}, // a series' id is not a movie's
	} {
		name, _, ok, err := db.TitleName(ctx, c.q)
		if err != nil || name != c.name || ok != c.ok {
			t.Errorf("%+v: %q %v %v", c.q, name, ok, err)
		}
	}
}

func TestDailyReload(t *testing.T) {
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	loads := 0
	fail := false
	d := &Daily{Now: func() time.Time { return now }, Load: func(context.Context) (*Anibridge, error) {
		loads++
		if fail {
			return nil, errors.New("offline")
		}
		return &Anibridge{movies: map[int][]int{149: {loads}}}, nil
	}}
	ctx := context.Background()
	q := indexer.Query{Type: indexer.QueryMovie, TMDb: 149}
	if ids, err := d.AniListIDs(ctx, q); err != nil || !slices.Equal(ids, []int{1}) {
		t.Fatalf("%v %v", ids, err)
	}
	now = now.Add(time.Hour)
	d.AniListIDs(ctx, q)
	if loads != 1 {
		t.Errorf("reloaded within the day: %d", loads)
	}
	now = now.Add(24 * time.Hour)
	fail = true
	if ids, err := d.AniListIDs(ctx, q); err != nil || !slices.Equal(ids, []int{1}) {
		t.Errorf("a failed reload must keep the old index: %v %v", ids, err)
	}
	now = now.Add(time.Minute)
	d.AniListIDs(ctx, q)
	if loads != 2 {
		t.Errorf("retried at once after a failure: %d loads", loads)
	}
	fail = false
	now = now.Add(11 * time.Minute)
	if ids, _ := d.AniListIDs(ctx, q); !slices.Equal(ids, []int{3}) {
		t.Errorf("after the retry pause: %v", ids)
	}
}
