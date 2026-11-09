package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// reassignEnv imports the two-episode pack (links 1: E01, 2: E02 in version tv) and adds E03 without a file.
func reassignEnv(t *testing.T) (*env, map[int64]string) {
	t.Helper()
	e := newEnv(t, true, policy.ModeActive)
	e.lib.set.Versions["tv"].PlexSection = 7
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES (203, 2, 1, 3, 'Third', 0, 0)`)
	e.importSeries()
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at) VALUES (20, 203, 1, 'wanted', 0)`)
	byEp := map[int64]string{}
	for _, ep := range []int64{201, 202} {
		byEp[ep] = e.one(`SELECT l.path FROM link l JOIN link_episode le ON le.link_id = l.id WHERE le.episode_id = ?`, ep)
	}
	return e, byEp
}

func (e *env) linkOf(path string) int64 {
	e.t.Helper()
	var id int64
	if err := e.st.Reader().QueryRowContext(e.ctx, `SELECT id FROM link WHERE path = ?`, path).Scan(&id); err != nil {
		e.t.Fatalf("link at %s: %v", path, err)
	}
	return id
}

func (e *env) epStates() string {
	return e.one(`SELECT group_concat(episode_id || ':' || state, ' ') FROM
		(SELECT * FROM episode_version_state WHERE version_id = 20 ORDER BY episode_id)`)
}

func (e *env) linkEps(id int64) string {
	return e.one(`SELECT COALESCE(group_concat(episode_id, ','), '') FROM (SELECT episode_id FROM link_episode
		WHERE link_id = ? ORDER BY episode_id)`, id)
}

