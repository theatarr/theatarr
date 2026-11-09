package router

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// An import whose every episode is held for anime mapping review (library.holdAnimeReview: library_import
// dry_run, reason MappingNotReviewed) is documented to stay ready with nothing blocklisted. The ready deadline fails
// and blocklists it.
func TestAnimeHeldImportExpires(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	start := e.clock
	old := store.Millis(start.Add(-24 * time.Hour))
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
		ready_at, added_at, created_at, updated_at) VALUES (5, 1, 10, 'movie', ?, 'Movie', 'pub', 'ready', ?, ?, ?, ?)`,
		h("a"), old, old, old, old)
	e.exec(`INSERT INTO library_import (download_id, state, last_error, created_at, updated_at)
		VALUES (5, 'dry_run', 'MappingNotReviewed: S01E01 franchise entry conflict', ?, ?)`, old, old)
	e.clock = start.Add(e.r.set.ReadyTimeout + time.Hour)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.count(`SELECT COUNT(*) FROM download WHERE id = 5 AND state = 'ready'`); got != 1 {
		t.Errorf("held (mapping review) download no longer ready")
	}
	if got := e.count(`SELECT COUNT(*) FROM router_blocklist WHERE download_id = 5`); got != 0 {
		t.Errorf("held (mapping review) download blocklisted: %d rows", got)
	}
}
