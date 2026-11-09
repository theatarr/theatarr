package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// A lease (library_lease, 00070) makes a library operation exclusive across processes: the service and a CLI run
// share the database, so an in-process mutex is not enough. The holder renews it while it works (holdLease); one
// left by a crash is taken over once until has passed.

// ErrBusy: another run holds the lease (the same operation, in this process or another).
var ErrBusy = errors.New("library: another run holds the lease")

// leaseFor is how long a lease lasts unless renewed.
const leaseFor = 10 * time.Minute

var leaseSeq atomic.Int64

type lease struct {
	name, owner string
	renewed     atomic.Int64 // unix millis of the last renewal
}

type leaseKey struct{}

func withLease(ctx context.Context, ls *lease) context.Context {
	return context.WithValue(ctx, leaseKey{}, ls)
}

// takeLease takes the named lease, or ErrBusy while another live holder has it.
func (l *Library) takeLease(ctx context.Context, name string) (*lease, error) {
	now := l.now()
	ls := &lease{name: name, owner: fmt.Sprintf("%d/%d", now.UnixNano(), leaseSeq.Add(1))}
	err := l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO library_lease (name, owner, until) VALUES (?, ?, ?)
			ON CONFLICT (name) DO UPDATE SET owner = excluded.owner, until = excluded.until
			WHERE library_lease.until <= ?`, name, ls.owner, store.Millis(now.Add(leaseFor)), store.Millis(now))
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrBusy
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	ls.renewed.Store(store.Millis(now))
	return ls, nil
}

// holdLease renews the context's lease when a third of it has passed, and fails with ErrBusy once it was lost
// (taken over after it expired). Without a lease in ctx it does nothing.
func (l *Library) holdLease(ctx context.Context) error {
	return l.renewLease(ctx, false)
}

// fenceLease renews the context's lease now, whenever it was last renewed: a holder that stalled past its lease
// (another run took it over) learns it before its next write (ErrBusy). Without a lease in ctx it does nothing.
func (l *Library) fenceLease(ctx context.Context) error {
	return l.renewLease(ctx, true)
}

func (l *Library) renewLease(ctx context.Context, always bool) error {
	ls, _ := ctx.Value(leaseKey{}).(*lease)
	if ls == nil {
		return nil
	}
	now := l.now()
	if !always && store.Millis(now)-ls.renewed.Load() < (leaseFor/3).Milliseconds() {
		return nil
	}
	err := l.st.WriteTx(ctx, func(tx *sql.Tx) error { return l.leaseHeldTx(ctx, tx) })
	if err == nil {
		ls.renewed.Store(store.Millis(now))
	}
	return err
}

// leaseHeldTx renews the context's lease inside tx, or fails with ErrBusy when another run took it over: a commit
// that must not land after the lease was lost checks it in its own transaction. Without a lease it does nothing.
func (l *Library) leaseHeldTx(ctx context.Context, tx *sql.Tx) error {
	ls, _ := ctx.Value(leaseKey{}).(*lease)
	if ls == nil {
		return nil
	}
	res, err := tx.ExecContext(ctx, `UPDATE library_lease SET until = ? WHERE name = ? AND owner = ?`,
		store.Millis(l.now().Add(leaseFor)), ls.name, ls.owner)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("%w: %s was taken over", ErrBusy, ls.name)
	}
	return nil
}

// dropLease releases the lease, even when ctx is cancelled.
func (l *Library) dropLease(ctx context.Context, ls *lease) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM library_lease WHERE name = ? AND owner = ?`, ls.name, ls.owner)
		return err
	}); err != nil {
		l.log.Warn("library: lease not released (it expires)", "lease", ls.name, "err", err)
	}
}
