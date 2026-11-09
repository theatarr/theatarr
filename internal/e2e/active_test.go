package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/arrview"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/web"
)

func TestMovie(t *testing.T) {
	t.Parallel()
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "Radarr", true: "Native"}[native], func(t *testing.T) {
			e := newEnvironment(t, nil)
			public := movieRelease(1, "2160p.WEB.H265")
			e.edge.set(public)
			id := e.addMovie(native, "movie")
			searched, err := e.ac.Interactive(e.ctx, id, "movie", nil, nil)
			must(t, err)
			accepted := false
			for _, d := range searched.Ranked {
				if d.Release == public.name {
					accepted = d.Accepted
				}
			}
			if !accepted {
				t.Fatalf("public accepted=%v: %+v", accepted, searched.Ranked)
			}
			e.mountRelease(public)
			e.wanted()
			plans := e.finish(1)
			if !strings.HasPrefix(plans[0].Links[0].Path, filepath.Join(e.pol.Versions["movie"].Library, "Arrival (2016)", "Arrival (2016) - [WEBDL-2160p]")) {
				t.Fatalf("Radarr naming: %s", plans[0].Links[0].Path)
			}
			e.movieAvailable(id, "movie")
			adds, scans, _ := e.edge.snapshot()
			if len(adds) != 1 || !strings.HasSuffix(adds[0], public.hash) {
				t.Fatalf("provider adds=%v", adds)
			}
			if len(scans) != 1 || scans[0] != filepath.Dir(plans[0].Links[0].Path) {
				t.Fatalf("Plex partial scans: %v", scans)
			}
			e.history(id, "movie", 2)
		})
	}
}

func TestMovieSearchSeederRules(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, nil)
	zero := movieRelease(30, "2160p.WEB.H265")
	zero.name += ".zero"
	zero.seeders, zero.seedersSet = 0, true
	unknown := movieRelease(31, "2160p.WEB.H265")
	unknown.seeders, unknown.seedersSet = -1, true
	e.edge.set(zero, unknown)
	id := e.addMovie(true, "movie")
	got, err := e.ac.Interactive(e.ctx, id, "movie", nil, nil)
	must(t, err)
	for _, d := range got.Ranked {
		switch d.Release {
		case zero.name:
			if d.Accepted {
				t.Errorf("zero seeders accepted: %+v", d)
			}
		case unknown.name:
			if !d.Accepted {
				t.Errorf("unknown seeders rejected: %+v", d)
			}
		}
	}
}

func (e *environment) movieAvailable(id int64, version string) {
	e.t.Helper()
	var detail web.TitleDetail
	e.request("GET", fmt.Sprintf("%s/titles/%d", web.APIBase, id), nil, &detail)
	available := false
	for _, v := range detail.Versions {
		if v.Key == version {
			available = v.HasFile
		}
	}
	if !available {
		e.t.Fatalf("native availability: %+v", detail)
	}
	var movie struct {
		HasFile   bool `json:"hasFile"`
		MovieFile struct {
			Path string `json:"path"`
			ID   int64  `json:"id"`
		} `json:"movieFile"`
	}
	e.request("GET", fmt.Sprintf("/v/%s/radarr/api/v3/movie/%d", version, id+arrview.IDBase), nil, &movie)
	if !movie.HasFile || movie.MovieFile.ID == 0 || movie.MovieFile.Path == "" {
		e.t.Fatalf("Radarr availability: %+v", movie)
	}
}

func (e *environment) history(id int64, version string, minimum int) []web.ActivityEvent {
	e.t.Helper()
	var history struct {
		Records []struct {
			EventType   string `json:"eventType"`
			SourceTitle string `json:"sourceTitle"`
		} `json:"records"`
	}
	e.request("GET", fmt.Sprintf("/v/%s/radarr/api/v3/history?movieId=%d&pageSize=100", version, id+arrview.IDBase), nil, &history)
	if len(history.Records) < minimum {
		e.t.Fatalf("history: %+v", history)
	}
	grabbed, imported := false, false
	for _, event := range history.Records {
		grabbed = grabbed || event.EventType == "grabbed"
		imported = imported || event.EventType == "downloadFolderImported"
	}
	if !grabbed || !imported {
		e.t.Fatalf("missing grab/import history: %+v", history)
	}
	var activity web.ActivityPage
	e.request("GET", web.APIBase+"/activity?state=history", nil, &activity)
	available := false
	for _, event := range activity.History {
		available = available || event.Title.ID == id && event.Version == version
	}
	if !available {
		e.t.Fatalf("native activity: %+v", activity)
	}
	return activity.History
}

