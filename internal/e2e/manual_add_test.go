package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/arrview"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/web"
)

// viewOps gives the web API the arr views' service for a grab and a manual add, as cmd/theatarr's serveOps does.
// The other recovery actions are not exercised here.
type viewOps struct{ av *arrview.Service }

func grabbed(g *arrview.Grabbed, err error) (*web.GrabbedRelease, error) {
	if g == nil {
		return nil, err
	}
	return &web.GrabbedRelease{Decision: g.Decision, Overridden: g.Overridden, Job: g.Job}, err
}

func (o viewOps) GrabRelease(ctx context.Context, id int64, version string, rel indexer.Release, override bool,
	source string) (*web.GrabbedRelease, error) {
	return grabbed(o.av.GrabRelease(ctx, id, version, rel, override, source))
}

func (o viewOps) AddRelease(ctx context.Context, id int64, version string, rel indexer.Release, override bool,
	source string) (*web.GrabbedRelease, error) {
	return grabbed(o.av.AddRelease(ctx, id, version, rel, override, source))
}

func (o viewOps) RetryDownload(context.Context, int64, string) (*web.RetriedDownload, error) {
	return nil, fmt.Errorf("e2e: retry is not wired")
}

func (o viewOps) UnlinkLink(context.Context, int64, string) (*web.UnlinkedLink, error) {
	return nil, fmt.Errorf("e2e: unlink is not wired")
}

// TestManualAddByHash: a release handed over by its hash (POST /titles/{id}/releases/add), which no search found
// and which has no indexer (it carries "manual add"), goes the whole way: decided and accepted without override, added once at the provider,
// found ready, linked into the library, the title filled, and the arr history and the activity show the grab and
// the import. No indexer is searched and no torrent is deleted.
func TestManualAddByHash(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, nil)
	rel := movieRelease(7, "2160p.WEB.H265")
	e.edge.set(rel) // the provider knows the hash; the indexer is never asked
	id := e.addMovie(true, "movie")
	var got web.GrabResult
	e.request("POST", fmt.Sprintf("%s/titles/%d/releases/add", web.APIBase, id),
		web.AddReleaseRequest{Version: "movie", InfoHash: strings.ToUpper(rel.hash), Name: rel.name}, &got)
	if !got.Accepted || got.Overridden || got.InfoHash != rel.hash || got.Release != rel.name {
		t.Fatalf("add: %+v", got)
	}
	e.drain()
	if row := e.text(`SELECT state || '|' || indexer || '|' || provider || '|' || source FROM download WHERE info_hash = ?`,
		rel.hash); row != "added|manual add|rd|ui:grab:key:e2e" && row != "ready|manual add|rd|ui:grab:key:e2e" {
		t.Fatalf("download %q\n%s", row, e.logs.String())
	}
	e.mountRelease(rel)
	plans := e.finish(1)
	if len(plans[0].Links) != 1 {
		t.Fatalf("links: %+v", plans[0].Links)
	}
	e.movieAvailable(id, "movie")
	if state := e.text(`SELECT state FROM version WHERE title_id = ? AND policy_key = 'movie'`, id); state == "wanted" {
		t.Fatalf("the copy is still wanted")
	}
	grab := false
	for _, ev := range e.history(id, "movie", 2) {
		grab = grab || ev.Kind == "grab" && ev.Release == rel.name && ev.Provider == "rd"
	}
	if !grab {
		t.Errorf("the activity has no grab of the release")
	}
	if n := e.count(`SELECT count(*) FROM journal_action WHERE action = 'grab' AND title_id = ? AND target = 'manual add'
		AND info_hash = ?`, id, rel.hash); n != 1 {
		t.Errorf("%d journal actions for the manual add", n)
	}
	adds, _, _ := e.edge.snapshot()
	if len(adds) != 1 || !strings.HasSuffix(adds[0], rel.hash) || !strings.HasPrefix(adds[0], "/rd/") {
		t.Fatalf("provider adds %v", adds)
	}
	e.edge.mu.Lock()
	deletes := append([]string(nil), e.edge.deletes...)
	e.edge.mu.Unlock()
	if len(deletes) != 0 {
		t.Fatalf("provider deletes %v", deletes)
	}
	if n := e.edge.searchCount(); n != 0 {
		t.Fatalf("%d indexer searches for a manual add", n)
	}
}

