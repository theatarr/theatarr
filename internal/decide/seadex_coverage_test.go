package decide

import (
	"context"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
	"testing"
)

func TestSeaDexEntryCoversOnlyItsCour(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	ti := animeMapped("auto")
	first := *ti.AnimeMap.Entries[0]
	second := first
	first.Episodes = first.Episodes[:6]
	second.Key = "al:9"
	second.Episodes = second.Episodes[6:]
	ti.AnimeMap = animemap.New(2002, []*animemap.Entry{&first, &second}, nil)
	r := rel("Isekai Story S01 Bluray 1080p remux (entry)", "public", 12*gb)
	r.Kind = "seadex"
	r.Extras = map[string]string{"anilist": "9"}
	d, err := e.Decide(context.Background(), r, ti, tvVersion(e, "anime-tv"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.EpisodeIDs) != 6 || d.EpisodeIDs[0] != 307 || d.EpisodeIDs[5] != 312 {
		t.Fatalf("one cour claimed the whole season: %v (%v)", d.EpisodeIDs, d.Rejections)
	}
}
