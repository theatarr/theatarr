package torrents

import (
	"context"
	"errors"
	"fmt"
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
// title 2 (Other) is in the library. Candidates hA..hG:
//   - hA: rd 1, our own add of five minutes ago (a download of ours, not a reuse), still downloading and not in the
//     registry yet: deleted (24 h guard lifted; ours, so the downloading guard too; the fresh-add unknown taken);
//   - hB: rd 2, linked by Other: kept, used by Other;
//   - hC: rd 3, a download of Other in flight: kept;
//   - hD (library w): rd 4 (our download's) and tb 90 (our copy to the other provider), unused: deleted on both;
//   - hE: rd 5, a file of the removed title that was kept (its link stays active): kept;
//   - hF: rd 6, linked from a configured other library's folder (scan root): kept;
//   - hG: rd 7, an old entry that was on the account before Theatarr reused it, unused: kept, not ours.
//
// hH (rd 8) is not a candidate and is never touched.
func removalFixture(t *testing.T) *env {
	e := newEnv(t)
	old := e.now.Add(-72 * time.Hour)
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
	e.synced("rd")
	e.synced("tb")
	for _, h := range []string{hB, hC, hD, hE, hF, hG, hH} {
		e.content(h, 1)
	}
	e.exec(`INSERT INTO link (path, target, title_id, state, file_metadata, created_at, updated_at)
		VALUES ('/lib/Other/B.mkv', '/elsewhere/B.mkv', 2, 'active', ?, 0, 0)`, `{"InfoHash":"`+hB+`"}`)
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at) VALUES (2, 2, 'v', ?, 'C', 'pub', 'downloading', 0, 0)`, hC)
	e.exec(`INSERT INTO link (path, target, title_id, state, file_metadata, created_at, updated_at)
		VALUES ('/lib/M/E.mkv', '/elsewhere/E.mkv', 1, 'active', ?, 0, 0)`, `{"InfoHash":"`+hE+`"}`)
	// Our own removed downloads ended with the removal (hA) or were imported (hD, its link now unlinked); hG's was a
	// reuse of an entry already on the account; hD's copy to tb was our own add.
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		provider, provider_torrent_id, reused, created_at, updated_at) VALUES
		(1, 1, 'v', ?, 'A', 'pub', 'failed', 'rd', '1', 0, 0, 0),
		(1, 3, 'w', ?, 'D', 'pub', 'imported', 'rd', '4', 0, 0, 0),
		(1, 1, 'v', ?, 'G', 'pub', 'imported', 'rd', '7', 1, 0, 0)`, hA, hD, hG)
	// hB, hC, hE and hF were our own adds too (imported for the removed title): their usage keeps them.
	for id, h := range map[string]string{"2": hB, "3": hC, "5": hE, "6": hF} {
		e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
			provider, provider_torrent_id, created_at, updated_at) VALUES (1, 1, 'v', ?, 'x', 'pub', 'imported', 'rd',
			?, 0, 0)`, h, id)
	}
	e.exec(`INSERT INTO provider_copy (info_hash, name, title_id, provider, torrent_id, own_add, state, created_at,
		updated_at) VALUES (?, 'D', 1, 'tb', '90', 1, 'added', 0, 0)`, hD)
	e.exec(`INSERT INTO link (path, target, title_id, state, file_metadata, created_at, updated_at)
		VALUES ('/lib/M/D.mkv', '/elsewhere/D.mkv', 1, 'unlinked', ?, 0, 0)`, `{"InfoHash":"`+hD+`"}`)
	root := t.TempDir()
	if err := os.Symlink(mountPath+"/__all__/Movie.F.2016.1080p.WEB-DL/F.mkv", filepath.Join(root, "F.mkv")); err != nil {
		t.Fatal(err)
	}
	e.pol.Torrents = &policy.Torrents{ScanRoots: []string{root}}
	return e
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
	if got, want := e.deletedIDs(), []string{"rd/1", "rd/4", "tb/90"}; !slices.Equal(got, want) {
		t.Errorf("deleted %v, want %v (%s)", got, want, words)
	}
	if e.ard.deleted != 2 || e.atb.deleted != 1 {
		t.Errorf("adders deleted rd %d, tb %d", e.ard.deleted, e.atb.deleted)
	}
	// Kept: Other's link and download, the removed title's own kept file, the other library's folder, the entry
	// that was on the account before Theatarr reused it.
	for _, w := range []string{"deleted on RD, TB", "kept on RD: used by Other", "used by M", "F.mkv"} {
		if !strings.Contains(words, w) {
			t.Errorf("words %q lack %q", words, w)
		}
	}
	if w := out.Words([]string{hG}); w != "kept on RD: it was on the account before Theatarr added it" {
		t.Errorf("hG: %q", w)
	}
	// Each library's own outcome: the kept torrent's library does not read deleted.
	if w := out.Words([]string{hD}); w != "deleted on RD, TB" {
		t.Errorf("hD: %q", w)
	}
	if w := out.Words([]string{hB}); strings.Contains(w, "deleted") || !strings.Contains(w, "used by Other") {
		t.Errorf("hB: %q", w)
	}
	// The full pre-delete backup was taken first, with every provider.
	bs, err := e.m.Backups()
	if err != nil || len(bs) != 1 || bs[0].Reason != ReasonPreDelete || len(bs[0].Providers) != 2 {
		t.Errorf("backups %+v %v", bs, err)
	}
}

// Still downloading at the provider: our own add goes (the removal ended its download), one not provably ours stays
// (AllowRecent-like lifting of the age guard does not lift the downloading guard).
func TestRemovalDeleteNeedsOwnershipWhileDownloading(t *testing.T) {
	t.Parallel()
	e := removalFixture(t)
	e.content(hA, 1)
	plan, err := e.m.PlanDelete(context.Background(), []DeleteSelection{
		{Provider: "rd", ID: "1", Hash: hA, removal: true, owned: false},
		{Provider: "rd", ID: "4", Hash: hD, removal: true, owned: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Items[0].Action != ActRefuse || !strings.Contains(plan.Items[0].Refusal, "still downloading") ||
		plan.Items[1].Action != ActDelete {
		t.Errorf("plan %+v", plan.Items)
	}
	// The Torrents page's own delete is unchanged: allow_recent lifts both guards there.
	plan, err = e.m.PlanDelete(context.Background(), []DeleteSelection{{Provider: "rd", ID: "1", Hash: hA,
		AllowRecent: true}})
	if err != nil || plan.Items[0].Refusal == "still downloading" {
		t.Errorf("admin plan %+v %v", plan, err)
	}
}

// No backup, no delete: the clean-up deletes nothing and says so.
func TestDeleteForRemovalBackupFails(t *testing.T) {
	t.Parallel()
	e := removalFixture(t)
	e.m.Dir = ""
	out, err := e.m.DeleteForRemoval(context.Background(), cleanup(removalCandidates...))
	if err != nil {
		t.Fatal(err)
	}
	if e.ard.deleted+e.atb.deleted != 0 || len(e.deletedIDs()) != 0 {
		t.Fatalf("deleted without a backup: %v", e.deletedIDs())
	}
	if w := out.Words([]string{hA}); w != "torrents not cleaned up: backup failed (RD, TB)" {
		t.Errorf("words %q", w)
	}
}

// The backup must hold every provider: one whose list fails while the backup is taken (it held no candidate) stops
// the clean-up everywhere.
func TestDeleteForRemovalNeedsACompleteBackup(t *testing.T) {
	t.Parallel()
	e := removalFixture(t)
	e.tb.list = nil
	e.tb.listErrAfter = 1 // the clean-up's own listing reads it; the backup's fails
	out, err := e.m.DeleteForRemoval(context.Background(), cleanup(hA))
	if err != nil {
		t.Fatal(err)
	}
	if e.ard.deleted+e.atb.deleted != 0 || !strings.HasPrefix(out.Abort, "torrents not cleaned up: backup failed") {
		t.Errorf("deleted %d, %q", e.ard.deleted+e.atb.deleted, out.Abort)
	}
}

// A provider whose delete errors: its part fails and says so, the other provider's deletes go on. A provider that
// cannot be listed stops the whole clean-up: nothing is deleted anywhere.
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

// A copy to another provider still adding one of the hashes is waited for; once it settles, its torrent (listed now,
// our own add) is cleaned up too. One that never settles within the wait is kept.
func TestDeleteForRemovalWaitsForMidAddCopy(t *testing.T) {
	t.Parallel()
	e := removalFixture(t)
	e.tb.list = nil
	e.exec(`UPDATE provider_copy SET state = 'adding', torrent_id = NULL WHERE info_hash = ?`, hD)
	sleeps := 0
	e.m.Sleep = func(_ context.Context, d time.Duration) error {
		e.now = e.now.Add(d)
		if sleeps++; sleeps == 2 {
			// The add lands and the copy settles.
			e.tb.list = []provider.Torrent{tor("91", hD, "Movie.D.2018.1080p.WEB-DL", 400, e.now, provider.StateDownloaded)}
			e.exec(`UPDATE provider_copy SET state = 'added', torrent_id = '91' WHERE info_hash = ?`, hD)
		}
		return nil
	}
	out, err := e.m.DeleteForRemoval(context.Background(), cleanup(hD))
	if err != nil {
		t.Fatal(err)
	}
	if got := e.deletedIDs(); !slices.Equal(got, []string{"rd/4", "tb/91"}) || sleeps < 2 {
		t.Errorf("deleted %v after %d waits: %q", got, sleeps, out.Words([]string{hD}))
	}

	e2 := removalFixture(t)
	e2.exec(`UPDATE provider_copy SET state = 'checking' WHERE info_hash = ?`, hD)
	out, err = e2.m.DeleteForRemoval(context.Background(), cleanup(hD))
	if err != nil {
		t.Fatal(err)
	}
	if got := e2.deletedIDs(); !slices.Equal(got, []string{"rd/4"}) ||
		!strings.Contains(out.Words([]string{hD}), "kept on TB: a copy to it is still being added") {
		t.Errorf("deleted %v, words %q", got, out.Words([]string{hD}))
	}
}

// A library restored (an Add) before the clean-up, or while it runs, keeps its torrents: checked after the wait and
// again right before each delete.
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
	e.synced("rd")
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (1, 'ui', 0)`)
	old := e.now.Add(-72 * time.Hour)
	var hashes []string
	for i := range MaxDelete + 2 {
		h := fmt.Sprintf("%040x", i+1)
		id := fmt.Sprint(i + 1)
		hashes = append(hashes, h)
		e.rd.list = append(e.rd.list, tor(id, h, "T."+id, 1, old, provider.StateDownloaded))
		e.content(h, 1)
		e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
			provider, provider_torrent_id, created_at, updated_at) VALUES (1, 1, 'v', ?, 'x', 'pub', 'imported', 'rd', ?,
			0, 0)`, h, id)
	}
	out, err := e.m.DeleteForRemoval(context.Background(), cleanup(hashes...))
	if err != nil {
		t.Fatal(err)
	}
	if e.ard.deleted != MaxDelete || !strings.Contains(out.Words(hashes), "more than 200 torrents in one removal") {
		t.Errorf("deleted %d, %q", e.ard.deleted, out.Words(hashes))
	}
}
