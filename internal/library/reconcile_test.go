package library

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/policy"
)

// An upgrade that moves the arr's file aside drops its version_file row, as the arr deletes the
// file it upgrades: a later removal of the new link then leaves the version wanted, not filled by a phantom.
func TestUpgradeRetiresArrFile(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.release("Old.Release", map[string]float64{"old.mkv": 7000})
	folder := filepath.Join(e.movies, "Amelie The Movie (2001)")
	old := filepath.Join(folder, "Amelie The Movie (2001) - old.mkv")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(e.mount, "__all__", "Old.Release", "old.mkv"), old); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path) VALUES (5, 10, 1, 'radarr', 1, ?)`, old)
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if n := e.one(`SELECT COUNT(*) FROM version_file`); n != "0" {
		t.Fatalf("arr file rows left: %s", n)
	}
	var linkID int64
	if err := e.st.Reader().QueryRowContext(e.ctx, `SELECT id FROM link WHERE path = ?`, plan.Links[0].Path).Scan(&linkID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.lib.Unlink(e.ctx, "movie-remux", plan.Links[0].Path, linkID, "test"); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT state FROM version WHERE id = 10`); got != "wanted" {
		t.Fatalf("state after the new link went: %s, want wanted", got)
	}
}

// A partial upgrade keeps the multi-episode arr file and both episodes' states.
func TestUpgradeRetiresArrEpisodeFile(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	rel := "Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP"
	e.release(rel, map[string]float64{"Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2700})
	e.release("Old", map[string]float64{"old.mkv": 2700})
	old := filepath.Join(e.shows, "Show (2020)", "Season 01", "Show - S01E01-E02.mkv")
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(e.mount, "__all__", "Old", "old.mkv"), old); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path) VALUES (6, 20, 2, 'sonarr', 1, ?)`, old)
	e.exec(`INSERT INTO version_file_episode (version_file_id, episode_id) VALUES (6, 201), (6, 202)`)
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, cutoff_met, updated_at)
		VALUES (20, 201, 1, 'filled', 0, 0), (20, 202, 1, 'filled', 0, 0)`)
	e.exec(`UPDATE version SET state = 'filled' WHERE id = 20`)
	e.seriesDownload(1, 2, 20, hashA, rel, "[201]", nil)
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if n := e.one(`SELECT COUNT(*) FROM version_file`); n != "1" {
		t.Fatalf("arr episode file removed: %s", n)
	}
	if _, err := os.Readlink(old); err != nil {
		t.Fatalf("arr episode symlink removed: %v", err)
	}
	if got := e.one(`SELECT group_concat(episode_id || ':' || state, ' ') FROM
		(SELECT * FROM episode_version_state WHERE version_id = 20 ORDER BY episode_id)`); got != "201:filled 202:filled" {
		t.Fatalf("episode states %s", got)
	}
}

