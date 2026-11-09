package activity

import (
	"context"
	"database/sql"
	"time"
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
}

// BlockedReleases lists the blocklist, newest first, with the total.
func BlockedReleases(ctx context.Context, db *sql.DB, limit, offset int) ([]Blocked, int, error) {
	var total int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM router_blocklist`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := db.QueryContext(ctx, `SELECT b.version_key, b.info_hash, COALESCE(b.title_id, 0), COALESCE(t.name, ''),
		COALESCE(t.year, 0), COALESCE(t.kind, ''), COALESCE(t.anime, 0), b.release_title, b.reason, b.source,
		COALESCE(b.download_id, 0), b.created_at, COALESCE(b.researched_at, 0)
		FROM router_blocklist b LEFT JOIN title t ON t.id = b.title_id
		ORDER BY b.created_at DESC, b.info_hash LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Blocked
	for rows.Next() {
		var b Blocked
		var created, researched int64
		if err := rows.Scan(&b.Version, &b.Hash, &b.TitleID, &b.TitleName, &b.TitleYear, &b.TitleKind, &b.Anime,
			&b.Release, &b.Reason, &b.Source, &b.DownloadID, &created, &researched); err != nil {
			return nil, 0, err
		}
		b.Created, b.Researched = at(created), at(researched)
		out = append(out, b)
	}
	return out, total, rows.Err()
}
