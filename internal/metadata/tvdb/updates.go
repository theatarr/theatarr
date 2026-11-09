package tvdb

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// MaxUpdatesWindow bounds how far back /updates is asked. After a longer gap the TTLs alone refresh the cache
// (an ended series within a month), and the result says it was truncated.
const MaxUpdatesWindow = 7 * 24 * time.Hour

// maxUpdatePages caps the pages read per entity type in one refresh (TVDB's /updates covers every record it has).
const maxUpdatePages = 50

// updateTypes are the entity types that change what we cache: series records, episode records, their translations.
var updateTypes = []string{"series", "episodes", "translatedseries", "translatedepisodes"}

// RefreshResult is what a Refresh did.
type RefreshResult struct {
	// First is true when there was no watermark yet: it is set to now and nothing is invalidated.
	First bool `json:"first,omitempty"`
	// Since and Until are the window asked (Until becomes the next Since).
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
	// Records is how many update records were read; Series and Episodes how many distinct ids they named.
	Records  int `json:"records"`
	Series   int `json:"series"`
	Episodes int `json:"episodes"`
	// Invalidated is how many cached responses were marked stale (revalidated, conditionally, on next use).
	Invalidated int64 `json:"invalidated"`
	// Truncated: the gap was longer than MaxUpdatesWindow, or a type had more than maxUpdatePages pages.
	Truncated bool `json:"truncated,omitempty"`
	// SeriesIDs are the changed series, sorted, for the catalog refresh (internal/metadata/tvrefresh).
	SeriesIDs []int `json:"-"`
}

// Refresh reads GET /updates since the last run and marks the cached responses of every changed series and episode
// stale, so the next read revalidates them. Nothing is fetched for series we
// have not cached. A failure leaves the watermark where it was.
func (c *Client) Refresh(ctx context.Context) (*RefreshResult, error) {
	st := c.opt.Store
	if st == nil {
		return nil, errors.New("tvdb: refresh needs the database")
	}
	now := c.opt.Now().UTC().Truncate(time.Second)
	var sinceMs int64
	err := st.Reader().QueryRowContext(ctx, `SELECT since_at FROM tvdb_sync WHERE id = 1`).Scan(&sinceMs)
	if errors.Is(err, sql.ErrNoRows) {
		res := &RefreshResult{First: true, Since: now, Until: now}
		return res, c.saveSync(ctx, now, res)
	}
	if err != nil {
		return nil, err
	}
	res := &RefreshResult{Since: store.FromMillis(sinceMs), Until: now}
	if now.Sub(res.Since) > MaxUpdatesWindow {
		res.Since, res.Truncated = now.Add(-MaxUpdatesWindow), true
	}
	series, episodes := map[int]bool{}, map[int]bool{}
	defer c.dropUpdatesCache(context.WithoutCancel(ctx))
	for _, typ := range updateTypes {
		for page := 0; ; page++ {
			if page == maxUpdatePages {
				res.Truncated = true
				break
			}
			var ups []Update
			q := url.Values{"since": {strconv.FormatInt(res.Since.Unix(), 10)}, "type": {typ}, "page": {strconv.Itoa(page)}}
			links, err := c.get(ctx, "/updates", q, fixed(time.Nanosecond), &ups)
			if err != nil {
				return nil, err
			}
			for _, u := range ups {
				res.Records++
				switch typ {
				case "series", "translatedseries":
					series[u.RecordID] = true
					if u.MergeToID > 0 {
						series[u.MergeToID] = true
					}
				default:
					episodes[u.RecordID] = true
					if u.SeriesID > 0 {
						series[u.SeriesID] = true
					}
				}
			}
			if links == nil || links.Next == nil || *links.Next == "" || len(ups) == 0 {
				break
			}
		}
	}
	res.Series, res.Episodes = len(series), len(episodes)
	for id := range series {
		res.SeriesIDs = append(res.SeriesIDs, id)
	}
	sort.Ints(res.SeriesIDs)
	n, err := c.invalidate(ctx, series, episodes)
	if err != nil {
		return nil, err
	}
	res.Invalidated = n
	return res, c.saveSync(ctx, now, res)
}

// Invalidate marks every cached answer of one series stale (its record and episode listings), so the next read
// revalidates them: an on-demand refresh sees TVDB's current data. Episode records are left to their TTL.
func (c *Client) Invalidate(ctx context.Context, seriesID int) (int64, error) {
	if c.opt.Store == nil || seriesID <= 0 {
		return 0, nil
	}
	return c.invalidate(ctx, map[int]bool{seriesID: true}, nil)
}

// invalidate marks stale every cached /series/{id}/... answer of a changed series and /episodes/{id}/... answer of
// a changed episode.
func (c *Client) invalidate(ctx context.Context, series, episodes map[int]bool) (int64, error) {
	if len(series) == 0 && len(episodes) == 0 {
		return 0, nil
	}
	var total int64
	err := c.opt.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		total = 0
		for _, k := range []struct {
			prefix string
			ids    map[int]bool
		}{{"GET " + c.base + "/series/", series}, {"GET " + c.base + "/episodes/", episodes}} {
			if len(k.ids) == 0 {
				continue
			}
			rows, err := tx.QueryContext(ctx, `SELECT key FROM metadata_cache WHERE source = ? AND substr(key, 1, ?) = ?`,
				Source, len(k.prefix), k.prefix)
			if err != nil {
				return err
			}
			var hit []string
			for rows.Next() {
				var key string
				if err := rows.Scan(&key); err != nil {
					rows.Close()
					return err
				}
				rest := key[len(k.prefix):]
				if i := strings.IndexAny(rest, "/?"); i >= 0 {
					rest = rest[:i]
				}
				if id, err := strconv.Atoi(rest); err == nil && k.ids[id] {
					hit = append(hit, key)
				}
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			for _, key := range hit {
				r, err := tx.ExecContext(ctx, `UPDATE metadata_cache SET expires_at = 0 WHERE source = ? AND key = ?`, Source, key)
				if err != nil {
					return err
				}
				n, _ := r.RowsAffected()
				total += n
			}
		}
		return nil
	})
	return total, err
}

// dropUpdatesCache removes the /updates answers the fetcher stored (they are never read back).
func (c *Client) dropUpdatesCache(ctx context.Context) {
	p := "GET " + c.base + "/updates?"
	_, _ = c.opt.Store.Writer().ExecContext(ctx, `DELETE FROM metadata_cache WHERE source = ? AND substr(key, 1, ?) = ?`,
		Source, len(p), p)
}

func (c *Client) saveSync(ctx context.Context, until time.Time, r *RefreshResult) error {
	_, err := c.opt.Store.Writer().ExecContext(ctx, `INSERT INTO tvdb_sync (id, since_at, run_at, records, invalidated, truncated)
		VALUES (1, ?, ?, ?, ?, ?) ON CONFLICT (id) DO UPDATE SET since_at = excluded.since_at, run_at = excluded.run_at,
		records = excluded.records, invalidated = excluded.invalidated, truncated = excluded.truncated`,
		store.Millis(until), store.Millis(c.opt.Now()), r.Records, r.Invalidated, r.Truncated)
	return err
}
