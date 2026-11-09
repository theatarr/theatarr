package arrview

import (
	"context"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
)

// wanted/cutoff lists a cutoff_met episode of a prefer-SeaDex version that a better curated alternative
// makes wanted, as acquire's cutoff search selects it.
func TestCutoffPageListsCuratorWanted(t *testing.T) {
	e := buildDevEnv(t, true, func(p *policy.Policy) *decide.Engine {
		p.Profiles = map[string]*policy.Profile{"review": {Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}},
			Cutoff: "WEBDL-2160p"}}
		for _, v := range p.Versions {
			v.Profile = "review"
		}
		p.Versions["tv-2160p"].PreferSeaDex = true
		eng, err := decide.New(p, decide.Options{})
		if err != nil {
			t.Fatal(err)
		}
		return eng
	})
	e.seedFiles()
	e.exec(`UPDATE title SET runtime_minutes = 45 WHERE id = 10`)
	e.exec(`UPDATE episode SET runtime_minutes = 45 WHERE title_id = 10`)
	e.exec(`INSERT INTO title_external_id VALUES (10, 'anilist', '1535')`)
	e.exec(`INSERT INTO metadata_cache (source, key, status, body, fetched_at, expires_at)
		VALUES ('seadex-standing', '1535', 200, ?, 0, 253402214400000)`,
		`{"anilist":1535,"torrents":[{"info_hash":"`+strings.Repeat("f", 40)+`","quality":"WEBDL-2160p","preference":3,`+
			`"is_best":true,"record_id":"abcdef012345678","size":4294967296,"facts":{"id":"abcdef012345678",`+
			`"tracker":"Nyaa","isBest":true,"files":[{"name":"The.Show.S01E01.2160p.WEB-DL.mkv","length":2147483648},`+
			`{"name":"The.Show.S01E02.2160p.WEB-DL.mkv","length":2147483648}]}}]}`)
	below, err := e.cutoff.EpisodesBelowQualityCutoff(context.Background(), "tv-2160p", 10, 10)
	if err != nil || !below[101] {
		t.Fatalf("fixture must be curator-wanted: below=%v err=%v", below, err)
	}
	if n := e.one(`SELECT count(*) FROM episode_version_state WHERE version_id = 10 AND episode_id = 101
		AND state = 'cutoff_met'`); n != 1 {
		t.Fatalf("fixture: episode 101 not cutoff_met")
	}
	var p struct{ TotalRecords int }
	e.call("GET", "/sonarr/api/v3/wanted/cutoff?pageSize=10000&monitored=true", nil, 200, &p)
	if p.TotalRecords != 1 {
		t.Fatalf("curator-wanted episode absent from the cutoff page: %+v", p)
	}
}
