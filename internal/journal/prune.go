package journal

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/theatarr/theatarr/internal/journal/snapshot"
	"github.com/theatarr/theatarr/internal/store"
)

// Retention of the journal's own rows. The entries themselves are read from their owners' tables and follow their
// owners' retention (none of them is pruned except shadow_run and shadow_rss_seen, which the journal does not
// read).
//
//   - journal_decision: in full for DecisionFull (180 days). After that a decision from which nothing was routed
//     (no download and no plan from it) is deleted, and the others keep the chosen release and one runner-up
//     (trimmed = 1) for good. A full row is ~3-6 KB (8 accepted and 4 rejected candidates), a trimmed one ~1 KB: at
//     the wanted loop's ceiling (10 searches an hour) that is at most ~45 MB for the full window and ~9 MB a year
//     after; at the expected pace (tens of searches a day) well under 10 MB in all.
//   - journal_verdict: kept for good (a few hundred bytes each, written by hand).
const DecisionFull = 180 * 24 * time.Hour

// PruneSummary is what Prune did.
type PruneSummary struct {
	Deleted int64 `json:"deleted"`
	Trimmed int64 `json:"trimmed"`
}

// Prune applies the retention. It runs in theatarr serve's nightly job.
func Prune(ctx context.Context, st *store.Store, now time.Time) (PruneSummary, error) {
	var sum PruneSummary
	cutoff := store.Millis(now.Add(-DecisionFull))
	res, err := st.Writer().ExecContext(ctx, `DELETE FROM journal_decision WHERE created_at < ? AND NOT EXISTS (
			SELECT 1 FROM download d WHERE d.source = journal_decision.source AND d.title_id = journal_decision.title_id
			AND d.version_key = journal_decision.version_key AND d.created_at >= journal_decision.created_at)
		AND NOT EXISTS (SELECT 1 FROM router_plan p WHERE p.source = journal_decision.source
			AND p.title_id = journal_decision.title_id AND p.version_key = journal_decision.version_key
			AND p.created_at >= journal_decision.created_at)`, cutoff)
	if err != nil {
		return sum, fmt.Errorf("journal: prune: %w", err)
	}
	sum.Deleted, _ = res.RowsAffected()
	for {
		n, err := trimBatch(ctx, st, cutoff)
		if err != nil {
			return sum, err
		}
		sum.Trimmed += n
		if n < 200 {
			return sum, nil
		}
	}
}

func trimBatch(ctx context.Context, st *store.Store, cutoff int64) (int64, error) {
	type row struct {
		id    int64
		cands string
	}
	rows, err := st.Writer().QueryContext(ctx, `SELECT id, candidates FROM journal_decision WHERE trimmed = 0
		AND created_at < ? ORDER BY id LIMIT 200`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("journal: prune: %w", err)
	}
	var todo []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.cands); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, r := range todo {
		var cs []snapshot.Candidate
		_ = json.Unmarshal([]byte(r.cands), &cs)
		if len(cs) > 2 {
			cs = cs[:2]
		}
		b, err := json.Marshal(cs)
		if err != nil {
			return 0, err
		}
		if _, err := st.Writer().ExecContext(ctx, `UPDATE journal_decision SET candidates = ?, trimmed = 1 WHERE id = ?`,
			string(b), r.id); err != nil {
			return 0, fmt.Errorf("journal: prune: %w", err)
		}
	}
	return int64(len(todo)), nil
}
