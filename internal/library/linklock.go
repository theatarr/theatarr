package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/torrents/reffence"
)

// Link locks. Every mutation of a link holds a lease on its path (library_lease 'link:<path>', 00070)
// from before it reads what it replaces until its database commit: an import, an unlink, a relink or revive, the
// sweep's dead mark and leftover cleanup, reconcile, adopt and rename. Two of them never
// interleave on one path, and the lease lives in the database, so it holds across processes (the CLI) and across
// the runtime generations a settings reload creates. A mutation that finds a path held does not wait: it leaves that
// link (ErrLinkChanged: an import retries, a sweep comes back, a client gets a 409). Each commit checks,
// in its transaction, that its leases are still its own (linksHeldTx), and the compare-and-swaps on the expected
// target and state (cas.go) stay as the last guard.

// leaseFor is how long a link lease lasts unless renewed; one left by a crash is taken over once it has passed.
const leaseFor = 10 * time.Minute

var leaseSeq atomic.Int64

type linkLock struct {
	owner string
	mu    sync.Mutex
	names map[string]bool
}

type linkLockKey struct{}

func linkLeaseName(path string) string { return "link:" + filepath.Clean(path) }

// lockLinks takes the link leases of paths, all of them or none (ErrLinkChanged while another run holds one), for
// the link lock ctx carries, or a new one. It returns ctx with the lock, and its release: of every lease of a new
// lock (also those taken later through the returned ctx), else of the leases this call took.
func (l *Library) lockLinks(ctx context.Context, paths ...string) (context.Context, func(), error) {
	ll, _ := ctx.Value(linkLockKey{}).(*linkLock)
	created := ll == nil
	if created {
		now := l.now()
		ll = &linkLock{owner: fmt.Sprintf("%d/%d", now.UnixNano(), leaseSeq.Add(1)), names: map[string]bool{}}
		ctx = context.WithValue(ctx, linkLockKey{}, ll)
	}
	ll.mu.Lock()
	var add []string
	seen := map[string]bool{}
	for _, p := range paths {
		if n := linkLeaseName(p); p != "" && !ll.names[n] && !seen[n] {
			seen[n] = true
			add = append(add, n)
		}
	}
	ll.mu.Unlock()
	release := func() {
		if created {
			ll.mu.Lock()
			add = add[:0]
			for n := range ll.names {
				add = append(add, n)
			}
			ll.mu.Unlock()
		}
		if len(add) > 0 {
			l.unlockLinks(ctx, ll, add)
		}
	}
	if len(add) == 0 {
		return ctx, release, nil
	}
	sort.Strings(add)
	now := l.now()
	err := l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, n := range add {
			res, err := tx.ExecContext(ctx, `INSERT INTO library_lease (name, owner, until) VALUES (?, ?, ?)
				ON CONFLICT (name) DO UPDATE SET owner = excluded.owner, until = excluded.until
				WHERE library_lease.until <= ? OR library_lease.owner = excluded.owner`,
				n, ll.owner, store.Millis(now.Add(leaseFor)), store.Millis(now))
			if err != nil {
				return err
			}
			if k, err := res.RowsAffected(); err != nil {
				return err
			} else if k == 0 {
				return linkChanged(0, n[len("link:"):], "", "another run is changing it now")
			}
		}
		return nil
	})
	if err != nil {
		add = nil
		return ctx, release, err
	}
	ll.mu.Lock()
	for _, n := range add {
		ll.names[n] = true
	}
	ll.mu.Unlock()
	return ctx, release, nil
}

// unlockLinks releases names of ll, even when ctx is cancelled.
func (l *Library) unlockLinks(ctx context.Context, ll *linkLock, names []string) {
	ll.mu.Lock()
	for _, n := range names {
		delete(ll.names, n)
	}
	ll.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, n := range names {
			if _, err := tx.ExecContext(ctx, `DELETE FROM library_lease WHERE name = ? AND owner = ?`, n, ll.owner); err != nil {
				return err
			}
		}
		// Link leases a crash left behind, long expired, go too: they are taken over only when the same path is.
		_, err := tx.ExecContext(ctx, `DELETE FROM library_lease WHERE name LIKE 'link:%' AND until <= ?`,
			store.Millis(l.now().Add(-leaseFor)))
		return err
	}); err != nil {
		l.log.Warn("library: link leases not released (they expire)", "links", len(names), "err", err)
	}
}

