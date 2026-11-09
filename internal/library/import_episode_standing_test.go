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

func TestExplicitEpisodeScopedCurrentStanding(t *testing.T) {
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
	rel := indexer.Release{InfoHash: hashA, Extras: map[string]string{"anilist": "12"}}
	raw, _ := json.Marshal(rel)
	e.exec(`UPDATE download SET release_data=? WHERE id=1`, string(raw))
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 4 {
		t.Fatalf("links=%d", len(plan.Links))
	}
	for _, id := range []int{201, 202, 203, 204} {
		rank := e.one(`SELECT json_extract(l.file_metadata,'$.SeaDexPreference') FROM link l JOIN link_episode le ON le.link_id=l.id WHERE le.episode_id=?`, id)
		entry := e.one(`SELECT json_extract(l.file_metadata,'$.SeaDexEntry') FROM link l JOIN link_episode le ON le.link_id=l.id WHERE le.episode_id=?`, id)
		t.Logf("episode %d saved rank=%s entry=%s", id, rank, entry)
		wantRank, wantEntry := "3", "11"
		if id != 201 {
			wantRank, wantEntry = "1", "12"
		}
		if rank != wantRank || entry != wantEntry {
			t.Errorf("episode %d: rank/entry=%s/%s want %s/%s", id, rank, entry, wantRank, wantEntry)
		}
	}
	e.exec(`DELETE FROM anime_mapping WHERE franchise='f'`)
	e.exec(`INSERT INTO anime_entry(franchise,key,origin,anilist_id,tmdb_movie,body) VALUES('f','al:13','anilist',13,'[]','{}')`)
	e.exec(`INSERT INTO anime_mapping(franchise,entry_key,status,tvdb_series,episodes,updated_at) VALUES('f','al:13','auto',5,'[[1,1]]',0)`)
	c := strings.Repeat("c", 40)
	other := fmt.Sprintf(`{"anilist":13,"torrents":[{"info_hash":"%s","preference":2,"is_best":true,"facts":{"isBest":true,"files":[{"name":"Show.S01E01.mkv","length":10000000}]}},{"info_hash":"%s","preference":1,"is_best":false,"facts":{"isBest":false,"files":[{"name":"Show.S01E01.mkv","length":10000000}]}}]}`, c, strings.Repeat("d", 40))
	e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','13',200,?,0,253402214400000)`, other)
	ctx, err := decide.WithStoredSeaDex(e.ctx, e.st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	ctx = decide.WithSeaDexTitle(decide.WithSeaDexPolicy(ctx, e.pol, e.pol.Versions["tv"]), 2)
	eps, err := decide.LoadEpisodes(ctx, e.st.Reader(), 2, 20)
	if err != nil {
		t.Fatal(err)
	}
	title, err := decide.LoadTitle(ctx, e.st.Reader(), 2)
	if err != nil {
		t.Fatal(err)
	}
	incoming := decide.RefreshSeaDexRelease(ctx, indexer.Release{InfoHash: c})
	nf := decide.LinkedFile(policy.KindSeries, "", decide.Release{Release: incoming}, "WEBDL-1080p")
	for _, ep := range eps {
		if ep.ID == 201 {
			ok, why, err := e.lib.engine.ImportUpgrade(&title, decide.Version{ID: 20, Key: "tv", Policy: e.pol.Versions["tv"]}, nf, ep.File)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("after remapping: held rank=%d incoming current rank=%d downgrade accepted=%v reason=%s", ep.File.SeaDexPreference, nf.SeaDexPreference, ok, why)
			if ok {
				t.Error("lost stored protection allowed a lower-quality incoming file of equal correct rank")
			}
		}
	}
	second := "Show.S01E01.1080p.WEB-DL-GRP"
	e.release(second, map[string]float64{second + ".mkv": 2700})
	e.videoSize(second, 1920, 1080)
	e.seriesDownload(2, 2, 20, c, second, "[201]", nil)
	plan2, err := e.lib.Import(e.ctx, 2, false)
	if err != nil {
		t.Fatal(err)
	} else {
		t.Logf("second import linked=%v skipped=%v", linkedEpisodes(plan2), skipped(plan2))
		if len(plan2.Links) != 0 {
			t.Error("downgrade was not refused")
		}
	}
	trash := e.one(`SELECT COUNT(*) FROM link_trash`)
	t.Logf("trash rows after downgrade import=%s", trash)
	if trash != "0" {
		t.Error("lower-quality release replaced and trashed the formerly protected link")
	}
}
