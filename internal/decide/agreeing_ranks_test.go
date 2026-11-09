package decide

import (
	"context"
	"github.com/theatarr/theatarr/internal/indexer"
	"strings"
	"testing"
)

func TestAgreeingRanksCannotClampUnknownMembership(t *testing.T) {
	t.Parallel()
	h := strings.Repeat("a", 40)
	st := seaDexState{configured: true, titles: map[int64][]int{1: {11, 12, 13}}, series: map[int64]bool{1: true}, cover: map[int64]map[int]*indexer.SeaDexEntryMapping{1: {11: {Series: true, Episodes: map[[2]int]bool{{1, 1}: true}}, 12: {Series: true, Episodes: map[[2]int]bool{{1, 1}: true}}, 13: {Series: true, Episodes: map[[2]int]bool{{1, 1}: true}}}}, snapshots: []indexer.SeaDexSnapshot{
		indexer.ConfigureSeaDex(factsSnapshot(indexer.SeaDexSnapshot{AniList: 11, Torrents: []indexer.SeaDexStanding{{InfoHash: h, Preference: 1}, {InfoHash: strings.Repeat("c", 40), Preference: 2, IsBest: true}}}), nil),
		indexer.ConfigureSeaDex(factsSnapshot(indexer.SeaDexSnapshot{AniList: 12, Torrents: []indexer.SeaDexStanding{{InfoHash: h, Preference: 1}, {InfoHash: strings.Repeat("d", 40), Preference: 2, IsBest: true}}}), nil),
	}}
	if st.snapshots[0].Torrents[0].Preference != 1 || st.snapshots[1].Torrents[0].Preference != 1 {
		t.Fatal("invalid configured ranks")
	}
	ctx := WithSeaDexTitle(context.WithValue(context.Background(), seaDexContext{}, indexStanding(st)), 1)
	held := &ExistingFile{Quality: "WEBDL-2160p", Revision: 1, InfoHash: h, SeaDexPreference: 3, seaDexEpisodes: [][2]int{{1, 1}}}
	got := resolveSeaDex(ctx, held)
	e, v, _ := reviewCutoffEngine(t, "tv")
	title := show()
	incoming := &ExistingFile{Quality: "Bluray-2160p Remux", Revision: 1, InfoHash: strings.Repeat("b", 40), SeaDexPreference: 2}
	before, whyBefore, err := e.ImportUpgrade(&title, v, incoming, held)
	if err != nil {
		t.Fatal(err)
	}
	after, whyAfter, err := e.ImportUpgrade(&title, v, incoming, &got)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("held rank=3; entry 11 and 12 rank=1; entry 13 has no snapshot: got %d; rank-2 import before=%v (%s), after=%v (%s)", got.SeaDexPreference, before, whyBefore, after, whyAfter)
	if got.SeaDexPreference != 3 {
		t.Fatal("lost held protection despite an unknown mapped membership")
	}
}