// A link whose symlink was deleted stops counting: the version is wanted again. A root
// that is missing or empty is skipped whole, and a shadow version only reports.
func TestReconcileVanishedLinks(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	path := plan.Links[0].Path
	// Something else keeps the root non-empty.
	if err := os.MkdirAll(filepath.Join(e.movies, "Other (1999)"), 0o755); err != nil {
		t.Fatal(err)
	}
	scans, err := e.lib.ReconcileLinks(e.ctx, "", nil)
	if err != nil || len(scans) != 2 || len(scans[0].Vanished)+len(scans[1].Vanished) != 0 {
		t.Fatalf("intact library: %+v %v", scans, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	// Shadow: reported, nothing written.
	e.pol.Versions["movie-remux"].Mode = policy.ModeShadow
	e.lib.gate = NewGate(e.pol)
	scans, err = e.lib.ReconcileLinks(e.ctx, "movie-remux", nil)
	if err != nil || len(scans) != 1 || len(scans[0].Vanished) != 1 || !scans[0].DryRun {
		t.Fatalf("shadow: %+v %v", scans, err)
	}
	if got := e.one(`SELECT state FROM link`) + e.one(`SELECT state FROM version WHERE id = 10`); got != "activecutoff_met" {
		t.Fatalf("shadow wrote: %s", got)
	}
	e.pol.Versions["movie-remux"].Mode = policy.ModeActive
	e.lib.gate = NewGate(e.pol)

	// Another title only: this link is not looked at.
	if scans, err = e.lib.ReconcileLinks(e.ctx, "movie-remux", []int64{99}); err != nil || len(scans[0].Vanished) != 0 {
		t.Fatalf("other title: %+v %v", scans, err)
	}
	scans, err = e.lib.ReconcileLinks(e.ctx, "movie-remux", []int64{1})
	if err != nil || len(scans[0].Vanished) != 1 || scans[0].Vanished[0].Path != path {
		t.Fatalf("vanished: %+v %v", scans, err)
	}
	if got := e.one(`SELECT state FROM link`) + "/" + e.one(`SELECT state FROM version WHERE id = 10`); got != "unlinked/wanted" {
		t.Fatalf("after reconcile: %s", got)
	}
	if n := e.one(`SELECT COUNT(*) FROM library_action WHERE kind = 'dead' AND mode = 'applied'`); n != "1" {
		t.Errorf("actions %s", n)
	}
	// History's File missing row names the file (00129): the link is unlinked, so nothing else would.
	if got := e.one(`SELECT release_title FROM library_action WHERE kind = 'dead' AND mode = 'applied'`); got != movieRelease {
		t.Errorf("dead action release %q", got)
	}
}

func TestReconcileSkipsUnavailableRoots(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	// An emptied root (an unmounted share shows as an empty directory): nothing is marked missing.
	if err := os.RemoveAll(e.movies); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e.movies, 0o755); err != nil {
		t.Fatal(err)
	}
	scans, err := e.lib.ReconcileLinks(e.ctx, "movie-remux", nil)
	if err != nil || scans[0].Skipped == "" || len(scans[0].Vanished) != 0 {
		t.Fatalf("empty root: %+v %v", scans, err)
	}
	// A missing root: the same.
	if err := os.Remove(e.movies); err != nil {
		t.Fatal(err)
	}
	scans, err = e.lib.ReconcileLinks(e.ctx, "movie-remux", nil)
	if err != nil || scans[0].Skipped == "" || len(scans[0].Vanished) != 0 {
		t.Fatalf("missing root: %+v %v", scans, err)
	}
	if got := e.one(`SELECT state FROM link`) + "/" + e.one(`SELECT state FROM version WHERE id = 10`); got != "active/cutoff_met" {
		t.Fatalf("an unavailable root changed the link: %s", got)
	}
}

// The relink sweep reconciles too; a pure dry run only reports.
func TestRelinkSweepReconciles(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(plan.Links[0].Path); err != nil {
		t.Fatal(err)
	}
	scans, err := e.lib.Relink(e.ctx, "movie-remux", true)
	if err != nil || len(scans[0].Vanished) != 1 {
		t.Fatalf("dry run %+v %v", scans, err)
	}
	if got := e.one(`SELECT state FROM link`); got != "active" {
		t.Fatalf("dry run wrote %s", got)
	}
	if _, err := e.lib.Relink(e.ctx, "movie-remux", false); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT state FROM link`) + "/" + e.one(`SELECT state FROM version WHERE id = 10`); got != "unlinked/wanted" {
		t.Fatalf("after the sweep: %s", got)
	}
	if _, err := os.Lstat(plan.Links[0].Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the sweep recreated the link")
	}
}

// More than a quarter of a root's links missing at once (a share below the root not mounted) marks
// none of them missing: an alert is recorded instead, which the health check reports. A few missing go as before.
func TestReconcileVanishFailsafe(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	share := filepath.Join(e.movies, "Share")
	var paths []string
	for i := range 9 {
		p := filepath.Join(share, fmt.Sprintf("Movie %d", i), "m.mkv")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(plan.Links[0].Target, p); err != nil {
			t.Fatal(err)
		}
		e.exec(`INSERT INTO link (path, target, title_id, state, created_at, updated_at) SELECT ?, target, title_id,
			'active', 0, 0 FROM link ORDER BY id LIMIT 1`, p)
		e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (last_insert_rowid(), 10)`)
		paths = append(paths, p)
	}
	// The share is unmounted: its directory is there, empty.
	if err := os.RemoveAll(share); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(share, 0o755); err != nil {
		t.Fatal(err)
	}
	scans, err := e.lib.ReconcileLinks(e.ctx, "movie-remux", nil)
	if err != nil || len(scans) != 1 || len(scans[0].Vanished) != 0 || !strings.HasPrefix(scans[0].Skipped, "failsafe") {
		t.Fatalf("reconcile %+v %v", scans, err)
	}
	if n := e.one(`SELECT COUNT(*) FROM link WHERE state = 'active'`); n != "10" {
		t.Fatalf("%s links active, want 10", n)
	}
	if r := health.VanishFailsafe(e.st, func() time.Time { return e.now })(e.ctx); r.Type != health.Warning {
		t.Fatalf("health %+v", r)
	}
	// A few missing (no more than the floor) go, whatever their share.
	for _, p := range paths[:2] {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(plan.Links[0].Target, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range paths[2:] {
		e.exec(`UPDATE link SET state = 'unlinked' WHERE path = ?`, p)
	}
	for _, p := range paths[:2] {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	scans, err = e.lib.ReconcileLinks(e.ctx, "movie-remux", nil)
	if err != nil || len(scans[0].Vanished) != 2 || scans[0].Skipped != "" {
		t.Fatalf("reconcile of two %+v %v", scans, err)
	}
}

// A Rescan of one title is measured against the root's whole link set: a season deleted from one
// series (more than a quarter of that title's links, few of the root's) goes as a full sweep would take it.
func TestReconcileTitleScanFailsafeCountsTheRoot(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	var title int64
	if err := e.st.Reader().QueryRow(`SELECT title_id FROM link ORDER BY id LIMIT 1`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO title (id, kind, name, year, tmdb_id, created_at, updated_at) VALUES (501, 'movie', 'Other',
		2011, 99501, 0, 0)`)
	add := func(dir string, i int, tid int64) string {
		p := filepath.Join(e.movies, dir, fmt.Sprintf("m%d.mkv", i))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(plan.Links[0].Target, p); err != nil {
			t.Fatal(err)
		}
		e.exec(`INSERT INTO link (path, target, title_id, state, created_at, updated_at) SELECT ?, target, ?,
			'active', 0, 0 FROM link ORDER BY id LIMIT 1`, p, tid)
		e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (last_insert_rowid(), 10)`)
		return p
	}
	for i := range 24 {
		add("Other", i, 501)
	}
	var gone []string
	for i := range 7 {
		gone = append(gone, add("Mine", i, title))
	}
	for _, p := range gone {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	scans, err := e.lib.ReconcileLinks(e.ctx, "movie-remux", []int64{title})
	if err != nil || len(scans) != 1 || scans[0].Skipped != "" || len(scans[0].Vanished) != 7 {
		t.Fatalf("reconcile %+v %v", scans, err)
	}
	if n := e.one(`SELECT COUNT(*) FROM link WHERE state = 'active'`); n != "25" {
		t.Fatalf("%s links active, want 25", n)
	}
}
