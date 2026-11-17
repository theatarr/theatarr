package removal

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// Invented hashes.
const (
	hA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hC = "cccccccccccccccccccccccccccccccccccccccc"
	hD = "dddddddddddddddddddddddddddddddddddddddd"
	hE = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	hF = "ffffffffffffffffffffffffffffffffffffffff"
	hH = "2222222222222222222222222222222222222222"
	hX = "3333333333333333333333333333333333333333"
)

var testNow = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

type refused string

func (r refused) Error() string { return string(r) }
func (refused) Refused() bool   { return true }

// fakeUnlinker marks a link unlinked as the library does; refuse and fail name links it refuses or fails.
type fakeUnlinker struct {
	st      *store.Store
	calls   []int64
	refuse  map[int64]bool
	fail    map[int64]bool
	removed []int64
	// before, when set, runs at the start of each call.
	before func(id int64)
}

func (f *fakeUnlinker) UnlinkRemoved(ctx context.Context, id int64, _ string) (string, error) {
	f.calls = append(f.calls, id)
	if f.before != nil {
		f.before(id)
	}
	switch {
	case f.refuse[id]:
		return "", refused("library: write refused (shadow): the library is in shadow")
	case f.fail[id]:
		return "", syscall.ENOSPC
	}
	if _, err := f.st.Writer().ExecContext(ctx, `UPDATE link SET state = 'unlinked' WHERE id = ?`, id); err != nil {
		return "", err
	}
	f.removed = append(f.removed, id)
	return "/trash/" + filepath.Base(strings.Repeat("x", int(id%7)+1)), nil
}

type fixture struct {
	t       *testing.T
	st      *store.Store
	u       *fakeUnlinker
	started []Cleanup
	removed []int64
}

// newFixture: Gone (title 1) has its 4K WEB copy (version 1, movie-web) and its Remux copy (version 2,
// movie-remux); Other (title 2) has a 4K WEB copy (version 3). Gone's links:
//   - 10 a.mkv (hA), 11 b.mkv dead (hB): Gone's web copy only;
//   - 12 c.mkv (hC): shared by Gone's two copies;
//   - 13 d.mkv (hD): the gate refuses it; 14 e.mkv (hE): its move fails; 15 f.mkv (hF): a hardlink.
//
// Other links hA too (a torrent shared with another title). Gone's web copy has a download in flight (hH, a fresh
// add) and a failed one (hX).
func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, st: st, u: &fakeUnlinker{st: st, refuse: map[int64]bool{13: true}, fail: map[int64]bool{14: true}}}
	f.exec(`INSERT INTO title (id, kind, name, tmdb_id, monitored, created_at, updated_at) VALUES
		(1, 'movie', 'Gone', 501, 1, 0, 0), (2, 'movie', 'Other', 502, 1, 0, 0)`)
	f.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
		(1, 1, 'movie-web', 1, 'filled', 0, 0), (2, 1, 'movie-remux', 1, 'filled', 0, 0),
		(3, 2, 'movie-web', 1, 'filled', 0, 0)`)
	link := func(id int64, path, kind, state, hash string, title int64, versions ...int64) {
		f.exec(`INSERT INTO link (id, path, target, kind, title_id, state, file_metadata, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 0, 0)`, id, path, "/mnt/debrid/__all__/R/"+filepath.Base(path), kind, title,
			state, `{"InfoHash":"`+hash+`"}`)
		for _, v := range versions {
			f.exec(`INSERT INTO link_version (link_id, version_id) VALUES (?, ?)`, id, v)
		}
	}
	link(10, "/lib/web/Gone/a.mkv", "symlink", "active", hA, 1, 1)
	link(11, "/lib/web/Gone/b.mkv", "symlink", "dead", hB, 1, 1)
	link(12, "/lib/web/Gone/c.mkv", "symlink", "active", hC, 1, 1, 2)
	link(13, "/lib/web/Gone/d.mkv", "symlink", "active", hD, 1, 1)
	link(14, "/lib/web/Gone/e.mkv", "symlink", "active", hE, 1, 1)
	link(15, "/lib/web/Gone/f.mkv", "hardlink", "active", hF, 1, 1)
	link(20, "/lib/web/Other/a.mkv", "symlink", "active", hA, 2, 3)
	f.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at) VALUES (31, 1, 1, 'movie-web', ?, 'Gone.WEB', 'idx', 'downloading', 0, 0),
		(32, 1, 1, 'movie-web', ?, 'Gone.Bad', 'idx', 'failed', 0, 0)`, hH, hX)
	f.exec(`INSERT INTO search_request (title_id, version_key, reason, season, created_at, next_at, tries) VALUES
		(1, 'movie-web', 'missing', -1, 0, 0, 0), (1, 'movie-remux', 'missing', -1, 0, 0, 0)`)
	return f
}

