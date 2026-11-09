package torrents

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/router"
)

// trackLock is a sync.Locker that records whether it is held.
type trackLock struct {
	sync.Mutex
	held bool
}

func (l *trackLock) Lock()   { l.Mutex.Lock(); l.held = true }
func (l *trackLock) Unlock() { l.held = false; l.Mutex.Unlock() }

// A restore's add is sent under the router's add lock and journaled as sending before it goes out, so a route never
// takes it for its own add (router.RestoredSince). The row ends with the add's outcome.
func TestRestoreAddIsAnnouncedAndLocked(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	old := e.now.Add(-72 * time.Hour)
	e.rd.list = []provider.Torrent{tor("4", hD, "D", 4, old, provider.StateDownloaded)}
	b, _, err := e.m.TakeBackup(ctx, BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	e.rd.list = nil
	lock := &trackLock{}
	e.m.AddLock = lock
	var locked bool
	var seen string
	e.ard.onAdd = func(string, string) {
		locked = lock.held
		seen, _ = router.RestoredSince(ctx, e.m.Store.Writer(), "rd", hD, e.now.Add(-time.Minute))
	}
	req := RestoreRequest{BackupID: b.ID}
	plan, err := e.m.PlanRestore(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Restore(ctx, req, plan.Confirmed(), "admin"); err != nil {
		t.Fatal(err)
	}
	if !locked || seen == "" {
		t.Fatalf("add sent under the lock %v, announced %q", locked, seen)
	}
	if lock.held {
		t.Fatal("the add lock was left held")
	}
	var n int
	var outcome, id string
	if err := e.m.Store.Reader().QueryRow(`SELECT COUNT(*), MAX(outcome), MAX(torrent_id) FROM torrent_journal
		WHERE action = 'restore'`).Scan(&n, &outcome, &id); err != nil {
		t.Fatal(err)
	}
	if n != 1 || outcome == OutSending || id == "" {
		t.Fatalf("journal rows %d, outcome %s, torrent %q", n, outcome, id)
	}
}
