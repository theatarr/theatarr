package acquire

import (
	"encoding/json"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"strings"
	"testing"
)

func TestDefaultWantedCuratedUpgradeRequiresEligibleAlternative(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, quality string
		rank          int
		want          bool
	}{
		{"allowed", "WEBDL-1080p", 3, true},
		{"unknown", "Unknown", 3, false},
		{"disallowed", "Bluray-720p", 3, false},
		{"retired", "WEBDL-1080p", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, true, &policy.Wanted{PerHour: 100})
			cutoffFixture(t, e)
			e.pol.Versions["movie"].PreferSeaDex = true
			for _, q := range []string{`UPDATE title SET runtime_minutes=120 WHERE id=3`, `UPDATE version SET state='cutoff_met' WHERE id=103`, `INSERT INTO title_external_id VALUES(3,'anilist','1535')`} {
				if _, err := e.st.Writer().Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			snapshot := indexer.SeaDexSnapshot{AniList: 1535, Torrents: []indexer.SeaDexStanding{{InfoHash: strings.Repeat("a", 40), Quality: tc.quality, Preference: tc.rank, Size: 5 * 1024 * 1024 * 1024}}}
			// Recorded with upstream facts: a legacy (facts-less) membership is no curated alternative.
			_ = json.Unmarshal([]byte(`{"facts":{}}`), &snapshot.Torrents[0])
			body, _ := json.Marshal(snapshot)
			if _, err := e.st.Writer().Exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','1535',200,?,0,253402214400000)`, body); err != nil {
				t.Fatal(err)
			}
			if _, err := e.a.Wanted(e.ctx); err != nil {
				t.Fatal(err)
			}
			got := strings.Contains(e.runs(), "103:movie")
			if got != tc.want {
				t.Fatalf("searches %s, wanted curated search=%v", e.runs(), tc.want)
			}
		})
	}
}
