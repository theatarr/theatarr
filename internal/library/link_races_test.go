package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/library/probe"
	"github.com/theatarr/theatarr/internal/policy"
)

// A dead link whose title was filled again by a new import at another path is revived when its old file
// comes back: two active links for one movie, and the dead symlink was never moved aside.
func TestDeadRevivedNextToReplacement(t *testing.T) {
	t.Parallel()
	e, deadPath, _ := deadEnv(t, true, policy.ModeActive)
	// The dead link's release is another torrent than the new download's.
	e.exec(`UPDATE content SET info_hash = ? WHERE id = 1`, hashB)
	e.exec(`UPDATE link SET state = 'dead' WHERE id = 1`)
	e.exec(`UPDATE version SET state = 'wanted' WHERE id = 10`)
	// The title is grabbed again and imported.
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 1 {
		t.Fatalf("plan %+v", plan)
	}
	t.Logf("replaces %v; dead symlink still on disk: %v", plan.Links[0].Replaces, exists(deadPath))
	// The old release's file comes back (zurg repair).
	e.release("Name", map[string]float64{"episode.mkv": 7000})
	e.sweep()
	if n := e.one(`SELECT COUNT(*) FROM link l JOIN link_version lv ON lv.link_id = l.id WHERE lv.version_id = 10 AND l.state = 'active'`); n != "1" {
		t.Errorf("active links of the movie version after the revive: %s, want 1", n)
	}
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

var _ = filepath.Join

// Two titles whose names collide under the version's naming (same clean title and year, no id in the
// template): importing the second replaces the first title's link in place and takes over its row.
func TestPathCollisionStealsOtherTitlesLink(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["movie-remux"].Naming.File = "{Movie CleanTitle} ({Release Year})"
	e.exec(`INSERT INTO title (id, kind, name, year, imdb_id, tmdb_id, runtime_minutes, created_at, updated_at)
		VALUES (3, 'movie', 'Amelie The Movie', 2001, 'tt9999999', 999, 120, 0, 0)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (30, 3, 'movie-remux', 'cutoff_met', 0, 0)`)
	e.release("Other.Movie.2001", map[string]float64{"other.mkv": 7000})
	other := filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie The Movie (2001).mkv")
	otherTgt := filepath.Join(e.mount, "__all__", "Other.Movie.2001", "other.mkv")
	os.MkdirAll(filepath.Dir(other), 0o755)
	if err := os.Symlink(otherTgt, other); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO link (id, path, target, title_id, release_title, quality, state, created_at, updated_at)
		VALUES (7, ?, ?, 3, 'Other.Movie.2001', 'Remux-2160p', 'active', 0, 0)`, other, otherTgt)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (7, 30)`)
	// Title 1 ("Amélie: The Movie", 2001) is imported.
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Logf("import err: %v", err)
	}
	if plan != nil && len(plan.Links) > 0 {
		t.Logf("planned %s (exists %v)", plan.Links[0].Path, plan.Links[0].Exists)
	}
	if tg, _ := os.Readlink(other); tg != otherTgt {
		t.Errorf("title 3's link now points at %s", tg)
	}
	if got := e.one(`SELECT title_id || ' ' || state FROM link WHERE id = 7`); got != "3 active" {
		t.Errorf("title 3's link row is now %q", got)
	}
	t.Logf("title 3 version state %s; link 7 versions %s", e.one(`SELECT state FROM version WHERE id = 30`),
		e.one(`SELECT group_concat(version_id) FROM link_version WHERE link_id = 7`))
}

