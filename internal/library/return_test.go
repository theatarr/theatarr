package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/outcome"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/rootprobe/probetest"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// Undo an upgrade (return.go): the file an import displaced is put back from its trash row's snapshot.

// readable makes a mount file big enough for a return's piece read.
func (e *env) readable(p string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Truncate(p, 4<<20); err != nil {
		e.t.Fatal(err)
	}
}

// adoptedFilm is an adopted WEBDL-1080p film (link 1, no download) upgraded by download 1 (movieRelease) at another
// path. It returns the old path, its target, and the History action id of the replacement.
func adoptedFilm(t *testing.T) (e *env, old, target string, action int64) {
	t.Helper()
	e = importUpEnv(t, "")
	old = filepath.Join(e.movies, "Amelie The Movie (2001)", "held.mkv")
	target = filepath.Join(e.mount, "__all__", "Held", "held.mkv")
	e.readable(target)
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, old); err != nil {
		t.Fatal(err)
	}
	e.adoptedFile(1, 10, 1, old, target, "WEBDL-1080p", "Amelie.2001.held-OLD")
	e.exec(`UPDATE link SET adopted = 1, adopted_source = 'radarr', folder_hash = 'feedbeef', score = NULL WHERE id = 1`)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT state FROM link WHERE id = 1`); got != "unlinked" {
		t.Fatalf("the old film is %s", got)
	}
	var id int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM library_action WHERE kind = 'replace' AND download_id = 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return e, old, target, id
}

func TestUndoAdoptedFilm(t *testing.T) {
	t.Parallel()
	e, old, target, action := adoptedFilm(t)
	before := e.one(`SELECT json_array(path, target, kind, title_id, COALESCE(content_file_id, 'null'),
		COALESCE(download_id, 'null'), release_title, quality, COALESCE(score, 'null'), file_metadata, adopted,
		adopted_from, adopted_source, adopted_media, folder_hash) FROM link WHERE id = 1`)
	newPath := e.one(`SELECT path FROM link WHERE download_id = 1`)
	offers, err := e.lib.UndoOffers(e.ctx, []int64{action})
	if err != nil {
		t.Fatal(err)
	}
	if got := offers[action]; got != "Puts the earlier file back and blocks only "+movieRelease+" in this library: "+
		"another better release can still take the file's place" {
		t.Fatalf("offer %q", got)
	}
	signals := 0
	e.lib.SetRecordChanged(func() { signals++ })
	u, err := e.lib.UndoUpgrade(e.ctx, action, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if signals != 1 {
		t.Errorf("usage worker told %d times", signals)
	}
	if u.Path != old || u.Release != movieRelease {
		t.Errorf("undone %+v", u)
	}
	if got, _ := os.Readlink(old); got != target {
		t.Errorf("old path points at %q", got)
	}
	if _, err := os.Lstat(newPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the upgrade's file stayed: %v", err)
	}
	// The same row, every fact as it was.
	if got := e.one(`SELECT state FROM link WHERE id = 1`); got != "active" {
		t.Errorf("old row %s", got)
	}
	if got := e.one(`SELECT json_array(path, target, kind, title_id, COALESCE(content_file_id, 'null'),
		COALESCE(download_id, 'null'), release_title, quality, COALESCE(score, 'null'), file_metadata, adopted,
		adopted_from, adopted_source, adopted_media, folder_hash) FROM link WHERE id = 1`); got != before {
		t.Errorf("facts\n got %s\nwant %s", got, before)
	}
	if got := e.one(`SELECT state FROM link WHERE path = ?`, newPath); got != "unlinked" {
		t.Errorf("upgrade's row %s", got)
	}
	// Blocked in this library, with no researched time (no search was handled for it; none starts:
	// TestUndoStartsNoSearch).
	if got := e.one(`SELECT source || '/' || (researched_at IS NOT NULL) FROM router_blocklist WHERE version_key = 'movie-remux'
		AND info_hash = ?`, hashA); got != "undo:ann/0" {
		t.Errorf("blocklist %s", got)
	}
	// History: "Upgrade undone", never "Removed".
	if got := e.one(`SELECT group_concat(event) FROM history WHERE event IN ('undone', 'removed')`); got != "undone" {
		t.Errorf("history %s", got)
	}
	if got := e.one(`SELECT release FROM history WHERE event = 'undone'`); got != movieRelease {
		t.Errorf("undone row's release %s", got)
	}
	// One History row for one Undo: no "Blocked" row beside it (the blocklist entry stays).
	if got := e.one(`SELECT COUNT(*) FROM history WHERE event = 'blocked'`); got != "0" {
		t.Errorf("blocked rows %s", got)
	}
	// Twice: refused, nothing changes.
	if offers, _ := e.lib.UndoOffers(e.ctx, []int64{action}); offers[action] != "" {
		t.Errorf("offered again: %q", offers[action])
	}
	var rr *ReturnRefused
	if _, err := e.lib.UndoUpgrade(e.ctx, action, "ann"); !errors.As(err, &rr) || rr.Sentence != returnChanged {
		t.Errorf("second undo: %v", err)
	}
	if got, _ := os.Readlink(old); got != target {
		t.Errorf("after the second undo: %q", got)
	}
}

// A replacement whose trash row has no snapshot (before 00152) or whose trash copy was purged offers no Undo.
func TestUndoNeedsSnapshot(t *testing.T) {
	t.Parallel()
	for _, change := range []string{`UPDATE link_trash SET snapshot = NULL`, `UPDATE link_trash SET purged_at = 1`} {
		e, _, _, action := adoptedFilm(t)
		e.exec(change)
		if offers, err := e.lib.UndoOffers(e.ctx, []int64{action}); err != nil || len(offers) != 0 {
			t.Errorf("%s: offers %v, %v", change, offers, err)
		}
		if _, err := e.lib.UndoUpgrade(e.ctx, action, ""); err == nil {
			t.Errorf("%s: undone", change)
		}
	}
}

// The earlier file gone from the mount: refused, nothing changes.
func TestUndoEarlierFileGone(t *testing.T) {
	t.Parallel()
	e, old, target, action := adoptedFilm(t)
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if offers, _ := e.lib.UndoOffers(e.ctx, []int64{action}); len(offers) != 0 {
		t.Errorf("offered: %v", offers)
	}
	var rr *ReturnRefused
	if _, err := e.lib.UndoUpgrade(e.ctx, action, ""); !errors.As(err, &rr) || rr.Sentence != returnGone {
		t.Fatalf("undo: %v", err)
	}
	if _, err := os.Lstat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old path: %v", err)
	}
	if got := e.one(`SELECT COUNT(*) FROM router_blocklist`); got != "0" {
		t.Errorf("blocklist rows %s", got)
	}
}

// The successor changed meanwhile (a later import took the slot): refused.
func TestUndoSuccessorChanged(t *testing.T) {
	t.Parallel()
	e, _, _, action := adoptedFilm(t)
	e.exec(`UPDATE link SET target = target || '.other' WHERE download_id = 1`)
	var rr *ReturnRefused
	if _, err := e.lib.UndoUpgrade(e.ctx, action, ""); !errors.As(err, &rr) || rr.Sentence != returnChanged {
		t.Fatalf("undo: %v", err)
	}
}

// A path another library shares: refused.
func TestUndoSharedPath(t *testing.T) {
	t.Parallel()
	e, _, _, action := adoptedFilm(t)
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (11, 1, 'movie-web', 0, 0)`)
	e.exec(`INSERT INTO link_version (link_id, version_id) SELECT id, 11 FROM link WHERE download_id = 1`)
	var rr *ReturnRefused
	if _, err := e.lib.UndoUpgrade(e.ctx, action, ""); !errors.As(err, &rr) || rr.Sentence != returnShared {
		t.Fatalf("undo: %v", err)
	}
}

