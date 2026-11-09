package arrview

import (
	"strconv"
	"strings"
	"testing"

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