// An in-place replacement (same path, new target) keeps the old target's missing_since: one later miss of the
// new file marks the link dead at once, without the second sweep B1's fix requires.
func TestInPlaceReplaceKeepsMissingSince(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release("Other", map[string]float64{"a.mkv": 7000})
	e.release("Other2", map[string]float64{"a.mkv": 7000})
	p := filepath.Join(e.movies, "Amelie The Movie (2001)",
		"Amelie The Movie (2001) {imdb-tt0211915} - [Remux-2160p][TrueHD Atmos 7.1][HDR10][HEVC]-GRP.mkv")
	oldTgt := filepath.Join(e.mount, "__all__", "Gone", "old.mkv")
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.Symlink(oldTgt, p); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO link (id, path, target, title_id, release_title, quality, state, missing_since, created_at, updated_at)
		VALUES (1, ?, ?, 1, 'Gone', 'WEBDL-1080p', 'active', ?, 0, 0)`, p, oldTgt, e.now.Add(-time.Hour).UnixMilli())
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 10)`)
	e.exec(`UPDATE version SET state = 'filled' WHERE id = 10`)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 1 || plan.Links[0].Path != p {
		t.Fatalf("plan %+v rejected %+v", plan.Links, plan.Rejected)
	}
	// History's facts (00129): the replace names the file it moved aside, though its row now holds the new one.
	if got := e.one(`SELECT release_title || '/' || quality FROM library_action WHERE kind = 'replace'`); got != "Gone/WEBDL-1080p" {
		t.Errorf("replace action facts %q", got)
	}
	if got := e.one(`SELECT release_title FROM library_action WHERE kind = 'link'`); got != movieRelease {
		t.Errorf("link action release %q", got)
	}
	t.Logf("after the import: target %s missing_since %s", e.one(`SELECT target FROM link WHERE id = 1`),
		e.one(`SELECT COALESCE(missing_since, 'null') FROM link WHERE id = 1`))
	// The new file misses one sweep (a zurg hiccup on that folder).
	os.Remove(filepath.Join(e.mount, "__all__", movieRelease, "movie.mkv"))
	e.now = e.now.Add(time.Minute)
	e.sweep()
	if got := e.one(`SELECT state FROM link WHERE id = 1`); got != "active" {
		t.Errorf("one miss of the new file marked the link %s", got)
	}
}