func (f *fixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.st.Writer().Exec(q, args...); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
}

func (f *fixture) str(q string, args ...any) string {
	f.t.Helper()
	var s sql.NullString
	if err := f.st.Reader().QueryRow(q, args...).Scan(&s); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
	return s.String
}

func (f *fixture) deps() Deps {
	return Deps{Store: f.st, Unlinker: f.u, Now: func() time.Time { return testNow },
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		AfterRemove:  func(_ context.Context, id int64) { f.removed = append(f.removed, id) },
		StartCleanup: func(c Cleanup) { f.started = append(f.started, c) }}
}

func webSpec(unlink bool) Spec {
	return Spec{TitleID: 1, Name: "Gone", Keys: []string{"movie-web"}, View: "ui", Unlink: unlink,
		Action: journal.ActRemoveVersion, By: "mgr", Reason: "copy removed by mgr"}
}

func paths(ls []Left) []string {
	var out []string
	for _, l := range ls {
		out = append(out, filepath.Base(l.Path))
	}
	return out
}

// Removing one library with its files: the order and every guard.
func TestRunWithFiles(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	res, err := Run(context.Background(), f.deps(), webSpec(true))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Removed, []string{"movie-web"}) || res.DownloadsEnded != 1 || !slices.Equal(f.removed, []int64{1}) {
		t.Fatalf("removed %+v, requests told %v", res, f.removed)
	}
	// The copy's own symlinks go, the dead one too; the link the Remux copy still uses, the hardlink and the
	// refused one stay; the failed one is reported.
	var unlinked []string
	for _, u := range res.Unlinked {
		unlinked = append(unlinked, filepath.Base(u.Path))
	}
	if !slices.Equal(unlinked, []string{"a.mkv", "b.mkv"}) || !slices.Equal(paths(res.Shared), []string{"c.mkv"}) ||
		!slices.Equal(paths(res.Kept), []string{"d.mkv", "f.mkv"}) || !slices.Equal(paths(res.Failed), []string{"e.mkv"}) {
		t.Errorf("unlinked %v shared %v kept %v failed %v", unlinked, paths(res.Shared), paths(res.Kept), paths(res.Failed))
	}
	if slices.Contains(f.u.calls, 12) || slices.Contains(f.u.calls, 15) {
		t.Errorf("asked to unlink the shared link or the hardlink: %v", f.u.calls)
	}
	if got := f.str(`SELECT state FROM link WHERE id = 12`); got != "active" {
		t.Errorf("the link the other library uses is %s", got)
	}
	// Candidates: hashes of the copy's links and downloads not failed, less those a link that stays names. hA stays a candidate although Other links it: the clean-up checks usage live.
	if len(f.started) != 1 || res.Cleanup == nil {
		t.Fatalf("clean-ups started %+v", f.started)
	}
	got := slices.Sorted(maps.Keys(f.started[0].Hashes))
	if want := []string{hH, hA, hB}; !slices.Equal(got, want) {
		t.Errorf("candidates %v, want %v", got, want)
	}
	// The hide and its search requests.
	if f.str(`SELECT COUNT(*) FROM arrview_removed_version WHERE version_id = 1 AND view = 'ui'`) != "1" ||
		f.str(`SELECT COUNT(*) FROM arrview_removed_version WHERE version_id = 2`) != "0" {
		t.Error("hidden copies")
	}
	if got := f.str(`SELECT group_concat(version_key) FROM search_request WHERE title_id = 1`); got != "movie-remux" {
		t.Errorf("search requests left %q", got)
	}
	// The Removed action says what became of the files; each unlinked link has its own entry.
	rm := journal.ParseRemovalDetail(f.str(`SELECT detail FROM journal_action WHERE action = ?`, journal.ActRemoveVersion))
	// Left: the shared link, the refused one, the failed one and the hardlink.
	if rm.Files != journal.RemovalFilesPartial || rm.Left != 4 {
		t.Errorf("removal outcome %+v", rm)
	}
	if got := f.str(`SELECT COUNT(*) FROM journal_action WHERE action = ?`, journal.ActUnlinkFile); got != "2" {
		t.Errorf("unlink entries %s", got)
	}
	// The clean-up's outcome is written into that action, keeping the files' outcome.
	RecordCleanup(context.Background(), f.st, nil, f.started[0].Actions["movie-web"], "deleted on RD")
	rm2 := journal.ParseRemovalDetail(f.str(`SELECT detail FROM journal_action WHERE action = ?`, journal.ActRemoveVersion))
	if rm2.Cleanup != "deleted on RD" || rm2.Files != rm.Files || rm2.Left != rm.Left {
		t.Errorf("after the clean-up %+v", rm2)
	}

	// The kept-files retry ("Delete them"): the leftover links go now, the earlier action gets the new outcome, and
	// their torrents are cleaned up too.
	f.u.refuse, f.u.fail = nil, nil
	f.started = nil
	res, err = Run(context.Background(), f.deps(), Spec{TitleID: 1, Name: "Gone", Leftover: []string{"movie-web"},
		View: "ui", Unlink: true, Action: journal.ActRemoveVersion, By: "mgr", Reason: "kept files deleted"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 0 || !slices.Equal(paths(res.Shared), []string{"c.mkv"}) || len(res.Unlinked) != 2 {
		t.Errorf("retry %+v", res)
	}
	if len(f.started) != 1 || !slices.Equal(slices.Sorted(maps.Keys(f.started[0].Hashes)), []string{hD, hE}) ||
		len(f.started[0].Actions) != 1 {
		t.Errorf("retry clean-up %+v", f.started)
	}
	if rm := journal.ParseRemovalDetail(f.str(`SELECT detail FROM journal_action WHERE action = ?`,
		journal.ActRemoveVersion)); rm.Files != journal.RemovalFilesPartial || rm.Left != 2 {
		t.Errorf("retry outcome %+v", rm)
	}
}

// Without the files: links and torrents stay; the action says the files were kept, no clean-up runs.
// The hide, the downloads, search requests and queued copies are handled the same.
func TestRunWithoutFiles(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	res, err := Run(context.Background(), f.deps(), webSpec(false))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.u.calls) != 0 || len(res.Unlinked) != 0 || res.Cleanup != nil || len(f.started) != 0 {
		t.Errorf("without the files: %+v, unlinks %v, clean-ups %v", res, f.u.calls, f.started)
	}
	if rm := journal.ParseRemovalDetail(f.str(`SELECT detail FROM journal_action`)); rm.Files != journal.RemovalFilesKept ||
		rm.Cleanup != "" {
		t.Errorf("outcome %+v", rm)
	}
	if res.DownloadsEnded != 1 || f.str(`SELECT COUNT(*) FROM search_request WHERE version_key = 'movie-web'`) != "0" {
		t.Error("the removal's records")
	}
}