// linksHeldTx renews, inside tx, every link lease of ctx's lock, or fails with ErrLinkChanged when another run took
// one over (it expired): a commit that must not land without its leases checks them in its own transaction.
// Without a lock in ctx it does nothing.
func (l *Library) linksHeldTx(ctx context.Context, tx *sql.Tx) error {
	ll, _ := ctx.Value(linkLockKey{}).(*linkLock)
	if ll == nil {
		return nil
	}
	ll.mu.Lock()
	names := make([]string, 0, len(ll.names))
	for n := range ll.names {
		names = append(names, n)
	}
	ll.mu.Unlock()
	until := store.Millis(l.now().Add(leaseFor))
	for _, n := range names {
		res, err := tx.ExecContext(ctx, `UPDATE library_lease SET until = ? WHERE name = ? AND owner = ?`, until, n, ll.owner)
		if err != nil {
			return err
		}
		if k, err := res.RowsAffected(); err != nil {
			return err
		} else if k == 0 {
			return linkChanged(0, n[len("link:"):], "", "its link lease was taken over")
		}
	}
	return nil
}

// refWriteTx runs fn as a write that makes a link or points one at a new target, a reference the torrent manager's
// delete checks: under the torrent delete fence (reffence), so it never lands between a delete's last
// usage check and the provider's delete. While another process deletes a torrent it is refused as ErrLinkChanged
// (an import retries, a client gets a 409). Under refFence (ctx holds the fence since before the symlink was made)
// the fence is not taken again.
func (l *Library) refWriteTx(ctx context.Context, fn func(*sql.Tx) error) error {
	return reffence.Write(ctx, func() error {
		return l.st.WriteTx(ctx, func(tx *sql.Tx) error {
			if err := reffence.CheckTx(ctx, tx, l.now()); err != nil {
				return fenceRefused(err)
			}
			return fn(tx)
		})
	})
}

// refFence takes the torrent delete fence for a write that changes a link on disk before its row records it:
// an adopt, a refresh, a relink, an import. It is held from before the filesystem change through
// the commit (refWriteTx under the returned ctx), so a delete's last usage check never runs between the two: the
// delete waits for the write, or the write for the delete. waited says it waited for a delete of this process: the
// caller checks again that its target is still there before it links. While another process deletes a torrent it
// is refused as ErrLinkChanged, before anything changed.
func (l *Library) refFence(ctx context.Context) (context.Context, bool, func(), error) {
	ctx, waited, release, err := reffence.Begin(ctx, l.st, l.now())
	if err != nil {
		return ctx, waited, func() {}, fenceRefused(err)
	}
	return ctx, waited, release, nil
}

// fenceRefused words a fence refusal (another process deletes, or the fence ran out) as ErrLinkChanged.
func fenceRefused(err error) error {
	if errors.Is(err, reffence.ErrDeleting) || errors.Is(err, reffence.ErrExpired) {
		return fmt.Errorf("library: %w (%w)", err, ErrLinkChanged)
	}
	return err
}

// targetGone reports whether target, a link target on the mount, is no longer a file there (after a wait for a
// torrent delete: refFence's waited). A mount that does not answer is ErrMountDown.
func (l *Library) targetGone(ctx context.Context, target string) (bool, error) {
	read, ok := l.mount.FromLink(target)
	if !ok {
		return false, nil
	}
	st, err := l.mount.Stat(ctx, read)
	if errors.Is(err, ErrMountDown) {
		return false, err
	}
	return err != nil || !st.Mode().IsRegular(), nil
}
