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

// splitUpgrades turns each stored upgrade row (one per download and library) into the derived reader's rows: one per
// old release, named by its first file's action, dated by its last, with its first file's release.
func splitUpgrades(rows []*HistoryRow) []*HistoryRow {
	var out []*HistoryRow
	for _, h := range rows {
		if h.Event != EvDeleted || len(h.Replaced) == 0 {
			out = append(out, h)
			continue
		}
		var keys []string
		groups := map[string][]ReplacedFile{}
		for _, f := range h.Replaced {
			k := ""
			if f.Release != nil {
				k = f.Release.Title
			}
			if _, ok := groups[k]; !ok {
				keys = append(keys, k)
			}
			groups[k] = append(groups[k], f)
		}
		for _, k := range keys {
			g := groups[k]
			x := *h
			x.ID, x.At, x.Release, x.Files, x.Replaced, x.Episodes = g[0].ID, g[0].At, g[0].Release, len(g), nil, nil
			for _, f := range g {
				if f.At.After(x.At) {
					x.At = f.At
				}
				for _, ep := range f.Episodes {
					if !slices.Contains(x.Episodes, ep) {
						x.Episodes = append(x.Episodes, ep)
					}
				}
			}
			out = append(out, &x)
		}
	}
	return out
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

// compareHistory reads every row of the derived History (History) and of the stored rows (StoredHistory) and returns
// the rows that differ, one line each, plus a summary. A stored upgrade row stands for the derived rows of its old
// releases (splitUpgrades); nothing else may differ.
func compareHistory(ctx context.Context, j *Journal) (summary string, diffs []string, err error) {
	t0 := time.Now()
	old, err := historyAll(ctx, j.History)
	if err != nil {
		return "", nil, err
	}
	t1 := time.Now()
	stored, err := historyAll(ctx, j.StoredHistory)
	if err != nil {
		return "", nil, err
	}
	t2 := time.Now()
	for i := 1; i < len(stored); i++ {
		a, b := stored[i-1], stored[i]
		if a.At.Before(b.At) || (a.At.Equal(b.At) && a.ID < b.ID) {
			diffs = append(diffs, fmt.Sprintf("stored rows out of order at %s, %s", a.ID, b.ID))
		}
	}
	upgrades := 0
	for _, h := range stored {
		if h.Event == EvDeleted {
			upgrades++
		}
	}
	split := splitUpgrades(stored)
	summary = fmt.Sprintf("derived %d rows in %s; stored %d rows in %s (%d upgrade rows, %d rows split by old release)",
		len(old), t1.Sub(t0).Round(time.Millisecond), len(stored), t2.Sub(t1).Round(time.Millisecond), upgrades,
		len(split))
	byID := map[string]*HistoryRow{}
	for _, h := range split {
		if byID[h.ID] != nil {
			diffs = append(diffs, "stored twice: "+h.ID)
		}
		byID[h.ID] = h
	}
	seen := map[string]bool{}
	for _, h := range old {
		seen[h.ID] = true
		s := byID[h.ID]
		switch {
		case s == nil:
			diffs = append(diffs, "only derived: "+historyKey(h))
		case historyKey(s) != historyKey(h):
			diffs = append(diffs, "differs "+h.ID+":\n  derived "+historyKey(h)+"\n  stored  "+historyKey(s))
		}
	}
	for _, h := range split {
		if !seen[h.ID] {
			diffs = append(diffs, "only stored: "+historyKey(h))
		}
	}
	return summary, diffs, nil
}

// checkHistory fails t with every difference between the derived and the stored History.
func checkHistory(t *testing.T, ctx context.Context, j *Journal) []*HistoryRow {
	t.Helper()
	summary, diffs, err := compareHistory(ctx, j)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(summary)
	for i, d := range diffs {
		if i == 200 {
			t.Errorf("... %d more differences", len(diffs)-i)
			break
		}
		t.Error(d)
	}
	if len(diffs) > 0 {
		t.Errorf("%d differences; %s", len(diffs), summary)
	}
	rows, err := historyAll(ctx, j.StoredHistory)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// migrationBackfill runs the backfill statements of migration 00140 on st's sources, after emptying history.
func migrationBackfill(t *testing.T, ctx context.Context, st *store.Store) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "store", "migrations", "00140_history.sql"))
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(strings.SplitN(string(b), "-- +goose Up", 2)[1], "-- +goose Down")
	if _, err := st.Writer().ExecContext(ctx, `DELETE FROM history`); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, q := range strings.Split(up, ";\n") {
		var keep []string
		for _, line := range strings.Split(q, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "--") {
				keep = append(keep, line)
			}
		}
		q = strings.TrimSpace(strings.Join(keep, "\n"))
		if !strings.HasPrefix(q, "INSERT") && !strings.HasPrefix(q, "WITH") {
			continue
		}
		if _, err := st.Writer().ExecContext(ctx, q); err != nil {
			t.Fatalf("migration backfill: %v\n%s", err, q)
		}
		n++
	}
	if n != 10 {
		t.Fatalf("migration backfill: %d statements, want 10", n)
	}
}

