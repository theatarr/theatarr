package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Stored flags (migration 00144, docs/journal.md "Flags"). A History row (internal/store history.go) is written when
// its event happens, with flags NULL. The journal works out the flags of the row's entry once, from what the
// entry's sources say then, and stores them on the row (flags, severity); they are not worked out again unless the
// row is rewritten in place (its writer clears them, store historyUpsert). The
// flagged view and its count are a filter on history. Entries without a History row (searches, grabs that never
// reached a provider, link and relink actions, dry-run or waiting imports, operator actions) carry no flags.
//
// Fill runs once at serve start in the background (it flags every row an upgrade left unchecked) and, bounded, before
// each journal read, so a new row is flagged by the next read.

// uncheckedWhere is a row of a kind the checks look at, not checked yet (the partial index history_unchecked, migration
// 00144: the same words). Rows of other kinds keep flags NULL.
const uncheckedWhere = `flags IS NULL AND kind IN ('grab', 'upgrade', 'failure', 'stall', 'import', 'blocklist')`

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
// fill runs at a time; a call while another runs, or on a read-only journal, returns 0 at once. Nothing is written
// when no row waits; a batch is written in one transaction.
func (j *Journal) Fill(ctx context.Context, limit int) (int, error) {
	if j.ro || !j.fillMu.TryLock() {
		return 0, nil
	}
	defer j.fillMu.Unlock()
	type row struct {
		id, at, title int64
		ref, version  string
		kind          string
		flags         []Flag
		counted       bool
		refused       bool
	}
	// refused: a failure whose download's import the guard refused, with no History row of its own (no held or
	// import row: the download failed with it).
	rows, err := j.st.Reader().QueryContext(ctx, `SELECT id, ref, at, title_id, version_key, kind,
		kind IN ('failure', 'stall') AND EXISTS (SELECT 1 FROM library_import li WHERE li.download_id = history.download_id
			AND li.state = 'rejected') AND NOT EXISTS (SELECT 1 FROM history h WHERE h.download_id = history.download_id
			AND h.kind = 'import')
		FROM history WHERE `+uncheckedWhere+` ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return 0, fmt.Errorf("journal: flags: %w", err)
	}
	var todo []*row
	for rows.Next() {
		r := &row{}
		if err := rows.Scan(&r.id, &r.ref, &r.at, &r.title, &r.version, &r.kind, &r.refused); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(todo) == 0 {
		return 0, err
	}
	b := j.newBuilder(ctx)
	check := func(h head) (*Entry, error) {
		e, err := b.load(h)
		if e == nil || err != nil {
			return e, err
		}
		if err := b.check(e); err != nil {
			return nil, fmt.Errorf("journal: %s: checks: %w", h.id, err)
		}
		return e, nil
	}
	for _, r := range todo {
		r.flags = []Flag{}
		e, err := check(head{id: entryOfRef(r.ref), at: r.at, titleID: r.title, version: r.version, kind: r.kind})
		if err != nil {
			return 0, err
		}
		if e == nil {
			continue
		}
		r.flags = e.Flags
		// A failure or a blocklist entry without a download is one of the failures a run counts.
		r.counted = e.Kind == KindFailure || e.Kind == KindStall ||
			e.Kind == KindBlocklist && !slices.ContainsFunc(e.Related, func(s string) bool { return strings.HasPrefix(s, "dl:") })
		// The import a failed download's guard refused has no History row of its own: its flags go on the failure's.
		if dl, ok := strings.CutSuffix(r.ref, ":fail"); ok && r.refused {
			ie, err := check(head{id: dl + ":import", at: r.at, titleID: r.title, version: r.version, kind: KindImport})
			if err != nil {
				return 0, err
			}
			if ie != nil {
				r.flags = append(r.flags, ie.Flags...)
			}
		}
	}
	err = j.st.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, r := range todo {
			sev := ""
			for _, f := range r.flags {
				if SevRank(f.Severity) > SevRank(sev) {
					sev = f.Severity
				}
			}
			raw, _ := json.Marshal(r.flags)
			if _, err := tx.ExecContext(ctx, `UPDATE history SET flags = ?, severity = ? WHERE id = ?`, string(raw), sev,
				r.id); err != nil {
				return err
			}
			// An import's row takes the flags from its held row: one entry, one flagged row.
			if dl, ok := strings.CutSuffix(r.ref, ":import"); ok {
				if _, err := tx.ExecContext(ctx, `UPDATE history SET severity = '' WHERE ref = ?`, dl+":held"); err != nil {
					return err
				}
			}
			if r.counted {
				if err := clearFailures(ctx, tx, r.id, r.title, r.version, r.at); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("journal: flags: %w", err)
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

// clearFailures takes repeated_failures from the earlier failure rows of a title and library in the window before at:
// every counted failure does, flagged or not, so the latest failure of a run is the one that may carry the flag.
func clearFailures(ctx context.Context, tx *sql.Tx, id, title int64, version string, at int64) error {
	const rest = `FROM json_each(history.flags) WHERE json_extract(value, '$.code') <> '` + CheckFailures + `'`
	_, err := tx.ExecContext(ctx, `UPDATE history SET flags = (SELECT json_group_array(json(value)) `+rest+`),
		severity = COALESCE((SELECT CASE MAX(CASE json_extract(value, '$.severity') WHEN 'error' THEN 3 WHEN 'warn' THEN 2
			WHEN 'info' THEN 1 END) WHEN 3 THEN 'error' WHEN 2 THEN 'warn' WHEN 1 THEN 'info' END `+rest+`), '')
		WHERE id <> ? AND title_id = ? AND version_key = ? AND kind IN ('failure', 'stall', 'blocklist')
			AND at BETWEEN ? AND ? AND flags LIKE '%`+CheckFailures+`%'`, id, title, version, at-failureWindow, at)
	return err
}

// flagRefs are the History rows that may hold an entry's flags, first found first: its own; for an import, its held
// row, then its failed download's row (an import refused with the download).
func flagRefs(id string) []string {
	if dl, ok := strings.CutSuffix(id, ":import"); ok {
		return []string{id, dl + ":held", dl + ":fail"}
	}
	return []string{id}
}

// setFlags fills the entries' flags from their stored History rows, in one query.
func (b *builder) setFlags(es []*Entry) error {
	var refs []string
	for _, e := range es {
		refs = append(refs, flagRefs(e.ID)...)
	}
	if len(refs) == 0 {
		return nil
	}
	arg, _ := json.Marshal(refs)
	rows, err := b.db.QueryContext(b.ctx, `SELECT ref, flags FROM history WHERE ref IN (SELECT value FROM json_each(?))
		AND flags IS NOT NULL`, string(arg))
	if err != nil {
		return err
	}
	defer rows.Close()
	stored := map[string]string{}
	for rows.Next() {
		var ref, raw string
		if err := rows.Scan(&ref, &raw); err != nil {
			return err
		}
		stored[ref] = raw
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, e := range es {
		for _, ref := range flagRefs(e.ID) {
			if raw, ok := stored[ref]; ok {
				_ = json.Unmarshal([]byte(raw), &e.Flags)
				break
			}
		}
		if e.Flags == nil {
			e.Flags = []Flag{}
		}
		finish(e)
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
