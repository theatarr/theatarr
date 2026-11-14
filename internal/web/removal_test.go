package web

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/torrents"
)

// A manager's removal with the files runs the debrid clean-up after the answer, on the service's context, and its
// outcome lands in the Removed event; without the files nothing is unlinked or cleaned up and the answer says the
// files and torrents stay. (The deletes themselves: torrents TestDeleteForRemoval; this provider has no write
// client, so its delete fails and the event says so.)
func TestRemoveRunsTheCleanupAfterTheAnswer(t *testing.T) {
	t.Parallel()
	const hb, hz = "b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0", "c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0"
	for _, unlink := range []bool{true, false} {
		old := testNow.Add(-72 * time.Hour)
		prov := &torrentProvider{list: []provider.Torrent{
			{ID: "20", Hash: hb, Name: "Beta.2021.2160p.WEB", Bytes: 100, Added: old, State: provider.StateDownloaded},
			{ID: "21", Hash: hz, Name: "Unrelated.2020.1080p", Bytes: 100, Added: old, State: provider.StateDownloaded},
		}}
		pol := &policy.Policy{Providers: map[string]*policy.Provider{"rd": {Kind: provider.KindRealDebrid, Order: 1}}}
		m := &torrents.Manager{Policy: pol, Providers: map[string]provider.Provider{"rd": prov},
			Dir: filepath.Join(t.TempDir(), "torrent-backups"), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
			Now: func() time.Time { return testNow }}
		m.Syncer = &provider.Syncer{Providers: m.Providers, Configured: []string{"rd"}, Now: m.Now, Log: m.Log}
		bg, cancel := context.WithCancel(context.Background())
		o := &opsEnv{ops: &fakeOps{}, acq: &opsAcquirer{}}
		o.env = newEnvWith(t, func(c *Config) {
			c.Ops, c.Add, c.Torrents, c.Background = o.ops, AddConfig{Acquirer: o.acq}, torrentSource{m}, bg
		})
		m.Store, m.Syncer.Store = o.st, o.st
		o.mc, o.mcsrf = o.session(o.account("mgr", auth.RoleManager, auth.StatusActive, false))
		o.ops.onUnlinkRemoved = func(id int64) { o.exec(`UPDATE link SET state = 'unlinked' WHERE id = ?`, id) }
		now := store.Millis(testNow)
		o.exec(`INSERT INTO content (info_hash, first_seen_at, last_seen_at) VALUES (?, 0, 0), (?, 0, 0)`, hb, hz)
		o.exec(`INSERT INTO provider_sync_run (provider, started_at, finished_at, ok) VALUES ('rd', 0, 0, 1)`)
		o.exec(`INSERT INTO link (id, path, target, kind, title_id, state, file_metadata, created_at, updated_at) VALUES
			(5, '/movies/Beta (2021)/Beta.mkv', '/mnt/zurg/__all__/Beta.2021.2160p.WEB/b.mkv', 'symlink', 2, 'active', ?,
			?, ?)`, `{"InfoHash":"`+hb+`"}`, now, now)
		o.exec(`INSERT INTO link_version (link_id, version_id) VALUES (5, 3)`)
		// Theatarr's own add of it (own_add, this account epoch): the clean-up may take it.
		o.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
			provider, provider_torrent_id, own_add, provider_epoch, created_at, updated_at) VALUES (2, 3, 'movie-web', ?,
			'Beta', 'idx', 'imported', 'rd', '20', 1, 0, ?, ?)`, hb, now, now)

		var got RemoveResult
		if code := o.jsonCall("POST", APIBase+"/titles/2/remove", RemoveRequest{Unlink: unlink}, o.mc, o.mcsrf,
			&got); code != 200 || !got.Title || got.Cleanup != unlink {
			t.Fatalf("remove (files %v): %d %+v", unlink, code, got)
		}
		// The request's context is over; the service's runs the clean-up on.
		o.web.cleanups.Wait()
		cancel()
		var detail string
		if err := o.st.Reader().QueryRow(`SELECT detail FROM journal_action WHERE action = ?`,
			journal.ActRemoveTitle).Scan(&detail); err != nil {
			t.Fatal(err)
		}
		rm := journal.ParseRemovalDetail(detail)
		tried := journalTorrentDeletes(t, o)
		if unlink {
			if !strings.Contains(got.Message, "1 library file deleted") ||
				!strings.Contains(got.Message, "debrid torrents are being cleaned up") ||
				rm.Files != journal.RemovalFilesDeleted || !strings.HasPrefix(rm.Cleanup, "RD: failed") ||
				strings.Join(tried, ",") != "rd/20" {
				t.Errorf("with files: %q, %+v, tried %v", got.Message, rm, tried)
			}
			// The arr file at the unlinked path went with it.
			if n := o.count(`SELECT COUNT(*) FROM version_file WHERE version_id = 3`); n != 0 {
				t.Errorf("arr files left: %d", n)
			}
		} else if !strings.Contains(got.Message, "its files stay linked and its debrid torrents stay") ||
			rm.Files != journal.RemovalFilesKept || rm.Cleanup != "" || len(tried) != 0 || len(o.ops.unlinks) != 0 {
			t.Errorf("without files: %q, %+v, tried %v, unlinks %v", got.Message, rm, tried, o.ops.unlinks)
		}
	}
}

// journalTorrentDeletes are the torrent deletes tried, as provider/id.
func journalTorrentDeletes(t *testing.T, o *opsEnv) []string {
	t.Helper()
	rows, err := o.st.Reader().Query(`SELECT provider || '/' || torrent_id FROM torrent_journal WHERE action = 'delete'
		ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// "Delete the files" without "Delete it from the debrid providers": the links go, the torrents stay and the event
// says so. The torrents cannot go without the files (400).
func TestRemoveFilesKeepsTorrentsWhenAsked(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	const hb = "b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0"
	now := store.Millis(testNow)
	o.exec(`INSERT INTO link (id, path, target, kind, title_id, state, file_metadata, created_at, updated_at) VALUES
		(5, '/movies/Beta (2021)/Beta.mkv', '/mnt/zurg/__all__/Beta/b.mkv', 'symlink', 2, 'active', ?, ?, ?)`,
		`{"InfoHash":"`+hb+`"}`, now, now)
	o.exec(`INSERT INTO link_version (link_id, version_id) VALUES (5, 3)`)
	yes, no := true, false
	if code := o.jsonCall("POST", APIBase+"/titles/2/remove", RemoveRequest{Debrid: &yes}, o.mc, o.mcsrf,
		nil); code != 400 {
		t.Errorf("torrents without the files: %d", code)
	}
	var got RemoveResult
	if code := o.jsonCall("POST", APIBase+"/titles/2/remove", RemoveRequest{Unlink: true, Debrid: &no}, o.mc, o.mcsrf,
		&got); code != 200 || got.Cleanup || len(got.Unlinked) != 1 ||
		!strings.Contains(got.Message, "its debrid torrents stay") {
		t.Fatalf("files only: %d %+v", code, got)
	}
	o.web.cleanups.Wait()
	var detail string
	if err := o.st.Reader().QueryRow(`SELECT detail FROM journal_action WHERE action = ?`,
		journal.ActRemoveTitle).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if rm := journal.ParseRemovalDetail(detail); rm.Files != journal.RemovalFilesDeleted ||
		!strings.HasPrefix(rm.Cleanup, "debrid torrents kept, as asked") {
		t.Errorf("event %+v", rm)
	}
}

// A dead symlink a removal without the files left is a kept file too: the scope lists it and Delete files takes it.
func TestLeftoverIncludesDeadLinks(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	now := store.Millis(testNow)
	o.exec(`INSERT INTO link (id, path, target, kind, title_id, state, created_at, updated_at) VALUES
		(7, '/lib/Beta/b.mkv', '/mnt/zurg/b.mkv', 'symlink', 2, 'dead', ?, ?)`, now, now)
	o.exec(`INSERT INTO link_version (link_id, version_id) VALUES (7, 3)`)
	if code := o.jsonCall("POST", APIBase+"/titles/2/remove", RemoveRequest{}, o.mc, o.mcsrf, nil); code != 200 {
		t.Fatalf("remove: %d", code)
	}
	var scope RemovalScope
	if code := o.jsonCall("GET", APIBase+"/titles/2/removal", nil, o.mc, "", &scope); code != 200 ||
		len(scope.Leftover) != 1 || scope.Leftover[0].Links != 1 {
		t.Fatalf("scope: %d %+v", code, scope)
	}
	var got RemoveResult
	if code := o.jsonCall("POST", APIBase+"/titles/2/remove", RemoveRequest{Unlink: true}, o.mc, o.mcsrf,
		&got); code != 200 || len(got.Unlinked) != 1 {
		t.Fatalf("delete files: %d %+v", code, got)
	}
}
