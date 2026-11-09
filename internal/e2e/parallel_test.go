package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/importarr"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
)

// TestParallelVersion runs a parallel version beside the arr (docs/parallel.md): the arr import gives the follower
// the followed version's titles and monitoring but none of its files, adopt links the arr's files into the
// follower's own root, and a new episode is grabbed and linked for the follower only.
func TestParallelVersion(t *testing.T) {
	t.Parallel()
	var arrRoot, theatarrRoot string
	e := newEnvironment(t, func(p *policy.Policy) {
		seriesPolicy(false)(p)
		tv := p.Versions["tv"]
		arrRoot = tv.Library
		tv.Mode = policy.ModeShadow
		f := *tv
		theatarrRoot = filepath.Join(filepath.Dir(arrRoot), "theatarr", "tv")
		f.Mode, f.Grab, f.Follows, f.Library, f.Naming = policy.ModeActive, true, "tv", theatarrRoot, policy.Naming{FromVersion: "tv"}
		p.Versions["tv-theatarr"] = &f
		p.Library.Versions["tv-theatarr"] = &policy.LibraryVersion{Roots: []string{theatarrRoot}, PlexSection: 18}
	})
	pack := release{name: "Severance.S01.2160p.WEB.H265-GRP", hash: fmt.Sprintf("%040x", 20), season: 1,
		files: []string{"Severance.S01E01E02.2160p.WEB.H265-GRP.mkv", "Severance.S01E03.2160p.WEB.H265-GRP.mkv"}}
	e.mountRelease(pack)

	// The arr's own links and its snapshot: S01 on disk, S02E01 airs tomorrow.
	show := filepath.Join(arrRoot, "Severance (2022)")
	var files []*importarr.File
	for i, name := range pack.files {
		p := filepath.Join(show, "Season 01", strings.Replace(name, "Severance.", "Severance - ", 1))
		must(t, os.MkdirAll(filepath.Dir(p), 0755))
		must(t, os.Symlink(filepath.Join(e.mount, "__all__", pack.name, name), p))
		eps := []int{103}
		if i == 0 {
			eps = []int{101, 102}
		}
		files = append(files, &importarr.File{ArrID: 900 + i, Path: p, RelativePath: "Season 01/" + filepath.Base(p),
			Quality: "WEBDL-2160p", Revision: 1, ReleaseGroup: "GRP", SceneName: strings.TrimSuffix(name, ".mkv"),
			Languages: []string{"English"}, EpisodeArrIDs: eps})
	}
	aired := e.now.Add(-30 * 24 * time.Hour)
	tomorrow := e.now.Add(24 * time.Hour)
	snap := &importarr.Snapshot{Instance: "sonarr_a", Flavour: "sonarr", Titles: []importarr.Title{{
		ArrID: 1, Kind: "series", Name: "Severance", Year: 2022, TvdbID: 371980, Monitored: true, SeriesType: "standard", Runtime: 55,
		Path: show, MonitorNewItems: "all", SeasonsMonitored: map[int]bool{1: true, 2: true},
		Episodes: []importarr.Episode{
			{ArrID: 101, Season: 1, Number: 1, Name: "Good News About Hell", AirDateUTC: &aired, Monitored: true, FileArrID: 900},
			{ArrID: 102, Season: 1, Number: 2, Name: "Half Loop", AirDateUTC: &aired, Monitored: true, FileArrID: 900},
			{ArrID: 103, Season: 1, Number: 3, Name: "In Perpetuity", AirDateUTC: &aired, Monitored: true, FileArrID: 901},
			{ArrID: 201, Season: 2, Number: 1, Name: "Hello Ms Cobel", AirDateUTC: &tomorrow, Monitored: true},
		},
		Files: files,
	}}}
	im := &importarr.Importer{Store: e.st, Now: func() time.Time { return e.now }}
	sum, err := im.Write(e.ctx, importarr.Source{Instance: "sonarr_a", Flavour: "sonarr", Version: "tv",
		Followers: e.pol.Followers("tv")}, snap)
	must(t, err)
	if sum.Followed.Created != 1 {
		t.Fatalf("followed %+v", sum.Followed)
	}
	if n := e.count(`SELECT count(*) FROM version_file vf JOIN version v ON v.id = vf.version_id WHERE v.policy_key = 'tv-theatarr'`); n != 0 {
		t.Fatalf("follower got %d arr files", n)
	}
	if n := e.count(`SELECT count(*) FROM episode_version_state s JOIN version v ON v.id = s.version_id
		WHERE v.policy_key = 'tv-theatarr' AND s.monitored = 1`); n != 4 {
		t.Fatalf("follower monitors %d episodes, want 4", n)
	}

	// Adopt: Theatarr's own links under the follower root, same targets; the arr's links are untouched.
	rep, err := e.lib.Adopt(e.ctx, library.AdoptOptions{Version: "tv-theatarr"})
	must(t, err)
	if rep.Linked != 2 || len(rep.Skipped) != 0 {
		t.Fatalf("adopt %+v", rep)
	}
	for _, l := range rep.Links {
		if !strings.HasPrefix(l.Path, theatarrRoot+"/Severance (2022)/Season 01/") {
			t.Fatalf("adopted link %s outside the follower root", l.Path)
		}
		target, err := os.Readlink(l.Path)
		must(t, err)
		if !strings.HasPrefix(target, filepath.Join(e.mount, "__all__", pack.name)) {
			t.Fatalf("adopted link %s -> %s", l.Path, target)
		}
	}
	e.drain()
	// WEBDL-2160p is below the Remux cutoff: the adopted episodes have a file and stay upgradable, as in the arr.
	if s := e.text(`SELECT group_concat(s.state, ' ') FROM (SELECT s.state FROM episode_version_state s
		JOIN version v ON v.id = s.version_id JOIN episode ep ON ep.id = s.episode_id
		WHERE v.policy_key = 'tv-theatarr' ORDER BY ep.season, ep.number) s`); s != "filled filled filled wanted" {
		t.Fatalf("follower episode states %q", s)
	}
	again, err := e.lib.Adopt(e.ctx, library.AdoptOptions{Version: "tv-theatarr"})
	must(t, err)
	if again.Linked != 0 || again.Existing != 2 {
		t.Fatalf("second adopt %+v", again)
	}

	// S02E01 airs: Theatarr searches and grabs for the follower, never for the shadow version the arr still serves.
	single := release{name: "Severance.S02E01.2160p.WEB.H265-GRP", hash: fmt.Sprintf("%040x", 21), season: 2,
		files: []string{"Severance.S02E01.2160p.WEB.H265-GRP.mkv"}}
	e.edge.set(single)
	e.mountRelease(single)
	e.now = e.now.Add(26 * time.Hour)
	res, err := e.ac.Wanted(e.ctx)
	must(t, err)
	e.drain()
	if n := e.count(`SELECT count(*) FROM download`); n != 1 {
		t.Fatalf("downloads %d after wanted %+v\n%s", n, res, e.logs.String())
	}
	e.finish(1)
	if n := e.count(`SELECT count(*) FROM acquire_run r JOIN version v ON v.id = r.version_id WHERE v.policy_key = 'tv'`); n != 0 {
		t.Fatalf("the shadow version was searched %d times", n)
	}
	var linked []string
	rows, err := e.st.Reader().QueryContext(e.ctx, `SELECT l.path FROM link l JOIN link_version lv ON lv.link_id = l.id
		JOIN version v ON v.id = lv.version_id WHERE l.state = 'active' ORDER BY l.path`)
	must(t, err)
	for rows.Next() {
		var p string
		must(t, rows.Scan(&p))
		if !strings.HasPrefix(p, theatarrRoot+"/") {
			t.Fatalf("theatarr wrote %s outside the follower root", p)
		}
		linked = append(linked, p)
	}
	must(t, rows.Err())
	rows.Close()
	if len(linked) != 3 || !strings.Contains(linked[2], "Season 02") {
		t.Fatalf("follower links %v", linked)
	}
	adds, _, _ := e.edge.snapshot()
	if len(adds) != 1 {
		t.Fatalf("debrid adds %v", adds)
	}
	// The arr's own links are where they were.
	for _, f := range files {
		if _, err := os.Lstat(f.Path); err != nil {
			t.Fatalf("arr link gone: %v", err)
		}
	}
}
