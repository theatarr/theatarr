package decide

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// packGrab is one arr grab of the history replay (docs/design/series.md), as the database replay loaded it: the release,
// the series with each episode's file just before the grab, and the search criteria.
type packGrab struct {
	Instance   string        `json:"instance"`
	HistoryID  int           `json:"history_id"`
	Date       time.Time     `json:"date"`
	Source     string        `json:"source"`
	ArrQuality string        `json:"arr_quality"`
	Release    Release       `json:"release"`
	Search     *SeriesSearch `json:"search"`
	Title      Title         `json:"title"`
}

// The five replay grabs that plain Sonarr refuses because some episodes already hold their best file while the pack
// fills or upgrades others: the arrs (the actuallyevan fork with ACCEPT_RELEASE_ANY_UPGRADABLE) took them. Under
// packs: all each is rejected by UpgradeDisk as the replay found; under packs: any_upgradable each is accepted as an
// upgrade.
func TestPackRuleReplayGrabs(t *testing.T) {
	t.Parallel()
	pol, err := policy.Load("testdata/packs/policy")
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	var now time.Time
	e, err := New(pol, Options{Arr: SnapshotDir("testdata/packs/arr"), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	b, err := os.ReadFile("testdata/packs/grabs.json")
	if err != nil {
		t.Fatal(err)
	}
	var grabs []packGrab
	if err := json.Unmarshal(b, &grabs); err != nil {
		t.Fatal(err)
	}
	if len(grabs) != 5 {
		t.Fatalf("%d grabs, want the replay's 5", len(grabs))
	}
	versions := map[string][2]string{"sonarr_4k_debrid": {"tv-plain", "tv-2160p"}, "sonarr_anime_debrid": {"anime-plain", "anime-tv"}}
	ctx := context.Background()
	for _, g := range grabs {
		now = g.Date
		decide := func(version string) *Decision {
			t.Helper()
			d, err := e.DecideSearch(ctx, g.Release, g.Title, Version{Key: version}, g.Search)
			if err != nil {
				t.Fatalf("%s #%d %s: %v", g.Instance, g.HistoryID, version, err)
			}
			var keep []Rejection // the arr's release carries the arr's indexer: our rules do not apply
			for _, r := range d.Rejections {
				if !r.Ours {
					keep = append(keep, r)
				}
			}
			d.Rejections, d.Accepted = keep, len(keep) == 0
			return d
		}
		vs := versions[g.Instance]
		plain, fork := decide(vs[0]), decide(vs[1])
		name := g.Instance + " #" + strconv.Itoa(g.HistoryID) + " " + g.Release.Title
		if plain.Accepted || plain.Rejections[0].Spec != "UpgradeDiskSpecification" ||
			plain.Rejections[0].Reason != "DiskCustomFormatScore" {
			t.Errorf("%s, packs all: accepted %v %v", name, plain.Accepted, plain.Rejections)
		}
		if !fork.Accepted || !fork.Upgrade || !strings.Contains(fork.UpgradeReason, "keep their file (packs: any_upgradable)") {
			t.Errorf("%s, packs any_upgradable: accepted %v %v upgrade %v (%s)", name, fork.Accepted, fork.Rejections,
				fork.Upgrade, fork.UpgradeReason)
		}
		if fork.Quality != g.ArrQuality || strings.Join(fork.Episodes, ",") != strings.Join(plain.Episodes, ",") {
			t.Errorf("%s: quality %s (arr %s), episodes %v / %v", name, fork.Quality, g.ArrQuality, fork.Episodes, plain.Episodes)
		}
		t.Logf("%s: %d episodes, %d without a file; %s", name, len(fork.Episodes), fork.Missing, fork.UpgradeReason)
	}
}
