package availability

import (
	"context"
	"database/sql"
	"slices"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Where a library's minimum availability for a movie comes from.
const (
	// SourceChoice is an explicit choice: the newest arrview_title row with a value whose view is the library's own
	// (v/<key>) or a configured view holding the library.
	SourceChoice = "choice"
	// SourceDefault is the library's minimum_availability, released when unset.
	SourceDefault = "default"
)

// Rule is one library's availability rule for a movie.
type Rule struct {
	// Minimum is tba, announced, inCinemas or released.
	Minimum string
	// Delay is the library's availability_delay in days.
	Delay int
	// Source is SourceChoice or SourceDefault.
	Source string
}

// queryer is a *sql.DB or *sql.Tx.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// copyRef names one version of a title.
type copyRef struct {
	titleID, versionID int64
	key                string
}

// Rules gives the availability rule of every version of the titles, by version id: the explicit choice, else the
// library's default, else released. (A value an arr import set was copied to the library's own choice by migrations
// 00121 and 00125.) Movie and every Radarr view read it.
func Rules(ctx context.Context, db queryer, pol *policy.Policy, titleIDs []int64) (map[int64]Rule, error) {
	out := map[int64]Rule{}
	for ids := range slices.Chunk(titleIDs, datesBatch) {
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		rows, err := db.QueryContext(ctx, `SELECT title_id, id, policy_key FROM version WHERE title_id IN (`+
			store.Placeholders(len(args))+`)`, args...)
		if err != nil {
			return nil, err
		}
		var copies []copyRef
		for rows.Next() {
			var c copyRef
			if err := rows.Scan(&c.titleID, &c.versionID, &c.key); err != nil {
				rows.Close()
				return nil, err
			}
			copies = append(copies, c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if err := rulesInto(ctx, db, pol, copies, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// rulesInto resolves the copies' rules into out, by version id.
func rulesInto(ctx context.Context, db queryer, pol *policy.Policy, copies []copyRef, out map[int64]Rule) error {
	if len(copies) == 0 {
		return nil
	}
	var titleArgs []any
	seen := map[int64]bool{}
	for _, c := range copies {
		if !seen[c.titleID] {
			seen[c.titleID] = true
			titleArgs = append(titleArgs, c.titleID)
		}
	}
	// Explicit choices, newest first per title.
	type choice struct{ view, value string }
	choices := map[int64][]choice{}
	rows, err := db.QueryContext(ctx, `SELECT title_id, view, minimum_availability FROM arrview_title
		WHERE title_id IN (`+store.Placeholders(len(titleArgs))+`) AND minimum_availability <> ''
		ORDER BY updated_at DESC, view`, titleArgs...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var c choice
		if err := rows.Scan(&id, &c.view, &c.value); err != nil {
			rows.Close()
			return err
		}
		choices[id] = append(choices[id], c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range copies {
		r := Rule{Minimum: "released", Source: SourceDefault}
		if vp := pol.Versions[c.key]; vp != nil {
			r.Delay = vp.AvailabilityDelay
			if vp.MinimumAvailability != "" {
				r.Minimum = vp.MinimumAvailability
			}
		}
		for _, ch := range choices[c.titleID] {
			v := pol.Views[ch.view]
			if ch.view == "v/"+c.key || v != nil && slices.Contains(v.Versions, c.key) {
				r.Minimum, r.Source = ch.value, SourceChoice
				break
			}
		}
		out[c.versionID] = r
	}
	return nil
}

// rank orders the minimum availabilities from earliest to latest; a value Available does not know gates as released.
func rank(minimum string) int {
	switch minimum {
	case "tba":
		return 0
	case "announced":
		return 1
	case "inCinemas":
		return 2
	}
	return 3
}

// Earliest is the earliest of the values (tba, announced, inCinemas, released), the first of equals; released
// without any.
func Earliest(values ...string) string {
	if len(values) == 0 {
		return "released"
	}
	best := values[0]
	for _, v := range values[1:] {
		if rank(v) < rank(best) {
			best = v
		}
	}
	return best
}

// execer is a *sql.DB or *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Choose stores value as the explicit minimum availability of the title's libraries keys, on each library's own
// v/<key> row. A new row carries nothing else (no profile, root folder or added time), and an existing one keeps
// the rest.
func Choose(ctx context.Context, tx execer, titleID int64, keys []string, value string, now time.Time) error {
	for _, k := range keys {
		if _, err := tx.ExecContext(ctx, `INSERT INTO arrview_title (view, title_id, minimum_availability, added_at,
			updated_at) VALUES (?, ?, ?, 0, ?) ON CONFLICT (view, title_id) DO UPDATE SET
			minimum_availability = excluded.minimum_availability, updated_at = excluded.updated_at`,
			"v/"+k, titleID, value, store.Millis(now)); err != nil {
			return err
		}
	}
	return nil
}
