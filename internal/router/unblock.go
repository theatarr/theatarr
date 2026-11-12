package router

import (
	"context"
	"database/sql"
	"strings"
)

// Blocked is one router_blocklist row.
type Blocked struct {
	Version    string
	Hash       string
	TitleID    int64
	Release    string
	Reason     string
	Source     string
	DownloadID int64
}

// Unblock takes hash off version's blocklist inside the caller's transaction (an operator's Unblock in the web UI),
// so routes may place it again; the row as it was is returned. It writes only our database, and changes nothing
// else: a download that failed stays failed.
// sql.ErrNoRows when the version has no such entry.
func Unblock(ctx context.Context, tx *sql.Tx, version, hash string) (*Blocked, error) {
	b := &Blocked{Version: version, Hash: strings.ToLower(strings.TrimSpace(hash))}
	var title, dl sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT title_id, release_title, reason, source, download_id FROM router_blocklist
		WHERE version_key = ? AND info_hash = ?`, version, b.Hash).Scan(&title, &b.Release, &b.Reason, &b.Source, &dl)
	if err != nil {
		return nil, err
	}
	b.TitleID, b.DownloadID = title.Int64, dl.Int64
	if _, err := tx.ExecContext(ctx, `DELETE FROM router_blocklist WHERE version_key = ? AND info_hash = ?`, version,
		b.Hash); err != nil {
		return nil, err
	}
	return b, nil
}
