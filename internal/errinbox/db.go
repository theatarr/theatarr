package errinbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// Row is one fingerprint of the inbox.
type Row struct {
	ID          int64           `json:"id"`
	Fingerprint string          `json:"fingerprint"`
	Source      string          `json:"source"`
	Component   string          `json:"component"`
	Level       string          `json:"level"`
	Message     string          `json:"message"`
	Sample      string          `json:"sample"`
	Context     json.RawMessage `json:"context"`
	Stack       string          `json:"stack,omitempty"`
	Count       int64           `json:"count"`
	FirstSeen   time.Time       `json:"first_seen"`
	LastSeen    time.Time       `json:"last_seen"`
	Status      string          `json:"status"`
	ResolvedAt  *time.Time      `json:"resolved_at,omitempty"`
	Regressions int64           `json:"regressions"`
	// FirstVersion and LastVersion are the build versions at the first and the latest sighting.
	FirstVersion string    `json:"first_version"`
	LastVersion  string    `json:"last_version"`
	Note         string    `json:"note"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ErrNotFound is an id with no row.
var ErrNotFound = errors.New("errinbox: no such error")

// Filter selects rows. Zero values select everything.
type Filter struct {
	// Status is one status, or "open" for new and seen.
	Status string
	Source string
	// Since keeps rows last seen after it.
	Since  time.Time
	Limit  int
	Offset int
}

// Page is a list answer: the rows (newest sighting first, without stacks), how many match, and the count per
// status over the whole table.
type Page struct {
	Items  []Row            `json:"items"`
	Total  int64            `json:"total"`
	Counts map[string]int64 `json:"counts"`
	// Drops is the number of events dropped because the queue was full, over the inbox's life.
	Drops int64 `json:"drops"`
}

// ValidStatus reports whether s is a status a filter may name ("open" included).
func ValidStatus(s string, filter bool) bool {
	return slices.Contains(Statuses, s) || (filter && (s == "open" || s == ""))
}

// ValidSource reports whether s is a source ("" included).
func ValidSource(s string) bool { return s == "" || slices.Contains(Sources, s) }

const rowColumns = `id, fingerprint, source, component, level, message, sample, context, stack, count, first_seen,
	last_seen, status, resolved_at, regressions, first_version, last_version, note, updated_at`

type scanner interface{ Scan(...any) error }

func scanRow(s scanner) (Row, error) {
	var r Row
	var ctx string
	var first, last, updated int64
	var resolved sql.NullInt64
	if err := s.Scan(&r.ID, &r.Fingerprint, &r.Source, &r.Component, &r.Level, &r.Message, &r.Sample, &ctx, &r.Stack,
		&r.Count, &first, &last, &r.Status, &resolved, &r.Regressions, &r.FirstVersion, &r.LastVersion, &r.Note,
		&updated); err != nil {
		return Row{}, err
	}
	r.Context = json.RawMessage(ctx)
	if !json.Valid(r.Context) {
		r.Context = json.RawMessage("{}")
	}
	r.FirstSeen, r.LastSeen, r.UpdatedAt = store.FromMillis(first), store.FromMillis(last), store.FromMillis(updated)
	if resolved.Valid {
		t := store.FromMillis(resolved.Int64)
		r.ResolvedAt = &t
	}
	return r, nil
}

func (f Filter) where() (string, []any) {
	var conds []string
	var args []any
	switch f.Status {
	case "":
	case "open":
		conds = append(conds, "status IN ('new', 'seen')")
	default:
		conds = append(conds, "status = ?")
		args = append(args, f.Status)
	}
	if f.Source != "" {
		conds = append(conds, "source = ?")
		args = append(args, f.Source)
	}
	if !f.Since.IsZero() {
		conds = append(conds, "last_seen > ?")
		args = append(args, store.Millis(f.Since))
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// List returns a page of rows. A limit of 0 means 100; at most 500.
func List(ctx context.Context, st *store.Store, f Filter) (Page, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	f.Limit = min(f.Limit, 500)
	where, args := f.where()
	p := Page{Items: []Row{}, Counts: map[string]int64{}}
	for _, s := range Statuses {
		p.Counts[s] = 0
	}
	db := st.Reader()
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM error_event`+where, args...).Scan(&p.Total); err != nil {
		return p, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+strings.Replace(rowColumns, "stack,", "'' AS stack,", 1)+
		` FROM error_event`+where+` ORDER BY last_seen DESC, id DESC LIMIT ? OFFSET ?`,
		append(args, f.Limit, max(f.Offset, 0))...)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			rows.Close()
			return p, err
		}
		p.Items = append(p.Items, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return p, err
	}
	counts, err := db.QueryContext(ctx, `SELECT status, COUNT(*) FROM error_event GROUP BY status`)
	if err != nil {
		return p, err
	}
	defer counts.Close()
	for counts.Next() {
		var s string
		var n int64
		if err := counts.Scan(&s, &n); err != nil {
			return p, err
		}
		p.Counts[s] = n
	}
	if err := counts.Err(); err != nil {
		return p, err
	}
	p.Drops, err = StateInt(ctx, st, "drops")
	return p, err
}

