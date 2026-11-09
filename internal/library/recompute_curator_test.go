package library

import (
	"fmt"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestSeriesRecomputeMissesEligibleCuratorAlternative(t *testing.T) {
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
	hash := strings.Repeat("b", 40)
	body := fmt.Sprintf(`{"anilist":11,"torrents":[{"info_hash":"%s","quality":"Bluray-2160p Remux","preference":2,"is_best":true,"size":10000000,"facts":{"isBest":true,"files":[{"name":"Show.S01E01.mkv","length":5000000},{"name":"Show.S01E02.mkv","length":5000000}]}}]}`, hash)
	e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','11',200,?,0,253402214400000)`, body)
	meta := fmt.Sprintf(`{"Quality":"Bluray-2160p Remux","Revision":1,"SceneName":"Show.S01E01.2160p.BluRay.REMUX-GRP","InfoHash":"%s"}`, strings.Repeat("a", 40))
	e.exec(`INSERT INTO link(id,title_id,path,target,state,file_metadata,created_at,updated_at) VALUES(1,2,'/fixture/link','/fixture/target','active',?,0,0)`, meta)
	e.exec(`INSERT INTO link_version(link_id,version_id) VALUES(1,20)`)
	e.exec(`INSERT INTO link_episode(link_id,episode_id) VALUES(1,201)`)
	e.exec(`INSERT INTO episode_version_state(version_id,episode_id,monitored,state,cutoff_met,updated_at) VALUES(20,201,1,'filled',0,0)`)
	ctx, err := decide.WithStoredSeaDex(e.ctx, e.st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	ctx = decide.WithSeaDexTitle(decide.WithSeaDexPolicy(ctx, e.pol, vp), 2)
	eps, err := decide.LoadEpisodes(ctx, e.st.Reader(), 2, 20)
	if err != nil {
		t.Fatal(err)
	}
	title, err := decide.LoadTitle(ctx, e.st.Reader(), 2)
	if err != nil {
		t.Fatal(err)
	}
	title.Episodes = eps
	var file *decide.ExistingFile
	for _, ep := range eps {
		if ep.ID == 201 {
			file = ep.File
		}
	}
	if file == nil {
		t.Fatal("fixture missing file")
	}
	unmet, err := e.lib.engine.FileCutoffNotMet(&title, decide.Version{ID: 20, Key: "tv", Policy: vp}, file)
	if err != nil || !unmet {
		t.Fatalf("fixture has no eligible alternative: unmet=%v err=%v file=%+v", unmet, err, file)
	}
	if _, err = e.lib.RecomputeLinkStates(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT state FROM episode_version_state WHERE version_id=20 AND episode_id=201`); got != "filled" {
		t.Fatalf("library recompute recorded %s despite verified eligible alternative", got)
	}
}