// TestReassignOneToOne: the file linked as E01 is really E03. It is renamed to E03's name, holds E03, E01 is wanted
// again, E03 takes the file's state, the choice is recorded for the download, and Plex scans both folders.
func TestReassignOneToOne(t *testing.T) {
	t.Parallel()
	e, byEp := reassignEnv(t)
	id := e.linkOf(byEp[201])
	target, _ := os.Readlink(byEp[201])
	before := e.epStates()
	r, err := e.lib.ReassignEpisodes(e.ctx, id, []int64{203}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(r.Path, "Season 01/Show (2020) - S01E03 - Third [Bluray-2160p Remux]-GRP.mkv") ||
		strings.Join(r.Previous, ",") != "S01E01" || strings.Join(r.Episodes, ",") != "S01E03" {
		t.Fatalf("result %+v", r)
	}
	if got, err := os.Readlink(r.Path); err != nil || got != target {
		t.Fatalf("new link %q %v, want %q", got, err, target)
	}
	if _, err := os.Lstat(byEp[201]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old link remains: %v", err)
	}
	if got := e.one(`SELECT path FROM link WHERE id = ?`, id); got != r.Path {
		t.Fatalf("row path %s", got)
	}
	if got := e.linkEps(id); got != "203" {
		t.Fatalf("link episodes %s", got)
	}
	if before != "201:cutoff_met 202:cutoff_met 203:wanted" {
		t.Fatalf("states before %s", before)
	}
	if got := e.epStates(); got != "201:wanted 202:cutoff_met 203:cutoff_met" {
		t.Fatalf("states %s", got)
	}
	if got := e.one(`SELECT episodes || ' ' || source || ' ' || how FROM download_file WHERE download_id = 2
		AND rel = 'Show.S01E01.2160p.BluRay.REMUX.HEVC-GRP.mkv'`); got != "[203] import manual" {
		t.Fatalf("recorded choice %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM library_action WHERE kind = 'link' AND link_id = ? AND detail =
		'episodes changed by hand: S01E01 -> S01E03'`, id); got != "1" {
		t.Fatalf("action not recorded")
	}
	if got := e.one(`SELECT COUNT(*) FROM job WHERE kind = 'plex.scan' AND json_extract(payload, '$.section') = 7`); got == "0" {
		t.Fatalf("no plex scan queued")
	}
	// The same episodes again is refused, and nothing moves.
	if _, err := e.lib.ReassignEpisodes(e.ctx, id, []int64{203}, false); !errors.Is(err, ErrRefused) {
		t.Fatalf("same episodes: %v", err)
	}
}

// TestReassignOneToMany: the file linked as E02 holds E02 and E03 (a double episode).
func TestReassignOneToMany(t *testing.T) {
	t.Parallel()
	e, byEp := reassignEnv(t)
	id := e.linkOf(byEp[202])
	r, err := e.lib.ReassignEpisodes(e.ctx, id, []int64{203, 202, 203}, false)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(r.Path) != "Show (2020) - S01E02-E03 - Second Part and Third [Bluray-2160p Remux]-GRP.mkv" {
		t.Fatalf("path %s", r.Path)
	}
	if got := e.linkEps(id); got != "202,203" {
		t.Fatalf("link episodes %s", got)
	}
	if got := e.epStates(); got != "201:cutoff_met 202:cutoff_met 203:cutoff_met" {
		t.Fatalf("states %s", got)
	}
}

// TestReassignConflict: E01's file chosen for E02, which has its own file. Refused without replace (nothing
// changes); with replace E02's link goes to the trash the normal way and E01 is wanted again.
func TestReassignConflict(t *testing.T) {
	t.Parallel()
	e, byEp := reassignEnv(t)
	id, other := e.linkOf(byEp[201]), e.linkOf(byEp[202])
	snap := tree(t, e.dir)
	states := e.epStates()
	_, err := e.lib.ReassignEpisodes(e.ctx, id, []int64{202}, false)
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "S01E02 already has a file in this library") {
		t.Fatalf("conflict: %v", err)
	}
	if got := tree(t, e.dir); strings.Join(got, "\n") != strings.Join(snap, "\n") {
		t.Fatalf("refused reassign changed the tree")
	}
	if e.epStates() != states || e.linkEps(id) != "201" {
		t.Fatalf("refused reassign changed state")
	}

	r, err := e.lib.ReassignEpisodes(e.ctx, id, []int64{202}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Replaced) != 1 || r.Replaced[0] != byEp[202] {
		t.Fatalf("replaced %v", r.Replaced)
	}
	// Both files of the pack are named alike: the replaced link's path is the new one.
	if r.Path != byEp[202] {
		t.Fatalf("path %s, want %s", r.Path, byEp[202])
	}
	// Its unlinked row made way for the moved link's at the path; its trash copy is recorded.
	if got := e.one(`SELECT COUNT(*) FROM link WHERE id = ?`, other); got != "0" {
		t.Fatalf("replaced link row kept: %s", got)
	}
	trash := e.one(`SELECT trash_path FROM link_trash WHERE path = ?`, byEp[202])
	if got, err := os.Readlink(trash); err != nil || got != e.one(`SELECT target FROM link_trash WHERE path = ?`, byEp[202]) {
		t.Fatalf("trash copy %q %v", got, err)
	}
	target, _ := os.Readlink(r.Path)
	if got := e.one(`SELECT target FROM link WHERE id = ?`, id); got != target {
		t.Fatalf("link at %s points at %s, row %s", r.Path, target, got)
	}
	if got := e.epStates(); got != "201:wanted 202:cutoff_met 203:wanted" {
		t.Fatalf("states %s", got)
	}
	if _, err := os.Stat(filepath.Join(e.mount, "__all__", "Show.S01.2160p.BluRay.REMUX.HEVC-GRP",
		"Show.S01E02.2160p.BluRay.REMUX.HEVC-GRP.mkv")); err != nil {
		t.Fatalf("the replaced file's source on the mount: %v", err)
	}
}

// TestReassignRenameFailureRollsBack: the move's transaction fails after the new symlink was made. The new link
// goes, the old one stays, and the episodes, states and recorded choice are as they were. A destination held by a
// file that is not a link being replaced refuses before anything changes.
func TestReassignRenameFailureRollsBack(t *testing.T) {
	t.Parallel()
	e, byEp := reassignEnv(t)
	id := e.linkOf(byEp[201])
	snap := tree(t, e.dir)
	states, files := e.epStates(), e.one(`SELECT group_concat(rel || episodes, ' ') FROM download_file`)
	e.exec(`CREATE TRIGGER fail_reassign BEFORE INSERT ON link_episode BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	if _, err := e.lib.ReassignEpisodes(e.ctx, id, []int64{203}, false); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err %v", err)
	}
	check := func(what string) {
		t.Helper()
		if got := tree(t, e.dir); strings.Join(got, "\n") != strings.Join(snap, "\n") {
			t.Fatalf("%s: tree changed:\n%s", what, strings.Join(got, "\n"))
		}
		if e.linkEps(id) != "201" || e.epStates() != states || e.one(`SELECT path FROM link WHERE id = ?`, id) != byEp[201] ||
			e.one(`SELECT group_concat(rel || episodes, ' ') FROM download_file`) != files {
			t.Fatalf("%s: state changed", what)
		}
	}
	check("failed commit")
	e.exec(`DROP TRIGGER fail_reassign`)

	// A regular file where the new name goes.
	dest := filepath.Join(filepath.Dir(byEp[201]), "Show (2020) - S01E03 - Third [Bluray-2160p Remux]-GRP.mkv")
	if err := os.WriteFile(dest, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	snap = tree(t, e.dir)
	if _, err := e.lib.ReassignEpisodes(e.ctx, id, []int64{203}, true); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("blocked destination: %v", err)
	}
	check("blocked destination")
}

// TestReassignOtherVersionUntouched: another version's own link to the same file on the mount keeps its path and
// episodes, and a link shared by two versions is refused.
func TestReassignOtherVersionUntouched(t *testing.T) {
	t.Parallel()
	e, byEp := reassignEnv(t)
	id := e.linkOf(byEp[201])
	target, _ := os.Readlink(byEp[201])
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (21, 2, 'tv-alt', 0, 0)`)
	alt := filepath.Join(e.dir, "media", "shows-alt", "Show", "S01E01.mkv")
	if err := os.MkdirAll(filepath.Dir(alt), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, alt); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO link (id, path, target, title_id, download_id, state, file_metadata, created_at, updated_at)
		VALUES (90, ?, ?, 2, 2, 'active', '{}', 0, 0)`, alt, target)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (90, 21)`)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (90, 201)`)
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at) VALUES (21, 201, 1, 'filled', 0)`)
	if _, err := e.lib.ReassignEpisodes(e.ctx, id, []int64{203}, false); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(alt); err != nil || got != target {
		t.Fatalf("other version's link %q %v", got, err)
	}
	if e.linkEps(90) != "201" || e.one(`SELECT path || state FROM link WHERE id = 90`) != alt+"active" ||
		e.one(`SELECT state FROM episode_version_state WHERE version_id = 21 AND episode_id = 201`) != "filled" {
		t.Fatalf("other version changed")
	}

	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (?, 21)`, id)
	if _, err := e.lib.ReassignEpisodes(e.ctx, id, []int64{201}, false); !errors.Is(err, ErrRefused) ||
		!strings.Contains(err.Error(), "shared by 2 libraries") {
		t.Fatalf("shared link: %v", err)
	}
}

