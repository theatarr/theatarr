package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
)

// Dead links, link races, dead actions for untracked links, and the trash.

// deadEnv is an active movie link of version 10 into Name/episode.mkv, the version filled, and another release so
// the listing is plausible. The file itself is not on the mount.
func deadEnv(t *testing.T, enabled bool, mode policy.Mode) (*env, string, string) {
	t.Helper()
	e := newEnv(t, enabled, mode)
	e.release("Other", map[string]float64{"a.mkv": 7000})
	e.release("Name", map[string]float64{"x.mkv": 7000})
	e.exec(`INSERT INTO content (id, info_hash, name, mount_folder, first_seen_at, last_seen_at) VALUES (1, ?, 'Name', 'Name', 0, 0)`, hashA)
	e.exec(`INSERT INTO content_file (id, content_id, path, size_bytes) VALUES (1, 1, 'episode.mkv', 1)`)
	p := filepath.Join(e.movies, "Movie (2001)", "Movie.mkv")
	tgt := filepath.Join(e.mount, "__all__", "Name", "episode.mkv")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(tgt, p); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO link (id, path, target, content_file_id, title_id, release_title, quality, state, created_at, updated_at)
		VALUES (1, ?, ?, 1, 1, 'Name', 'WEBDL-1080p', 'active', 0, 0)`, p, tgt)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 10)`)
	e.exec(`UPDATE version SET state = 'filled' WHERE id = 10`)
	return e, p, tgt
}

func (e *env) sweep() []*LinkScan {
	e.t.Helper()
	scans, err := e.lib.Relink(e.ctx, "", false)
	if err != nil {
		e.t.Fatal(err)
	}
	return scans
}

// One sweep that misses a file only notes it; a later sweep (deadConfirm on) that still misses it marks the
// link dead. A sweep that finds it again in between starts over.
func TestRelinkDeadNeedsTwoSweeps(t *testing.T) {
	t.Parallel()
	e, _, _ := deadEnv(t, true, policy.ModeActive)
	e.sweep()
	if got := e.one(`SELECT state FROM link WHERE id = 1`) + " " + e.one(`SELECT state FROM version WHERE id = 10`); got != "active filled" {
		t.Fatalf("after one sweep: %s, want active filled", got)
	}
	e.now = e.now.Add(time.Minute)
	e.sweep()
	if got := e.one(`SELECT state FROM link WHERE id = 1`); got != "active" {
		t.Fatalf("a second sweep a minute later marked it %s", got)
	}
	// Back on the mount: the miss is forgotten.
	e.release("Name", map[string]float64{"episode.mkv": 7000})
	e.sweep()
	if got := e.one(`SELECT COALESCE(missing_since, 'null') FROM link WHERE id = 1`); got != "null" {
		t.Fatalf("missing_since %s after the file came back", got)
	}
	if err := os.Remove(filepath.Join(e.mount, "__all__", "Name", "episode.mkv")); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(2 * deadConfirm)
	e.sweep()
	if got := e.one(`SELECT state FROM link WHERE id = 1`); got != "active" {
		t.Fatalf("a first miss after a hit marked it %s", got)
	}
	e.now = e.now.Add(deadConfirm)
	e.sweep()
	if got := e.one(`SELECT state FROM link WHERE id = 1`) + " " + e.one(`SELECT state FROM version WHERE id = 10`); got != "dead wanted" {
		t.Fatalf("after two sweeps: %s, want dead wanted", got)
	}
}