// A write that fails after the link was made (the commit refused) takes the symlinks back and leaves no row.
func TestUndoFailedWriteRollsBack(t *testing.T) {
	t.Parallel()
	e, old, _, action := adoptedFilm(t)
	newPath := e.one(`SELECT path FROM link WHERE download_id = 1`)
	newTarget, _ := os.Readlink(newPath)
	rows := e.one(`SELECT (SELECT COUNT(*) FROM library_action) || '/' || (SELECT COUNT(*) FROM link_trash) || '/' ||
		(SELECT COUNT(*) FROM history)`)
	// The blocklist write fails inside the commit.
	e.exec(`CREATE TRIGGER no_block BEFORE INSERT ON router_blocklist BEGIN SELECT RAISE(ABORT, 'no'); END`)
	if _, err := e.lib.UndoUpgrade(e.ctx, action, ""); err == nil {
		t.Fatal("undone")
	}
	if _, err := os.Lstat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old path left: %v", err)
	}
	if got, _ := os.Readlink(newPath); got != newTarget {
		t.Errorf("the upgrade's link is %q", got)
	}
	if got := e.one(`SELECT (SELECT COUNT(*) FROM library_action) || '/' || (SELECT COUNT(*) FROM link_trash) || '/' ||
		(SELECT COUNT(*) FROM history)`); got != rows {
		t.Errorf("rows %s, were %s", got, rows)
	}
	if got := e.one(`SELECT state FROM link WHERE id = 1`); got != "unlinked" {
		t.Errorf("old row %s", got)
	}
	trash, _ := filepath.Glob(filepath.Join(e.trash, "*", "*", "*", "*"))
	if len(trash) != 1 { // the import's own copy of the adopted file
		t.Errorf("trash %v", trash)
	}
}

