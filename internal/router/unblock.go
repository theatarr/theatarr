package router

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/theatarr/theatarr/internal/store"
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
	store.HistoryUnblock(ctx, tx, version, b.Hash)
	if _, err := tx.ExecContext(ctx, `DELETE FROM router_blocklist WHERE version_key = ? AND info_hash = ?`, version,
		b.Hash); err != nil {
		return nil, err
	}
	return b, nil
}

// BlockedRelease is the blocklist row that keeps version from routing a release (Blocklisted: by its hash or by its
// title key), the hash row first; nil when there is none.
func BlockedRelease(ctx context.Context, db *sql.DB, version, hash, release string) (*Blocked, error) {
	b := &Blocked{Version: version}
	var title, dl sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT info_hash, title_id, release_title, reason, source, download_id
		FROM router_blocklist WHERE version_key = ? AND info_hash IN (?, ?) ORDER BY info_hash LIKE 'title:%' LIMIT 1`,
		version, BlocklistKey(hash, ""), BlocklistKey("", release)).Scan(&b.Hash, &title, &b.Release, &b.Reason,
		&b.Source, &dl)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b.TitleID, b.DownloadID = title.Int64, dl.Int64
	return b, nil
}

// UnblockRelease is Unblock for a grab by hand that takes a release off version's blocklist: both rows that match it
// go, the hash row and the title-key row, or a route would still skip it. The first row removed is returned (the
// hash row when there is one); sql.ErrNoRows when neither is there.
func UnblockRelease(ctx context.Context, tx *sql.Tx, version, hash, release string) (*Blocked, error) {
	var first *Blocked
	for _, k := range []string{BlocklistKey(hash, ""), BlocklistKey("", release)} {
		if k == "" {
			continue
		}
		b, err := Unblock(ctx, tx, version, k)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if first == nil {
			first = b
		}
	}
	if first == nil {
		return nil, sql.ErrNoRows
	}
	return first, nil
}
