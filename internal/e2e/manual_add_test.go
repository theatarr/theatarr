package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/arrview"
	"github.com/theatarr/theatarr/internal/indexer"
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
