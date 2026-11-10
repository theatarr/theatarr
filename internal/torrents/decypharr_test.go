package torrents

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/library/decypharrtest"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
)

// plainDoer sends a request as it is (no gateway).
type plainDoer struct{}

func (plainDoer) Do(ctx context.Context, _ gateway.Target, req *http.Request) (*http.Response, error) {
	return http.DefaultClient.Do(req.WithContext(ctx))
}

// decypharrFixture: the mount is decypharr's. rd holds 1 and 4, tb holds 2; decypharr lists 1 and 2 under one
// folder, which a library link points into (no hash recorded), and does not list 4 (same name on the same debrid as
// 1). rd 6 is in the registry with no reference and listed by decypharr; rd 7 is a second hidden torrent of the
// folder whose name on the debrid is another one, so nothing leads from it to the link.
func decypharrFixture(t *testing.T) (*env, *decypharrtest.Server) {
	e := newEnv(t)
	old := e.now.Add(-72 * time.Hour)
	e.pol.Library.Mount.Decypharr = &policy.LibraryDecypharr{URL: "http://decypharr.invalid"}
	folder := decypharrtest.SharedName
	e.rd.list = []provider.Torrent{
		tor("1", hA, "marrow.lantern.2014.1080p.bluray.x264-fernwood.mkv", 100, old, provider.StateDownloaded),
		tor("4", hD, folder, 400, old, provider.StateDownloaded),
		tor("6", hF, "Hollow.Meridian.2018.2160p.WEB-DL", 600, old, provider.StateDownloaded),
		tor("7", hG, "Marrow Lantern 2014 1080p", 700, old, provider.StateDownloaded),
	}
	e.tb.list = []provider.Torrent{tor("2", hB, "Marrow Lantern (2014)", 200, old, provider.StateDownloaded)}
	e.synced("rd")
	e.synced("tb")
	for _, h := range []string{hA, hB, hD, hF, hG} {
		e.content(h, 1)
	}
	e.exec(`INSERT INTO link (path, target, title_id, created_at, updated_at) VALUES ('/lib/M/M.mkv', ?, 1, 0, 0)`,
		mountPath+"/__all__/"+folder+"/M.mkv")
	srv := decypharrtest.New(t)
	srv.Set(func(s *decypharrtest.Server) {
		s.Providers = []string{"realdebrid", "torbox"}
		s.Torrents = []decypharrtest.Torrent{
			{Hash: hA, Name: folder, Provider: "realdebrid"},
			{Hash: hB, Name: folder, Provider: "torbox"},
			{Hash: hD, Name: folder, Provider: "realdebrid", Unlisted: true},
			{Hash: hG, Name: folder, Provider: "realdebrid", Unlisted: true},
			{Hash: hF, Name: "Hollow.Meridian.2018.2160p.WEB-DL", Provider: "realdebrid"},
		}
	})
	e.m.Decypharr = &library.Decypharr{Config: library.DecypharrConfig{URL: srv.URL(), Interval: time.Minute},
		GW: plainDoer{}, Now: func() time.Time { return e.now }}
	return e, srv
}

// TestUsageDecypharr: with library.mount.decypharr every hash decypharr lists under a linked folder's name is used,
// whatever the provider calls the torrent; a hash it does not list is still found by its name, and without a
// reference it is unknown (the folder it is under is not known); so is every torrent without references while the
// index cannot be read. Neither is ever unused.
func TestUsageDecypharr(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e, srv := decypharrFixture(t)
	r, err := e.m.Report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		prov, id string
		want     Usage
	}{{"rd", "1", Used}, {"tb", "2", Used}, {"rd", "4", Used}, {"rd", "6", Unused}, {"rd", "7", Unknown}} {
		if got := find(r, c.prov, c.id); got == nil || got.Usage != c.want {
			t.Errorf("%s %s: %+v, want %s", c.prov, c.id, got, c.want)
		}
	}

	if got := find(r, "rd", "7"); got != nil && !strings.Contains(strings.Join(got.Reasons, ";"), "decypharr does not list it") {
		t.Errorf("hidden hash: %+v", got)
	}
	if len(r.Unused) != 1 {
		t.Errorf("unused: %+v", r.Unused)
	}

	// The delete's last check reads the same names: a used torrent stays used, and an index that cannot be read any
	// more stops the delete of the unused one.
	plan, err := e.m.PlanDelete(ctx, []DeleteSelection{{Provider: "rd", ID: "6", Hash: hF}, {Provider: "rd", ID: "1", Hash: hA}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Count != 1 || !plan.Items[1].NeedsOverride {
		t.Fatalf("plan: %+v", plan.Items)
	}
	for i := range plan.Items {
		if why, err := e.m.usageChanged(ctx, plan, &plan.Items[i], newScanIndex()); err != nil || why != "" {
			t.Errorf("item %d: usage changed (%q, %v) with nothing changed", i, why, err)
		}
	}
	// decypharr stops listing the unused one (a same-named torrent took its place): no longer unused at the delete.
	e.now = e.now.Add(2 * time.Minute)
	srv.Set(func(s *decypharrtest.Server) { s.Torrents[4].Unlisted = true })
	if why, err := e.m.usageChanged(ctx, plan, &plan.Items[0], newScanIndex()); err != nil || !strings.Contains(why, "unknown") {
		t.Errorf("hash no longer listed at the delete: %q, %v", why, err)
	}
	e.now = e.now.Add(2 * time.Minute)
	srv.Set(func(s *decypharrtest.Server) { s.Fail = 500 })
	if why, err := e.m.usageChanged(ctx, plan, &plan.Items[0], newScanIndex()); err != nil || !strings.Contains(why, "unknown") {
		t.Errorf("index unknown at the delete: %q, %v", why, err)
	}
	r, err = e.m.Report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := find(r, "rd", "6"); got.Usage != Unknown || !strings.Contains(strings.Join(got.Reasons, ";"), "decypharr") {
		t.Errorf("index unknown: %+v", got)
	}
	if got := find(r, "rd", "4"); got.Usage != Used {
		t.Errorf("references found still count: %+v", got)
	}
	if len(r.Unused) != 0 {
		t.Errorf("nothing is unused: %+v", r.Unused)
	}

	// Without a client (a command without a gateway) nothing is known either.
	e.m.Decypharr = nil
	if r, err = e.m.Report(ctx); err != nil {
		t.Fatal(err)
	}
	if got := find(r, "rd", "6"); got.Usage != Unknown {
		t.Errorf("no client: %+v", got)
	}
}
