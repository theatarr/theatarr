package store

import (
	"context"
	"database/sql"
)

// SeriesTypeSQL is the SQL expression for a series' effective type (standard, daily or anime), given the alias of
// its title row; ” for a movie. It is the one rule every reader of the type uses (docs/design/series.md "Series
// type"): title.series_type; when that is empty, anime when the title's anime flag is set, else standard.
func SeriesTypeSQL(alias string) string {
	t := alias + "."
	return `(CASE WHEN ` + t + `kind <> 'series' THEN '' ELSE COALESCE(NULLIF(` + t + `series_type, ''),
		CASE WHEN ` + t + `anime = 1 THEN 'anime' ELSE 'standard' END) END)`
}

// SeriesType is a title's effective series type (SeriesTypeSQL); ” for a movie or an unknown title.
func SeriesType(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, titleID int64) (string, error) {
	var typ string
	err := q.QueryRowContext(ctx, `SELECT `+SeriesTypeSQL("t")+` FROM title t WHERE t.id = ?`, titleID).Scan(&typ)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return typ, err
}
