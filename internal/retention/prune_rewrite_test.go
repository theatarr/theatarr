package retention

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A candidate rewritten while the prune waits for the write lock, within the same fetched_at
// millisecond, to reference a held hash is judged by its new body and kept.
func TestPruneSameMillisecondRewrite(t *testing.T) {
	st := openStore(t)
	oldHash, newHash := strings.Repeat("a", 40), strings.Repeat("b", 40)
	exec(t, st, `INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Held',0,0)`)
	exec(t, st, `INSERT INTO link(id,title_id,path,target,file_metadata,created_at,updated_at) VALUES(1,1,'/l','/t',?,0,0)`, `{"InfoHash":"`+newHash+`"}`)
	exec(t, st, `INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','7',200,?,1,9999999999999)`,
		`{"anilist":7,"torrents":[{"info_hash":"`+oldHash+`","preference":2}]}`)
	tx, err := st.Writer().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	wait := st.Writer().Stats().WaitCount
	done := make(chan error, 1)
	go func() { _, err := pruneSeaDex(context.Background(), st, 100); done <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for st.Writer().Stats().WaitCount == wait && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if st.Writer().Stats().WaitCount == wait {
		t.Fatal("prune did not reach its delete transaction")
	}
	// Same fetched_at, new body naming the held hash.
	if _, err = tx.Exec(`UPDATE metadata_cache SET body=? WHERE source='seadex-standing' AND key='7'`,
		`{"anilist":7,"torrents":[{"info_hash":"`+newHash+`","preference":2}]}`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM metadata_cache WHERE source='seadex-standing' AND key='7'`); n != 1 {
		t.Fatal("prune deleted the rewritten, now held snapshot with the same fetched_at")
	}
}
