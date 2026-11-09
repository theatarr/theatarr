package importarr

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Counts is how many rows of one table a run inserted, updated or deleted.
type Counts struct {
	Inserted int `json:"inserted,omitempty"`
	Updated  int `json:"updated,omitempty"`
	Deleted  int `json:"deleted,omitempty"`
}

// Changes maps a table name to its counts. A run that changed nothing has no entries.
type Changes map[string]*Counts

func (c Changes) add(table string, op byte, n int) {
	if n == 0 || op == 0 {
		return
	}
	x := c[table]
	if x == nil {
		x = &Counts{}
		c[table] = x
	}
	switch op {
	case opInsert:
		x.Inserted += n
	case opUpdate:
		x.Updated += n
	case opDelete:
		x.Deleted += n
	}
}

// Total is the number of row changes over every table.
func (c Changes) Total() int {
	n := 0
	for _, x := range c {
		n += x.Inserted + x.Updated + x.Deleted
	}
	return n
}

// String renders the changes as "table +i ~u -d, ...", sorted by table.
func (c Changes) String() string {
	if len(c) == 0 {
		return "none"
	}
	ks := make([]string, 0, len(c))
	for k := range c {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	var b strings.Builder
	for i, k := range ks {
		if i > 0 {
			b.WriteString(", ")
		}
		x := c[k]
		fmt.Fprintf(&b, "%s +%d ~%d -%d", k, x.Inserted, x.Updated, x.Deleted)
	}
	return b.String()
}

const (
	opInsert byte = 'i'
	opUpdate byte = 'u'
	opDelete byte = 'd'
)

// kv is a column and its value. Values are int64, string, bool or nil (NULL).
type kv struct {
	k string
	v any
}

// wtx is a write transaction that counts its changes.
type wtx struct {
	tx      *sql.Tx
	changes Changes
	now     int64
	nowTime time.Time
}

func (w *wtx) exec(ctx context.Context, q string, args ...any) (int64, error) {
	res, err := w.tx.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("%w (%s)", err, firstLine(q))
	}
	return res.RowsAffected()
}

func firstLine(q string) string {
	q = strings.TrimSpace(q)
	if i := strings.IndexByte(q, '\n'); i > 0 {
		q = q[:i]
	}
	return q
}

// upsert makes the row keyed by key hold vals. It inserts the row (with insertOnly too) when missing, and updates
// only the columns that differ (setting onUpdate as well) when present. It returns the operation, 0 for none.
func (w *wtx) upsert(ctx context.Context, table string, key, vals, insertOnly, onUpdate []kv) (byte, error) {
	where, wargs := whereOf(key)
	cols := make([]string, len(vals))
	for i, c := range vals {
		cols[i] = c.k
	}
	var cur []any
	if len(vals) > 0 {
		cur = make([]any, len(vals))
		ptrs := make([]any, len(vals))
		for i := range cur {
			ptrs[i] = &cur[i]
		}
		err := w.tx.QueryRowContext(ctx, "SELECT "+strings.Join(cols, ", ")+" FROM "+table+" WHERE "+where, wargs...).
			Scan(ptrs...)
		if errors.Is(err, sql.ErrNoRows) {
			cur = nil
		} else if err != nil {
			return 0, fmt.Errorf("%s: %w", table, err)
		}
	} else {
		var one int
		err := w.tx.QueryRowContext(ctx, "SELECT 1 FROM "+table+" WHERE "+where, wargs...).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			cur = nil
		} else if err != nil {
			return 0, fmt.Errorf("%s: %w", table, err)
		} else {
			return 0, nil
		}
	}
	if cur == nil {
		all := append(append(append([]kv{}, key...), vals...), insertOnly...)
		names := make([]string, len(all))
		marks := make([]string, len(all))
		args := make([]any, len(all))
		for i, c := range all {
			names[i], marks[i], args[i] = c.k, "?", dbValue(c.v)
		}
		if _, err := w.exec(ctx, "INSERT INTO "+table+" ("+strings.Join(names, ", ")+") VALUES ("+
			strings.Join(marks, ", ")+")", args...); err != nil {
			return 0, fmt.Errorf("%s: %w", table, err)
		}
		w.changes.add(table, opInsert, 1)
		return opInsert, nil
	}
	var sets []string
	var args []any
	for i, c := range vals {
		if !same(cur[i], c.v) {
			sets = append(sets, c.k+" = ?")
			args = append(args, dbValue(c.v))
		}
	}
	if len(sets) == 0 {
		return 0, nil
	}
	for _, c := range onUpdate {
		sets = append(sets, c.k+" = ?")
		args = append(args, dbValue(c.v))
	}
	args = append(args, wargs...)
	if _, err := w.exec(ctx, "UPDATE "+table+" SET "+strings.Join(sets, ", ")+" WHERE "+where, args...); err != nil {
		return 0, fmt.Errorf("%s: %w", table, err)
	}
	w.changes.add(table, opUpdate, 1)
	return opUpdate, nil
}

func whereOf(key []kv) (string, []any) {
	parts := make([]string, len(key))
	args := make([]any, len(key))
	for i, c := range key {
		parts[i] = c.k + " = ?"
		args[i] = dbValue(c.v)
	}
	return strings.Join(parts, " AND "), args
}

// dbValue converts a Go value to what the driver stores.
func dbValue(v any) any {
	switch x := v.(type) {
	case bool:
		if x {
			return int64(1)
		}
		return int64(0)
	case int:
		return int64(x)
	case *int:
		if x == nil {
			return nil
		}
		return int64(*x)
	case *int64:
		if x == nil {
			return nil
		}
		return *x
	case *string:
		if x == nil {
			return nil
		}
		return *x
	}
	return v
}

// same compares a stored value with a Go value.
func same(stored, v any) bool {
	v = dbValue(v)
	if b, ok := stored.([]byte); ok {
		stored = string(b)
	}
	if stored == nil || v == nil {
		return stored == nil && v == nil
	}
	switch s := stored.(type) {
	case int64:
		n, ok := v.(int64)
		return ok && n == s
	case string:
		t, ok := v.(string)
		return ok && t == s
	case float64:
		switch n := v.(type) {
		case float64:
			return n == s
		case int64:
			return float64(n) == s
		}
	}
	return fmt.Sprint(stored) == fmt.Sprint(v)
}

// nullInt returns nil for 0, else the value: for optional numbers stored as NULL.
func nullInt(n int) any {
	if n == 0 {
		return nil
	}
	return int64(n)
}

// nullStr returns nil for "", else the string.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