// deadLinkJobs counts the dead-link searches queued (router.ReasonDeadLink).
func (e *env) deadLinkJobs() int {
	e.t.Helper()
	var n int
	if err := e.st.Reader().QueryRow(`SELECT COUNT(*) FROM job WHERE kind = ? AND state = 'queued'
		AND json_extract(payload, '$.reason') = ?`, router.JobResearch, router.ReasonDeadLink).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// The dead mark queues one search for the movie (router.ReasonDeadLink); a later sweep
// queues no other.
func TestRelinkDeadQueuesOneSearch(t *testing.T) {
	t.Parallel()
	e, _, _ := deadEnv(t, true, policy.ModeActive)
	e.sweep()
	if n := e.deadLinkJobs(); n != 0 {
		t.Fatalf("%d searches queued on the first miss", n)
	}
	e.now = e.now.Add(deadConfirm)
	e.sweep()
	var p router.ResearchRequest
	var payload string
	if err := e.st.Reader().QueryRow(`SELECT payload FROM job WHERE kind = ?`, router.JobResearch).Scan(&payload); err != nil {
		t.Fatalf("no search queued for the dead link: %v", err)
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatal(err)
	}
	if p.Reason != router.ReasonDeadLink || p.TitleID != 1 || p.VersionID != 10 || p.Version != "movie-remux" ||
		len(p.Episodes) != 0 {
		t.Errorf("search %+v", p)
	}
	e.now = e.now.Add(2 * deadConfirm)
	e.sweep()
	if n := e.deadLinkJobs(); n != 1 {
		t.Errorf("%d searches queued after another sweep, want 1", n)
	}
}

// A link shared with a version the library gate does not let write (shadow, unlisted) queues a search for the
// active version only.
func TestRelinkDeadSharedLinkQueuesActiveVersionOnly(t *testing.T) {
	t.Parallel()
	e, _, _ := deadEnv(t, true, policy.ModeActive)
	e.pol.Versions["movie-shadow"] = &policy.VersionPolicy{Mode: policy.ModeShadow, Kind: policy.KindMovie,
		Profile: "movie-state"}
	e.lib.gate = NewGate(e.pol)
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (11, 1, 'movie-shadow', 0, 0)`)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 11)`)
	e.sweep()
	e.now = e.now.Add(deadConfirm)
	e.sweep()
	if got := e.one(`SELECT state FROM link WHERE id = 1`); got != "dead" {
		t.Fatalf("link %s, want dead", got)
	}
	if got := e.one(`SELECT group_concat(json_extract(payload, '$.version'), ',') FROM job WHERE kind = ?`,
		router.JobResearch); got != "movie-remux" {
		t.Errorf("searches queued for %s, want movie-remux only", got)
	}
}

// A dead series link queues one search of its own episodes only.
func TestRelinkDeadSeriesQueuesItsEpisodes(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release("Other", map[string]float64{"a.mkv": 7000})
	e.release("Show.S01", map[string]float64{"x.mkv": 2700})
	tgt := filepath.Join(e.mount, "__all__", "Show.S01", "e1.mkv")
	e.exec(`INSERT INTO content (id, info_hash, name, mount_folder, first_seen_at, last_seen_at) VALUES (1, ?, 'Show.S01', 'Show.S01', 0, 0)`, hashA)
	e.exec(`INSERT INTO content_file (id, content_id, path, size_bytes) VALUES (1, 1, 'e1.mkv', 1)`)
	for i, ep := range []int64{201, 202} {
		p := filepath.Join(e.shows, "Show (2020)", "Season 01", fmt.Sprintf("e%d.mkv", i+1))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		target := tgt
		if ep == 202 { // the other episode's file is on the mount
			target = filepath.Join(e.mount, "__all__", "Show.S01", "x.mkv")
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		e.exec(`INSERT INTO link (id, path, target, content_file_id, title_id, quality, state, created_at, updated_at)
			VALUES (?, ?, ?, 1, 2, 'WEBDL-1080p', 'active', 0, 0)`, i+1, p, target)
		e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (?, 20)`, i+1)
		e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (?, ?)`, i+1, ep)
		e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, cutoff_met, updated_at)
			VALUES (20, ?, 1, 'filled', 0, 0)`, ep)
	}
	e.sweep()
	e.now = e.now.Add(deadConfirm)
	e.sweep()
	if got := e.one(`SELECT state FROM link WHERE id = 1`) + " " + e.one(`SELECT state FROM link WHERE id = 2`); got != "dead active" {
		t.Fatalf("links %s, want dead active", got)
	}
	var payload string
	if err := e.st.Reader().QueryRow(`SELECT payload FROM job WHERE kind = ?`, router.JobResearch).Scan(&payload); err != nil {
		t.Fatalf("no search queued for the dead link: %v", err)
	}
	var p router.ResearchRequest
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatal(err)
	}
	if p.Reason != router.ReasonDeadLink || p.TitleID != 2 || p.VersionID != 20 || p.Version != "tv" ||
		len(p.Episodes) != 1 || p.Episodes[0] != 201 {
		t.Errorf("search %+v, want episode 201 of version 20", p)
	}
	if n := e.deadLinkJobs(); n != 1 {
		t.Errorf("%d searches queued", n)
	}
}

// A dead link whose file is back turns active again, and the title is no longer wanted.
func TestRelinkRevivesDeadLink(t *testing.T) {
	t.Parallel()
	e, _, _ := deadEnv(t, true, policy.ModeActive)
	e.exec(`UPDATE link SET state = 'dead' WHERE id = 1`)
	e.exec(`UPDATE version SET state = 'wanted' WHERE id = 10`)
	e.release("Name", map[string]float64{"episode.mkv": 7000})
	e.sweep()
	if got := e.one(`SELECT state FROM link WHERE id = 1`) + " " + e.one(`SELECT state FROM version WHERE id = 10`); got != "active filled" {
		t.Fatalf("after the file came back: %s, want active filled", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM library_action WHERE kind = 'relink' AND mode = 'applied' AND detail LIKE 'revived%'`); got != "1" {
		t.Errorf("revive actions %s", got)
	}
	// Gated: reported, not revived.
	e.exec(`UPDATE link SET state = 'dead' WHERE id = 1`)
	e.exec(`UPDATE version SET state = 'wanted' WHERE id = 10`)
	e.pol.Versions["movie-remux"].Mode = policy.ModeShadow
	e.lib.gate = NewGate(e.pol)
	e.sweep()
	if got := e.one(`SELECT state FROM link WHERE id = 1`) + " " + e.one(`SELECT state FROM version WHERE id = 10`); got != "dead wanted" {
		t.Fatalf("gated revive wrote: %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM library_action WHERE kind = 'relink' AND mode = 'dry_run' AND detail LIKE 'revive%'`); got != "1" {
		t.Errorf("gated revive dry runs %s", got)
	}
}

