package torrents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/torrents/reffence"
)

// f2Fixture: two unused torrents confirmed for deletion, F (rd 6) and G (rd 9).
func f2Fixture(t *testing.T) (*env, []DeleteSelection, *DeletePlan) {
	t.Helper()
	e := deleteFixture(t)
	e.rd.list = append(e.rd.list, tor("9", hG, "Movie.G.2010.1080p.WEB-DL", 900, e.now.Add(-72*time.Hour),
		provider.StateDownloaded))
	e.content(hG, 1)
	sel := []DeleteSelection{{Provider: "rd", ID: "6", Hash: hF}, {Provider: "rd", ID: "9", Hash: hG}}
	plan, err := e.m.PlanDelete(context.Background(), sel)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Count != 2 {
		t.Fatalf("plan: %+v", plan.Items)
	}
	return e, sel, plan
}

const linkToG = `INSERT INTO link (path, target, title_id, created_at, updated_at) VALUES ('/lib/G/G.mkv', ?, 1, 0, 0)`

// A library link to the second torrent written while the first one's delete runs stops the second delete:
// each item's usage is checked right before its own delete, not once for the batch.
func TestDeleteRechecksEachItemBeforeItsDelete(t *testing.T) {
	t.Parallel()
	e, sel, plan := f2Fixture(t)
	first := true
	e.ard.onDelete = func() {
		if first {
			first = false
			e.exec(linkToG, mountPath+"/__all__/Movie.G.2010.1080p.WEB-DL/G.mkv")
		}
	}
	done, err := e.m.Delete(context.Background(), sel, plan.Token, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if e.ard.deleted != 1 || done.Items[0].Outcome != "deleted" || done.Items[1].Outcome != OutSkipped ||
		!strings.Contains(done.Items[1].Error, "1 link") {
		t.Fatalf("deleted %d: %+v", e.ard.deleted, done.Items)
	}
}

// A reference writer of this process (library links, arr files) waits on the fence while a delete is between its
// last usage check and the provider's answer, and the next item's check sees what it wrote.
func TestDeleteFencesReferenceWriters(t *testing.T) {
	e, sel, plan := f2Fixture(t)
	wrote := make(chan error, 1)
	first := true
	e.ard.onDelete = func() {
		if !first {
			return
		}
		first = false
		go func() {
			wrote <- reffence.Write(context.Background(), func() error {
				_, err := e.st.Writer().Exec(linkToG, mountPath+"/__all__/Movie.G.2010.1080p.WEB-DL/G.mkv")
				return err
			})
		}()
		select {
		case err := <-wrote:
			t.Errorf("the writer did not wait for the delete in flight (%v)", err)
			wrote <- err
		case <-time.After(100 * time.Millisecond):
		}
	}
	done, err := e.m.Delete(context.Background(), sel, plan.Token, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := <-wrote; err != nil && !t.Failed() {
		t.Fatal(err)
	}
	if e.ard.deleted != 1 || done.Items[1].Outcome != OutSkipped {
		t.Fatalf("deleted %d: %+v", e.ard.deleted, done.Items)
	}
}

// Across processes the delete holds a lease: while another process holds it nothing is deleted, and a writer's
// transaction refuses while this process's delete holds it in another's eyes.
func TestDeleteFenceLeaseAcrossProcesses(t *testing.T) {
	t.Parallel()
	e, sel, plan := f2Fixture(t)
	ctx := context.Background()
	e.exec(`INSERT INTO library_lease (name, owner, until) VALUES (?, 'pid 1 at 1', ?)`, reffence.LeaseName,
		store.Millis(e.now.Add(time.Hour)))
	done, err := e.m.Delete(ctx, sel, plan.Token, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if e.ard.deleted != 0 || done.Items[0].Outcome != OutSkipped ||
		!strings.Contains(done.Items[0].Error, "another Theatarr process") {
		t.Fatalf("deleted %d: %+v", e.ard.deleted, done.Items)
	}
	tx, err := e.st.Writer().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := reffence.CheckTx(ctx, tx, e.now); !errors.Is(err, reffence.ErrDeleting) {
		t.Errorf("a writer while another process deletes: %v", err)
	}
	tx.Rollback()
	// An expired lease (a crash) is taken over; this process's own lease does not stop its writers' check.
	e.exec(`UPDATE library_lease SET until = ? WHERE name = ?`, store.Millis(e.now.Add(-time.Second)),
		reffence.LeaseName)
	release, err := reffence.Hold(ctx, e.st, e.now)
	if err != nil {
		t.Fatal(err)
	}
	tx, err = e.st.Writer().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := reffence.CheckTx(ctx, tx, e.now); err != nil {
		t.Errorf("our own lease: %v", err)
	}
	tx.Rollback()
	release()
	var n int
	if err := e.st.Reader().QueryRow(`SELECT count(*) FROM library_lease WHERE name = ?`,
		reffence.LeaseName).Scan(&n); err != nil || n != 0 {
		t.Fatalf("lease left after release: %d %v", n, err)
	}
}

// A backup that reads a Real-Debrid torrent while it awaits its file selection records the selection as unknown and
// does not cache it; once one episode is selected, a pre-delete backup reads it again (a needed torrent is read
// whatever an earlier backup holds) and holds that episode alone.
func TestBackupRefreshesSelectionBeforeDelete(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	old := e.now.Add(-72 * time.Hour)
	e.rd.list = []provider.Torrent{{ID: "1", Hash: hA, Name: "Show.S01", Bytes: 10, Added: old,
		Status: rdWaitingFiles}}
	e.rd.detail = map[string]*provider.Torrent{"1": {ID: "1", Hash: hA, Status: rdWaitingFiles,
		Files: []provider.File{{ID: "1", Path: "/S/e1.mkv"}, {ID: "2", Path: "/S/e2.mkv"}}}}
	b1, _, err := e.m.TakeBackup(ctx, BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := b1.Providers[0].Torrents[0]; got.FilesKnown || !got.SelectionUnknown || selectionOf(got) != SelUnknown {
		t.Fatalf("a torrent awaiting its selection: %+v", got)
	}
	// Episode 2 is selected; the list no longer says waiting.
	e.rd.list[0].Status, e.rd.list[0].State = "downloaded", provider.StateDownloaded
	e.rd.detail["1"] = &provider.Torrent{ID: "1", Hash: hA, Status: "downloaded",
		Files: []provider.File{{ID: "1", Path: "/S/e1.mkv"}, {ID: "2", Path: "/S/e2.mkv", Selected: true}}}
	e.now = e.now.Add(time.Hour)
	b2, _, err := e.m.TakeBackup(ctx, BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := b2.Providers[0].Torrents[0]; !got.FilesKnown || len(got.Files) != 1 || got.Files[0].Path != "/S/e2.mkv" {
		t.Fatalf("the unknown selection is not reused: %+v", got)
	}
	// Selected on the site again (episode 1 too): a cached selection is reread for a delete, not for a schedule.
	e.rd.detail["1"].Files[0].Selected = true
	reads := e.rd.reads
	e.now = e.now.Add(time.Hour)
	if _, _, err := e.m.TakeBackup(ctx, BackupOptions{}); err != nil || e.rd.reads != reads {
		t.Fatalf("a scheduled backup reuses the settled selection: %v, %d reads", err, e.rd.reads-reads)
	}
	e.now = e.now.Add(time.Hour)
	b4, _, err := e.m.TakeBackup(ctx, BackupOptions{Reason: ReasonPreDelete,
		Need: map[string]map[string]bool{"rd": {"1": true}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := b4.Providers[0].Torrents[0]; len(got.Files) != 2 || e.rd.reads != reads+1 {
		t.Fatalf("the pre-delete backup rereads the selection: %+v, %d reads", got, e.rd.reads-reads)
	}
}

// A version 1 backup's empty known selection reads as unknown: it is not cached, and a restore never takes it as
// every file.
func TestBackupV1EmptySelectionIsUnknown(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	e.rd.list = []provider.Torrent{tor("1", hA, "A", 10, e.now.Add(-72*time.Hour), provider.StateDownloaded)}
	id := "20261028T120000.000Z-manual"
	doc := `{"format":"theatarr-torrents-backup","version":1,"id":"` + id + `","created_at":"2026-10-28T12:00:00Z",` +
		`"reason":"manual","providers":[{"provider":"rd","ok":true,"torrents":[{"id":"1","hash":"` + hA +
		`","name":"A","files_known":true}]}]}`
	if err := os.MkdirAll(e.m.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.m.Dir, id+".json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := e.m.ReadBackup(id)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Providers[0].Torrents[0]; got.FilesKnown || !got.SelectionUnknown {
		t.Fatalf("v1 empty selection: %+v", got)
	}
	if c := e.m.fileCache(); len(c) != 0 {
		t.Fatalf("cached: %+v", c)
	}
	e.rd.list = nil
	plan, err := e.m.PlanRestore(ctx, RestoreRequest{BackupID: id})
	if err != nil || plan.Items[0].Selection != SelUnknown {
		t.Fatalf("restore selection: %+v %v", plan, err)
	}
	list, err := e.m.Backups()
	if err != nil || len(list) != 1 || list[0].Torrents != 1 || list[0].Bytes != 0 {
		t.Fatalf("listing: %+v %v", list, err)
	}
}

// restoreFixture: a backup of rd with A (two files, e2 selected), B (awaiting its selection) and C (files never
// read), all gone from rd since.
func restoreFixture(t *testing.T) (*env, RestoreRequest) {
	t.Helper()
	e := newEnv(t)
	ctx := context.Background()
	old := e.now.Add(-72 * time.Hour)
	e.rd.list = []provider.Torrent{tor("1", hA, "A", 1, old, provider.StateDownloaded),
		{ID: "2", Hash: hB, Name: "B", Added: old, Status: rdWaitingFiles}}
	e.rd.detail = map[string]*provider.Torrent{
		"1": {ID: "1", Hash: hA, Files: []provider.File{{ID: "1", Path: "/A/e1.mkv"},
			{ID: "2", Path: "/A/e2.mkv", Selected: true}}},
		"2": {ID: "2", Hash: hB, Status: rdWaitingFiles, Files: []provider.File{{ID: "1", Path: "/B/b.mkv"}}}}
	b, _, err := e.m.TakeBackup(ctx, BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	e.rd.list = nil
	e.ard.onAdd = func(id, hash string) {
		files := []provider.File{{ID: "7", Path: "/X/e1.mkv"}, {ID: "8", Path: "/X/e2.mkv"}}
		switch hash {
		case hA:
			files = []provider.File{{ID: "7", Path: "/A/e1.mkv"}, {ID: "8", Path: "/A/e2.mkv"}}
		case hB:
			files = []provider.File{{ID: "7", Path: "/B/b.mkv"}}
		}
		e.rd.detail[id] = &provider.Torrent{ID: id, Hash: hash, Status: rdWaitingFiles, Files: files}
		e.rd.list = append(e.rd.list, provider.Torrent{ID: id, Hash: hash, Status: rdWaitingFiles})
	}
	return e, RestoreRequest{BackupID: b.ID}
}

// A restore selects the backup's files; an unknown selection (awaited when backed up) is added but never taken as
// every file: incomplete, for the person to select; a backup that never read the files selects every file, as the
// preview says.
func TestRestoreSelections(t *testing.T) {
	t.Parallel()
	e, req := restoreFixture(t)
	ctx := context.Background()
	plan, err := e.m.PlanRestore(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	sel := map[string]string{}
	for _, it := range plan.Items {
		sel[it.Name] = it.Selection
	}
	if sel["A"] != SelFiles || sel["B"] != SelUnknown {
		t.Fatalf("selections: %v", sel)
	}
	got, err := e.m.Restore(ctx, req, plan.Confirmed(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	if s := e.ard.selected["new-aaaa"]; len(s) != 1 || s[0] != "8" {
		t.Errorf("A selects e2: %v", s)
	}
	if s, ok := e.ard.selected["new-bbbb"]; ok {
		t.Errorf("B's unknown selection was taken as %v", s)
	}
	out := map[string]RestoreItem{}
	for _, it := range got.Items {
		out[it.Name] = it
	}
	if out["A"].Outcome != OutUncached || out["B"].Outcome != OutIncomplete ||
		!strings.Contains(out["B"].Error, "select its files") {
		t.Fatalf("outcomes: %+v", got.Items)
	}
	if err := restoreJobError(got, 2); err == nil || !strings.Contains(err.Error(), "1 of 2 still wait") {
		t.Fatalf("the job fails visibly: %v", err)
	}
	// A DMM import (files never read) selects every file; one whose files match nothing is left incomplete.
	b2, _, err := e.m.ImportBackup(ctx, []byte(`[{"filename":"C","hash":"`+hC+`"}]`), "rd", "admin")
	if err != nil {
		t.Fatal(err)
	}
	got, err = e.m.Restore(ctx, RestoreRequest{BackupID: b2.ID}, []string{"rd|" + hC}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if s := e.ard.selected["new-cccc"]; len(s) != 2 || got.Items[0].Selection != SelAll {
		t.Errorf("C (never read) selects every file: %v %+v", s, got.Items[0])
	}
	if ids := pickFiles([]provider.File{{ID: "1", Path: "x.mkv"}}, []BackupFile{{Path: "y.mkv"}}, false); len(ids) != 0 {
		t.Errorf("no match is no selection, not every file: %v", ids)
	}
}

// A failed detail read right after the add leaves the restore's own torrent pending: the outcome is
// incomplete and fails the job; a restore again resumes the selection on that torrent instead of skipping it as
// present.
func TestRestoreResumesPendingSelection(t *testing.T) {
	t.Parallel()
	e, req := restoreFixture(t)
	ctx := context.Background()
	failOnce := false
	e.rd.onDetail = func() {
		if failOnce {
			failOnce = false
			e.rd.detailErr = errors.New("rd: 503")
		} else {
			e.rd.detailErr = nil
		}
	}
	e.ard.onAdd = func(id, hash string) {
		e.rd.detail[id] = &provider.Torrent{ID: id, Hash: hash, Status: rdWaitingFiles,
			Files: []provider.File{{ID: "7", Path: "/A/e1.mkv"}, {ID: "8", Path: "/A/e2.mkv"}}}
		e.rd.list = append(e.rd.list, provider.Torrent{ID: id, Hash: hash, Status: rdWaitingFiles})
		failOnce = true
	}
	req.Hashes = []string{hA}
	plan, err := e.m.PlanRestore(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.m.Restore(ctx, req, plan.Confirmed(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	if it := got.Items[0]; it.Outcome != OutIncomplete || !strings.Contains(it.Error, "restore it again") {
		t.Fatalf("after a failed read: %+v", it)
	}
	if len(e.ard.selected) != 0 || restoreJobError(got, 1) == nil {
		t.Fatalf("selected %v; the job must fail", e.ard.selected)
	}
	plan, err = e.m.PlanRestore(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if it := plan.Items[0]; it.Action != ActResume || it.NewID != "new-aaaa" || plan.Resume != 1 ||
		len(plan.Confirmed()) != 1 {
		t.Fatalf("retry plan: %+v", plan)
	}
	got, err = e.m.Restore(ctx, req, plan.Confirmed(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	if s := e.ard.selected["new-aaaa"]; len(s) != 1 || s[0] != "8" || got.Items[0].Outcome != OutUncached ||
		len(e.ard.added) != 1 {
		t.Fatalf("resumed: selected %v, %+v, added %v", e.ard.selected, got.Items[0], e.ard.added)
	}
	if p, _ := e.m.pendingSelections(ctx); len(p) != 0 {
		t.Fatalf("pending left: %+v", p)
	}
	if plan, _ = e.m.PlanRestore(ctx, req); plan.Items[0].Action != ActPresent {
		t.Fatalf("done: %+v", plan.Items[0])
	}
}

// A torrent still awaiting its selection can be deleted: the pre-delete backup knows that state exactly.
func TestDeleteTorrentAwaitingSelection(t *testing.T) {
	t.Parallel()
	e := deleteFixture(t)
	ctx := context.Background()
	e.rd.list = append(e.rd.list, provider.Torrent{ID: "9", Hash: hG, Name: "Movie.G.2010.1080p.WEB-DL", Bytes: 900,
		Added: e.now.Add(-72 * time.Hour), Status: rdWaitingFiles})
	e.content(hG, 1)
	e.rd.detail = map[string]*provider.Torrent{"9": {ID: "9", Hash: hG, Status: rdWaitingFiles,
		Files: []provider.File{{ID: "1", Path: "/G/g.mkv"}}}}
	sel := []DeleteSelection{{Provider: "rd", ID: "9", Hash: hG}}
	plan, err := e.m.PlanDelete(ctx, sel)
	if err != nil || plan.Count != 1 {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	done, err := e.m.Delete(ctx, sel, plan.Token, "admin")
	if err != nil || done.Deleted != 1 {
		t.Fatalf("delete: %+v %v", done, err)
	}
	b, err := e.m.ReadBackup(done.BackupID)
	if err != nil {
		t.Fatal(err)
	}
	for _, bt := range b.Providers[0].Torrents {
		if bt.ID == "9" && (!bt.SelectionUnknown || bt.FilesKnown) {
			t.Fatalf("backup: %+v", bt)
		}
	}
}
