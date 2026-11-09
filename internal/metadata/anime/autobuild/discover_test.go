package autobuild

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func openStore(t *testing.T) *store.Store {
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
	return st
}

func exec(t *testing.T, st *store.Store, q string, args ...any) {
	t.Helper()
	if _, err := st.Writer().Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// Invented list records: TVDB 500 has seasons 1 and 2, 600 seasons 2 and 3 (no season 1), 700 is absolute, 800 has
// an AniDB id no list joins to AniList, 900 is not in ScudLee.
const scudXML = `<anime-list>
<anime anidbid="100" tvdbid="500" defaulttvdbseason="1" episodeoffset=""><name>Show One</name></anime>
<anime anidbid="101" tvdbid="500" defaulttvdbseason="2" episodeoffset=""><name>Show One II</name></anime>
<anime anidbid="200" tvdbid="600" defaulttvdbseason="1" episodeoffset="12"><name>Show Two part 2</name></anime>
<anime anidbid="201" tvdbid="600" defaulttvdbseason="3" episodeoffset=""><name>Show Two III</name></anime>
<anime anidbid="202" tvdbid="600" defaulttvdbseason="2" episodeoffset=""><name>Show Two II</name></anime>
<anime anidbid="300" tvdbid="700" defaulttvdbseason="a" episodeoffset=""><name>Show Three</name></anime>
<anime anidbid="301" tvdbid="700" defaulttvdbseason="0" episodeoffset=""><name>Show Three OVA</name></anime>
<anime anidbid="400" tvdbid="800" defaulttvdbseason="1" episodeoffset=""><name>Show Four</name></anime>
<anime anidbid="401" tvdbid="movie" defaulttvdbseason="1" episodeoffset=""><name>Movie</name></anime>
</anime-list>`

const fribbJSON = `[{"anidb_id":100,"anilist_id":1000},{"anidb_id":101,"anilist_id":1001},{"anidb_id":200,"anilist_id":2000},
{"anidb_id":201,"anilist_id":2001},{"anidb_id":202,"anilist_id":2002},{"anidb_id":300,"anilist_id":3000},
{"anidb_id":301,"anilist_id":3001},{"anidb_id":400}]`

func testLists(t *testing.T) *anime.Lists {
	t.Helper()
	l := &anime.Lists{}
	if err := l.ParseScudLee(strings.NewReader(scudXML)); err != nil {
		t.Fatal(err)
	}
	if err := l.ParseFribb(strings.NewReader(fribbJSON)); err != nil {
		t.Fatal(err)
	}
	return l
}

// fakeAniList answers start dates and counts its calls.
type fakeAniList struct {
	start map[int][3]int
	calls int
}

func (f *fakeAniList) Media(_ context.Context, ids []int) ([]*anime.Media, error) {
	f.calls++
	var out []*anime.Media
	for _, id := range ids {
		m := &anime.Media{ID: id}
		if d, ok := f.start[id]; ok {
			y, mo, da := d[0], d[1], d[2]
			m.StartDate = anime.FuzzyDate{Year: &y, Month: &mo, Day: &da}
		}
		out = append(out, m)
	}
	return out, nil
}

func discoverer(t *testing.T, st *store.Store, al anime.AniList, shipped ...anime.Franchise) *Discoverer {
	l := testLists(t)
	return &Discoverer{Store: st, Lists: l, AniList: al, Shipped: shipped, index: scudLeeByTVDB(l)}
}

func TestDiscoverSeeds(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	al := &fakeAniList{start: map[int][3]int{2000: {2019, 1, 5}, 2001: {2021, 4, 1}, 2002: {2018, 10, 3}}}
	d := discoverer(t, st, al)

	// season 1 with offset 0 is the seed; no AniList call
	s, err := d.Discover(ctx, 500, "Show One")
	if err != nil || s.Outcome != "" || s.Franchise.SeedAniList != 1000 || s.Franchise.Slug != "show-one" ||
		s.Franchise.Name != "Show One" || s.Origin != OriginAuto || s.Franchise.Scope != "" {
		t.Fatalf("season 1: %+v %v", s, err)
	}
	if al.calls != 0 {
		t.Errorf("AniList called for a season-1 pick")
	}
	// absolute ("a") counts as season 1
	if s, err := d.Discover(ctx, 700, "Show Three"); err != nil || s.Franchise.SeedAniList != 3000 {
		t.Fatalf("absolute: %+v %v", s, err)
	}
	// no season 1 at offset 0 (200 is season 1 at offset 12): the earliest AniList start
	if s, err := d.Discover(ctx, 600, "Show Two"); err != nil || s.Franchise.SeedAniList != 2002 || al.calls != 1 {
		t.Fatalf("earliest: %+v %v (calls %d)", s, err, al.calls)
	}
	// no AniList id for the AniDB id, or no ScudLee record at all
	for _, tvdb := range []int{800, 900} {
		if s, err := d.Discover(ctx, tvdb, "Nothing"); err != nil || s.Outcome != OutcomeNoMapping {
			t.Fatalf("%d: %+v %v", tvdb, s, err)
		}
	}
}

func TestDiscoverShippedListFirst(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	shipped := anime.Franchise{Slug: "the-show", Name: "The Show", SeedAniList: 1001, Scope: "stem"}
	d := discoverer(t, st, &fakeAniList{}, anime.Franchise{Slug: "other", SeedAniList: 9}, shipped)
	s, err := d.Discover(ctx, 500, "Show One")
	if err != nil || s.Franchise.Slug != "the-show" || s.Franchise.Scope != "stem" || s.Origin != OriginList {
		t.Fatalf("%+v %v", s, err)
	}
	// once that franchise exists, the series is covered by it
	exec(t, st, `INSERT INTO anime_franchise (slug, name, seed_anilist, created_at, updated_at) VALUES ('the-show', 'The Show', 1001, 0, 0)`)
	if s, err := d.Discover(ctx, 500, "Show One"); err != nil || s.Outcome != OutcomeCovered || s.CoveredBy != "the-show" {
		t.Fatalf("covered: %+v %v", s, err)
	}
}

func TestDiscoverCoveredAndSlugCollision(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	d := discoverer(t, st, &fakeAniList{})
	exec(t, st, `INSERT INTO anime_franchise (slug, name, seed_anilist, created_at, updated_at) VALUES
		('big', 'Big', 1, 0, 0), ('show-one', 'Show One', 77, 0, 0)`)
	// another franchise maps into TVDB 600
	exec(t, st, `INSERT INTO anime_mapping (franchise, entry_key, status, tvdb_series, updated_at) VALUES ('big', 'al:5', 'conflict', 600, 0)`)
	if s, err := d.Discover(ctx, 600, "Show Two"); err != nil || s.Outcome != OutcomeCovered || s.CoveredBy != "big" {
		t.Fatalf("mapping: %+v %v", s, err)
	}
	// another franchise has an entry for one of the series' AniList ids (its mapping was rejected)
	exec(t, st, `INSERT INTO anime_entry (franchise, key, origin, anilist_id, body) VALUES ('big', 'al:3001', 'anilist', 3001, '{}')`)
	if s, err := d.Discover(ctx, 700, "Show Three"); err != nil || s.Outcome != OutcomeCovered || s.CoveredBy != "big" {
		t.Fatalf("entry: %+v %v", s, err)
	}
	// show-one is taken by another franchise: the TVDB id makes the slug unique
	if s, err := d.Discover(ctx, 500, "Show One"); err != nil || s.Franchise.Slug != "show-one-500" {
		t.Fatalf("collision: %+v %v", s, err)
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Re:Zero - Starting Life in Another World": "re-zero-starting-life-in-another-world",
		"Pokémon":                            "pokemon",
		"Frieren: Beyond Journey's End":      "frieren-beyond-journeys-end",
		"葬送のフリーレン":                           "",
		"  JoJo's Bizarre Adventure (2012) ": "jojos-bizarre-adventure-2012",
	} {
		if got := Slugify(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
