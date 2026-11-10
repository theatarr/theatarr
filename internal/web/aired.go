package web

import (
	"database/sql"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// airClock is the one rule the pages show an episode as aired or still to come by (counts, sorts, the title page,
// list rows' coverage and Home's rows). An episode with an air instant (air_date_utc_at) has aired once the instant
// has passed, as the engine and Wanted have it. One without an instant (an import can leave a date alone) goes by
// the day of its air date against the UTC day, as these pages did before instants were read: aired from that day on,
// still to come up to and including it. What is searched does not read this.
type airClock struct {
	nowMs int64
	today string // YYYY-MM-DD, UTC
}

func newAirClock(now time.Time) airClock {
	return airClock{nowMs: store.Millis(now), today: now.UTC().Format(time.DateOnly)}
}

// airedSQL is the rule as a condition on an episode aliased by alias ("" for none), with its two arguments.
func (c airClock) airedSQL(alias string) (string, []any) {
	return c.sql(alias, "<=", "<=")
}

// comingSQL is its counterpart for "next" and "upcoming": the instant is still to come; without an instant, the air
// date is today or later.
func (c airClock) comingSQL(alias string) (string, []any) {
	return c.sql(alias, ">", ">=")
}

func (c airClock) sql(alias, instant, date string) (string, []any) {
	if alias != "" {
		alias += "."
	}
	at, day := alias+"air_date_utc_at", alias+"air_date"
	return `(CASE WHEN ` + at + ` IS NOT NULL THEN ` + at + ` ` + instant + ` ? ELSE ` + day + ` IS NOT NULL AND ` +
		day + ` ` + date + ` ? END)`, []any{c.nowMs, c.today}
}

// aired is airedSQL for a row in hand: its instant, and its air date ("" for none).
func (c airClock) aired(at sql.NullInt64, date string) bool {
	if at.Valid {
		return at.Int64 <= c.nowMs
	}
	return date != "" && date <= c.today
}
