package notify

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// failingPayloads fails while fail is set, as a transient payload preparation failure.
type failingPayloads struct{ fail *bool }

func (f failingPayloads) Payloads(ctx context.Context, ev Event) ([]Payload, error) {
	if *f.fail {
		return nil, errors.New("title lookup failed")
	}
	return fakePayloads{}.Payloads(ctx, ev)
}

// A grab or download event whose preparation fails is not marked sent; the next scan sends it.
func TestScanRetriesEventAfterPreparationFailure(t *testing.T) {
	t.Parallel()
	e := newNEnv(t, true)
	ctx := context.Background()
	if err := e.n.Scan(ctx); err != nil { // sets since
		t.Fatal(err)
	}
	fail := true
	e.n.pay = failingPayloads{fail: &fail}
	e.now = e.now.Add(time.Minute)
	at := store.Millis(e.now)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at, added_at, imported_at) VALUES (2, 1, 2, 'movie-web', 'bb', 'New', 'dmm', 'imported', ?, ?, ?, ?)`,
		at, at, at, at)
	_ = e.n.Scan(ctx)
	fail = false
	if err := e.n.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []string{"theatarr.grab", "theatarr.download"} {
		if n := e.one(`SELECT COUNT(*) FROM notify_delivery WHERE event = ? AND target = 'policy:automation'`, ev); n != 1 {
			t.Errorf("%s deliveries %d after the failure cleared, want 1", ev, n)
		}
	}
}
