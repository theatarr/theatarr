package decide

import (
	"context"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// Clamp is one-directional: a held file whose saved rank is BELOW every current rank of its (disagreeing, fully
// known, non-legacy) memberships keeps the lower saved rank and gets no alternatives, while the agreeing case is
// raised to the current rank.
func TestEqualCurrentRankKeepsAlternatives(t *testing.T) {
	t.Parallel()
	h := strings.Repeat("a", 40)
	other := strings.Repeat("e", 40)
	cover := map[int64]map[int]*indexer.SeaDexEntryMapping{1: {
		11: {Series: true, Episodes: map[[2]int]bool{{1, 1}: true}},
		12: {Series: true, Episodes: map[[2]int]bool{{1, 1}: true}},
	}}
	mk := func(agree bool) context.Context {
		a := factsSnapshot(indexer.SeaDexSnapshot{AniList: 11, Torrents: []indexer.SeaDexStanding{{InfoHash: h, Size: 1, Preference: 2, IsBest: true}}})
		b := factsSnapshot(indexer.SeaDexSnapshot{AniList: 12, Torrents: []indexer.SeaDexStanding{{InfoHash: h, Size: 1, Preference: 2, IsBest: agree}}})
		o := factsSnapshot(indexer.SeaDexSnapshot{Torrents: []indexer.SeaDexStanding{{InfoHash: other, Size: 2, Preference: 2, IsBest: true}}}).Torrents[0]
		a.Torrents = append(a.Torrents, o)
		b.Torrents = append(b.Torrents, o)
		sc := &policy.SeaDexScoring{SizeWeight: 1}
		a, b = indexer.ConfigureSeaDex(a, sc), indexer.ConfigureSeaDex(b, sc)
		t.Logf("agree=%v configured ranks: a=%d b=%d other=%d", agree, a.Torrents[0].Preference, b.Torrents[0].Preference, a.Torrents[1].Preference)
		s := indexStanding(seaDexState{configured: true, titles: map[int64][]int{1: {11, 12}}, series: map[int64]bool{1: true}, cover: cover, snapshots: []indexer.SeaDexSnapshot{a, b}})
		return WithSeaDexTitle(context.WithValue(context.Background(), seaDexContext{}, s), 1)
	}
	for _, saved := range []int{1, 2, 3} {
		for _, agree := range []bool{true, false} {
			f := &ExistingFile{InfoHash: h, SeaDexPreference: saved, seaDexEpisodes: [][2]int{{1, 1}}}
			got := resolveSeaDex(mk(agree), f)
			t.Logf("agree=%v saved rank %d -> rank=%d alternatives=%d", agree, saved, got.SeaDexPreference, len(got.SeaDexAlternatives))
			if !agree && saved == 2 && len(got.SeaDexAlternatives) == 0 {
				t.Errorf("held rank equals the highest current rank (2) of fully known disagreeing memberships, rank-3 'other' listed by both: no alternatives")
			}
		}
	}
}