func TestMovieTwoVersions(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, func(p *policy.Policy) {
		v := *p.Versions["movie"]
		v.Library = filepath.Join(filepath.Dir(v.Library), "movies-remux")
		v.Profile = "remux"
		p.Versions["remux"] = &v
		p.Profiles["remux"] = &policy.Profile{Qualities: []policy.QualityItem{{Name: "Remux-2160p"}}, Cutoff: "Remux-2160p"}
		p.Library.Versions["remux"] = &policy.LibraryVersion{Roots: []string{v.Library}, PlexSection: 18}
	})
	rel := movieRelease(3, "2160p.UHD.BluRay.REMUX.HEVC")
	e.edge.set(rel)
	e.mountRelease(rel)
	id := e.addMovie(true, "movie", "remux")
	e.wanted()
	plans := e.finish(2)
	if plans[0].Version == plans[1].Version || plans[0].Links[0].Path == plans[1].Links[0].Path {
		t.Fatalf("versions did not get separate files: %+v", plans)
	}
	for _, v := range []string{"movie", "remux"} {
		e.movieAvailable(id, v)
	}
	adds, _, _ := e.edge.snapshot()
	if len(adds) != 1 || e.count(`SELECT count(*) FROM download WHERE reused=1`) != 1 {
		t.Fatalf("shared release added more than once: %v", adds)
	}
	if searches := e.edge.searchCount(); searches != 1 {
		t.Fatalf("shared title searches=%d, want 1", searches)
	}
	if n := e.count(`SELECT count(*) FROM acquire_run WHERE state='routed'`); n != 2 {
		t.Fatalf("decided searches=%d, want 2", n)
	}
}

func TestMovieSearchFanoutKeepsVersionDecisions(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, func(p *policy.Policy) {
		p.Profiles["web-only"] = &policy.Profile{Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p"}
		p.Versions["movie"].Profile = "web-only"
		v := *p.Versions["movie"]
		v.Library = filepath.Join(filepath.Dir(v.Library), "movies-remux")
		v.Profile = "remux-only"
		p.Versions["remux"] = &v
		p.Profiles["remux-only"] = &policy.Profile{Qualities: []policy.QualityItem{{Name: "Remux-2160p"}}, Cutoff: "Remux-2160p"}
		p.Library.Versions["remux"] = &policy.LibraryVersion{Roots: []string{v.Library}, PlexSection: 18}
	})
	web := movieRelease(30, "2160p.WEB.H265")
	remux := movieRelease(31, "2160p.UHD.BluRay.REMUX.HEVC")
	e.edge.set(remux, web)
	e.mountRelease(web)
	e.mountRelease(remux)
	id := e.addMovie(true, "movie", "remux")
	e.wanted()
	e.finish(2)
	if got := e.text(`SELECT info_hash FROM download WHERE version_key='movie'`); got != web.hash {
		t.Fatalf("movie selected %s, want web %s", got, web.hash)
	}
	if got := e.text(`SELECT info_hash FROM download WHERE version_key='remux'`); got != remux.hash {
		t.Fatalf("remux selected %s, want remux %s", got, remux.hash)
	}
	if searches := e.edge.searchCount(); searches != 1 {
		t.Fatalf("shared title searches=%d, want 1", searches)
	}
	adds, _, _ := e.edge.snapshot()
	if len(adds) != 2 {
		t.Fatalf("provider adds=%v, want the two version-specific releases", adds)
	}
	for _, version := range []string{"movie", "remux"} {
		e.movieAvailable(id, version)
	}
}