// TestReassignGate: a version the gate refuses (shadow) changes nothing.
func TestReassignGate(t *testing.T) {
	t.Parallel()
	e, byEp := reassignEnv(t)
	id := e.linkOf(byEp[201])
	e.pol.Versions["tv"].Mode = policy.ModeShadow
	e.lib = e.lib.WithPolicy(e.pol, e.lib.engine, nil, nil)
	snap := tree(t, e.dir)
	if _, err := e.lib.ReassignEpisodes(e.ctx, id, []int64{203}, false); !errors.Is(err, ErrRefused) {
		t.Fatalf("shadow: %v", err)
	}
	if got := tree(t, e.dir); strings.Join(got, "\n") != strings.Join(snap, "\n") || e.linkEps(id) != "201" {
		t.Fatalf("refused reassign changed something")
	}
}

// TestReassignKeptByLaterScans: after the change, the relink sweep, the vanished-link check, the state recompute and
// a re-import of the download keep the corrected mapping; the re-import still maps the pack's other file.
func TestReassignKeptByLaterScans(t *testing.T) {
	t.Parallel()
	e, byEp := reassignEnv(t)
	id := e.linkOf(byEp[201])
	r, err := e.lib.ReassignEpisodes(e.ctx, id, []int64{203}, false)
	if err != nil {
		t.Fatal(err)
	}
	e.sweep()
	if _, err := e.lib.RecomputeLinkStates(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE download SET state = 'ready' WHERE id = 2`)
	plan, err := e.lib.Import(e.ctx, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, lk := range plan.Links {
		got[lk.File] = filepath.Base(lk.Path)
	}
	if got["Show.S01E01.2160p.BluRay.REMUX.HEVC-GRP.mkv"] != filepath.Base(r.Path) ||
		got["Show.S01E02.2160p.BluRay.REMUX.HEVC-GRP.mkv"] != filepath.Base(byEp[202]) {
		t.Fatalf("re-import planned %v", got)
	}
	if e.linkEps(id) != "203" || e.one(`SELECT path || state FROM link WHERE id = ?`, id) != r.Path+"active" {
		t.Fatalf("mapping not kept: %s", e.linkEps(id))
	}
	if n := e.one(`SELECT COUNT(*) FROM link WHERE state = 'active'`); n != "2" {
		t.Fatalf("active links %s", n)
	}
	if got := e.epStates(); got != "201:wanted 202:cutoff_met 203:cutoff_met" {
		t.Fatalf("states %s", got)
	}
}

// replaceSnapshot is what a failed replace must leave as it was: the link rows, their episodes, the episode states,
// the recorded choices and the symlinks outside the trash, with no trash copy left.
func (e *env) replaceSnapshot() string {
	e.t.Helper()
	var links []string
	for _, s := range tree(e.t, e.dir) {
		if !strings.HasPrefix(s, e.trash) || strings.Contains(s, " -> ") {
			links = append(links, s)
		}
	}
	return strings.Join(links, "\n") + "\n" + e.one(`SELECT group_concat(x, ' ') FROM (SELECT id || ':' || path || ':' ||
		state AS x FROM link ORDER BY id)`) + "\n" + e.one(`SELECT group_concat(link_id || ':' || episode_id, ' ') FROM
		(SELECT * FROM link_episode ORDER BY link_id, episode_id)`) + "\n" + e.epStates() + " " +
		e.one(`SELECT state FROM version WHERE id = 20`) + "\n" + e.one(`SELECT group_concat(rel || episodes || source || how, ' ')
		FROM (SELECT * FROM download_file ORDER BY rel)`) + "\n" + e.one(`SELECT COUNT(*) FROM link_trash`)
}

// TestReassignReplaceFailureRestores: a replace whose move fails after the other link was unlinked
// puts that link back (its symlink, row, episodes and states, the trash copy gone), and the change is not made.
func TestReassignReplaceFailureRestores(t *testing.T) {
	t.Parallel()
	e, byEp := reassignEnv(t)
	id := e.linkOf(byEp[201])
	before := e.replaceSnapshot()
	e.exec(`CREATE TRIGGER fail_reassign BEFORE INSERT ON link_episode BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	if _, err := e.lib.ReassignEpisodes(e.ctx, id, []int64{202}, true); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err %v", err)
	}
	if got := e.replaceSnapshot(); got != before {
		t.Fatalf("after the failed replace:\n%s\nwant:\n%s", got, before)
	}
	if got := e.one(`SELECT COUNT(*) FROM library_action WHERE kind = 'replace' AND detail LIKE '%put back%'`); got != "1" {
		t.Fatalf("restore not recorded: %s", got)
	}
	// With the cause gone, the same replace works.
	e.exec(`DROP TRIGGER fail_reassign`)
	if _, err := e.lib.ReassignEpisodes(e.ctx, id, []int64{202}, true); err != nil {
		t.Fatal(err)
	}
}

