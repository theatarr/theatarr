package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

type activeProviderDownload struct {
	id                 int64
	key, torrent, hash string
	state              string
}

type providerContinuity struct {
	removed []string
	changed []string
	probed  map[string]map[int64]activeProviderDownload
	orphan  bool
}

const activeProviderRows = `SELECT id, provider, COALESCE(provider_torrent_id, ''), info_hash, state FROM download
	WHERE state IN ('adding', 'added', 'downloading', 'ready') AND provider IS NOT NULL ORDER BY provider, id`

func readActiveProviderDownloads(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) ([]activeProviderDownload, error) {
	rows, err := db.QueryContext(ctx, activeProviderRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []activeProviderDownload
	for rows.Next() {
		var d activeProviderDownload
		if err := rows.Scan(&d.id, &d.key, &d.torrent, &d.hash, &d.state); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *serveRuntime) CommitSettings(ctx context.Context, tx *sql.Tx) error {
	if r == nil {
		return nil
	}
	if r.Continuity == nil {
		return nil
	}
	c := r.Continuity
	rows, err := readActiveProviderDownloads(ctx, tx)
	if err != nil {
		return err
	}
	for key, probed := range c.probed {
		for _, d := range rows {
			if d.key == key && probed[d.id] != d {
				return settings.ErrDownloadsChanged
			}
		}
	}
	for _, key := range c.removed {
		count, adding := 0, 0
		for _, d := range rows {
			if d.key == key {
				count++
				if d.state == "adding" {
					adding++
				}
			}
		}
		if adding > 0 {
			return &policy.Errors{List: []policy.FieldError{{Path: "providers[" + key + "]",
				Msg: fmt.Sprintf("%d add(s) in progress for provider %s; retry shortly", adding, key)}}}
		}
		if count > 0 && !c.orphan {
			return &policy.Errors{List: []policy.FieldError{{Path: "providers[" + key + "]",
				Msg: fmt.Sprintf("removal would orphan %d active download(s); set orphan_active_downloads to confirm", count)}}}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE download SET state = 'failed', error = ?, updated_at = ?
			WHERE provider = ? AND state IN ('added', 'downloading', 'ready')`,
			"provider removed by settings", store.Millis(time.Now()), key); err != nil {
			return err
		}
	}
	for _, key := range c.changed {
		if _, err := tx.ExecContext(ctx, "DELETE FROM content_provider WHERE provider = ?", key); err != nil {
			return err
		}
	}
	if c.advancesEpoch() {
		if err := settings.IncrementProviderEpoch(ctx, tx); err != nil {
			return err
		}
	}
	return nil
}

// advancesEpoch reports whether this provider transition, computed against the active runtime, removes or changes
// an account. It is the one transition behind the prepared epoch, the commit's increment and the published runtime.
func (c *providerContinuity) advancesEpoch() bool {
	return c != nil && (len(c.removed) > 0 || len(c.changed) > 0)
}

// preparedProviderEpoch is the epoch a runtime prepared from snap must fence adds with. A startup generation already
// carries its epoch; a save/import candidate or a reloaded head advances the stored epoch when its transition changes
// an account.
func preparedProviderEpoch(snap *settings.Snapshot, c *providerContinuity) int64 {
	if snap.EpochPending() && c.advancesEpoch() {
		return snap.ProviderEpoch + 1
	}
	return snap.ProviderEpoch
}

// PreparedProviderEpoch lets the settings coordinator check the committed epoch before publication.
func (r *serveRuntime) PreparedProviderEpoch() int64 { return r.ProviderEpoch }

func validateProviderContinuity(ctx context.Context, st *store.Store, snap *settings.Snapshot, previous *serveRuntime,
	keys map[string]string, clients map[string]provider.Provider) (*providerContinuity, error) {
	c := &providerContinuity{orphan: settings.OrphanActiveDownloads(ctx), probed: map[string]map[int64]activeProviderDownload{}}
	if previous == nil {
		return c, nil
	}
	enabled := map[string]bool{}
	for _, record := range snap.Records.Providers {
		enabled[record.Key] = record.Enabled
	}
	for key, old := range previous.Policy.Providers {
		if _, wasEnabled := previous.ProviderKeys[key]; !wasEnabled {
			continue
		}
		current := snap.Policy.Providers[key]
		if current == nil || !enabled[key] {
			c.removed = append(c.removed, key)
		} else if previous.ProviderKeys[key] != keys[key] {
			c.changed = append(c.changed, key)
			c.probed[key] = map[int64]activeProviderDownload{}
		} else if old.Kind != current.Kind || old.URL != current.URL {
			// The same credential at a new address (a change the save confirmed) is the same account:
			// it is not probed, so the stored key never reaches the new address before the change commits. Its
			// downloads stay; the registry sync after publication reads the new address.
			c.changed = append(c.changed, key)
		}
	}
	sort.Strings(c.removed)
	sort.Strings(c.changed)
	if len(c.changed) == 0 {
		return c, nil
	}
	downloads, err := readActiveProviderDownloads(ctx, st.Reader())
	if err != nil {
		return nil, err
	}
	// All account probes share one deadline and four workers, including budget waits.
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for _, key := range c.changed {
		if c.probed[key] == nil {
			continue
		}
		var active []activeProviderDownload
		for _, d := range downloads {
			if d.key == key {
				active = append(active, d)
			}
		}
		if len(active) > 100 {
			return nil, continuityRefusal(key, "account change: probe limit is 100 active download(s)")
		}
		jobs := make(chan activeProviderDownload)
		var wg sync.WaitGroup
		var mu sync.Mutex
		missing, uncertain := 0, 0
		for range min(4, len(active)) {
			wg.Go(func() {
				for d := range jobs {
					var torrent *provider.Torrent
					var probeErr error
					if d.torrent != "" && clients[key] != nil {
						torrent, probeErr = clients[key].Torrent(ctx, d.torrent)
					}
					mu.Lock()
					switch {
					case d.torrent == "" || clients[key] == nil || ctx.Err() != nil:
						uncertain++
					case errors.Is(probeErr, provider.ErrNotFound):
						missing++
					case probeErr != nil || torrent == nil || indexer.NormalizeHash(torrent.Hash) == "":
						uncertain++
					case indexer.NormalizeHash(torrent.Hash) != indexer.NormalizeHash(d.hash):
						missing++
					default:
						c.probed[key][d.id] = d
					}
					mu.Unlock()
				}
			})
		}
		for _, d := range active {
			jobs <- d
		}
		close(jobs)
		wg.Wait()
		if uncertain > 0 {
			return nil, continuityRefusal(key, fmt.Sprintf("could not verify %d active download(s) on the new account; retry", uncertain))
		}
		if missing > 0 {
			return nil, continuityRefusal(key, fmt.Sprintf("account change would orphan %d active download(s): new account cannot see their torrents", missing))
		}
	}
	return c, nil
}

// continuityRefusal is a typed refusal, so the admin API reports it as a 4xx with its path rather than an opaque failure.
func continuityRefusal(key, msg string) error {
	return &policy.Errors{List: []policy.FieldError{{Path: "providers[" + key + "]", Msg: msg}}}
}