func TestUpgrade(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, nil)
	low := movieRelease(4, "2160p.WEB.H265")
	high := movieRelease(5, "2160p.UHD.BluRay.REMUX.HEVC")
	e.edge.set(low)
	e.mountRelease(low)
	id := e.addMovie(true, "movie")
	e.wanted()
	first := e.finish(1)[0].Links[0]
	if state := e.text(`SELECT state FROM version WHERE title_id=?`, id); state != "filled" {
		t.Fatalf("below-cutoff movie state=%s, want filled", state)
	}
	e.edge.set(high)
	e.mountRelease(high)
	e.now = e.now.Add(25 * time.Hour)
	e.wanted()
	e.assertUpgradeDecision(id, "movie", high, "WEBDL-2160p", "Remux-2160p")
	second := e.finish(1)[0].Links[0]
	if first.Target == second.Target {
		t.Fatal("upgrade still points at the old release")
	}
	if n := e.count(`SELECT count(*) FROM link_trash`); n != 1 {
		t.Fatalf("old link not retained in trash: %d", n)
	}
	var trashed []string
	must(t, filepath.WalkDir(e.trash, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			trashed = append(trashed, p)
		}
		return nil
	}))
	if len(trashed) != 1 {
		t.Fatalf("trash links=%v", trashed)
	}
	target, err := os.Readlink(trashed[0])
	must(t, err)
	if target != first.Target {
		t.Fatalf("trash points to %s, want %s", target, first.Target)
	}
	_, err = os.Stat(first.Target)
	must(t, err)
	e.movieAvailable(id, "movie")
	upgraded := false
	for _, event := range e.history(id, "movie", 4) {
		upgraded = upgraded || event.Kind == "upgrade" && event.Release == high.name
	}
	if !upgraded {
		t.Fatal("native history has no upgrade event for the better release")
	}
	if state := e.text(`SELECT state FROM version WHERE title_id=?`, id); state != "cutoff_met" {
		t.Fatalf("upgraded movie state=%s, want cutoff_met", state)
	}
	if got := e.text(`SELECT state FROM link WHERE path=?`, first.Path); got != "unlinked" {
		t.Fatalf("old link state=%s, want unlinked", got)
	}
	before := e.count(`SELECT count(*) FROM acquire_run WHERE source='wanted'`)
	sum, err := e.ac.Wanted(e.ctx)
	must(t, err)
	if sum.Due != 0 || sum.Queued != 0 || e.count(`SELECT count(*) FROM acquire_run WHERE source='wanted'`) != before {
		t.Fatalf("cutoff-met version was searched again: %+v", sum)
	}
	adds, _, _ := e.edge.snapshot()
	if len(adds) != 2 {
		t.Fatalf("upgrade adds=%v", adds)
	}
}

func TestProviderFallback(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, func(p *policy.Policy) {
		second := *p.Providers["rd"]
		second.Order = 2
		second.URL = strings.TrimSuffix(second.URL, "/rd") + "/backup"
		p.Providers["backup"] = &second
		p.Versions["movie"].Providers = []string{"rd", "backup"}
	})
	e.edge.refuse = true
	rel := movieRelease(6, "2160p.WEB.H265")
	e.edge.set(rel)
	e.mountRelease(rel)
	e.addMovie(false, "movie")
	e.wanted()
	e.finish(1)
	adds, _, _ := e.edge.snapshot()
	if len(adds) != 2 || !strings.HasPrefix(adds[0], "/rd/") || !strings.HasPrefix(adds[1], "/backup/") || e.text(`SELECT provider FROM download`) != "backup" {
		t.Fatalf("fallback order: %v", adds)
	}
}

func TestNeverReady(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, func(p *policy.Policy) {
		cached := false
		stalled := policy.Duration(time.Minute)
		p.Router.CachedOnly = &cached
		p.Router.StalledAfter = &stalled
	})
	e.edge.stalled = true
	rel := movieRelease(7, "2160p.WEB.H265")
	e.edge.set(rel)
	e.addMovie(true, "movie")
	e.wanted()
	if state := e.text(`SELECT state FROM download`); state != "added" {
		t.Fatalf("initial state=%s", state)
	}
	e.now = e.now.Add(2 * time.Minute)
	_, err := e.rt.Poll(e.ctx)
	must(t, err)
	if state := e.text(`SELECT state FROM download`); state != "failed" {
		t.Fatalf("stalled state=%s", state)
	}
	if n := e.count(`SELECT count(*) FROM router_blocklist WHERE info_hash=?`, rel.hash); n != 1 {
		t.Fatalf("stalled release not blocklisted: %d", n)
	}
	if n := e.count(`SELECT count(*) FROM link`); n != 0 {
		t.Fatalf("stalled release linked: %d", n)
	}
}

