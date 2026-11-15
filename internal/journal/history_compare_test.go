package journal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/demo"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// historyAll reads every row of a History reader, page by page.
func historyAll(ctx context.Context, read func(context.Context, Filter) (*HistoryPage, error)) ([]*HistoryRow, error) {
	var out []*HistoryRow
	f := Filter{Limit: 500}
	for {
		p, err := read(ctx, f)
		if err != nil {
			return nil, err
		}
		out = append(out, p.Items...)
		if p.NextCursor == "" {
			return out, nil
		}
		f.Cursor = p.NextCursor
	}
}

// historyKey is a row as compared: every field, episodes in order.
func historyKey(h *HistoryRow) string {
	x := *h
	x.Episodes = slices.Clone(h.Episodes)
	slices.Sort(x.Episodes)
	if len(x.Episodes) == 0 {
		x.Episodes = nil
	}
	b, _ := json.Marshal(x)
	return string(b)
}

// writeHistory writes every History row with the event writers, as each source's events would have: a download's
// grab, failure and import, each library action in order, each blocklist entry and removal. Copies have no event
// writer any more: their rows come from the backfill.
func writeHistory(t *testing.T, ctx context.Context, st *store.Store) {
	t.Helper()
	db := st.Writer()
	if _, err := db.ExecContext(ctx, `DELETE FROM history`); err != nil {
		t.Fatal(err)
	}
	if err := store.BackfillHistory(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM history WHERE ref NOT LIKE 'copy:%'`); err != nil {
		t.Fatal(err)
	}
	ids := func(q string) []int64 {
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			out = append(out, id)
		}
		return out
	}
	for _, id := range ids(`SELECT id FROM download ORDER BY id`) {
		store.HistoryGrab(ctx, db, id)
		store.HistoryFailure(ctx, db, id)
		store.HistoryImport(ctx, db, id)
		store.HistoryHeld(ctx, db, id)
	}
	type action struct {
		id, dl             int64
		kind, mode, detail string
	}
	var acts []action
	rows, err := db.QueryContext(ctx, `SELECT id, kind, mode, COALESCE(download_id, 0), detail FROM library_action
		ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a action
		if err := rows.Scan(&a.id, &a.kind, &a.mode, &a.dl, &a.detail); err != nil {
			t.Fatal(err)
		}
		acts = append(acts, a)
	}
	rows.Close()
	for _, a := range acts {
		store.HistoryLibraryAction(ctx, db, a.id, a.kind, a.mode, a.dl, a.detail)
	}
	rows, err = db.QueryContext(ctx, `SELECT version_key, info_hash FROM router_blocklist`)
	if err != nil {
		t.Fatal(err)
	}
	var keys [][2]string
	for rows.Next() {
		var k [2]string
		if err := rows.Scan(&k[0], &k[1]); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	rows.Close()
	for _, k := range keys {
		store.HistoryBlocklist(ctx, db, k[0], k[1])
	}
	for _, id := range ids(`SELECT id FROM journal_action WHERE action IN ('remove_title', 'remove_version')`) {
		store.HistoryRemoval(ctx, db, id)
	}
}

// checkStoredHistory checks that the event writers (writeHistory) and migration 00140's backfill
// (store.BackfillHistory) store the same History, row for row; it returns the rows.
func checkStoredHistory(t *testing.T, ctx context.Context, j *Journal, st *store.Store) []*HistoryRow {
	t.Helper()
	writeHistory(t, ctx, st)
	written, err := historyAll(ctx, j.History)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().ExecContext(ctx, `DELETE FROM history`); err != nil {
		t.Fatal(err)
	}
	if err := store.BackfillHistory(ctx, st.Writer()); err != nil {
		t.Fatal(err)
	}
	migrated, err := historyAll(ctx, j.History)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != len(migrated) {
		t.Fatalf("writers stored %d rows, the migration %d", len(written), len(migrated))
	}
	for i := range written {
		if a, b := historyKey(written[i]), historyKey(migrated[i]); a != b {
			t.Errorf("row %d:\n  written  %s\n  migrated %s", i, a, b)
		}
	}
	for i := 1; i < len(migrated); i++ {
		if a, b := migrated[i-1], migrated[i]; a.At.Before(b.At) || (a.At.Equal(b.At) && a.ID < b.ID) {
			t.Errorf("rows out of order at %s, %s", a.ID, b.ID)
		}
	}
	return migrated
}

// The demo database's History: the writers and the migration's backfill store the same rows, and the demo has a row
// of each kind of event the writers write.
func TestStoredHistoryDemo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("..", "..", "examples", "policy"))); err != nil {
		t.Fatal(err)
	}
	overlay, _ := filepath.Glob(filepath.Join("..", "..", "examples", "demo", "*.yaml"))
	for _, f := range overlay {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(f)), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pol, err := policy.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	if _, err := demo.Seed(ctx, st, demo.Options{Policy: pol, Seed: 1, Now: now, Logger: log}); err != nil {
		t.Fatal(err)
	}
	j := New(Config{Store: st, Policy: pol, Now: func() time.Time { return now }, Logger: log})
	rows := checkStoredHistory(t, ctx, j, st)
	var events []string
	for _, h := range rows {
		if !slices.Contains(events, h.Event) {
			events = append(events, h.Event)
		}
	}
	slices.Sort(events)
	t.Logf("events: %v", events)
	for _, ev := range []string{EvGrabbed, EvImported, EvFailed, EvBlocked, EvDeleted, EvAdopted, EvMissing} {
		if !slices.Contains(events, ev) {
			t.Errorf("the demo has no %s row", ev)
		}
	}
}