// An upgrade at the same path (a better score, same name): Undo points the path at the earlier file again and puts
// its row's facts back.
func TestUndoSamePathUpgrade(t *testing.T) {
	t.Parallel()
	e := importUpEnv(t, "")
	e.pol.Profiles["movie-state"].CutoffScore = 1000
	engine, err := decide.New(e.pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.lib.engine = engine
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	path := e.one(`SELECT path FROM link WHERE download_id = 1`)
	first, _ := os.Readlink(path)
	e.readable(first)
	before := e.one(`SELECT json_array(id, target, content_file_id, download_id, release_title, quality, score, file_metadata)
		FROM link WHERE path = ?`, path)
	good := "Amelie.2001.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1.GOOD-GRP"
	e.release(good, map[string]float64{"movie.mkv": 7000})
	e.download(2, "movie-remux", 1, 10, good, good, "[]")
	e.exec(`UPDATE download SET info_hash = '1111111111111111111111111111111111111111' WHERE id = 2`)
	if _, err := e.lib.Import(e.ctx, 2, false); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT download_id FROM link WHERE path = ?`, path); got != "2" {
		t.Fatalf("not upgraded in place: download %s", got)
	}
	var action int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM library_action WHERE kind = 'replace' AND download_id = 2`).Scan(&action); err != nil {
		t.Fatal(err)
	}
	if _, err := e.lib.UndoUpgrade(e.ctx, action, "ann"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(path); got != first {
		t.Errorf("path points at %q, want %q", got, first)
	}
	if got := e.one(`SELECT json_array(id, target, content_file_id, download_id, release_title, quality, score, file_metadata)
		FROM link WHERE path = ?`, path); got != before {
		t.Errorf("facts\n got %s\nwant %s", got, before)
	}
	if got := e.one(`SELECT COUNT(*) FROM router_blocklist WHERE info_hash = '1111111111111111111111111111111111111111'`); got != "1" {
		t.Errorf("blocklist %s", got)
	}
	// No search was queued for the film.
	if got := e.one(`SELECT COUNT(*) FROM job WHERE kind LIKE 'acquire%'`); got != "0" {
		t.Errorf("searches %s", got)
	}
}

// The current file holds an episode the earlier one does not: refused with the episodes named.
func TestUndoDisplacedExtraEpisode(t *testing.T) {
	t.Parallel()
	e := importUpEnv(t, "")
	old := e.heldLink(1, "WEBDL-1080p", "Show.S01E01.1080p.WEB-DL.DDP5.1.H.264-OLD", 201)
	target, _ := os.Readlink(old)
	e.readable(target)
	rel := "Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP"
	e.release(rel, map[string]float64{rel + ".mkv": 2700})
	e.seriesDownload(1, 2, 20, hashA, rel, "[201]", nil)
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO link_episode (link_id, episode_id) SELECT id, 202 FROM link WHERE download_id = 1`)
	var action int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM library_action WHERE kind = 'replace' AND download_id = 1`).Scan(&action); err != nil {
		t.Fatal(err)
	}
	var rr *ReturnRefused
	_, err := e.lib.UndoUpgrade(e.ctx, action, "")
	if !errors.As(err, &rr) || rr.Sentence != "The current file also holds episode 2; the earlier file holds only episode 1" {
		t.Fatalf("undo: %v", err)
	}
	if _, err := os.Lstat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old path: %v", err)
	}
}

