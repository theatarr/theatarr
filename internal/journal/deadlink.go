package journal

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/theatarr/theatarr/internal/store"
)

// Dead-link flags (docs/journal.md "dead_link"). The library writes a library_action row of kind dead every time a
// sweep finds a link dead: a gated version's sweep records the same dead link as a dry run on every scan. The flag
// is one per finding, not one per scan: a finding is a link, its target and the action's mode, and only its first
// dead action carries the flag. A later dead action for the same link and target in the same mode is a repeat and
// is not flagged (an open flag for the link already says it; an acknowledged one stays acknowledged). Nothing about
// the sweep changes: it still records every scan and repairs or searches as before.

// deadModes are the library_action modes whose dead rows are flagged.
const deadModes = `('applied', 'dry_run')`

// firstDeadSQL selects the first dead action of every finding (link, target, mode), as journal ids.
const firstDeadSQL = `SELECT 'lib:' || MIN(id) FROM library_action WHERE kind = 'dead' AND mode IN ` + deadModes + `
	GROUP BY link_id, COALESCE(target, ''), mode`

// firstDead says whether the library_action id is the first dead action of its finding. The builder reads every
// finding's first action once.
func (b *builder) firstDead(entryID string) (bool, error) {
	if b.firstDeads == nil {
		b.firstDeads = map[string]bool{}
		rows, err := b.db.QueryContext(b.ctx, firstDeadSQL)
		if err != nil {
			return false, err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return false, err
			}
			b.firstDeads[id] = true
		}
		if err := rows.Err(); err != nil {
			return false, err
		}
	}
	return b.firstDeads[entryID], nil
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

// ResolvedDeadLinks lists the open (unjudged) dead-link flags that are resolved or superseded:
//   - gone: the link row no longer exists or was unlinked;
//   - repaired: the link points somewhere else now;
//   - works again: the link is active with the same target and the latest dangling-link report, taken after the
//     flag, does not list it;
//   - superseded: a later finding (another target or mode) for the same link has its own flag.
//
// A link still marked dead with the same target, or still listed as dangling, is a current problem and is not
// listed. Flags on anything other than dead-link actions are never listed.
func (j *Journal) ResolvedDeadLinks(ctx context.Context) ([]ResolvedLink, error) {
	if !j.hasOwnTables(ctx) {
		return []ResolvedLink{}, nil
	}
	db := j.st.Reader()
	var reportAt sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MAX(at) FROM library_dangling_scan`).Scan(&reportAt); err != nil {
		return nil, fmt.Errorf("journal: dangling report: %w", err)
	}
	rows, err := db.QueryContext(ctx, `SELECT a.id, a.at, COALESCE(a.link_id, 0), COALESCE(a.target, ''), a.mode,
		COALESCE(a.version_key, ''), l.id IS NOT NULL, COALESCE(l.state, ''), COALESCE(l.target, ''),
		COALESCE(l.title_id, 0), EXISTS (SELECT 1 FROM library_dangling d WHERE d.path = a.path),
		EXISTS (SELECT 1 FROM journal_verdict jv WHERE jv.entry_id = 'lib:' || a.id)
		FROM library_action a LEFT JOIN link l ON l.id = a.link_id
		WHERE 'lib:' || a.id IN (`+firstDeadSQL+`) ORDER BY a.id`)
	if err != nil {
		return nil, fmt.Errorf("journal: dead links: %w", err)
	}
	defer rows.Close()
	type finding struct {
		id, at, link, title      int64
		target, mode, version    string
		exists, dangling, judged bool
		linkState, linkTarget    string
	}
	var all []finding
	newest := map[int64]int64{} // link id → its newest finding's action id
	for rows.Next() {
		var f finding
		if err := rows.Scan(&f.id, &f.at, &f.link, &f.target, &f.mode, &f.version, &f.exists, &f.linkState,
			&f.linkTarget, &f.title, &f.dangling, &f.judged); err != nil {
			return nil, err
		}
		all = append(all, f)
		newest[f.link] = max(newest[f.link], f.id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []ResolvedLink{}
	for _, f := range all {
		if f.judged {
			continue
		}
		why := ""
		switch {
		case !f.exists || f.linkState == "unlinked":
			why = ResolvedGone
		case newest[f.link] > f.id:
			why = ResolvedSuperseded
		case f.linkTarget != f.target:
			why = ResolvedRepaired
		case f.linkState == "active" && !f.dangling && reportAt.Valid && reportAt.Int64 > f.at:
			why = ResolvedWorks
		}
		if why != "" {
			out = append(out, ResolvedLink{Entry: "lib:" + strconv.FormatInt(f.id, 10), Why: why, titleID: f.title,
				version: f.version})
		}
	}
	return out, nil
}

// AcknowledgeResolved records an "ok" verdict on every flag ResolvedDeadLinks lists, acknowledging its dead_link
// flag. The entries and their actions stay; nothing is blocklisted, searched or changed in the library. It returns
// how many it acknowledged.
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
				SELECT ?, ?, ?, ?, ?, ?, '', ?, '', ?, ? WHERE NOT EXISTS (SELECT 1 FROM journal_verdict WHERE entry_id = ?)`,
				r.Entry, VerdictOK, "Acknowledged in bulk: "+r.Why, `["`+CheckDeadLink+`"]`,
				title, r.version, ActionRecorded, by, at, r.Entry)
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
