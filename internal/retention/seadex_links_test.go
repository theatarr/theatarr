package retention

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A held link committed while the prune waits for the writer keeps the snapshot that is its only provenance.
func TestPruneKeepsLinkAddedWhileDeleteWaits(t *testing.T) {
	st := openStore(t)
	hash := strings.Repeat("a", 40)
	exec(t, st, `INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Held',0,0)`)
	exec(t, st, `INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','7',200,?,1,9999999999999)`, `{"anilist":7,"torrents":[{"info_hash":"`+hash+`","preference":2}]}`)
	tx, err := st.Writer().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	start := st.Writer().Stats().WaitCount
	done := make(chan error, 1)
	go func() { _, err := pruneSeaDex(context.Background(), st, 100); done <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for st.Writer().Stats().WaitCount == start && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if st.Writer().Stats().WaitCount == start {
		t.Fatal("prune did not reach delete")
	}
	if _, err := tx.Exec(`INSERT INTO link(id,title_id,path,target,file_metadata,created_at,updated_at) VALUES(1,1,'/l','/t',?,0,0)`, `{"InfoHash":"`+hash+`","SeaDexPreference":2}`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM metadata_cache WHERE source='seadex-standing' AND key='7'`); n != 1 {
		t.Fatal("prune deleted the sole provenance of the just-committed held link")
	}
}

// A held link whose hash is known only through its content file references the snapshot listing it.
func TestPruneKeepsContentFileHashReference(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	hash := strings.Repeat("b", 40)
	exec(t, st, `INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Held',0,0)`)
	exec(t, st, `INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (1, ?, 'present', 0, 0)`, hash)
	exec(t, st, `INSERT INTO content_file (id, content_id, path, size_bytes) VALUES (1, 1, 'file.mkv', 1000)`)
	exec(t, st, `INSERT INTO link(id,title_id,path,target,content_file_id,created_at,updated_at) VALUES(1,1,'/l','/t',1,0,0)`)
	exec(t, st, `INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','7',200,?,1,9999999999999)`, `{"anilist":7,"torrents":[{"info_hash":"`+hash+`","preference":2}]}`)
	exec(t, st, `INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','8',200,?,1,9999999999999)`, `{"anilist":8,"torrents":[{"info_hash":"`+strings.Repeat("c", 40)+`","preference":2}]}`)
	if _, err := pruneSeaDex(context.Background(), st, 100); err != nil {
		t.Fatal(err)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM metadata_cache WHERE source='seadex-standing' AND key='7'`); n != 1 {
		t.Fatal("prune deleted the snapshot a content-file hash still references")
	}
	if n := count(t, st, `SELECT COUNT(*) FROM metadata_cache WHERE source='seadex-standing' AND key='8'`); n != 0 {
		t.Fatal("an unreferenced snapshot survived")
	}
}
