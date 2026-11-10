package torrents

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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

	// The delete preview and its last check agree without a read; a hash zurg does not list still reads its
	// original name (rd 3: used under it).
	plan, err := e.m.PlanDelete(ctx, []DeleteSelection{{Provider: "rd", ID: "1", Hash: hA},
		{Provider: "rd", ID: "3", Hash: hC}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Items[0].Usage != Used || plan.Items[1].Usage != Used || e.rd.reads != 1 {
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
