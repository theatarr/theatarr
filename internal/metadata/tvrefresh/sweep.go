package tvrefresh

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// Refresh cadence, after Sonarr's ShouldRefreshSeries (src/NzbDrone.Core/Tv/ShouldRefreshSeries.cs @ cab419a) and its
// 12-hour RefreshSeries task.
const (
	maxAge       = 30 * 24 * time.Hour // refreshed longer ago: due whatever the status
	tbaAge       = 6 * time.Hour       // an aired episode still TBA: due after this
	minAge       = 12 * time.Hour      // otherwise never sooner (Sonarr's task interval)
	recentAired  = 30 * 24 * time.Hour // an ended series whose last episode aired within this is still due
	retryFailed  = 6 * time.Hour       // a failed try waits this long before the sweep tries the series again
	maxFailInRow = 3                   // a sweep stops after this many failures in a row (the source is down)
)

// SweepSummary is what a sweep did.
type SweepSummary struct {
	Due       int `json:"due"`
	Refreshed int `json:"refreshed"`
	NotFound  int `json:"not_found"`
	Failed    int `json:"failed"`
}

type candidate struct {
	id                   int64
	status               string
	refreshed, attempted sql.NullInt64
	lastAir              sql.NullInt64
	tba                  bool
}

// due reports whether a series is due a refresh at now (Sonarr's ShouldRefreshSeries).
func (c candidate) due(now time.Time) bool {
	ms := func(d time.Duration) int64 { return store.Millis(now.Add(-d)) }
	if c.attempted.Valid && (!c.refreshed.Valid || c.attempted.Int64 > c.refreshed.Int64) && c.attempted.Int64 > ms(retryFailed) {
		return false // failed recently
	}
	switch {
	case !c.refreshed.Valid:
		return true
	case c.refreshed.Int64 < ms(maxAge):
		return true
	case c.tba && c.refreshed.Int64 < ms(tbaAge):
		return true
	case c.refreshed.Int64 >= ms(minAge):
		return false
	case c.status != StatusEnded:
		return true
	}
	return c.lastAir.Valid && c.lastAir.Int64 > ms(recentAired)
}

// dueSeries lists the series due a refresh, never refreshed first, then the longest ago.
func (s *Service) dueSeries(ctx context.Context) ([]int64, error) {
	now := s.now()
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT t.id, COALESCE(m.status, ''), m.refreshed_at, m.attempted_at,
		(SELECT MAX(e.air_date_utc_at) FROM episode e WHERE e.title_id = t.id AND e.removed_at IS NULL),
		EXISTS (SELECT 1 FROM episode e WHERE e.title_id = t.id AND e.removed_at IS NULL AND e.season > 0
			AND e.air_date_utc_at < ? AND e.name IN ('', 'TBA'))
		FROM title t LEFT JOIN series_meta m ON m.title_id = t.id
		WHERE t.kind = 'series' AND (t.tvdb_id IS NOT NULL OR t.tmdb_id IS NOT NULL)
		AND EXISTS (SELECT 1 FROM version v WHERE v.title_id = t.id AND NOT EXISTS
			(SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id))`, store.Millis(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var due []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.status, &c.refreshed, &c.attempted, &c.lastAir, &c.tba); err != nil {
			return nil, err
		}
		if c.due(now) {
			due = append(due, c)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(due, func(i, j int) bool {
		a, b := due[i], due[j]
		if a.refreshed.Valid != b.refreshed.Valid {
			return !a.refreshed.Valid
		}
		if a.refreshed.Int64 != b.refreshed.Int64 {
			return a.refreshed.Int64 < b.refreshed.Int64
		}
		return a.id < b.id
	})
	ids := make([]int64, len(due))
	for i, c := range due {
		ids[i] = c.id
	}
	return ids, nil
}

// Sweep refreshes the due series, at most metadata.series.per_run. A series the sources lack is recorded and
// skipped; after a few failures in a row (the source is down or the budget spent) the sweep stops and fails, and the
// next one carries on.
func (s *Service) Sweep(ctx context.Context) (*SweepSummary, error) {
	if !s.Enabled() {
		return nil, ErrOff
	}
	ids, err := s.dueSeries(ctx)
	if err != nil {
		return nil, err
	}
	sum := &SweepSummary{Due: len(ids)}
	if len(ids) > s.set.PerRun {
		ids = ids[:s.set.PerRun]
	}
	inRow := 0
	var last error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		r, err := s.RefreshTitle(ctx, id, false)
		switch {
		case err != nil:
			sum.Failed++
			inRow++
			last = err
			s.log.Warn("series metadata: refresh failed", "title", id, "err", err)
			if inRow >= maxFailInRow {
				s.log.Warn("series metadata: sweep stopped", "refreshed", sum.Refreshed, "failed", sum.Failed)
				return sum, last
			}
			continue
		case r.NotFound:
			sum.NotFound++
		default:
			sum.Refreshed++
		}
		inRow = 0
	}
	if len(ids) > 0 {
		s.log.Info("series metadata sweep", "due", sum.Due, "refreshed", sum.Refreshed, "not_found", sum.NotFound,
			"failed", sum.Failed)
	}
	return sum, nil
}
