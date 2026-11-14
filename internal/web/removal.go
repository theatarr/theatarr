package web

import (
	"context"
	"encoding/json"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
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
// context (Config.Background), pinned to the settings generation that removed (CaptureRuntime), and writes each
// removed library its own torrents' outcome (removal.RecordCleanup). The arr views' DELETE hands its clean-up here
// too (arrview SetCleanup). Removal is manager-accessible, its clean-up included; the torrent manager's own delete
// stays admin only. Nothing is retried.
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
		for k, id := range c.Actions {
			ws := removal.JoinWords(words(c.HashesOf(k)), c.Kept[k])
			w.log.Info("removal: debrid clean-up", "title", c.TitleID, "version", k, "outcome", ws)
			removal.RecordCleanup(context.WithoutCancel(ctx), w.st, w.log, id, ws)
		}
		// Open History and Torrents pages refetch the outcome.
		var name string
		_ = w.st.Reader().QueryRowContext(context.WithoutCancel(ctx), `SELECT name FROM title WHERE id = ?`,
			c.TitleID).Scan(&name)
		data, _ := json.Marshal(TitleEvent{ID: c.TitleID, Name: name, Reason: "versions"})
		w.hub.publish(event{name: "title", data: data, need: auth.PermBrowse})
	}()
}

// cleanup runs the clean-up and answers the words for a library's hashes.
func (w *Web) cleanup(ctx context.Context, c removal.Cleanup) func([]string) string {
	fixed := func(s string) func([]string) string { return func([]string) string { return s } }
	if w.torrents == nil {
		return fixed("torrents not cleaned up: the torrent manager is not running")
	}
	m, err := w.torrents.Manager(ctx)
	if err != nil {
		return fixed("torrents not cleaned up: " + err.Error())
	}
	out, err := m.DeleteForRemoval(ctx, torrents.RemovalCleanup{TitleID: c.TitleID, Hashes: c.Hashes, By: c.By})
	if err != nil {
		return fixed("torrents not cleaned up: " + err.Error())
	}
	return out.Words
}
