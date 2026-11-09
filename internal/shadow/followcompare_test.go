package shadow

import (
	"github.com/theatarr/theatarr/internal/store"
	"testing"
	"time"
)

func TestFollowerComparisonEpisodeAliases(t *testing.T) {
	for _, mapped := range []bool{true, false} {
		t.Run(map[bool]string{true: "mapped", false: "unmapped"}[mapped], func(t *testing.T) {
			e := newEnv(t, false)
			newShow(e.tv)
			if _, err := e.r.Import(e.ctx); err != nil {
				t.Fatal(err)
			}
			exec := func(q string, args ...any) {
				t.Helper()
				if _, err := e.st.Writer().Exec(q, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`DELETE FROM arr_history`)
			// Deliberately use different Sonarr and Theatarr ids. An alias from another instance must not match.
			exec(`INSERT INTO arr_alias(instance,kind,arr_id,target_id,created_at) VALUES ('other','episode',3607,7003,0)`)
			if mapped {
				exec(`INSERT INTO arr_alias(instance,kind,arr_id,target_id,created_at) VALUES ('sonarr_tv','episode',3607,7003,0)`)
			}
			exec(`INSERT INTO arr_history(instance,arr_id,event_type,date_at,arr_episode_id,title_id,info_hash)
    SELECT 'sonarr_tv',999,'grabbed',?,3607,id,'same-hash' FROM title WHERE tvdb_id=1001`, store.Millis(t0))
			episodes := `[7003]`
			if !mapped {
				episodes = `[3607,7003]`
			} // Even a coincidental equal id is not a mapping.
			exec(`INSERT INTO download(title_id,version_id,version_key,info_hash,release_title,indexer,quality,state,episodes,created_at,updated_at)
    SELECT t.id,v.id,v.policy_key,'same-hash','release','test','WEBDL-2160p','added',?,?,? FROM title t JOIN version v ON v.title_id=t.id WHERE t.tvdb_id=1001 AND v.policy_key=?`, episodes, store.Millis(t0), store.Millis(t0), e.pol.Import.Arr["sonarr_tv"].Version)
			r, err := CompareFollow(e.ctx, e.st.Reader(), "sonarr_tv", "followed", e.pol.Import.Arr["sonarr_tv"].Version, t0.Add(-time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if mapped && (r.Counts["both_same_hash"] != 1 || len(r.Pairs) != 1) {
				t.Fatalf("mapped episode: %+v", r)
			}
			if !mapped && (r.Counts["arr_only"] != 1 || r.Counts["theatarr_only"] != 1) {
				t.Fatalf("unmapped episode lost or matched: %+v", r)
			}
		})
	}
}