// B1(a), series: the link's episodes are no longer wanted once it is revived.
func TestRelinkRevivesDeadSeriesLink(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release("Other", map[string]float64{"a.mkv": 7000})
	e.release("Show.S01", map[string]float64{"e1.mkv": 2700})
	p := filepath.Join(e.shows, "Show (2020)", "Season 01", "e1.mkv")
	tgt := filepath.Join(e.mount, "__all__", "Show.S01", "e1.mkv")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(tgt, p); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO content (id, info_hash, name, mount_folder, first_seen_at, last_seen_at) VALUES (1, ?, 'Show.S01', 'Show.S01', 0, 0)`, hashA)
	e.exec(`INSERT INTO content_file (id, content_id, path, size_bytes) VALUES (1, 1, 'e1.mkv', 1)`)
	e.exec(`INSERT INTO link (id, path, target, content_file_id, title_id, quality, state, created_at, updated_at)
		VALUES (1, ?, ?, 1, 2, 'WEBDL-1080p', 'dead', 0, 0)`, p, tgt)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 20)`)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (1, 201)`)
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, cutoff_met, updated_at)
		VALUES (20, 201, 1, 'wanted', 0, 0)`)
	e.exec(`UPDATE version SET state = 'wanted' WHERE id = 20`)
	e.sweep()
	if got := e.one(`SELECT state FROM link WHERE id = 1`) + " " +
		e.one(`SELECT state FROM episode_version_state WHERE version_id = 20 AND episode_id = 201`); got != "active filled" {
		t.Fatalf("after revive: %s, want active filled", got)
	}
}

