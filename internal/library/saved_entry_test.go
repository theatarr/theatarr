package library

import (
	"fmt"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// On a loaded link: a saved E01 entry naming the E02-E04 entry keeps rank 1 and gets entry 11's
// alternative.
func TestLoadedSavedEntryNeedsCoverage(t *testing.T) {
	t.Parallel()
	e := importUpEnv(t, policy.ModeActive, "")
	e.pol.Versions["tv"].PreferSeaDex = true
	for _, q := range []string{
		`INSERT INTO anime_franchise(slug,name,seed_anilist,created_at,updated_at) VALUES('f','F',11,0,0)`,
		`INSERT INTO anime_entry(franchise,key,origin,anilist_id,tmdb_movie,body) VALUES('f','al:11','anilist',11,'[]','{}'),('f','al:12','anilist',12,'[]','{}')`,
		`INSERT INTO anime_mapping(franchise,entry_key,status,tvdb_series,episodes,updated_at) VALUES('f','al:11','auto',5,'[[1,1]]',0),('f','al:12','auto',5,'[[1,2],[1,3],[1,4]]',0)`,
	} {
		e.exec(q)
	}
	other := strings.Repeat("b", 40)
	a := fmt.Sprintf(`{"anilist":11,"torrents":[{"info_hash":"%s","preference":2,"is_best":true,"facts":{"isBest":true}}]}`, other)
	b := fmt.Sprintf(`{"anilist":12,"torrents":[{"info_hash":"%s","preference":2,"is_best":true,"facts":{"isBest":true}}]}`, hashA)
	e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','11',200,?,0,253402214400000),('seadex-standing','12',200,?,0,253402214400000)`, a, b)
	e.heldLink(1, "WEBDL-2160p", "Show.S01E01.2160p.WEB-DL-GRP", 201)
	meta := fmt.Sprintf(`{"Revision":1,"InfoHash":"%s","SeaDexPreference":1,"SeaDex":1,"SeaDexEntry":12}`, hashA)
	e.exec(`UPDATE link SET file_metadata=? WHERE id=1`, meta)
	ctx, err := decide.WithStoredSeaDex(e.ctx, e.st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	ctx = decide.WithSeaDexTitle(decide.WithSeaDexPolicy(ctx, e.pol, e.pol.Versions["tv"]), 2)
	eps, err := decide.LoadEpisodes(ctx, e.st.Reader(), 2, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range eps {
		if ep.ID != 201 {
			continue
		}
		f := ep.File
		if f == nil {
			t.Fatal("file not loaded")
		}
		if f.SeaDexPreference != 1 || len(f.SeaDexAlternatives) != 1 || f.SeaDexAlternatives[0].Entry != 11 {
			t.Fatalf("loaded episode: rank=%d entry=%d alternatives=%+v; want saved rank 1 and covering entry 11", f.SeaDexPreference, f.SeaDexEntry, f.SeaDexAlternatives)
		}
		return
	}
	t.Fatal("episode not found")
}

// End to end: the series decision, which resolves the loaded file again, refuses the same lower
// release the import check refuses.
func TestDecisionKeepsUnresolvedHeldRank(t *testing.T) {
	t.Parallel()
	e := importUpEnv(t, policy.ModeActive, "")
	e.pol.Versions["tv"].PreferSeaDex = true
	for _, q := range []string{
		`INSERT INTO anime_franchise(slug,name,seed_anilist,created_at,updated_at) VALUES('f','F',11,0,0)`,
		`INSERT INTO anime_entry(franchise,key,origin,anilist_id,tmdb_movie,body) VALUES('f','al:11','anilist',11,'[]','{}'),('f','al:12','anilist',12,'[]','{}')`,
		`INSERT INTO anime_mapping(franchise,entry_key,status,tvdb_series,episodes,updated_at) VALUES('f','al:11','auto',5,'[[1,1]]',0),('f','al:12','auto',5,'[[1,1]]',0)`,
	} {
		e.exec(q)
	}
	better := strings.Repeat("b", 40)
	body := fmt.Sprintf(`{"anilist":11,"torrents":[{"info_hash":"%s","preference":1,"facts":{"isBest":false}},{"info_hash":"%s","preference":2,"is_best":true,"facts":{"isBest":true}}]}`, hashA, better)
	e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','11',200,?,0,253402214400000)`, body)
	e.heldLink(1, "WEBDL-2160p", "Show.S01E01.2160p.WEB-DL-GRP", 201)
	meta := fmt.Sprintf(`{"Revision":1,"InfoHash":"%s","SeaDexPreference":3,"SeaDex":2}`, hashA)
	e.exec(`UPDATE link SET file_metadata=? WHERE id=1`, meta)
	ctx, err := decide.WithStoredSeaDex(e.ctx, e.st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	ctx = decide.WithSeaDexTitle(decide.WithSeaDexPolicy(ctx, e.pol, e.pol.Versions["tv"]), 2)
	title, err := decide.LoadTitle(ctx, e.st.Reader(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := decide.LoadSeriesVersion(ctx, e.st.Reader(), &title, 20); err != nil {
		t.Fatal(err)
	}
	var held *decide.ExistingFile
	for _, ep := range title.Episodes {
		if ep.ID == 201 {
			held = ep.File
		}
	}
	if held == nil {
		t.Fatal("file not loaded")
	}
	t.Logf("loaded held rank=%d entry=%d", held.SeaDexPreference, held.SeaDexEntry)
	rel := indexer.Release{Title: "Show.S01E01.1080p.WEB-DL-GRP", InfoHash: better, Size: 2700000000, Indexer: "public"}
	refreshed := decide.RefreshSeaDexRelease(ctx, rel)
	nf := decide.LinkedFile(policy.KindSeries, "", decide.Release{Release: refreshed}, "WEBDL-1080p")
	v := decide.Version{ID: 20, Key: "tv", Policy: e.pol.Versions["tv"]}
	ok, why, err := e.lib.engine.ImportUpgrade(&title, v, nf, held)
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.lib.engine.DecideSearch(ctx, decide.Release{Release: rel}, title, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("import check: accepted=%v reason=%s; subsequent series decision: accepted=%v rejections=%+v", ok, why, d.Accepted, d.Rejections)
	if ok || d.Accepted {
		t.Fatal("a lower-quality equal-curator-rank upgrade was accepted")
	}
}
