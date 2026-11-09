package jobs

import (
	"context"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// KindBacklog is the queue of one kind right now: what is ready and waiting for a slot, how long the oldest ready
// job has waited, what is scheduled later, what runs, and what failed recently. Queue age is measured from run_at:
// a job scheduled for later is not late until its time has come.
type KindBacklog struct {
	Kind    string `json:"kind"`
	Ready   int    `json:"ready"`
	Later   int    `json:"later"`
	Running int    `json:"running"`
	// OldestReady is how long the oldest ready job has waited past its run_at; 0 when none is ready.
	OldestReady time.Duration `json:"-"`
	// OldestReadyS is OldestReady in whole seconds, for JSON.
	OldestReadyS int64 `json:"oldest_ready_s"`
	// Failed is the jobs of the kind that failed for good in the window Backlog was given.
	Failed int `json:"failed"`
}

// Backlog returns every kind with queued, running or recently failed jobs, sorted by kind. failedSince bounds the
// failed count (finished_at at or after it). Two grouped reads on the job_ready index; cheap enough for a health
// check every minute.
func (q *Queue) Backlog(ctx context.Context, failedSince time.Time) ([]KindBacklog, error) {
	now := store.Millis(q.now())
	rows, err := q.st.Reader().QueryContext(ctx, `
		SELECT kind,
		       SUM(state = 'queued' AND run_at <= ?1),
		       SUM(state = 'queued' AND run_at > ?1),
		       SUM(state = 'running'),
		       MIN(CASE WHEN state = 'queued' AND run_at <= ?1 THEN run_at END),
		       0
		FROM job WHERE state IN ('queued', 'running') GROUP BY kind
		UNION ALL
		SELECT kind, 0, 0, 0, NULL, COUNT(*) FROM job WHERE state = 'failed' AND finished_at >= ?2 GROUP BY kind
		ORDER BY 1`, now, store.Millis(failedSince))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KindBacklog
	for rows.Next() {
		var k KindBacklog
		var oldest *int64
		if err := rows.Scan(&k.Kind, &k.Ready, &k.Later, &k.Running, &oldest, &k.Failed); err != nil {
			return nil, err
		}
		if oldest != nil && *oldest <= now {
			k.OldestReady = time.Duration(now-*oldest) * time.Millisecond
			k.OldestReadyS = int64(k.OldestReady / time.Second)
		}
		if n := len(out); n > 0 && out[n-1].Kind == k.Kind {
			p := &out[n-1]
			p.Ready, p.Later, p.Running, p.Failed = p.Ready+k.Ready, p.Later+k.Later, p.Running+k.Running, p.Failed+k.Failed
			p.OldestReady, p.OldestReadyS = max(p.OldestReady, k.OldestReady), max(p.OldestReadyS, k.OldestReadyS)
			continue
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
