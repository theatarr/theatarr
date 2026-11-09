package retention

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// Only a live claim or an import still in progress keeps an old failed download; a finished import
// record (failed, rejected, linked, dry run) does not keep it for good.
func TestPruneFailedDownloadImportStates(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	now := time.Date(2026, 10, 29, 3, 0, 0, 0, time.UTC)
	old := store.Millis(now.Add(-400 * 24 * time.Hour))
	title := exec(t, st, `INSERT INTO title (kind, name, created_at, updated_at) VALUES ('movie', 'M', 0, 0)`)
	version := exec(t, st, `INSERT INTO version (title_id, policy_key, created_at, updated_at) VALUES (?, 'movie-web', 0, 0)`, title)
	download := func() int64 {
		return exec(t, st, `INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state, created_at, updated_at)
			VALUES (?, ?, 'movie-web', 'h', 'r', 'i', 'failed', ?, ?)`, title, version, old, old)
	}
	want, label := map[int64]int{}, map[int64]string{}
	for state, keep := range map[string]int{"failed": 0, "rejected": 0, "linked": 0, "dry_run": 0, "waiting_mount": 1, "partial": 1} {
		id := download()
		exec(t, st, `INSERT INTO library_import (download_id, state, created_at, updated_at) VALUES (?, ?, ?, ?)`, id, state, old, old)
		want[id], label[id] = keep, state
	}
	live, expired := download(), download()
	exec(t, st, `INSERT INTO library_claim (download_id, owner, until) VALUES (?, 'x', ?), (?, 'y', ?)`,
		live, store.Millis(now.Add(time.Minute)), expired, old)
	want[live], want[expired] = 1, 0
	label[live], label[expired] = "live claim", "expired claim"
	if _, err := Prune(context.Background(), st, now, Defaults); err != nil {
		t.Fatal(err)
	}
	for id, keep := range want {
		if got := count(t, st, `SELECT COUNT(*) FROM download WHERE id = ?`, id); got != keep {
			t.Errorf("download %d (%s) kept %d, want %d", id, label[id], got, keep)
		}
	}
	if got := count(t, st, `SELECT COUNT(*) FROM library_import`); got != 2 {
		t.Errorf("import rows kept %d, want the two in progress", got)
	}
}

// The library actions offer sources still read stay: the applied link and replace actions of a kept
// download, and an applied replace at a live link's path. Dry runs and actions of nothing kept go.
func TestPruneKeepsLibraryActionsInUse(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	now := time.Date(2026, 10, 29, 3, 0, 0, 0, time.UTC)
	old := store.Millis(now.Add(-400 * 24 * time.Hour))
	title := exec(t, st, `INSERT INTO title (kind, name, created_at, updated_at) VALUES ('movie', 'M', 0, 0)`)
	version := exec(t, st, `INSERT INTO version (title_id, policy_key, created_at, updated_at) VALUES (?, 'movie-web', 0, 0)`, title)
	dl := exec(t, st, `INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state, created_at, updated_at)
		VALUES (?, ?, 'movie-web', 'h', 'r', 'i', 'imported', ?, ?)`, title, version, old, old)
	exec(t, st, `INSERT INTO link (path, target, title_id, download_id, created_at, updated_at) VALUES ('/l/a.mkv', '/t/a', ?, ?, 0, 0)`, title, dl)
	act := func(kind, mode string, download any, path string) int64 {
		return exec(t, st, `INSERT INTO library_action (at, kind, mode, version_key, download_id, path) VALUES (?, ?, ?, 'movie-web', ?, ?)`,
			old, kind, mode, download, path)
	}
	want := map[int64]int{
		act("link", "applied", dl, "/l/a.mkv"):        1,
		act("replace", "applied", dl, "/l/old.mkv"):   1,
		act("replace", "applied", nil, "/l/a.mkv"):    1,
		act("link", "dry_run", dl, "/l/a.mkv"):        0,
		act("link", "applied", 999, "/l/gone.mkv"):    0,
		act("replace", "applied", nil, "/l/gone.mkv"): 0,
		act("scan", "applied", nil, ""):               0,
	}
	if _, err := Prune(context.Background(), st, now, Defaults); err != nil {
		t.Fatal(err)
	}
	for id, keep := range want {
		if got := count(t, st, `SELECT COUNT(*) FROM library_action WHERE id = ?`, id); got != keep {
			t.Errorf("library action %d kept %d, want %d", id, got, keep)
		}
	}
}

// The candidate scans of the download and library_action rules look kept rows up by index; a full
// scan of a referring table per candidate made a night with 20k blocklisted failed downloads hold the writer for
// seconds.
func TestPruneRulesUseIndexes(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	scan := regexp.MustCompile(`^SCAN (\S+)`)
	for _, r := range rules {
		if r.table != "download" && r.table != "library_action" {
			continue
		}
		q := `EXPLAIN QUERY PLAN SELECT rowid FROM ` + r.table + ` WHERE ` + r.where
		args := []any{0}
		if strings.Contains(r.where, "?2") {
			args = append(args, 0)
		}
		rows, err := st.Reader().Query(q, args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
			if m := scan.FindStringSubmatch(detail); m != nil && m[1] != r.table && m[1] != "job" {
				t.Errorf("%s: the rule scans %s: %s", r.table, m[1], detail)
			}
		}
		rows.Close()
		t.Logf("%s:\n%s", r.table, strings.Join(plan, "\n"))
	}
}