// TestReassignReplaceStaleRowAtDestination: an unlinked row from an earlier unlink already holds
// the new name. It makes way for the moved link, as for an import, and the replace goes through.
func TestReassignReplaceStaleRowAtDestination(t *testing.T) {
	t.Parallel()
	e, byEp := reassignEnv(t)
	id, other := e.linkOf(byEp[201]), e.linkOf(byEp[202])
	dest := filepath.Join(filepath.Dir(byEp[201]),
		"Show (2020) - S01E02-E03 - Second Part and Third [Bluray-2160p Remux]-GRP.mkv")
	e.exec(`INSERT INTO link (id, path, target, title_id, download_id, state, file_metadata, created_at, updated_at)
		VALUES (91, ?, '/gone', 2, 2, 'unlinked', '{}', 0, 0)`, dest)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (91, 20)`)
	r, err := e.lib.ReassignEpisodes(e.ctx, id, []int64{202, 203}, true)
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != dest || e.one(`SELECT path || state FROM link WHERE id = ?`, id) != dest+"active" ||
		e.one(`SELECT COUNT(*) FROM link WHERE id = 91`) != "0" {
		t.Fatalf("path %s, row %s", r.Path, e.one(`SELECT path || state FROM link WHERE id = ?`, id))
	}
	if e.one(`SELECT state FROM link WHERE id = ?`, other) != "unlinked" || e.linkEps(id) != "202,203" {
		t.Fatalf("replace not made")
	}
}

// TestReassignReplaceKeptByReimport: the file linked as E02 is really E01, replacing E01's file.
// A re-import of the pack keeps the correction (the displaced file no longer claims E01), and the pack's other
// files still import.
func TestReassignReplaceKeptByReimport(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES (203, 2, 1, 3, 'Third', 0, 0)`)
	rel := "Show.S01.2160p.BluRay.REMUX.HEVC-GRP"
	e.release(rel, map[string]float64{"Show.S01E01.2160p.BluRay.REMUX.HEVC-GRP.mkv": 2700,
		"Show.S01E02.2160p.BluRay.REMUX.HEVC-GRP.mkv": 2700, "Show.S01E03.2160p.BluRay.REMUX.HEVC-GRP.mkv": 2700})
	e.seriesDownload(2, 2, 20, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", rel, "[201, 202, 203]", nil)
	e.exec(`UPDATE download SET quality = 'Bluray-2160p Remux' WHERE id = 2`)
	if _, err := e.lib.Import(e.ctx, 2, false); err != nil {
		t.Fatal(err)
	}
	byEp := map[int64]string{}
	for _, ep := range []int64{201, 202, 203} {
		byEp[ep] = e.one(`SELECT l.path FROM link l JOIN link_episode le ON le.link_id = l.id WHERE le.episode_id = ?`, ep)
	}
	id := e.linkOf(byEp[202])
	r, err := e.lib.ReassignEpisodes(e.ctx, id, []int64{201}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT COUNT(*) FROM download_file WHERE rel = 'Show.S01E01.2160p.BluRay.REMUX.HEVC-GRP.mkv'`); got != "0" {
		t.Fatalf("the displaced file keeps its recorded choice")
	}
	e.exec(`UPDATE download SET state = 'ready' WHERE id = 2`)
	plan, err := e.lib.Import(e.ctx, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, lk := range plan.Links {
		got[lk.File] = filepath.Base(lk.Path)
	}
	if len(got) != 2 || got["Show.S01E02.2160p.BluRay.REMUX.HEVC-GRP.mkv"] != filepath.Base(r.Path) ||
		got["Show.S01E03.2160p.BluRay.REMUX.HEVC-GRP.mkv"] != filepath.Base(byEp[203]) {
		t.Fatalf("re-import planned %v (unmatched %v)", got, plan.Unmatched)
	}
	if e.linkEps(id) != "201" || e.one(`SELECT path || state FROM link WHERE id = ?`, id) != r.Path+"active" {
		t.Fatalf("correction not kept: %s", e.linkEps(id))
	}
	if n := e.one(`SELECT COUNT(*) FROM link WHERE state = 'active'`); n != "2" {
		t.Fatalf("active links %s", n)
	}
	if got := e.epStates(); got != "201:cutoff_met 202:wanted 203:cutoff_met" {
		t.Fatalf("states %s", got)
	}
}

// TestReassignReplaceKeptAfterCommit: a move that committed and then left the old symlink (it changed meanwhile)
// keeps the replace; the replaced link is not put back over the moved one.
func TestReassignReplaceKeptAfterCommit(t *testing.T) {
	t.Parallel()
	e, byEp := reassignEnv(t)
	id, other := e.linkOf(byEp[201]), e.linkOf(byEp[202])
	target, _ := os.Readlink(byEp[201])
	now, done := e.lib.now, false
	// Inside the move's transaction (the new link made, the old one not yet removed), the old symlink changes.
	e.lib.now = func() time.Time {
		if !done && linkPointsAt(byEp[202], target) && linkPointsAt(byEp[201], target) {
			done = true
			_ = os.Remove(byEp[201])
			_ = os.Symlink("/elsewhere", byEp[201])
		}
		return now()
	}
	if _, err := e.lib.ReassignEpisodes(e.ctx, id, []int64{202}, true); !errors.Is(err, ErrLinkChanged) || !done {
		t.Fatalf("err %v", err)
	}
	if got, _ := os.Readlink(byEp[202]); got != target {
		t.Fatalf("%s points at %s, want the moved link's %s", byEp[202], got, target)
	}
	if e.one(`SELECT path || state FROM link WHERE id = ?`, id) != byEp[202]+"active" || e.linkEps(id) != "202" ||
		e.one(`SELECT COUNT(*) FROM link WHERE id = ?`, other) != "0" {
		t.Fatalf("replace not kept")
	}
}

// TestReassignReplaceCancelledRestores: the request is cancelled while the move runs, after the replaced
// link went to the trash. The move does not commit, and the replaced link is still put back: the commit check and
// the put-back do not follow the request's cancel.
func TestReassignReplaceCancelledRestores(t *testing.T) {
	t.Parallel()
	e, byEp := reassignEnv(t)
	id := e.linkOf(byEp[201])
	before := e.replaceSnapshot()
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	target, _ := os.Readlink(byEp[201])
	now := e.lib.now
	// Inside the move's transaction (the replaced link in the trash, the new link made), the request goes.
	e.lib.now = func() time.Time {
		if linkPointsAt(byEp[202], target) {
			cancel()
		}
		return now()
	}
	if _, err := e.lib.ReassignEpisodes(ctx, id, []int64{202}, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	e.lib.now = now
	if got := e.replaceSnapshot(); got != before {
		t.Fatalf("after the cancelled replace:\n%s\nwant:\n%s", got, before)
	}
	if got := e.one(`SELECT COUNT(*) FROM library_action WHERE kind = 'replace' AND detail LIKE '%put back from%'`); got != "1" {
		t.Fatalf("restore not recorded: %s", got)
	}
}

// TestReassignReplaceManualKeptByReimport: as TestReassignReplaceKeptByReimport, for a pack imported
// by hand, the displaced E01 file larger than the corrected E02 one. The displaced file's manual row is left with no
// episodes, marked replaced: a re-import does not read its name (E01) and take E01 back first, while the pack's
// other files still import.
func TestReassignReplaceManualKeptByReimport(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES (203, 2, 1, 3, 'Third', 0, 0)`)
	rel := "Show.S01.2160p.BluRay.REMUX.HEVC-GRP"
	f1, f2, f3 := "Show.S01E01.2160p.BluRay.REMUX.HEVC-GRP.mkv", "Show.S01E02.2160p.BluRay.REMUX.HEVC-GRP.mkv",
		"Show.S01E03.2160p.BluRay.REMUX.HEVC-GRP.mkv"
	e.release(rel, map[string]float64{f1: 2900, f2: 2700, f3: 2700})
	e.seriesDownload(2, 2, 20, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", rel, "[201, 202, 203]", nil)
	e.exec(`UPDATE download SET quality = 'Bluray-2160p Remux', indexer = 'import' WHERE id = 2`)
	e.exec(`INSERT INTO download_file (download_id, rel, episodes, source, how, updated_at) VALUES
		(2, ?, '[201]', 'manual', 'manual', 0), (2, ?, '[202]', 'manual', 'manual', 0), (2, ?, '[]', 'manual', 'manual', 0)`,
		f1, f2, f3)
	if _, err := e.lib.Import(e.ctx, 2, false); err != nil {
		t.Fatal(err)
	}
	byEp := map[int64]string{}
	for _, ep := range []int64{201, 202, 203} {
		byEp[ep] = e.one(`SELECT l.path FROM link l JOIN link_episode le ON le.link_id = l.id WHERE le.episode_id = ?`, ep)
	}
	id := e.linkOf(byEp[202])
	r, err := e.lib.ReassignEpisodes(e.ctx, id, []int64{201}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT episodes || source || how FROM download_file WHERE rel = ?`, f1); got != "[]manual"+HowReplaced {
		t.Fatalf("the displaced file's row: %s", got)
	}
	// An ordinary manual file with no episodes is still decided by its name.
	if got := e.one(`SELECT episodes || source || how FROM download_file WHERE rel = ?`, f3); got != "[]manualmanual" {
		t.Fatalf("E03's row: %s", got)
	}
	e.exec(`UPDATE download SET state = 'ready' WHERE id = 2`)
	plan, err := e.lib.Import(e.ctx, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, lk := range plan.Links {
		got[lk.File] = filepath.Base(lk.Path)
	}
	if len(got) != 2 || got[f2] != filepath.Base(r.Path) || got[f3] != filepath.Base(byEp[203]) {
		t.Fatalf("re-import planned %v (unmatched %v)", got, plan.Unmatched)
	}
	if !strings.HasPrefix(plan.Unresolved[f1], "replaced:") {
		t.Fatalf("the displaced file: %q", plan.Unresolved[f1])
	}
	if e.linkEps(id) != "201" || e.one(`SELECT path || state FROM link WHERE id = ?`, id) != r.Path+"active" {
		t.Fatalf("correction not kept: %s", e.linkEps(id))
	}
	if got := e.epStates(); got != "201:cutoff_met 202:wanted 203:cutoff_met" {
		t.Fatalf("states %s", got)
	}
}

// TestReassignReplaceManualByNameKeepsRest: a manual file chosen with no episodes, resolved by its name as
// E01–E02, is displaced from E01. Its row records the episodes it still holds (E02, a choice by hand), not replaced:
// a re-import links E02 from it again, and E01 stays corrected.
func TestReassignReplaceManualByNameKeepsRest(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES (203, 2, 1, 3, 'Third', 0, 0)`)
	rel := "Show.S01.2160p.BluRay.REMUX.HEVC-GRP"
	f12, f3 := "Show.S01E01E02.2160p.BluRay.REMUX.HEVC-GRP.mkv", "Show.S01E03.2160p.BluRay.REMUX.HEVC-GRP.mkv"
	e.release(rel, map[string]float64{f12: 5400, f3: 2700})
	e.seriesDownload(2, 2, 20, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", rel, "[201, 202, 203]", nil)
	e.exec(`UPDATE download SET quality = 'Bluray-2160p Remux', indexer = 'import' WHERE id = 2`)
	e.exec(`INSERT INTO download_file (download_id, rel, episodes, source, how, updated_at) VALUES
		(2, ?, '[]', 'manual', 'manual', 0), (2, ?, '[203]', 'manual', 'manual', 0)`, f12, f3)
	if _, err := e.lib.Import(e.ctx, 2, false); err != nil {
		t.Fatal(err)
	}
	pack := e.one(`SELECT l.path FROM link l JOIN link_episode le ON le.link_id = l.id WHERE le.episode_id = 201`)
	if other := e.linkOf(pack); e.linkEps(other) != "201,202" {
		t.Fatalf("the pack file links %s", e.linkEps(other))
	}
	id := e.linkOf(e.one(`SELECT l.path FROM link l JOIN link_episode le ON le.link_id = l.id WHERE le.episode_id = 203`))
	r, err := e.lib.ReassignEpisodes(e.ctx, id, []int64{201}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT episodes || source || how FROM download_file WHERE rel = ?`, f12); got != "[202]manualmanual" {
		t.Fatalf("the displaced file's row: %s", got)
	}
	e.exec(`UPDATE download SET state = 'ready' WHERE id = 2`)
	plan, err := e.lib.Import(e.ctx, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, lk := range plan.Links {
		got[lk.File] = filepath.Base(lk.Path)
	}
	if len(got) != 2 || got[f3] != filepath.Base(r.Path) || got[f12] == "" {
		t.Fatalf("re-import planned %v (unmatched %v, unresolved %v)", got, plan.Unmatched, plan.Unresolved)
	}
	if e.linkEps(id) != "201" || e.one(`SELECT path || state FROM link WHERE id = ?`, id) != r.Path+"active" {
		t.Fatalf("correction not kept: %s", e.linkEps(id))
	}
	if got := e.linkEps(e.linkOf(e.one(`SELECT l.path FROM link l JOIN link_episode le ON le.link_id = l.id
		WHERE le.episode_id = 202 AND l.state = 'active'`))); got != "202" {
		t.Fatalf("E02 links as %s", got)
	}
	if got := e.epStates(); got != "201:cutoff_met 202:cutoff_met 203:wanted" {
		t.Fatalf("states %s", got)
	}
}

// A naming that gives no file name (a format rendering nothing, or a regex timeout under a starved CPU) is refused by
// change episodes and import, and rename skips the links: no "Season 01/.mkv" link, the link stays as it was.
func TestEmptyFileNameRefusedLinkKept(t *testing.T) {
	t.Parallel()
	e, byEp := reassignEnv(t)
	id := e.linkOf(byEp[201])
	target, _ := os.Readlink(byEp[201])
	before := tree(t, e.shows)
	e.pol.Versions["tv"].Naming.File = "{Release Hash}"
	if r, err := e.lib.ReassignEpisodes(e.ctx, id, []int64{203}, false); err == nil || !strings.Contains(err.Error(), "empty file name") {
		t.Fatalf("reassign %+v %v", r, err)
	}
	if p, err := e.lib.ApplyRename(e.ctx, 2, nil); err != nil || len(p.Files) != 0 || p.Skipped != 2 {
		t.Fatalf("rename %+v %v (the links are skipped)", p, err)
	}
	if got, err := os.Readlink(byEp[201]); err != nil || got != target {
		t.Fatalf("link %q %v", got, err)
	}
	if got := e.one(`SELECT path FROM link WHERE id = ?`, id); got != byEp[201] {
		t.Fatalf("row path %s", got)
	}
	if got := e.linkEps(id); got != "201" {
		t.Fatalf("link episodes %s", got)
	}
	if after := tree(t, e.shows); strings.Join(before, "|") != strings.Join(after, "|") {
		t.Errorf("the library changed:\n%v\n%v", before, after)
	}
	rel := "Show.S01E03.2160p.BluRay.REMUX.HEVC-GRP"
	e.release(rel, map[string]float64{rel + ".mkv": 2700})
	e.seriesDownload(3, 2, 20, hashA, rel, "[203]", nil)
	if _, err := e.lib.Import(e.ctx, 3, false); err == nil || !strings.Contains(err.Error(), "empty file name") {
		t.Fatalf("import %v", err)
	}
	if after := tree(t, e.shows); strings.Join(before, "|") != strings.Join(after, "|") {
		t.Errorf("the import changed the library:\n%v\n%v", before, after)
	}
}
