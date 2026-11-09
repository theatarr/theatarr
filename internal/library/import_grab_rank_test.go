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

func TestIncomingImportClearsGrabRank(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["tv"].PreferSeaDex = true
	hash := strings.Repeat("a", 40)
	for _, query := range []string{
		`INSERT INTO anime_franchise(slug,name,seed_anilist,created_at,updated_at) VALUES('f','F',11,0,0)`,
		`INSERT INTO anime_entry(franchise,key,origin,anilist_id,tmdb_movie,body) VALUES('f','al:11','anilist',11,'[]','{}'),('f','al:12','anilist',12,'[]','{}')`,
		`INSERT INTO anime_mapping(franchise,entry_key,status,tvdb_series,episodes,updated_at) SELECT 'f','al:11','auto',tvdb_id,'[[1,1]]',0 FROM title WHERE id=2`,
		`INSERT INTO anime_mapping(franchise,entry_key,status,tvdb_series,episodes,updated_at) SELECT 'f','al:12','auto',tvdb_id,'[[1,2]]',0 FROM title WHERE id=2`,
	} {
		e.exec(query)
	}
	for entry, best := range map[int]bool{11: false, 12: true} {
		body := fmt.Sprintf(`{"anilist":%d,"torrents":[{"info_hash":"%s","preference":2,"is_best":%t,"facts":{"isBest":%t,"files":[{"name":"Show.S01E01.1080p.BluRay.mkv","length":10000000}]}},{"info_hash":"%s","preference":2,"is_best":true,"facts":{"isBest":true,"files":[{"name":"Show.S01E01.1080p.BluRay.mkv","length":20000000}]}}]}`, entry, hash, best, best, strings.Repeat("b", 40))
		e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing',?,200,?,0,253402214400000)`, fmt.Sprint(entry), body)
	}
	e.seriesDownload(1, 2, 20, hash, packRelease, "[201,202]", nil)
	rel := indexer.Release{Flags: []string{indexer.FlagSeaDex, indexer.FlagSeaDexBest, indexer.FlagSeaDexPreferred}}
	raw, _ := json.Marshal(rel)
	e.exec(`UPDATE download SET release_data=? WHERE id=1`, string(raw))
	d, err := loadDownload(e.ctx, e.st.Reader(), e.pol, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := decide.WithStoredSeaDex(e.ctx, e.st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	ctx = decide.WithSeaDexTitle(decide.WithSeaDexPolicy(ctx, e.pol, d.Version.Policy), 2)
	f, _, err := e.lib.linkedMetadata(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if f.SeaDexPreference != 0 {
		t.Fatalf("incoming import retained grab rank %d", f.SeaDexPreference)
	}
}
