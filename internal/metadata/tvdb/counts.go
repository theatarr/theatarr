package tvdb

import (
	"context"
	"fmt"
)

// EpisodeCounts returns {season: episodes} of a series in its default order, specials in season 0, from the
// cached listing: what Torrentio's season packs are scaled by (TMDb gives the same).
func (c *Client) EpisodeCounts(ctx context.Context, id int) (map[int]int, error) {
	if id <= 0 {
		return nil, fmt.Errorf("tvdb: bad series id %d", id)
	}
	_, eps, err := c.Episodes(ctx, id, "default", "")
	if err != nil {
		return nil, err
	}
	out := map[int]int{}
	for _, e := range eps {
		out[e.SeasonNumber]++
	}
	return out, nil
}