// E13 and F25: a gone (dead) file replaced by a lower quality is a replacement, not an upgrade (History's Replaced,
// the journal's kind grab); an adopted file's replacement is an upgrade.
func TestReplacementOfGoneFileIsNoUpgrade(t *testing.T) {
	t.Parallel()
	kind := func(e *env) string {
		return e.one(`SELECT ` + store.JournalGrabKindSQL + ` FROM download d WHERE d.id = 1`)
	}
	// History's own kind is as before: an earlier import of the version's episodes.
	historyKind := func(e *env) string {
		return e.one(`SELECT ` + store.HistoryGrabKindSQL + ` FROM download d WHERE d.id = 1`)
	}
	t.Run("dead, lower", func(t *testing.T) {
		e := importUpEnv(t, "")
		p := filepath.Join(e.movies, "Amelie The Movie (2001)", "held.mkv")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(e.mount, "__all__", "Gone", "held.mkv"), p); err != nil {
			t.Fatal(err)
		}
		e.exec(`INSERT INTO link (id, path, target, kind, title_id, release_title, quality, state, created_at, updated_at)
			VALUES (1, ?, ?, 'symlink', 1, 'Amelie.2001.held-OLD', 'Remux-2160p', 'dead', 0, 0)`, p,
			filepath.Join(e.mount, "__all__", "Gone", "held.mkv"))
		e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 10)`)
		rel := "Amelie.2001.1080p.WEB-DL.DDP5.1.H.264-GRP"
		e.release(rel, map[string]float64{"movie.mkv": 7000})
		e.videoSize(rel, 1920, 1080)
		e.download(1, "movie-remux", 1, 10, rel, rel, "[]")
		e.exec(`UPDATE download SET quality = 'WEBDL-1080p' WHERE id = 1`)
		// An earlier import of the version (the old rule's upgrade).
		e.download(9, "movie-remux", 1, 10, "Amelie.2001.held-OLD", "x", "[]")
		e.exec(`UPDATE download SET state = 'imported', imported_at = -1, info_hash = 'ffffffffffffffffffffffffffffffffffffffff' WHERE id = 9`)
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		if got := e.one(`SELECT detail FROM library_action WHERE kind = 'replace' AND download_id = 1`); !strings.HasSuffix(got, store.HistoryDeadLower) {
			t.Errorf("detail %q", got)
		}
		if got := e.one(`SELECT json_extract(replaced, '$[0].dead_lower') FROM history WHERE event = 'deleted'`); got != "1" {
			t.Errorf("history replaced dead_lower %s", got)
		}
		if got := kind(e); got != "grab" {
			t.Errorf("kind %s", got)
		}
		if got := historyKind(e); got != "upgrade" {
			t.Errorf("History's kind %s", got)
		}
	})
	t.Run("failed, after an earlier import", func(t *testing.T) {
		e := importUpEnv(t, "")
		e.download(9, "movie-remux", 1, 10, "Amelie.2001.held-OLD", "x", "[]")
		e.exec(`UPDATE download SET state = 'imported', imported_at = -1, info_hash = 'ffffffffffffffffffffffffffffffffffffffff' WHERE id = 9`)
		e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
		e.exec(`UPDATE download SET state = 'failed' WHERE id = 1`)
		if got := kind(e); got != "grab" {
			t.Errorf("kind %s", got)
		}
		if got := historyKind(e); got != "upgrade" {
			t.Errorf("History's kind %s", got)
		}
	})
	t.Run("on its way, after an earlier import", func(t *testing.T) {
		e := importUpEnv(t, "")
		e.download(9, "movie-remux", 1, 10, "Amelie.2001.held-OLD", "x", "[]")
		e.exec(`UPDATE download SET state = 'imported', imported_at = -1, info_hash = 'ffffffffffffffffffffffffffffffffffffffff' WHERE id = 9`)
		e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
		if got := kind(e); got != "grab" {
			t.Errorf("kind %s", got)
		}
		if got := historyKind(e); got != "upgrade" {
			t.Errorf("History's kind %s", got)
		}
	})
	t.Run("adopted", func(t *testing.T) {
		e, _, _, _ := adoptedFilm(t)
		if got := e.one(`SELECT detail FROM library_action WHERE kind = 'replace' AND download_id = 1`); strings.HasSuffix(got, store.HistoryDeadLower) {
			t.Errorf("detail %q", got)
		}
		if got := kind(e); got != "upgrade" {
			t.Errorf("kind %s", got)
		}
		if got := historyKind(e); got != "grab" {
			t.Errorf("History's kind %s", got)
		}
	})
}

// While the replacing download is still being imported in this library (a pack whose E02 link failed waits for its
// retry), Undo is not offered and refused; once the import has finished it is offered and works.
func TestUndoWaitsForPendingImport(t *testing.T) {
	t.Parallel()
	e := importUpEnv(t, "")
	old := e.heldLink(1, "WEBDL-1080p", "Show.S01E01.1080p.WEB-DL.DDP5.1.H.264-OLD", 201)
	target, _ := os.Readlink(old)
	e.readable(target)
	rel := "Show.S01.2160p.WEB-DL.DDP5.1.H.265-GRP"
	e.release(rel, map[string]float64{"Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2700,
		"Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2650})
	e.seriesDownload(1, 2, 20, hashA, rel, "[201, 202]", nil)
	blocker := filepath.Join(e.shows, "Show (2020)", "Season 01", "Show (2020) - S01E02 - Second Part [WEBDL-2160p]-GRP.mkv")
	if err := os.MkdirAll(filepath.Dir(blocker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.lib.Import(e.ctx, 1, false); err == nil {
		t.Fatal("a partial import reported no error")
	}
	var action int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM library_action WHERE kind = 'replace' AND download_id = 1`).Scan(&action); err != nil {
		t.Fatal(err)
	}
	state := func() string {
		return e.one(`SELECT (SELECT state FROM download WHERE id = 1) || '/' ||
			(SELECT state FROM library_import WHERE download_id = 1) || '/' || (SELECT COUNT(*) FROM router_blocklist)`)
	}
	if offers, _ := e.lib.UndoOffers(e.ctx, []int64{action}); len(offers) != 0 {
		t.Errorf("offered while importing: %v", offers)
	}
	var rr *ReturnRefused
	if _, err := e.lib.UndoUpgrade(e.ctx, action, "ann"); !errors.As(err, &rr) ||
		rr.Sentence != "That download is still being imported; try again when it has finished" {
		t.Fatalf("undo while importing: %v", err)
	}
	if got := state(); got != "importing/partial/0" {
		t.Errorf("state %s", got)
	}
	if _, err := os.Lstat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old path: %v", err)
	}
	// The import finishes: Undo is offered and works.
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Hour)
	if _, err := e.lib.Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != "imported/linked/0" {
		t.Fatalf("after the retry %s", got)
	}
	if offers, _ := e.lib.UndoOffers(e.ctx, []int64{action}); offers[action] == "" {
		t.Errorf("not offered after the import")
	}
	if _, err := e.lib.UndoUpgrade(e.ctx, action, "ann"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(old); got != target {
		t.Errorf("E01 points at %q", got)
	}
}

