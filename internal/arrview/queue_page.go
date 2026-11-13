package arrview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/activity"
	"github.com/theatarr/theatarr/internal/store"
)

const queueCompletedSQL = `i.import_state IN ('rejected','dry_run') OR i.import_error LIKE 'episode title hold: %' OR i.state IN ('ready','importing')`
const queueProgressSQL = `CASE WHEN i.size<=0 THEN 0 WHEN ` + queueCompletedSQL + ` THEN 100 ELSE MIN(MAX(i.progress,0),100) END`

type queueIdentity struct{ source, episode int64 }

// Sonarr hashes the download and episode identity. Keep ours within JSON's exact integer range and apart
// from download ids. Clients have stored these ids: the hash does not change.
func queueEpisodeID(source, episode int64) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strconv.FormatInt(source, 10) + ":" + strconv.FormatInt(episode, 10)))
	return int64(h.Sum64()&((1<<52)-1)) + (1 << 52)
}

// queueItemID is the id a queue item without an episode carries: the download id.
func queueItemID(source int64) int64 {
	return source
}

func queueSQL(v *View, titleIDs []int64, now time.Time) (string, []any) {
	q := `WITH downloads AS (
 SELECT d.id source, d.title_id tid, d.episodes, d.release_title title, d.indexer, d.quality,
 COALESCE(d.size_bytes,0) size, d.progress, COALESCE(d.provider,'') provider, d.state,
 COALESCE(d.error,'') error, COALESCE(li.state,'') import_state, COALESCE(li.last_error,'') import_error, d.created_at
 FROM download d LEFT JOIN library_import li ON li.download_id=d.id
 WHERE d.version_key IN (` + store.Placeholders(len(v.Versions)) + `) AND
 (d.state IN ('queued','adding','added','downloading','ready','importing') OR
 (` + activity.QueueFailureSQL + `))
 ), items AS (SELECT d.*, `
	if v.Flavour == Sonarr {
		q += `CAST(j.value AS INTEGER)`
	} else {
		q += `0`
	}
	q += ` eid FROM downloads d`
	if v.Flavour == Sonarr {
		q += ` LEFT JOIN json_each(d.episodes) j ON 1=1`
	}
	q += ` WHERE 1=1`
	args := append(toAny(v.Versions), store.Millis(now.Add(-7*24*time.Hour)))
	if titleIDs != nil {
		q += ` AND d.tid IN (SELECT value FROM json_each(?))`
		args = append(args, jsonIDs(titleIDs))
	}
	q += `)`
	return q, args
}

