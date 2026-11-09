package golive

import (
	"context"
	"github.com/theatarr/theatarr/internal/store"
	"testing"
)

func TestReviewGoLiveExcludesMetadataWaits(t *testing.T) {
	t.Parallel()
	pol := examplePolicy(t)
	st := testStore(t)
	seed(t, st, "movie-web", "movie", "radarr_4k_web", 1, 19, 19, 20)
	exec(t, st, `INSERT INTO shadow_diff(at,title_id,version_key,bucket,reason,created_at,kind) VALUES(?,1,'movie-web','arr_grabbed_we_wouldnt','metadata_pending',?,'movie')`, store.Millis(now), store.Millis(now))
	rep := &Report{}
	c := &checker{o: Options{Policy: pol, Version: "movie-web", DB: st.Reader(), Now: now, Days: 14, Bar: 0.95, MinGrabs: 20}, r: rep}
	c.shadow(context.Background())
	rows := find(rep, SecShadow, "agreement")
	if len(rows) != 1 || rows[0].Status != Warn {
		t.Fatalf("expected too few real comparisons, got %+v", rows)
	}
}
