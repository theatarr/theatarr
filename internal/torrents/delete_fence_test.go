package torrents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/rootprobe"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/torrents/reffence"
)

// Another process making a library link (its writer lease, reffence.Begin) keeps a delete from
// running its last usage check until the link is recorded: after HoldWait the item is skipped, nothing deleted.
func TestDeleteWaitsForForeignLinkWrite(t *testing.T) {
	e, sel, plan := f2Fixture(t)
	old := reffence.HoldWait
	reffence.HoldWait = 300 * time.Millisecond
	t.Cleanup(func() { reffence.HoldWait = old })
	e.exec(`INSERT INTO library_lease (name, owner, until) VALUES ('torrents:write:pid 1 at 1/1', 'pid 1 at 1', ?)`,
		store.Millis(e.now.Add(time.Minute)))
	done, err := e.m.Delete(context.Background(), sel, plan.Token, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if e.ard.deleted != 0 || done.Items[0].Outcome != OutSkipped ||
		!strings.Contains(done.Items[0].Error, "link write is still in progress") {
		t.Fatalf("deleted %d: %+v", e.ard.deleted, done.Items)
	}
	// An expired writer lease (a crash) does not stop deletes.
	e.exec(`UPDATE library_lease SET until = ? WHERE name LIKE 'torrents:write:%'`, store.Millis(e.now.Add(-time.Second)))
	release, err := reffence.Hold(context.Background(), e.st, e.now)
	if err != nil {
		t.Fatalf("after the writer's lease expired: %v", err)
	}
	release()
}

// A writer that holds the fence (Begin) keeps its own lease while it writes; CheckTx refuses once it ran out.
func TestBeginLeaseExpiryRefusesCommit(t *testing.T) {
	t.Parallel()
	e, _, _ := f2Fixture(t)
	ctx, _, release, err := reffence.Begin(context.Background(), e.st, e.now)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	tx, err := e.st.Writer().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := reffence.CheckTx(ctx, tx, e.now); err != nil {
		t.Errorf("fresh fence: %v", err)
	}
	if err := reffence.CheckTx(ctx, tx, e.now.Add(reffence.LeaseFor+time.Second)); err != reffence.ErrExpired {
		t.Errorf("expired fence: %v", err)
	}
	tx.Rollback()
}

// A scan root whose resolution hangs ends the scan after the root timeout (an error: unknown usage),
// and at once while that call is stuck.
func TestScanRootBoundsHungResolution(t *testing.T) {
	root := t.TempDir()
	release := make(chan struct{})
	orig, origTimeout := policy.ResolveLstat, policy.RootResolveTimeout
	policy.RootResolveTimeout = 50 * time.Millisecond
	policy.ResolveLstat = func(p string) (os.FileInfo, error) {
		if p == root {
			<-release
		}
		return orig(p)
	}
	t.Cleanup(func() {
		close(release)
		for deadline := time.Now().Add(5 * time.Second); !rootprobe.Default.Idle() && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
		policy.ResolveLstat, policy.RootResolveTimeout = orig, origTimeout
	})
	mv := &mountView{path: "/nonexistent-debrid", all: "__all__"}
	for i := range 2 {
		done := make(chan error, 1)
		go func() { done <- scanRoot(context.Background(), root, nil, mv, newScanIndex()) }()
		select {
		case err := <-done:
			if !errors.Is(err, policy.ErrRootUnreachable) {
				t.Fatalf("scan %d: %v", i, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("scan %d did not return", i)
		}
	}
}

// A directory of a scan root that cannot be read makes the scan incomplete (an error), as before.
func TestScanRootUnreadableDirIsIncomplete(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	root := t.TempDir()
	sub := filepath.Join(root, "locked")
	if err := os.Mkdir(sub, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })
	err := scanRoot(context.Background(), root, nil, &mountView{path: "/nonexistent-debrid", all: "__all__"}, newScanIndex())
	if err == nil {
		t.Fatal("an unreadable directory gave a complete scan")
	}
}

// The bounded walk ties links to their mount folders as before.
func TestScanRootFindsLinks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mount := filepath.Join(t.TempDir(), "zurg")
	if err := os.MkdirAll(filepath.Join(root, "Movie (2001)"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(mount, "__all__", "Movie.2001.1080p", "m.mkv"),
		filepath.Join(root, "Movie (2001)", "m.mkv")); err != nil {
		t.Fatal(err)
	}
	ix := newScanIndex()
	if err := scanRoot(context.Background(), root, nil, &mountView{path: mount, all: "__all__"}, ix); err != nil {
		t.Fatal(err)
	}
	if refs := ix.byFolder["Movie.2001.1080p"]; len(refs) != 1 || refs[0].Kind != RefScan {
		t.Fatalf("refs %+v", ix.byFolder)
	}
}

// A delete's per-item check reads only the rows one torrent's usage sees
// (loadDBRefsFor); its usage and override token match the full index's for every torrent, including several
// references of one kind (the token lists them in order), a base32 arr hash, an untied link and a link by folder.
func TestScopedRefsMatchFullIndex(t *testing.T) {
	t.Parallel()
	e, _ := usageFixture(t)
	ctx := context.Background()
	b32 := "77777777777777777777777777777777" // base32 of twenty 0xff bytes: hF
	e.exec(`INSERT INTO version_file (version_id, title_id, instance, arr_file_id, path, download_id)
		VALUES (1, 1, 'radarr', 8, '/movies/F.mkv', ?)`, b32)
	for _, p := range []string{"/lib/A/A2.mkv", "/lib/A/A1.mkv"} {
		e.exec(`INSERT INTO link (path, target, title_id, created_at, updated_at, file_metadata)
			VALUES (?, '/elsewhere/A.mkv', 1, 0, 0, ?)`, p, `{"InfoHash":"`+hA+`"}`)
	}
	e.exec(`INSERT INTO link (path, target, title_id, created_at, updated_at) VALUES ('/lib/B/B2.mkv', ?, 1, 0, 0)`,
		mountPath+"/__all__/Movie.B.2021.2160p.WEB-DL/other.mkv")
	e.exec(`INSERT INTO link (path, target, title_id, created_at, updated_at) VALUES ('/lib/X.mkv', ?, 1, 0, 0)`,
		mountPath+"/x.mkv") // in the mount, no folder, no hash: untied
	full, err := e.m.loadDBRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range append(append([]provider.Torrent{}, e.rd.list...), e.tb.list...) {
		key := "rd"
		if tr.ID == "90" {
			key = "tb"
		}
		h := indexer.NormalizeHash(tr.Hash)
		scoped, err := e.m.loadDBRefsFor(ctx, &refScope{hash: h, folders: library.ReleaseFolderNames(h, tr.Name)})
		if err != nil {
			t.Fatal(err)
		}
		fu, fc, fl, fr := full.usage(key, tr)
		su, scnt, sl, sr := scoped.usage(key, tr)
		if fu != su || overrideToken(key, tr, fu, fc, fl, fr) != overrideToken(key, tr, su, scnt, sl, sr) {
			t.Errorf("%s %s: full %s %v %v %v, scoped %s %v %v %v", key, tr.ID, fu, fc, fl, fr, su, scnt, sl, sr)
		}
	}
	if u, refs, _, _ := full.usage("rd", e.rd.list[5]); u != Used || refs[RefArrFile] != 1 {
		t.Fatalf("base32 arr hash not tied: %s %v", u, refs)
	}
}

// A link write of this process stuck in its filesystem change keeps a delete waiting at most HoldWait,
// and the delete does not hold the router's add lock while it waits; the item is skipped, nothing deleted.
func TestDeleteDoesNotHoldAddLockWhileWaitingForWriter(t *testing.T) {
	e, sel, plan := f2Fixture(t)
	lock := &sync.Mutex{}
	e.m.AddLock = lock
	old := reffence.HoldWait
	reffence.HoldWait = 300 * time.Millisecond
	t.Cleanup(func() { reffence.HoldWait = old })
	_, _, release, err := reffence.Begin(context.Background(), e.st, e.now)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	type result struct {
		plan *DeletePlan
		err  error
	}
	done := make(chan result, 1)
	go func() {
		p, err := e.m.Delete(context.Background(), sel, plan.Token, "admin")
		done <- result{p, err}
	}()
	time.Sleep(150 * time.Millisecond)
	if !lock.TryLock() {
		t.Error("the delete holds the router's add lock while it waits for a link write")
	} else {
		lock.Unlock()
	}
	var r result
	select {
	case r = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the delete still waits for the stuck link write")
	}
	if r.err != nil {
		t.Fatal(r.err)
	}
	if e.ard.deleted != 0 || r.plan.Items[0].Outcome != OutSkipped ||
		!strings.Contains(r.plan.Items[0].Error, "link write is still in progress") {
		t.Fatalf("deleted %d: %+v", e.ard.deleted, r.plan.Items)
	}
}