func (s *Service) queuePage(r *request, p *pagingResource) ([]queueIdentity, error) {
	ids, given := s.titleFilter(r, "movieIds", "seriesIds")
	if given && ids == nil {
		ids = []int64{}
	}
	q, args := queueSQL(r.v, ids, s.now())
	ctx := r.Context()
	if err := s.st.Reader().QueryRowContext(ctx, q+` SELECT COUNT(*) FROM items`, args...).Scan(&p.TotalRecords); err != nil {
		return nil, err
	}
	key, _ := r.query("sortKey")
	key = strings.ToLower(key)
	dir := " ASC"
	if p.SortDirection == "descending" {
		dir = " DESC"
	}
	order := ""
	estimateJoin := ""
	switch key {
	case "title":
		order = "i.title COLLATE NOCASE"
		p.SortKey = "title"
	case "added":
		order = "i.created_at"
		p.SortKey = "added"
	case "progress":
		order = queueProgressSQL
		p.SortKey = "progress"
	case "status":
		order = `CASE WHEN ` + queueCompletedSQL + ` THEN 'completed' WHEN i.state='failed' THEN 'failed' WHEN i.state='downloading' THEN 'downloading' ELSE 'queued' END`
		p.SortKey = "status"
	case "indexer":
		order = "i.indexer COLLATE NOCASE"
		p.SortKey = "indexer"
	case "downloadclient":
		order = `('theatarr router'||CASE WHEN i.provider='' THEN '' ELSE ' ('||i.provider||')' END) COLLATE NOCASE`
		p.SortKey = "downloadClient"
	case "protocol":
		order = "'torrent'"
		p.SortKey = "protocol"
	case "quality":
		order = "theatarr_quality_weight('" + r.v.Flavour + "',i.quality)"
		p.SortKey = "quality"
	case "series.sorttitle", "movies.sorttitle", "moviemetadata.sorttitle":
		order = `COALESCE(` + titleSortSQL + `,i.title)`
		p.SortKey = key
	case "episode":
		order = "COALESCE(e.season,0)" + dir + ", COALESCE(e.number,0)"
		p.SortKey = key
	case "episode.airdateutc", "episodes.airdateutc":
		order = "e.air_date_utc_at"
		p.SortKey = key
	case "episode.title", "episodes.title":
		order = "COALESCE(e.name,'') COLLATE NOCASE"
		p.SortKey = key
	case "language", "languages":
		order = "0+0"
		p.SortKey = key
	case "year":
		order = "t.year"
		p.SortKey = key
	case "size":
		order = "i.size"
		p.SortKey = key
	default:
		// ETA depends on provider samples and Go duration arithmetic. Read only compact identities in batches;
		// retain one numeric key per download, then let SQL sort and page before loading any queue resources.
		estimates, err := s.queueEstimates(ctx, q, args)
		if err != nil {
			return nil, err
		}
		q += `, estimates AS MATERIALIZED (SELECT CAST(key AS INTEGER) source,value remaining FROM json_each(?))`
		args = append(args, estimates)
		estimateJoin = ` LEFT JOIN estimates eta ON eta.source=i.source`
		order = `eta.remaining`
		if key == "estimatedcompletiontime" {
			p.SortKey = "estimatedCompletionTime"
		}
		// The arr comparers place unknown times last in ascending order.
		order += " IS NULL" + dir + ", " + order
	}
	// QueueController falls back to its last page when a requested page is empty.
	if p.PageSize > 0 && p.Page > 1 && p.offset() >= int64(p.TotalRecords) {
		p.Page = max(p.TotalRecords/p.PageSize, 1)
	}
	rows, err := s.st.Reader().QueryContext(ctx, q+` SELECT i.source,COALESCE(i.eid,0) FROM items i LEFT JOIN title t ON t.id=i.tid LEFT JOIN episode e ON e.id=i.eid`+estimateJoin+` ORDER BY `+order+dir+`, `+queueProgressSQL+` DESC, i.source`+dir+`, i.eid`+dir+` LIMIT ? OFFSET ?`, append(args, p.PageSize, p.offset())...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []queueIdentity{}
	for rows.Next() {
		var it queueIdentity
		if err := rows.Scan(&it.source, &it.episode); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Service) queueEstimates(ctx context.Context, q string, args []any) (string, error) {
	estimates := map[int64]*int64{}
	var after int64 = -1 << 63
	for {
		rows, err := s.st.Reader().QueryContext(ctx, q+` SELECT source,state,import_state,import_error FROM downloads WHERE source IN (SELECT source FROM items) AND source>? ORDER BY source LIMIT 128`, append(args, after)...)
		if err != nil {
			return "", err
		}
		var batch []queueRow
		for rows.Next() {
			var x queueRow
			if err := rows.Scan(&x.id, &x.state, &x.importState, &x.importErr); err != nil {
				rows.Close()
				return "", err
			}
			batch = append(batch, x)
		}
		if err := closeRows(rows); err != nil {
			return "", err
		}
		if len(batch) == 0 {
			break
		}
		remainingByID, err := s.queueRemaining(ctx, batch)
		if err != nil {
			return "", err
		}
		for _, x := range batch {
			after = x.id
			status, _, _, _ := s.queueTracking(x)
			remaining, known := remainingByID[x.id]
			if status == "completed" {
				remaining, known = 0, true
			}
			if known {
				n := int64(remaining)
				estimates[x.id] = &n
			} else {
				estimates[x.id] = nil
			}
		}
	}
	b, err := json.Marshal(estimates)
	return string(b), err
}

func (s *Service) queueSource(ctx context.Context, v *View, id int64) (int64, error) {
	q, args := queueSQL(v, nil, s.now())
	expr := `source`
	if v.Flavour == Sonarr {
		expr = `CASE WHEN eid IS NULL THEN ` + expr + ` ELSE theatarr_queue_id(source,eid) END`
	}
	var source int64
	err := s.st.Reader().QueryRowContext(ctx, q+` SELECT source FROM items WHERE `+expr+`=? OR source=? LIMIT 1`, append(args, id, id)...).Scan(&source)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return source, err
}
