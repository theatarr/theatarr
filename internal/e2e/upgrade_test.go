package e2e

import (
	"fmt"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/web"
)

// TestUpgradeApproval: a version with upgrades: approve holds the better release instead of adding it (no provider
// add, no download); after a manager approves it through the native API the release is added, imported, and the old
// link replaced, exactly as an automatic upgrade.
func TestUpgradeApproval(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, func(p *policy.Policy) { p.Versions["movie"].Upgrades = policy.UpgradesApprove })
	low := movieRelease(4, "2160p.WEB.H265")
	high := movieRelease(5, "2160p.UHD.BluRay.REMUX.HEVC")
	e.edge.set(low)
	e.mountRelease(low)
	id := e.addMovie(true, "movie")
	// The first file fills a missing movie: never held.
	e.wanted()
	first := e.finish(1)[0].Links[0]
	if n := e.count(`SELECT count(*) FROM upgrade_hold`); n != 0 {
		t.Fatalf("a missing file was held: %d holds", n)
	}

	e.edge.set(high)
	e.mountRelease(high)
	e.now = e.now.Add(25 * time.Hour)
	e.wanted()
	e.assertUpgradeDecision(id, "movie", high, "WEBDL-2160p", "Remux-2160p")
	adds, _, _ := e.edge.snapshot()
	if len(adds) != 1 || e.count(`SELECT count(*) FROM download`) != 1 {
		t.Fatalf("held upgrade reached a provider: adds %v, %d downloads", adds, e.count(`SELECT count(*) FROM download`))
	}
	var list web.UpgradeHolds
	e.request("GET", web.APIBase+"/upgrades", nil, &list)
	if len(list.Items) != 1 {
		t.Fatalf("holds %+v", list)
	}
	h := list.Items[0]
	if h.Release != high.name || h.Title.ID != id || h.Current.Quality != "WEBDL-2160p" || h.Quality != "Remux-2160p" ||
		!h.CanApprove || h.Reason != "better quality: Remux-2160p over WEBDL-2160p" {
		t.Fatalf("hold %+v", h)
	}
	if status := e.text(`SELECT state FROM version WHERE title_id=?`, id); status != "filled" {
		t.Fatalf("state %s while held", status)
	}

	var approved web.UpgradeHold
	e.request("POST", fmt.Sprintf("%s/upgrades/%d/approve", web.APIBase, h.ID), nil, &approved)
	if approved.State != "approved" || approved.DecidedBy == nil || approved.DecidedVia != "key e2e" {
		t.Fatalf("approved %+v", approved)
	}
	e.drain()
	second := e.finish(1)[0].Links[0]
	if second.Target == first.Target {
		t.Fatal("the approved upgrade still points at the old release")
	}
	if n := e.count(`SELECT count(*) FROM link_trash`); n != 1 {
		t.Fatalf("old link not moved to the trash: %d", n)
	}
	if got := e.text(`SELECT state FROM link WHERE path=?`, first.Path); got != "unlinked" {
		t.Fatalf("old link state=%s, want unlinked", got)
	}
	if state := e.text(`SELECT state FROM version WHERE title_id=?`, id); state != "cutoff_met" {
		t.Fatalf("upgraded movie state=%s, want cutoff_met", state)
	}
	adds, _, _ = e.edge.snapshot()
	if len(adds) != 2 {
		t.Fatalf("adds %v", adds)
	}
	if out := e.text(`SELECT route_outcome FROM upgrade_hold WHERE id=?`, h.ID); out != "added" {
		t.Fatalf("route outcome %q", out)
	}
	if n := e.count(`SELECT count(*) FROM upgrade_hold h JOIN download d ON d.id = h.download_id
		WHERE h.id = ? AND d.state = 'imported'`, h.ID); n != 1 {
		t.Fatal("the hold does not point at its imported download")
	}
}

func (e *environment) assertUpgradeDecision(id int64, version string, rel release, oldQuality, newQuality string) {
	e.t.Helper()
	d, err := e.ac.Decide(e.ctx, decide.Release{Release: indexer.Release{Title: rel.name, InfoHash: rel.hash,
		Indexer: "public", Size: rel.size, Seeders: 20, Privacy: policy.PrivacyPublic}}, id, version)
	must(e.t, err)
	if !d.Upgrade || d.Existing == nil || d.Existing.Quality != oldQuality ||
		d.UpgradeReason != "better quality: "+newQuality+" over "+oldQuality {
		e.t.Fatalf("upgrade decision from Theatarr link: %+v", d)
	}
}
