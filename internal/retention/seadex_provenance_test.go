package retention

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// A stored snapshot that is the only entry provenance of a held file (a legacy link by hash, an imported arr file
// by content hash or by its SeaDex record id) survives pruning, so the file keeps its standing afterwards.
func TestPruneKeepsHeldProvenance(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	exec(t, st, `INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Held',0,0)`)
	exec(t, st, `INSERT INTO version(id,title_id,policy_key,created_at,updated_at) VALUES(1,1,'movie',0,0)`)
	exec(t, st, `INSERT INTO link(id,title_id,path,target,file_metadata,created_at,updated_at) VALUES(1,1,'/l','/t',?,0,0)`,
		`{"InfoHash":"`+a+`","SeaDexPreference":3,"SeaDex":2,"Revision":1,"Quality":"Remux-2160p"}`)
	exec(t, st, `INSERT INTO link_version VALUES(1,1)`)
	exec(t, st, `INSERT INTO content(id,info_hash,first_seen_at,last_seen_at) VALUES(1,?,0,0)`, b)
	exec(t, st, `INSERT INTO version_source VALUES (1, 'owner', 1)`)
	exec(t, st, `INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path, quality, scene_name, added_at, content_id)
		VALUES (1, 1, 1, 'owner', 1, '/b.mkv', 'Remux-2160p', 'b', 1, 1), (2, 1, 1, 'owner', 2, '/c.mkv', 'Remux-2160p', 'Film (7vdej49pr26gca0)', 1, NULL)`)
	put := func(key int, body string) {
		exec(t, st, `INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing',?,200,?,0,253402214400000)`, key, body)
	}
	put(1535, `{"anilist":1535,"torrents":[{"info_hash":"`+a+`","preference":3,"facts":{}}]}`)
	put(1536, `{"anilist":1536,"torrents":[{"info_hash":"`+b+`","preference":2}]}`)
	put(1537, `{"anilist":1537,"torrents":[{"record_id":"7vdej49pr26gca0","info_hash":"`+strings.Repeat("c", 40)+`","preference":1}]}`)
	put(1538, `{"anilist":1538,"torrents":[{"info_hash":"`+strings.Repeat("d", 40)+`","preference":1}]}`)
	put(1539, `not json`)
	sum, err := Prune(context.Background(), st, time.Now(), Config{History: Defaults.History, Runs: Defaults.Runs, MaxBatches: 1})
	if err != nil {
		t.Fatal(err)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM metadata_cache WHERE source='seadex-standing' AND key IN ('1535','1536','1537')`); n != 3 {
		t.Fatalf("held provenance pruned: %d of 3 kept", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM metadata_cache WHERE source='seadex-standing'`); n != 3 || sum["seadex-standing"] != 2 {
		t.Fatalf("unreferenced unmapped rows kept: %d rows, summary %v", n, sum)
	}
	ctx, err := decide.WithStoredSeaDex(context.Background(), st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	ctx = decide.WithSeaDexTitle(decide.WithSeaDexPolicy(ctx, nil, &policy.VersionPolicy{PreferSeaDex: true}), 1)
	f := &decide.ExistingFile{InfoHash: a, SeaDexPreference: 3}
	if err := decide.ResolveSeaDexFiles(ctx, st.Reader(), []*decide.ExistingFile{f}); err != nil || f.SeaDexEntry != 1535 || f.SeaDexPreference == 0 {
		t.Fatalf("held file lost its standing after pruning: %+v %v", f, err)
	}
}

// seedSeaDexScale is a large library: titles with AniList ids, active links (a tenth naming their
// entry), and standing rows of which all but one per title map to nothing.
func seedSeaDexScale(t testing.TB, st *store.Store, titles, links, snapshots int) {
	t.Helper()
	// Each table in one statement over a counter: tens of thousands of single-row inserts are slow under -race.
	const n = `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?1) `
	err := st.WriteTx(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(n+`INSERT INTO title(id,kind,name,created_at,updated_at) SELECT i,'movie','T'||i,0,0 FROM n`,
			titles); err != nil {
			return err
		}
		if _, err := tx.Exec(n+`INSERT INTO title_external_id SELECT i,'anilist',CAST(i AS TEXT) FROM n`, titles); err != nil {
			return err
		}
		if _, err := tx.Exec(n+`INSERT INTO link(id,title_id,path,target,file_metadata,created_at,updated_at)
			SELECT i, 1+i%?2, '/l/'||i, '/t/'||i, CASE WHEN i%10 = 0
				THEN printf('{"InfoHash":"%040x","Quality":"Bluray-1080p","SeaDexEntry":%d}', i, 1+i%?2)
				ELSE printf('{"InfoHash":"%040x","Quality":"Bluray-1080p","SceneName":"Some.Release.%d.1080p.BluRay-GRP"}', i, i)
				END, 0, 0 FROM n`, links, titles); err != nil {
			return err
		}
		_, err := tx.Exec(n+`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at)
			SELECT 'seadex-standing', CAST(id AS TEXT), 200,
				printf('{"anilist":%d,"torrents":[{"info_hash":"%040x","preference":2,"is_best":true,"facts":{"isBest":true}}]}', id, (1<<40)+i),
				0, 253402214400000
			FROM (SELECT i, CASE WHEN i <= ?2 THEN i ELSE 100000+i END AS id FROM n)`, snapshots, titles)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Pruning thousands of unmapped rows against tens of thousands of links reads everything once outside the write
// lock and deletes in small key batches: bounded in time, and the mapped rows stay.
func TestPruneAtScale(t *testing.T) {
	if testing.Short() {
		t.Skip("scale test")
	}
	st := openStore(t)
	seedSeaDexScale(t, st, 300, 20000, 2000)
	start := time.Now()
	sum, err := Prune(context.Background(), st, time.Now(), Config{History: Defaults.History, Runs: Defaults.Runs})
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if sum["seadex-standing"] != 1700 || count(t, st, `SELECT COUNT(*) FROM metadata_cache WHERE source='seadex-standing'`) != 300 {
		t.Fatalf("pruned %v", sum)
	}
	t.Logf("pruned 1700 of 2000 rows against 20000 links in %s", took)
	// The bound is for the plain build; the race detector makes the same work several times slower, more so on a
	// machine running other packages' race tests (13 s seen), so a -race build gets five times the room.
	bound := 10 * time.Second
	if raceDetector {
		bound *= 5
	}
	if took > bound {
		t.Fatalf("prune took %s, bound %s", took, bound)
	}
}

// BenchmarkPruneScan is the read side of a prune with nothing to delete (every sync after the first).
func BenchmarkPruneScan(b *testing.B) {
	st := openStore(b)
	seedSeaDexScale(b, st, 2000, 20000, 2000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := pruneSeaDex(context.Background(), st, 1000); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkStandingPin is the whole pin of one operation at live scale: the read, the catalog, and one
// prefer-on version's configured view.
func BenchmarkStandingPin(b *testing.B) {
	st := openStore(b)
	seedSeaDexScale(b, st, 2000, 20000, 2000)
	on := &policy.VersionPolicy{PreferSeaDex: true}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, err := decide.WithStoredSeaDex(context.Background(), st.Reader())
		if err != nil {
			b.Fatal(err)
		}
		if err := decide.SeaDexError(decide.WithSeaDexPolicy(ctx, nil, on)); err != nil {
			b.Fatal(err)
		}
	}
}
