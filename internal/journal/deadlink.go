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
// is one per finding, not one per scan. A finding is one unbroken run of dead observations of a link: its first
// dead action carries the flag, and the repeats after it do not (an open flag already says it; an acknowledged one
// stays acknowledged). A new finding, with its own flag, starts at the first dead action after the link was seen
// working again: the link pointed somewhere else (another target), a library action repaired or revived it
// (recoverySQL), or a bulk acknowledgement settled the previous finding because the link was working
// (AcknowledgeResolved). Nothing about the sweep changes: it still records every scan and repairs or searches as
// before.

// deadModes are the library_action modes whose dead rows are flagged.
const deadModes = `('applied', 'dry_run')`

// recoverySQL is a library_action that shows a link working again at its path: a link, relink or replace that was
// applied, or a gated sweep's revive (its file is on the mount again).
const recoverySQL = `((mode = 'applied' AND kind IN ('link', 'relink', 'replace'))
	OR (mode = 'dry_run' AND kind = 'relink' AND detail LIKE 'revive:%'))`

// settledNote marks a bulk acknowledgement's verdict (journal_verdict.action_note): the link was seen working
// then, so a later dead action of it starts a new finding.
const settledNote = "settled: "

// deadRow is one dead action.
type deadRow struct {
	id, at, link int64
	target, path string
}

// deadRun is one finding: its first (flagged) and latest dead action.
type deadRun struct{ first, last deadRow }

// startsFinding says whether cur starts a new finding after prev, the link's previous dead action: none before,
// another target, a recovery at the path after prev (recovered: the latest one's id), or a settled acknowledgement
// of the link between the two.
func startsFinding(prev *deadRow, cur deadRow, recovered int64, settled []int64) bool {
	if prev == nil || prev.target != cur.target || recovered > prev.id {
		return true
	}
	for _, t := range settled {
		if t > prev.at && t <= cur.at {
			return true
		}
	}
	return false
}

// settledTimes are the times of the settled acknowledgements, by link.
func settledTimes(ctx context.Context, db *sql.DB, link int64) (map[int64][]int64, error) {
	q := `SELECT a.link_id, jv.created_at FROM journal_verdict jv JOIN library_action a
		ON a.id = CAST(substr(jv.entry_id, 5) AS INTEGER) WHERE jv.entry_id LIKE 'lib:%' AND jv.action_note LIKE ?`
	args := []any{settledNote + "%"}
	if link != 0 {
		q += ` AND a.link_id = ?`
		args = append(args, link)
	}
	out := map[int64][]int64{}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var l, at int64
		if err := rows.Scan(&l, &at); err != nil {
			return nil, err
		}
		out[l] = append(out[l], at)
	}
	return out, rows.Err()
}

