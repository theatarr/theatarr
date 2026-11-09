package arrview

import (
	"context"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// pinProbe judges every item below the cutoff and records the SeaDex rank its context gives one hash, then clears
// the stored standing: a list that re-pinned per version would see the change.
type pinProbe struct {
	e     *devEnv
	hash  string
	ranks []int
}

func (p *pinProbe) probe(ctx context.Context) {
	ctx = decide.WithSeaDexTitle(decide.WithSeaDexPolicy(ctx, nil, &policy.VersionPolicy{PreferSeaDex: true}), 1)
	rel := decide.RefreshSeaDexRelease(ctx, indexer.Release{InfoHash: p.hash})
	p.ranks = append(p.ranks, indexer.SeaDexPreference(&rel))
	p.e.exec(`UPDATE metadata_cache SET body=json_set(body,'$.torrents',json('[]')) WHERE source='seadex-standing'`)
}

func (p *pinProbe) BelowQualityCutoff(ctx context.Context, _ string, _, _ int64) (bool, error) {
	p.probe(ctx)
	return true, nil
}

func (p *pinProbe) EpisodesBelowQualityCutoff(ctx context.Context, _ string, _, _ int64) (map[int64]bool, error) {
	p.probe(ctx)
	return nil, nil
}

// One wanted/cutoff list judges every version against one standing pin.
func TestCutoffListPinsStandingOnce(t *testing.T) {
	t.Parallel()
	e := newCutoffEnv(t, "")
	probe := &pinProbe{e: e, hash: strings.Repeat("a", 40)}
	e.exec(`INSERT INTO title_external_id VALUES(1,'anilist','1')`)
	e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','1',200,?,1,9999999999999)`,
		`{"anilist":1,"torrents":[{"info_hash":"`+probe.hash+`","preference":2,"is_best":true,"facts":{"isBest":true}}]}`)
	e.cutoff = probe
	e.startDev(devDecider{e: e})
	var p struct{ TotalRecords int }
	e.call("GET", "/radarr/api/v3/wanted/cutoff", nil, 200, &p)
	if len(probe.ranks) < 2 {
		t.Fatalf("bad setup: %d versions judged", len(probe.ranks))
	}
	for _, r := range probe.ranks {
		if r != probe.ranks[0] || r == 0 {
			t.Fatalf("one cutoff list judged against changing standing: %v", probe.ranks)
		}
	}
}
