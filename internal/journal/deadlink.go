package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/store"
)

// Dead-link flags (docs/journal.md "dead_link"). The library writes a library_action row of kind dead every time a
// sweep finds a link dead: a gated version's sweep records the same dead link as a dry run on every scan. The flag
// is one per finding, not one per scan. A finding is one unbroken run of dead actions of a link: its first dead
// action carries the flag, and the repeats after it do not (an open flag already says it; an acknowledged one stays
// acknowledged). Only the action log breaks a run: a recovery action at the link's path (recoverySQL) or a dead
// action with another target starts a new finding with its own flag. A recovery that left no library action cannot
// start a new finding. Nothing about the sweep changes: it still records every scan and repairs or searches as
// before.

// deadModes are the library_action modes whose dead rows are flagged.
const deadModes = `('applied', 'dry_run')`

// recoverySQL is a library_action that shows a link working again at its path: a link, relink or replace that was
// applied, or a gated sweep's revive (its file is on the mount again).
const recoverySQL = `((mode = 'applied' AND kind IN ('link', 'relink', 'replace'))
	OR (mode = 'dry_run' AND kind = 'relink' AND detail LIKE 'revive:%'))`

// deadRow is one dead action.
type deadRow struct {
	id, link              int64
	target, path, version string
}

// deadRun is one finding: its first (flagged) and latest dead action.
type deadRun struct{ first, last deadRow }

// startsFinding says whether cur starts a new finding after prev, the link's previous dead action: none before,
// another target, or a recovery at the path after prev (recovered: the latest one's id).
func startsFinding(prev *deadRow, cur deadRow, recovered int64) bool {
	return prev == nil || prev.target != cur.target || recovered > prev.id
}

// deadRuns walks every dead and recovery action once, in order, and returns the findings and, per path, the id of
// its latest recovery.
func (j *Journal) deadRuns(ctx context.Context, db *sql.DB) ([]*deadRun, map[string]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, kind, COALESCE(link_id, 0), COALESCE(path, ''), COALESCE(target, ''),
		COALESCE(version_key, '') FROM library_action WHERE (kind = 'dead' AND mode IN `+deadModes+`) OR `+recoverySQL+`
		ORDER BY id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var runs []*deadRun
	open := map[int64]*deadRun{} // link → its latest finding
	recovered := map[string]int64{}
	for rows.Next() {
		var r deadRow
		var kind string
		if err := rows.Scan(&r.id, &kind, &r.link, &r.path, &r.target, &r.version); err != nil {
			return nil, nil, err
		}
		if kind != "dead" {
			recovered[r.path] = r.id
			continue
		}
		run := open[r.link]
		var prev *deadRow
		if run != nil {
			prev = &run.last
		}
		if startsFinding(prev, r, recovered[r.path]) {
			run = &deadRun{first: r}
			runs = append(runs, run)
			open[r.link] = run
		}
		run.last = r
	}
	return runs, recovered, rows.Err()
}

// deadFirsts are the library_action ids of every finding's first action.
func (j *Journal) deadFirsts(ctx context.Context, db *sql.DB) (map[int64]bool, error) {
	runs, _, err := j.deadRuns(ctx, db)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]bool, len(runs))
	for _, r := range runs {
		out[r.first.id] = true
	}
	return out, nil
}

// firstDead says whether a dead action starts its finding. A listing reads every finding once (b.firsts); a single
// entry seeks back by rowid to the link's previous dead action and checks only what lies between.
func (b *builder) firstDead(e *Entry) (bool, error) {
	id, err := strconv.ParseInt(strings.TrimPrefix(e.ID, "lib:"), 10, 64)
	if err != nil {
		return false, nil
	}
	if b.firsts != nil {
		return b.firsts[id], nil
	}
	if e.Import == nil || len(e.Import.Links) == 0 {
		return false, nil
	}
	lk := e.Import.Links[0]
	cur := deadRow{id: id, link: lk.ID, target: lk.Target, path: lk.Path}
	var prev deadRow
	err = b.db.QueryRowContext(b.ctx, `SELECT id, COALESCE(target, '') FROM library_action WHERE id < ?
		AND kind = 'dead' AND mode IN `+deadModes+` AND link_id = ? ORDER BY id DESC LIMIT 1`, id, lk.ID).
		Scan(&prev.id, &prev.target)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	var recovered sql.NullInt64
	if err := b.db.QueryRowContext(b.ctx, `SELECT MAX(id) FROM library_action WHERE id > ? AND id < ? AND path = ?
		AND `+recoverySQL, prev.id, id, lk.Path).Scan(&recovered); err != nil {
		return false, err
	}
	return startsFinding(&prev, cur, recovered.Int64), nil
}

// Why an open dead-link flag is settled (ResolvedDeadLinks).
const (
	ResolvedRepaired   = "the link was repaired"
	ResolvedSuperseded = "a newer flag for the same link exists"
)

