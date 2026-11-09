package router

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// AddLock is the lock every route and poll of every settings generation holds while it reads the account and adds
// (r.mu). The torrent manager sends a restore's add under it, so a restore never lands between a route's
// look at the account and its own add.
func (r *Router) AddLock() sync.Locker { return r.mu }

// RestoredSince says whether the torrent manager restored hash to provider pk, or was sending it, at or after since
// (a torrent_journal restore row that got as far as the add, from any process), and when. On AllDebrid such an entry
// may be the restore's magnet, which an upload of the same hash hands back: never provably our own add.
func RestoredSince(ctx context.Context, db *sql.DB, pk, hash string, since time.Time) (string, error) {
	var at int64
	var outcome string
	err := db.QueryRowContext(ctx, `SELECT created_at, outcome FROM torrent_journal WHERE action = 'restore'
		AND provider = ? AND lower(info_hash) = lower(?) AND outcome NOT IN ('present', 'skipped') AND created_at >= ?
		ORDER BY created_at DESC LIMIT 1`, pk, hash, store.Millis(since)).Scan(&at, &outcome)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("router: torrent restores: %w", err)
	}
	return fmt.Sprintf("the torrent manager restored the hash to %s at %s (%s)", pk,
		store.FromMillis(at).UTC().Format(time.RFC3339), outcome), nil
}
