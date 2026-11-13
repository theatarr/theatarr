package activity

import (
	"context"
	"database/sql"
	"time"

	"github.com/theatarr/theatarr/internal/textsearch"
)

// Blocked is a release a version never routes again (router_blocklist, migration 00016).
type Blocked struct {
	Version    string
	Hash       string
	TitleID    int64
	TitleName  string
	TitleYear  int
	TitleKind  string
	Anime      bool
	Release    string
	Reason     string
	Source     string // library, stalled, torrent_files, queue, journal, ...
	DownloadID int64
	Created    time.Time
	Researched time.Time // zero when no re-search followed yet
	Quality    string    // the blocked download's quality and indexer; "" when not known
	Indexer    string
}

// BlockedFilter narrows the blocklist. Empty fields match every row.
type BlockedFilter struct {
	Title   string // words, each in the title's name or the blocked release's name
	Version string // a library (version) key
}

// BlockedReleases lists the blocklist f selects, newest first, with the total f selects. Quality and Indexer come
// from the blocked download when it is known.
func BlockedReleases(ctx context.Context, db *sql.DB, f BlockedFilter, limit, offset int) ([]Blocked, int, error) {
	where := ` WHERE 1 = 1`
	var args []any
	if cond, a := textsearch.Where(f.Title, "t.name", "b.release_title"); cond != "" {
		where += ` AND ` + cond
		args = append(args, a...)
	}
	if f.Version != "" {
		where += ` AND b.version_key = ?`
		args = append(args, f.Version)
	}
	from := ` FROM router_blocklist b LEFT JOIN title t ON t.id = b.title_id`
	var total int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*)`+from+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := db.QueryContext(ctx, `SELECT b.version_key, b.info_hash, COALESCE(b.title_id, 0), COALESCE(t.name, ''),
		COALESCE(t.year, 0), COALESCE(t.kind, ''), COALESCE(t.anime, 0), b.release_title, b.reason, b.source,
		COALESCE(b.download_id, 0), b.created_at, COALESCE(b.researched_at, 0), COALESCE(d.quality, ''),
		COALESCE(d.indexer, '')`+from+` LEFT JOIN download d ON d.id = b.download_id`+where+`
		ORDER BY b.created_at DESC, b.info_hash LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Blocked
	for rows.Next() {
		var b Blocked
		var created, researched int64
		if err := rows.Scan(&b.Version, &b.Hash, &b.TitleID, &b.TitleName, &b.TitleYear, &b.TitleKind, &b.Anime,
			&b.Release, &b.Reason, &b.Source, &b.DownloadID, &created, &researched, &b.Quality, &b.Indexer); err != nil {
			return nil, 0, err
		}
		b.Created, b.Researched = at(created), at(researched)
		out = append(out, b)
	}
	return out, total, rows.Err()
}
