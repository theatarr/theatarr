// Package reffence keeps a torrent delete and the writers of local references to torrents apart.
//
// A delete of a torrent a person chose checks the torrent's usage once more right before the provider's delete; a
// reference written between that check and the provider's answer would point at a torrent that is going. The
// writers of the references the check reads hold this fence around their database write: a library link made or
// pointed at a new target (an import, a manual import, adopt, a relink), and the arr import's files (version_file).
// Routes and polls (downloads) are kept apart by the router's add lock, which the deletes hold too.
//
// In one process the fence is a read-write lock: a writer holds it shared around its whole write transaction, a
// delete exclusively from its last usage check until the provider answered, so a writer waits for that one delete
// and a delete waits for the writes in progress. Across processes (the CLI and the service share the database) the
// delete also holds a lease row (library_lease 'torrents:delete'), written before its last usage check; a writer's
// transaction refuses while another process holds it (CheckTx). SQLite serializes write transactions, so a write
// either committed before the lease (and the check sees it) or sees the lease. No SQLite transaction stays open
// across the provider call.
//
// A library link is a reference on disk before its row records it: a writer that makes or repoints a
// symlink and then records it holds the fence from before the filesystem change through its commit (Begin). Across
// processes it also writes a lease row of its own ('torrents:write:<owner>/<n>') before the change, refused while a
// delete's lease is held, and a delete's Hold refuses while another process's writer lease is held (it waits a
// little first). So a delete's last usage check never runs between a link made on disk and its row.
//
// No wait for the fence is unbounded: a writer's filesystem change can stall (a library root or the
// trash on a share that stops answering), so a delete waits at most HoldWait for this process's writers too, and
// gives up with ErrWriting; a writer waiting for a delete gives up when its ctx ends. Both poll the lock instead of
// queueing on it, so a delete that waits never blocks new writers the way a queued RWMutex writer would.
package reffence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// LeaseName is the delete's lease in library_lease.
const LeaseName = "torrents:delete"

// LeaseFor is how long a delete's lease holds: a delete bounds its provider call below it (Hold's caller), and a
// lease a crash left behind stops other processes' writers for at most this long.
const LeaseFor = 5 * time.Minute

// ErrDeleting: another process is deleting a torrent; the reference was not written (try again shortly).
var ErrDeleting = errors.New("a torrent delete of another Theatarr process is in progress; try again shortly")

// ErrWriting: a library link write (Begin) of this or another Theatarr process was still in progress after HoldWait;
// the delete was not sent (try again shortly).
var ErrWriting = errors.New("a library link write is still in progress; try again shortly")

// ErrExpired: a writer's fence lease (Begin) ran out before its commit, so a delete may have run meanwhile; the
// reference was not recorded.
var ErrExpired = errors.New("the torrent delete fence of this write expired before its commit")

// writerPrefix starts the name of a writer's lease (Begin).
const writerPrefix = "torrents:write:"

var (
	mu        sync.RWMutex
	owner     = fmt.Sprintf("pid %d at %d", os.Getpid(), time.Now().UnixNano())
	writerSeq atomic.Int64
)

// heldKey marks a context whose writer holds the fence (Begin).
type heldKey struct{}

// lockPoll is how often a wait for the in-process lock looks again.
var lockPoll = 5 * time.Millisecond

// rwaiting counts the writers waiting for a delete of this process (rlock): the next delete lets them in first, as a
// queued RWMutex would.
var rwaiting atomic.Int64

