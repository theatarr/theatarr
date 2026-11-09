package arrview

import (
	"context"
	"math"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// queueRemaining reads progress samples for the whole batch. Keep the same five-minute, increasing-sample
// estimate as router.EstimateRemaining; completed/held imports are handled by queueTracking instead.
func (s *Service) queueRemaining(ctx context.Context, batch []queueRow) (map[int64]time.Duration, error) {
	out := map[int64]time.Duration{}
	ids := make([]int64, 0, len(batch))
	for _, x := range batch {
		if x.id > 0 && x.state == "downloading" {
			ids = append(ids, x.id)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	now := s.now()
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT d.id,p.sampled_at,p.progress FROM download_progress p
 JOIN download d ON d.id=p.download_id WHERE d.id IN (SELECT value FROM json_each(?)) AND d.state='downloading'
 AND p.provider=d.provider AND p.torrent_id=d.provider_torrent_id AND p.progress<=d.progress
 AND p.sampled_at>=? ORDER BY d.id,p.sampled_at`, jsonIDs(ids), store.Millis(now.Add(-5*time.Minute)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type sample struct {
		at       int64
		progress float64
	}
	var id int64
	var first, prev, last sample
	count := 0
	finish := func() {
		if count < 2 || last.at > store.Millis(now) || last.progress <= prev.progress || last.progress <= first.progress {
			return
		}
		remaining := float64(time.Duration(last.at-first.at)*time.Millisecond) * (100 - last.progress) / (last.progress - first.progress)
		if math.IsNaN(remaining) || math.IsInf(remaining, 0) || remaining >= float64(math.MaxInt64) {
			return
		}
		d := time.Duration(remaining) - now.Sub(store.FromMillis(last.at))
		if d > 0 {
			out[id] = d
		}
	}
	for rows.Next() {
		var nextID int64
		var x sample
		if err := rows.Scan(&nextID, &x.at, &x.progress); err != nil {
			return nil, err
		}
		if nextID != id {
			finish()
			id = nextID
			count = 0
			first = x
		}
		prev, last = last, x
		count++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	finish()
	return out, nil
}
