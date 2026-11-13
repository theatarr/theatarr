package router

import (
	"context"
	"testing"

	"github.com/theatarr/theatarr/internal/provider"
)

// The final pass (copy.go): the delete decides from a read under the add lock, gone needs a listing, and a worker
// that lost its row stops.

// P1: the torrent completes while the delete waits for the add lock: kept, Cached on, added.
func TestCopyFinalReadUnderLock(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloading, "rd")
	e.seedChecking("rd", "rd-1", provider.StateDownloading, true)
	reads := 0
	e.pr["rd"].read = func(_ context.Context, id string) {
		if reads++; reads == 2 { // the read under the lock
			e.pr["rd"].set("rd-1", provider.Torrent{Hash: h("a"), State: provider.StateDownloaded})
		}
	}
	e.run()
	if s, _ := e.copyState(h("a"), "rd"); s != CopyAdded || e.del["rd"].deleted() != 0 ||
		e.count(`SELECT COUNT(*) FROM content_provider WHERE provider = 'rd' AND progress = 100`) != 1 {
		t.Fatalf("%s, %d deletes", s, e.del["rd"].deleted())
	}
}

// P2: a not-found read while the listing still shows the torrent is not "gone": the row stays and the delete is
// retried.
func TestCopyFinalGoneNeedsListing(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloading, "rd")
	e.seedChecking("rd", "rd-1", provider.StateDownloading, true)
	e.pr["rd"].readErr = map[string]error{"rd-1": provider.ErrNotFound}
	e.pr["rd"].list = []provider.Torrent{{ID: "rd-1", Hash: h("a")}}
	e.run()
	if s, _ := e.copyState(h("a"), "rd"); s != CopyChecking ||
		e.count(`SELECT COUNT(*) FROM job WHERE kind = ? AND state = 'queued'`, JobCopy) != 1 {
		t.Fatalf("%s", s)
	}
}

// P3: the row turned refused while the worker read DMM: the worker no longer owns it and sends nothing.
func TestCopyFinalLostRowStops(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloaded, "rd")
	e.r.availHash = func(context.Context, string, string) (bool, bool, error) {
		e.exec(`UPDATE provider_copy SET state = 'refused' WHERE info_hash = ?`, h("a"))
		return false, true, nil
	}
	e.queue(h("a"), "Movie.2024.2160p.WEB-GRP")
	e.run()
	if s, _ := e.copyState(h("a"), "rd"); s != CopyRefused || len(e.ad["rd"].adds) != 0 {
		t.Fatalf("%s, adds %v", s, e.ad["rd"].adds)
	}
}
