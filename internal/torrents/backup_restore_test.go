package torrents

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/router"
)

// A restore re-adds every hash of the backup that is not on its target: the backup is the
// user's own debrid list, so a hash Theatarr saw from a private-labelled indexer is re-added like any other.
func TestRestoreReaddsPrivateLabelledHashes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	dmm := []byte(`[{"filename":"A","hash":"` + hA + `"},{"filename":"B","hash":"` + hB + `"}]`)
	b, _, err := e.m.ImportBackup(ctx, dmm, "rd", "admin")
	if err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO router_plan (title_id, version_key, mode, outcome, release_title, indexer, info_hash, created_at)
		VALUES (1, 'v', 'shadow', 'would_add', 'A.2024.1080p', 'priv', ?, 0)`, hA)
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at, release_data) VALUES (1, 1, 'v', ?, 'B', 'priv', 'imported', 0, 0, '{"private":true}')`, hB)
	plan, err := e.m.PlanRestore(ctx, RestoreRequest{BackupID: b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 2 || plan.Readd != 2 {
		t.Fatalf("plan %+v", plan)
	}
	for _, it := range plan.Items {
		if it.Action != ActReadd {
			t.Errorf("%s: %s (%s), want %s", it.Name, it.Action, it.Reason, ActReadd)
		}
	}
}

// routeLock stands for the router's add lock while a route holds it: the first time the restore asks for it, the
// route (still holding it) does its work first.
type routeLock struct {
	sync.Mutex
	once  sync.Once
	route func()
}

func (l *routeLock) Lock() {
	l.once.Do(l.route)
	l.Mutex.Lock()
}

// A restore reserves its pacing slot under the router's add lock, just before the add: a route that
// adds while the restore waits for the lock leaves Real-Debrid's 20 s min_gap before the restore's add, and a back-off
// a route started meanwhile stops the restore instead of the add going out.
func TestRestorePacingReservedUnderAddLock(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		route   func(e *env) error
		outcome string
	}{
		{"route add", func(e *env) error {
			_, _, err := router.ReserveAdd(context.Background(), e.st, "rd", e.pol.Providers["rd"].EffectivePacing(),
				e.now)
			return err
		}, OutUncached},
		{"route back-off", func(e *env) error {
			return router.RecordAddOutcome(context.Background(), e.st, "rd", e.pol.Providers["rd"].EffectivePacing(),
				e.now, router.AddOutcome{Reason: "auth", ToMax: true})
		}, OutSkipped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.ard.selects = false // the pacing alone: no file selection after the add
			ctx := context.Background()
			b, _, err := e.m.ImportBackup(ctx, []byte(`[{"filename":"D","hash":"`+hD+`"}]`), "rd", "admin")
			if err != nil {
				t.Fatal(err)
			}
			var routeAt, sentAt time.Time
			e.m.AddLock = &routeLock{route: func() {
				e.now = e.now.Add(30 * time.Second) // the route's slow account read
				routeAt = e.now
				if err := tc.route(e); err != nil {
					t.Error(err)
				}
			}}
			e.ard.onAdd = func(string, string) { sentAt = e.now }
			req := RestoreRequest{BackupID: b.ID}
			plan, err := e.m.PlanRestore(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			got, err := e.m.Restore(ctx, req, plan.Confirmed(), "admin")
			if err != nil {
				t.Fatal(err)
			}
			if o := got.Items[0].Outcome; o != tc.outcome {
				t.Fatalf("outcome %s (%s), want %s", o, got.Items[0].Error, tc.outcome)
			}
			if tc.outcome == OutSkipped {
				if len(e.ard.added) != 0 {
					t.Fatalf("sent during the route's back-off: %v", e.ard.added)
				}
				return
			}
			if gap := sentAt.Sub(routeAt); gap < 20*time.Second {
				t.Fatalf("the restore's add went %s after the route's (min_gap 20s)", gap)
			}
		})
	}
}

// A delete checks the usage again after its backup, under the router's add lock: a route that took up a
// confirmed torrent while the backup ran (here a download of its hash, as a reuse or an AllDebrid re-add makes)
// keeps it; the other deletes run under the lock.
func TestDeleteRechecksUsageAfterTheBackup(t *testing.T) {
	t.Parallel()
	e := deleteFixture(t)
	ctx := context.Background()
	e.rd.list = append(e.rd.list, tor("9", hG, "Movie.G.2010.1080p.WEB-DL", 900, e.now.Add(-72*time.Hour),
		provider.StateDownloaded))
	e.content(hG, 1)
	sel := []DeleteSelection{{Provider: "rd", ID: "6", Hash: hF}, {Provider: "rd", ID: "9", Hash: hG}}
	plan, err := e.m.PlanDelete(ctx, sel)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Count != 2 {
		t.Fatalf("plan: %+v", plan.Items)
	}
	lock := &trackLock{}
	e.m.AddLock = lock
	routed := false
	e.rd.onDetail = func() {
		if !routed {
			routed = true
			e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
				provider, provider_torrent_id, reused, created_at, updated_at)
				VALUES (1, 1, 'v', ?, 'F', 'pub', 'added', 'rd', '6', 1, 0, 0)`, hF)
		}
	}
	var locked []bool
	e.ard.onDelete = func() { locked = append(locked, lock.held) }
	done, err := e.m.Delete(ctx, sel, plan.Token, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if !routed {
		t.Fatal("the backup read no details")
	}
	if e.ard.deleted != 1 || done.Items[0].Outcome != OutSkipped || !strings.Contains(done.Items[0].Error, "changed") ||
		done.Items[1].Outcome != "deleted" {
		t.Fatalf("deleted %d: %+v", e.ard.deleted, done.Items)
	}
	if len(locked) != 1 || !locked[0] || lock.held {
		t.Fatalf("deletes under the add lock %v, left held %v", locked, lock.held)
	}
}
