package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Link mutations (rename, relink, the dead mark) are compare-and-swaps on the link: each one
// checks, right before it replaces or removes anything, that the symlink still points at the target it read and
// that the link's row still has that path and target, and its row update requires the same. A mutation that loses
// the race leaves the link to the one that won: it removes only what it made itself. Unlink is one too (by id, path,
// state and the target it moved aside). Every link mutation also holds its paths' link leases
// (linklock.go) through its commit, so these checks are the last guard, not the only one.

// ErrLinkChanged: the link changed under a mutation (a concurrent rename, relink or refresh), which left it alone.
var ErrLinkChanged = errors.New("library: link changed concurrently")

// LinkChanged is ErrLinkChanged with the link. Refused() makes it a refusal for the web API (409).
type LinkChanged struct {
	ID           int64
	Path, Target string
	Detail       string
}

func (e *LinkChanged) Error() string {
	return fmt.Sprintf("library: link %d (%s -> %s) changed concurrently: %s; it was left alone", e.ID, e.Path,
		e.Target, e.Detail)
}

// Is matches ErrLinkChanged.
func (e *LinkChanged) Is(err error) bool { return err == ErrLinkChanged }

// Refused reports that nothing was changed for this link.
func (e *LinkChanged) Refused() bool { return true }

func linkChanged(id int64, path, target, detail string) error {
	return &LinkChanged{ID: id, Path: path, Target: target, Detail: detail}
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// rowIs checks that link id is still active at path with target.
func rowIs(ctx context.Context, q rowQuerier, id int64, path, target string) error {
	return rowIsIn(ctx, q, id, path, target, "active")
}

// rowIsIn checks that link id is still at path with target, in state.
func rowIsIn(ctx context.Context, q rowQuerier, id int64, path, target, state string) error {
	var p, t, st string
	err := q.QueryRowContext(ctx, `SELECT path, target, state FROM link WHERE id = ?`, id).Scan(&p, &t, &st)
	if errors.Is(err, sql.ErrNoRows) {
		return linkChanged(id, path, target, "its row is gone")
	}
	if err != nil {
		return err
	}
	if p != path || t != target || st != state {
		return linkChanged(id, path, target, fmt.Sprintf("its row is now %s -> %s (%s)", p, t, st))
	}
	return nil
}
