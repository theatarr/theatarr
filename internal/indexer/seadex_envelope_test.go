package indexer

import (
	"encoding/json"
	"github.com/theatarr/theatarr/internal/policy"
	"net/http"
	"strings"
	"testing"
)

func TestSeaDexSnapshotRequiresCompleteEnvelopeAndRelations(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"object", `{}`, 0},
		{"missing pagination", `{"items":[]}`, 0},
		{"missing expansion", `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":1,"trs":["x"]}]}`, 0},
		{"partial expansion", `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":1,"trs":["x"],"expand":{"trs":[]}}]}`, 0},
		{"partial page", `{"page":1,"perPage":100,"totalItems":2,"totalPages":1,"items":[]}`, 0},
		{"wrong entry", `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":2,"trs":[]}]}`, 0},
		{"complete missing entry", `{"page":1,"perPage":100,"totalItems":0,"totalPages":0,"items":[]}`, 1},
		{"complete empty relations", `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":1,"trs":[]}]}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := newNet(t)
			n.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(tc.body)) }))
			h := newHarness(t, n, testPolicy(), false)
			got := search(t, h, "seadex", Query{Type: QueryMovie, AniList: 1})
			if len(got.SeaDex) != tc.want {
				t.Fatalf("snapshots=%+v", got.SeaDex)
			}
		})
	}
}

func TestSeaDexFactsRescoreWithoutRefreshAndCloneOwnsFacts(t *testing.T) {
	var best, dual sdTorrent
	_ = json.Unmarshal([]byte(`{"isBest":true,"tags":["best"]}`), &best)
	_ = json.Unmarshal([]byte(`{"dualAudio":true,"tags":["dual"]}`), &dual)
	s := SeaDexSnapshot{AniList: 1, Torrents: []SeaDexStanding{{InfoHash: strings.Repeat("a", 40), IsBest: true, Preference: 2, Facts: &best}, {InfoHash: strings.Repeat("b", 40), Preference: 1, Facts: &dual}}}
	first := ConfigureSeaDex(s, nil)
	weight := 1
	second := ConfigureSeaDex(s, &policy.SeaDexScoring{Best: &weight, DualAudio: 100})
	if first.Torrents[0].Preference != 3 || second.Torrents[1].Preference != 3 || second.Torrents[1].IsBest {
		t.Fatalf("ranks: %+v %+v", first, second)
	}
	r := &Result{SeaDex: []SeaDexSnapshot{s}}
	c := r.clone()
	c.SeaDex[0].Torrents[0].Facts.Tags[0] = "changed"
	c.SeaDex[0].Torrents[0].Preference = 0
	if r.SeaDex[0].Torrents[0].Facts.Tags[0] != "best" || r.SeaDex[0].Torrents[0].Preference != 2 {
		t.Fatal("clone aliases snapshot")
	}
}

func TestLegacyConfiguredRankIsNotReusedAfterSettingsChange(t *testing.T) {
	s := ConfigureSeaDex(SeaDexSnapshot{AniList: 1, Torrents: []SeaDexStanding{{Preference: 3}}}, &policy.SeaDexScoring{DualAudio: 100})
	if s.Torrents[0].Preference != 0 || s.Torrents[0].IsBest {
		t.Fatal("legacy configured rank reused without upstream facts")
	}
}