// checkStoredHistory compares History with its rows as the writers store them (BackfillHistory, the event writers'
// statements) and as migration 00140 backfills them: all three alike.
func checkStoredHistory(t *testing.T, ctx context.Context, j *Journal, st *store.Store) {
	t.Helper()
	if _, err := st.Writer().ExecContext(ctx, `DELETE FROM history`); err != nil {
		t.Fatal(err)
	}
	if err := store.BackfillHistory(ctx, st.Writer()); err != nil {
		t.Fatal(err)
	}
	written := checkHistory(t, ctx, j)
	migrationBackfill(t, ctx, st)
	migrated := checkHistory(t, ctx, j)
	if len(written) != len(migrated) {
		t.Fatalf("writers stored %d rows, the migration %d", len(written), len(migrated))
	}
	for i := range written {
		if a, b := historyKey(written[i]), historyKey(migrated[i]); a != b {
			t.Errorf("row %d:\n  written  %s\n  migrated %s", i, a, b)
		}
	}
}

// The demo database's History, derived and stored, row for row; the demo seed stores its rows as the writers do.
func TestStoredHistoryMatchesDemo(t *testing.T) {
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
	rows := checkHistory(t, ctx, j)
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
			t.Errorf("the demo has no %s row to compare", ev)
		}
	}
	checkStoredHistory(t, ctx, j, st)
}

// THEATARR_HISTORY_DB=<database file>: the same comparison on a copy of that file (any schema this binary can
// migrate), migrated to this binary's schema, so migration 00140's backfill is what is compared. The file itself is
// not opened.
func TestStoredHistoryMatchesDatabase(t *testing.T) {
	src := os.Getenv("THEATARR_HISTORY_DB")
	if src == "" {
		t.Skip("THEATARR_HISTORY_DB is not set")
	}
	ctx := context.Background()
	dst := filepath.Join(t.TempDir(), "theatarr.db")
	for _, sfx := range []string{"", "-wal"} {
		b, err := os.ReadFile(src + sfx)
		if os.IsNotExist(err) && sfx != "" {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst+sfx, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(ctx, dst, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	t0 := time.Now()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("migrated in %s", time.Since(t0).Round(time.Millisecond))
	checkHistory(t, ctx, New(Config{Store: st, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}))
}

// A pack that replaced three single-episode files is one stored upgrade row listing the three files (the derived
// reader gives one row per old release), written by the event writer as each replace is recorded; and a stored row
// that disagrees with its source is reported.
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
		if err := store.HistoryLibraryAction(ctx, e.st.Writer(), 80+i); err != nil {
			t.Fatal(err)
		}
	}
	derived := e.history(Filter{Events: []string{EvDeleted}})
	if len(derived) != 3 {
		t.Fatalf("derived rows %q", derived)
	}
	p, err := e.j.StoredHistory(ctx, Filter{Events: []string{EvDeleted}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 {
		t.Fatalf("stored rows %d", len(p.Items))
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
	e.exec(`UPDATE history SET quality = 'HDTV-720p' WHERE ref = 'dl:60'`)
	if _, diffs, err := compareHistory(ctx, e.j); err != nil || len(diffs) != 1 ||
		!strings.HasPrefix(diffs[0], "differs dl:60") {
		t.Errorf("a changed row: %q %v", diffs, err)
	}
}
