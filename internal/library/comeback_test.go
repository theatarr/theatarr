package library

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/library/decypharrtest"
	"github.com/theatarr/theatarr/internal/outcome"
	"github.com/theatarr/theatarr/internal/policy"
)

// brokenWhilePlaying: deadEnv's movie link marked dead by a playing incident (its file stays on the mount, as when
// zurg repairs it), the release's miss on rd. It returns the incident.
func brokenWhilePlaying(t *testing.T) (*env, int64) {
	t.Helper()
	e, _, _ := deadEnv(t, true)
	e.pol.Router = &policy.Router{Enabled: true}
	// The file is big enough for a piece read at the saved range.
	tgt := filepath.Join(e.mount, "__all__", "Name", "episode.mkv")
	if err := os.WriteFile(tgt, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(tgt, 4<<20); err != nil {
		t.Fatal(err)
	}
	id, err := e.lib.OpenPlayingIncident(e.ctx, PlayingIncident{TitleID: 1, Version: "movie-remux"},
		PlayingFile{LinkID: 1, Offset: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := e.lib.MarkBrokenPlaying(e.ctx, id, PlayingFile{LinkID: 1, Target: e.linkTarget(1), Offset: 1 << 20}, true,
		[]string{"rd"}); err != nil || !ok {
		t.Fatalf("marked %v: %v", ok, err)
	}
	return e, id
}

// The sweep leaves a dead link of an open incident alone while its target is the old copy: its file listed again is
// not revived by the listing (it comes back only through a read); once the incident settles the sweep revives it.
func TestSweepLeavesUnsettledDeadLink(t *testing.T) {
	e, id := brokenWhilePlaying(t)
	e.sweep()
	if got := e.one(`SELECT state FROM link WHERE id = 1`); got != "dead" {
		t.Fatalf("revived by the listing: %s", got)
	}
	e.exec(`UPDATE playing_break SET settled = 'aside' WHERE id = ?`, id)
	e.sweep()
	if got := e.one(`SELECT state FROM link WHERE id = 1`); got != "active" {
		t.Errorf("after the incident settled: %s", got)
	}
}

// Nor is it repointed into a renamed folder that holds its file.
func TestSweepDoesNotRepointUnsettledDeadLink(t *testing.T) {
	e, _ := brokenWhilePlaying(t)
	if err := os.Rename(filepath.Join(e.mount, "__all__", "Name"), filepath.Join(e.mount, "__all__", "Name {"+hashA[:6]+"}")); err != nil {
		t.Fatal(err)
	}
	e.sweep()
	if got := e.one(`SELECT state || ' ' || target FROM link WHERE id = 1`); got != "dead "+filepath.Join(e.mount, "__all__", "Name", "episode.mkv") {
		t.Errorf("link %s", got)
	}
}

// A replacement imported at the same path (same row, a new target) is an ordinary active link to the sweep, and the
// incident keeps the old copy's facts.
func TestSweepTreatsSamePathReplacementAsActive(t *testing.T) {
	e, id := brokenWhilePlaying(t)
	newTarget := filepath.Join(e.mount, "__all__", "Gone", "movie.mkv")
	e.exec(`UPDATE link SET target = ?, state = 'active', release_title = 'New', content_file_id = NULL WHERE id = 1`, newTarget)
	if err := os.Remove(filepath.Join(e.movies, "Movie (2001)", "Movie.mkv")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(newTarget, filepath.Join(e.movies, "Movie (2001)", "Movie.mkv")); err != nil {
		t.Fatal(err)
	}
	e.sweep()
	if got := e.one(`SELECT missing_since IS NOT NULL FROM link WHERE id = 1`); got != "1" {
		t.Errorf("the replacement's missing file was not noted: %s", got)
	}
	if got := e.one(`SELECT release_title || ' ' || old_target FROM playing_break WHERE id = ?`, id); got !=
		"Name "+filepath.Join(e.mount, "__all__", "Name", "episode.mkv") {
		t.Errorf("incident %s", got)
	}
}

// The old copy read on two sweeps at least deadConfirm apart, with nothing else holding its film: revived as the sweep
// would, the incident settled revived, the release's miss lifted. A failed read between ends the streak.
func TestComeBackRevivesUncoveredCopy(t *testing.T) {
	e, id := brokenWhilePlaying(t)
	if err := e.lib.ComeBack(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT back_since IS NOT NULL FROM playing_break WHERE id = ?`, id) + " " +
		e.one(`SELECT state FROM link WHERE id = 1`); got != "1 dead" {
		t.Fatalf("after the first read: %s", got)
	}
	e.now = e.now.Add(deadConfirm - time.Minute)
	if err := e.lib.ComeBack(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT state FROM link WHERE id = 1`); got != "dead" {
		t.Fatalf("revived before deadConfirm: %s", got)
	}
	// A failed read (the file gone again) ends the streak.
	tgt := filepath.Join(e.mount, "__all__", "Name", "episode.mkv")
	if err := os.Truncate(tgt, 0); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Hour)
	if err := e.lib.ComeBack(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT back_since IS NULL FROM playing_break WHERE id = ?`, id); got != "1" {
		t.Fatalf("streak kept after a failed read")
	}
	if err := os.Truncate(tgt, 4<<20); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := e.lib.ComeBack(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.now = e.now.Add(time.Hour)
	}
	if got := e.one(`SELECT state FROM link WHERE id = 1`) + " " + e.one(`SELECT settled FROM playing_break WHERE id = ?`, id) +
		" " + e.one(`SELECT COUNT(*) FROM provider_cache WHERE source = 'router:playing'`) + " " +
		e.one(`SELECT state FROM version WHERE id = 10`); got != "active revived 0 filled" {
		t.Errorf("after two reads an hour apart: %s", got)
	}
	if got := e.one(`SELECT detail FROM library_action WHERE kind = 'relink' AND link_id = 1`); got !=
		"revived: the copy that broke while playing reads again" {
		t.Errorf("history %s", got)
	}
}

// A returned copy nothing else holds is left unsettled and dead while paused (a covered one is kept aside:
// comeback_covered_test.go). Monitoring does not matter: a version no longer monitored gets its copy back as the
// sweep's own revive would.
func TestComeBackLeavesStoppedCopy(t *testing.T) {
	for name, change := range map[string]func(e *env){
		"paused": func(e *env) { e.pol.Router.Enabled = false },
	} {
		e, id := brokenWhilePlaying(t)
		change(e)
		for range 3 {
			if err := e.lib.ComeBack(e.ctx); err != nil {
				t.Fatal(err)
			}
			e.now = e.now.Add(time.Hour)
		}
		if got := e.one(`SELECT state FROM link WHERE id = 1`) + " " + e.one(`SELECT settled FROM playing_break WHERE id = ?`, id); got != "dead " {
			t.Errorf("%s: %s", name, got)
		}
	}
}

// An old copy that reads again in a renamed folder (the registry still names the old one) is found by the sweep's
// rename evidence and read there; the sweep itself leaves it alone; on the second hourly read the come-back repoints
// and revives it, and only then settles it and lifts the miss. A sweep a zurg rename started is not one of the
// come-back's hourly sweeps.
func TestComeBackRenamedFolder(t *testing.T) {
	e, id := brokenWhilePlaying(t)
	renamed := "Name {" + hashA[:6] + "}"
	if err := os.Rename(filepath.Join(e.mount, "__all__", "Name"), filepath.Join(e.mount, "__all__", renamed)); err != nil {
		t.Fatal(err)
	}
	before := e.lib.LastSweep()
	if _, err := e.lib.RelinkRenamed(e.ctx, map[string]string{"Name": renamed}); err != nil {
		t.Fatal(err)
	}
	if e.lib.LastSweep() != before {
		t.Error("a rename's sweep counted as an hourly sweep")
	}
	if err := e.lib.ComeBack(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.sweep()
	if got := e.one(`SELECT state FROM link WHERE id = 1`) + " " + e.one(`SELECT back_since IS NOT NULL FROM playing_break WHERE id = ?`, id); got != "dead 1" {
		t.Fatalf("after the first read and a sweep: %q", got)
	}
	e.now = e.now.Add(time.Hour)
	if err := e.lib.ComeBack(e.ctx); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(e.mount, "__all__", renamed, "episode.mkv")
	if got := e.one(`SELECT state || ' ' || target FROM link WHERE id = 1`); got != "active "+want {
		t.Fatalf("after the second read: %s", got)
	}
	if tg, _ := os.Readlink(filepath.Join(e.movies, "Movie (2001)", "Movie.mkv")); tg != want {
		t.Errorf("symlink -> %s", tg)
	}
	if got := e.one(`SELECT settled FROM playing_break WHERE id = ?`, id) + " " +
		e.one(`SELECT COUNT(*) FROM provider_cache WHERE source = 'router:playing'`); got != "revived 0" {
		t.Errorf("incident %q", got)
	}
}

// zurg moved the old torrent to its hash-suffixed folder and another torrent took the bare name with the same file
// name: the come-back reads only where the sweep's own selection puts this torrent (the suffixed folder), and revives
// it there, never the other torrent's file at the bare path.
func TestComeBackSkipsReusedBareName(t *testing.T) {
	e, id := brokenWhilePlaying(t)
	moved := "Name {" + hashA[:6] + "}"
	if err := os.Rename(filepath.Join(e.mount, "__all__", "Name"), filepath.Join(e.mount, "__all__", moved)); err != nil {
		t.Fatal(err)
	}
	// Another torrent now holds the bare folder, with a file of the same name (unreadable as this one: empty).
	if err := os.MkdirAll(filepath.Join(e.mount, "__all__", "Name"), 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(e.mount, "__all__", "Name", "episode.mkv")
	if err := os.WriteFile(other, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(other, 4<<20); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := e.lib.ComeBack(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.now = e.now.Add(time.Hour)
	}
	want := filepath.Join(e.mount, "__all__", moved, "episode.mkv")
	if got := e.one(`SELECT state || ' ' || target FROM link WHERE id = 1`); got != "active "+want {
		t.Errorf("link %s, want active at %s", got, want)
	}
	if got := e.one(`SELECT settled FROM playing_break WHERE id = ?`, id); got != "revived" {
		t.Errorf("incident %q", got)
	}
}

// A rename only decypharr knows (no name in the registry leads to the new folder): the come-back finds it as the sweep
// would, by decypharr's current name for the hash, and revives it there.
func TestComeBackDecypharrRename(t *testing.T) {
	e, id := brokenWhilePlaying(t)
	if err := os.Rename(filepath.Join(e.mount, "__all__", "Name"), filepath.Join(e.mount, "__all__", decyFolder)); err != nil {
		t.Fatal(err)
	}
	e.withDecypharr(decypharrtest.Torrent{Hash: hashA, Name: decyFolder, Provider: "realdebrid"})
	for range 2 {
		if err := e.lib.ComeBack(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.now = e.now.Add(time.Hour)
	}
	want := filepath.Join(e.mount, "__all__", decyFolder, "episode.mkv")
	if got := e.one(`SELECT state || ' ' || target FROM link WHERE id = 1`) + " " +
		e.one(`SELECT settled FROM playing_break WHERE id = ?`, id); got != "active "+want+" revived" {
		t.Errorf("got %s", got)
	}
}

// The saved hash's suffixed folder exists but no longer holds the watched file, while another torrent's same-named
// file reads at the bare name: the come-back reads nothing there, revives nothing, and keeps the miss.
func TestComeBackNoFallbackToBareName(t *testing.T) {
	e, id := brokenWhilePlaying(t)
	moved := "Name {" + hashA[:6] + "}"
	if err := os.MkdirAll(filepath.Join(e.mount, "__all__", moved), 0o755); err != nil { // the torrent, without the file
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.mount, "__all__", moved, "other.nfo"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := e.lib.ComeBack(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.now = e.now.Add(time.Hour)
	}
	if got := e.one(`SELECT state FROM link WHERE id = 1`) + " " + e.one(`SELECT settled FROM playing_break WHERE id = ?`, id) +
		" " + e.one(`SELECT COUNT(*) FROM provider_cache WHERE source = 'router:playing'`) + " " +
		e.one(`SELECT back_since IS NULL FROM playing_break WHERE id = ?`, id); got != "dead  1 1" {
		t.Errorf("got %q", got)
	}
}

// The old torrent left the account and another took its bare folder name with a same-named file (no hash-suffixed
// folder anywhere): where zurg names another hash for that file, the come-back reads nothing there and revives
// nothing; without any such evidence (a plain mount) it reads as before (TestComeBackRevivesUncoveredCopy).
func TestComeBackConfirmsTheOldFolder(t *testing.T) {
	e, id := brokenWhilePlaying(t)
	srv := newZurgServer(t)
	srv.set(func(s *zurgServer) {
		s.entries[davPath("Name", "episode.mkv")] = ZurgEntry{Hash: hashB, Name: "Name"}
	})
	e.withZurg(srv)
	for range 3 {
		if err := e.lib.ComeBack(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.now = e.now.Add(time.Hour)
	}
	if got := e.one(`SELECT state FROM link WHERE id = 1`) + " " + e.one(`SELECT settled FROM playing_break WHERE id = ?`, id) +
		" " + e.one(`SELECT back_since IS NULL FROM playing_break WHERE id = ?`, id); got != "dead  1" {
		t.Errorf("got %q", got)
	}
}

// A come-back read that times out ends the pass (the next pass goes on), and its row is stamped checked: the next pass
// reads the others first, so one file that always hangs cannot hold them back.
func TestComeBackEndsAtATimeOut(t *testing.T) {
	e := playingEnv(t)
	for _, id := range []int64{1, 2} {
		inc, err := e.lib.OpenPlayingIncident(e.ctx, PlayingIncident{TitleID: 2, Version: "tv"}, PlayingFile{LinkID: id})
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := e.lib.MarkBrokenPlaying(e.ctx, inc, PlayingFile{LinkID: id, Target: e.linkTarget(id)}, true,
			nil); err != nil || !ok {
			t.Fatalf("marked %v: %v", ok, err)
		}
	}
	// e1 hangs (a FIFO with no writer blocks in open(2)); e2 reads.
	hung := e.linkTarget(1)
	if err := os.Remove(hung); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(hung, 0o644); err != nil {
		t.Skip("mkfifo:", err)
	}
	defer func() {
		if w, err := os.OpenFile(hung, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			w.Close()
		}
		waitUntil(t, func() bool { return abandonedPieces.Load() == 0 })
	}()
	was := comeBackLimit
	comeBackLimit = 100 * time.Millisecond
	t.Cleanup(func() { comeBackLimit = was })
	back := func() string {
		return e.one(`SELECT group_concat(back_since IS NOT NULL) FROM (SELECT back_since FROM playing_break ORDER BY link_id)`)
	}
	e.now = e.now.Add(time.Minute)
	if err := e.lib.ComeBack(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := back(); got != "0,0" {
		t.Fatalf("after a pass that timed out on e1: %s (e2 read in the same pass)", got)
	}
	e.now = e.now.Add(time.Hour)
	if err := e.lib.ComeBack(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := back(); got != "0,1" {
		t.Errorf("the next pass: %s (e2 not read first)", got)
	}
}

// A watched copy that reads again on two sweeps is revived at once, also while its search is still open; the settle
// pass then reports it once as readable again when that search has ended and queues the search of the release's other
// broken file. The release's miss stays while that other file of the same hash is still dead and unsettled.
func TestComeBackRevivedWatchedIsReportedAndKeepsMiss(t *testing.T) {
	e := playingEnv(t)
	var got []outcome.Outcome
	e.lib.OnPlayingOutcome(func(o outcome.Outcome) { got = append(got, o) })
	id, err := e.lib.OpenPlayingIncident(e.ctx, PlayingIncident{TitleID: 2, Version: "tv"}, PlayingFile{LinkID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := e.lib.MarkBrokenPlaying(e.ctx, id, PlayingFile{LinkID: 1, Target: e.linkTarget(1)}, true,
		[]string{"rd"}); err != nil || !ok {
		t.Fatalf("marked %v: %v", ok, err)
	}
	if ok, err := e.lib.MarkBrokenPlaying(e.ctx, id, PlayingFile{LinkID: 2, Target: e.linkTarget(2)}, false,
		nil); err != nil || !ok {
		t.Fatalf("sibling %v: %v", ok, err)
	}
	if err := os.Truncate(e.linkTarget(2), 0); err != nil { // e2 still does not read
		t.Fatal(err)
	}
	for range 2 { // the watched file's search is still queued
		if err := e.lib.ComeBack(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.now = e.now.Add(deadConfirm + time.Minute)
	}
	if got := e.one(`SELECT state FROM link WHERE id = 1`) + " " + e.one(`SELECT settled FROM playing_break WHERE id = ?`, id) +
		" " + e.one(`SELECT COUNT(*) FROM provider_cache WHERE source = 'router:playing'`); got != "active revived 1" {
		t.Fatalf("after two reads: %s (want revived, the miss kept for e2)", got)
	}
	if err := e.lib.SettlePlayingRuns(e.ctx); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || len(e.playingJobs()) != 1 {
		t.Fatalf("while its search is open: outcomes %+v, searches %+v", got, e.playingJobs())
	}
	e.exec(`UPDATE job SET state = 'done'`)
	for range 2 {
		if err := e.lib.SettlePlayingRuns(e.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 1 || got[0].Kind != outcome.SameRelease {
		t.Errorf("outcomes %+v", got)
	}
	if jobs := e.playingJobs(); len(jobs) != 2 || fmt.Sprint(jobs[1].Episodes) != "[202]" {
		t.Errorf("searches %+v", jobs)
	}
}

// A version no longer monitored: the copy that reads again on two sweeps is revived all the same.
func TestComeBackRevivesUnmonitoredCopy(t *testing.T) {
	for name, q := range map[string]string{
		"version": `UPDATE version SET monitored = 0 WHERE id = 10`,
		"title":   `UPDATE title SET monitored = 0 WHERE id = 1`,
	} {
		e, id := brokenWhilePlaying(t)
		e.exec(q)
		for range 2 {
			if err := e.lib.ComeBack(e.ctx); err != nil {
				t.Fatal(err)
			}
			e.now = e.now.Add(time.Hour)
		}
		if got := e.one(`SELECT state FROM link WHERE id = 1`) + " " + e.one(`SELECT settled FROM playing_break WHERE id = ?`, id); got != "active revived" {
			t.Errorf("%s unmonitored: %s", name, got)
		}
	}
}

// A row whose link was removed by hand with its files kept, or whose title is gone, is settled at once without a
// read (it would never be read again): the kept symlink is not touched and the release's miss stays.
func TestComeBackSettlesRemovedRowsUnread(t *testing.T) {
	for name, q := range map[string]string{
		"unlinked":    `UPDATE link SET state = 'unlinked' WHERE id = 1`,
		"title gone":  `DELETE FROM version WHERE id = 10`,
		"arr removed": `INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (10, 'ui', 0)`,
	} {
		e, id := brokenWhilePlaying(t)
		e.exec(q)
		if err := e.lib.ComeBack(e.ctx); err != nil {
			t.Fatal(err)
		}
		if got := e.one(`SELECT settled || ' ' || (back_since IS NULL) FROM playing_break WHERE id = ?`, id) + " " +
			e.one(`SELECT COUNT(*) FROM provider_cache WHERE source = 'router:playing'`); got != "aside 1 1" {
			t.Errorf("%s: %s", name, got)
		}
		if _, err := os.Lstat(filepath.Join(e.movies, "Movie (2001)", "Movie.mkv")); err != nil {
			t.Errorf("%s: the symlink was moved: %v", name, err)
		}
	}
}

// A version removed with "Keep files" while its file was broken: the row is settled unread, and the kept dead link,
// which belongs only to removed versions, stays out of the ordinary sweep (no revive from the listing, no rewrite
// after its folder is renamed) over later maintenance passes.
func TestRemovedVersionKeepsItsDeadLinkOutOfTheSweep(t *testing.T) {
	e, id := brokenWhilePlaying(t)
	e.exec(`UPDATE version SET monitored = 0 WHERE id = 10`)
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (10, 'ui', 0)`)
	target := e.linkTarget(1)
	for range 2 {
		if err := e.lib.ComeBack(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.sweep()
		e.now = e.now.Add(time.Hour)
	}
	if got := e.one(`SELECT settled FROM playing_break WHERE id = ?`, id) + " " +
		e.one(`SELECT state || ' ' || target FROM link WHERE id = 1`); got != "aside dead "+target {
		t.Fatalf("after two passes: %s", got)
	}
	if err := os.Rename(filepath.Join(e.mount, "__all__", "Name"), filepath.Join(e.mount, "__all__", "Name {"+hashA[:6]+"}")); err != nil {
		t.Fatal(err)
	}
	e.sweep()
	if got := e.one(`SELECT state || ' ' || target FROM link WHERE id = 1`); got != "dead "+target {
		t.Errorf("after a rename: %s", got)
	}
}

// A dead link whose symlink was removed from the library folder by hand is settled unread: its mount file reading
// again does not make a link without a library path active.
func TestComeBackSettlesRemovedSymlinkUnread(t *testing.T) {
	e, id := brokenWhilePlaying(t)
	e.exec(`UPDATE version SET monitored = 0 WHERE id = 10`)
	if err := os.Remove(filepath.Join(e.movies, "Movie (2001)", "Movie.mkv")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := e.lib.ComeBack(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.now = e.now.Add(time.Hour)
	}
	if got := e.one(`SELECT settled || ' ' || (back_since IS NULL) FROM playing_break WHERE id = ?`, id) + " " +
		e.one(`SELECT state FROM link WHERE id = 1`); got != "aside 1 dead" {
		t.Errorf("got %s", got)
	}
}

// The watched copy removed by hand before its search ended: its row is settled unread as removed from the library
// (no outcome, no History row), and the settle pass still queues the search of its release's other broken file.
func TestComeBackWatchedRemovedStillSearchesSiblings(t *testing.T) {
	e := playingEnv(t)
	var got []outcome.Outcome
	e.lib.OnPlayingOutcome(func(o outcome.Outcome) { got = append(got, o) })
	id, err := e.lib.OpenPlayingIncident(e.ctx, PlayingIncident{TitleID: 2, Version: "tv"}, PlayingFile{LinkID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := e.lib.MarkBrokenPlaying(e.ctx, id, PlayingFile{LinkID: 1, Target: e.linkTarget(1)}, true, nil); err != nil || !ok {
		t.Fatalf("marked %v: %v", ok, err)
	}
	if ok, err := e.lib.MarkBrokenPlaying(e.ctx, id, PlayingFile{LinkID: 2, Target: e.linkTarget(2)}, false, nil); err != nil || !ok {
		t.Fatalf("sibling %v: %v", ok, err)
	}
	e.exec(`UPDATE link SET state = 'unlinked' WHERE id = 1`) // removed with its files kept
	if err := e.lib.ComeBack(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT settled || ' ' || reported FROM playing_break WHERE id = ?`, id); got != "aside removed" {
		t.Fatalf("watched row %s", got)
	}
	e.exec(`UPDATE job SET state = 'done'`)
	if err := e.lib.SettlePlayingRuns(e.ctx); err != nil {
		t.Fatal(err)
	}
	if jobs := e.playingJobs(); len(jobs) != 2 || fmt.Sprint(jobs[1].Episodes) != "[202]" {
		t.Errorf("searches %+v", jobs)
	}
	if len(got) != 0 {
		t.Errorf("outcomes %+v", got)
	}
}

// A library share that is away is not a deliberate removal: with the root missing during a pass, the dead row stays
// open (not removed), and once the share is back the sweep does not revive the link from its listing; only the
// come-back's own reads can.
func TestComeBackLeavesRowOpenWhileRootAway(t *testing.T) {
	e, id := brokenWhilePlaying(t)
	away := e.movies + ".away"
	if err := os.Rename(e.movies, away); err != nil {
		t.Fatal(err)
	}
	if err := e.lib.ComeBack(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT settled || '/' || reported FROM playing_break WHERE id = ?`, id); got != "/" {
		t.Fatalf("with the root away: %q", got)
	}
	if err := os.Rename(away, e.movies); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Hour)
	if err := e.lib.ComeBack(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.sweep()
	if got := e.one(`SELECT state FROM link WHERE id = 1`) + " " + e.one(`SELECT settled FROM playing_break WHERE id = ?`, id); got != "dead " {
		t.Errorf("after the share came back: %s", got)
	}
}

// A come-back pass that outlives a settings save asks the live settings before it writes: with Pause saved meanwhile
// (the pass's own library still says it may write), nothing is revived or moved; the next pass on writable settings
// does it.
func TestComeBackAsksLiveSettingsBeforeWriting(t *testing.T) {
	for _, covered := range []bool{false, true} {
		e, id := brokenWhilePlaying(t)
		e.exec(`UPDATE playing_break SET reported = 'unavailable' WHERE id = ?`, id) // its search has ended
		if covered {                                                                 // another file holds the film: the copy would be kept aside (its dead link to the trash)
			e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at) VALUES (5, '/media/other.mkv',
				'/mnt/other.mkv', 1, 'active', 0, 0)`)
			e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (5, 10)`)
		}
		paused := true
		e.lib.SetLiveWritable(func(string) bool { return !paused })
		for range 2 {
			if err := e.lib.ComeBack(e.ctx); err != nil {
				t.Fatal(err)
			}
			e.now = e.now.Add(time.Hour)
		}
		if got := e.one(`SELECT state FROM link WHERE id = 1`) + " " + e.one(`SELECT settled FROM playing_break WHERE id = ?`, id); got != "dead " {
			t.Errorf("covered %v, Pause saved: %s", covered, got)
		}
		paused = false
		if err := e.lib.ComeBack(e.ctx); err != nil {
			t.Fatal(err)
		}
		want := "active revived"
		if covered {
			want = "unlinked aside" // its dead link moved to the trash
		}
		if got := e.one(`SELECT state FROM link WHERE id = 1`) + " " + e.one(`SELECT settled FROM playing_break WHERE id = ?`, id); got != want {
			t.Errorf("covered %v, Pause off again: %s", covered, got)
		}
	}
}
