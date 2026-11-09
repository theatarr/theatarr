package router

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// Blocklist sources for provider failures and downloads that never reached the library.
const (
	BlocklistProvider     = "provider"
	BlocklistReadyTimeout = "ready_timeout"
)

// mountOutages records when the poller found the debrid mount down, so a ready download's deadline counts only time
// the mount was healthy: after a long outage, downloads that became ready during it are not failed the
// moment it ends. The record is in memory; a restart forgets the outages before it.
type mountOutages struct {
	downSince time.Time // when the current outage was first seen; zero while the mount is up
	past      []outage
}

type outage struct{ from, to time.Time }

// keepOutages is how long an ended outage is remembered (past the longest ready_timeout, 7d).
const keepOutages = 30 * 24 * time.Hour

// note records one mount check at now.
func (m *mountOutages) note(healthy bool, now time.Time) {
	switch {
	case !healthy && m.downSince.IsZero():
		m.downSince = now
	case healthy && !m.downSince.IsZero():
		m.past = append(m.past, outage{m.downSince, now})
		m.downSince = time.Time{}
	}
	m.past = slices.DeleteFunc(m.past, func(o outage) bool { return o.to.Before(now.Add(-keepOutages)) })
}

// downBetween is how long the mount was down between from and now.
func (m *mountOutages) downBetween(from, now time.Time) time.Duration {
	var d time.Duration
	add := func(a, b time.Time) {
		a, b = later(a, from), earlier(b, now)
		if b.After(a) {
			d += b.Sub(a)
		}
	}
	for _, o := range m.past {
		add(o.from, o.to)
	}
	if !m.downSince.IsZero() {
		add(m.downSince, now)
	}
	return d
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// expireReady ends downloads still waiting to be imported past their deadline: ready_timeout of time the mount was
// healthy since they became ready. The library's live claims are checked in the same transaction: an import already
// holding the download gets to finish. A held import never expires: one held for review (rejected), for its episode
// title, or planned as a dry run (the gate refused the version, or every episode waits for its anime mapping review),
// which a later pass imports once that changes. No provider is touched. Called under r.mu.
func (r *Router) expireReady(ctx context.Context, now time.Time) (int, error) {
	mount := &r.shared.mount
	if healthy := r.mountCheck(); healthy != nil {
		// The mount check lists the release root (Library.MountListed): only while a download is ready, or an outage
		// is open (to see it end).
		var ready int
		if err := r.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM download WHERE state = 'ready'`).
			Scan(&ready); err != nil {
			return 0, err
		}
		if ready == 0 && mount.downSince.IsZero() {
			return 0, nil
		}
		err := healthy(ctx)
		mount.note(err == nil, now)
		if err != nil {
			r.log.Warn("ready deadline paused: the mount is down", "err", err, "since", mount.downSince.UTC())
			return 0, nil
		}
	}
	n := 0
	at := store.Millis(now)
	why := "ready timeout: not imported after " + r.set.ReadyTimeout.String()
	err := r.st.WriteTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, COALESCE(provider, ''), COALESCE(ready_at, added_at, created_at)
			FROM download WHERE state = 'ready' AND COALESCE(ready_at, added_at, created_at) <= ?
			AND NOT EXISTS (SELECT 1 FROM library_import li WHERE li.download_id = download.id AND li.state IN ('rejected', 'dry_run'))
			AND NOT EXISTS (SELECT 1 FROM library_import li WHERE li.download_id = download.id AND li.state = 'waiting_mount' AND li.last_error LIKE 'episode title hold: %')
			AND NOT EXISTS (SELECT 1 FROM library_claim c WHERE c.download_id = download.id AND c.until > ?)
			ORDER BY id`, store.Millis(now.Add(-r.set.ReadyTimeout)), at)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id, since int64
			var key string
			if err := rows.Scan(&id, &key, &since); err != nil {
				rows.Close()
				return err
			}
			if key != "" && r.provs[key] == nil {
				continue // left unpolled with its provider (TestPollLeavesUnavailableProvidersUnpolled)
			}
			if at := store.FromMillis(since); now.Sub(at)-mount.downBetween(at, now) >= r.set.ReadyTimeout {
				ids = append(ids, id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE download SET state = 'failed', error = ?, updated_at = ?
				WHERE id = ?`, why, at, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE library_import SET state = 'failed', last_error = ?,
				updated_at = ? WHERE download_id = ? AND state = 'waiting_mount'`, why, at, id); err != nil {
				return err
			}
			// An expired importer must also lose its claim, so it cannot record a later outcome over the failure.
			if _, err := tx.ExecContext(ctx, `DELETE FROM library_claim WHERE download_id = ?`, id); err != nil {
				return err
			}
			if err := blocklistDownload(ctx, tx, id, why, BlocklistReadyTimeout, at); err != nil {
				return err
			}
		}
		n = len(ids)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("router: ready deadline: %w", err)
	}
	return n, nil
}
