package router

import (
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// TestRestoreInFlightIsNeverOurAdd: a torrent manager restore of a hash sent after a route read
// AllDebrid's magnet list and before its upload hands the route the restore's magnet, which looked like the route's
// own new add: with router.alldebrid_delete_uncached on, the route deleted the user's restore as uncached. A restore
// journaled since the route's look at the account (less addSettle) now keeps the entry.
func TestRestoreInFlightIsNeverOurAdd(t *testing.T) {
	t.Parallel()
	title := "Movie.2024.2160p.WEB-DL.DDP5.1-GRP" // RD's name rule refuses it: AD is tried first
	setup := func() (*env, *fakeDebrid) {
		e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
		e.r.set.AllDebridDeleteUncached = true
		// Sent by a restore (any process) moments before the route; not on the account's list yet.
		e.exec(`INSERT INTO torrent_journal (action, provider, info_hash, outcome, created_at)
			VALUES ('restore', 'ad', ?, 'sending', ?)`, strings.ToUpper(h("a")), e.clock.Add(-time.Minute).UnixMilli())
		// A restore long before, and one that never sent anything, change nothing.
		e.exec(`INSERT INTO torrent_journal (action, provider, info_hash, outcome, created_at)
			VALUES ('restore', 'ad', ?, 'added', ?), ('restore', 'ad', ?, 'skipped', ?)`,
			h("b"), e.clock.Add(-time.Hour).UnixMilli(), h("b"), e.clock.UnixMilli())
		return e, f
	}
	e, f := setup()
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), title)))
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(res); !strings.Contains(got, "ad/accepted/uncached_kept_shared") ||
		!strings.Contains(res.Steps[len(res.Steps)-1].Message, "torrent manager restored") {
		t.Fatalf("steps %s %+v", got, res.Steps)
	}
	if f.deletes() != 0 {
		t.Fatal("the restore's magnet was deleted")
	}
	// Without a recent restore of its hash the switch still deletes our own uncached add.
	e, f = setup()
	res, err = e.r.Route(e.ctx, movieReq(cand(h("b"), title)))
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(res); !strings.Contains(got, "ad/refused/uncached_deleted") || f.deletes() != 1 {
		t.Fatalf("steps %s, deletes %d", got, f.deletes())
	}
}

// The router's add lock is the one every generation shares.
func TestAddLockIsShared(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	next, err := New(Config{Store: e.st, Policy: e.pol, Previous: e.r})
	if err != nil {
		t.Fatal(err)
	}
	if next.AddLock() != e.r.AddLock() {
		t.Fatal("generations hold different add locks")
	}
}
