package ceiling

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

const JobKind = "ceiling.compute"

type Payload struct {
	TitleID int64 `json:"title_id,omitempty"`
}

// RegisterJobs computes one title or a small batch of new/due titles. No outbound work is queued.
func RegisterJobs(w *jobs.Worker, st *store.Store, pol *policy.Policy) {
	w.Handle(JobKind, func(ctx context.Context, j *jobs.Job) error {
		var p Payload
		if err := j.Decode(&p); err != nil {
			return jobs.Permanent(err)
		}
		now := time.Now()
		ids := []int64{}
		if p.TitleID > 0 {
			ids = append(ids, p.TitleID)
		} else {
			rows, err := st.Reader().QueryContext(ctx, `SELECT t.id FROM title t LEFT JOIN source_ceiling c ON c.title_id=t.id
    WHERE EXISTS(SELECT 1 FROM version v WHERE v.title_id=t.id)
 AND (t.kind='movie' OR EXISTS(SELECT 1 FROM episode e WHERE e.title_id=t.id))
 GROUP BY t.id HAVING MIN(c.next_check_at)<=? OR COUNT(c.title_id)=0 ORDER BY COALESCE(MIN(c.next_check_at),0),t.id LIMIT 25`, now.UnixMilli())
			if err != nil {
				return err
			}
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				ids = append(ids, id)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		for _, id := range ids {
			if err := st.WriteTx(ctx, func(tx *sql.Tx) error { return Compute(ctx, tx, pol, id, now) }); err != nil {
				return err
			}
		}
		return nil
	})
}

// Run schedules local recomputation every minute. Titles are checked daily and overrides expire on the next pass.
func Run(ctx context.Context, q *jobs.Queue, log *slog.Logger) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		if _, _, err := q.Enqueue(ctx, JobKind, Payload{}, jobs.EnqueueOptions{DedupeKey: JobKind, Priority: -30}); err != nil && ctx.Err() == nil {
			log.Warn("ceiling: compute not queued", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
