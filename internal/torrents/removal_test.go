package torrents

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
)

// removalFixture: title 1 (M) had two libraries, v (version 1) and w (version 3), both removed with their files;
// title 2 (Other) is in the library. The accounts are read through the provider sync, as Refresh does. Candidates:
//   - hA: rd 1, added five minutes ago, still downloading: deleted (the 24 h and downloading guards lifted);
//   - hB: rd 2, linked by Other: kept, used by Other;
//   - hC: rd 3, a download of Other in flight: kept;
//   - hD (library w): rd 4, unused: deleted; tb 90, a copy on the other provider: deleted too;
//   - hE: rd 5, a file of the removed title that was kept (its link stays active): kept;
//   - hF: rd 6, linked from a configured other library's folder (scan root): kept;
//   - hG: rd 7, an entry that was on the account before Theatarr reused it, unused: deleted (whoever added it).
//
// hH (rd 8) is not a candidate and is never touched.
func removalFixture(t *testing.T) *env {
	e := newEnv(t)
	old := e.now.Add(-72 * time.Hour)
	e.m.Syncer = &provider.Syncer{Store: e.st, Providers: e.m.Providers, Configured: []string{"rd", "tb"},
		Now: func() time.Time { return e.now }, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	e.exec(`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (2, 'movie', 'Other', 0, 0)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (2, 2, 'v', 0, 0),
		(3, 1, 'w', 0, 0)`)
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (1, 'ui', 0), (3, 'ui', 0)`)
	e.rd.list = []provider.Torrent{
		tor("1", hA, "Movie.A.2020.1080p.WEB-DL", 100, e.now.Add(-5*time.Minute), provider.StateDownloading),
		tor("2", hB, "Movie.B.2021.2160p.WEB-DL", 200, old, provider.StateDownloaded),
		tor("3", hC, "Movie.C.2019.720p.BluRay", 300, old, provider.StateDownloaded),
		tor("4", hD, "Movie.D.2018.1080p.WEB-DL", 400, old, provider.StateDownloaded),
		tor("5", hE, "Movie.E.2017.1080p.WEB-DL", 500, old, provider.StateDownloaded),
		tor("6", hF, "Movie.F.2016.1080p.WEB-DL", 600, old, provider.StateDownloaded),
		tor("7", hG, "Movie.G.2015.1080p.WEB-DL", 700, old, provider.StateDownloaded),
		tor("8", hH, "Movie.H.2014.1080p.WEB-DL", 800, old, provider.StateDownloaded),
	}
	e.tb.list = []provider.Torrent{tor("90", hD, "Movie.D.2018.1080p.WEB-DL", 400, old, provider.StateDownloaded)}
	e.exec(`INSERT INTO link (path, target, title_id, state, file_metadata, created_at, updated_at)
		VALUES ('/lib/Other/B.mkv', '/elsewhere/B.mkv', 2, 'active', ?, 0, 0)`, `{"InfoHash":"`+hB+`"}`)
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at) VALUES (2, 2, 'v', ?, 'C', 'pub', 'downloading', 0, 0)`, hC)
	e.exec(`INSERT INTO link (path, target, title_id, state, file_metadata, created_at, updated_at)
		VALUES ('/lib/M/E.mkv', '/elsewhere/E.mkv', 1, 'active', ?, 0, 0)`, `{"InfoHash":"`+hE+`"}`)
	e.exec(`INSERT INTO link (path, target, title_id, state, file_metadata, created_at, updated_at)
		VALUES ('/lib/M/D.mkv', '/elsewhere/D.mkv', 1, 'unlinked', ?, 0, 0)`, `{"InfoHash":"`+hD+`"}`)
	// The removed title's downloads: hA's ended with the removal, the others were imported; hG's was a reuse of an
	// entry already on the account; hD's copy to tb was ours.
	for id, h := range map[string]string{"1": hA, "2": hB, "3": hC, "4": hD, "5": hE, "6": hF} {
		e.imported("rd", id, h)
	}
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		provider, provider_torrent_id, reused, provider_epoch, created_at, updated_at) VALUES
		(1, 1, 'v', ?, 'G', 'pub', 'imported', 'rd', '7', 1, 0, 0, 0)`, hG)
	e.exec(`INSERT INTO provider_copy (info_hash, name, title_id, provider, torrent_id, own_add, state, created_at,
		updated_at) VALUES (?, 'D', 1, 'tb', '90', 1, 'added', 0, 0)`, hD)
	root := t.TempDir()
	if err := os.Symlink(mountPath+"/__all__/Movie.F.2016.1080p.WEB-DL/F.mkv", filepath.Join(root, "F.mkv")); err != nil {
		t.Fatal(err)
	}
	e.pol.Torrents = &policy.Torrents{ScanRoots: []string{root}}
	return e
}

// imported records an imported download of title 1 as torrent id on prov.
func (e *env) imported(prov, id, hash string) {
	e.t.Helper()
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		provider, provider_torrent_id, created_at, updated_at) VALUES (1, 1, 'v', ?, 'x', 'pub', 'imported', ?, ?, 0,
		0)`, hash, prov, id)
}

