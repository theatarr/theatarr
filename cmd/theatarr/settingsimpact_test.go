package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// The impact preview counts the files decided differently, not every file it evaluated, and only
// checks the versions the change can affect.
func TestQualityDecisionImpactCountsChangedFiles(t *testing.T) {
	t.Parallel()
	db, _, _, _ := runtimeFixture(t)
	for _, q := range []string{
		`INSERT INTO title(id,kind,name,year,runtime_minutes,created_at,updated_at) VALUES(1,'movie','Example',2026,120,0,0),(2,'movie','Other',2026,120,0,0)`,
		`INSERT INTO version(id,title_id,policy_key,created_at,updated_at) VALUES(1,1,'movie',0,0),(2,2,'movie',0,0),(3,1,'other',0,0)`,
		`INSERT INTO link(id,path,target,kind,title_id,release_title,quality,file_metadata,adopted_from,state,created_at,updated_at) VALUES
			(1,'/a.mkv','/a.mkv','symlink',1,'Example.2026.1080p.WEB-DL-GRP','WEBDL-1080p','{"Quality":"WEBDL-1080p","Revision":1,"SceneName":"Example.2026.1080p.WEB-DL-GRP","Languages":["English"],"Size":10000000000}','fixture /a.mkv','active',0,0),
			(2,'/b.mkv','/b.mkv','symlink',2,'Other.2026.1080p.WEB-DL-NONE','WEBDL-1080p','{"Quality":"WEBDL-1080p","Revision":1,"SceneName":"Other.2026.1080p.WEB-DL-NONE","Languages":["English"],"Size":10000000000}','fixture /b.mkv','active',0,0),
			(3,'/c.mkv','/c.mkv','symlink',1,'Example.2026.1080p.WEB-DL-GRP','WEBDL-1080p','{"Quality":"WEBDL-1080p","Revision":1,"SceneName":"Example.2026.1080p.WEB-DL-GRP","Languages":["English"],"Size":10000000000}','fixture /c.mkv','active',0,0)`,
		`INSERT INTO link_version(link_id,version_id) VALUES(1,1),(2,2),(3,3)`,
	} {
		if _, err := db.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	profile := func() *policy.Profile {
		return &policy.Profile{Qualities: []policy.QualityItem{{Name: "WEBDL-1080p"}}, Cutoff: "WEBDL-1080p", CutoffScore: 50, Formats: map[string]int{"group": 100}}
	}
	before := &policy.Policy{
		Versions: map[string]*policy.VersionPolicy{"movie": {Kind: policy.KindMovie, Profile: "p"}, "other": {Kind: policy.KindMovie, Profile: "q"}},
		Profiles: map[string]*policy.Profile{"p": profile(), "q": profile()},
		Formats:  map[string]*policy.FormatRef{"group": {Flavour: "radarr", Specification: json.RawMessage(`{"name":"Group","specifications":[{"name":"Group","implementation":"ReleaseTitleSpecification","fields":{"value":"GRP"}}]}`)}}}
	after, err := before.Clone()
	if err != nil {
		t.Fatal(err)
	}
	after.Profiles["p"].Formats["group"] = 0
	impact, err := qualityDecisionImpact(context.Background(), db, before, after, "")
	if err != nil {
		t.Fatal(err)
	}
	// Only a.mkv changes: b.mkv has no GRP (scored the same), and c.mkv's version uses the unchanged profile q.
	if impact.Changed != 1 || impact.Files != 2 || impact.ScoreChanged != 1 || len(impact.Examples) != 1 {
		t.Fatalf("impact=%+v", impact)
	}
}