// A gated version's sweep (disabled library or shadow) never marks a link dead or flips its title to
// wanted: it records the plan as a dry run.
func TestRelinkDeadWhileGated(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		enabled bool
		mode    policy.Mode
	}{{"disabled", false, policy.ModeActive}, {"shadow", true, policy.ModeShadow}} {
		t.Run(tt.name, func(t *testing.T) {
			e, _, _ := deadEnv(t, tt.enabled, tt.mode)
			e.sweep()
			e.now = e.now.Add(2 * deadConfirm)
			scans := e.sweep()
			if !scans[0].DryRun || len(scans[0].Dangling) != 1 {
				t.Fatalf("scan %+v", scans[0])
			}
			if got := e.one(`SELECT state FROM link WHERE id = 1`) + " " + e.one(`SELECT state FROM version WHERE id = 10`) +
				" " + e.one(`SELECT COUNT(*) FROM library_action WHERE mode = 'applied'`); got != "active filled 0" {
				t.Fatalf("gated sweep wrote: link, version, applied actions %s", got)
			}
			if got := e.one(`SELECT COUNT(*) FROM library_action WHERE kind = 'dead' AND mode = 'dry_run'`); got == "0" {
				t.Error("no dry-run plan recorded")
			}
			if n := e.deadLinkJobs(); n != 0 {
				t.Errorf("%d dead-link searches queued for a gated version", n)
			}
		})
	}
}

// An untracked dangling symlink (an arr's file in the root) has no row to mark: no "dead applied" action.
func TestRelinkUntrackedNoDeadAction(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release("Other", map[string]float64{"a.mkv": 7000})
	p := filepath.Join(e.movies, "Arr Movie (2001)", "Arr.mkv")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(e.mount, "__all__", "Gone", "x.mkv"), p); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if scans := e.sweep(); len(scans[0].Dangling) != 1 {
			t.Fatalf("sweep %d: %+v", i, scans[0])
		}
		e.now = e.now.Add(2 * deadConfirm)
	}
	if got := e.one(`SELECT COUNT(*) FROM library_action WHERE kind = 'dead' AND mode = 'applied'`); got != "0" {
		t.Fatalf("dead applied actions for an untracked link: %s", got)
	}
}

// Relink's repoint never revives an unlinked row.
func TestRelinkKeepsUnlinkedRow(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release("Other", map[string]float64{"a.mkv": 7000})
	e.release("Name {"+hashA[:6]+"}", map[string]float64{"episode.mkv": 7000})
	e.exec(`INSERT INTO content (id, info_hash, name, mount_folder, first_seen_at, last_seen_at) VALUES (1, ?, 'Name', 'Name', 0, 0)`, hashA)
	e.exec(`INSERT INTO content_file (id, content_id, path, size_bytes) VALUES (1, 1, 'episode.mkv', 1)`)
	p := filepath.Join(e.movies, "Movie (2001)", "Movie.mkv")
	old := filepath.Join(e.mount, "__all__", "Name", "episode.mkv")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(old, p); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO link (id, path, target, content_file_id, title_id, state, created_at, updated_at)
		VALUES (1, ?, ?, 1, 1, 'unlinked', 0, 0)`, p, old)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 10)`)
	e.sweep()
	if got := e.one(`SELECT state FROM link WHERE id = 1`); got != "unlinked" {
		t.Fatalf("relink revived an unlinked row: %s", got)
	}
}

// Unlink is a compare-and-swap on the row: a link a rename moved meanwhile is not unlinked by a stale path.
func TestUnlinkCompareAndSwap(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	oldPath := filepath.Join(e.movies, "Old", "old.mkv")
	newPath := filepath.Join(e.movies, "New", "new.mkv")
	target := filepath.Join(e.mount, "__all__", "R", "movie.mkv")
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, newPath); err != nil {
		t.Fatal(err)
	}
	// The rename already moved the row; the client still names the old path.
	e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at) VALUES (1, ?, ?, 1, 'active', 0, 0)`,
		newPath, target)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 10)`)
	e.exec(`UPDATE version SET state = 'filled' WHERE id = 10`)
	_, err := e.lib.Unlink(e.ctx, "movie-remux", oldPath, 1, "test")
	if !errors.Is(err, ErrLinkChanged) {
		t.Errorf("unlink of a moved row: %v, want ErrLinkChanged", err)
	}
	if got := e.one(`SELECT state FROM link WHERE id = 1`) + " " + e.one(`SELECT state FROM version WHERE id = 10`); got != "active filled" {
		t.Fatalf("stale unlink changed the row: %s", got)
	}
	if _, err := os.Lstat(newPath); err != nil {
		t.Fatalf("the moved link went: %v", err)
	}
}

