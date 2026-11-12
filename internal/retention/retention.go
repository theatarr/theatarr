// Package retention bounds the tables that grow with every run, poll, delivery and failure. It runs as the
// low-priority db.retention job the nightly job enqueues, and deletes in small batches so the writer is never held
// for long; a backlog drains over a few nights.
//
// What it keeps, whatever its age:
//   - the latest successful and the latest failed provider_sync_run of each provider;
//   - the latest settings_quality_sync_run of each TRaSH source and outcome, and runs still running;
//   - provider_cache rows that have not expired, or expired less than the runs window ago (rows without an expiry);
//   - metadata detail responses and other sources used for offline reads and conditional refresh; expired TMDb and TVDB
//     search responses drain in batches;
//   - pending notify_delivery rows (only sent, failed and dropped ones go);
//   - every download that is not failed, and failed ones that a link, a live library claim, an import still in
//     progress (waiting for the mount or partial), a router blocklist entry, a router offer or a
//     queued or running job refers to (download_attempt, download_file, download_progress and a finished
//     library_import go with their download);
//   - acquire runs that are not finished, the latest run of each version and target (the wanted loop's "last
//     searched") and the latest that no retry_new chain started, and runs a queued or running job refers to;
//   - the applied link and replace library actions of a kept download or at a live link's path (the router's offer
//     sources read them: which download a replacement superseded, which arr file Theatarr replaced); adoption provenance
//     is on the link row itself (link.adopted_from);
//   - the current settings generation's audit rows;
//   - pending quality reviews, the newest 100 closed reviews per source, and reviews referenced by kept sync runs;
//   - new and seen error inbox rows (error_event), and resolved or ignored ones seen within the runs window.
//
// Not pruned, by design: router_offer (one row per version, title and release: deleting one re-offers that release
// to the other libraries at the next sweep), notify_event (the once-only memory of scanned events), arr_history (a mirror
// of the arrs' own history that decisions read) and imported downloads (offer sources and the journal's routed
// decisions read them; they grow with imports, not with polls).
package retention

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/store"
)

// JobKind is the retention job, enqueued by the nightly job at a low priority.
const JobKind = "db.retention"

// Priority is below every other job's (they use 0 or more): retention runs when nothing else waits.
const Priority = -10

// Config is how long each kind of row is kept.
type Config struct {
	// History covers failed downloads, acquire runs, router plans, library actions and settings audit.
	History time.Duration
	// Runs covers provider and quality sync runs, finished notification deliveries and expired provider cache rows.
	Runs time.Duration
	// MaxBatches bounds the batches (of batchSize rows) one run deletes per table; 0 means 100.
	MaxBatches int
}

// Defaults are 180 days of history (the journal keeps decisions in full as long) and 30 days of runs.
var Defaults = Config{History: 180 * 24 * time.Hour, Runs: 30 * 24 * time.Hour}

const batchSize = 500
const qualityReviewKeep = 100

// active is the job rows that still run or wait: what they refer to stays.
const active = `SELECT payload FROM job WHERE state IN ('queued', 'running')`

type rule struct {
	table string
	// where selects deletable rows; ?1 is the cutoff and ?2 now (unix millis). A run's children (search_run) go with
	// it, so a run whose children are kept stays too.
	where string
	runs  bool
	// more is added to where when the query when finds a row (neither is set for most tables).
	when, more string
}

