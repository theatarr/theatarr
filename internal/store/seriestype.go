package store

import (
	"context"
	"database/sql"
)

// SeriesTypeSQL is the SQL expression for a series' effective type (standard, daily or anime), given the alias of
// its title row; ” for a movie. It is the one rule every reader of the type uses (docs/design/series.md "Series
// type"): title.series_type; when that is empty, the type an arr import left on an arr_title row of the title (the
// first by instance: the import copies it onto the title, so this only reads a title that missed the copy); else
// anime when the title's anime flag is set, else standard. The per-instance imported types never override the
// title's value.
func SeriesTypeSQL(alias string) string {
	t := alias + "."
	return `(CASE WHEN ` + t + `kind <> 'series' THEN '' ELSE COALESCE(NULLIF(` + t + `series_type, ''),
		(SELECT a.series_type FROM arr_title a WHERE a.title_id = ` + t + `id AND a.kind = 'series'
			AND a.series_type <> '' ORDER BY a.instance, a.arr_id LIMIT 1),
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