// TestFailedManualAddStartsNoSearch: with cached_only on (the default), a release the provider does not have cached is
// a miss on every provider. For a release a person added by magnet or hash that ends it: no re-search is queued, no
// indexer is asked and no follow-up search is scheduled, although another release of the title is there to be found.
// The same release grabbed by hand from a search (GrabRelease) is searched again, as before.
func TestFailedManualAddStartsNoSearch(t *testing.T) {
	t.Parallel()
	for _, manual := range []bool{true, false} {
		e := newEnvironment(t, nil)
		e.edge.stalled = true // never cached: Real-Debrid keeps converting the magnet
		rel, other := movieRelease(9, "2160p.WEB.H265"), movieRelease(10, "2160p.WEB.H265")
		other.name = "Arrival.2016.2160p.WEB.H265-OTHER"
		e.edge.set(rel, other)
		id := e.addMovie(true, "movie")
		if manual {
			var got web.GrabResult
			e.request("POST", fmt.Sprintf("%s/titles/%d/releases/add", web.APIBase, id),
				web.AddReleaseRequest{Version: "movie", InfoHash: rel.hash, Name: rel.name}, &got)
			if !got.Accepted {
				t.Fatalf("add: %+v", got)
			}
		} else {
			_, err := e.av.GrabRelease(e.ctx, id, "movie", indexer.Release{Title: rel.name, InfoHash: rel.hash,
				Indexer: "public", Kind: "torznab", Privacy: "public", Size: 20 << 30, Seeders: 20, Peers: 20}, false,
				"ui:grab:key:e2e")
			must(t, err)
		}
		e.drain()
		if row := e.text(`SELECT state || '|' || error FROM download WHERE info_hash = ?`, rel.hash); row != "failed|miss on every provider" {
			t.Fatalf("manual %v: download %q\n%s", manual, row, e.logs.String())
		}
		research := e.count(`SELECT count(*) FROM job WHERE kind = ?`, router.JobResearch)
		searches := e.edge.searchCount()
		others := e.count(`SELECT count(*) FROM download WHERE info_hash = ?`, other.hash)
		if manual && (research != 0 || searches != 0 || others != 0) {
			t.Errorf("a failed manual add: %d re-search jobs, %d indexer searches, %d other downloads, want none\n%s",
				research, searches, others, e.logs.String())
		}
		if !manual && (research == 0 || searches == 0 || others == 0) {
			t.Errorf("a failed manual grab: %d re-search jobs, %d indexer searches, %d other downloads, want a search as before",
				research, searches, others)
		}
	}
}

// TestManualAddUnknownSizeAccepted: at a quality with a size limit (Radarr's default WEBDL-1080p maximum) a manual
// add, whose size nothing reports, is accepted without override (the person picked the release) and goes the whole
// way: added, found ready, linked, the title filled.
func TestManualAddUnknownSizeAccepted(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, func(p *policy.Policy) {
		p.Profiles["movie"].Qualities = append(p.Profiles["movie"].Qualities, policy.QualityItem{Name: "WEBDL-1080p"})
	})
	rel := movieRelease(12, "1080p.WEB.H265")
	e.edge.set(rel)
	id := e.addMovie(true, "movie")
	var got web.GrabResult
	e.request("POST", fmt.Sprintf("%s/titles/%d/releases/add", web.APIBase, id),
		web.AddReleaseRequest{Version: "movie", InfoHash: rel.hash, Name: rel.name}, &got)
	if !got.Accepted || got.Overridden {
		t.Fatalf("add: %+v", got)
	}
	e.drain()

	e.mountRelease(rel)
	if plans := e.finish(1); len(plans[0].Links) != 1 {
		t.Fatalf("links: %+v", plans[0].Links)
	}
	e.movieAvailable(id, "movie")
}
