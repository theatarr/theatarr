package router

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// ReserveAdd and RecordAddOutcome (a restore's adds) use the router's own pacing rows: the minute's adds, the hourly
// uncached cap and the back-off hold for both.
func TestReserveAddSharesPacing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	pc := policy.Pacing{AddsPerMinute: 2, UncachedPerHr: 1}
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	for i := range 2 {
		if until, why, err := ReserveAdd(ctx, st, "rd", pc, now); err != nil || !until.IsZero() {
			t.Fatalf("add %d: %v %s %v", i, until, why, err)
		}
	}
	until, why, err := ReserveAdd(ctx, st, "rd", pc, now.Add(time.Second))
	if err != nil || !until.Equal(now.Add(time.Minute)) || why == "" {
		t.Fatalf("third add in the minute: %v %q %v", until, why, err)
	}
	// The router reads the same row.
	p, err := loadPace(ctx, st.Reader(), "rd")
	if err != nil || p.WindowAdds != 2 {
		t.Fatalf("router's pace %+v %v", p, err)
	}
	later := now.Add(2 * time.Minute)
	if err := RecordAddOutcome(ctx, st, "rd", pc, later, AddOutcome{OK: true, Uncached: true}); err != nil {
		t.Fatal(err)
	}
	if until, _, _ := ReserveAdd(ctx, st, "rd", pc, later); until.IsZero() {
		t.Error("uncached_adds_per_hour 1 used, yet an add was allowed")
	}
	if err := RecordAddOutcome(ctx, st, "ad", pc, later, AddOutcome{Reason: "rate limited"}); err != nil {
		t.Fatal(err)
	}
	if until, why, _ := ReserveAdd(ctx, st, "ad", pc, later); !until.Equal(later.Add(BackoffBase)) || why == "" {
		t.Errorf("back-off after a failure: %v %q", until, why)
	}
}
