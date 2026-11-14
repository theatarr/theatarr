package web

import (
	"context"
	"time"

	"github.com/theatarr/theatarr/internal/removal"
	"github.com/theatarr/theatarr/internal/torrents"
)

// cleanupBound bounds one removal's debrid clean-up: the wait for copies still adding, the backup and the deletes.
const cleanupBound = 2 * time.Hour

// removalDeps are the shared removal's (internal/removal) for the web UI: the library links through Ops, the
// requests after it (TitleRemoved), and the debrid clean-up on the service's context (StartCleanup).
func (w *Web) removalDeps() removal.Deps {
	return removal.Deps{Store: w.st, Unlinker: removalUnlinker{w.ops}, AfterRemove: w.TitleRemoved,
		StartCleanup: w.StartCleanup, Now: w.now, Log: w.log}
}

type removalUnlinker struct{ ops Ops }

func (u removalUnlinker) UnlinkRemoved(ctx context.Context, linkID int64, reason string) (string, error) {
	if u.ops == nil {
		return "", fakeRefusal("the library is not available")
	}
	l, err := u.ops.UnlinkRemoved(ctx, linkID, reason)
	if err != nil || l == nil {
		return "", err
	}
	return l.Trash, nil
}

// fakeRefusal is a refusal of ours (Refused() true): the link stays and nothing changed.
type fakeRefusal string

func (f fakeRefusal) Error() string { return string(f) }
func (fakeRefusal) Refused() bool   { return true }

// StartCleanup runs a removal's debrid clean-up (torrents.DeleteForRemoval) after the answer, on the service's
// context (Config.Background), pinned to the settings generation that removed (CaptureRuntime), and writes its
// outcome into the Removed actions (removal.RecordCleanup). The arr views' DELETE hands its clean-up here too
// (arrview SetCleanup). Removal is manager-accessible, its clean-up included; the torrent manager's own delete stays
// admin only. Nothing is retried.
func (w *Web) StartCleanup(c removal.Cleanup) {
	ctx := w.background
	if w.captureRuntime != nil {
		ctx = w.captureRuntime(ctx)
	}
	w.cleanups.Add(1)
	go func() {
		defer w.cleanups.Done()
		ctx, cancel := context.WithTimeout(ctx, cleanupBound)
		defer cancel()
		words := w.cleanup(ctx, c)
		w.log.Info("removal: debrid clean-up", "title", c.TitleID, "hashes", len(c.Hashes), "outcome", words)
		removal.RecordCleanup(context.WithoutCancel(ctx), w.st, w.log, c.Actions, words)
	}()
}

func (w *Web) cleanup(ctx context.Context, c removal.Cleanup) string {
	if w.torrents == nil {
		return "torrents not cleaned up: the torrent manager is not running"
	}
	m, err := w.torrents.Manager(ctx)
	if err != nil {
		return "torrents not cleaned up: " + err.Error()
	}
	words, err := m.DeleteForRemoval(ctx, torrents.RemovalCleanup{Hashes: c.Hashes, By: c.By})
	if err != nil {
		return "torrents not cleaned up: " + err.Error()
	}
	return words
}
