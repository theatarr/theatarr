package decide

import (
	"context"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/indexer"
)

func TestConflictingIdentityKeepsCoveringAlternatives(t *testing.T) {
	t.Parallel()
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	ctx := configuredState([]indexer.SeaDexSnapshot{{AniList: 11, Torrents: []indexer.SeaDexStanding{{InfoHash: b, Preference: 3, IsBest: true}}}},
		map[int64][]int{1: {11}}, 1)
	state := ctx.Value(seaDexContext{}).(seaDexState)
	state.series = map[int64]bool{1: true}
	state.cover = map[int64]map[int]*indexer.SeaDexEntryMapping{1: {
		11: {Series: true, Episodes: map[[2]int]bool{{1, 1}: true}},
	}}
	ctx = context.WithValue(ctx, seaDexContext{}, state)
	title := &Title{Episodes: []Episode{{ID: 1, Season: 1, Number: 1}, {ID: 2, Season: 1, Number: 2}}}
	for _, tc := range []struct {
		name         string
		ids          []int64
		alternatives int
	}{
		{"covered", []int64{1}, 1},
		{"uncovered", []int64{2}, 0},
		{"partially covered", []int64{1, 2}, 0},
		{"unknown scope", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rel := RefreshSeaDexRelease(ctx, indexer.Release{InfoHash: b, Magnet: indexer.BareMagnet(a), Extras: map[string]string{"anilist": "11"}})
			f := LinkedFile("series", "", Release{Release: rel}, "WEBDL-1080p")
			ResolveIncomingSeaDexFile(ctx, title, f, tc.ids)
			if f.SeaDexPreference != 0 || f.SeaDex != format.SeaDexNone || f.SeaDexEntry != 0 {
				t.Fatalf("conflicting identity gained standing: %+v", f)
			}
			if len(f.SeaDexAlternatives) != tc.alternatives {
				t.Fatalf("alternatives=%d, want %d", len(f.SeaDexAlternatives), tc.alternatives)
			}
		})
	}
}
