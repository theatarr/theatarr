package store

import (
	"context"
	"testing"
)

// 00140 creates history and backfills it once from the sources (a grab, its failure and the blocklist entry here);
// it rolls back cleanly and applies again. The rows themselves are compared with the derived History in
// internal/journal (TestStoredHistoryMatchesDemo).
func TestHistoryMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := Open(ctx, migratedPath(t, 138), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO title (id, kind, name, created_at, updated_at)
		VALUES (1, 'movie', 'M', 0, 0);
		INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'movie-remux', 0, 0);
		INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state, error,
			created_at, updated_at, added_at) VALUES (7, 1, 1, 'movie-remux', 'h7', 'M.2020.Remux', 'x', 'failed',
			'stalled at 0%', 1, 5, 2);
		INSERT INTO router_blocklist (version_key, info_hash, title_id, release_title, reason, source, download_id,
			created_at) VALUES ('movie-remux', 'h7', 1, 'M.2020.Remux', 'stalled', 'stalled', 7, 6)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := p.UpTo(ctx, 140); err != nil {
			t.Fatal(err)
		}
		var rows string
		if err := st.Reader().QueryRowContext(ctx, `SELECT group_concat(ref || ' ' || at || ' ' || event || ' ' || kind,
			', ') FROM (SELECT * FROM history ORDER BY ref)`).Scan(&rows); err != nil ||
			rows != "bl:1 6 blocked blocklist, dl:7 2 grabbed grab, dl:7:fail 5 failed stall" {
			t.Fatalf("backfilled %q %v", rows, err)
		}
		if _, err := p.DownTo(ctx, 138); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'history'`).
			Scan(&n); err != nil || n != 0 {
			t.Fatalf("table left %d: %v", n, err)
		}
	}
}

// A held import keeps its own row when a later import links (both stay, and writing again changes nothing); one whose
// download then fails is held, then failed. An unblocked entry keeps its row when a new entry takes its rowid.
func TestHistoryHeldImportAndUnblock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := Open(ctx, migratedPath(t, 144), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.Writer()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	rows := func() string {
		t.Helper()
		var s string
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(group_concat(ref || ' ' || event || ' ' || reason, ', '), '')
			FROM (SELECT * FROM history ORDER BY ref)`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	exec(`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'M', 0, 0);
		INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'movie-remux', 0, 0);
		INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
			created_at, updated_at) VALUES (7, 1, 1, 'movie-remux', 'h7', 'R7', 'x', 'importing', 1, 1),
			(8, 1, 1, 'movie-remux', 'h8', 'R8', 'x', 'importing', 1, 1);
		INSERT INTO library_import (download_id, state, last_error, created_at, updated_at) VALUES
			(7, 'rejected', 'runtime off', 2, 2), (8, 'rejected', 'sample', 2, 2)`)
	HistoryHeld(ctx, db, 7)
	HistoryHeld(ctx, db, 8)
	exec(`UPDATE library_import SET state = 'linked', last_error = NULL, updated_at = 3 WHERE download_id = 7`)
	for i := 0; i < 2; i++ {
		HistoryImport(ctx, db, 7)
		HistoryHeld(ctx, db, 7)
	}
	exec(`UPDATE download SET state = 'failed', error = 'library: sample', updated_at = 4 WHERE id = 8`)
	HistoryHeld(ctx, db, 8)
	HistoryFailure(ctx, db, 8)
	if got := rows(); got != "dl:7:held failed runtime off, dl:7:import imported , dl:8:fail failed library: sample, "+
		"dl:8:held failed sample" {
		t.Errorf("import rows %q", got)
	}

	exec(`DELETE FROM history`)
	block := func(hash, reason string) {
		exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, release_title, reason, source, created_at)
			VALUES ('movie-remux', ?, 1, 'R', ?, 'queue', 5)`, hash, reason)
		HistoryBlocklist(ctx, db, "movie-remux", hash)
	}
	block("ha", "first")
	HistoryUnblock(ctx, db, "movie-remux", "ha")
	exec(`DELETE FROM router_blocklist WHERE info_hash = 'ha'`)
	block("hb", "second")
	if got := rows(); got != "bl:1 blocked second, bl:1:5 blocked first" {
		t.Errorf("blocked rows %q", got)
	}
}

// 00144 adds the stored flags (unchecked: NULL) and drops verdicts, the error inbox and the copy events (their History
// rows stay, and their alert claims go); it rolls back cleanly.
func TestJournalFlagsMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := Open(ctx, migratedPath(t, 143), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO history (ref, at, event, kind) VALUES
			('copy:1', 1, 'copy_added', 'copy'), ('dl:1:fail', 2, 'failed', 'failure');
		INSERT INTO journal_verdict (entry_id, verdict, reason, action, created_at) VALUES ('dl:1', 'ok', 'x', 'recorded', 0);
		INSERT INTO error_event (fingerprint, source, message, first_seen, last_seen, updated_at) VALUES ('f', 'log', 'm', 0, 0, 0);
		INSERT INTO notify_state (key, value, updated_at) VALUES ('errors_since', '0', 0);
		INSERT INTO notify_event (kind, ref, created_at) VALUES ('error:0', 1, 0), ('digest', 20261115, 0)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := p.UpTo(ctx, 144); err != nil {
			t.Fatal(err)
		}
		for q, want := range map[string]string{
			`SELECT group_concat(ref || ':' || COALESCE(flags, 'null') || ':' || severity, ' ') FROM (SELECT * FROM history ORDER BY ref)`:    "copy:1:null: dl:1:fail:null:",
			`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('journal_verdict', 'error_event', 'error_inbox_state', 'provider_copy_event')`: "0",
			`SELECT COUNT(*) FROM notify_state WHERE key = 'errors_since'`:                                                                    "0",
			`SELECT group_concat(kind) FROM notify_event`:                                                                                     "digest",
		} {
			var got string
			if err := st.Reader().QueryRowContext(ctx, q).Scan(&got); err != nil || got != want {
				t.Errorf("%s: %q %v, want %q", q, got, err, want)
			}
		}
		if _, err := p.DownTo(ctx, 143); err != nil {
			t.Fatal(err)
		}
	}
}