// deadRuns walks every dead and recovery action once, in order, and returns the findings and, per path, the id of
// its latest recovery.
func (j *Journal) deadRuns(ctx context.Context, db *sql.DB) ([]*deadRun, map[string]int64, error) {
	settled := map[int64][]int64{}
	if j.hasOwnTables(ctx) {
		var err error
		if settled, err = settledTimes(ctx, db, 0); err != nil {
			return nil, nil, err
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT id, at, kind, COALESCE(link_id, 0), COALESCE(path, ''),
		COALESCE(target, '') FROM library_action WHERE (kind = 'dead' AND mode IN `+deadModes+`) OR `+recoverySQL+`
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
		if err := rows.Scan(&r.id, &r.at, &kind, &r.link, &r.path, &r.target); err != nil {
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
		if startsFinding(prev, r, recovered[r.path], settled[r.link]) {
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
	cur := deadRow{id: id, at: store.Millis(e.At), link: lk.ID, target: lk.Target, path: lk.Path}
	var prev deadRow
	err = b.db.QueryRowContext(b.ctx, `SELECT id, at, COALESCE(target, '') FROM library_action WHERE id < ?
		AND kind = 'dead' AND mode IN `+deadModes+` AND link_id = ? ORDER BY id DESC LIMIT 1`, id, lk.ID).
		Scan(&prev.id, &prev.at, &prev.target)
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
	settled := map[int64][]int64{}
	if b.own {
		if settled, err = settledTimes(b.ctx, b.db, lk.ID); err != nil {
			return false, err
		}
	}
	return startsFinding(&prev, cur, recovered.Int64, settled[lk.ID]), nil
}

// Why an open dead-link flag is settled (ResolvedDeadLinks).
const (
	ResolvedGone       = "the link is gone"
	ResolvedRepaired   = "the link was repaired"
	ResolvedWorks      = "the link works again"
	ResolvedSuperseded = "a newer flag for the same link exists"
)

// ResolvedLink is an open dead-link flag whose finding is settled.
type ResolvedLink struct {
	Entry   string `json:"entry"`
	Why     string `json:"why"`
	titleID int64
	version string
}

// ResolvedDeadLinks lists the open (unjudged) dead-link flags whose finding is settled:
//   - superseded: a later finding of the same link has its own flag;
//   - otherwise only while the link is not marked dead and its path is not in the latest dangling-link report, and
//     then: gone (no link row, or unlinked); repaired (a recovery action at its path after the finding's latest dead
//     action); works again (the link is active and the latest report, taken after that dead action, walked its
//     root without an error).
//
// Anything else is a current problem and is not listed.
func (j *Journal) ResolvedDeadLinks(ctx context.Context) ([]ResolvedLink, error) {
	if !j.hasOwnTables(ctx) {
		return []ResolvedLink{}, nil
	}
	db := j.st.Reader()
	var scanAt int64
	var scanRoots string
	var scanErr sql.NullString
	err := db.QueryRowContext(ctx, `SELECT at, roots, error FROM library_dangling_scan ORDER BY id DESC LIMIT 1`).
		Scan(&scanAt, &scanRoots, &scanErr)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("journal: dangling report: %w", err)
	}
	var roots []string
	_ = json.Unmarshal([]byte(scanRoots), &roots)
	walked := func(path string) bool {
		for _, r := range roots {
			r = strings.TrimRight(r, "/")
			if strings.HasPrefix(path, r+"/") && !strings.Contains(scanErr.String, r+": ") {
				return true
			}
		}
		return false
	}
	runs, recovered, err := j.deadRuns(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("journal: dead links: %w", err)
	}
	newest := map[int64]int64{} // link → its newest finding's first action
	for _, r := range runs {
		newest[r.first.link] = max(newest[r.first.link], r.first.id)
	}
	out := []ResolvedLink{}
	for _, r := range runs {
		var judged, dangling bool
		var state, title, version sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM journal_verdict WHERE entry_id = ?),
			EXISTS (SELECT 1 FROM library_dangling WHERE path = ?),
			(SELECT state FROM link WHERE id = ?), (SELECT title_id FROM link WHERE id = ?),
			(SELECT version_key FROM library_action WHERE id = ?)`, "lib:"+strconv.FormatInt(r.first.id, 10),
			r.first.path, r.first.link, r.first.link, r.first.id).Scan(&judged, &dangling, &state, &title,
			&version); err != nil {
			return nil, err
		}
		if judged {
			continue
		}
		why := ""
		switch {
		case newest[r.first.link] > r.first.id:
			why = ResolvedSuperseded
		case dangling || state.String == "dead":
		case !state.Valid || state.String == "unlinked":
			why = ResolvedGone
		case recovered[r.first.path] > r.last.id:
			why = ResolvedRepaired
		case state.String == "active" && scanAt > r.last.at && walked(r.first.path):
			why = ResolvedWorks
		}
		if why != "" {
			t, _ := strconv.ParseInt(title.String, 10, 64)
			out = append(out, ResolvedLink{Entry: "lib:" + strconv.FormatInt(r.first.id, 10), Why: why, titleID: t,
				version: version.String})
		}
	}
	return out, nil
}

// note is a bulk verdict's action_note: settled when the link was seen working (or gone), so its next dead action
// starts a new finding; a superseded flag's is not, since the link's newer finding is still open.
func note(why string) string {
	if why == ResolvedSuperseded {
		return "superseded by a newer flag"
	}
	return settledNote + why
}

// AcknowledgeResolved records an "ok" verdict on every flag ResolvedDeadLinks lists, acknowledging its dead_link
// flag (action_note: note). The entries and their actions stay; nothing is blocklisted, searched or
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
				title, r.version, ActionRecorded, note(r.Why), by, at, r.Entry)
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