// rules run in order.
var rules = []rule{
	{table: "provider_sync_run", runs: true, where: `started_at < ?1
		AND id NOT IN (SELECT MAX(id) FROM provider_sync_run GROUP BY provider, ok)`},
	// The quality sync reads the latest run per source and the latest one that read TRaSH (its commit and config
	// digest): the latest of each source and outcome stays, and so does a run still running.
	{table: "settings_quality_sync_run", runs: true, where: `started_at < ?1 AND outcome != 'running'
		AND id NOT IN (SELECT MAX(id) FROM settings_quality_sync_run GROUP BY source_key, outcome)`},
	// Candidates contain complete quality settings. Only closed reviews outside both the history window and
	// the per-source cap may go; settings generations and audit retain the actual applied changes.
	{table: "settings_quality_review", where: fmt.Sprintf(`created_at < ?1 AND state IN ('applied','superseded')
  AND id IN (SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY source_key ORDER BY id DESC) AS rank
   FROM settings_quality_review WHERE state IN ('applied','superseded')) WHERE rank > %d)
  AND NOT EXISTS (SELECT 1 FROM settings_quality_sync_run r WHERE r.review_id=settings_quality_review.id)`, qualityReviewKeep)},
	{table: "notify_delivery", runs: true, where: `created_at < ?1 AND state IN ('sent', 'failed', 'dropped')`},
	// Cache evidence and misses no lookup reads any more (expired; a route reads live rows only).
	// The error inbox (docs/design/errors.md): resolved and ignored fingerprints not seen within the runs window. Open
	// ones stay whatever their age; the inbox writer caps the table at 5000 rows.
	{table: "error_event", runs: true, where: `status IN ('resolved', 'ignored') AND last_seen < ?1
		AND COALESCE(resolved_at, updated_at) < ?1`},
	{table: "provider_cache", runs: true, where: `expires_at IS NOT NULL AND expires_at < ?1`},
	// Search caches have arbitrary query/page keys; detail caches keep stale bodies and validators.
	{table: "metadata_cache", runs: true, where: `expires_at <= ?2 AND ((source = 'tmdb' AND key GLOB '/search/*')
  OR (source = 'tvdb' AND (key GLOB 'GET */search[?]*' OR key GLOB 'GET */search/*')))`},
	{table: "download", where: `updated_at < ?1 AND state = 'failed'
		AND NOT EXISTS (SELECT 1 FROM link l WHERE l.download_id = download.id)
		AND NOT EXISTS (SELECT 1 FROM library_claim c WHERE c.download_id = download.id AND c.until > ?2)
		AND NOT EXISTS (SELECT 1 FROM library_import i WHERE i.download_id = download.id
			AND i.state IN ('waiting_mount', 'partial'))
		AND NOT EXISTS (SELECT 1 FROM router_blocklist b WHERE b.download_id = download.id)
		AND NOT EXISTS (SELECT 1 FROM router_offer o WHERE o.source_kind = 'download' AND o.source_id = download.id)
		AND id NOT IN (SELECT json_extract(payload, '$.download_id') FROM (` + active + `)
			WHERE json_extract(payload, '$.download_id') IS NOT NULL)`},
	{table: "acquire_run", where: `created_at < ?1 AND state IN ('routed', 'nothing', 'skipped', 'failed')
		AND id NOT IN (SELECT MAX(id) FROM acquire_run GROUP BY version_id, target)
		AND NOT EXISTS (SELECT 1 FROM acquire_run c WHERE c.search_run = acquire_run.id AND (c.created_at >= ?1
			OR c.state NOT IN ('routed', 'nothing', 'skipped', 'failed')
			OR c.id IN (SELECT MAX(id) FROM acquire_run GROUP BY version_id, target)))
		AND NOT EXISTS (SELECT 1 FROM job j WHERE j.state IN ('queued', 'running') AND (j.id = acquire_run.route_job
			OR json_extract(j.payload, '$.source') = 'acquire:' || acquire_run.id))`,
		// The searches of router.wanted.retry_new's chains (research retry_new) are left out by the readers that
		// decide for an ordinary search (internal/acquire "ordinaryRuns"), so a target's latest ordinary run is
		// kept too, with the parent of a shared search that holds it: a newer chain run must not let it go. An
		// install without such runs prunes by the condition above alone.
		when: `SELECT 1 FROM acquire_run WHERE research = 'retry_new' LIMIT 1`,
		more: `id NOT IN (` + latestOrdinaryRun + `) AND NOT EXISTS (SELECT 1 FROM acquire_run c
			WHERE c.search_run = acquire_run.id AND c.id IN (` + latestOrdinaryRun + `))`},
	{table: "library_action", where: `at < ?1 AND NOT (mode = 'applied' AND kind IN ('link', 'replace') AND (
		EXISTS (SELECT 1 FROM download d WHERE d.id = library_action.download_id)
		OR EXISTS (SELECT 1 FROM link l WHERE l.path = library_action.path AND l.state IN ('active', 'dead'))))`},
	{table: "settings_audit", where: `created_at < ?1 AND generation < (SELECT MAX(generation) FROM settings_generation)`},
	{table: "router_plan", where: `created_at < ?1`},
}

// latestOrdinaryRun selects the latest run of each version and target that no retry_new chain started.
const latestOrdinaryRun = `SELECT MAX(id) FROM acquire_run WHERE COALESCE(research, '') <> 'retry_new'
	GROUP BY version_id, target`

// condition is the rule's where for the store as it is now.
func (r rule) condition(ctx context.Context, st *store.Store) (string, error) {
	if r.when == "" {
		return r.where, nil
	}
	var found int
	err := st.Reader().QueryRowContext(ctx, r.when).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return r.where, nil
	}
	if err != nil {
		return "", err
	}
	return r.where + ` AND ` + r.more, nil
}

// Summary is the rows deleted per table.
type Summary map[string]int64

