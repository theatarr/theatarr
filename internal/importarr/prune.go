package importarr

import (
	"context"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// RunKeep is how long import_run rows are kept. The shadow runner imports every 15 minutes (about 200 rows a day).
const RunKeep = 7 * 24 * time.Hour

// PruneRuns deletes import_run rows that started before now minus keep, except each instance's latest run, which
// stays however old it is (theatarr import-arr and the web UI show it). It returns the rows deleted.
func PruneRuns(ctx context.Context, st *store.Store, now time.Time, keep time.Duration) (int64, error) {
	res, err := st.Writer().ExecContext(ctx, `DELETE FROM import_run WHERE started_at < ?
		AND id NOT IN (SELECT MAX(id) FROM import_run GROUP BY instance)`, store.Millis(now.Add(-keep)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
