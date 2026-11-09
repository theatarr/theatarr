package library

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
	"modernc.org/sqlite"
)

func extraAdoptFixture(t *testing.T) (*env, *adoptFile) {
	t.Helper()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["tv"].PreferSeaDex = true
	e.exec(`INSERT INTO title_external_id VALUES(2,'anilist','11')`)
	hash := strings.Repeat("a", 40)
	body := fmt.Sprintf(`{"anilist":11,"torrents":[{"info_hash":"%s","quality":"Bluray-2160p Remux","preference":2,"is_best":true,"size":10000000,"facts":{"isBest":true,"files":[{"name":"Show.S01E01.mkv","length":10000000}]}}]}`, hash)
	e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','11',200,?,0,253402214400000)`, body)
	f := &adoptFile{titleID: 2, followerID: 20, hash: hash, quality: "Bluray-2160p Remux", rev: 1, scene: "Show.S01E01.2160p.BluRay.REMUX-GRP", path: "/source/1.mkv", instance: "sonarr"}
	return e, f
}

func TestAdoptPersistsResolvedStanding(t *testing.T) {
	t.Parallel()
	e, f := extraAdoptFixture(t)
	ctx := withCutoffTitles(e.ctx)
	if err := e.lib.recordAdopted(ctx, "tv", e.pol.Versions["tv"], f, "/test/first", "/source/1.mkv", []int64{201}, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	var meta decide.ExistingFile
	if err := json.Unmarshal([]byte(e.one(`SELECT file_metadata FROM link WHERE path='/test/first'`)), &meta); err != nil {
		t.Fatal(err)
	}
	t.Logf("saved adopted metadata: SeaDexEntry=%d SeaDexPreference=%d", meta.SeaDexEntry, meta.SeaDexPreference)
	e.exec(`UPDATE title_external_id SET external_id='12' WHERE title_id=2 AND source='anilist'`)
	load := func() int {
		c, err := decide.WithStoredSeaDex(e.ctx, e.st.Reader())
		if err != nil {
			t.Fatal(err)
		}
		c = decide.WithSeaDexPolicy(c, e.pol, e.pol.Versions["tv"])
		eps, err := decide.LoadEpisodes(c, e.st.Reader(), 2, 20)
		if err != nil {
			t.Fatal(err)
		}
		for _, ep := range eps {
			if ep.ID == 201 && ep.File != nil {
				return ep.File.SeaDexPreference
			}
		}
		t.Fatal("no adopted file")
		return -1
	}
	before := load()
	e.exec(`UPDATE link SET file_metadata=json_set(file_metadata,'$.SeaDexEntry',11,'$.SeaDexPreference',3) WHERE path='/test/first'`)
	after := load()
	if before != after {
		t.Fatalf("same adopted file loses protection on mapping change: persisted rank=%d, with originally resolved entry/rank saved=%d", before, after)
	}
}

// standingCounterID names each run's SQL function: a name stays registered for the process (-count > 1).
var standingCounterID atomic.Int64

func TestAdoptPinsStandingOncePerPass(t *testing.T) {
	var reads atomic.Int64
	fn := fmt.Sprintf("standing_counter_%d", standingCounterID.Add(1))
	if err := sqlite.RegisterScalarFunction(fn, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		reads.Add(1)
		return args[0], nil
	}); err != nil {
		t.Fatal(err)
	}
	e := newParallelEnv(t, true)
	e.pol.Versions["tv-theatarr"].PreferSeaDex = true
	e.exec(`INSERT INTO title_external_id VALUES(2,'anilist','11')`)
	e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','11',200,'{"anilist":11,"torrents":[]}',0,253402214400000)`)
	e.release("pack", map[string]float64{"Show.S01E01.mkv": 100, "Show.S01E02.mkv": 100})
	for i := 1; i <= 2; i++ {
		name := fmt.Sprintf("Show.S01E%02d.mkv", i)
		e.arrFile(int64(i), "sonarr", 20, 2, filepath.Join(e.shows, name), "pack/"+name, "Bluray-2160p Remux", int64(200+i))
	}
	rows, err := e.st.Reader().Query(`SELECT name FROM pragma_table_info('metadata_cache')`)
	if err != nil {
		t.Fatal(err)
	}
	var cols []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		col := `"` + name + `"`
		if name == "body" {
			col = fn + "(" + col + ") AS " + col
		}
		cols = append(cols, col)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	e.exec(`ALTER TABLE metadata_cache RENAME TO extra_metadata_cache`)
	e.exec(`CREATE VIEW metadata_cache AS SELECT ` + strings.Join(cols, ",") + ` FROM extra_metadata_cache`)
	if rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "tv-theatarr"}); err != nil || rep.Linked != 2 {
		t.Fatalf("adopt: %+v %v", rep, err)
	}
	if n := reads.Load(); n != 1 {
		t.Fatalf("SeaDex snapshot body read %d times for 2 files in one pass; want 1", n)
	}

	reads.Store(0)
	e.release("upgrade", map[string]float64{"Show.S01E01.mkv": 100, "Show.S01E02.mkv": 100})
	for i := 1; i <= 2; i++ {
		name := fmt.Sprintf("Show.S01E%02d.mkv", i)
		e.arrUpgrade(int64(i), "", int64(10+i), "sonarr", 20, 2, filepath.Join(e.shows, "new-"+name), "upgrade/"+name, "Bluray-2160p Remux", int64(200+i))
	}
	if rep, err := e.lib.Refresh(e.ctx, RefreshOptions{Version: "tv-theatarr"}); err != nil || rep.Swapped != 2 {
		t.Fatalf("refresh: %+v %v", rep, err)
	}
	if n := reads.Load(); n != 1 {
		t.Fatalf("refresh read standing %d times; want 1", n)
	}
}

func TestAdoptDoesNotSilentlyIgnoreStandingLoadFailure(t *testing.T) {
	t.Parallel()
	e, f := extraAdoptFixture(t)
	f.cutoffNotMet = true
	e.exec(`ALTER TABLE metadata_cache RENAME TO extra_metadata_cache`)
	e.exec(`CREATE VIEW metadata_cache AS SELECT source,key,status,json_extract('broken','$') AS body,fetched_at,expires_at FROM extra_metadata_cache`)
	if _, err := e.lib.cutoffTitle(withCutoffTitles(e.ctx), f.titleID, f.followerID, e.pol.Versions["tv"]); err != nil {
		t.Fatalf("title loader must succeed: %v", err)
	}
	ctx := withCutoffTitles(e.ctx)
	if err := e.lib.recordAdopted(ctx, "tv", e.pol.Versions["tv"], f, "/test/first", "/source/1.mkv", []int64{201}, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if state := e.one(`SELECT state FROM episode_version_state WHERE version_id=20 AND episode_id=201`); state != "filled" {
		t.Fatalf("standing catalog failed to load; adoption recorded %s instead of falling back to arr's filled flag", state)
	}
}

func TestCutoffLoadsImportMappings(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.isekai()
	vp := e.pol.Versions["tv"]
	vp.PreferSeaDex = true
	title, err := e.lib.cutoffTitle(withCutoffTitles(e.ctx), 3, 30, vp)
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := decide.ResolveFile(title, vp.Sonarr, decide.FileInput{Path: "[Group] Isekai Story 2nd Season - 01 (1080p).mkv", OtherVideoFiles: true})
	if err != nil || len(mapped.EpisodeIDs) != 1 || mapped.EpisodeIDs[0] != 313 {
		t.Fatalf("cutoff mapping=%+v err=%v; want S2E1 (313)", mapped, err)
	}
}
