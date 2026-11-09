package retention

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

func TestPruneQualityReviewsKeepsPendingRecentCapAndRunReferences(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	now := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	old := store.Millis(now.Add(-400 * 24 * time.Hour))
	insert := func(source, state string, at int64) int64 {
		return exec(t, st, `INSERT INTO settings_quality_review(source_key,base_generation,commit_id,preview,candidate,state,created_at) VALUES(?,1,'fixture','{}','{}',?,?)`, source, state, at)
	}
	// The first closed row is still needed by the retained latest run.
	referenced := insert("guides", "applied", old)
	exec(t, st, `INSERT INTO settings_quality_sync_run(source_key,started_at,outcome,review_id) VALUES('guides',?,'applied',?)`, old, referenced)
	gone := insert("guides", "superseded", old)
	fresh := insert("guides", "superseded", store.Millis(now))
	pending := insert("guides", "pending", old)
	var retained []int64
	for i := 0; i < 100; i++ {
		state := "applied"
		if i%2 == 0 {
			state = "superseded"
		}
		retained = append(retained, insert("guides", state, old+int64(i)))
	}
	other := insert("other", "applied", old)
	sum, err := Prune(context.Background(), st, now, Defaults)
	if err != nil {
		t.Fatal(err)
	}
	if sum["settings_quality_review"] != 1 {
		t.Fatalf("pruned %v, want one obsolete review", sum)
	}
	for _, id := range append(retained, referenced, fresh, pending, other) {
		if n := count(t, st, `SELECT COUNT(*) FROM settings_quality_review WHERE id=?`, id); n != 1 {
			t.Errorf("review %d removed", id)
		}
	}
	if n := count(t, st, `SELECT COUNT(*) FROM settings_quality_review WHERE id=?`, gone); n != 0 {
		t.Fatal("obsolete candidate kept")
	}
	// The cap does not override an explicitly disabled history window.
	for range 2 {
		insert("guides", "applied", old)
	}
	sum, err = Prune(context.Background(), st, now, Config{Runs: Defaults.Runs})
	if err != nil || sum["settings_quality_review"] != 0 {
		t.Fatal(fmt.Sprint(sum, err))
	}
}