// The successor is the recorded one in full: the same row holding another download's file (an earlier release
// imported again) is a change; the recorded successor marked dead still owns the slot, and Undo then helps.
func TestUndoSuccessorIdentity(t *testing.T) {
	t.Parallel()
	t.Run("another download in the row", func(t *testing.T) {
		e, _, _, action := adoptedFilm(t)
		e.download(7, "movie-remux", 1, 10, "Amelie.other", "x", "[]")
		e.exec(`UPDATE download SET info_hash = '7777777777777777777777777777777777777777' WHERE id = 7`)
		e.exec(`UPDATE link SET download_id = 7 WHERE download_id = 1`)
		if offers, _ := e.lib.UndoOffers(e.ctx, []int64{action}); len(offers) != 0 {
			t.Errorf("offered: %v", offers)
		}
		var rr *ReturnRefused
		if _, err := e.lib.UndoUpgrade(e.ctx, action, ""); !errors.As(err, &rr) || rr.Sentence != returnChanged {
			t.Errorf("undo: %v", err)
		}
	})
	t.Run("another library's row", func(t *testing.T) {
		e, _, _, action := adoptedFilm(t)
		e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (11, 1, 'movie-web', 0, 0)`)
		e.exec(`UPDATE link_version SET version_id = 11 WHERE link_id = (SELECT id FROM link WHERE download_id = 1)`)
		var rr *ReturnRefused
		if _, err := e.lib.UndoUpgrade(e.ctx, action, ""); !errors.As(err, &rr) || rr.Sentence != returnChanged {
			t.Errorf("undo: %v", err)
		}
	})
	t.Run("dead successor", func(t *testing.T) {
		e, old, target, action := adoptedFilm(t)
		e.exec(`UPDATE link SET state = 'dead' WHERE download_id = 1`)
		if offers, _ := e.lib.UndoOffers(e.ctx, []int64{action}); offers[action] == "" {
			t.Errorf("not offered")
		}
		if _, err := e.lib.UndoUpgrade(e.ctx, action, ""); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.Readlink(old); got != target {
			t.Errorf("old path %q", got)
		}
		if got := e.one(`SELECT state FROM link WHERE download_id = 1`); got != "unlinked" {
			t.Errorf("successor %s", got)
		}
	})
}

// Two files took one earlier file's place (E01 and E02 over an E01-E02 file): a one-file Undo would leave two files
// on one episode, so it is neither offered nor done.
func TestUndoSplitUpgradeRefused(t *testing.T) {
	t.Parallel()
	e := importUpEnv(t, "")
	old := e.heldLink(1, "WEBDL-1080p", "Show.S01E01E02.1080p.WEB-DL.H.264-OLD", 201, 202)
	target, _ := os.Readlink(old)
	e.readable(target)
	e.release(packRelease, map[string]float64{"Show.S01E01.2160p.WEB-DL.H.265-GRP.mkv": 2700,
		"Show.S01E02.2160p.BluRay.REMUX.HEVC-GRP.mkv": 2700})
	e.seriesDownload(1, 2, 20, hashA, packRelease, "[201, 202]", nil)
	e.exec(`UPDATE download SET quality = 'WEBDL-2160p' WHERE id = 1`)
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT COUNT(*) FROM link WHERE download_id = 1 AND state = 'active'`); got != "2" {
		t.Fatalf("links %s", got)
	}
	var action int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM library_action WHERE kind = 'replace' AND download_id = 1`).Scan(&action); err != nil {
		t.Fatal(err)
	}
	if offers, _ := e.lib.UndoOffers(e.ctx, []int64{action}); len(offers) != 0 {
		t.Errorf("offered: %v", offers)
	}
	var rr *ReturnRefused
	if _, err := e.lib.UndoUpgrade(e.ctx, action, ""); !errors.As(err, &rr) ||
		rr.Sentence != "More than one file took the earlier file's place; nothing was undone" {
		t.Fatalf("undo: %v", err)
	}
	if _, err := os.Lstat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old path: %v", err)
	}
}

// A failed or cancelled Undo leaves nothing behind: no trash copy without a row, no restored symlink, no row.
func TestUndoFailureLeavesNothing(t *testing.T) {
	t.Parallel()
	state := func(e *env) string {
		trash, _ := filepath.Glob(filepath.Join(e.trash, "*", "*", "*", "*"))
		return fmt.Sprint(len(trash), " ", e.one(`SELECT (SELECT COUNT(*) FROM library_action) || '/' ||
			(SELECT COUNT(*) FROM link_trash) || '/' || (SELECT group_concat(state) FROM link ORDER BY id)`))
	}
	t.Run("the successor's removal fails", func(t *testing.T) {
		e, old, _, action := adoptedFilm(t)
		newPath := e.one(`SELECT path FROM link WHERE download_id = 1`)
		before := state(e)
		e.lib.w.after = func(op, p string) {
			if op == "trash" && p == newPath {
				// The successor's symlink can no longer be removed: a directory now stands at its path.
				_ = os.Remove(p)
				_ = os.Mkdir(p, 0o755)
			}
		}
		if _, err := e.lib.UndoUpgrade(e.ctx, action, ""); err == nil {
			t.Fatal("undone")
		}
		if got := state(e); got != before {
			t.Errorf("left %s, was %s", got, before)
		}
		if _, err := os.Lstat(old); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("old path: %v", err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		e, old, _, action := adoptedFilm(t)
		before := state(e)
		ctx, cancel := context.WithCancel(e.ctx)
		defer cancel()
		e.lib.w.after = func(op, p string) {
			if op == "symlink" && p == old {
				cancel()
			}
		}
		if _, err := e.lib.UndoUpgrade(ctx, action, ""); err == nil {
			t.Fatal("undone")
		}
		if _, err := os.Lstat(old); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("old path left: %v", err)
		}
		if got := state(e); got != before {
			t.Errorf("left %s, was %s", got, before)
		}
	})
}

// An earlier file of size zero on the mount is not offered.
func TestUndoOfferEmptyFile(t *testing.T) {
	t.Parallel()
	e, _, target, action := adoptedFilm(t)
	if err := os.Truncate(target, 0); err != nil {
		t.Fatal(err)
	}
	if offers, _ := e.lib.UndoOffers(e.ctx, []int64{action}); len(offers) != 0 {
		t.Errorf("offered: %v", offers)
	}
}

// A library that does not write offers no Undo, and the POST says why in the gate's words; a successor whose
// symlink is gone is not offered either.
func TestUndoAsksTheGate(t *testing.T) {
	t.Parallel()
	t.Run("library off", func(t *testing.T) {
		e, _, _, action := adoptedFilm(t)
		e.lib.gate.enabled = false
		if offers, _ := e.lib.UndoOffers(e.ctx, []int64{action}); len(offers) != 0 {
			t.Errorf("offered: %v", offers)
		}
		var rr *ReturnRefused
		if _, err := e.lib.UndoUpgrade(e.ctx, action, ""); !errors.As(err, &rr) ||
			rr.Sentence != "The library does not write now: library.enabled is false" {
			t.Errorf("undo: %v", err)
		}
	})
	t.Run("successor symlink gone", func(t *testing.T) {
		e, _, _, action := adoptedFilm(t)
		if err := os.Remove(e.one(`SELECT path FROM link WHERE download_id = 1`)); err != nil {
			t.Fatal(err)
		}
		e.exec(`UPDATE link SET state = 'dead' WHERE download_id = 1`)
		if offers, _ := e.lib.UndoOffers(e.ctx, []int64{action}); len(offers) != 0 {
			t.Errorf("offered: %v", offers)
		}
	})
}

// A link a rename moved during a return goes to the trash in the return's words: never a "Removed" row.
func TestMoveStrayInReturnWords(t *testing.T) {
	t.Parallel()
	e, _, _, _ := adoptedFilm(t)
	p := filepath.Join(e.movies, "Amelie The Movie (2001)", "stray.mkv")
	tgt := filepath.Join(e.mount, "__all__", "Held", "held.mkv")
	if err := os.Symlink(tgt, p); err != nil {
		t.Fatal(err)
	}
	e.lib.moveStray(e.ctx, 0, e.movies, trashed{version: "movie-remux", path: p, target: tgt, reason: "upgrade", linkID: 1},
		undoPrefix+" by ann: ")
	if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stray stayed: %v", err)
	}
	if got := e.one(`SELECT COUNT(*) FROM history WHERE event = 'removed'`); got != "0" {
		t.Errorf("removed rows %s", got)
	}
	if got := e.one(`SELECT detail FROM library_action ORDER BY id DESC LIMIT 1`); !strings.HasPrefix(got, undoPrefix) {
		t.Errorf("detail %q", got)
	}
}

// A library share that stops answering never holds History: the successor's symlink is read bounded, and a row whose
// read does not answer offers no Undo.
func TestUndoOffersBoundedRead(t *testing.T) {
	e, _, _, action := adoptedFilm(t)
	newPath := e.one(`SELECT path FROM link WHERE download_id = 1`)
	probetest.Mounts(t, e.movies, e.shows, e.trash, filepath.Dir(newPath))
	hangRootRead(t, &rootLstat, func(orig func(string) (os.FileInfo, error), block func(string)) func(string) (os.FileInfo, error) {
		return func(p string) (os.FileInfo, error) { block(p); return orig(p) }
	}, newPath)
	offers := within(t, 3*time.Second, func() map[int64]string {
		o, err := e.lib.UndoOffers(e.ctx, []int64{action})
		if err != nil {
			t.Error(err)
		}
		return o
	})
	if len(offers) != 0 {
		t.Errorf("offered: %v", offers)
	}
}

// A library whose root moved since offers no Undo for paths under the former root, and the POST gives the gate's
// reason.
func TestUndoAsksTheGateAboutPaths(t *testing.T) {
	t.Parallel()
	e, _, _, action := adoptedFilm(t)
	e.lib.gate.versions["movie-remux"] = gateVersion{known: true, roots: []string{e.outside}}
	if offers, _ := e.lib.UndoOffers(e.ctx, []int64{action}); len(offers) != 0 {
		t.Errorf("offered: %v", offers)
	}
	var rr *ReturnRefused
	if _, err := e.lib.UndoUpgrade(e.ctx, action, ""); !errors.As(err, &rr) ||
		!strings.HasPrefix(rr.Sentence, "The library does not write now: ") {
		t.Errorf("undo: %v", err)
	}
}

// A library the title was removed from ("Remove from library" with its files kept) offers no Undo, and an Undo
// posted anyway is refused: nothing changes.
func TestUndoRemovedVersion(t *testing.T) {
	t.Parallel()
	e, old, _, action := adoptedFilm(t)
	newPath := e.one(`SELECT path FROM link WHERE download_id = 1`)
	newTarget, _ := os.Readlink(newPath)
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (10, 'ui', 0)`)
	if offers, err := e.lib.UndoOffers(e.ctx, []int64{action}); err != nil || len(offers) != 0 {
		t.Errorf("offers %v %v", offers, err)
	}
	var rr *ReturnRefused
	if _, err := e.lib.UndoUpgrade(e.ctx, action, "ann"); !errors.As(err, &rr) {
		t.Fatalf("undo: %v", err)
	}
	if got, _ := os.Readlink(newPath); got != newTarget {
		t.Errorf("the kept file points at %q", got)
	}
	if _, err := os.Lstat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the earlier file came back: %v", err)
	}
	if got := e.one(`SELECT COUNT(*) FROM router_blocklist`); got != "0" {
		t.Errorf("blocklist rows %s", got)
	}
}

