package autobuild

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// queuedRetry is how long a title stays "queued" without a job before discovery tries it again (its job was lost).
const queuedRetry = time.Hour

// Run is the discovery and refresh loop of theatarr serve, while auto_build is on and the sources are configured.
// Each pass, when no anime_build job is queued or running (builds run one at a time, so the loop feeds them one at a
// time): the next anime series without a franchise that was not tried recently gets a discovery job; else, at most
// one per refresh_interval / franchises (so rebuilds spread over the interval), the stalest franchise built more than
// refresh_interval ago and not waiting for a held refresh's review gets a rebuild.
func (s *Service) Run(ctx context.Context) {
	if !s.Ready() {
		s.log.Info("anime: automatic builds off", "reason", s.cfg.Off)
		return
	}
	if !s.set.AutoBuild {
		s.log.Info("anime: automatic builds off (metadata.anime.auto_build)")
		return
	}
	s.log.Info("anime: automatic builds on", "refresh_interval", s.set.RefreshInterval.String())
	t := time.NewTimer(s.cfg.FirstDelay)
	defer t.Stop()
	var nextRefresh time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-wake:
		}
		ReleaseIdle()
		if err := s.Pass(ctx, &nextRefresh); err != nil && ctx.Err() == nil {
			s.log.Warn("anime: automatic builds", "err", err)
		}
		t.Reset(s.cfg.Poll)
	}
}

// Pass runs one pass of the loop (Run); nextRefresh paces the rebuilds. It reports nothing when it queued nothing.
func (s *Service) Pass(ctx context.Context, nextRefresh *time.Time) error {
	busy, err := s.busy(ctx)
	if err != nil || busy {
		return err
	}
	now := s.cfg.Now()
	titleID, tvdb, err := s.nextUncovered(ctx, now)
	if err != nil {
		return err
	}
	if titleID > 0 {
		_, err := s.enqueueDiscover(ctx, titleID, tvdb)
		return err
	}
	if now.Before(*nextRefresh) {
		return nil
	}
	slug, total, err := s.nextStale(ctx, now)
	if err != nil || slug == "" {
		return err
	}
	if _, _, err := s.cfg.Queue.Enqueue(ctx, JobKind, Payload{Slug: slug}, jobOptions(SlugKey(slug))); err != nil {
		return err
	}
	*nextRefresh = now.Add(s.set.RefreshInterval / time.Duration(max(total, 1)))
	return nil
}

// busy reports an anime_build job queued (ready) or running.
func (s *Service) busy(ctx context.Context) (bool, error) {
	var n int
	err := s.cfg.Store.Reader().QueryRowContext(ctx, `SELECT count(*) FROM job WHERE kind = ?
		AND state IN ('queued', 'running')`, JobKind).Scan(&n)
	return n > 0, err
}

// uncoveredWhere selects anime series with a TVDB id that no franchise maps into.
const uncoveredWhere = `t.kind = 'series' AND t.anime = 1 AND t.tvdb_id IS NOT NULL AND t.tvdb_id > 0
	AND NOT EXISTS (SELECT 1 FROM anime_mapping m WHERE m.tvdb_series = t.tvdb_id)`

// nextUncovered is the next anime series to discover: never tried, or tried longer ago than the refresh interval
// (an hour for one left queued without a job).
func (s *Service) nextUncovered(ctx context.Context, now time.Time) (int64, int, error) {
	var id int64
	var tvdb int
	err := s.cfg.Store.Reader().QueryRowContext(ctx, `SELECT t.id, t.tvdb_id FROM title t
		LEFT JOIN anime_discovery d ON d.title_id = t.id
		WHERE `+uncoveredWhere+` AND (d.title_id IS NULL
			OR (d.outcome = 'queued' AND d.tried_at < ?) OR (d.outcome <> 'queued' AND d.tried_at < ?))
		ORDER BY d.tried_at IS NOT NULL, d.tried_at, t.id LIMIT 1`,
		store.Millis(now.Add(-queuedRetry)), store.Millis(now.Add(-s.set.RefreshInterval))).Scan(&id, &tvdb)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	return id, tvdb, err
}

// nextStale is the stalest franchise due for a rebuild (none with a held refresh), and how many franchises there are.
func (s *Service) nextStale(ctx context.Context, now time.Time) (string, int, error) {
	db := s.cfg.Store.Reader()
	var total int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM anime_franchise WHERE built_at IS NOT NULL`).Scan(&total); err != nil {
		return "", 0, err
	}
	var slug string
	err := db.QueryRowContext(ctx, `SELECT slug FROM anime_franchise WHERE built_at IS NOT NULL AND built_at < ?
		AND pending_result IS NULL ORDER BY built_at, slug LIMIT 1`, store.Millis(now.Add(-s.set.RefreshInterval))).Scan(&slug)
	if errors.Is(err, sql.ErrNoRows) {
		return "", total, nil
	}
	return slug, total, err
}
