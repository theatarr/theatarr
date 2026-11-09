package decide

import (
	"context"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/indexer"
)

// A download whose hash and magnet name different torrents must not become curated at import either.
func TestIncomingRejectsConflictingIdentity(t *testing.T) {
	t.Parallel()
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	ctx := configuredState([]indexer.SeaDexSnapshot{{AniList: 1, Torrents: []indexer.SeaDexStanding{{InfoHash: b, Preference: 3, IsBest: true}}}},
		map[int64][]int{1: {1}}, 1)
	rel := indexer.Release{Title: "Movie 2020 1080p", InfoHash: b, Magnet: indexer.BareMagnet(a)}
	rel = RefreshSeaDexRelease(ctx, rel) // loadLinkedMetadata
	f := LinkedFile("movie", "", Release{Release: rel}, "Bluray-1080p")
	t.Logf("after RefreshSeaDexRelease (upgrade gate view): rank=%d entry=%d", f.SeaDexPreference, f.SeaDexEntry)
	ResolveIncomingSeaDexFile(ctx, nil, f, nil) // apply.go:68
	t.Logf("after ResolveIncomingSeaDexFile (stored metadata/state): rank=%d entry=%d", f.SeaDexPreference, f.SeaDexEntry)
	if f.SeaDexPreference != 0 {
		t.Errorf("conflicting identity took standing at import: rank %d", f.SeaDexPreference)
	}
}

func TestIncomingEntryOnlyBreaksCoveringTies(t *testing.T) {
	t.Parallel()
	hash := strings.Repeat("b", 40)
	ctx := configuredState([]indexer.SeaDexSnapshot{
		{AniList: 11, Torrents: []indexer.SeaDexStanding{{InfoHash: hash, Preference: 3}}},
		{AniList: 12, Torrents: []indexer.SeaDexStanding{{InfoHash: hash, Preference: 1}}},
	}, map[int64][]int{1: {11, 12}}, 1)
	state := ctx.Value(seaDexContext{}).(seaDexState)
	state.series = map[int64]bool{1: true}
	state.cover = map[int64]map[int]*indexer.SeaDexEntryMapping{1: {
		11: {Series: true, Episodes: map[[2]int]bool{{1, 1}: true}},
		12: {Series: true, Episodes: map[[2]int]bool{{1, 1}: true}},
	}}
	ctx = context.WithValue(ctx, seaDexContext{}, state)
	title := &Title{Episodes: []Episode{{ID: 1, Season: 1, Number: 1}, {ID: 2, Season: 1, Number: 2}}}
	for _, tc := range []struct {
		name        string
		ids         []int64
		rank, entry int
	}{
		{"covering tie", []int64{1}, 1, 12},
		{"uncovered", []int64{2}, 0, 0},
		{"partially covered", []int64{1, 2}, 0, 0},
		{"unknown scope", nil, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := LinkedFile("series", "", Release{Release: indexer.Release{InfoHash: hash, Extras: map[string]string{"anilist": "12"}}}, "WEBDL-1080p")
			ResolveIncomingSeaDexFile(ctx, title, f, tc.ids)
			if f.SeaDexPreference != tc.rank || f.SeaDexEntry != tc.entry {
				t.Fatalf("rank/entry=%d/%d want %d/%d", f.SeaDexPreference, f.SeaDexEntry, tc.rank, tc.entry)
			}
		})
	}
}