// A pack that replaced three single-episode files is one upgrade row listing the three files, written by the event
// writer as each replace is recorded.
func TestStoredHistoryUpgradeIsOneRow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	at := testNow.Add(-2 * time.Hour)
	e.dlEvent(60, "imported", at, 3, 3, "tv-2160p", hashA, "Gamma.S01.2160p.REMUX", "Remux-2160p", 5000, 30<<30,
		[]int64{101, 102, 103}, "")
	for i := int64(1); i <= 3; i++ {
		e.exec(`INSERT INTO library_action (id, at, kind, mode, version_key, download_id, path, detail, release_title,
			quality, score, title_id, episodes) VALUES (?, ?, 'replace', 'applied', 'tv-2160p', 60, ?, 'moved aside', ?,
			'WEBDL-1080p', 100, 3, ?)`, 80+i, msOf(at.Add(time.Duration(i)*time.Minute)),
			fmt.Sprintf("/tv/G/S01E0%d.mkv", i), fmt.Sprintf("Gamma.S01E0%d.1080p.WEB", i), fmt.Sprintf("[%d]", 100+i))
		store.HistoryLibraryAction(ctx, e.st.Writer(), 80+i, "replace", "applied", 60, "moved aside")
	}
	p, err := e.j.History(ctx, Filter{Events: []string{EvDeleted}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 {
		t.Fatalf("upgrade rows %d", len(p.Items))
	}
	h := p.Items[0]
	var files []string
	for _, f := range h.Replaced {
		files = append(files, f.Path+" "+f.Release.Title+" "+strings.Join(f.Episodes, ","))
	}
	if h.ID != "lib:81" || h.Files != 3 || h.Release != nil || h.ReplacedBy == nil ||
		h.ReplacedBy.Title != "Gamma.S01.2160p.REMUX" || strings.Join(h.Episodes, ",") != "S01E01,S01E02,S01E03" ||
		!h.At.Equal(at.Add(3*time.Minute)) || strings.Join(files, "|") != "/tv/G/S01E01.mkv Gamma.S01E01.1080p.WEB "+
		"S01E01|/tv/G/S01E02.mkv Gamma.S01E02.1080p.WEB S01E02|/tv/G/S01E03.mkv Gamma.S01E03.1080p.WEB S01E03" {
		t.Errorf("upgrade row %s %v", historyKey(h), files)
	}
	checkStoredHistory(t, ctx, e.j, e.st)
	// A row names its journal entry while the entry's source exists; once retention deleted the download, not.
	entries := func() string {
		p, err := e.j.History(ctx, Filter{})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, h := range p.Items {
			out = append(out, h.ID+"="+h.Entry)
		}
		return strings.Join(out, " ")
	}
	if got := entries(); got != "lib:81=lib:81 dl:60=dl:60" {
		t.Errorf("entries %s", got)
	}
	e.exec(`DELETE FROM download WHERE id = 60`)
	if got := entries(); got != "lib:81=lib:81 dl:60=" {
		t.Errorf("entries after retention %s", got)
	}
}