// Get returns one row with its stack.
func Get(ctx context.Context, st *store.Store, id int64) (Row, error) {
	r, err := scanRow(st.Reader().QueryRowContext(ctx, `SELECT `+rowColumns+` FROM error_event WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Row{}, ErrNotFound
	}
	return r, err
}

// Update sets a row's status (resolved stamps resolved_at, any other status clears it) and, when note is not nil,
// its note. An empty status keeps the status.
func Update(ctx context.Context, st *store.Store, id int64, status string, note *string, now time.Time) (Row, error) {
	if status != "" && !ValidStatus(status, false) {
		return Row{}, fmt.Errorf("status must be one of %s", strings.Join(Statuses, ", "))
	}
	err := st.WriteTx(Quiet(ctx), func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE error_event SET
			status = CASE WHEN ?1 = '' THEN status ELSE ?1 END,
			resolved_at = CASE WHEN ?1 = '' THEN resolved_at WHEN ?1 = 'resolved' THEN ?2 ELSE NULL END,
			note = CASE WHEN ?3 THEN ?4 ELSE note END, updated_at = ?2
			WHERE id = ?5`, status, store.Millis(now), note != nil, deref(note), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return Row{}, err
	}
	return Get(ctx, st, id)
}

// Bulk sets the status of the given ids, or with no ids of every row the filter selects (an empty filter is
// refused). It returns the rows changed.
func Bulk(ctx context.Context, st *store.Store, ids []int64, f Filter, status string, note *string, now time.Time) (int64, error) {
	if !ValidStatus(status, false) {
		return 0, fmt.Errorf("status must be one of %s", strings.Join(Statuses, ", "))
	}
	var where string
	var args []any
	if len(ids) > 0 {
		where = " WHERE id IN (" + store.Placeholders(len(ids)) + ")"
		for _, id := range ids {
			args = append(args, id)
		}
	} else {
		where, args = f.where()
		if where == "" {
			return 0, errors.New("give ids or a filter (status, source or since)")
		}
	}
	var n int64
	err := st.WriteTx(Quiet(ctx), func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE error_event SET status = ?, updated_at = ?,
			resolved_at = CASE WHEN ? = 'resolved' THEN ? ELSE NULL END,
			note = CASE WHEN ? THEN ? ELSE note END`+where,
			append([]any{status, store.Millis(now), status, store.Millis(now), note != nil, deref(note)}, args...)...)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// State returns a state value (health, drops) and when it was written; "" when unset.
func State(ctx context.Context, st *store.Store, key string) (string, time.Time, error) {
	var v string
	var at int64
	err := st.Reader().QueryRowContext(ctx, `SELECT value, updated_at FROM error_inbox_state WHERE key = ?`, key).Scan(&v, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, nil
	}
	if err != nil {
		return "", time.Time{}, err
	}
	return v, store.FromMillis(at), nil
}

// StateInt is State for a number; 0 when unset.
func StateInt(ctx context.Context, st *store.Store, key string) (int64, error) {
	v, _, err := State(ctx, st, key)
	if err != nil || v == "" {
		return 0, err
	}
	var n int64
	_ = json.Unmarshal([]byte(v), &n)
	return n, nil
}
