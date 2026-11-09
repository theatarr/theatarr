package library

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// Import a pack while both cours are mapped, confirm a retirement for the second cour's entry, then
// remove only that cour's mapping. Entry 11 covers E01 only, so it does not contradict E02's saved entry 12, which
// stays its scope: the retirement holds and an allowed upgrade is not blocked.
func TestPartialMappingAfterRealImport(t *testing.T) {
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
	for _, entry := range []int{11, 12} {
		body := fmt.Sprintf(`{"targets":[{"TVDB":5,"Season":1,"Exact":true}],"anilist":%d,"torrents":[{"info_hash":"%s","preference":2,"is_best":true,"facts":{"isBest":true}}]}`, entry, hashA)
		e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing',?,200,?,0,253402214400000)`, fmt.Sprint(entry), body)
	}
	e.pack(1, "WEBDL-2160p")
	raw, err := json.Marshal(indexer.Release{InfoHash: hashA})
	if err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE download SET release_data=? WHERE id=1`, string(raw))
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 4 {
		t.Fatalf("import links=%d rejected=%+v", len(plan.Links), plan.Rejected)
	}
	saved := e.one(`SELECT json_extract(l.file_metadata,'$.SeaDexPreference') || '/' || json_extract(l.file_metadata,'$.SeaDexEntry') FROM link l JOIN link_episode le ON le.link_id=l.id WHERE le.episode_id=202`)
	if saved != "3/12" {
		t.Fatalf("import saved=%s, want 3/12", saved)
	}
	retired := fmt.Sprintf(`{"version":2,"targets":[{"TVDB":5,"Season":1,"Exact":true}],"anilist":12,"torrents":[{"info_hash":"%s","preference":0,"missing_since":1}]}`, hashA)
	e.exec(`UPDATE metadata_cache SET body=? WHERE source='seadex-standing' AND key='12'`, retired)
	// A normal decision sees the retirement while the second cour is still mapped.
	beforeCtx, err := decide.WithStoredSeaDex(e.ctx, e.st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	beforeCtx = decide.WithSeaDexTitle(decide.WithSeaDexPolicy(beforeCtx, e.pol, e.pol.Versions["tv"]), 2)
	before, err := decide.LoadEpisodes(beforeCtx, e.st.Reader(), 2, 20)
	if err != nil {
		t.Fatal(err)
	}
	var beforeFile *decide.ExistingFile
	for _, ep := range before {
		if ep.ID == 202 {
			beforeFile = ep.File
		}
	}
	if beforeFile == nil || beforeFile.SeaDexPreference != 0 || beforeFile.SeaDexEntry != 12 {
		t.Fatalf("before losing the mapping, retired file=%+v", beforeFile)
	}
	e.exec(`DELETE FROM anime_mapping WHERE franchise='f' AND entry_key='al:12'`)
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
		if ep.ID == 202 {
			held = ep.File
		}
	}
	if held == nil {
		t.Fatal("missing imported file")
	}
	t.Logf("previously imported E02: rank=%d entry=%d, want rank=0 entry=12", held.SeaDexPreference, held.SeaDexEntry)
	if held.SeaDexPreference != 0 || held.SeaDexEntry != 12 {
		t.Error("unrelated first-cour mapping suppressed the saved second-cour entry")
	}
	rel := indexer.Release{Title: "Show.S01E02.2160p.BluRay.REMUX.HEVC-GRP", InfoHash: strings.Repeat("b", 40), Size: 2700000000, Indexer: "public"}
	v := decide.Version{ID: 20, Key: "tv", Policy: e.pol.Versions["tv"]}
	d, err := e.lib.engine.DecideSearch(ctx, decide.Release{Release: rel}, title, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("quality upgrade accepted=%v rejections=%+v", d.Accepted, d.Rejections)
	if !d.Accepted {
		t.Error("retired standing blocked an otherwise allowed quality upgrade")
	}
}
