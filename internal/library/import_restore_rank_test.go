package library

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestActualImportRestoresLegacyRank(t *testing.T) {
	t.Parallel()
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			e := importUpEnv(t, policy.ModeActive, "")
			e.pol.Versions["tv"].PreferSeaDex = true
			e.exec(`INSERT INTO title_external_id VALUES(2,'anilist','11')`)
			body := fmt.Sprintf(`{"anilist":11,"torrents":[{"info_hash":"%s","preference":2,"is_best":true}]}`, hashA)
			e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','11',200,?,0,253402214400000)`, body)
			e.heldLink(1, "Bluray-2160p Remux", "Show.S01E01.2160p.BluRay.REMUX.HEVC-OLD", 201)
			e.pack(1, "WEBDL-2160p")
			rel := indexer.Release{InfoHash: hashA}
			if explicit {
				rel.Extras = map[string]string{"anilist": "11"}
			}
			raw, _ := json.Marshal(rel)
			e.exec(`UPDATE download SET release_data=? WHERE id=1`, string(raw))
			applied, err := e.lib.Import(e.ctx, 1, false)
			if err != nil {
				t.Fatal(err)
			}
			rank := e.one(`SELECT json_extract(l.file_metadata,'$.SeaDexPreference') FROM link l JOIN link_episode le ON le.link_id=l.id WHERE l.state='active' AND le.episode_id=201 LIMIT 1`)
			t.Logf("actual import: linked=%v, trash rows=%s, E01 saved rank=%s", linkedEpisodes(applied), e.one(`SELECT COUNT(*) FROM link_trash`), rank)
			for _, ep := range linkedEpisodes(applied) {
				if ep == 201 {
					t.Errorf("legacy-only incoming WEB replaced the held remux episode")
				}
			}
			if got := e.one(`SELECT COUNT(*) FROM link_trash`); got != "0" {
				t.Errorf("replacement trashed held file: %s", got)
			}
			if got := e.one(`SELECT COUNT(*) FROM link WHERE json_extract(file_metadata,'$.SeaDexPreference')=2`); got != "0" {
				t.Errorf("saved legacy rank 2 on %s links", got)
			}

		})
	}
}

func TestIncomingCurrentHashStanding(t *testing.T) {
	t.Parallel()
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			e.pol.Versions["tv"].PreferSeaDex = true
			e.exec(`INSERT INTO title_external_id VALUES(2,'anilist','11')`)
			body := fmt.Sprintf(`{"anilist":11,"torrents":[{"info_hash":"%s","preference":2,"is_best":true,"facts":{"isBest":true,"files":[{"name":"Show.S01E01.1080p.BluRay.mkv","length":10000000}]}}]}`, hashA)
			e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','11',200,?,0,253402214400000)`, body)
			e.seriesDownload(1, 2, 20, hashA, packRelease, "[201,202]", nil)
			rel := indexer.Release{InfoHash: hashA}
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
			f, _, err := e.lib.linkedMetadata(ctx, d)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("explicit=%v incoming rank=%d entry=%d", explicit, f.SeaDexPreference, f.SeaDexEntry)
			if f.SeaDexPreference != 3 {
				t.Fatalf("fresh current evidence lost: %+v", f)
			}
		})
	}
}
