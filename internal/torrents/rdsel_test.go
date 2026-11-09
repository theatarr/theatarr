package torrents

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
)

// delAdder is the Real-Debrid fake that can delete its own add (adder.Deleter), gives every add a new id, and turns
// a selection in cached (as "<hash>|<ids>") into a downloaded torrent, as Real-Debrid caches per selected set.
type delAdder struct {
	*fakeAdder
	rd      *fakeProvider
	cached  map[string]bool
	dels    []string
	delErr  error
	selects []string // "<id>:<ids>"
	n       int
}

func (a *delAdder) AddMagnet(ctx context.Context, m adder.Magnet, o adder.AddOptions) (*adder.Added, error) {
	got, err := a.fakeAdder.AddMagnet(ctx, m, o)
	if err != nil {
		return nil, err
	}
	a.n++
	old := got.ID
	got.ID += "-" + strconv.Itoa(a.n)
	t := a.rd.detail[old]
	delete(a.rd.detail, old)
	c := *t
	c.ID = got.ID
	a.rd.detail[got.ID] = &c
	return got, nil
}

func (a *delAdder) SelectFiles(ctx context.Context, id string, ids []string) error {
	a.selects = append(a.selects, id+":"+strings.Join(ids, ","))
	if t := a.rd.detail[id]; t != nil {
		t.Status, t.State = "downloading", provider.StateDownloading
		if a.cached[t.Hash+"|"+strings.Join(ids, ",")] {
			t.Status, t.State = "downloaded", provider.StateDownloaded
		}
	}
	return a.fakeAdder.SelectFiles(ctx, id, ids)
}

func (a *delAdder) DeleteOwnUncachedAdd(_ context.Context, ad *adder.Added, p adder.OwnAdd) error {
	if !p.NotOnAccountBefore || !p.NewEntry {
		return adder.ErrNotOwnAdd
	}
	if a.delErr != nil {
		return a.delErr
	}
	a.dels = append(a.dels, ad.ID)
	delete(a.rd.detail, ad.ID)
	return nil
}