// ResolvedLink is an open dead-link flag whose finding is settled.
type ResolvedLink struct {
	Entry   string `json:"entry"`
	Why     string `json:"why"`
	titleID int64
	version string
}

// ResolvedDeadLinks lists the open (unjudged) dead-link flags the action log proves settled:
//   - superseded: a newer finding of the same link has its own open flag;
//   - repaired: a recovery action at the path is newer than the finding's latest dead action, and the link is not
//     marked dead and not in the latest dangling-link report now.
//
// Anything else is not listed: nothing is inferred from a link row alone or from a report that does not list it.
func (j *Journal) ResolvedDeadLinks(ctx context.Context) ([]ResolvedLink, error) {
	if !j.hasOwnTables(ctx) {
		return []ResolvedLink{}, nil
	}
	db := j.st.Reader()
	runs, recovered, err := j.deadRuns(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("journal: dead links: %w", err)
	}
	judged := map[string]bool{}
	if err := scanStrings(ctx, db, `SELECT entry_id FROM journal_verdict WHERE entry_id LIKE 'lib:%'`, nil,
		func(s string) { judged[s] = true }); err != nil {
		return nil, err
	}
	dangling := map[string]bool{}
	if err := scanStrings(ctx, db, `SELECT path FROM library_dangling`, nil,
		func(s string) { dangling[s] = true }); err != nil {
		return nil, err
	}
	type linkRow struct {
		state string
		title int64
	}
	links := map[int64]linkRow{}
	ids := make([]int64, 0, len(runs))
	for _, r := range runs {
		ids = append(ids, r.first.link)
	}
	idsJSON, _ := json.Marshal(ids)
	lr, err := db.QueryContext(ctx, `SELECT id, state, title_id FROM link WHERE id IN (SELECT value FROM json_each(?))`,
		string(idsJSON))
	if err != nil {
		return nil, err
	}
	defer lr.Close()
	for lr.Next() {
		var id int64
		var l linkRow
		if err := lr.Scan(&id, &l.state, &l.title); err != nil {
			return nil, err
		}
		links[id] = l
	}
	if err := lr.Err(); err != nil {
		return nil, err
	}
	entry := func(r *deadRun) string { return "lib:" + strconv.FormatInt(r.first.id, 10) }
	newestOpen := map[int64]int64{} // link → its newest open finding's first action
	for _, r := range runs {
		if !judged[entry(r)] {
			newestOpen[r.first.link] = max(newestOpen[r.first.link], r.first.id)
		}
	}
	out := []ResolvedLink{}
	for _, r := range runs {
		if judged[entry(r)] {
			continue
		}
		l, ok := links[r.first.link]
		why := ""
		switch {
		case newestOpen[r.first.link] > r.first.id:
			why = ResolvedSuperseded
		case ok && l.state != "dead" && !dangling[r.first.path] && recovered[r.first.path] > r.last.id:
			why = ResolvedRepaired
		}
		if why != "" {
			out = append(out, ResolvedLink{Entry: entry(r), Why: why, titleID: l.title, version: r.first.version})
		}
	}
	return out, nil
}

// scanStrings runs a one-column query and hands each value to fn.
func scanStrings(ctx context.Context, db *sql.DB, q string, args []any, fn func(string)) error {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return err
		}
		fn(s)
	}
	return rows.Err()
}

// AcknowledgeResolved records an "ok" verdict on every flag ResolvedDeadLinks lists, acknowledging its dead_link
// flag. The entries and their actions stay; nothing is blocklisted, searched or
// changed in the library. It returns how many it acknowledged.
func (j *Journal) AcknowledgeResolved(ctx context.Context, by string) (int, error) {
	if !j.hasOwnTables(ctx) {
		return 0, ErrNotMigrated
	}
	list, err := j.ResolvedDeadLinks(ctx)
	if err != nil {
		return 0, err
	}
	if len(list) == 0 {
		return 0, nil
	}
	at := store.Millis(j.now().UTC())
	n := 0
	err = j.st.WriteTx(ctx, func(tx *sql.Tx) error {
		n = 0
		for _, r := range list {
			var title any
			if r.titleID > 0 {
				title = r.titleID
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO journal_verdict (entry_id, verdict, reason, acked, title_id,
				version_key, info_hash, action, action_note, by, created_at)
				SELECT ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM journal_verdict WHERE entry_id = ?)`,
				r.Entry, VerdictOK, "Acknowledged in bulk: "+r.Why, `["`+CheckDeadLink+`"]`,
				title, r.version, ActionRecorded, "", by, at, r.Entry)
			if err != nil {
				return err
			}
			k, _ := res.RowsAffected()
			n += int(k)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("journal: acknowledge: %w", err)
	}
	j.log.Info("journal: resolved dead-link flags acknowledged", "count", n, "by", by)
	return n, nil
}
