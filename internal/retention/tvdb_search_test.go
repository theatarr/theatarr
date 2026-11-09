package retention

import (
	"context"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// Regression: expired TVDB free-text search rows (arr series/lookup?term=) are never pruned.
func TestPruneLeavesExpiredTVDBSearches(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	now := time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC)
	at := store.Millis(now)
	for _, key := range []string{
		"GET https://api4.thetvdb.com/v4/search?query=the+offi&type=series",
		"GET https://api4.thetvdb.com/v4/search?query=the+offic&type=series",
		"GET https://api4.thetvdb.com/v4/search/remoteid/tt0386676",
	} {
		exec(t, st, `INSERT INTO metadata_cache (source, key, status, body, fetched_at, expires_at)
   VALUES ('tvdb', ?, 200, x'00', ?, ?)`, key, at-40*24*3600*1000, at-30*24*3600*1000)
	}
	sum, err := Prune(context.Background(), st, now, Defaults)
	if err != nil {
		t.Fatal(err)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM metadata_cache WHERE source = 'tvdb'`); n != 0 {
		t.Fatalf("expired TVDB search rows kept: %d (pruned %v)", n, sum)
	}
}
