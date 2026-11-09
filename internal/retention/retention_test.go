package retention

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func openStore(t testing.TB) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), storetest.Seed(t, filepath.Join(t.TempDir(), "r.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

func exec(t *testing.T, st *store.Store, q string, args ...any) int64 {
	t.Helper()
	res, err := st.Writer().Exec(q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	id, _ := res.LastInsertId()
	return id
}

func count(t *testing.T, st *store.Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := st.Reader().QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// Old run logs, finished deliveries, failed downloads, finished acquire runs and audit rows go; the latest
// run per provider, pending deliveries, open downloads and anything a pending job, a link or an offer refers to stay.
func TestPruneKeepsWhatIsStillUsed(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	now := time.Date(2026, 10, 29, 3, 0, 0, 0, time.UTC)
	old := store.Millis(now.Add(-400 * 24 * time.Hour))
	recent := store.Millis(now.Add(-time.Hour))

	// Provider sync runs: rd has three old runs (ok, failed, ok) and one recent; tb only one old one.
	for _, r := range []struct {
		provider string
		ok       int
		at       int64
	}{{"rd", 1, old}, {"rd", 0, old + 1}, {"rd", 1, old + 2}, {"rd", 1, recent}, {"tb", 0, old}} {
		exec(t, st, `INSERT INTO provider_sync_run (provider, started_at, finished_at, ok) VALUES (?, ?, ?, ?)`, r.provider, r.at, r.at, r.ok)
	}
	// Deliveries: old sent, old failed, old pending, recent sent.
	for _, d := range []struct {
		state string
		at    int64
	}{{"sent", old}, {"failed", old}, {"pending", old}, {"sent", recent}} {
		exec(t, st, `INSERT INTO notify_delivery (target, event, payload, state, next_at, created_at) VALUES ('t', 'e', '{}', ?, 0, ?)`, d.state, d.at)
	}
	title := exec(t, st, `INSERT INTO title (kind, name, created_at, updated_at) VALUES ('movie', 'M', 0, 0)`)
	version := exec(t, st, `INSERT INTO version (title_id, policy_key, created_at, updated_at) VALUES (?, 'movie-web', 0, 0)`, title)
	download := func(state string, at int64) int64 {
		id := exec(t, st, `INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state, created_at, updated_at)
			VALUES (?, ?, 'movie-web', 'h', 'r', 'i', ?, ?, ?)`, title, version, state, at, at)
		exec(t, st, `INSERT INTO download_attempt (download_id, provider, outcome, attempted_at) VALUES (?, 'rd', 'refused', ?)`, id, at)
		return id
	}
	gone := download("failed", old)
	queued := download("queued", old)
	imported := download("imported", old)
	jobRef := download("failed", old)
	linked := download("failed", old)
	offered := download("failed", old)
	fresh := download("failed", recent)
	exec(t, st, `INSERT INTO job (kind, payload, state, run_at, created_at, updated_at) VALUES ('router.research', json_object('download_id', ?), 'queued', 0, 0, 0)`, jobRef)
	exec(t, st, `INSERT INTO link (path, target, title_id, download_id, created_at, updated_at) VALUES ('/l', '/t', ?, ?, 0, 0)`, title, linked)
	exec(t, st, `INSERT INTO router_offer (version_key, title_id, info_hash, from_version, outcome, first_at, updated_at, source_kind, source_id)
		VALUES ('tv', ?, 'h', 'movie-web', 'routed', 0, 0, 'download', ?)`, title, offered)
	// Acquire runs: an old finished one, the latest for its target (old too), an old queued one.
	run := func(state, target string, at int64) int64 {
		return exec(t, st, `INSERT INTO acquire_run (source, title_id, version_id, version_key, target, state, created_at, updated_at)
			VALUES ('wanted', ?, ?, 'movie-web', ?, ?, ?, ?)`, title, version, target, state, at, at)
	}
	oldRun := run("nothing", "movie", old)
	latestRun := run("routed", "movie", old+1)
	openRun := run("queued", "other", old)
	exec(t, st, `INSERT INTO library_action (at, kind, mode) VALUES (?, 'link', 'applied'), (?, 'link', 'applied')`, old, recent)

	sum, err := Prune(context.Background(), st, now, Defaults)
	if err != nil {
		t.Fatal(err)
	}
	if got := count(t, st, `SELECT COUNT(*) FROM provider_sync_run WHERE provider = 'rd'`); got != 2 {
		t.Errorf("rd runs kept %d, want the latest failed and the recent one", got)
	}
	if got := count(t, st, `SELECT COUNT(*) FROM provider_sync_run WHERE provider = 'tb'`); got != 1 {
		t.Errorf("tb's only run pruned")
	}
	if got := count(t, st, `SELECT COUNT(*) FROM notify_delivery WHERE state = 'pending'`); got != 1 {
		t.Errorf("pending delivery pruned")
	}
	if got := count(t, st, `SELECT COUNT(*) FROM notify_delivery`); got != 2 {
		t.Errorf("deliveries kept %d, want pending and recent", got)
	}
	for id, want := range map[int64]int{gone: 0, queued: 1, imported: 1, jobRef: 1, linked: 1, offered: 1, fresh: 1} {
		if got := count(t, st, `SELECT COUNT(*) FROM download WHERE id = ?`, id); got != want {
			t.Errorf("download %d kept %d, want %d", id, got, want)
		}
	}
	if got := count(t, st, `SELECT COUNT(*) FROM download_attempt WHERE download_id = ?`, gone); got != 0 {
		t.Errorf("attempts of a pruned download kept")
	}
	for id, want := range map[int64]int{oldRun: 0, latestRun: 1, openRun: 1} {
		if got := count(t, st, `SELECT COUNT(*) FROM acquire_run WHERE id = ?`, id); got != want {
			t.Errorf("acquire run %d kept %d, want %d", id, got, want)
		}
	}
	if got := count(t, st, `SELECT COUNT(*) FROM library_action`); got != 1 {
		t.Errorf("library actions kept %d", got)
	}
	if sum["download"] != 1 || sum["provider_sync_run"] != 2 {
		t.Errorf("summary %v", sum)
	}
	// A zero window keeps everything of that kind.
	exec(t, st, `INSERT INTO library_action (at, kind, mode) VALUES (?, 'link', 'applied')`, old)
	if _, err := Prune(context.Background(), st, now, Config{Runs: Defaults.Runs}); err != nil {
		t.Fatal(err)
	}
	if got := count(t, st, `SELECT COUNT(*) FROM library_action`); got != 2 {
		t.Errorf("history pruned with History 0")
	}
}

// A run is bounded: MaxBatches batches per table, the rest the next night.
func TestPruneIsBounded(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	now := time.Now()
	old := store.Millis(now.Add(-400 * 24 * time.Hour))
	for i := 0; i < 3*batchSize; i++ {
		exec(t, st, `INSERT INTO library_action (at, kind, mode) VALUES (?, 'link', 'applied')`, old)
	}
	sum, err := Prune(context.Background(), st, now, Config{History: Defaults.History, MaxBatches: 2})
	if err != nil {
		t.Fatal(err)
	}
	if sum["library_action"] != 2*batchSize || count(t, st, `SELECT COUNT(*) FROM library_action`) != batchSize {
		t.Fatalf("summary %v", sum)
	}
}

// Quality sync runs: old runs go after the runs window; the latest of each source and
// outcome (the sync reads the latest run and the latest that read TRaSH) and a run still running stay.
func TestPruneQualitySyncRuns(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	now := time.Date(2026, 10, 29, 3, 0, 0, 0, time.UTC)
	old := store.Millis(now.Add(-400 * 24 * time.Hour))
	recent := store.Millis(now.Add(-time.Hour))
	ids := map[string]int64{}
	for i, r := range []struct {
		name, source, outcome string
		at                    int64
	}{
		{"old-unchanged", "trash", "unchanged", old}, {"old-error", "trash", "error", old + 1},
		{"last-unchanged", "trash", "unchanged", old + 2}, {"stuck-running", "trash", "running", old + 3},
		{"last-error", "trash", "error", recent - 1}, {"recent-review", "trash", "review", recent},
		{"other-old", "mirror", "applied", old}, {"other-last", "mirror", "applied", old + 1},
	} {
		ids[r.name] = exec(t, st, `INSERT INTO settings_quality_sync_run (source_key, started_at, outcome, commit_id)
			VALUES (?, ?, ?, ?)`, r.source, r.at, r.outcome, i)
	}
	sum, err := Prune(context.Background(), st, now, Defaults)
	if err != nil {
		t.Fatal(err)
	}
	if sum["settings_quality_sync_run"] != 3 {
		t.Fatalf("pruned %v", sum)
	}
	for name, id := range ids {
		gone := name == "old-unchanged" || name == "old-error" || name == "other-old"
		if n := count(t, st, `SELECT COUNT(*) FROM settings_quality_sync_run WHERE id = ?`, id); (n == 0) != gone {
			t.Errorf("%s: %d rows left", name, n)
		}
	}
}