// Prune deletes what the config no longer keeps, as of now. A zero window keeps that kind of row for good.
func Prune(ctx context.Context, st *store.Store, now time.Time, cfg Config) (Summary, error) {
	batches := cfg.MaxBatches
	if batches <= 0 {
		batches = 100
	}
	out := Summary{}
	if cfg.History > 0 {
		n, err := pruneSeaDex(ctx, st, batches*batchSize)
		if n > 0 {
			out["seadex-standing"] = n
		}
		if err != nil {
			return out, fmt.Errorf("retention: seadex standing: %w", err)
		}
	}
	for _, r := range rules {
		keep := cfg.History
		if r.runs {
			keep = cfg.Runs
		}
		if keep <= 0 {
			continue
		}
		args := []any{store.Millis(now.Add(-keep))}
		if strings.Contains(r.where, "?2") {
			args = append(args, store.Millis(now))
		}
		where, err := r.condition(ctx, st)
		if err != nil {
			return out, fmt.Errorf("retention: %s: %w", r.table, err)
		}
		q := fmt.Sprintf(`DELETE FROM %s WHERE rowid IN (SELECT rowid FROM %s WHERE %s LIMIT %d)`, r.table, r.table, where, batchSize)
		for range batches {
			res, err := st.Writer().ExecContext(ctx, q, args...)
			if err != nil {
				return out, fmt.Errorf("retention: %s: %w", r.table, err)
			}
			n, _ := res.RowsAffected()
			out[r.table] += n
			if n < batchSize {
				break
			}
		}
	}
	return out, nil
}

// seadexDeleteBatch bounds one SeaDex standing delete transaction; each deletes by primary key.
const seadexDeleteBatch = 1000

// pruneSeaDex deletes stored SeaDex standing no catalog title maps to and no held file references, at most limit
// rows. The candidates are found by reading the catalog, the held references and the snapshots through the reader,
// without the write lock; each bounded delete transaction then re-reads every candidate's current body and checks
// it again against the catalog and held references read inside it, and deletes by key only a row whose body is
// still the one it checked (a snapshot rewritten meanwhile, even within the same fetched_at millisecond, is judged
// by its new body). A row that does not decode is deleted only when its key maps to nothing.
func pruneSeaDex(ctx context.Context, st *store.Store, limit int) (int64, error) {
	db := st.Reader()
	type row struct {
		key   string
		s     indexer.SeaDexSnapshot
		valid bool
	}
	var rows []row
	var snapshots []indexer.SeaDexSnapshot
	rs, err := db.QueryContext(ctx, `SELECT key, body FROM metadata_cache WHERE source = 'seadex-standing'`)
	if err != nil {
		return 0, err
	}
	for rs.Next() {
		var r row
		var body []byte
		if err := rs.Scan(&r.key, &body); err != nil {
			rs.Close()
			return 0, err
		}
		r.valid = json.Unmarshal(body, &r.s) == nil
		if !r.valid {
			r.s = indexer.SeaDexSnapshot{}
		}
		if id, err := strconv.Atoi(r.key); err == nil {
			r.s.AniList = id
		}
		if r.valid {
			snapshots = append(snapshots, r.s)
		}
		rows = append(rows, r)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	catalog, err := indexer.LoadSeaDexCatalog(ctx, db, snapshots)
	if err != nil {
		return 0, err
	}
	held, err := indexer.LoadSeaDexHeldRefs(ctx, db)
	if err != nil {
		return 0, err
	}
	var doomed []row
	for _, r := range rows {
		if len(doomed) >= limit {
			break
		}
		if catalog.Mapped(r.s) || r.valid && held.References(r.s) {
			continue
		}
		doomed = append(doomed, r)
	}
	var n int64
	for len(doomed) > 0 {
		batch := doomed[:min(seadexDeleteBatch, len(doomed))]
		doomed = doomed[len(batch):]
		var deleted int64
		err := st.WriteTx(ctx, func(tx *sql.Tx) error {
			deleted = 0
			// Revalidated under the write lock: a title, mapping or held file committed since the scan keeps its
			// snapshot. The catalog and held references are read in the transaction, so nothing can
			// commit between this check and the deletes.
			catalog, err := indexer.LoadSeaDexCatalog(ctx, tx, snapshots)
			if err != nil {
				return err
			}
			held, err := indexer.LoadSeaDexHeldRefs(ctx, tx)
			if err != nil {
				return err
			}
			for _, r := range batch {
				var body []byte
				err := tx.QueryRowContext(ctx, `SELECT body FROM metadata_cache WHERE source = 'seadex-standing' AND key = ?`, r.key).Scan(&body)
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				if err != nil {
					return err
				}
				var cur indexer.SeaDexSnapshot
				valid := json.Unmarshal(body, &cur) == nil
				if !valid {
					cur = indexer.SeaDexSnapshot{}
				}
				if id, err := strconv.Atoi(r.key); err == nil {
					cur.AniList = id
				}
				if catalog.Mapped(r.s) || r.valid && held.References(r.s) || catalog.Mapped(cur) || valid && held.References(cur) {
					continue
				}
				res, err := tx.ExecContext(ctx, `DELETE FROM metadata_cache WHERE source = 'seadex-standing' AND key = ?
					AND CAST(body AS BLOB) = CAST(? AS BLOB)`, r.key, body)
				if err != nil {
					return err
				}
				k, _ := res.RowsAffected()
				deleted += k
			}
			return nil
		})
		n += deleted
		if err != nil {
			return n, err
		}
	}
	return n, nil
}
