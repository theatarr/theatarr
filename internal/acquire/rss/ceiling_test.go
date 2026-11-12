package rss

import (
	"strings"
	"testing"
)

// A rejected RSS item still proves the source exists, without counting as an absence search.
func TestRSSCeilingEvidence(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	e.seed(t)
	e.prow.setRSS(item{title: alienWebRel, hash: hAlienWeb, size: 20e9, pub: t0},
		item{title: "Alien.Romulus.2024.720p.HDTV.x264-GRP", hash: strings.Repeat("a", 40), size: 2e9, pub: t0})
	if _, err := e.r.RSS(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM source_sighting WHERE source LIKE 'rss:%'`); n == 0 {
		t.Fatal("RSS sightings not recorded")
	}
	if n := one[int](t, e.st, "SELECT COUNT(*) FROM source_sighting WHERE source LIKE 'rss:%' AND tier='tv'"); n == 0 {
		t.Fatal("rejected RSS quality not recorded")
	}
	if sources := one[string](t, e.st, `SELECT COALESCE(GROUP_CONCAT(DISTINCT source),'') FROM ceiling_search`); strings.Contains(sources, "rss") {
		t.Fatalf("RSS counted as searches: %s", sources)
	}
}
