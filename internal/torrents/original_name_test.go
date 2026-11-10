package torrents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
)

const (
	hX = "3333333333333333333333333333333333333333"
	hY = "4444444444444444444444444444444444444444"
)

// origFolder is the folder the mount names X by: the torrent's original name, which its provider list does not show.
const origFolder = "Movie.X.2013.1080p.WEB-DL"

// originalFixture is usageFixture plus X on rd and Y on an AllDebrid account, each listed under another name than
// its folder on the mount, and a scan root link into each folder (the only references to them).
func originalFixture(t *testing.T) (*env, *fakeProvider) {
	e, root := usageFixture(t)
	old := e.now.Add(-72 * time.Hour)
	e.rd.list = append(e.rd.list, tor("20", hX, "movie.x.2013.1080p.web-dl.mkv.part", 900, old,
		provider.StateDownloaded))
	e.rd.detail = map[string]*provider.Torrent{"20": {ID: "20", Hash: hX, Name: "movie.x.2013.1080p.web-dl.mkv.part",
		OriginalName: origFolder, Files: []provider.File{{ID: "1", Path: "X.mkv", Bytes: 900, Selected: true}}}}
	ad := &fakeProvider{key: "ad", kind: provider.KindAllDebrid,
		list: []provider.Torrent{tor("30", hY, "Movie Y (2012)", 950, old, provider.StateDownloaded)},
		detail: map[string]*provider.Torrent{"30": {ID: "30", Hash: hY, Name: "Movie Y (2012)",
			Files:        []provider.File{{Path: "Movie.Y.2012.1080p.WEB-DL/Y.mkv", Bytes: 950}},
			OriginalName: "Movie.Y.2012.1080p.WEB-DL"}}}
	e.pol.Providers["ad"] = &policy.Provider{Kind: provider.KindAllDebrid, Order: 3}
	e.m.Providers["ad"] = ad
	e.synced("ad")
	e.content(hX, 1)
	e.content(hY, 1)
	for name, folder := range map[string]string{"X.mkv": origFolder, "Y.mkv": "Movie.Y.2012.1080p.WEB-DL"} {
		if err := os.Symlink(mountPath+"/__all__/"+folder+"/"+name, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	return e, ad
}

// A torrent used only through a folder named by its original name is used in the delete preview (RD and AD), and
// nothing is deleted.
func TestDeleteSeesOriginalName(t *testing.T) {
	t.Parallel()
	e, ad := originalFixture(t)
	ctx := context.Background()
	sel := []DeleteSelection{{Provider: "rd", ID: "20", Hash: hX}, {Provider: "ad", ID: "30", Hash: hY}}
	plan, err := e.m.PlanDelete(ctx, sel)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range plan.Items {
		if it.Usage != Used || it.Refs[RefScan] != 1 || !it.NeedsOverride || !strings.Contains(it.Refusal, "1 scan") {
			t.Errorf("%s %s: used through its original name: %+v", it.Provider, it.ID, it)
		}
	}
	if plan.Count != 0 || e.rd.reads != 1 || ad.reads != 1 {
		t.Fatalf("one detail read each, nothing to delete: count %d, reads rd %d ad %d", plan.Count, e.rd.reads,
			ad.reads)
	}
	if _, err := e.m.Delete(ctx, sel, plan.Token, "admin"); err != nil || e.ard.deleted != 0 {
		t.Fatalf("nothing deleted: %v, %d", err, e.ard.deleted)
	}

	// The last check reads the name too: with the override for those references it agrees with the plan.
	sel = []DeleteSelection{{Provider: "rd", ID: "20", Hash: hX, Override: true,
		OverrideToken: plan.Items[0].ExpectedOverride}}
	again, err := e.m.PlanDelete(ctx, sel)
	if err != nil || again.Count != 1 {
		t.Fatalf("override: %v %+v", err, again)
	}
	if why, err := e.m.usageChanged(ctx, again, &again.Items[0], e.scan(ctx)); err != nil || why != "" {
		t.Fatalf("last check agrees: %q %v", why, err)
	}
	// A renamed original name since the plan is a change: skipped.
	e.rd.detail["20"].OriginalName = "Movie.X.2013.REPACK"
	if why, _ := e.m.usageChanged(ctx, again, &again.Items[0], e.scan(ctx)); !strings.Contains(why, "changed") {
		t.Fatalf("last check sees the original name: %q", why)
	}
	// The override deletes it through Delete itself (its re-plan and last check read the same name).
	e.rd.detail["20"].OriginalName = origFolder
	done, err := e.m.Delete(ctx, sel, again.Token, "admin")
	if err != nil || e.ard.deleted != 1 || done.Deleted != 1 {
		t.Fatalf("override delete: %v, %d deleted, %+v", err, e.ard.deleted, done)
	}
}

func (e *env) scan(ctx context.Context) *refIndex {
	sc := newScanIndex()
	e.m.scanRefs(ctx, sc)
	return sc
}

// A failed detail read, or one without a name (an AllDebrid magnet not ready), leaves the torrent unknown.
func TestDeleteOriginalNameUnreadIsUnknown(t *testing.T) {
	t.Parallel()
	e, ad := originalFixture(t)
	ctx := context.Background()
	e.rd.detailErr = errors.New("rd: 503")
	ad.detail["30"] = &provider.Torrent{ID: "30", Hash: hY, Name: "Movie Y (2012)"}
	sel := []DeleteSelection{{Provider: "rd", ID: "6", Hash: hF}, {Provider: "ad", ID: "30", Hash: hY}}
	plan, err := e.m.PlanDelete(ctx, sel)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{reasonNoOriginal, reasonNoName}
	for i, it := range plan.Items {
		if it.Usage != Unknown || len(it.Reasons) != 1 || it.Reasons[0] != want[i] || it.Action != ActRefuse {
			t.Errorf("%s %s: unknown, never unused: %+v", it.Provider, it.ID, it)
		}
	}
	if _, err := e.m.Delete(ctx, sel, plan.Token, "admin"); err != nil || e.ard.deleted != 0 {
		t.Fatalf("nothing deleted: %v, %d", err, e.ard.deleted)
	}
}

// Torrents matched by hash, and installs whose references all carry hashes, read no detail.
func TestDeleteOriginalNameNoReads(t *testing.T) {
	t.Parallel()
	e, ad := originalFixture(t)
	ctx := context.Background()
	plan, err := e.m.PlanDelete(ctx, []DeleteSelection{{Provider: "rd", ID: "1", Hash: hA},
		{Provider: "rd", ID: "3", Hash: hC}, {Provider: "rd", ID: "4", Hash: hD}})
	if err != nil || plan.Items[0].Usage != Used || plan.Items[1].Usage != Used || plan.Items[2].Usage != Used {
		t.Fatalf("used by hash: %v %+v", err, plan)
	}
	if e.rd.reads != 0 || ad.reads != 0 {
		t.Fatalf("hash matches read nothing: rd %d ad %d", e.rd.reads, ad.reads)
	}

	// No scan roots, no link without a hash: an unused torrent is unused without a read.
	e.pol.Torrents = nil
	e.exec(`DELETE FROM link WHERE content_file_id IS NULL`)
	plan, err = e.m.PlanDelete(ctx, []DeleteSelection{{Provider: "rd", ID: "6", Hash: hF},
		{Provider: "ad", ID: "30", Hash: hY}})
	if err != nil || plan.Items[0].Usage != Unused || plan.Items[1].Usage != Unused || plan.Count != 2 {
		t.Fatalf("unused: %v %+v", err, plan)
	}
	if e.rd.reads != 0 || ad.reads != 0 {
		t.Fatalf("no folder-only references, no reads: rd %d ad %d", e.rd.reads, ad.reads)
	}
}

// TorBox's list carries the files, so the report names its folder by them without a read.
func TestReportTorBoxOriginalName(t *testing.T) {
	t.Parallel()
	e, _ := originalFixture(t)
	e.synced("tb")
	e.content(hH, 1)
	e.tb.list = append(e.tb.list, provider.Torrent{ID: "91", Hash: hH, Name: "Movie T 2011", Bytes: 10,
		Added: e.now.Add(-72 * time.Hour), State: provider.StateDownloaded,
		Files: []provider.File{{Path: "Movie.X.2013.1080p.WEB-DL/X.mkv", Bytes: 10, Selected: true}}})
	r, err := e.m.Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := find(r, "tb", "91"); got == nil || got.Usage != Used || got.Refs[RefScan] != 1 {
		t.Fatalf("tb 91 used through its files' folder: %+v", got)
	}
	if e.tb.reads != 0 || e.rd.reads != 0 {
		t.Fatalf("the report reads no detail: tb %d rd %d", e.tb.reads, e.rd.reads)
	}
}

// A Real-Debrid detail without original_filename is unknown even with files: its folder is not guessed from them.
func TestDeleteOriginalNameEmptyWithFilesIsUnknown(t *testing.T) {
	t.Parallel()
	e, _ := originalFixture(t)
	ctx := context.Background()
	e.rd.detail["20"].OriginalName = ""
	e.rd.detail["20"].Files = []provider.File{{ID: "1", Path: "movie.x.2013.1080p.web-dl.mkv", Bytes: 900,
		Selected: true}}
	sel := []DeleteSelection{{Provider: "rd", ID: "20", Hash: hX}}
	plan, err := e.m.PlanDelete(ctx, sel)
	if err != nil {
		t.Fatal(err)
	}
	if it := plan.Items[0]; it.Usage != Unknown || len(it.Reasons) != 1 || it.Reasons[0] != reasonNoName ||
		it.Action != ActRefuse {
		t.Fatalf("unknown, never unused: %+v", it)
	}
	if _, err := e.m.Delete(ctx, sel, plan.Token, "admin"); err != nil || e.ard.deleted != 0 {
		t.Fatalf("nothing deleted: %v, %d", err, e.ard.deleted)
	}
}
