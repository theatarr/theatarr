package arrview

import (
	"context"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/router"
)

func TestQueueBatchedEstimatesMatchRouter(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	batch := []queueRow{}
	for i, progress := range []float64{40, 60, 80} {
		id := int64(i + 1)
		e.exec(`INSERT INTO download(id,title_id,version_id,version_key,info_hash,release_title,indexer,provider,
   provider_torrent_id,state,progress,created_at,updated_at) VALUES(?,1,2,'movie-web',?,'Test','dmm','rd','tid','downloading',?,?,?)`, id, string(rune('a'+i)), progress, ms(e.now), ms(e.now))
		e.exec(`INSERT INTO download_progress(download_id,sampled_at,provider,torrent_id,progress)
   VALUES(?,?,'rd','tid',?),(?,?,'rd','tid',?)`, id, ms(e.now.Add(-time.Minute)), progress-20, id, ms(e.now), progress)
		batch = append(batch, queueRow{id: id, state: "downloading"})
	}
	// One sample, a stale sample and a changed provider/torrent identity must not become an estimate.
	e.exec(`DELETE FROM download_progress WHERE download_id=2 AND progress=40`)
	e.exec(`UPDATE download_progress SET torrent_id='old' WHERE download_id=3`)
	got, err := e.svc.queueRemaining(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range batch {
		want, known, err := router.EstimateRemaining(context.Background(), e.st.Reader(), x.id, e.now)
		d, ok := got[x.id]
		if err != nil || ok != known || d != want {
			t.Errorf("download %d: %v,%t want %v,%t (%v)", x.id, d, ok, want, known, err)
		}
	}
}
