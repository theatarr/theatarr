package retention

import (
	"context"
	"testing"
	"time"
)

func TestPruneUnmappedSeaDexStanding(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	exec(t, st, `INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Held',0,0)`)
	exec(t, st, `INSERT INTO title_external_id VALUES(1,'anilist','1')`)
	for _, key := range []string{"1", "2"} {
		exec(t, st, `INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing',?,200,'{}',0,253402214400000)`, key)
	}
	if _, err := Prune(context.Background(), st, time.Now(), Config{History: Defaults.History, Runs: Defaults.Runs, MaxBatches: 1}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM metadata_cache WHERE source='seadex-standing' AND key='1'`); n != 1 {
		t.Fatal("mapped snapshot pruned")
	}
	if n := count(t, st, `SELECT COUNT(*) FROM metadata_cache WHERE source='seadex-standing'`); n != 1 {
		t.Fatal("unmapped snapshot kept")
	}
}
