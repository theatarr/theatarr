package library

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestImportCuratedCutoffState(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	vp := e.pol.Versions["tv"]
	vp.PreferSeaDex = true
	p := e.lib.engine.Profile(vp.Profile, format.Sonarr)
	p.Profile.CutoffFormatScore = 0
	maxSize := float64(1)
	p.Engine.Definitions = profile.DefaultDefinitions(format.Sonarr)
	p.Engine.Definitions.Set(profile.Definition{Quality: profile.MustQuality(format.Sonarr, "Bluray-2160p Remux"), MaxSize: &maxSize})
	e.exec(`INSERT INTO title_external_id VALUES(2,'anilist','11')`)
	body := fmt.Sprintf(`{"anilist":11,"torrents":[{"info_hash":"%s","quality":"Bluray-2160p Remux","preference":2,"is_best":true,"size":10000000,"facts":{"isBest":true,"files":[{"name":"Show.S01E01.mkv","length":5000000},{"name":"Show.S01E02.mkv","length":5000000}]}}]}`, strings.Repeat("b", 40))
	e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','11',200,?,0,253402214400000)`, body)
	release := "Show.S01E01.2160p.BluRay.REMUX-GRP"
	e.release(release, map[string]float64{release + ".mkv": 2700})
	e.download(1, "tv", 2, 20, release, release, "[201]")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 1 {
		t.Fatalf("links=%v skips=%v", plan.Links, skipped(plan))
	}
	before := e.one(`SELECT state FROM episode_version_state WHERE version_id=20 AND episode_id=201`)
	n, err := e.lib.RecomputeLinkStates(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	after := e.one(`SELECT state FROM episode_version_state WHERE version_id=20 AND episode_id=201`)
	t.Logf("import recorded=%s; explicit recompute changed=%d, now=%s", before, n, after)
	if before != "filled" {
		t.Fatal("import missed an eligible curator-only upgrade")
	}
}

func TestImportEpisodeScopedCurrentStanding(t *testing.T) {
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
	rel := indexer.Release{InfoHash: hashA}
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
	other := fmt.Sprintf(`{"anilist":13,"torrents":[{"info_hash":"%s","preference":1,"is_best":false,"facts":{"isBest":false,"files":[{"name":"Show.S01E01.mkv","length":10000000}]}},{"info_hash":"%s","preference":2,"is_best":true,"facts":{"isBest":true,"files":[{"name":"Show.S01E01.mkv","length":10000000}]}}]}`, c, strings.Repeat("d", 40))
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
				t.Error("lost stored protection allowed lower-quality, lower-ranked incoming file")
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
