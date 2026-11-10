package tvrefresh

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/metadata/tvdb"
	"github.com/theatarr/theatarr/internal/store"
)

// Result is what one series refresh did.
type Result struct {
	TitleID int64  `json:"title_id"`
	Source  string `json:"source,omitempty"`
	// NotFound: no source has the series; nothing was changed (the error is on series_meta).
	NotFound bool   `json:"not_found,omitempty"`
	Error    string `json:"error,omitempty"`
	Episodes int    `json:"episodes"`
	Added    int    `json:"added"`
	Updated  int    `json:"updated"`
	Moved    int    `json:"moved"`
	Deleted  int    `json:"deleted"`
	Removed  int    `json:"removed"`
	// States are the per-version episode states created for new episodes.
	States int `json:"states"`
}

// titleRow is what a refresh reads of the title.
type titleRow struct {
	id         int64
	name       string
	year       sql.NullInt64
	tvdb, tmdb sql.NullInt64
	imdb       sql.NullString
	anime      bool
	runtime    sql.NullInt64
	seriesType string
	addedAt    sql.NullInt64
}

// isAnime is Sonarr's series type anime (store.SeriesTypeSQL: the title's type, else its anime flag).
func (t titleRow) isAnime() bool { return t.seriesType == "anime" }

func (s *Service) loadTitle(ctx context.Context, id int64) (*titleRow, error) {
	var t titleRow
	err := s.st.Reader().QueryRowContext(ctx, `SELECT t.id, t.name, t.year, t.tvdb_id, t.tmdb_id, t.imdb_id, t.anime,
		t.runtime_minutes, `+store.SeriesTypeSQL("t")+`, m.added_at FROM title t LEFT JOIN series_meta m ON m.title_id = t.id
		WHERE t.id = ? AND t.kind = 'series'`, id).Scan(&t.id, &t.name, &t.year, &t.tvdb, &t.tmdb, &t.imdb, &t.anime,
		&t.runtime, &t.seriesType, &t.addedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoTitle
	}
	return &t, err
}

// fetch reads the series from TVDB, else (not anime) from TMDb. errNotFound when no source has it.
func (s *Service) fetch(ctx context.Context, t *titleRow, force bool) (*show, error) {
	var why []string
	if t.tvdb.Valid && t.tvdb.Int64 > 0 && s.tvdb != nil {
		id := int(t.tvdb.Int64)
		if force {
			if _, err := s.tvdb.Invalidate(ctx, id); err != nil {
				return nil, err
			}
		}
		ser, eps, err := s.tvdb.SeriesEpisodes(ctx, id, false)
		switch {
		case err == nil:
			return fromTVDB(ser, eps), nil
		case !errors.Is(err, tvdb.ErrNotFound):
			return nil, err
		}
		why = append(why, fmt.Sprintf("TVDB has no series %d", id))
	} else if s.tvdb == nil {
		why = append(why, "no TVDB key")
	} else {
		why = append(why, "no TVDB id")
	}
	if t.isAnime() {
		return nil, fmt.Errorf("%w: %s (anime series take TVDB only)", errNotFound, strings.Join(why, "; "))
	}
	if s.tmdb == nil {
		return nil, fmt.Errorf("%w: %s; no TMDb key", errNotFound, strings.Join(why, "; "))
	}
	id := int(t.tmdb.Int64)
	if id <= 0 && t.tvdb.Valid && t.tvdb.Int64 > 0 {
		var err error
		if id, err = s.tmdb.FindTVByTVDB(ctx, int(t.tvdb.Int64)); errors.Is(err, tmdb.ErrNotFound) {
			return nil, fmt.Errorf("%w: %s; TMDb has no series for TVDB %d", errNotFound, strings.Join(why, "; "),
				t.tvdb.Int64)
		} else if err != nil {
			return nil, err
		}
	}
	if id <= 0 {
		return nil, fmt.Errorf("%w: %s; no TMDb id", errNotFound, strings.Join(why, "; "))
	}
	sh, err := fromTMDb(ctx, s.tmdb, id)
	if errors.Is(err, tmdb.ErrNotFound) {
		return nil, fmt.Errorf("%w: %s; TMDb has no series %d", errNotFound, strings.Join(why, "; "), id)
	}
	return sh, err
}

// RefreshTitle refreshes one series now: fetch, then store in one transaction. A series no source has is recorded
// (Result.NotFound) and is not an error. After a first refresh of an added series, the searches that waited for it
// go to the acquirer.
func (s *Service) RefreshTitle(ctx context.Context, titleID int64, force bool) (*Result, error) {
	if !s.Enabled() {
		return nil, ErrOff
	}
	t, err := s.loadTitle(ctx, titleID)
	if err != nil {
		return nil, err
	}
	res := &Result{TitleID: titleID}
	sh, err := s.fetch(ctx, t, force)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		notFound := errors.Is(err, errNotFound)
		if rerr := s.recordFailure(ctx, titleID, err.Error(), notFound && t.addedAt.Valid); rerr != nil {
			return nil, rerr
		}
		if !notFound {
			return nil, err
		}
		res.NotFound, res.Error = true, err.Error()
		s.log.Info("series metadata: not found", "title", titleID, "name", t.name, "why", err)
		s.release(ctx, titleID, t.addedAt.Valid)
		return res, nil
	}
	sh.prepare(t.isAnime())
	res.Source, res.Episodes = sh.source, len(sh.episodes)
	if err := s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		r := *res
		if err := s.store(ctx, tx, t, sh, &r); err != nil {
			return err
		}
		*res = r
		return nil
	}); err != nil {
		return nil, err
	}
	s.log.Info("series metadata refreshed", "title", titleID, "name", t.name, "source", sh.source, "episodes",
		res.Episodes, "added", res.Added, "updated", res.Updated, "moved", res.Moved, "deleted", res.Deleted,
		"removed", res.Removed, "states", res.States)
	if err := s.refreshScene(ctx, titleID); err != nil {
		s.log.Warn("series scene refresh", "title", titleID, "err", err)
	}
	s.release(ctx, titleID, t.addedAt.Valid)
	return res, nil
}

// release hands the searches that waited for the series to the acquirer.
func (s *Service) release(ctx context.Context, titleID int64, added bool) {
	if s.search == nil {
		return
	}
	if !added {
		var n int
		if err := s.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM series_search_wait WHERE title_id = ?`,
			titleID).Scan(&n); err != nil || n == 0 {
			return
		}
	}
	if err := s.search.ReleaseWaiting(ctx, titleID); err != nil {
		s.log.Warn("series metadata: releasing held searches", "title", titleID, "err", err)
	}
}

// recordFailure notes a failed try; done (a source said it has no such series) also ends the add's wait.
func (s *Service) recordFailure(ctx context.Context, titleID int64, msg string, done bool) error {
	now := store.Millis(s.now())
	q := `INSERT INTO series_meta (title_id, attempted_at, error) VALUES (?, ?, ?) ON CONFLICT (title_id) DO UPDATE
		SET attempted_at = excluded.attempted_at, error = excluded.error`
	if done {
		q = `INSERT INTO series_meta (title_id, attempted_at, error) VALUES (?, ?, ?) ON CONFLICT (title_id) DO UPDATE
			SET attempted_at = excluded.attempted_at, error = excluded.error, added_at = NULL`
	}
	_, err := s.st.Writer().ExecContext(ctx, q, titleID, now, msg)
	return err
}