// rdselFixture: a backup of rd holding A, a movie whose main file and nfo were selected (not a subset of the
// playable files), with a sample, subtitle and extra beside it, gone from rd since; adds go to a delAdder.
// selected overrides the selected file offsets.
func rdselFixture(t *testing.T, selected ...int) (*env, *delAdder, RestoreRequest) {
	t.Helper()
	e := newEnv(t)
	files := func(dir string) []provider.File {
		return []provider.File{{ID: "1", Path: dir + "/A.nfo", Bytes: 1}, {ID: "2", Path: dir + "/A.mkv", Bytes: 9000},
			{ID: "3", Path: dir + "/A.sample.mkv", Bytes: 50}, {ID: "4", Path: dir + "/Subs/eng.srt", Bytes: 2},
			{ID: "5", Path: dir + "/Extras/Making.Of.mp4", Bytes: 800}}
	}
	old := e.now.Add(-72 * time.Hour)
	e.rd.list = []provider.Torrent{tor("1", hA, "A", 1, old, provider.StateDownloaded)}
	backed := files("/A")
	if len(selected) == 0 {
		selected = []int{0, 1}
	}
	for _, i := range selected {
		backed[i].Selected = true
	}
	e.rd.detail = map[string]*provider.Torrent{"1": {ID: "1", Hash: hA, Files: backed}}
	b, _, err := e.m.TakeBackup(context.Background(), BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	e.rd.list = nil
	e.ard.onAdd = func(id, hash string) {
		e.rd.detail[id] = &provider.Torrent{ID: id, Hash: hash, Status: rdWaitingFiles, Files: files("/A")}
	}
	da := &delAdder{fakeAdder: e.ard, rd: e.rd, cached: map[string]bool{}}
	e.m.Adders["rd"] = da
	return e, da, RestoreRequest{BackupID: b.ID, Hashes: []string{hA}}
}

func restoreAll(t *testing.T, e *env, req RestoreRequest) RestoreItem {
	t.Helper()
	ctx := context.Background()
	plan, err := e.m.PlanRestore(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.m.Restore(ctx, req, plan.Confirmed(), "admin")
	if err != nil || len(got.Items) != 1 {
		t.Fatalf("restore: %+v %v", got, err)
	}
	return got.Items[0]
}

func journalRows(t *testing.T, e *env) []JournalEntry {
	t.Helper()
	j, err := e.m.Journal(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(j)
	return j
}

// A known selection including a non-playable file comes back uncached on Real-Debrid: the restore's own add is
// deleted and the torrent added once more with every playable file (the main video and the extra; never the nfo,
// sample or subtitle) when the selection's playable part is not a strict subset of them (here: the nfo and both
// videos). Both adds are paced and the first is counted uncached; the journal keeps both.
func TestRestoreKnownSelectionFallsBackToPlayable(t *testing.T) {
	t.Parallel()
	e, da, req := rdselFixture(t, 0, 1, 4)
	da.cached[hA+"|2,5"] = true
	it := restoreAll(t, e, req)
	if want := []string{"new-aaaa-1:1,2,5", "new-aaaa-2:2,5"}; !slices.Equal(da.selects, want) {
		t.Fatalf("selections %v, want %v", da.selects, want)
	}
	if !slices.Equal(da.dels, []string{"new-aaaa-1"}) {
		t.Errorf("deletes %v", da.dels)
	}
	if it.Outcome != OutAdded || it.NewID != "new-aaaa-2" || it.Selection != SelAll || it.Error != "" {
		t.Errorf("item %+v", it)
	}
	j := journalRows(t, e)
	if len(j) != 2 || j[0].TorrentID != "new-aaaa-1" || j[0].Outcome != OutUncached ||
		!strings.Contains(j[0].Detail, "added again with every playable file") || j[1].TorrentID != "new-aaaa-2" ||
		j[1].Outcome != OutAdded {
		t.Errorf("journal %+v", j)
	}
	var adds, uncached int
	if err := e.st.Reader().QueryRow(`SELECT window_adds, hour_uncached FROM router_pacing WHERE provider = 'rd'`).
		Scan(&adds, &uncached); err != nil || adds != 2 || uncached != 1 {
		t.Errorf("pacing: adds %d, uncached %d, %v", adds, uncached, err)
	}
	if p, _ := e.m.pendingSelections(context.Background()); len(p) != 0 {
		t.Errorf("pending left: %+v", p)
	}
}

// A known selection whose playable part is a strict subset of the playable files (the main video,
// not the extra) but that holds the nfo too comes back uncached: the second add selects that playable part alone,
// still a known selection, never every playable file.
func TestRestoreMixedPartialSelectionKeepsItsPart(t *testing.T) {
	t.Parallel()
	e, da, req := rdselFixture(t)
	da.cached[hA+"|2"] = true
	it := restoreAll(t, e, req)
	if want := []string{"new-aaaa-1:1,2", "new-aaaa-2:2"}; !slices.Equal(da.selects, want) {
		t.Fatalf("selections %v, want %v", da.selects, want)
	}
	if !slices.Equal(da.dels, []string{"new-aaaa-1"}) || it.Outcome != OutAdded || it.Selection != SelFiles ||
		it.NewID != "new-aaaa-2" {
		t.Errorf("deletes %v, item %+v", da.dels, it)
	}
	if j := journalRows(t, e); len(j) != 2 || !strings.Contains(j[0].Detail, "playable part alone (1 files)") {
		t.Errorf("journal %+v", j)
	}
}

// A shorter set is not necessarily a deliberate partial selection: the known nfo-only set is not a subset of
// the playable files, so keep the retry even though it has fewer files than the playable set.
func TestRestoreNonPlayableKnownSelectionRetries(t *testing.T) {
	t.Parallel()
	e, da, req := rdselFixture(t, 0)
	da.cached[hA+"|2,5"] = true
	it := restoreAll(t, e, req)
	if want := []string{"new-aaaa-1:1", "new-aaaa-2:2,5"}; !slices.Equal(da.selects, want) {
		t.Fatalf("selections %v, want %v", da.selects, want)
	}
	if !slices.Equal(da.dels, []string{"new-aaaa-1"}) || it.Outcome != OutAdded || it.Selection != SelAll {
		t.Errorf("deletes %v, item %+v", da.dels, it)
	}
}

// Never a third add: both sets uncached leaves the second add (uncached). The exact set cached needs no retry; a
// known selection that is every playable file already has no other set; a failed delete keeps the first add.
func TestRestoreKnownSelectionRetryLimits(t *testing.T) {
	t.Parallel()
	e, da, req := rdselFixture(t)
	it := restoreAll(t, e, req)
	if len(da.selects) != 2 || len(da.dels) != 1 || it.Outcome != OutUncached || it.NewID != "new-aaaa-2" {
		t.Errorf("both uncached: selects %v, deletes %v, %+v", da.selects, da.dels, it)
	}

	e, da, req = rdselFixture(t)
	da.cached[hA+"|1,2"] = true
	if it := restoreAll(t, e, req); len(da.selects) != 1 || len(da.dels) != 0 || it.Outcome != OutAdded {
		t.Errorf("exact set cached: selects %v, deletes %v, %+v", da.selects, da.dels, it)
	}

	e, da, req = rdselFixture(t, 1, 4)
	if it := restoreAll(t, e, req); len(da.selects) != 1 || len(da.dels) != 0 || it.Outcome != OutUncached {
		t.Errorf("same set: selects %v, deletes %v, %+v", da.selects, da.dels, it)
	}

	e, da, req = rdselFixture(t)
	da.delErr = errors.New("rd: 503")
	it = restoreAll(t, e, req)
	if len(da.selects) != 1 || it.Outcome != OutUncached || it.NewID != "new-aaaa-1" ||
		!strings.Contains(it.Error, "deleting it to try every playable file failed") {
		t.Errorf("failed delete: selects %v, %+v", da.selects, it)
	}
}

// A backup that never read the files (a DMM import) selects every playable file, never the nfo, text, images or
// subtitles; one with no playable file is added and left for the person (incomplete), nothing selected.
func TestRestoreAllSelectsPlayableOnly(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	e.ard.onAdd = func(id, hash string) {
		files := []provider.File{{ID: "1", Path: "/C/C.nfo"}, {ID: "2", Path: "/C/RARBG.txt"},
			{ID: "3", Path: "/C/C.2160p.mkv", Bytes: 9000}, {ID: "4", Path: "/C/poster.jpg"},
			{ID: "5", Path: "/C/Subs/eng.srt"}, {ID: "6", Path: "/C/C.sample.mkv", Bytes: 9}}
		if hash == hD {
			files = []provider.File{{ID: "1", Path: "/D/D.nfo"}, {ID: "2", Path: "/D/D.jpg"}, {ID: "3", Path: "/D/D.rar"}}
		}
		e.rd.detail[id] = &provider.Torrent{ID: id, Hash: hash, Status: rdWaitingFiles, Files: files}
	}
	e.rd.detail = map[string]*provider.Torrent{}
	b, _, err := e.m.ImportBackup(ctx, []byte(`[{"filename":"C","hash":"`+hC+`"},{"filename":"D","hash":"`+hD+`"}]`),
		"rd", "admin")
	if err != nil {
		t.Fatal(err)
	}
	req := RestoreRequest{BackupID: b.ID}
	plan, err := e.m.PlanRestore(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.m.Restore(ctx, req, plan.Confirmed(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	if s := e.ard.selected["new-cccc"]; !slices.Equal(s, []string{"3"}) {
		t.Errorf("C selected %v", s)
	}
	if s, ok := e.ard.selected["new-dddd"]; ok {
		t.Errorf("D selected %v", s)
	}
	out := map[string]RestoreItem{}
	for _, it := range got.Items {
		out[it.Name] = it
	}
	if d := out["D"]; d.Outcome != OutIncomplete || !strings.Contains(d.Error, "playable") ||
		!strings.Contains(d.Error, "select its files") {
		t.Errorf("D: %+v", d)
	}
}
