package torrents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
)

// removalFixture: title 1 (M) was removed with its files; title 2 (Other) is in the library. Candidates hA..hG:
//   - hA: rd 1, our add of five minutes ago, still downloading and not in the registry yet: deleted (24 h guard
//     lifted, the fresh-add unknown taken);
//   - hB: rd 2, linked by Other: kept, used by Other;
//   - hC: rd 3, a download of Other in flight: kept;
//   - hD: rd 4 and tb 90 (a copy on the other provider), unused: deleted on both;
//   - hE: rd 5, a file of the removed title that was kept (its link stays active): kept;
//   - hF: rd 6, linked from a configured other library's folder (scan root): kept;
//   - hG: rd 7, an old entry that was on the account before Theatarr reused it, unused: deleted, as the
//     Torrents page's delete would.
//
// hH (rd 8) is not a candidate and is never touched.
func removalFixture(t *testing.T) *env {
	e := newEnv(t)
	old := e.now.Add(-72 * time.Hour)
	e.exec(`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (2, 'movie', 'Other', 0, 0)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (2, 2, 'v', 0, 0)`)
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
	// Our own removed download of hA ended with the removal; the removed copy's unlinked link of hD no longer counts.
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		provider, provider_torrent_id, created_at, updated_at) VALUES (1, 1, 'v', ?, 'A', 'pub', 'failed', 'rd', '1',
		0, 0)`, hA)
	e.exec(`INSERT INTO link (path, target, title_id, state, file_metadata, created_at, updated_at)
		VALUES ('/lib/M/D.mkv', '/elsewhere/D.mkv', 1, 'unlinked', ?, 0, 0)`, `{"InfoHash":"`+hD+`"}`)
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		provider, provider_torrent_id, reused, created_at, updated_at) VALUES (1, 1, 'v', ?, 'G', 'pub', 'imported',
		'rd', '7', 1, 0, 0)`, hG)
	root := t.TempDir()
	if err := os.Symlink(mountPath+"/__all__/Movie.F.2016.1080p.WEB-DL/F.mkv", filepath.Join(root, "F.mkv")); err != nil {
		t.Fatal(err)
	}
	e.pol.Torrents = &policy.Torrents{ScanRoots: []string{root}}
	return e
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
	words, err := e.m.DeleteForRemoval(context.Background(), RemovalCleanup{Hashes: removalCandidates, By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("clean-up: %s", words)
	if got, want := e.deletedIDs(), []string{"rd/1", "rd/4", "rd/7", "tb/90"}; !slices.Equal(got, want) {
		t.Errorf("deleted %v, want %v (%s)", got, want, words)
	}
	if e.ard.deleted != 3 || e.atb.deleted != 1 {
		t.Errorf("adders deleted rd %d, tb %d", e.ard.deleted, e.atb.deleted)
	}
	// Kept: Other's link and download, the removed title's own kept file, the other library's folder.
	for _, w := range []string{"deleted on RD, TB", "kept on RD: used by Other", "used by M", "F.mkv"} {
		if !strings.Contains(words, w) {
			t.Errorf("words %q lack %q", words, w)
		}
	}
	// The full pre-delete backup was taken first, with every provider.
	bs, err := e.m.Backups()
	if err != nil || len(bs) != 1 || bs[0].Reason != ReasonPreDelete || len(bs[0].Providers) != 2 {
		t.Errorf("backups %+v %v", bs, err)
	}
}

// No backup, no delete: the clean-up deletes nothing and says so.
func TestDeleteForRemovalBackupFails(t *testing.T) {
	t.Parallel()
	e := removalFixture(t)
	e.m.Dir = ""
	words, err := e.m.DeleteForRemoval(context.Background(), RemovalCleanup{Hashes: removalCandidates})
	if err != nil {
		t.Fatal(err)
	}
	if e.ard.deleted+e.atb.deleted != 0 || len(e.deletedIDs()) != 0 {
		t.Fatalf("deleted without a backup: %v", e.deletedIDs())
	}
	if words != "torrents not cleaned up: backup failed (RD, TB)" {
		t.Errorf("words %q", words)
	}
}

// A provider that errors: its part fails and says so, the other provider's deletes go on; a provider that cannot be
// listed is reported too.
func TestDeleteForRemovalProviderErrors(t *testing.T) {
	t.Parallel()
	e := removalFixture(t)
	e.atb.deleteErr = errors.New("tb is down")
	words, err := e.m.DeleteForRemoval(context.Background(), RemovalCleanup{Hashes: []string{hD}})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.deletedIDs(); !slices.Equal(got, []string{"rd/4"}) || !strings.Contains(words, "TB: failed, tb is down") ||
		!strings.Contains(words, "deleted on RD") {
		t.Errorf("deleted %v, words %q", got, words)
	}
	e2 := removalFixture(t)
	e2.rd.listErr = errors.New("rd unreachable")
	words, err = e2.m.DeleteForRemoval(context.Background(), RemovalCleanup{Hashes: []string{hD}})
	if err != nil {
		t.Fatal(err)
	}
	if got := e2.deletedIDs(); !slices.Equal(got, []string{"tb/90"}) ||
		!strings.Contains(words, "RD: failed, could not be listed: rd unreachable") {
		t.Errorf("deleted %v, words %q", got, words)
	}
}

// A copy to another provider still adding one of the hashes is waited for; once it settles, its torrent (listed now)
// is cleaned up too. One that never settles within the wait is kept.
func TestDeleteForRemovalWaitsForMidAddCopy(t *testing.T) {
	t.Parallel()
	e := removalFixture(t)
	e.tb.list = nil
	e.exec(`INSERT INTO provider_copy (info_hash, name, title_id, provider, own_add, state, created_at, updated_at)
		VALUES (?, 'D', 1, 'tb', 1, 'adding', 0, 0)`, hD)
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
	words, err := e.m.DeleteForRemoval(context.Background(), RemovalCleanup{Hashes: []string{hD}})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.deletedIDs(); !slices.Equal(got, []string{"rd/4", "tb/91"}) || sleeps < 2 {
		t.Errorf("deleted %v after %d waits: %q", got, sleeps, words)
	}

	e2 := removalFixture(t)
	e2.exec(`INSERT INTO provider_copy (info_hash, name, title_id, provider, own_add, state, created_at, updated_at)
		VALUES (?, 'D', 1, 'tb', 1, 'checking', 0, 0)`, hD)
	words, err = e2.m.DeleteForRemoval(context.Background(), RemovalCleanup{Hashes: []string{hD}})
	if err != nil {
		t.Fatal(err)
	}
	if got := e2.deletedIDs(); !slices.Equal(got, []string{"rd/4"}) ||
		!strings.Contains(words, "kept on TB: a copy to it is still being added") {
		t.Errorf("deleted %v, words %q", got, words)
	}
}