// A rename that crashed after making the new symlink and before its commit is retried: the new path already
// points at the target.
func TestRenameRetryAfterCrash(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	old := filepath.Join(e.movies, "Old", "old.mkv")
	newPath := filepath.Join(e.movies, "New", "new.mkv")
	target := filepath.Join(e.mount, "__all__", "movie.mkv")
	for _, p := range []string{old, newPath} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at) VALUES (1, ?, ?, 1, 'active', 0, 0)`,
		old, target)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 10)`)
	if err := e.lib.RenameLink(e.ctx, 1, newPath); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := e.one(`SELECT path FROM link WHERE id = 1`); got != newPath {
		t.Errorf("row path %s", got)
	}
	if _, err := os.Lstat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old link remains: %v", err)
	}
	// A destination that points elsewhere still refuses.
	other := filepath.Join(e.movies, "Other", "other.mkv")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(e.mount, "__all__", "else.mkv"), other); err != nil {
		t.Fatal(err)
	}
	if err := e.lib.RenameLink(e.ctx, 1, other); err == nil {
		t.Error("rename onto a foreign link succeeded")
	}
}

// An upgrade racing a rename: the replaced link is not left active beside the new file. The
// import holds the replaced link's path under its link lease (linklock.go), so the rename is refused
// (ErrLinkChanged) and the old link is moved aside and its row unlinked by the import.
func TestUpgradeRacingRename(t *testing.T) {
	t.Parallel()
	e, old, _ := upgradeEnv(t)
	renamed := filepath.Join(e.movies, "Amelie The Movie (2001)", "renamed.mkv")
	done := false
	e.lib.w.after = func(op, path string) {
		if op == "symlink" && !done && path != renamed {
			done = true
			if err := e.lib.RenameLink(e.ctx, 7, renamed); !errors.Is(err, ErrLinkChanged) {
				t.Errorf("rename during the upgrade: %v, want ErrLinkChanged", err)
			}
		}
	}
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Fatal("the rename did not run")
	}
	if got := e.one(`SELECT state FROM link WHERE id = 7`); got != "unlinked" {
		t.Errorf("replaced link row %s, want unlinked", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM link WHERE state = 'active'`); got != "1" {
		t.Errorf("%s active links, want the new one", got)
	}
	for _, p := range []string{old, renamed} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still in the library: %v", p, err)
		}
	}
}

// Trash copies an interrupted run left without a link_trash row are purged after keep_days too (symlinks only).
func TestPurgeTrashOrphans(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.lib.set.TrashKeepDays = 7
	dir := filepath.Join(e.trash, "movie-remux", "2026-10-01", "Movie (2001)")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(dir, "orphan.mkv")
	if err := os.Symlink(filepath.Join(e.mount, "__all__", "x.mkv"), orphan); err != nil {
		t.Fatal(err)
	}
	old := e.now.Add(-30 * 24 * time.Hour)
	file := filepath.Join(dir, "regular.mkv")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Today's copy of an interrupted run is too young.
	freshDir := filepath.Join(e.trash, "movie-remux", e.now.UTC().Format("2006-01-02"))
	if err := os.MkdirAll(freshDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(freshDir, "fresh.mkv")
	if err := os.Symlink(filepath.Join(e.mount, "__all__", "y.mkv"), fresh); err != nil {
		t.Fatal(err)
	}
	// A recorded copy is purged by its row, as before.
	recorded := filepath.Join(dir, "recorded.mkv")
	if err := os.Symlink(filepath.Join(e.mount, "__all__", "z.mkv"), recorded); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO link_trash (version_key, path, trash_path, target, reason, moved_at) VALUES ('movie-remux', 'p', ?, 't', 'r', ?)`,
		recorded, old.UnixMilli())
	n, err := e.lib.PurgeTrash(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("purged %d, want 2", n)
	}
	for p, gone := range map[string]bool{orphan: true, recorded: true, file: false, fresh: false} {
		_, err := os.Lstat(p)
		if gone != errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: gone=%v (%v)", filepath.Base(p), !gone, err)
		}
	}
	if got := e.one(`SELECT COUNT(*) FROM library_action WHERE kind = 'purge' AND detail LIKE '%no trash row%'`); got != "1" {
		t.Errorf("orphan purge actions %s", got)
	}
	if !strings.HasPrefix(orphan, e.trash) {
		t.Fatal("test setup")
	}
}

