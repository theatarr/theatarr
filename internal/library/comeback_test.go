package library

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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
	if n, err := e.lib.MarkBrokenPlaying(e.ctx, id, []PlayingFile{{LinkID: 1, Offset: 1 << 20}}, true,
		[]string{"rd"}); err != nil || n != 1 {
		t.Fatalf("marked %d: %v", n, err)
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

// A returned copy whose film another file holds now is the later comparison's: left unsettled and dead. So is one
// while paused, or of a version no longer monitored.
func TestComeBackLeavesCoveredOrStoppedCopy(t *testing.T) {
	for name, change := range map[string]func(e *env){
		"covered": func(e *env) {
			e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at)
				VALUES (2, '/media/movies/new.mkv', '/x/new.mkv', 1, 'active', 0, 0)`)
			e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (2, 10)`)
		},
		"paused":      func(e *env) { e.pol.Router.Enabled = false },
		"unmonitored": func(e *env) { e.exec(`UPDATE version SET monitored = 0 WHERE id = 10`) },
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

// An old copy that reads again in a renamed folder is not called revived until its link works: the come-back hands it
// to the sweep's ordinary repoint, and settles it (lifting the miss) on the next pass that sees the link active. A
// sweep a zurg rename started is not one of the come-back's hourly sweeps.
func TestComeBackRenamedWaitsForRepoint(t *testing.T) {
	e, id := brokenWhilePlaying(t)
	if err := os.Rename(filepath.Join(e.mount, "__all__", "Name"), filepath.Join(e.mount, "__all__", "Name.Renamed")); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE content SET mount_folder = 'Name.Renamed' WHERE id = 1`)
	before := e.lib.LastSweep()
	if _, err := e.lib.RelinkRenamed(e.ctx, map[string]string{"Name": "Name.Renamed"}); err != nil {
		t.Fatal(err)
	}
	if e.lib.LastSweep() != before {
		t.Error("a rename's sweep counted as an hourly sweep")
	}
	for range 2 {
		if err := e.lib.ComeBack(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.now = e.now.Add(time.Hour)
	}
	if got := e.one(`SELECT settled FROM playing_break WHERE id = ?`, id) + " " + e.one(`SELECT state FROM link WHERE id = 1`) +
		" " + e.one(`SELECT COUNT(*) FROM provider_cache WHERE source = 'router:playing'`); got != " dead 1" {
		t.Fatalf("before the repoint: %q", got)
	}
	e.sweep()
	if got := e.one(`SELECT state || ' ' || target FROM link WHERE id = 1`); got != "active "+filepath.Join(e.mount, "__all__", "Name.Renamed", "episode.mkv") {
		t.Fatalf("the sweep did not repoint it: %s", got)
	}
	if err := e.lib.ComeBack(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT settled FROM playing_break WHERE id = ?`, id) + " " +
		e.one(`SELECT COUNT(*) FROM provider_cache WHERE source = 'router:playing'`); got != "revived 0" {
		t.Errorf("after the repoint: %q", got)
	}
}
