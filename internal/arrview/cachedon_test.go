package arrview

import (
	"slices"
	"testing"

	"github.com/theatarr/theatarr/internal/indexer"
)

// cachedOn takes every piece of cache evidence the search put on the release, not only DMM's own extra: a Torrentio
// release whose hash DMM's AllDebrid feed listed is cached on ad.
func TestCachedOnFromEvidence(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		rel  indexer.Release
		want []string
	}{
		{indexer.Release{Indexer: "torrentio", CachedOn: []string{"ad"}}, []string{"ad"}},
		{indexer.Release{Indexer: "dmm", Extras: map[string]string{"cached_on": "rd,ad"}}, []string{"rd", "ad"}},
		{indexer.Release{Indexer: "dmm-tb", Extras: map[string]string{"cached_on": "tb"}, CachedOn: []string{"rd", "tb"}},
			[]string{"rd", "tb"}},
		{indexer.Release{Indexer: "torrentio"}, nil},
	} {
		if got := cachedOn(&c.rel); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.rel.Indexer, got, c.want)
		}
	}
}
