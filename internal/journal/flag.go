package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Stored flags (migration 00144, docs/journal.md "Flags"). A History row (internal/store history.go) is written when
// its event happens, with flags NULL. The journal works out the flags of the row's entry once, from what the
// entry's sources say then, and stores them on the row (flags, severity); they are never worked out again. The
// flagged view and its count are a filter on history. Entries without a History row (searches, grabs that never
// reached a provider, link and relink actions, dry-run or waiting imports, operator actions) carry no flags.
//
// Fill runs once at serve start in the background (it flags every row an upgrade left unchecked) and, bounded, before
// each journal read, so a new row is flagged by the next read.

// checkedKinds are the History kinds whose entries the checks look at; other rows are stored with no flags.
var checkedKinds = []string{KindGrab, KindUpgrade, KindFailure, KindStall, KindImport, KindBlocklist}

// entryOfRef is a History row's journal entry id: its ref, a held import's import entry.
func entryOfRef(ref string) string {
	if dl, ok := strings.CutSuffix(ref, ":held"); ok {
		return dl + ":import"
	}
	return ref
}

// entryOfRefSQL is entryOfRef in SQL (column ref).
const entryOfRefSQL = `CASE WHEN ref LIKE '%:held' THEN substr(ref, 1, length(ref) - 5) || ':import' ELSE ref END`

// flaggedWhere keeps rows with a flag of at least warn.
const flaggedWhere = `severity IN ('warn', 'error')`

// fillBatch is how many rows one bounded fill checks.
const fillBatch = 50

// Fill flags up to limit History rows not checked yet, oldest first, and returns how many it checked. Only one
// fill runs at a time; a call while another runs, or on a read-only journal, returns 0 at once.
func (j *Journal) Fill(ctx context.Context, limit int) (int, error) {
	if j.ro || !j.fillMu.TryLock() {
		return 0, nil
	}
	defer j.fillMu.Unlock()
	// Rows the checks do not look at are stored with no flags in one statement.
	if _, err := j.st.Writer().ExecContext(ctx, `UPDATE history SET flags = '[]', severity = '' WHERE flags IS NULL
		AND kind NOT IN (`+quoteList(checkedKinds)+`)`); err != nil {
		return 0, fmt.Errorf("journal: flags: %w", err)
	}
	type row struct {
		id, at, title int64
		ref, version  string
		kind          string
	}
	rows, err := j.st.Reader().QueryContext(ctx, `SELECT id, ref, at, title_id, version_key, kind FROM history
		WHERE flags IS NULL ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return 0, fmt.Errorf("journal: flags: %w", err)
	}
	var todo []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.ref, &r.at, &r.title, &r.version, &r.kind); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	b := j.newBuilder(ctx)
	for _, r := range todo {
		flags := []Flag{}
		e, err := b.load(head{id: entryOfRef(r.ref), at: r.at, titleID: r.title, version: r.version, kind: r.kind})
		if err != nil {
			return 0, err
		}
		if e != nil {
			if err := b.check(e); err != nil {
				return 0, fmt.Errorf("journal: %s: checks: %w", r.ref, err)
			}
			flags = e.Flags
		}
		// A failure or a blocklist entry without a download is one of the failures a run counts.
		counted := e != nil && (e.Kind == KindFailure || e.Kind == KindStall ||
			e.Kind == KindBlocklist && !slices.ContainsFunc(e.Related, func(s string) bool { return strings.HasPrefix(s, "dl:") }))
		if err := j.storeFlags(ctx, r.id, r.ref, r.title, r.version, r.at, flags, counted); err != nil {
			return 0, err
		}
	}
	return len(todo), nil
}

// FillAll flags every History row not checked yet (the start pass).
func (j *Journal) FillAll(ctx context.Context) (int, error) {
	total := 0
	for {
		n, err := j.Fill(ctx, 500)
		total += n
		if err != nil || n < 500 {
			return total, err
		}
	}
}

// storeFlags stores one row's flags. An import's row takes the flags from its held row (one entry, one flagged
// row), and a repeated failure that counts as one (counted) takes the flag from the earlier failures of the title and
// library in its window.
func (j *Journal) storeFlags(ctx context.Context, id int64, ref string, title int64, version string, at int64,
	flags []Flag, counted bool) error {
	sev := ""
	for _, f := range flags {
		if SevRank(f.Severity) > SevRank(sev) {
			sev = f.Severity
		}
	}
	raw, _ := json.Marshal(flags)
	return j.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE history SET flags = ?, severity = ? WHERE id = ?`, string(raw), sev,
			id); err != nil {
			return err
		}
		if dl, ok := strings.CutSuffix(ref, ":import"); ok {
			if _, err := tx.ExecContext(ctx, `UPDATE history SET severity = '' WHERE ref = ?`, dl+":held"); err != nil {
				return err
			}
		}
		if counted && slices.ContainsFunc(flags, func(f Flag) bool { return f.Code == CheckFailures }) {
			return clearFailures(ctx, tx, id, title, version, at)
		}
		return nil
	})
}

// clearFailures takes repeated_failures from the earlier failure rows of a title and library in the window before at:
// the latest failure of a run carries the flag.
func clearFailures(ctx context.Context, tx *sql.Tx, id, title int64, version string, at int64) error {
	const rest = `FROM json_each(history.flags) WHERE json_extract(value, '$.code') <> '` + CheckFailures + `'`
	_, err := tx.ExecContext(ctx, `UPDATE history SET flags = (SELECT json_group_array(json(value)) `+rest+`),
		severity = COALESCE((SELECT CASE MAX(CASE json_extract(value, '$.severity') WHEN 'error' THEN 3 WHEN 'warn' THEN 2
			WHEN 'info' THEN 1 END) WHEN 3 THEN 'error' WHEN 2 THEN 'warn' WHEN 1 THEN 'info' END `+rest+`), '')
		WHERE id <> ? AND title_id = ? AND version_key = ? AND kind IN ('failure', 'stall', 'blocklist')
			AND at BETWEEN ? AND ? AND flags LIKE '%`+CheckFailures+`%'`, id, title, version, at-failureWindow, at)
	return err
}

// storedFlags reads an entry's flags from its History row (a held import's row when the import has none).
func (b *builder) storedFlags(e *Entry) error {
	refs := []any{e.ID, e.ID}
	if dl, ok := strings.CutSuffix(e.ID, ":import"); ok {
		refs[1] = dl + ":held"
	}
	var raw sql.NullString
	err := b.db.QueryRowContext(b.ctx, `SELECT flags FROM history WHERE ref IN (?, ?) AND flags IS NOT NULL
		ORDER BY ref = ? DESC LIMIT 1`, refs[0], refs[1], e.ID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_ = json.Unmarshal([]byte(raw.String), &e.Flags)
	if e.Flags == nil {
		e.Flags = []Flag{}
	}
	return nil
}

// FlaggedCount counts the flagged entries (a flag of warn or error): the badge.
func (j *Journal) FlaggedCount(ctx context.Context) (int, error) {
	if _, err := j.Fill(ctx, fillBatch); err != nil {
		return 0, err
	}
	var n int
	err := j.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM history WHERE `+flaggedWhere).Scan(&n)
	return n, err
}

func quoteList(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = "'" + s + "'"
	}
	return strings.Join(q, ", ")
}