// The import claim (30 min) is never renewed; planning a large pack (a probe per file, up to 2 min each) that
// takes longer than the claim fails its first fence with ErrClaimed every time: the download never imports.
func TestSlowPlanNeverImports(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	rel := "Show.S01.2160p.WEB-DL.DDP5.1.H.265-GRP"
	e.release(rel, map[string]float64{"Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2700,
		"Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2650})
	inner := e.lib.probe
	e.lib.probe = func(p string, n int64) (*probe.Result, error) {
		e.now = e.now.Add(16 * time.Minute) // a slow mount: two files take 32 minutes
		return inner(p, n)
	}
	e.download(1, "tv", 2, 20, rel, rel, "[201, 202]")
	for i := 0; i < 3; i++ {
		_, err := e.lib.Import(e.ctx, 1, false)
		t.Logf("attempt %d: %v; download %s, links %s", i+1, err, e.one(`SELECT state FROM download WHERE id = 1`),
			e.one(`SELECT COUNT(*) FROM link`))
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "imported" {
		t.Errorf("after 3 attempts the download is %s", got)
	}
}

// A link an operator restored from the trash (mv back, which keeps the copy's mtime) after an unlink is
// moved aside again by the next sweep as a "leftover": its row is unlinked with the same target and was updated after
// the symlink's mtime.
func TestRestoredFromTrashMovedAsideAgain(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
	e.release("Other", map[string]float64{"a.mkv": 7000})
	e.release("Name", map[string]float64{"episode.mkv": 7000})
	p := filepath.Join(e.movies, "Movie (2001)", "Movie.mkv")
	tgt := filepath.Join(e.mount, "__all__", "Name", "episode.mkv")
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.Symlink(tgt, p); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO link (id, path, target, title_id, release_title, quality, state, created_at, updated_at)
		VALUES (1, ?, ?, 1, 'Name', 'WEBDL-1080p', 'active', 0, 0)`, p, tgt)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 10)`)
	e.now = time.Now().Add(50 * time.Millisecond) // the row's commit comes after the trash copy is made
	res, err := e.lib.Unlink(e.ctx, "movie-remux", p, 1, "deleted by mistake")
	if err != nil {
		t.Fatal(err)
	}
	e.now = time.Now().Add(time.Hour)
	// The operator puts it back.
	if err := os.Rename(res.TrashPath, p); err != nil {
		t.Fatal(err)
	}
	e.sweep()
	if !linkPointsAt(p, tgt) {
		t.Errorf("the restored link was moved aside again (row %s)", e.one(`SELECT state FROM link WHERE id = 1`))
	}
}

// An unlink that pauses after its trash copy while a relink repoints the same path must not remove the
// replacement link and mark its row unlinked with the old target in the trash. Either the unlink wins (the relink
// is left for a later sweep) or the replacement survives; the row always matches the disk.
func TestUnlinkKeepsConcurrentReplacement(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release("Other", map[string]float64{"a.mkv": 7000})
	e.release("Name {abcdef}", map[string]float64{"episode.mkv": 7000})
	p := filepath.Join(e.movies, "Movie (2001)", "Movie.mkv")
	old := filepath.Join(e.mount, "__all__", "Name", "episode.mkv")
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.Symlink(old, p); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO link (id, path, target, title_id, release_title, quality, state, created_at, updated_at)
		VALUES (1, ?, ?, 1, 'Name', 'WEBDL-1080p', 'active', 0, 0)`, p, old)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 10)`)
	e.lib.w.after = func(op, path string) {
		if op == "trash" && path == p {
			e.lib.w.after = nil
			if _, err := e.lib.RelinkRenamed(e.ctx, map[string]string{"Name": "Name {abcdef}"}); err != nil {
				t.Log(err)
			}
		}
	}
	_, err := e.lib.Unlink(e.ctx, "movie-remux", p, 1, "deleted")
	t.Logf("unlink: %v", err)
	state, target := e.one(`SELECT state FROM link WHERE id = 1`), e.one(`SELECT target FROM link WHERE id = 1`)
	switch state {
	case "unlinked":
		if exists(p) {
			t.Errorf("row unlinked but %s is still there", p)
		}
		if got := e.one(`SELECT target FROM link_trash WHERE link_id = 1 ORDER BY id DESC LIMIT 1`); got != target {
			t.Errorf("row unlinked with target %s, but the trash holds %s: the replacement was removed", target, got)
		}
	case "active":
		if tg, _ := os.Readlink(p); tg != target {
			t.Errorf("active row -> %s, symlink -> %s", target, tg)
		}
	default:
		t.Errorf("link state %s", state)
	}
}

// Two imports that plan the same path run at once: the second must not replace the first's link while the
// first records it, leaving the database with one target and the disk with another.
func TestConcurrentImportsSamePath(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["movie-remux"].Naming.File = "{Movie CleanTitle} ({Release Year})"
	other := "Amelie.2001.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-OTHER"
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.release(other, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality, score,
		episodes, provider, name, state, created_at, updated_at, ready_at)
		VALUES (2, 1, 10, 'movie-remux', ?, ?, 'idx', 'Remux-2160p', 100, '[]', 'rd', ?, 'ready', 0, 0, 0)`,
		strings.Repeat("b", 40), other, other)
	var path string
	e.lib.w.after = func(op, p string) {
		if op == "symlink" && strings.HasPrefix(p, e.movies) {
			e.lib.w.after = nil
			path = p
			_, err := e.lib.Import(e.ctx, 2, false)
			t.Logf("second import: %v", err)
		}
	}
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Logf("first import: %v", err)
	}
	if path == "" {
		t.Fatal("no link made")
	}
	tg, _ := os.Readlink(path)
	if got := e.one(`SELECT target FROM link WHERE path = ?`, path); got != tg {
		t.Errorf("row -> %s, symlink -> %s", got, tg)
	}
	if n := e.one(`SELECT COUNT(*) FROM download WHERE state = 'imported'`); n != "1" {
		t.Errorf("%s downloads imported for one link", n)
	}
}

// A dead link revived by a sweep (its file is back) gets a Plex scan of its folder, as a repoint does.
func TestRevivedLinkScansPlex(t *testing.T) {
	t.Parallel()
	e, deadPath, _ := deadEnv(t, true, policy.ModeActive)
	e.exec(`UPDATE link SET state = 'dead' WHERE id = 1`)
	e.release("Name", map[string]float64{"episode.mkv": 7000})
	e.sweep()
	if got := e.one(`SELECT state FROM link WHERE id = 1`); got != "active" {
		t.Fatalf("not revived: %s", got)
	}
	if n := e.one(`SELECT COUNT(*) FROM job WHERE kind = ? AND payload LIKE ?`, ScanJobKind,
		"%"+filepath.Base(filepath.Dir(deadPath))+"%"); n != "1" {
		t.Errorf("plex scans of the revived link's folder: %s", n)
	}
}
