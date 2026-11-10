package store

import (
	"context"
	"database/sql"
	"errors"
)

// SourceSettings are the arr's title settings for a version, inherited through follows for a parallel version.
// Files, profiles and ownership remain the version's own.
type SourceSettings struct {
	MinimumAvailability string
}

// LoadSourceSettings prefers the owning instance, then any other imported view of the source version.
// No imported view leaves the fields empty, so callers can keep their native defaults.
func LoadSourceSettings(ctx context.Context, db *sql.DB, versionID int64) (SourceSettings, error) {
	var s SourceSettings
	err := db.QueryRowContext(ctx, `SELECT a.minimum_availability
		FROM version v LEFT JOIN version_follow f ON f.version_id = v.id
		JOIN arr_title a ON a.version_id = COALESCE(f.follows_id, v.id)
		LEFT JOIN version_source vs ON vs.version_id = a.version_id AND vs.instance = a.instance AND vs.arr_id = a.arr_id
		WHERE v.id = ? ORDER BY (vs.instance IS NOT NULL) DESC, a.instance LIMIT 1`, versionID).
		Scan(&s.MinimumAvailability)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil
	}
	return s, err
}