func TestMissingMountFolder(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, nil)
	rel := movieRelease(8, "2160p.WEB.H265")
	e.edge.set(rel)
	e.addMovie(true, "movie")
	e.wanted()
	_, err := e.rt.Poll(e.ctx)
	must(t, err)
	id := int64(e.count(`SELECT id FROM download`))
	plan, err := e.lib.Import(e.ctx, id, false)
	must(t, err)
	if !plan.Waiting || e.text(`SELECT state FROM download WHERE id=?`, id) == "failed" || e.count(`SELECT count(*) FROM router_blocklist`) != 0 {
		t.Fatalf("missing mount must wait: %+v", plan)
	}
	if state := e.text(`SELECT state FROM library_import WHERE download_id=?`, id); state != "waiting_mount" {
		t.Fatalf("import state=%s", state)
	}
	e.mountRelease(rel)
	e.now = e.now.Add(30 * time.Second)
	e.finish(1)
}

func TestGates(t *testing.T) {
	t.Parallel()
	for _, gate := range []string{"router_disabled", "shadow"} {
		t.Run(gate, func(t *testing.T) {
			e := newEnvironment(t, func(p *policy.Policy) {
				if gate == "router_disabled" {
					p.Router.Enabled = false
				} else {
					p.Versions["movie"].Mode = policy.ModeShadow
				}
			})
			rel := movieRelease(9, "2160p.WEB.H265")
			e.edge.set(rel)
			e.mountRelease(rel)
			id := e.addMovie(true, "movie")
			_, _, err := e.ac.Submit(e.ctx, 0, acquire.Selection{Versions: []string{"movie"}, TitleIDs: []int64{id}})
			must(t, err)
			e.drain()
			// Interactive search is also how the shadow runner builds the dry-run route request.
			searched, err := e.ac.Interactive(e.ctx, id, "movie", nil, nil)
			must(t, err)
			if len(searched.Request.Candidates) == 0 {
				t.Fatal("gate fixture did not produce an accepted candidate")
			}
			result, err := e.rt.Route(e.ctx, searched.Request)
			must(t, err)
			t.Logf("dry-run route: %+v", result)
			if e.count(`SELECT count(*) FROM router_plan`) == 0 {
				t.Fatal("missing dry-run route plan")
			}
			if e.count(`SELECT count(*) FROM download`) != 0 || e.count(`SELECT count(*) FROM link`) != 0 {
				t.Fatal("gate created a download or link")
			}
			adds, scans, _ := e.edge.snapshot()
			if len(adds) != 0 || len(scans) != 0 {
				t.Fatalf("gate wrote to edges: adds=%v scans=%v", adds, scans)
			}
			entries, err := os.ReadDir(e.pol.Versions["movie"].Library)
			must(t, err)
			if len(entries) != 0 {
				t.Fatal("gate wrote library files")
			}
		})
	}
}

// TestClientRoutedMovie: a version with add_via: client sends its grab to the fixture decypharr client, never to
// a provider, and imports the finished torrent from the debrid mount.
func TestClientRoutedMovie(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, func(p *policy.Policy) {
		p.Versions["movie"].AddVia = policy.AddViaClient
		p.Versions["movie"].Providers = nil
	})
	public := movieRelease(1, "2160p.WEB.H265")
	e.edge.set(public)
	e.edge.client.OnAdd = func(hash, category string) map[string]any {
		return map[string]any{"name": public.name, "progress": 1, "state": "pausedUP", "category": category}
	}
	id := e.addMovie(true, "movie")
	e.mountRelease(public)
	e.wanted()
	if got := e.text(`SELECT state || ' ' || client || ' ' || provider_torrent_id || ' ' || (provider IS NULL) FROM download`); got != "added fixture "+public.hash+" 1" {
		t.Fatalf("download %q\n%s", got, e.logs.String())
	}
	plans := e.finish(1)
	if !strings.HasPrefix(plans[0].Links[0].Target, filepath.Join(e.mount, "__all__", public.name)) {
		t.Fatalf("link target %s", plans[0].Links[0].Target)
	}
	e.movieAvailable(id, "movie")
	if adds, _, _ := e.edge.snapshot(); len(adds) != 0 {
		t.Fatalf("provider adds %v", adds)
	}
	if n := e.edge.client.CallCount("/api/v2/torrents/add"); n != 1 {
		t.Fatalf("client adds %d", n)
	}
}
