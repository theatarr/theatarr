package arrview

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/notify"
	"github.com/theatarr/theatarr/internal/removal"

	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
)

// removeEnv is a dev env whose movie-web version of Alpha has one active link (8) and a download in import (71).
func removeEnv(t *testing.T) *devEnv {
	e := newDevEnv(t)
	e.seedFiles()
	e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at)
		VALUES (8, '/media/movies-web/Alpha (2020)/Alpha (2020) other.mkv',
		'/mnt/debrid/__all__/Alpha.WEB/other.mkv', 1, 'active', 0, 0)`)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (8, 2)`)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at) VALUES (71, 1, 2, 'movie-web', 'aaaa', 'Alpha.2020.2160p.WEB-DL-GRP', 'idx',
		'importing', 0, 0)`)
	e.pol.Library = &policy.Library{Enabled: true, Versions: map[string]*policy.LibraryVersion{
		"movie-web": {Roots: []string{"/media/movies-web"}}}}
	e.lib.gate = library.NewGate(e.pol)
	return e
}

// TestDeleteUnlinksWhatAnImportCommittedMeanwhile: DELETE ?deleteFiles=true used to move the links it
// had enumerated to trash and only then ended the downloads; an import that committed a link in between had its
// download imported already, so EndRemoved skipped it and the removed version kept an active link. Now the downloads
// end with the hide, and the links are enumerated after it (they are only checked before it).
func TestDeleteUnlinksWhatAnImportCommittedMeanwhile(t *testing.T) {
	t.Parallel()
	e := removeEnv(t)
	committed := false
	e.lib.onUnlink = func(id int64) {
		e.exec(`UPDATE link SET state = 'unlinked' WHERE id = ?`, id)
	}
	e.lib.onCheck = func() {
		if !committed {
			// The running import commits its link while the first link is checked, before the hide.
			committed = true
			e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at)
				VALUES (9, '/media/movies-web/Alpha (2020)/Alpha (2020).mkv',
				'/mnt/debrid/__all__/Alpha.2020.2160p.WEB-DL-GRP/a.mkv', 1, 'active', 0, 0)`)
			e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (9, 2)`)
			e.exec(`UPDATE download SET state = 'imported' WHERE id = 71`)
		}
	}
	alpha := strconv.Itoa(IDBase + 1)
	if rec := e.do("DELETE", "/v/movie-web/radarr/api/v3/movie/"+alpha+"?deleteFiles=true", e.key, nil); rec.Code != 200 {
		t.Fatalf("DELETE: %d %s", rec.Code, rec.Body.String())
	}
	if n := e.one(`SELECT COUNT(*) FROM link l JOIN link_version lv ON lv.link_id = l.id WHERE lv.version_id = 2
		AND l.state = 'active'`); n != 0 {
		t.Fatalf("%d active links left on the removed version (unlinked %v)", n, e.lib.unlinks)
	}
}

// TestDeleteEndsDownloadsWithTheHide: the hide and the end of the downloads were two
// transactions; EndRemoved failing after the hide answered 500, a retry 404, and the download could still import. A
// failure now leaves nothing removed.
func TestDeleteEndsDownloadsWithTheHide(t *testing.T) {
	t.Parallel()
	e := removeEnv(t)
	e.exec(`CREATE TRIGGER zz_no_end BEFORE UPDATE OF state ON download WHEN NEW.state = 'failed'
		BEGIN SELECT RAISE(ABORT, 'end refused'); END`)
	alpha := strconv.Itoa(IDBase + 1)
	if rec := e.do("DELETE", "/v/movie-web/radarr/api/v3/movie/"+alpha, e.key, nil); rec.Code != 500 {
		t.Fatalf("DELETE: %d %s", rec.Code, rec.Body.String())
	}
	if n := e.one(`SELECT COUNT(*) FROM arrview_removed_version WHERE version_id = 2`); n != 0 {
		t.Fatal("the version was hidden although its downloads were not ended")
	}
	e.exec(`DROP TRIGGER zz_no_end`)
	if rec := e.do("DELETE", "/v/movie-web/radarr/api/v3/movie/"+alpha, e.key, nil); rec.Code != 200 {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
	}
	var state, reason string
	if err := e.st.Reader().QueryRow(`SELECT state, COALESCE(error, '') FROM download WHERE id = 71`).Scan(&state,
		&reason); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || !strings.Contains(reason, "deleted through") {
		t.Fatalf("download 71 %s %q", state, reason)
	}
}

// A DELETE that removed versions tells the web UI (SetOnRemoved), so the title's requests follow the removal; one that
// failed does not.
func TestDeleteTellsTheRemoval(t *testing.T) {
	t.Parallel()
	e := removeEnv(t)
	var told []int64
	e.svc.SetOnRemoved(func(_ context.Context, id int64) { told = append(told, id) })
	alpha := strconv.Itoa(IDBase + 1)
	if rec := e.do("DELETE", "/v/movie-web/radarr/api/v3/movie/"+alpha, e.key, nil); rec.Code != 200 {
		t.Fatalf("DELETE: %d %s", rec.Code, rec.Body.String())
	}
	if len(told) != 1 || told[0] != 1 {
		t.Errorf("told %v, want title 1", told)
	}
}

// The arr-compatible DELETE runs the shared removal with the client's own flag: deleteFiles omitted keeps the files
// and the torrents (as Radarr and Sonarr default), deleteFiles=true deletes the links and hands the torrents to the
// clean-up, as the web UI's dialog with its box ticked (Seerr sends true). Both record the Removed event.
func TestDeleteKeepsTheClientsFlag(t *testing.T) {
	t.Parallel()
	const h = "abababababababababababababababababababab"
	for _, deleteFiles := range []bool{false, true} {
		e := removeEnv(t)
		hooks := deleteHooks(e)
		e.exec(`UPDATE link SET file_metadata = ? WHERE id = 8`, `{"InfoHash":"`+h+`"}`)
		var started []removal.Cleanup
		e.svc.SetCleanup(func(c removal.Cleanup) { started = append(started, c) })
		path := "/v/movie-web/radarr/api/v3/movie/" + strconv.Itoa(IDBase+1)
		if deleteFiles {
			path += "?deleteFiles=true"
		}
		if rec := e.do("DELETE", path, e.key, nil); rec.Code != 200 {
			t.Fatalf("DELETE %v: %d %s", deleteFiles, rec.Code, rec.Body.String())
		}
		state := e.str(`SELECT state FROM link WHERE id = 8`)
		detail := e.str(`SELECT detail FROM journal_action WHERE action IN ('remove_version', 'remove_title')
			AND version_key = 'movie-web'`)
		rm := journal.ParseRemovalDetail(detail)
		switch {
		case !deleteFiles && (state != "active" || len(e.lib.unlinks) != 0 || len(started) != 0 ||
			rm.Files != journal.RemovalFilesKept):
			t.Errorf("without deleteFiles: link %v, unlinks %v, clean-ups %v, %+v", state, e.lib.unlinks, started, rm)
		case deleteFiles && (state != "unlinked" || len(started) != 1 || !hasKey(started[0].Hashes, h) ||
			rm.Files != journal.RemovalFilesDeleted):
			t.Errorf("with deleteFiles: link %v, clean-ups %+v, %+v", state, started, rm)
		}
		if deleteFiles && (len(e.lib.rmdirs) == 0 || !strings.HasPrefix(e.lib.rmdirs[0], "movie-web /media/movies-web/")) {
			t.Errorf("empty folders not cleared: %v", e.lib.rmdirs)
		}
		hooks.check(t, "deleteFiles="+strconv.FormatBool(deleteFiles), deleteFiles)
	}
	// Files asked for but one kept (not a symlink): the removal commits, the webhook says no files were deleted.
	e := removeEnv(t)
	hooks := deleteHooks(e)
	e.exec(`UPDATE link SET kind = 'hardlink' WHERE id = 8`)
	if rec := e.do("DELETE", "/v/movie-web/radarr/api/v3/movie/"+strconv.Itoa(IDBase+1)+"?deleteFiles=true", e.key,
		nil); rec.Code != 409 {
		t.Fatalf("kept file: %d %s", rec.Code, rec.Body.String())
	}
	hooks.check(t, "kept file", false)
	// A refused pre-check removes nothing and sends nothing.
	e = removeEnv(t)
	hooks = deleteHooks(e)
	e.pol.Library.Versions["movie-web"].Roots = []string{"/elsewhere"}
	e.lib.gate = library.NewGate(e.pol)
	if rec := e.do("DELETE", "/v/movie-web/radarr/api/v3/movie/"+strconv.Itoa(IDBase+1)+"?deleteFiles=true", e.key,
		nil); rec.Code != 409 {
		t.Fatalf("refused: %d %s", rec.Code, rec.Body.String())
	}
	if len(hooks.bodies) != 0 || len(hooks.events) != 0 {
		t.Errorf("refused DELETE sent %v", hooks.bodies)
	}
}

// hookPayloads builds the arr-shaped payloads of each event as the notifier does when it records them, and notes
// whether the removal had committed by then.
type hookPayloads struct {
	*recorder
	e      *devEnv
	bodies []obj
	after  []bool
}

func (h *hookPayloads) Emit(ctx context.Context, ev notify.Event) {
	h.recorder.Emit(ctx, ev)
	ps, err := h.e.svc.Payloads(ctx, ev)
	if err != nil {
		panic(err)
	}
	for _, p := range ps {
		h.bodies = append(h.bodies, p.Body.(obj))
		h.after = append(h.after, h.e.one(`SELECT COUNT(*) FROM arrview_removed_version`) > 0)
	}
}

func deleteHooks(e *devEnv) *hookPayloads {
	h := &hookPayloads{recorder: &recorder{}, e: e}
	e.svc.SetNotifier(h)
	return h
}

// check: one MovieDelete for Alpha, sent after the removal committed, with the given deletedFiles.
func (h *hookPayloads) check(t *testing.T, name string, deleted bool) {
	t.Helper()
	if len(h.bodies) != 1 {
		t.Fatalf("%s: %d delete webhooks", name, len(h.bodies))
	}
	b := h.bodies[0]
	m, _ := b["movie"].(obj)
	if b["eventType"] != "MovieDelete" || m["title"] != "Alpha" || b["deletedFiles"] != deleted || !h.after[0] {
		t.Errorf("%s: webhook %v (after the removal: %v)", name, b, h.after[0])
	}
}

func hasKey(m map[string][]string, k string) bool {
	_, ok := m[k]
	return ok
}

// syncRemoveEnv is removeEnv with Alpha's two libraries kept in sync; the /v/movie-web view covers movie-web only,
// so movie-remux is a partner outside it. Its movie-remux row holds a minimum availability choice.
func syncRemoveEnv(t *testing.T) *devEnv {
	e := removeEnv(t)
	e.pol.Library.SyncGroups = [][]string{{"movie-remux", "movie-web"}}
	e.exec(`INSERT INTO arrview_title (view, title_id, minimum_availability, added_at, updated_at)
		VALUES ('v/movie-remux', 1, 'released', 0, 0)`)
	return e
}

// A compatible DELETE removes the libraries kept in sync with the view's too, also one outside the view: with
// deleteFiles the partner's links are checked first (a refusal removes nothing) and go with the view's; without, every
// file stays. The partner's Removed action names the library it followed, and every view's record of the title goes.
func TestDeleteRemovesTheSyncPartners(t *testing.T) {
	t.Parallel()
	alpha := "/v/movie-web/radarr/api/v3/movie/" + strconv.Itoa(IDBase+1)
	// The gate refuses movie-remux's link (no root listed for it): nothing is removed.
	e := syncRemoveEnv(t)
	if rec := e.do("DELETE", alpha+"?deleteFiles=true", e.key, nil); rec.Code != 409 {
		t.Fatalf("refused partner link: %d %s", rec.Code, rec.Body.String())
	}
	if n := e.one(`SELECT COUNT(*) FROM arrview_removed_version`); n != 0 || len(e.lib.unlinks) != 0 {
		t.Fatalf("a refused DELETE removed %d versions, unlinked %v", n, e.lib.unlinks)
	}
	e.pol.Library.Versions["movie-remux"] = &policy.LibraryVersion{Roots: []string{"/media/movies"}}
	e.lib.gate = library.NewGate(e.pol)
	if rec := e.do("DELETE", alpha+"?deleteFiles=true", e.key, nil); rec.Code != 200 {
		t.Fatalf("DELETE: %d %s", rec.Code, rec.Body.String())
	}
	if n := e.one(`SELECT COUNT(*) FROM arrview_removed_version WHERE version_id IN (1, 2)`); n != 2 {
		t.Errorf("removed %d of the pair", n)
	}
	if e.str(`SELECT state FROM link WHERE id = 1`) != "unlinked" || e.str(`SELECT state FROM link WHERE id = 8`) != "unlinked" {
		t.Errorf("links: unlinked %v", e.lib.unlinks)
	}
	if rm := journal.ParseRemovalDetail(e.str(`SELECT detail FROM journal_action WHERE action = 'remove_title'
		AND version_key = 'movie-remux'`)); rm.Sync != "movie-web" {
		t.Errorf("partner's action %+v", rm)
	}
	// The title's last copies went: every view's record of it goes, the partner's choice with it.
	if n := e.one(`SELECT COUNT(*) FROM arrview_title WHERE title_id = 1`); n != 0 {
		t.Errorf("%d arr view records left", n)
	}

	// Without deleteFiles every file stays, the partner's too.
	e = syncRemoveEnv(t)
	if rec := e.do("DELETE", alpha, e.key, nil); rec.Code != 200 {
		t.Fatalf("DELETE: %d %s", rec.Code, rec.Body.String())
	}
	if n := e.one(`SELECT COUNT(*) FROM arrview_removed_version WHERE version_id IN (1, 2)`); n != 2 ||
		len(e.lib.unlinks) != 0 || e.str(`SELECT state FROM link WHERE id = 1`) != "active" {
		t.Errorf("without deleteFiles: removed %d, unlinked %v", n, e.lib.unlinks)
	}
}

// The pre-check covers every link the removal takes, dead ones too: a partner outside the view whose dead link its
// gate refuses answers 409 before anything is hidden, unlinked or handed to the clean-up; the same with Pause on.
func TestDeleteChecksThePartnersDeadLinks(t *testing.T) {
	t.Parallel()
	for _, paused := range []bool{false, true} {
		e := syncRemoveEnv(t)
		e.pol.Router.Enabled = !paused
		e.exec(`UPDATE link SET state = 'dead' WHERE id = 1`)
		var started []removal.Cleanup
		e.svc.SetCleanup(func(c removal.Cleanup) { started = append(started, c) })
		rec := e.do("DELETE", "/v/movie-web/radarr/api/v3/movie/"+strconv.Itoa(IDBase+1)+"?deleteFiles=true", e.key, nil)
		if rec.Code != 409 {
			t.Fatalf("paused %v: %d %s", paused, rec.Code, rec.Body.String())
		}
		if n := e.one(`SELECT COUNT(*) FROM arrview_removed_version`); n != 0 || len(e.lib.unlinks) != 0 || len(started) != 0 ||
			e.str(`SELECT state FROM link WHERE id = 8`) != "active" {
			t.Errorf("paused %v: removed %d, unlinked %v, clean-ups %v", paused, n, e.lib.unlinks, started)
		}
	}
}

// A DELETE whose libraries in sync take the title's last copies drops every view's record of it, so a later re-add
// through the partner's own view starts fresh (no old root folder or added date); a removal that leaves a copy keeps
// the other views' records.
func TestDeleteOfTheLastCopiesInSyncStartsFresh(t *testing.T) {
	t.Parallel()
	e := syncRemoveEnv(t)
	e.exec(`UPDATE arrview_title SET root_folder_path = '/old/movies', added_at = 7 WHERE view = 'v/movie-remux'`)
	e.exec(`INSERT INTO arrview_title (view, title_id, minimum_availability, root_folder_path, added_at, updated_at)
		VALUES ('radarr', 1, '', '/old/combined', 7, 7)`)
	e.call("DELETE", "/v/movie-web/radarr/api/v3/movie/"+strconv.Itoa(IDBase+1), nil, 200, nil)
	if n := e.one(`SELECT COUNT(*) FROM arrview_title WHERE title_id = 1`); n != 0 {
		t.Fatalf("%d arr view records left", n)
	}
	e.tick()
	var out map[string]any
	e.call("POST", "/v/movie-remux/radarr/api/v3/movie", map[string]any{"title": "Alpha", "tmdbId": 101,
		"qualityProfileId": e.profileID("v/movie-remux", "movie-remux"), "monitored": true}, 201, &out)
	if want := e.now.Format("2006-01-02T15:04:05Z"); out["added"] != want || out["rootFolderPath"] == "/old/movies" {
		t.Errorf("re-added: added %v (want %s), root %v", out["added"], want, out["rootFolderPath"])
	}

	// Without partners taking the last copy (no group), the other views keep their records.
	g := removeEnv(t)
	g.exec(`INSERT INTO arrview_title (view, title_id, minimum_availability, root_folder_path, added_at, updated_at)
		VALUES ('v/movie-remux', 1, '', '/old/movies', 7, 7)`)
	g.call("DELETE", "/v/movie-web/radarr/api/v3/movie/"+strconv.Itoa(IDBase+1), nil, 200, nil)
	if n := g.one(`SELECT COUNT(*) FROM arrview_title WHERE view = 'v/movie-remux' AND title_id = 1`); n != 1 {
		t.Errorf("the remaining library's record went (%d)", n)
	}
}
