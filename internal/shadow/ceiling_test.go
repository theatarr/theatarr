package shadow

import (
	"strings"
	"testing"
	"time"
)

func TestSearchAndRSSCeilingEvidence(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.prow.set(argylle.imdb, item{title: argWeb, hash: hArgWeb, size: 20e9, pub: t0.Add(-time.Hour)})
	e.zurg.add(grabEvent(2, argylle, t0.Add(-time.Hour), argWeb, hArgWeb, "WEBDL-2160p", 18, 1700, "Search"))
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM ceiling_search_indexer WHERE source_kind='torznab' AND positive_only=0`); n == 0 {
		t.Fatal("shadow search lost indexer coverage")
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM source_sighting WHERE source LIKE 'shadow_search:%'`); n == 0 {
		t.Fatal("shadow search lost candidates")
	}
	// A rejected RSS item still proves the source exists, without counting as an absence search.
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
