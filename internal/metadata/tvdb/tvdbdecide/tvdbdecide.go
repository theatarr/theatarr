// Package tvdbdecide offers TVDB v4 data to the series decision side where our imported Sonarr has none: a series
// no instance imported gets its episodes (TVDB numbering, absolute numbers, air dates) and names from TVDB. It is
// additive: a title that already has episodes is left alone, and nothing here is called unless a caller opts in.
package tvdbdecide

import (
	"context"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/metadata/anime"
)

// Episodes returns a series' episodes as the decision engine reads them (no ids: they are not in the database).
func Episodes(ctx context.Context, src anime.TVDB, tvdbID int) ([]decide.Episode, error) {
	sh, err := src.Show(ctx, tvdbID)
	if err != nil {
		return nil, err
	}
	out := make([]decide.Episode, 0, len(sh.Episodes))
	for _, e := range sh.Episodes {
		d := decide.Episode{Season: e.SeasonNumber, Number: e.EpisodeNumber}
		if e.AbsoluteEpisodeNumber != nil {
			d.Absolute = *e.AbsoluteEpisodeNumber
		}
		if e.Title != nil {
			d.Title = *e.Title
		}
		if e.AirDate != nil {
			d.AirDate = *e.AirDate
		}
		if e.Runtime != nil {
			d.Runtime = *e.Runtime
		}
		out = append(out, d)
	}
	return out, nil
}
