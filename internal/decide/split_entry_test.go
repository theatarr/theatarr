package decide

import (
	"context"
	"testing"

	animemeta "github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
)

// anime/merge.go:227 splitFarApart turns one ONA/OVA AniList entry released >180 days apart into al:N#1..al:N#k,
// one episode each: a partition of one entry, not an ambiguity. A SeaDex season pack for AniList N used to get the
// union of those parts in the asked season (keyless); seaDexEntry now calls it ambiguous and gives no episodes.
// This runs for prefer-off versions too (tvVersion is prefer-off here).
func TestSplitEntrySeasonPack(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	ti := animeMapped("auto")
	first := *ti.AnimeMap.Entries[0]
	var parts []*animemap.Entry
	for i := 1; i <= 3; i++ {
		p := first
		p.Key = "al:9#" + string(rune('0'+i))
		p.Episodes = []animemeta.SE{{1, i}}
		parts = append(parts, &p)
	}
	ti.AnimeMap = animemap.New(2002, parts, nil)
	r := rel("Isekai Story S01 Bluray 1080p remux (entry)", "public", 12*gb)
	r.Kind = "seadex"
	r.Extras = map[string]string{"anilist": "9"}
	d, err := e.Decide(context.Background(), r, ti, tvVersion(e, "anime-tv"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("episodes=%v mapping=%s entry=%q rejections=%v", d.EpisodeIDs, d.AnimeMapping, d.AnimeEntry, d.Rejections)
	if len(d.EpisodeIDs) != 3 {
		t.Fatalf("split AniList entry (al:9#1..#3) season pack lost its episodes: %v", d.EpisodeIDs)
	}
	if d.AnimeEntry != "" {
		t.Fatalf("split union has a single entry key: %q", d.AnimeEntry)
	}
	parts[1].Episodes = parts[0].Episodes
	ti.AnimeMap = animemap.New(2002, parts, nil)
	d, err = e.Decide(context.Background(), r, ti, tvVersion(e, "anime-tv"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.EpisodeIDs) != 0 {
		t.Fatalf("overlapping split parts must fail closed: %v", d.EpisodeIDs)
	}

}
