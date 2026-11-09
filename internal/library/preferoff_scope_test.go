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

// A split-cour pack imported under a prefer-on version stores each file's own scoped rank and entry; a later
// prefer-off read (which resolves nothing and reads no standing) gets exactly those, not the pack's merged rank, even
// after the mappings that scoped them are gone.
func TestPreferOffReadKeepsSplitCourScope(t *testing.T) {
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
	a := fmt.Sprintf(`{"anilist":11,"torrents":[{"info_hash":"%s","preference":2,"is_best":true,"facts":{"isBest":true,"files":[{"name":"Show.S01E01.mkv","length":10000000}]}}]}`, hashA)
	b := fmt.Sprintf(`{"anilist":12,"torrents":[{"info_hash":"%s","preference":1,"is_best":false,"facts":{"isBest":false,"files":[{"name":"Show.S01E02.mkv","length":10000000}]}},{"info_hash":"%s","preference":2,"is_best":true,"facts":{"isBest":true,"files":[{"name":"Show.S01E02.mkv","length":10000000}]}}]}`, hashA, strings.Repeat("b", 40))
	e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','11',200,?,0,253402214400000),('seadex-standing','12',200,?,0,253402214400000)`, a, b)
	e.pack(1, "WEBDL-2160p")
	raw, _ := json.Marshal(indexer.Release{InfoHash: hashA})
	e.exec(`UPDATE download SET release_data=? WHERE id=1`, string(raw))
	if plan, err := e.lib.Import(e.ctx, 1, false); err != nil || len(plan.Links) != 4 {
		t.Fatalf("import: %v", err)
	}
	e.exec(`DELETE FROM anime_mapping WHERE franchise='f'`)
	off := *e.pol.Versions["tv"]
	off.PreferSeaDex = false
	ctx, err := decide.WithStoredSeaDex(e.ctx, e.st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	eps, err := decide.LoadEpisodes(decide.WithSeaDexPolicy(ctx, e.pol, &off), e.st.Reader(), 2, 20)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, ep := range eps {
		if ep.ID < 201 || ep.ID > 204 || ep.File == nil {
			continue
		}
		seen++
		wantRank, wantEntry := 3, 11
		if ep.ID != 201 {
			wantRank, wantEntry = 1, 12
		}
		if ep.File.SeaDexPreference != wantRank || ep.File.SeaDexEntry != wantEntry || len(ep.File.SeaDexAlternatives) != 0 {
			t.Errorf("episode %d prefer-off read: rank/entry %d/%d alternatives %d, want %d/%d and none", ep.ID,
				ep.File.SeaDexPreference, ep.File.SeaDexEntry, len(ep.File.SeaDexAlternatives), wantRank, wantEntry)
		}
	}
	if seen != 4 {
		t.Fatalf("loaded %d of the 4 imported files", seen)
	}
}