// A sweep that saw a present link as active (with a noted miss) while another sweep marked it dead in
// the meantime revives it: the state is read again in the write transaction, not taken from the sweep's start.
func TestRelinkRevivesLinkMarkedDeadDuringSweep(t *testing.T) {
	e, _, _ := deadEnv(t, true, policy.ModeActive)
	e.sweep() // the miss is noted
	e.release("Name", map[string]float64{"episode.mkv": 7000})
	e.lib.beforeSweepWrite = func() {
		e.lib.beforeSweepWrite = nil
		// The concurrent sweep that still missed it marks it dead and the title wanted.
		e.exec(`UPDATE link SET state = 'dead', missing_since = NULL WHERE id = 1`)
		e.exec(`UPDATE version SET state = 'wanted' WHERE id = 10`)
	}
	e.sweep()
	if got := e.one(`SELECT state FROM link WHERE id = 1`) + " " + e.one(`SELECT state FROM version WHERE id = 10`); got != "active filled" {
		t.Fatalf("after the racing dead mark: %s, want active filled", got)
	}
}

// A rename that crashed after its commit left the old symlink; an upgrade that crashed after its commit
// left the stray it was to move aside. The sweep moves both aside (they are Theatarr's, retired by a row that changed after
// the symlink was made), and leaves a symlink newer than its row (a rename still in flight) and a gated version alone.
func TestRelinkMovesAsideCrashLeftovers(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
	e.release("Other", map[string]float64{"a.mkv": 7000})
	e.release("Name", map[string]float64{"movie.mkv": 7000, "old.mkv": 7000})
	target := filepath.Join(e.mount, "__all__", "Name", "movie.mkv")
	oldTarget := filepath.Join(e.mount, "__all__", "Name", "old.mkv")
	link := func(p, tgt string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(tgt, p); err != nil {
			t.Fatal(err)
		}
	}
	renamedFrom := filepath.Join(e.movies, "Old", "old.mkv")
	renamedTo := filepath.Join(e.movies, "New", "new.mkv")
	stray := filepath.Join(e.movies, "Stray", "stray.mkv")
	inFlight := filepath.Join(e.movies, "Flight", "flight.mkv")
	link(renamedFrom, target)
	link(renamedTo, target)
	link(stray, oldTarget)
	time.Sleep(10 * time.Millisecond)
	at := time.Now().UnixMilli() // the rows' last change: after those symlinks, before the in-flight one
	time.Sleep(10 * time.Millisecond)
	link(inFlight, target)
	e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at) VALUES (1, ?, ?, 1, 'active', 0, ?)`,
		renamedTo, target, at)
	e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at) VALUES (2, ?, ?, 1, 'unlinked', 0, ?)`,
		stray, oldTarget, at)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 10), (2, 10)`)

	// Gated: nothing moves.
	e.pol.Versions["movie-remux"].Mode = policy.ModeShadow
	e.lib.gate = NewGate(e.pol)
	e.sweep()
	for _, p := range []string{renamedFrom, stray, inFlight} {
		if _, err := os.Lstat(p); err != nil {
			t.Fatalf("gated sweep moved %s: %v", p, err)
		}
	}
	e.pol.Versions["movie-remux"].Mode = policy.ModeActive
	e.lib.gate = NewGate(e.pol)
	e.sweep()
	for _, p := range []string{renamedFrom, stray} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("leftover %s stayed: %v", p, err)
		}
	}
	for _, p := range []string{renamedTo, inFlight} {
		if !linkPointsAt(p, target) {
			t.Errorf("%s was touched", p)
		}
	}
	if got := e.one(`SELECT COUNT(*) FROM library_action WHERE kind = 'replace' AND mode = 'applied' AND detail LIKE 'leftover symlink moved aside%'`); got != "2" {
		t.Errorf("leftover actions %s, want 2", got)
	}
	if got := e.one(`SELECT state FROM link WHERE id = 1`); got != "active" {
		t.Errorf("renamed row %s", got)
	}
}