// rlock takes mu shared, waiting while a delete of this process holds it, until ctx ends. waited says it waited.
func rlock(ctx context.Context) (waited bool, err error) {
	for !mu.TryRLock() {
		if !waited {
			waited = true
			rwaiting.Add(1)
			defer rwaiting.Add(-1)
		}
		select {
		case <-ctx.Done():
			return waited, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
	return waited, nil
}

// lock takes mu exclusively, waiting for this process's writers (those in progress and those waiting for the
// previous delete) until ctx ends (its error) or deadline passes (ErrWriting).
func lock(ctx context.Context, deadline time.Time) error {
	for rwaiting.Load() > 0 || !mu.TryLock() {
		if !time.Now().Before(deadline) {
			return ErrWriting
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(lockPoll):
		}
	}
	return nil
}

// Write runs fn, a write of references (its whole write transaction), with the fence held shared: it waits while a
// delete of this process is between its last usage check and the provider's answer, until ctx ends. A ctx whose
// writer already holds the fence (Begin) runs fn at once: the fence is held from before its filesystem change. fn
// must not take the fence again.
func Write(ctx context.Context, fn func() error) error {
	if _, ok := ctx.Value(heldKey{}).(string); ok {
		return fn()
	}
	if _, err := rlock(ctx); err != nil {
		return err
	}
	defer mu.RUnlock()
	return fn()
}

// Begin takes the fence for a writer that changes the filesystem before it records the reference (a symlink made,
// then its row): shared in this process (waiting while a delete of this process is between its last usage check
// and the provider's answer), and a lease of its own that keeps other processes' deletes out, refused with
// ErrDeleting while another process deletes. The returned ctx carries it: WriteCtx and CheckTx under it know the
// fence is held. waited says it waited for a delete of this process: the writer checks again that what it links to
// is still there. release frees both, even when ctx is cancelled. A ctx that holds the fence already gets it back
// as it is, with a release that does nothing. A wait for a delete ends with ctx (its error).
func Begin(ctx context.Context, st *store.Store, now time.Time) (_ context.Context, waited bool, release func(),
	err error) {
	if _, ok := ctx.Value(heldKey{}).(string); ok {
		return ctx, false, func() {}, nil
	}
	if waited, err = rlock(ctx); err != nil {
		return ctx, waited, nil, err
	}
	name := fmt.Sprintf("%s%s/%d", writerPrefix, owner, writerSeq.Add(1))
	err = st.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := foreignDelete(ctx, tx, now); err != nil {
			return err
		}
		// Writer leases a crash left behind go once long expired.
		if _, err := tx.ExecContext(ctx, `DELETE FROM library_lease WHERE name LIKE ? AND until <= ?`,
			writerPrefix+"%", store.Millis(now.Add(-LeaseFor))); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO library_lease (name, owner, until) VALUES (?, ?, ?)`, name, owner,
			store.Millis(now.Add(LeaseFor)))
		return err
	})
	if err != nil {
		mu.RUnlock()
		return ctx, waited, nil, err
	}
	return context.WithValue(ctx, heldKey{}, name), waited, func() {
		defer mu.RUnlock()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = st.WriteTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `DELETE FROM library_lease WHERE name = ? AND owner = ?`, name, owner)
			return err
		})
	}, nil
}

// CheckTx, inside a writer's transaction (under Write), fails with ErrDeleting while another process holds the
// delete's lease. This process's own deletes are kept apart by Write. Under Begin it also fails with ErrExpired when
// the writer's own lease ran out (a delete of another process may have run since the filesystem change).
func CheckTx(ctx context.Context, tx *sql.Tx, now time.Time) error {
	if err := foreignDelete(ctx, tx, now); err != nil {
		return err
	}
	name, ok := ctx.Value(heldKey{}).(string)
	if !ok {
		return nil
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM library_lease WHERE name = ? AND owner = ? AND until > ?`,
		name, owner, store.Millis(now)).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrExpired
	}
	return nil
}

func foreignDelete(ctx context.Context, tx *sql.Tx, now time.Time) error {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM library_lease WHERE name = ? AND owner <> ? AND until > ?`,
		LeaseName, owner, store.Millis(now)).Scan(&n)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrDeleting
	}
	return nil
}

// HoldWait is how long Hold waits for other processes' link writes (Begin) to finish before it refuses
// (ErrWriting); holdPoll is how often it looks.
var (
	HoldWait = 15 * time.Second
	holdPoll = 250 * time.Millisecond
)

// Hold takes the fence for one delete: exclusively in this process (waiting for the writes in progress), then the
// lease, refused while another process holds it, and refused (ErrWriting) while a link write (Begin) of this
// process still holds the fence, or one of another process still holds its lease, after HoldWait. The caller checks the usage after Hold returns, then calls the
// provider (bounded below LeaseFor), then release. release frees both, even when ctx is cancelled.
func Hold(ctx context.Context, st *store.Store, now time.Time) (release func(), err error) {
	start := time.Now()
	for {
		if err := lock(ctx, start.Add(HoldWait)); err != nil {
			return nil, err
		}
		at := now.Add(time.Since(start))
		err = st.WriteTx(ctx, func(tx *sql.Tx) error {
			var w int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM library_lease WHERE name LIKE ? AND owner <> ?
				AND until > ?`, writerPrefix+"%", owner, store.Millis(at)).Scan(&w); err != nil {
				return err
			}
			if w > 0 {
				return ErrWriting
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO library_lease (name, owner, until) VALUES (?, ?, ?)
				ON CONFLICT (name) DO UPDATE SET owner = excluded.owner, until = excluded.until
				WHERE library_lease.until <= ? OR library_lease.owner = excluded.owner`,
				LeaseName, owner, store.Millis(at.Add(LeaseFor)), store.Millis(at))
			if err != nil {
				return err
			}
			if k, err := res.RowsAffected(); err != nil {
				return err
			} else if k == 0 {
				return ErrDeleting
			}
			return nil
		})
		if err == nil {
			break
		}
		mu.Unlock()
		if !errors.Is(err, ErrWriting) || time.Since(start) >= HoldWait {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(holdPoll):
		}
	}
	return func() {
		defer mu.Unlock()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = st.WriteTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `DELETE FROM library_lease WHERE name = ? AND owner = ?`, LeaseName, owner)
			return err
		})
	}, nil
}
