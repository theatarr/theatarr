package library

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/policy"
)

// A client may import by path with no known torrent hash. Its file is ordinary,
// but covering curated alternatives can still justify a search after import.
func TestHashlessSeriesImportCutoff(t *testing.T) {
	t.Parallel()
	e := importUpEnv(t, policy.ModeActive, "")
	e.pol.Profiles["tv-state"].Cutoff = "WEBDL-2160p"
	e.pol.Profiles["tv-state"].CutoffScore = 0
	engine, err := decide.New(e.pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.lib.engine = engine
	vp := e.pol.Versions["tv"]
	vp.PreferSeaDex = true
	p := e.lib.engine.Profile(vp.Profile, format.Sonarr)
	maxSize := float64(1)
	p.Engine.Definitions = profile.DefaultDefinitions(format.Sonarr)
	p.Engine.Definitions.Set(profile.Definition{Quality: profile.MustQuality(format.Sonarr, "Bluray-2160p Remux"), MaxSize: &maxSize})
	e.exec(`INSERT INTO title_external_id VALUES(2,'anilist','11')`)
	body := fmt.Sprintf(`{"anilist":11,"torrents":[{"info_hash":"%s","quality":"Bluray-2160p Remux","preference":2,"is_best":true,"size":10000000,"facts":{"isBest":true,"files":[{"name":"Show.S01E01.mkv","length":5000000},{"name":"Show.S01E02.mkv","length":5000000}]}}]}`, strings.Repeat("b", 40))
	e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','11',200,?,0,253402214400000)`, body)
	rel := "Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP"
	name := rel + ".mkv"
	e.release(rel, map[string]float64{name: 1440})
	e.seriesDownload(1, 2, 20, "", rel, "[201]", nil)
	e.exec(`UPDATE download SET quality='WEBDL-2160p', indexer='import', source_path=? WHERE id=1`, filepath.Join(e.mount, "__all__", rel, name))
	e.exec(`INSERT INTO download_file (download_id, rel, episodes, source, how, updated_at) VALUES (1, ?, '[201]', 'manual', 'manual', 0)`, name)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 1 {
		t.Fatalf("links=%v rejected=%+v", linkedEpisodes(plan), plan.Rejected)
	}
	if got := e.one(`SELECT COALESCE(json_extract(file_metadata,'$.SeaDexPreference'),0) || '/' || COALESCE(json_extract(file_metadata,'$.SeaDexEntry'),0) FROM link`); got != "0/0" {
		t.Fatalf("hashless import stored rank/entry=%s, want 0/0", got)
	}
	before := e.one(`SELECT state FROM episode_version_state WHERE version_id=20 AND episode_id=201`)
	if _, err = e.lib.RecomputeLinkStates(e.ctx); err != nil {
		t.Fatal(err)
	}
	after := e.one(`SELECT state FROM episode_version_state WHERE version_id=20 AND episode_id=201`)
	t.Logf("hashless path import: initial=%s; after recompute=%s", before, after)
	if before != "filled" || after != "filled" {
		t.Fatalf("expected both filled: initial=%s after=%s", before, after)
	}
}

func TestHashlessMovieImportCutoff(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	vp := e.pol.Versions["movie-remux"]
	vp.PreferSeaDex = true
	p := e.lib.engine.Profile(vp.Profile, format.Radarr)
	p.Profile.CutoffFormatScore = 0
	maxSize := float64(1)
	p.Engine.Definitions = profile.DefaultDefinitions(format.Radarr)
	p.Engine.Definitions.Set(profile.Definition{Quality: profile.MustQuality(format.Radarr, "Remux-2160p"), MaxSize: &maxSize})
	e.exec(`INSERT INTO title_external_id VALUES(1,'anilist','11')`)
	body := fmt.Sprintf(`{"anilist":11,"torrents":[{"info_hash":"%s","quality":"Remux-2160p","preference":2,"is_best":true,"size":10000000,"facts":{"isBest":true,"files":[{"name":"Movie.mkv","length":10000000}]}}]}`, strings.Repeat("b", 40))
	e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','11',200,?,0,253402214400000)`, body)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	e.exec(`UPDATE download SET info_hash='', indexer='import', source_path=? WHERE id=1`, filepath.Join(e.mount, "__all__", movieRelease, "movie.mkv"))
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 1 {
		t.Fatalf("links=%v rejected=%v", plan.Links, plan.Rejected)
	}
	if got := e.one(`SELECT COALESCE(json_extract(file_metadata,'$.SeaDexPreference'),0) || '/' || COALESCE(json_extract(file_metadata,'$.SeaDexEntry'),0) FROM link`); got != "0/0" {
		t.Fatalf("hashless import stored rank/entry=%s, want 0/0", got)
	}
	before := e.one(`SELECT state FROM version WHERE id=10`)
	if _, err = e.lib.RecomputeLinkStates(e.ctx); err != nil {
		t.Fatal(err)
	}
	after := e.one(`SELECT state FROM version WHERE id=10`)
	t.Logf("hashless path import: initial=%s; after recompute=%s", before, after)
	if before != "filled" || after != "filled" {
		t.Fatalf("expected both filled: initial=%s after=%s", before, after)
	}
}
