package decide

import (
	"context"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/indexer/lookup"
	"github.com/theatarr/theatarr/internal/policy"
)

// An entry mapped to S01E01-E02 only, whose stored targets come from the lookup, is no curated
// alternative of an S01E13 file: that file is not searched for it.
func TestExactMappingDoesNotSearchOtherCour(t *testing.T) {
	t.Parallel()
	st := linkedDB(t)
	for _, q := range []string{
		`INSERT INTO title(id,kind,name,tvdb_id,created_at,updated_at) VALUES(1,'series','Show',100,0,0)`,
		`INSERT INTO anime_franchise(slug,name,seed_anilist,created_at,updated_at) VALUES('f','F',11,0,0)`,
		`INSERT INTO anime_entry(franchise,key,origin,anilist_id,tmdb_movie,body) VALUES('f','al:11','anilist',11,'[]','{}')`,
		`INSERT INTO anime_mapping(franchise,entry_key,status,tvdb_series,episodes,updated_at) VALUES('f','al:11','auto',100,'[[1,1],[1,2]]',0)`,
	} {
		seadexExec(t, st, q)
	}
	ctx := context.Background()
	targets, err := (lookup.DB{DB: st.Reader()}).AniListTargets(ctx, 11)
	if err != nil {
		t.Fatal(err)
	}
	seadexSnapshotRow(t, st, factsSnapshot(indexer.SeaDexSnapshot{AniList: 11, Targets: targets,
		Torrents: []indexer.SeaDexStanding{{InfoHash: strings.Repeat("a", 40), Quality: "Remux-2160p", IsBest: true, Preference: 2}}}))
	ctx, err = WithStoredSeaDex(ctx, st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithSeaDexTitle(WithSeaDexPolicy(ctx, nil, &policy.VersionPolicy{PreferSeaDex: true}), 1)
	other := &ExistingFile{Quality: "Remux-2160p", Revision: 1, InfoHash: strings.Repeat("b", 40), seaDexEpisodes: [][2]int{{1, 13}}}
	mapped := &ExistingFile{Quality: "Remux-2160p", Revision: 1, InfoHash: strings.Repeat("c", 40), seaDexEpisodes: [][2]int{{1, 2}}}
	if err := ResolveSeaDexFiles(ctx, st.Reader(), []*ExistingFile{other, mapped}); err != nil {
		t.Fatal(err)
	}
	eng := testEngine(t)
	eng.Profile("remux-2160p", format.Radarr).Profile.CutoffFormatScore = 0
	vp := *eng.Policy.Versions["movie-remux"]
	vp.PreferSeaDex = true
	v := Version{Key: "movie-remux", Policy: &vp}
	if wanted, err := eng.QualityCutoffNotMet(v, other); err != nil || wanted {
		t.Fatalf("S01E13 searched for an entry mapped only to E01-E02: %v %v alternatives=%+v", wanted, err, other.SeaDexAlternatives)
	}
	// The mapped episode still has its entry's curated torrent as an alternative.
	if len(mapped.SeaDexAlternatives) != 1 {
		t.Fatalf("mapped episode lost its entry's alternative: %+v", mapped.SeaDexAlternatives)
	}
}

// Split-cour layout, a torrent both cours' entries list at different ranks. A file of
// cour 2 is scoped to the entry covering it (narrowing, seadex_standing.go resolveStanding) and takes that entry's
// rank; it is not left unresolved by the cour-1 entry, which no longer covers it.
func TestSplitCourNarrowsToCoveringEntry(t *testing.T) {
	t.Parallel()
	st := linkedDB(t)
	for _, q := range []string{
		`INSERT INTO title(id,kind,name,tvdb_id,created_at,updated_at) VALUES(1,'series','Show',100,0,0)`,
		`INSERT INTO title_external_id(title_id,source,external_id) VALUES(1,'anilist','11')`,
		`INSERT INTO anime_franchise(slug,name,seed_anilist,created_at,updated_at) VALUES('f','F',11,0,0)`,
		`INSERT INTO anime_entry(franchise,key,origin,anilist_id,tmdb_movie,body) VALUES('f','al:11','anilist',11,'[]','{}')`,
		`INSERT INTO anime_entry(franchise,key,origin,anilist_id,tmdb_movie,body) VALUES('f','al:12','anilist',12,'[]','{}')`,
		`INSERT INTO anime_mapping(franchise,entry_key,status,tvdb_series,episodes,updated_at) VALUES('f','al:11','auto',100,'[[1,1],[1,2],[1,3]]',0)`,
		`INSERT INTO anime_mapping(franchise,entry_key,status,tvdb_series,episodes,updated_at) VALUES('f','al:12','auto',100,'[[1,4],[1,5],[1,6]]',0)`,
	} {
		seadexExec(t, st, q)
	}
	batch, cour1 := strings.Repeat("d", 40), strings.Repeat("e", 40)
	// Entry 11 lists the batch as upstream best; entry 12 lists it beside its own best (so the batch is rank 1 there).
	seadexSnapshotRow(t, st, factsSnapshot(indexer.SeaDexSnapshot{AniList: 11, Torrents: []indexer.SeaDexStanding{
		{InfoHash: batch, Quality: "Remux-2160p", IsBest: true, Preference: 2}}}))
	seadexSnapshotRow(t, st, factsSnapshot(indexer.SeaDexSnapshot{AniList: 12, Torrents: []indexer.SeaDexStanding{
		{InfoHash: batch, Quality: "Remux-2160p", Preference: 1},
		{InfoHash: cour1, Quality: "Remux-2160p", IsBest: true, Preference: 2}}}))
	ctx, err := WithStoredSeaDex(context.Background(), st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithSeaDexTitle(WithSeaDexPolicy(ctx, nil, &policy.VersionPolicy{PreferSeaDex: true}), 1)
	f := &ExistingFile{Quality: "Remux-2160p", Revision: 1, InfoHash: batch, seaDexEpisodes: [][2]int{{1, 5}}}
	g := &ExistingFile{Quality: "Remux-2160p", Revision: 1, InfoHash: batch, seaDexEpisodes: [][2]int{{1, 2}}}
	if err := ResolveSeaDexFiles(ctx, st.Reader(), []*ExistingFile{f, g}); err != nil {
		t.Fatal(err)
	}
	if f.SeaDexEntry != 12 || f.SeaDexPreference != 1 {
		t.Fatalf("cour-2 file: entry %d rank %d, want entry 12 rank 1", f.SeaDexEntry, f.SeaDexPreference)
	}
	if g.SeaDexEntry != 11 || g.SeaDexPreference != 3 {
		t.Fatalf("cour-1 file: entry %d rank %d, want entry 11 as its configured winner (3)", g.SeaDexEntry, g.SeaDexPreference)
	}
}
