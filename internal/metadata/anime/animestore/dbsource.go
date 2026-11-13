package animestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/store"
)

// SonarrTVDB is the TVDB backend over our own imported Sonarr episodes (`theatarr import-arr`): the episode table holds
// TVDB season and number, the TVDB absolute number (episode_number scheme absolute_tvdb), air dates and runtimes.
//
// Limits against SkyHook or TVDB itself: only series some Sonarr instance has are known; Sonarr's API has no
// airsBefore/After (episode.airs_* stay empty), so TVDB-only specials are placed by date only; there is no
// overview, so the recap flag from TVDB overviews never fires. A TVDB v4 client goes behind the same interface.
type SonarrTVDB struct {
	DB *store.Store
}

// ErrNoSeries is returned for a TVDB series no imported instance has.
var ErrNoSeries = errors.New("animestore: series not imported")

// Show implements anime.TVDB.
func (s SonarrTVDB) Show(ctx context.Context, tvdbID int) (*anime.Show, error) {
	db := s.DB.Reader()
	var titleID int64
	var name string
	err := db.QueryRowContext(ctx, `SELECT id, name FROM title WHERE kind = 'series' AND tvdb_id = ?`, tvdbID).Scan(&titleID, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: TVDB %d", ErrNoSeries, tvdbID)
	}
	if err != nil {
		return nil, err
	}
	show := &anime.Show{TVDBID: tvdbID, Title: &name, HasEpisodes: true, Episodes: []anime.ShowEpisode{}}
	var ids []int
	rows, err := db.QueryContext(ctx, `SELECT external_id FROM title_external_id WHERE title_id = ? AND source = 'anilist'
		ORDER BY CAST(external_id AS INTEGER)`, titleID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var x string
		if err := rows.Scan(&x); err != nil {
			rows.Close()
			return nil, err
		}
		var n int
		if _, err := fmt.Sscan(x, &n); err == nil {
			ids = append(ids, n)
		}
	}
	rows.Close()
	if len(ids) > 0 {
		show.AniListIDs, _ = json.Marshal(ids)
	}
	rows, err = db.QueryContext(ctx, `SELECT e.season, e.number, e.name, e.air_date, e.runtime_minutes, e.airs_before_season,
		e.airs_before_episode, e.airs_after_season,
		(SELECT n.number FROM episode_number n WHERE n.episode_id = e.id AND n.scheme = 'absolute_tvdb' ORDER BY n.id LIMIT 1)
		FROM episode e WHERE e.title_id = ? ORDER BY e.season, e.number`, titleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ep anime.ShowEpisode
		var title string
		var air sql.NullString
		var runtime, bs, be, as, abs sql.NullInt64
		if err := rows.Scan(&ep.SeasonNumber, &ep.EpisodeNumber, &title, &air, &runtime, &bs, &be, &as, &abs); err != nil {
			return nil, err
		}
		ep.Title = &title
		if air.Valid {
			ep.AirDate = &air.String
		}
		ep.Runtime, ep.AiredBeforeSeasonNumber, ep.AiredBeforeEpisodeNumber = nInt(runtime), nInt(bs), nInt(be)
		ep.AiredAfterSeasonNumber, ep.AbsoluteEpisodeNumber = nInt(as), nInt(abs)
		show.Episodes = append(show.Episodes, ep)
	}
	return show, rows.Err()
}

func nInt(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}
