package router

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// An import recorded as a dry run (the library gate refused the version: library_import dry_run) stays ready with
// nothing blocklisted past the ready deadline; a later pass imports it once the gate allows it.
func TestDryRunImportNeverExpires(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	start := e.clock
	old := store.Millis(start.Add(-24 * time.Hour))
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
		ready_at, added_at, created_at, updated_at) VALUES (5, 1, 10, 'movie', ?, 'Movie', 'pub', 'ready', ?, ?, ?, ?)`,
		h("a"), old, old, old, old)
	e.exec(`INSERT INTO library_import (download_id, state, last_error, created_at, updated_at)
		VALUES (5, 'dry_run', 'library.enabled is false', ?, ?)`, old, old)
	e.clock = start.Add(e.r.set.ReadyTimeout + time.Hour)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.count(`SELECT COUNT(*) FROM download WHERE id = 5 AND state = 'ready'`); got != 1 {
		t.Errorf("dry-run download no longer ready")
	}
	if got := e.count(`SELECT COUNT(*) FROM router_blocklist WHERE download_id = 5`); got != 0 {
		t.Errorf("dry-run download blocklisted: %d rows", got)
	}
}
