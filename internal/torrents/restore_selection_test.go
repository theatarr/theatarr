package torrents

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/provider"
)

// A backup selected one episode of a ten-episode pack. Even when it is uncached, restoring it
// must keep that exact selection instead of deleting the add and selecting the entire pack.
func TestRestorePartialSelectionKept(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	files := func() []provider.File {
		var out []provider.File
		for i := 1; i <= 10; i++ {
			out = append(out, provider.File{ID: fmt.Sprint(i), Path: fmt.Sprintf("/Show.S01/Show.S01E%02d.mkv", i), Bytes: 4000})
		}
		return out
	}
	old := e.now.Add(-72 * time.Hour)
	e.rd.list = []provider.Torrent{tor("1", hA, "Show.S01", 1, old, provider.StateDownloaded)}
	backed := files()
	backed[4].Selected = true
	e.rd.detail = map[string]*provider.Torrent{"1": {ID: "1", Hash: hA, Files: backed}}
	b, _, err := e.m.TakeBackup(context.Background(), BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	e.rd.list = nil
	e.ard.onAdd = func(id, hash string) {
		e.rd.detail[id] = &provider.Torrent{ID: id, Hash: hash, Status: rdWaitingFiles, Files: files()}
	}
	da := &delAdder{fakeAdder: e.ard, rd: e.rd, cached: map[string]bool{}}
	e.m.Adders["rd"] = da
	it := restoreAll(t, e, RestoreRequest{BackupID: b.ID, Hashes: []string{hA}})
	if len(da.dels) != 0 || !slices.Equal(da.selects, []string{"new-aaaa-1:5"}) {
		t.Fatalf("partial selection changed: selects %v; deletes %v", da.selects, da.dels)
	}
	if it.Outcome != OutUncached || it.NewID != "new-aaaa-1" || it.Selection != SelFiles || it.Error != "" {
		t.Errorf("item %+v", it)
	}
	if j := journalRows(t, e); len(j) != 1 || j[0].Outcome != OutUncached || j[0].TorrentID != it.NewID {
		t.Errorf("journal %+v", j)
	}
	var adds, uncached int
	if err := e.st.Reader().QueryRow(`SELECT window_adds, hour_uncached FROM router_pacing WHERE provider = 'rd'`).
		Scan(&adds, &uncached); err != nil || adds != 1 || uncached != 1 {
		t.Errorf("pacing: adds %d, uncached %d, %v", adds, uncached, err)
	}
}