// cleanup is a removal clean-up of title 1 for hashes, hD in library w and the others in v.
func cleanup(hashes ...string) RemovalCleanup {
	c := RemovalCleanup{TitleID: 1, Hashes: map[string][]string{}, By: "mgr"}
	for _, h := range hashes {
		c.Hashes[h] = []string{"v"}
		if h == hD {
			c.Hashes[h] = []string{"w"}
		}
	}
	return c
}

var removalCandidates = []string{hA, hB, hC, hD, hE, hF, strings.ToUpper(hG)}

// deletedIDs are the torrents the journal says were deleted, as provider/id.
func (e *env) deletedIDs() []string {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT provider || '/' || torrent_id FROM torrent_journal
		WHERE action = 'delete' AND outcome = 'deleted' ORDER BY provider, torrent_id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestDeleteForRemoval(t *testing.T) {
	t.Parallel()
	e := removalFixture(t)
	out, err := e.m.DeleteForRemoval(context.Background(), cleanup(removalCandidates...))
	if err != nil {
		t.Fatal(err)
	}
	words := out.Words(removalCandidates)
	t.Logf("clean-up: %s", words)
	if got, want := e.deletedIDs(), []string{"rd/1", "rd/4", "rd/7", "tb/90"}; !slices.Equal(got, want) {
		t.Errorf("deleted %v, want %v (%s)", got, want, words)
	}
	if e.ard.deleted != 3 || e.atb.deleted != 1 {
		t.Errorf("adders deleted rd %d, tb %d", e.ard.deleted, e.atb.deleted)
	}
	// Each library's own outcome.
	for h, want := range map[string]string{
		hD: "deleted on RD, TB",
		hB: "kept on RD: used by Other",
		hE: "kept on RD: used by M",
		hG: "deleted on RD",
	} {
		if w := out.Words([]string{h}); w != want {
			t.Errorf("%s: %q, want %q", h[:1], w, want)
		}
	}
	if w := out.Words([]string{hF}); !strings.Contains(w, "F.mkv") {
		t.Errorf("hF: %q", w)
	}
	// One read of the accounts (the sync), none for planning or the backup; the backup holds every provider.
	if e.rd.lists != 1 || e.tb.lists != 1 {
		t.Errorf("listed rd %d, tb %d times", e.rd.lists, e.tb.lists)
	}
	bs, err := e.m.Backups()
	if err != nil || len(bs) != 1 || bs[0].Reason != ReasonPreDelete || len(bs[0].Providers) != 2 {
		t.Errorf("backups %+v %v", bs, err)
	}
}

// No backup, no delete: the clean-up deletes nothing and names the provider whose backup failed.
func TestDeleteForRemovalBackupFails(t *testing.T) {
	t.Parallel()
	e := removalFixture(t)
	e.m.Dir = ""
	out, err := e.m.DeleteForRemoval(context.Background(), cleanup(removalCandidates...))
	if err != nil {
		t.Fatal(err)
	}
	if e.ard.deleted+e.atb.deleted != 0 || len(e.deletedIDs()) != 0 || !strings.HasPrefix(out.Abort,
		"torrents not cleaned up: backup failed") {
		t.Fatalf("deleted %v, %q", e.deletedIDs(), out.Abort)
	}
	// A provider whose files cannot be read for the backup: named, nothing deleted anywhere.
	e2 := removalFixture(t)
	reads := 0
	e2.tb.onDetail = func() {
		if reads++; reads > 1 { // the plan's read passes, the backup's fails
			e2.tb.detailErr = errors.New("tb is down")
		}
	}
	out, err = e2.m.DeleteForRemoval(context.Background(), cleanup(hD))
	if err != nil {
		t.Fatal(err)
	}
	if e2.ard.deleted+e2.atb.deleted != 0 || out.Abort != "torrents not cleaned up: backup failed (TB)" {
		t.Errorf("deleted %d, %q", e2.ard.deleted+e2.atb.deleted, out.Abort)
	}
}

// A provider whose delete errors: its part fails and says so, the other provider's deletes go on. A provider that
// cannot be read stops the whole clean-up: nothing is deleted anywhere.
func TestDeleteForRemovalProviderErrors(t *testing.T) {
	t.Parallel()
	e := removalFixture(t)
	e.atb.deleteErr = errors.New("tb is down")
	out, err := e.m.DeleteForRemoval(context.Background(), cleanup(hD))
	if err != nil {
		t.Fatal(err)
	}
	words := out.Words([]string{hD})
	if got := e.deletedIDs(); !slices.Equal(got, []string{"rd/4"}) || !strings.Contains(words, "TB: failed, tb is down") ||
		!strings.Contains(words, "deleted on RD") {
		t.Errorf("deleted %v, words %q", got, words)
	}
	e2 := removalFixture(t)
	e2.rd.listErr = errors.New("rd unreachable")
	out, err = e2.m.DeleteForRemoval(context.Background(), cleanup(hD))
	if err != nil {
		t.Fatal(err)
	}
	if got := e2.deletedIDs(); len(got) != 0 || out.Words([]string{hD}) != "torrents not cleaned up: could not read RD" {
		t.Errorf("deleted %v, words %q", got, out.Words([]string{hD}))
	}
}

// A library restored (an Add) before the clean-up, or while it runs, keeps its torrents: checked first and again
// right before each delete.
func TestDeleteForRemovalKeepsARestoredLibrary(t *testing.T) {
	t.Parallel()
	e := removalFixture(t)
	e.exec(`DELETE FROM arrview_removed_version WHERE version_id = 3`) // w restored
	out, err := e.m.DeleteForRemoval(context.Background(), cleanup(hA, hD))
	if err != nil {
		t.Fatal(err)
	}
	if got := e.deletedIDs(); !slices.Equal(got, []string{"rd/1"}) ||
		out.Words([]string{hD}) != "kept: restored before the clean-up ran" {
		t.Errorf("deleted %v, hD %q", got, out.Words([]string{hD}))
	}

	// Restored while the deletes run (between rd's two deletes): hD's are kept.
	e2 := removalFixture(t)
	e2.m.Sleep = func(_ context.Context, d time.Duration) error {
		e2.now = e2.now.Add(d)
		e2.exec(`DELETE FROM arrview_removed_version WHERE version_id = 3`)
		return nil
	}
	out, err = e2.m.DeleteForRemoval(context.Background(), cleanup(hA, hD))
	if err != nil {
		t.Fatal(err)
	}
	if got := e2.deletedIDs(); !slices.Equal(got, []string{"rd/1"}) ||
		!strings.Contains(out.Words([]string{hD}), "restored before the clean-up ran") {
		t.Errorf("deleted %v, hD %q", got, out.Words([]string{hD}))
	}
}

// More than MaxDelete torrents: the first MaxDelete are cleaned up, the rest are kept and said so.
func TestDeleteForRemovalSaysWhatItLeftOver(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.m.Syncer = &provider.Syncer{Store: e.st, Providers: e.m.Providers, Configured: []string{"rd", "tb"},
		Now: func() time.Time { return e.now }, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (1, 'ui', 0)`)
	old := e.now.Add(-72 * time.Hour)
	var hashes []string
	for i := range MaxDelete + 2 {
		h := fmt.Sprintf("%040x", i+1)
		id := fmt.Sprint(i + 1)
		hashes = append(hashes, h)
		e.rd.list = append(e.rd.list, tor(id, h, "T."+id, 1, old, provider.StateDownloaded))
		e.imported("rd", id, h)
	}
	out, err := e.m.DeleteForRemoval(context.Background(), cleanup(hashes...))
	if err != nil {
		t.Fatal(err)
	}
	if e.ard.deleted != MaxDelete || !strings.Contains(out.Words(hashes), "more than 200 torrents in one removal") {
		t.Errorf("deleted %d, %q", e.ard.deleted, out.Words(hashes))
	}
}

// A torrent another title holds only through an adopted link (its hash in adopted_source, its mount folder named
// otherwise) is used: kept by the clean-up, and used on the Torrents page.
func TestDeleteForRemovalKeepsAnAdoptedLinksTorrent(t *testing.T) {
	t.Parallel()
	e := removalFixture(t)
	const hI = "3333333333333333333333333333333333333333"
	e.rd.list = append(e.rd.list, tor("9", hI, "Movie.I.2013.1080p.WEB-DL", 900, e.now.Add(-72*time.Hour),
		provider.StateDownloaded))
	e.imported("rd", "9", hI)
	e.exec(`INSERT INTO link (path, target, title_id, state, adopted_source, created_at, updated_at)
		VALUES ('/lib/Other/I.mkv', ?, 2, 'active', ?, 0, 0)`, mountPath+"/__all__/Some Other Name/I.mkv",
		`{"hash":"`+strings.ToUpper(hI)+`"}`)
	out, err := e.m.DeleteForRemoval(context.Background(), cleanup(hI))
	if err != nil {
		t.Fatal(err)
	}
	if got := e.deletedIDs(); len(got) != 0 || out.Words([]string{hI}) != "kept on RD: used by Other" {
		t.Errorf("deleted %v, %q", got, out.Words([]string{hI}))
	}
	if err := e.m.StoreUsage(context.Background()); err != nil {
		t.Fatal(err)
	}
	r, err := e.m.Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := find(r, "rd", "9"); got == nil || got.Usage != Used {
		t.Errorf("report: %+v", got)
	}
}

// The backup is taken from the clean-up's one read, with the files the listing gave: nothing is listed again, and a
// torrent whose files the list carried needs no detail read.
func TestDeleteForRemovalBacksUpTheReadWithItsFiles(t *testing.T) {
	t.Parallel()
	e := removalFixture(t)
	for i := range e.rd.list {
		e.rd.list[i].Files = []provider.File{{ID: "1", Path: "/" + e.rd.list[i].Name + "/a.mkv", Bytes: 1, Selected: true}}
	}
	if _, err := e.m.DeleteForRemoval(context.Background(), cleanup(hD)); err != nil {
		t.Fatal(err)
	}
	bs, err := e.m.Backups()
	if err != nil || len(bs) != 1 {
		t.Fatalf("backups %+v %v", bs, err)
	}
	b, err := e.m.ReadBackup(bs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range b.Providers {
		for _, bt := range p.Torrents {
			if p.Provider == "rd" && (!bt.FilesKnown || len(bt.Files) != 1) {
				t.Errorf("rd %s: files %+v", bt.ID, bt.Files)
			}
		}
	}
	if e.rd.lists != 1 || e.tb.lists != 1 {
		t.Errorf("listed rd %d, tb %d", e.rd.lists, e.tb.lists)
	}
}
