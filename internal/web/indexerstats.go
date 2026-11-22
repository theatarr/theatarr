package web

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/theatarr/theatarr/internal/activity"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// IndexerStats is GET /indexers/stats: each indexer's requests over the last Days UTC days (today included), settings
// tests left out, and its downloads from History in the same range (docs/design/indexers.md "Statistics").
type IndexerStats struct {
	Days int `json:"days"`
	// MeasuredSince is the first UTC day (YYYY-MM-DD) with a measured request in the kept counts; absent when none.
	MeasuredSince string             `json:"measured_since,omitempty"`
	Indexers      []IndexerStatsItem `json:"indexers"`
}

// IndexerStatsItem is one configured indexer's statistics. Requests are HTTP requests (each redirect hop is one).
// Failed (at most Measured) and AverageMS are over the measured requests only and absent when none was measured (rows from before the
// measurement have a count only).
type IndexerStatsItem struct {
	Key       string `json:"key"`
	Requests  int    `json:"requests"`
	Measured  int    `json:"measured"`
	Failed    *int   `json:"failed,omitempty"`
	AverageMS *int64 `json:"average_ms,omitempty"`
	// Grabbed, Imported and FailedDownloads count distinct downloads with that History event in the range.
	Grabbed         int `json:"grabbed"`
	Imported        int `json:"imported"`
	FailedDownloads int `json:"failed_downloads"`
}

func (w *Web) apiIndexerStats(rw http.ResponseWriter, r *http.Request) {
	days := 7
	switch r.URL.Query().Get("days") {
	case "", "7":
	case "30":
		days = 30
	case "90":
		days = 90
	default:
		badRequest(rw, "days must be 7, 30 or 90")
		return
	}
	s, err := w.indexerStats(r.Context(), days)
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, s)
}

func (w *Web) indexerStats(ctx context.Context, days int) (IndexerStats, error) {
	out := IndexerStats{Days: days, Indexers: []IndexerStatsItem{}}
	u := w.now().UTC()
	today := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	from := store.Millis(today.AddDate(0, 0, -(days - 1)))
	type counts struct {
		requests, measured, failures int
		took                         int64
		grabbed, imported, failed    int
	}
	by := map[string]*counts{}
	get := func(k string) *counts {
		if by[k] == nil {
			by[k] = &counts{}
		}
		return by[k]
	}
	db := w.st.Reader()
	if err := scanAll(ctx, db, `SELECT indexer, SUM(count), SUM(measured), SUM(failures), SUM(took_ms)
		FROM gateway_indexer_request WHERE day_start_at >= ? AND purpose <> 'test' GROUP BY indexer`, []any{from},
		func(rows *sql.Rows) error {
			var k string
			var c counts
			if err := rows.Scan(&k, &c.requests, &c.measured, &c.failures, &c.took); err != nil {
				return err
			}
			n := get(k)
			n.requests, n.measured, n.failures, n.took = c.requests, c.measured, c.failures, c.took
			return nil
		}); err != nil {
		return out, err
	}
	if err := scanAll(ctx, db, `SELECT indexer, event, COUNT(DISTINCT download_id) FROM history
		WHERE at >= ? AND event IN ('grabbed', 'imported', 'failed') AND download_id IS NOT NULL AND indexer <> ?
		AND NOT (event = 'failed' AND EXISTS (SELECT 1 FROM download d WHERE d.id = history.download_id
			AND `+activity.MissedSQL("d")+`))
		GROUP BY indexer, event`, []any{from, policy.ManualIndexer},
		func(rows *sql.Rows) error {
			var k, ev string
			var n int
			if err := rows.Scan(&k, &ev, &n); err != nil {
				return err
			}
			c := get(k)
			switch ev {
			case "grabbed":
				c.grabbed = n
			case "imported":
				c.imported = n
			case "failed":
				c.failed = n
			}
			return nil
		}); err != nil {
		return out, err
	}
	var since sql.NullInt64
	if err := db.QueryRowContext(ctx,
		`SELECT MIN(day_start_at) FROM gateway_indexer_request WHERE measured > 0`).Scan(&since); err != nil {
		return out, err
	}
	if since.Valid {
		out.MeasuredSince = store.FromMillis(since.Int64).UTC().Format(time.DateOnly)
	}
	if w.pol == nil {
		return out, nil
	}
	for _, k := range sortedKeys(w.pol.Indexers) {
		it := IndexerStatsItem{Key: k}
		if c := by[k]; c != nil {
			it.Requests, it.Measured = c.requests, c.measured
			it.Grabbed, it.Imported, it.FailedDownloads = c.grabbed, c.imported, c.failed
			if c.measured > 0 {
				// A failure can be recorded with no measured request on the same key (a Cardigann error answer
				// fetched through the solver): never more failed than measured.
				f, avg := min(c.failures, c.measured), c.took/int64(c.measured)
				it.Failed, it.AverageMS = &f, &avg
			}
		}
		out.Indexers = append(out.Indexers, it)
	}
	return out, nil
}