// Without a clean-up starter (no torrent manager) the Removed action says the torrents were not cleaned up; with
// every candidate's link kept, it says they were kept.
func TestRunCleanupWords(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d := f.deps()
	d.StartCleanup = nil
	if _, err := Run(context.Background(), d, webSpec(true)); err != nil {
		t.Fatal(err)
	}
	if rm := journal.ParseRemovalDetail(f.str(`SELECT detail FROM journal_action WHERE action = ?`,
		journal.ActRemoveVersion)); rm.Cleanup != "torrents not cleaned up: the torrent manager is not running; torrents of the files that stay kept" {
		t.Errorf("outcome %+v", rm)
	}
	g := newFixture(t)
	g.exec(`DELETE FROM link WHERE id IN (10, 11, 14, 15)`)
	g.exec(`DELETE FROM download`)
	if _, err := Run(context.Background(), g.deps(), webSpec(true)); err != nil {
		t.Fatal(err)
	}
	if rm := journal.ParseRemovalDetail(g.str(`SELECT detail FROM journal_action WHERE action = ?`,
		journal.ActRemoveVersion)); len(g.started) != 0 || rm.Cleanup != "torrents of the files that stay kept" {
		t.Errorf("outcome %+v, started %v", rm, g.started)
	}
}

// After a removal with the files the copy's rows stay, so an import-list or collection sync does not add it again
// (catalog missingVersions: TestRadarrListAddsMissingMovies); a manual add restores it, without the old search
// request.
func TestRunThenReAdd(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if _, err := Run(context.Background(), f.deps(), Spec{TitleID: 1, Name: "Gone",
		Keys: []string{"movie-web", "movie-remux"}, View: "ui", Unlink: true, Action: journal.ActRemoveTitle}); err != nil {
		t.Fatal(err)
	}
	if f.str(`SELECT COUNT(*) FROM version WHERE title_id = 1`) != "2" ||
		f.str(`SELECT COUNT(*) FROM arrview_removed_version r JOIN version v ON v.id = r.version_id WHERE v.title_id = 1`) != "2" {
		t.Fatal("the removed copies' rows")
	}
	err := f.st.WriteTx(context.Background(), func(tx *sql.Tx) error {
		_, err := catalog.Add(context.Background(), tx, catalog.Input{Kind: policy.KindMovie, TmdbID: 501,
			Versions: func(bool) ([]string, error) { return []string{"movie-web"}, nil }, Monitored: true, Source: "test"},
			testNow, slog.New(slog.NewTextHandler(io.Discard, nil)))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.str(`SELECT COUNT(*) FROM arrview_removed_version WHERE version_id = 1`) != "0" ||
		f.str(`SELECT COUNT(*) FROM arrview_removed_version WHERE version_id = 2`) != "1" ||
		f.str(`SELECT COUNT(*) FROM search_request WHERE title_id = 1`) != "0" {
		t.Error("the add restores the copy it names, with no old search request")
	}
}

// A failure before the transaction commits changes nothing.
func TestRunFailsClosed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	s := webSpec(true)
	s.Extra = func(context.Context, *sql.Tx) error { return errors.New("boom") }
	if _, err := Run(context.Background(), f.deps(), s); err == nil {
		t.Fatal("no error")
	}
	if f.str(`SELECT COUNT(*) FROM arrview_removed_version`) != "0" || len(f.u.calls) != 0 ||
		f.str(`SELECT state FROM download WHERE id = 31`) != "downloading" || len(f.started) != 0 {
		t.Error("a failed removal changed something")
	}
}

// A library restored (an Add) while the links are taken out uses its links again: the shared check is made again
// right before each unlink, and the link stays, its torrent with it.
func TestRunRechecksBeforeEachUnlink(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.u.before = func(id int64) {
		if id == 10 {
			f.exec(`DELETE FROM arrview_removed_version WHERE version_id = 1`)
		}
	}
	res, err := Run(context.Background(), f.deps(), webSpec(true))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(f.u.calls, 11) || !slices.Contains(paths(res.Shared), "b.mkv") {
		t.Errorf("unlinked %v, shared %v", f.u.calls, paths(res.Shared))
	}
	if len(f.started) == 1 {
		if _, ok := f.started[0].Hashes[hB]; ok {
			t.Errorf("the restored library's kept link's torrent is a candidate: %v", f.started[0].Hashes)
		}
	}
}

// A link that stays without a hash keeps every candidate of its library; one that stays keeps the hash of every
// link sharing its target.
func TestRunKeepsTheTorrentsOfUntiedLinks(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.exec(`UPDATE link SET file_metadata = '{}' WHERE id = 13`) // the refused link has no hash
	if _, err := Run(context.Background(), f.deps(), webSpec(true)); err != nil {
		t.Fatal(err)
	}
	rm := journal.ParseRemovalDetail(f.str(`SELECT detail FROM journal_action WHERE action = ?`, journal.ActRemoveVersion))
	if len(f.started) != 0 || rm.Cleanup != "torrents of the files that stay kept" {
		t.Errorf("clean-ups %+v, outcome %+v", f.started, rm)
	}

	g := newFixture(t)
	g.exec(`UPDATE link SET target = (SELECT target FROM link WHERE id = 13) WHERE id = 10`) // a.mkv and d.mkv: one file
	if _, err := Run(context.Background(), g.deps(), webSpec(true)); err != nil {
		t.Fatal(err)
	}
	if len(g.started) != 1 {
		t.Fatalf("clean-ups %+v", g.started)
	}
	if _, ok := g.started[0].Hashes[hA]; ok {
		t.Errorf("a.mkv shares the refused link's target; its torrent is a candidate: %v", g.started[0].Hashes)
	}
}

// Each removed library carries its own hashes, so each Removed event gets its own torrents' outcome.
func TestRunCleanupPerLibrary(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	const hY = "4444444444444444444444444444444444444444"
	f.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at) VALUES (33, 1, 2, 'movie-remux', ?, 'Gone.Remux', 'idx', 'imported', 0, 0)`, hY)
	if _, err := Run(context.Background(), f.deps(), Spec{TitleID: 1, Name: "Gone",
		Keys: []string{"movie-web", "movie-remux"}, View: "ui", Unlink: true, Action: journal.ActRemoveTitle}); err != nil {
		t.Fatal(err)
	}
	if len(f.started) != 1 || len(f.started[0].Actions) != 2 {
		t.Fatalf("clean-ups %+v", f.started)
	}
	c := f.started[0]
	if !slices.Equal(c.HashesOf("movie-remux"), []string{hY, hC}) || slices.Contains(c.HashesOf("movie-web"), hY) {
		t.Errorf("remux %v, web %v", c.HashesOf("movie-remux"), c.HashesOf("movie-web"))
	}
}

// Kept files are counted by path; a library with no file says nothing about files; the torrents its kept files keep are named with the clean-up's outcome.
func TestRunCountsFilesByPath(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	res, err := Run(context.Background(), f.deps(), webSpec(true))
	if err != nil {
		t.Fatal(err)
	}
	rm := journal.ParseRemovalDetail(f.str(`SELECT detail FROM journal_action WHERE action = ?`, journal.ActRemoveVersion))
	if rm.Left != 4 {
		t.Errorf("left %d (c, d, e, f), %+v", rm.Left, rm)
	}
	if res.Cleanup == nil || res.Cleanup.Kept["movie-web"] != "torrents of the files that stay kept" {
		t.Errorf("clean-up %+v", res.Cleanup)
	}

	g := newFixture(t)
	g.exec(`DELETE FROM link_version WHERE version_id = 2`)
	if _, err := Run(context.Background(), g.deps(), Spec{TitleID: 1, Name: "Gone", Keys: []string{"movie-remux"},
		View: "ui", Unlink: true, Action: journal.ActRemoveVersion}); err != nil {
		t.Fatal(err)
	}
	if rm := journal.ParseRemovalDetail(g.str(`SELECT detail FROM journal_action WHERE version_key = 'movie-remux'`)); rm.Files != "" {
		t.Errorf("a library with no file: %+v", rm)
	}
}

// "Delete them" for a library removed before removals were recorded (an older arr-compatible delete): the removal is
// recorded now, and its torrents are cleaned up.
func TestRunLeftoverWithoutAnEarlierRemoval(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (1, 'radarr', 0)`)
	res, err := Run(context.Background(), f.deps(), Spec{TitleID: 1, Name: "Gone", Leftover: []string{"movie-web"},
		View: "ui", Unlink: true, Action: journal.ActRemoveVersion, By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	if f.str(`SELECT COUNT(*) FROM journal_action WHERE action = ?`, journal.ActRemoveVersion) != "1" ||
		res.Cleanup == nil || res.Cleanup.Actions["movie-web"] == 0 {
		t.Errorf("recorded %s, clean-up %+v", f.str(`SELECT COUNT(*) FROM journal_action WHERE action = ?`,
			journal.ActRemoveVersion), res.Cleanup)
	}
}

// With "Delete the files" off, a library that had no file records no file outcome; one with files says they were kept.
func TestRunWithoutFilesOnALibraryWithNoFile(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.exec(`DELETE FROM link_version WHERE version_id = 2`)
	if _, err := Run(context.Background(), f.deps(), Spec{TitleID: 1, Name: "Gone", Keys: []string{"movie-remux", "movie-web"},
		View: "ui", Action: journal.ActRemoveTitle}); err != nil {
		t.Fatal(err)
	}
	if rm := journal.ParseRemovalDetail(f.str(`SELECT detail FROM journal_action WHERE version_key = 'movie-remux'`)); rm.Files != "" {
		t.Errorf("no file: %+v", rm)
	}
	if rm := journal.ParseRemovalDetail(f.str(`SELECT detail FROM journal_action WHERE version_key = 'movie-web'`)); rm.Files != journal.RemovalFilesKept {
		t.Errorf("files: %+v", rm)
	}
}

// A refused link whose hash is stored in base32 still keeps its torrent: hashes are compared normalised.
func TestRunNormalisesKeptLinkHashes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.exec(`UPDATE link SET file_metadata = '{"InfoHash":"3XO53XO53XO53XO53XO53XO53XO53XO5"}' WHERE id = 13`) // hD
	f.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at) VALUES (1, 1, 'movie-web', ?, 'Gone.D', 'idx', 'imported', 0, 0)`, hD)
	if _, err := Run(context.Background(), f.deps(), webSpec(true)); err != nil {
		t.Fatal(err)
	}
	if len(f.started) == 1 {
		if _, ok := f.started[0].Hashes[hD]; ok {
			t.Errorf("the refused link's torrent is a candidate: %v", f.started[0].Hashes)
		}
	}
}

// A library removed with the one it is kept in sync with (the web scope's and an arr view's expansion): the link the
// pair shares goes to the trash once, the partner's Removed action says which library it followed, through the
// files' outcome; a third current library outside the pair keeps the shared link.
func TestRunSyncPartner(t *testing.T) {
	t.Parallel()
	spec := Spec{TitleID: 1, Name: "Gone", Keys: []string{"movie-web", "movie-remux"}, View: "ui", Unlink: true,
		Action: journal.ActRemoveTitle, By: "mgr", Reason: "copy removed by mgr",
		SyncOf: map[string]string{"movie-remux": "movie-web"}}
	f := newFixture(t)
	res, err := Run(context.Background(), f.deps(), spec)
	if err != nil {
		t.Fatal(err)
	}
	var shared int
	for _, u := range res.Unlinked {
		if filepath.Base(u.Path) == "c.mkv" {
			shared++
			if !slices.Equal(u.Versions, []string{"movie-remux", "movie-web"}) && !slices.Equal(u.Versions,
				[]string{"movie-web", "movie-remux"}) {
				t.Errorf("c.mkv unlinked for %v", u.Versions)
			}
		}
	}
	if shared != 1 || len(res.Shared) != 0 || slices.Contains(paths(res.Kept), "c.mkv") {
		t.Errorf("the pair's shared link: unlinked %d times, shared %v kept %v", shared, paths(res.Shared), paths(res.Kept))
	}
	detail := func(f *fixture, key string) journal.Removal {
		return journal.ParseRemovalDetail(f.str(`SELECT detail FROM journal_action WHERE version_key = ? AND action = ?`,
			key, journal.ActRemoveTitle))
	}
	if rm := detail(f, "movie-remux"); rm.Sync != "movie-web" || rm.Files != journal.RemovalFilesDeleted {
		t.Errorf("partner's action %+v", rm)
	}
	if rm := detail(f, "movie-web"); rm.Sync != "" {
		t.Errorf("named library's action %+v", rm)
	}
	// The clean-up's outcome keeps the library it followed.
	RecordCleanup(context.Background(), f.st, nil, f.started[0].Actions["movie-remux"], "deleted on RD")
	if rm := detail(f, "movie-remux"); rm.Sync != "movie-web" || rm.Cleanup != "deleted on RD" {
		t.Errorf("after the clean-up %+v", rm)
	}

	// A third library outside the group, still current, uses c.mkv: it stays for it.
	g := newFixture(t)
	g.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
		(4, 1, 'anime-movie', 1, 'filled', 0, 0)`)
	g.exec(`INSERT INTO link_version (link_id, version_id) VALUES (12, 4)`)
	spec.Action = journal.ActRemoveVersion
	res, err = Run(context.Background(), g.deps(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(paths(res.Shared), []string{"c.mkv"}) || g.str(`SELECT state FROM link WHERE id = 12`) != "active" {
		t.Errorf("third library's link: shared %v", paths(res.Shared))
	}
}

// The detail's sync part reads back, alone and with every other part.
func TestRemovalDetailSync(t *testing.T) {
	t.Parallel()
	for _, rm := range []journal.Removal{{Files: journal.RemovalFilesKept, Sync: "tv-a"},
		{Files: journal.RemovalFilesPartial, Left: 2, Reason: "refused; really", Cleanup: "deleted on RD", Sync: "tv-a"},
		{Files: journal.RemovalFilesDeleted}} {
		if got := journal.ParseRemovalDetail(journal.RemovalDetail(rm)); got != rm {
			t.Errorf("%q read back as %+v", journal.RemovalDetail(rm), got)
		}
	}
}