// Undo of a replacement a playing-break incident made settles that incident in the same transaction, quietly: the
// row says the earlier copy is back, no outcome is sent, History gains only the "Upgrade undone" row, and the sweep
// past 30 days does not record it kept aside.
func TestUndoSettlesThePlayingIncident(t *testing.T) {
	t.Parallel()
	e, old, target, action := adoptedFilm(t)
	e.pol.Router = &policy.Router{Enabled: true}
	var outcomes []string
	e.lib.OnPlayingOutcome(func(o outcome.Outcome) { outcomes = append(outcomes, o.Kind) })
	// Broken 31 days ago: the next sweep would settle it aside unread.
	at := store.Millis(e.lib.now().Add(-31 * 24 * time.Hour))
	e.exec(`INSERT INTO playing_break (id, incident, watched, link_id, title_id, version_key, path, old_target, broken_at,
		searched, fixed_by, new_release, reported, updated_at) VALUES (50, 50, 1, 1, 1, 'movie-remux', ?, ?, ?, 1, 'replaced',
		?, 'replaced', ?)`, old, target, at, movieRelease, at)
	// Another incident on the same copy that another release replaced is not the one undone: it stays open.
	e.exec(`INSERT INTO playing_break (id, incident, watched, link_id, title_id, version_key, path, old_target, broken_at,
		searched, fixed_by, new_release, reported, updated_at) VALUES (51, 51, 1, 1, 1, 'movie-remux', ?, ?, ?, 1, 'replaced',
		'Amelie.2001.1080p.WEB-OTHER', 'replaced', ?)`, old, target, at+1, at)
	// Another broken file of the incident whose search is not queued yet (a pack's other episode).
	e.exec(`INSERT INTO playing_break (id, incident, watched, link_id, title_id, version_key, path, old_target, broken_at,
		updated_at) VALUES (52, 50, 0, 1, 1, 'movie-remux', ?, ?, ?, ?)`, old+".other", target+".other", at, at)
	const others = `SELECT COUNT(*) FROM history WHERE event NOT IN ('undone', 'blocked')`
	history := e.one(others)
	if _, err := e.lib.UndoUpgrade(e.ctx, action, "ann"); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT settled || '|' || reported FROM playing_break WHERE id = 50`); got != "restored|replaced" {
		t.Errorf("incident %s", got)
	}
	if got := e.one(`SELECT settled FROM playing_break WHERE id = 51`); got != "" {
		t.Errorf("the other replacement's incident %q", got)
	}
	// The settle pass still queues the other broken file's search, and reports nothing for the settled incident.
	if err := e.lib.SettlePlayingRuns(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT searched FROM playing_break WHERE id = 52`); got != "1" {
		t.Errorf("the other file's search was not queued: searched %s", got)
	}
	if got := e.one(`SELECT reported FROM playing_break WHERE id = 50`); got != "replaced" {
		t.Errorf("reported %s", got)
	}
	if got := e.one(others); got != history {
		t.Errorf("history rows %s, before %s", got, history)
	}
	if err := e.lib.ComeBack(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT settled FROM playing_break WHERE id = 50`); got != "restored" {
		t.Errorf("after the sweep %s", got)
	}
	if len(outcomes) != 0 {
		t.Errorf("outcomes %v", outcomes)
	}
}

// The Undo's block starts no search: the router's pass over new blocklist rows does not take an Undo's source, and the
// row keeps no researched time (the Blocklist shows no "handled" line).
func TestUndoStartsNoSearch(t *testing.T) {
	t.Parallel()
	e, _, _, action := adoptedFilm(t)
	e.pol.Router = &policy.Router{Enabled: true}
	if _, err := e.lib.UndoUpgrade(e.ctx, action, "ann"); err != nil {
		t.Fatal(err)
	}
	r, err := router.New(router.Config{Store: e.st, Policy: e.pol, Queue: jobs.NewQueue(e.st)})
	if err != nil {
		t.Fatal(err)
	}
	sum, err := r.Poll(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Researched != 0 {
		t.Errorf("researched %d", sum.Researched)
	}
	if got := e.one(`SELECT COUNT(*) FROM job WHERE kind = ?`, router.JobResearch); got != "0" {
		t.Errorf("searches queued %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM router_blocklist WHERE researched_at IS NOT NULL`); got != "0" {
		t.Errorf("researched rows %s", got)
	}
}

// An older handled block of the same release (an earlier refusal, researched) does not lend the Undo's row its
// researched time: the row starts clean, and no search starts.
func TestUndoBlockStartsClean(t *testing.T) {
	t.Parallel()
	e, _, _, action := adoptedFilm(t)
	e.exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, release_title, reason, source, created_at,
		researched_at) VALUES ('movie-remux', ?, 1, ?, 'an earlier refusal', 'library', 1, 2)`, hashA, movieRelease)
	if _, err := e.lib.UndoUpgrade(e.ctx, action, "ann"); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT source || '/' || (researched_at IS NULL) FROM router_blocklist WHERE version_key = 'movie-remux'
		AND info_hash = ?`, hashA); got != "undo:ann/1" {
		t.Errorf("blocklist %s", got)
	}
}
