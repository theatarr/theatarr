package decide

import (
	"context"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	am "github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
)

func TestPreferOffDuplicateMappingCountsEpisodesOnce(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	ti := animeMapped("auto")
	// Overlapping ranges such as "S01E01-E02, S01E01-E02" are accepted by mapping overrides.
	entry := *ti.AnimeMap.Entries[0]
	entry.Episodes = []am.SE{{1, 1}, {1, 1}, {1, 1}, {1, 2}}
	ti.AnimeMap = animemap.New(ti.TVDB, []*animemap.Entry{&entry}, nil)
	v := tvVersion(e, "anime-tv")
	vp := *v.Policy
	vp.PreferSeaDex = false
	v.Policy = &vp
	p := e.Profile(vp.Profile, format.Sonarr)
	q := profile.MustQuality(format.Sonarr, "Bluray-1080p")
	maxSize := float64(1)
	p.Engine.Definitions = profile.DefaultDefinitions(format.Sonarr)
	p.Engine.Definitions.Set(profile.Definition{Quality: q, MaxSize: &maxSize})
	r := rel("Isekai Story S01 Bluray 1080p (record)", "public", 80*1024*1024)
	r.Kind = "seadex"
	r.Extras = map[string]string{"anilist": "1"}
	d, err := e.Decide(context.Background(), r, ti, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("prefer-off: episodes=%v missing=%d quality=%s accepted=%v rejections=%v", d.Episodes, d.Missing, d.Quality, d.Accepted, d.Rejections)
	if len(d.EpisodeIDs) != 2 {
		t.Fatalf("duplicate mapping yielded %d episode entries, want 2", len(d.EpisodeIDs))
	}
	if d.Accepted || !strings.Contains(reasons(d), "AboveMaximumSize") {
		t.Fatalf("duplicate mappings inflated the size allowance: %+v", d)
	}

}
