package removal

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
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
	hG = "1111111111111111111111111111111111111111"
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
}

func (f *fakeUnlinker) UnlinkRemoved(ctx context.Context, id int64, _ string) (string, error) {
	f.calls = append(f.calls, id)
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
// Other links hA too (a torrent shared with another title). Gone's web copy has an arr file at a.mkv (hA) and one at
// an arr path no link has (hG), a download in flight (hH, a fresh add) and a failed one (hX).
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
	f.exec(`INSERT INTO version_file (version_id, title_id, instance, arr_file_id, path, download_id) VALUES
		(1, 1, 'radarr', 1, '/lib/web/Gone/a.mkv', ?), (1, 1, 'radarr', 2, '/arr/Gone.mkv', ?)`,
		strings.ToUpper(hA), hG)
	f.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at) VALUES (31, 1, 1, 'movie-web', ?, 'Gone.WEB', 'idx', 'downloading', 0, 0),
		(32, 1, 1, 'movie-web', ?, 'Gone.Bad', 'idx', 'failed', 0, 0)`, hH, hX)
	f.exec(`INSERT INTO search_request (title_id, version_key, reason, season, created_at, next_at, tries) VALUES
		(1, 'movie-web', 'missing', -1, 0, 0, 0), (1, 'movie-remux', 'missing', -1, 0, 0, 0)`)
	f.exec(`INSERT INTO router_offer (version_key, title_id, info_hash, from_version, outcome, first_at, next_at,
		updated_at) VALUES ('movie-web', 1, ?, 'movie-remux', 'pending', 0, 0, 0),
		('movie-remux', 1, ?, 'movie-web', 'pending', 0, 0, 0)`, hC, hA)
	f.exec(`INSERT INTO provider_copy (info_hash, provider, state, created_at, updated_at) VALUES
		(?, 'tb', 'queued', 0, 0), (?, 'ad', 'adding', 0, 0), (?, 'tb', 'queued', 0, 0)`, hH, hH, hA)
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
	// Candidates: hashes of the copy's links, arr files and downloads not failed, less those a link that stays (or
	// an arr file left) names. hA stays a candidate although Other links it: the clean-up checks usage live.
	if len(f.started) != 1 || res.Cleanup == nil {
		t.Fatalf("clean-ups started %+v", f.started)
	}
	got := slices.Clone(f.started[0].Hashes)
	slices.Sort(got)
	if want := []string{hH, hA, hB}; !slices.Equal(got, want) {
		t.Errorf("candidates %v, want %v", got, want)
	}
	// The arr file at the unlinked path goes with it; the other arr file stays.
	if got := f.str(`SELECT group_concat(path) FROM version_file WHERE version_id = 1`); got != "/arr/Gone.mkv" {
		t.Errorf("arr files left %q", got)
	}
	// The hide, its search requests and offers; queued copies nothing else wants stop, others stay.
	if f.str(`SELECT COUNT(*) FROM arrview_removed_version WHERE version_id = 1 AND view = 'ui'`) != "1" ||
		f.str(`SELECT COUNT(*) FROM arrview_removed_version WHERE version_id = 2`) != "0" {
		t.Error("hidden copies")
	}
	if got := f.str(`SELECT group_concat(version_key) FROM search_request WHERE title_id = 1`); got != "movie-remux" {
		t.Errorf("search requests left %q", got)
	}
	if got := f.str(`SELECT outcome || reasons FROM router_offer WHERE version_key = 'movie-web'`); got != `declined["removed"]` {
		t.Errorf("offer into the removed library: %s", got)
	}
	if got := f.str(`SELECT outcome FROM router_offer WHERE version_key = 'movie-remux'`); got != "pending" {
		t.Errorf("offer into the remaining library: %s", got)
	}
	if got := f.str(`SELECT group_concat(provider || ':' || state, ' ') FROM (SELECT provider, state FROM provider_copy
		WHERE info_hash = ? ORDER BY provider)`, hH); got != "ad:adding tb:skipped" {
		t.Errorf("copies of the removed download's hash: %s", got)
	}
	if got := f.str(`SELECT state FROM provider_copy WHERE info_hash = ?`, hA); got != "queued" {
		t.Errorf("a copy Other still wants: %s", got)
	}
	// The Removed action says what became of the files; each unlinked link has its own entry.
	rm := journal.ParseRemovalDetail(f.str(`SELECT detail FROM journal_action WHERE action = ?`, journal.ActRemoveVersion))
	if rm.Files != journal.RemovalFilesPartial || rm.Left != 4 {
		t.Errorf("removal outcome %+v", rm)
	}
	if got := f.str(`SELECT COUNT(*) FROM journal_action WHERE action = ?`, journal.ActUnlinkFile); got != "2" {
		t.Errorf("unlink entries %s", got)
	}
	// The clean-up's outcome is written into that action, keeping the files' outcome.
	RecordCleanup(context.Background(), f.st, nil, f.started[0].Actions, "deleted on RD")
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
	if len(f.started) != 1 || !slices.Equal(slices.Sorted(slices.Values(f.started[0].Hashes)), []string{hD, hE}) ||
		len(f.started[0].Actions) != 1 {
		t.Errorf("retry clean-up %+v", f.started)
	}
	if rm := journal.ParseRemovalDetail(f.str(`SELECT detail FROM journal_action WHERE action = ?`,
		journal.ActRemoveVersion)); rm.Files != journal.RemovalFilesPartial || rm.Left != 2 {
		t.Errorf("retry outcome %+v", rm)
	}
}

// Without the files: links, arr files and torrents stay; the action says the files were kept, no clean-up runs.
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
	if got := f.str(`SELECT COUNT(*) FROM version_file WHERE version_id = 1`); got != "2" {
		t.Errorf("arr files %s", got)
	}
	if rm := journal.ParseRemovalDetail(f.str(`SELECT detail FROM journal_action`)); rm.Files != journal.RemovalFilesKept ||
		rm.Cleanup != "" {
		t.Errorf("outcome %+v", rm)
	}
	if res.DownloadsEnded != 1 || f.str(`SELECT state FROM provider_copy WHERE info_hash = ? AND provider = 'tb'`, hH) != "skipped" ||
		f.str(`SELECT COUNT(*) FROM search_request WHERE version_key = 'movie-web'`) != "0" {
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
		journal.ActRemoveVersion)); rm.Cleanup != "torrents not cleaned up: the torrent manager is not running" {
		t.Errorf("outcome %+v", rm)
	}
	g := newFixture(t)
	g.exec(`DELETE FROM link WHERE id IN (10, 11, 14, 15)`)
	g.exec(`DELETE FROM version_file`)
	g.exec(`DELETE FROM download`)
	if _, err := Run(context.Background(), g.deps(), webSpec(true)); err != nil {
		t.Fatal(err)
	}
	if rm := journal.ParseRemovalDetail(g.str(`SELECT detail FROM journal_action WHERE action = ?`,
		journal.ActRemoveVersion)); len(g.started) != 0 || rm.Cleanup != "torrents kept: their files stay in the library" {
		t.Errorf("outcome %+v, started %v", rm, g.started)
	}
}

// Torrents only Sonarr's or Radarr's own files (arr file rows, no link of Theatarr's) name stay, and the event says
// whose files they are.
func TestRunKeepsTheArrsFiles(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.exec(`DELETE FROM link`)
	f.exec(`DELETE FROM version_file WHERE path <> '/arr/Gone.mkv'`)
	f.exec(`DELETE FROM download`)
	if _, err := Run(context.Background(), f.deps(), webSpec(true)); err != nil {
		t.Fatal(err)
	}
	if rm := journal.ParseRemovalDetail(f.str(`SELECT detail FROM journal_action WHERE action = ?`,
		journal.ActRemoveVersion)); len(f.started) != 0 || rm.Cleanup != "torrents kept: Sonarr's or Radarr's own files of it stay theirs" {
		t.Errorf("outcome %+v, started %v", rm, f.started)
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
