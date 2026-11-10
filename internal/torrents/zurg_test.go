package torrents

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
)

// fakeZurg serves /debug/torrents (fails with 500 while fail is set) and counts the reads.
type fakeZurg struct {
	srv   *httptest.Server
	list  []library.ZurgTorrent
	fail  atomic.Bool
	reads atomic.Int32
}

func newFakeZurg(t *testing.T, list []library.ZurgTorrent) *fakeZurg {
	z := &fakeZurg{list: list}
	z.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/debug/torrents" {
			http.NotFound(w, r)
			return
		}
		z.reads.Add(1)
		if z.fail.Load() {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(z.list)
	}))
	t.Cleanup(z.srv.Close)
	return z
}

// zurgFixture: rd 1 and 2 are listed by the providers under other names than zurg's, and only scan root links lead
// into the folders zurg names them by (1: zurg's name with .mkv trimmed; 2: a shared name, so " {hash6}"). rd 3 is not
// listed by zurg and its scan link names its original name; rd 6 is listed by zurg and unreferenced; tb 9 is rd 1's
// hash on another provider.
func zurgFixture(t *testing.T) (*env, *fakeZurg) {
	e := newEnv(t)
	old := e.now.Add(-72 * time.Hour)
	e.rd.list = []provider.Torrent{
		tor("1", hA, "marrow lantern 2014", 100, old, provider.StateDownloaded),
		tor("2", hB, "quill harbor", 200, old, provider.StateDownloaded),
		tor("3", hC, "ember.road.mkv", 300, old, provider.StateDownloaded),
		tor("6", hF, "Hollow.Meridian.2018", 600, old, provider.StateDownloaded),
	}
	e.rd.detail = map[string]*provider.Torrent{"3": {ID: "3", Hash: hC, Name: "ember.road.mkv",
		OriginalName: "Ember.Road.2016.1080p"}}
	e.tb.list = []provider.Torrent{tor("9", hA, "Marrow (2014)", 100, old, provider.StateDownloaded)}
	e.synced("rd")
	e.synced("tb")
	for _, h := range []string{hA, hB, hC, hF} {
		e.content(h, 1)
	}
	root := t.TempDir()
	for name, folder := range map[string]string{
		"1.mkv": "Marrow.Lantern.2014.1080p",
		"2.mkv": "Quill.Harbor.2019 {" + hB[:6] + "}",
		"3.mkv": "Ember.Road.2016.1080p",
	} {
		if err := os.Symlink(mountPath+"/__all__/"+folder+"/"+name, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	e.pol.Torrents = &policy.Torrents{ScanRoots: []string{root}}
	z := newFakeZurg(t, []library.ZurgTorrent{
		{Hash: hA, Name: "Marrow.Lantern.2014.1080p.mkv", Size: 100},
		{Hash: hB, Name: "Quill.Harbor.2019", Size: 200},
		{Hash: hF, Name: "Hollow.Meridian.2018.2160p", Size: 600},
	})
	e.m.Zurg = &library.ZurgAPI{URL: z.srv.URL, GW: plainDoer{}, AllDir: "__all__", Interval: time.Minute,
		Now: func() time.Time { return e.now }}
	return e, z
}

func usages(t *testing.T, r *Report) map[string]Torrent {
	t.Helper()
	out := map[string]Torrent{}
	for _, p := range r.Providers {
		for _, x := range p.Torrents {
			out[p.Provider+"/"+x.ID] = Torrent{Usage: x.Usage, Refs: x.Refs, RefList: x.RefList, Reasons: x.Reasons}
		}
	}
	return out
}

// zurg's name for a hash names its folder: a scan link into it makes the torrent used, on every provider holding
// the hash, with no provider read.
func TestUsageZurgName(t *testing.T) {
	t.Parallel()
	e, z := zurgFixture(t)
	ctx := context.Background()
	r, err := e.m.Report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]Usage{"rd/1": Used, "rd/2": Used, "rd/3": Unused, "rd/6": Unused,
		"tb/9": Used} {
		if got := usages(t, r)[id]; got.Usage != want || (want == Used && got.Refs[RefScan] != 1) {
			t.Errorf("%s: %+v, want %s", id, got, want)
		}
	}
	if e.rd.reads != 0 || e.tb.reads != 0 || z.reads.Load() != 1 {
		t.Fatalf("no provider reads, one listing: rd %d tb %d zurg %d", e.rd.reads, e.tb.reads, z.reads.Load())
	}

	// The delete preview still reads each original name first (zurg's name never stands in for it), then zurg's
	// name adds its folder: rd 1 used under zurg's name, rd 3 (not listed by zurg) under its original name.
	plan, err := e.m.PlanDelete(ctx, []DeleteSelection{{Provider: "rd", ID: "1", Hash: hA},
		{Provider: "rd", ID: "3", Hash: hC}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Items[0].Usage != Used || plan.Items[1].Usage != Used || e.rd.reads != 2 {
		t.Fatalf("preview: %+v, %d reads", plan.Items, e.rd.reads)
	}
	if why, err := e.m.usageChanged(ctx, plan, &plan.Items[0], e.scan(ctx)); err != nil || why != "" {
		t.Fatalf("last check agrees: %q %v", why, err)
	}
}

// Without zurg_api, with a failed listing, or with no folder-name references, the usage is as without zurg; with no
// folder-name references zurg is not read.
func TestUsageZurgAdditiveOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e, z := zurgFixture(t)
	zc := e.m.Zurg
	e.m.Zurg = nil
	base, err := e.m.Report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := usages(t, base)["rd/1"]; got.Usage != Unused {
		t.Fatalf("without zurg rd 1 is unused as before: %+v", got)
	}
	e.m.Zurg = zc
	z.fail.Store(true)
	failed, err := e.m.Report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(usages(t, base), usages(t, failed)) || len(failed.Unknown) != len(base.Unknown) {
		t.Fatalf("a failed listing changes nothing:\n%+v\n%+v", usages(t, base), usages(t, failed))
	}

	e2, z2 := zurgFixture(t)
	e2.pol.Torrents = nil
	if _, err := e2.m.Report(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e2.m.PlanDelete(ctx, []DeleteSelection{{Provider: "rd", ID: "6", Hash: hF}}); err != nil {
		t.Fatal(err)
	}
	if z2.reads.Load() != 0 {
		t.Fatalf("no folder-name references, zurg not read: %d", z2.reads.Load())
	}
}

// A torrent with a link under its original name and one under zurg's name: the preview lists both, the override
// names both, and a link under the original name written after the preview is seen by the last check.
func TestDeleteZurgKeepsOriginalNameRefs(t *testing.T) {
	t.Parallel()
	e, z := zurgFixture(t)
	ctx := context.Background()
	z.list = append(z.list, library.ZurgTorrent{Hash: hC, Name: "Ember Road Z", Size: 300})
	e.exec(`INSERT INTO link (path, target, title_id, created_at, updated_at) VALUES ('/lib/E/z.mkv', ?, 1, 0, 0)`,
		mountPath+"/__all__/Ember Road Z/z.mkv")
	plan, err := e.m.PlanDelete(ctx, []DeleteSelection{{Provider: "rd", ID: "3", Hash: hC}})
	if err != nil {
		t.Fatal(err)
	}
	it := plan.Items[0]
	if it.Usage != Used || it.Refs[RefScan] != 1 || it.Refs[RefLink] != 1 || len(it.RefList) != 2 {
		t.Fatalf("both references: %+v", it)
	}
	sel := []DeleteSelection{{Provider: "rd", ID: "3", Hash: hC, Override: true, OverrideToken: it.ExpectedOverride}}
	again, err := e.m.PlanDelete(ctx, sel)
	if err != nil || again.Count != 1 {
		t.Fatalf("override: %v %+v", err, again)
	}
	if why, err := e.m.usageChanged(ctx, again, &again.Items[0], e.scan(ctx)); err != nil || why != "" {
		t.Fatalf("last check agrees: %q %v", why, err)
	}
	// A link under the original name written meanwhile (during the backup): the last check refuses.
	e.exec(`INSERT INTO link (path, target, title_id, created_at, updated_at) VALUES ('/lib/E/o.mkv', ?, 1, 0, 0)`,
		mountPath+"/__all__/Ember.Road.2016.1080p/o.mkv")
	if why, _ := e.m.usageChanged(ctx, again, &again.Items[0], e.scan(ctx)); !strings.Contains(why, "changed") {
		t.Fatalf("last check sees the new link: %q", why)
	}

	// The same through Delete: the link written during the backup stops the delete.
	e2, z2 := zurgFixture(t)
	z2.list = append(z2.list, library.ZurgTorrent{Hash: hC, Name: "Ember Road Z", Size: 300})
	e2.rd.detail["3"].Files = []provider.File{{ID: "1", Path: "e.mkv", Bytes: 300, Selected: true}}
	e2.exec(`INSERT INTO link (path, target, title_id, created_at, updated_at) VALUES ('/lib/E/z.mkv', ?, 1, 0, 0)`,
		mountPath+"/__all__/Ember Road Z/z.mkv")
	first, err := e2.m.PlanDelete(ctx, []DeleteSelection{{Provider: "rd", ID: "3", Hash: hC}})
	if err != nil {
		t.Fatal(err)
	}
	sel = []DeleteSelection{{Provider: "rd", ID: "3", Hash: hC, Override: true,
		OverrideToken: first.Items[0].ExpectedOverride}}
	tok, err := e2.m.PlanDelete(ctx, sel)
	if err != nil || tok.Count != 1 {
		t.Fatalf("override: %v %+v", err, tok)
	}
	reads := e2.rd.reads
	e2.rd.onDetail = func() {
		// The backup's read of the files (after Delete's re-plan read): a link appears under the original name.
		if e2.rd.reads == reads+2 {
			e2.exec(`INSERT INTO link (path, target, title_id, created_at, updated_at) VALUES ('/lib/E/o.mkv', ?, 1,
				0, 0)`, mountPath+"/__all__/Ember.Road.2016.1080p/o.mkv")
		}
	}
	done, err := e2.m.Delete(ctx, sel, tok.Token, "admin")
	if err != nil || e2.ard.deleted != 0 || !strings.Contains(done.Items[0].Error, "changed") {
		t.Fatalf("not deleted: %v, %d, %+v", err, e2.ard.deleted, done)
	}
}

// zurg never touches an unknown torrent: tb never synced stays unknown with its reason though zurg lists its hash
// under a linked folder.
func TestUsageZurgLeavesUnknown(t *testing.T) {
	t.Parallel()
	e, _ := zurgFixture(t)
	e.exec(`DELETE FROM provider_sync_run WHERE provider = 'tb'`)
	r, err := e.m.Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := usages(t, r)["tb/9"]
	if got.Usage != Unknown || len(got.Reasons) == 0 || !strings.Contains(got.Reasons[0], "no complete sync of tb") {
		t.Fatalf("unknown with its reason: %+v", got)
	}
	if rd := usages(t, r)["rd/1"]; rd.Usage != Used {
		t.Fatalf("rd 1 used through zurg: %+v", rd)
	}
}
