package library

import (
	"encoding/json"
	"fmt"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"strings"
	"testing"
)

func TestImportLegacyRankMustNotReturn(t *testing.T) {
	t.Parallel()
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			e.pol.Versions["tv"].PreferSeaDex = true
			hash := strings.Repeat("a", 40)
			for _, q := range []string{
				`INSERT INTO anime_franchise(slug,name,seed_anilist,created_at,updated_at) VALUES('f','F',11,0,0)`,
				`INSERT INTO anime_entry(franchise,key,origin,anilist_id,tmdb_movie,body) VALUES('f','al:11','anilist',11,'[]','{}')`,
				`INSERT INTO anime_mapping(franchise,entry_key,status,tvdb_series,episodes,updated_at) SELECT 'f','al:11','auto',tvdb_id,'[[1,1]]',0 FROM title WHERE id=2`,
			} {
				e.exec(q)
			}
			body := fmt.Sprintf(`{"anilist":11,"torrents":[{"info_hash":"%s","preference":2,"is_best":true}]}`, hash)
			e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','11',200,?,0,253402214400000)`, body)
			e.seriesDownload(1, 2, 20, hash, packRelease, "[201,202]", nil)
			rel := indexer.Release{InfoHash: hash, Flags: []string{indexer.FlagSeaDex, indexer.FlagSeaDexBest, indexer.FlagSeaDexPreferred}}
			if explicit {
				rel.Extras = map[string]string{"anilist": "11"}
			}
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
			refreshed := decide.RefreshSeaDexRelease(ctx, rel)
			t.Logf("refresh rank=%d", indexer.SeaDexPreference(&refreshed))
			f, title, err := e.lib.linkedMetadata(ctx, d)
			if err != nil {
				t.Fatal(err)
			}
			held := &decide.ExistingFile{Quality: "Bluray-2160p Remux", Revision: 1, InfoHash: strings.Repeat("c", 40)}
			v := decide.Version{Key: "tv", Policy: d.Version.Policy}
			allowed, why, err := e.lib.engine.ImportUpgrade(title, v, f, held)
			if err != nil {
				t.Fatal(err)
			}
			clean := *f
			clean.SeaDexPreference = 0
			clean.SeaDex = 0
			expected, whyExpected, err := e.lib.engine.ImportUpgrade(title, v, &clean, held)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("linked rank=%d entry=%d; lower-quality import allowed=%v (%s); with correct rank 0 allowed=%v (%s)", f.SeaDexPreference, f.SeaDexEntry, allowed, why, expected, whyExpected)
			if f.SeaDexPreference != 0 {
				t.Fatalf("legacy-only incoming acquired unverified rank %d", f.SeaDexPreference)
			}
		})
	}
}
