package provider

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// fakeProvider serves a fixed list (sync tests don't need HTTP; the clients have their own tests).
type fakeProvider struct {
	key  string
	list []Torrent
	err  error
}

func (p *fakeProvider) Key() string                                       { return p.key }
func (p *fakeProvider) Kind() string                                      { return "fake" }
func (p *fakeProvider) Account(context.Context) (*Account, error)         { return &Account{}, nil }
func (p *fakeProvider) Torrents(context.Context) ([]Torrent, error)       { return p.list, p.err }
func (p *fakeProvider) Torrent(context.Context, string) (*Torrent, error) { return nil, ErrNotFound }
func (p *fakeProvider) Cache() CacheSupport                               { return CacheSupport{} }
func (p *fakeProvider) CheckCached(context.Context, CacheQuery) (map[string]bool, error) {
	return nil, ErrCacheUnsupported
}

func tor(id, hash, name string, st State) Torrent {
	return Torrent{ID: id, Hash: hash, Name: name, Bytes: 1000, Status: string(st), State: st, Progress: 100,
		Added: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Links: 1}
}

type syncEnv struct {
	t   *testing.T
	st  *store.Store
	rd  *fakeProvider
	ad  *fakeProvider
	s   *Syncer
	now time.Time
}

func newSyncEnv(t *testing.T) *syncEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	e := &syncEnv{t: t, st: st, rd: &fakeProvider{key: "rd"}, ad: &fakeProvider{key: "ad"},
		now: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	e.s = &Syncer{Store: st, Providers: map[string]Provider{"rd": e.rd, "ad": e.ad}, Configured: []string{"rd", "ad"},
		Now: func() time.Time { return e.now }, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// Seeded from history, as import-arr leaves them: X and Y referenced (content_version), Z gone.
	e.exec(`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'M', 0, 0)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'movie-web', 0, 0)`)
	for i, c := range []struct{ hash, state string }{{h("a"), "unknown"}, {h("9"), "unknown"}, {h("8"), "gone"}} {
		e.exec(`INSERT INTO content (id, info_hash, name, state, first_seen_at, last_seen_at) VALUES (?, ?, '', ?, 1, 1)`,
			i+1, c.hash, c.state)
		e.exec(`INSERT INTO content_version (content_id, version_id, source) VALUES (?, 1, 'radarr')`, i+1)
	}
	return e
}

func (e *syncEnv) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.st.Writer().Exec(q, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *syncEnv) states() map[string]string {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT info_hash, state FROM content`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var hsh, s string
		rows.Scan(&hsh, &s)
		out[hsh[:1]] = s
	}
	return out
}

// dump is every registry row as text, to show a re-run changed nothing.
func (e *syncEnv) dump() string {
	e.t.Helper()
	var b strings.Builder
	for _, q := range []string{`SELECT * FROM content ORDER BY id`,
		`SELECT * FROM content_provider ORDER BY content_id, provider`} {
		rows, err := e.st.Reader().Query(q)
		if err != nil {
			e.t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			rows.Scan(ptrs...)
			fmt.Fprintln(&b, vals...)
		}
		rows.Close()
	}
	return b.String()
}

func (e *syncEnv) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.st.Reader().QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *syncEnv) run(o SyncOptions) *SyncResult {
	e.t.Helper()
	res, err := e.s.Run(context.Background(), o)
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func TestSyncRegistry(t *testing.T) {
	e := newSyncEnv(t)
	// RD holds X (twice: a re-add) and an unknown hash b.
	e.rd.list = []Torrent{
		tor("R2", h("a"), "Movie.A", StateDownloaded),
		tor("R9", h("b"), "Movie.B", StateDownloaded),
		{ID: "R3", Hash: h("a"), Name: "Movie.A", State: StateDownloading, Added: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Links: 0},
		{ID: "R4", Hash: "", Name: "no hash", State: StateError, Links: -1},
	}
	res := e.run(SyncOptions{Providers: []string{"rd"}})
	sum := res.Providers[0]
	if !sum.OK || sum.Torrents != 4 || sum.Hashes != 2 || sum.InvalidHashes != 1 || sum.Duplicates != 1 ||
		sum.NewContents != 1 || sum.NowPresent != 2 || sum.NamesFilled != 1 {
		t.Errorf("summary: %+v", sum)
	}
	// AD was never listed, so Y can't be absent yet.
	if got := e.states(); got["a"] != "present" || got["b"] != "unreferenced" || got["9"] != "unknown" || got["8"] != "gone" {
		t.Errorf("states after rd: %v", got)
	}
	// The downloaded copy stands for the hash, even though the other is older.
	var tid string
	var dups int
	e.st.Reader().QueryRow(`SELECT torrent_id, duplicates FROM content_provider WHERE provider = 'rd' AND content_id = 1`).
		Scan(&tid, &dups)
	if tid != "R2" || dups != 1 {
		t.Errorf("primary torrent %s with %d duplicates, want R2 with 1", tid, dups)
	}
	if res.Transitions["unknown->present"] != 1 || res.Transitions["new->unreferenced"] != 1 {
		t.Errorf("transitions %v", res.Transitions)
	}

	// Idempotent: nothing changed upstream, nothing changes here but a run row.
	before := e.dump()
	e.now = e.now.Add(time.Hour)
	res = e.run(SyncOptions{Providers: []string{"rd"}})
	if !res.Providers[0].RowsUnchanged || res.Providers[0].Unchanged != 2 || e.dump() != before {
		t.Errorf("re-run changed rows: %+v", res.Providers[0])
	}
	if n := e.count(`SELECT count(*) FROM provider_sync_run WHERE provider = 'rd' AND ok = 1`); n != 2 {
		t.Errorf("%d rd runs, want 2", n)
	}

	// AD lists b and a new d. Every provider is now listed: Y becomes absent; Z stays gone.
	e.ad.list = []Torrent{tor("101", h("b"), "Movie.B", StateDownloaded), tor("102", h("d"), "Show.D", StateDownloading)}
	e.run(SyncOptions{Providers: []string{"ad"}})
	if got := e.states(); got["9"] != "absent" || got["d"] != "unreferenced" || got["8"] != "gone" || got["a"] != "present" {
		t.Errorf("states after ad: %v", got)
	}

	// X leaves RD: its RD row turns absent and X is on no provider.
	e.rd.list = e.rd.list[1:2]
	res = e.run(SyncOptions{Providers: []string{"rd"}})
	if res.Providers[0].NowAbsent != 1 {
		t.Errorf("now absent %d", res.Providers[0].NowAbsent)
	}
	if got := e.states(); got["a"] != "absent" {
		t.Errorf("X: %s, want absent", got["a"])
	}
	if n := e.count(`SELECT count(*) FROM content_provider WHERE provider = 'rd' AND state = 'absent' AND absent_at IS NOT NULL`); n != 1 {
		t.Errorf("%d absent rd rows", n)
	}

	// It comes back: present again, absent_at cleared.
	e.rd.list = append(e.rd.list, tor("R5", h("a"), "Movie.A", StateDownloaded))
	e.run(SyncOptions{Providers: []string{"rd"}})
	if got := e.states(); got["a"] != "present" {
		t.Errorf("X back: %s", got["a"])
	}
	if n := e.count(`SELECT count(*) FROM content_provider WHERE absent_at IS NOT NULL`); n != 0 {
		t.Errorf("%d rows keep absent_at", n)
	}
	if e.count(`SELECT count(*) FROM pragma_foreign_key_check`) != 0 {
		t.Error("foreign key problems")
	}
}

func TestSyncFailedListChangesNothing(t *testing.T) {
	e := newSyncEnv(t)
	e.rd.list = []Torrent{tor("R1", h("a"), "Movie.A", StateDownloaded)}
	e.run(SyncOptions{Providers: []string{"rd"}})
	before := e.dump()
	e.rd.err = fmt.Errorf("rd: %w", ErrIncomplete)
	res, err := e.s.Run(context.Background(), SyncOptions{Providers: []string{"rd"}})
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("err %v", err)
	}
	if res.Providers[0].OK || e.dump() != before {
		t.Error("a failed list changed the registry")
	}
	// With every provider selected, the others still run.
	if _, err := e.s.Run(context.Background(), SyncOptions{}); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("err %v", err)
	}
	if n := e.count(`SELECT count(*) FROM provider_sync_run WHERE provider = 'rd' AND ok = 0 AND error != ''`); n != 2 {
		t.Errorf("%d failed run rows", n)
	}
	if e.count(`SELECT count(*) FROM provider_sync_run WHERE provider = 'ad' AND ok = 1`) != 1 {
		t.Error("ad did not run after rd failed")
	}
}

func TestSyncDryRun(t *testing.T) {
	e := newSyncEnv(t)
	before := e.dump()
	e.rd.list = []Torrent{tor("R1", h("a"), "Movie.A", StateDownloaded), tor("R2", h("b"), "B", StateDownloaded)}
	res := e.run(SyncOptions{Providers: []string{"rd"}, DryRun: true})
	if res.Providers[0].NewContents != 1 || res.Transitions["unknown->present"] != 1 {
		t.Errorf("dry run computed nothing: %+v %v", res.Providers[0], res.Transitions)
	}
	if res.Contents["present"] != 1 || res.Contents["unreferenced"] != 1 || res.Contents["unknown"] != 1 {
		t.Errorf("would-be counts %v", res.Contents)
	}
	if e.dump() != before || e.count(`SELECT count(*) FROM provider_sync_run`) != 0 {
		t.Error("dry run wrote to the database")
	}
}

func TestSyncJob(t *testing.T) {
	e := newSyncEnv(t)
	e.rd.list = []Torrent{tor("R1", h("a"), "Movie.A", StateDownloaded)}
	q := jobs.NewQueue(e.st)
	w := jobs.NewWorker(q, jobs.WorkerOptions{Owner: "test"})
	e.s.RegisterJobs(w)
	ctx := context.Background()
	id, _, err := q.Enqueue(ctx, JobKind, SyncPayload{Providers: []string{"rd"}}, jobs.EnqueueOptions{DedupeKey: JobKind})
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := w.RunOne(ctx); !ran || err != nil {
		t.Fatalf("RunOne: %v %v", ran, err)
	}
	info, err := q.Get(ctx, id)
	if err != nil || info.State != jobs.StateDone {
		t.Fatalf("job: %+v %v", info, err)
	}
	if e.states()["a"] != "present" {
		t.Error("job did not sync")
	}
	// An unknown provider fails at once.
	id, _, _ = q.Enqueue(ctx, JobKind, SyncPayload{Providers: []string{"zz"}}, jobs.EnqueueOptions{})
	w.RunOne(ctx)
	if info, _ := q.Get(ctx, id); info.State != jobs.StateFailed {
		t.Errorf("unknown provider job: %s", info.State)
	}
}

// One provider failing is a partial success: the job succeeds, the failed provider backs off and is retried by its
// own job, and scheduled syncs keep listing the healthy one.
func TestSyncJobPartialFailure(t *testing.T) {
	e := newSyncEnv(t)
	ctx := context.Background()
	q := jobs.NewQueue(e.st)
	q.SetClock(func() time.Time { return e.now })
	e.s.Queue = q
	w := jobs.NewWorker(q, jobs.WorkerOptions{Owner: "test"})
	e.s.RegisterJobs(w)
	e.rd.list = []Torrent{tor("R1", h("a"), "Movie.A", StateDownloaded)}
	e.ad.err = errors.New("alldebrid: AUTH_BLOCKED")
	start := e.now
	runs := func(p string, ok int) int {
		return e.count(`SELECT count(*) FROM provider_sync_run WHERE provider = ? AND ok = ?`, p, ok)
	}
	job := func(want string) {
		t.Helper()
		var id int64
		if err := e.st.Reader().QueryRow(`SELECT id FROM job WHERE state = 'queued' AND run_at <= ?
			ORDER BY priority DESC, run_at, id LIMIT 1`, store.Millis(e.now)).Scan(&id); err != nil {
			t.Fatalf("no ready job: %v", err)
		}
		ran, err := w.RunOne(ctx)
		if !ran || err != nil {
			t.Fatalf("RunOne: %v %v", ran, err)
		}
		var state string
		if err := e.st.Reader().QueryRow(`SELECT state FROM job WHERE id = ?`, id).
			Scan(&state); err != nil || state != want {
			t.Fatalf("job state %q, want %q (%v)", state, want, err)
		}
	}
	retryAt := func() (int64, string) {
		var at int64
		var key string
		e.st.Reader().QueryRow(`SELECT run_at, dedupe_key FROM job WHERE state = 'queued' AND payload LIKE '%"retry":true%'`).
			Scan(&at, &key)
		return at, key
	}
	scheduled := func() {
		t.Helper()
		if _, _, err := q.Enqueue(ctx, JobKind, SyncPayload{}, jobs.EnqueueOptions{DedupeKey: JobKind}); err != nil {
			t.Fatal(err)
		}
	}

	scheduled()
	job(jobs.StateDone)
	if runs("rd", 1) != 1 || runs("ad", 0) != 1 || e.states()["a"] != "present" {
		t.Fatalf("first sync: rd %d ad %d", runs("rd", 1), runs("ad", 0))
	}
	if at, key := retryAt(); at != store.Millis(start.Add(RetryBase)) || key != "provider.sync/ad/1" {
		t.Errorf("ad retry at %v key %q", store.FromMillis(at), key)
	}

	// A scheduled sync while ad backs off lists rd only.
	e.now = start.Add(time.Minute)
	scheduled()
	job(jobs.StateDone)
	if runs("rd", 1) != 2 || runs("ad", 0) != 1 {
		t.Errorf("sync during back-off: rd %d ad %d", runs("rd", 1), runs("ad", 0))
	}

	// ad's own retry fails again: the job is done, and the next retry waits twice as long.
	e.now = start.Add(RetryBase)
	job(jobs.StateDone)
	if runs("rd", 1) != 2 || runs("ad", 0) != 2 {
		t.Errorf("retry: rd %d ad %d", runs("rd", 1), runs("ad", 0))
	}
	if at, key := retryAt(); at != store.Millis(e.now.Add(2*RetryBase)) || key != "provider.sync/ad/2" {
		t.Errorf("second retry at %v key %q", store.FromMillis(at), key)
	}
	st, err := ReadStatus(ctx, e.st.Reader(), []string{"rd", "ad"})
	if err != nil {
		t.Fatal(err)
	}
	if ad := st.Providers[1]; ad.Failures != 2 || ad.RetryAt == nil || !ad.RetryAt.Equal(e.now.Add(2*RetryBase)) ||
		st.Providers[0].Failures != 0 || st.Providers[0].RetryAt != nil {
		t.Errorf("status %+v %+v", st.Providers[0], ad)
	}

	// ad recovers: its retry lists it, and no further retry is scheduled.
	e.ad.err, e.ad.list = nil, nil
	e.now = e.now.Add(2 * RetryBase)
	job(jobs.StateDone)
	if runs("ad", 1) != 1 {
		t.Error("ad did not sync on its retry")
	}
	if at, _ := retryAt(); at != 0 {
		t.Error("a retry is still queued")
	}

	// Every provider failing fails the job; its retry finds both backing off and finishes.
	e.rd.err, e.ad.err = errors.New("rd down"), errors.New("ad down")
	scheduled()
	job(jobs.StateQueued)
	e.now = e.now.Add(time.Minute)
	n := runs("rd", 0) + runs("ad", 0)
	job(jobs.StateDone)
	if runs("rd", 0)+runs("ad", 0) != n {
		t.Error("the job's retry listed providers that back off")
	}
}

func TestBackoff(t *testing.T) {
	for n, want := range map[int]time.Duration{0: 0, 1: 2 * time.Minute, 2: 4 * time.Minute, 5: 32 * time.Minute,
		6: time.Hour, 40: time.Hour} {
		if got := Backoff(n); got != want {
			t.Errorf("Backoff(%d) = %v, want %v", n, got, want)
		}
	}
}

func TestReadStatus(t *testing.T) {
	e := newSyncEnv(t)
	e.rd.list = []Torrent{tor("R1", h("a"), "Movie.A", StateDownloaded)}
	e.run(SyncOptions{Providers: []string{"rd"}})
	st, err := ReadStatus(context.Background(), e.st.Reader(), []string{"rd", "ad"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Providers[0].Present != 1 || st.Providers[0].LastOK == nil || st.Providers[1].LastRun != nil ||
		len(st.Unlisted) != 1 || st.Unlisted[0] != "ad" || st.Contents["present"] != 1 {
		t.Errorf("status %+v", st)
	}
}

// Migration 00006 rebuilds content with foreign keys off: the rows that point at content survive.
func TestMigration00006KeepsRegistry(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "theatarr.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sub, err := fs.Sub(os.DirFS("../store"), "migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(database.DialectSQLite3, st.Writer(), sub, goose.WithDisableGlobalRegistry(true),
		goose.WithTableName(store.MigrationTable), goose.WithAllowOutofOrder(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 3); err != nil {
		t.Fatal(err)
	}
	w := st.Writer()
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'M', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'v', 0, 0)`,
		`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (7, '` + h("a") + `', 'unknown', 1, 2)`,
		`INSERT INTO content_version (content_id, version_id, source) VALUES (7, 1, 'radarr')`,
		`INSERT INTO content_provider (content_id, provider, torrent_id, seen_at) VALUES (7, 'rd', 'X', 3)`,
		`INSERT INTO content_file (id, content_id, path, size_bytes) VALUES (1, 7, 'a.mkv', 10)`,
	} {
		if _, err := w.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.UpTo(ctx, 6); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"content", "content_version", "content_provider", "content_file"} {
		var n int
		if err := w.QueryRowContext(ctx, "SELECT count(*) FROM "+tbl).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s: %d rows (%v), want 1", tbl, n, err)
		}
	}
	var state string
	var fk bool
	w.QueryRowContext(ctx, `SELECT state FROM content_provider WHERE content_id = 7`).Scan(&state)
	w.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk)
	if state != "present" || !fk {
		t.Errorf("content_provider state %q, foreign keys on %v", state, fk)
	}
	if _, err := w.ExecContext(ctx, `UPDATE content SET state = 'unreferenced' WHERE id = 7`); err != nil {
		t.Errorf("new state refused: %v", err)
	}
	// Down and up again.
	if _, err := p.DownTo(ctx, 5); err != nil {
		t.Fatal(err)
	}
	var cs string
	w.QueryRowContext(ctx, `SELECT state FROM content WHERE id = 7`).Scan(&cs)
	if cs != "present" {
		t.Errorf("down: state %q, want present", cs)
	}
	if _, err := p.UpTo(ctx, 6); err != nil {
		t.Fatal(err)
	}
	var n int
	w.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&n)
	if n != 0 {
		t.Errorf("%d foreign key problems", n)
	}
}

// Another writer can create or fill content after the reader preload. Applying the list must resolve the new
// identity and fill only fields that are still empty, including on a retried write callback.
func TestSyncPreloadConcurrentContent(t *testing.T) {
	e := newSyncEnv(t)
	ctx := context.Background()
	list := []Torrent{tor("R1", h("a"), "provider name", StateDownloaded), tor("R2", h("b"), "new name", StateDownloaded)}
	contents, err := preloadContents(ctx, e.st.Reader(), list)
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) != 1 || contents[h("a")].id != 1 {
		t.Fatalf("preloaded contents: %+v", contents)
	}
	e.exec(`UPDATE content SET name = 'arr name', size_bytes = 42 WHERE id = 1`)
	e.exec(`INSERT INTO content (info_hash, name, first_seen_at, last_seen_at) VALUES (?, 'other writer', 1, 1)`, h("b"))
	for range 2 {
		sum := &ProviderSummary{ByState: map[State]int{}}
		if err := e.st.WriteTx(ctx, func(tx *sql.Tx) error {
			_, err := applyList(ctx, tx, "rd", list, contents, store.Millis(e.now), sum)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if sum.NewContents != 0 {
			t.Fatalf("concurrent row counted as new: %+v", sum)
		}
	}
	if e.count(`SELECT count(*) FROM content WHERE id = 1 AND name = 'arr name' AND size_bytes = 42`) != 1 {
		t.Fatal("stale preload overwrote existing metadata")
	}
	if e.count(`SELECT count(*) FROM content WHERE info_hash = ? AND name = 'other writer' AND size_bytes = 1000`, h("b")) != 1 {
		t.Fatal("concurrent insert was not reused and filled")
	}
	if e.count(`SELECT count(*) FROM content_provider WHERE provider = 'rd' AND state = 'present'`) != 2 {
		t.Fatal("listing was not applied atomically")
	}
}

func TestSyncStaleEpochKeepsRegistry(t *testing.T) {
	e := newSyncEnv(t)
	e.s.SettingsManaged, e.s.ProviderEpoch = true, 1
	e.exec(`INSERT INTO settings_runtime_state (id, last_published_generation, provider_epoch) VALUES (1, 1, 2)`)
	e.rd.list = []Torrent{tor("R1", h("a"), "stale account", StateDownloaded)}
	before := e.dump()
	res := e.run(SyncOptions{Providers: []string{"rd"}})
	if len(res.Providers) != 1 || !res.Providers[0].Discarded || res.Providers[0].OK {
		t.Fatalf("stale listing not discarded: %+v", res.Providers)
	}
	if after := e.dump(); after != before || e.count(`SELECT count(*) FROM provider_sync_run`) != 0 {
		t.Fatal("stale listing changed registry or recorded a run")
	}
}

// Empty successful inventories are complete: the account may have had its last torrent removed by its owner.
// Errors, including partial listings, must instead leave presence unchanged.
func TestSyncEmptySuccessfulInventory(t *testing.T) {
	e := newSyncEnv(t)
	e.rd.list = []Torrent{tor("R1", h("a"), "Movie.A", StateDownloaded)}
	e.run(SyncOptions{Providers: []string{"rd"}})
	e.rd.list = nil
	e.rd.err = errors.New("partial listing")
	if _, err := e.s.Run(context.Background(), SyncOptions{Providers: []string{"rd"}}); err == nil {
		t.Fatal("listing error accepted")
	}
	if n := e.count(`SELECT count(*) FROM content_provider WHERE provider='rd' AND state='present'`); n != 1 {
		t.Fatalf("failed empty listing marked absent: %d", n)
	}
	e.rd.err = nil
	res := e.run(SyncOptions{Providers: []string{"rd"}})
	if !res.Providers[0].OK || res.Providers[0].NowAbsent != 1 {
		t.Fatalf("complete empty inventory: %+v", res.Providers[0])
	}
}

// The record (provider_torrent): a successful sync replaces the provider's rows with its whole list, two torrents of
// one hash on one account as two rows, and sets the read time; a failed listing leaves the rows and the time alone.
func TestSyncWritesRecord(t *testing.T) {
	t.Parallel()
	e := newSyncEnv(t)
	ctx := context.Background()
	dup := tor("R2", h("a"), "X.2020.1080p", StateDownloaded)
	dup.Files = []File{{Path: "X/x.mkv", Selected: true}, {Path: "X/x.nfo"}}
	e.rd.list = []Torrent{tor("R1", h("a"), "X.2020.1080p", StateDownloaded), dup,
		tor("R3", h("b"), "Y.2021.1080p", StateDownloading)}
	e.ad.list = []Torrent{}
	if _, err := e.s.Run(ctx, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	record := func() string {
		t.Helper()
		var out string
		if err := e.st.Reader().QueryRow(`SELECT COALESCE(group_concat(provider || ':' || torrent_id || ':' ||
			substr(info_hash, 1, 1) || ':' || files || ':' || original_name, ' '), '') FROM (SELECT * FROM provider_torrent
			ORDER BY provider, torrent_id)`).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	readAt := func(p string) (int64, int) {
		t.Helper()
		var at int64
		var n int
		if err := e.st.Reader().QueryRow(`SELECT read_at, (SELECT count(*) FROM provider_torrent WHERE provider = ?)
			FROM provider_torrent_read WHERE provider = ?`, p, p).Scan(&at, &n); err != nil {
			t.Fatalf("%s read: %v", p, err)
		}
		return at, n
	}
	if got, want := record(), "rd:R1:a:-1: rd:R2:a:1:X rd:R3:b:-1:"; got != want {
		t.Fatalf("record %q, want %q", got, want)
	}
	if at, n := readAt("rd"); at != store.Millis(e.now) || n != 3 {
		t.Fatalf("rd read %d %d", at, n)
	}
	if at, n := readAt("ad"); at != store.Millis(e.now) || n != 0 {
		t.Fatalf("an empty account is read: %d %d", at, n)
	}
	var cp int
	if err := e.st.Reader().QueryRow(`SELECT count(*) FROM content_provider WHERE provider = 'rd'`).Scan(&cp); err != nil || cp != 2 {
		t.Fatalf("the registry keeps one row per hash: %d %v", cp, err)
	}

	first := e.now
	e.now = e.now.Add(time.Hour)
	e.rd.err = errors.New("rd: HTTP 503")
	e.rd.list = nil
	if _, err := e.s.Run(ctx, SyncOptions{}); err == nil {
		t.Fatal("rd failed")
	}
	if got := record(); got != "rd:R1:a:-1: rd:R2:a:1:X rd:R3:b:-1:" {
		t.Fatalf("a failed read keeps the record: %q", got)
	}
	if at, _ := readAt("rd"); at != store.Millis(first) {
		t.Fatalf("a failed read keeps the time: %d", at)
	}
	if at, _ := readAt("ad"); at != store.Millis(e.now) {
		t.Fatalf("ad read again: %d", at)
	}

	e.rd.err = nil
	e.rd.list = []Torrent{tor("R3", h("b"), "Y.2021.1080p", StateDownloaded)}
	if _, err := e.s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := record(); got != "rd:R3:b:-1:" {
		t.Fatalf("a successful read replaces the rows: %q", got)
	}
}

// An account change clears the account's record and read time, and the stored usage of its hashes, also of one
// another account still holds (it is worked out again); the other account's rows stay.
func TestClearRecord(t *testing.T) {
	t.Parallel()
	e := newSyncEnv(t)
	ctx := context.Background()
	e.exec(`INSERT INTO provider_torrent (provider, torrent_id, info_hash) VALUES ('rd', '1', ?), ('ad', '2', ?),
		('ad', '3', ?)`, h("a"), h("a"), h("b"))
	e.exec(`INSERT INTO provider_torrent_read (provider, read_at) VALUES ('rd', 1), ('ad', 1)`)
	e.exec(`INSERT INTO provider_hash_usage (info_hash, usage) VALUES (?, 'used'), (?, 'unused')`, h("a"), h("b"))
	if err := e.st.WriteTx(ctx, func(tx *sql.Tx) error { return ClearRecord(ctx, tx, "rd") }); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := e.st.Reader().QueryRow(`SELECT (SELECT group_concat(provider || torrent_id) FROM provider_torrent) || ' ' ||
		(SELECT group_concat(provider) FROM provider_torrent_read) || ' ' ||
		(SELECT group_concat(substr(info_hash, 1, 1)) FROM provider_hash_usage)`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "ad2,ad3 ad b" {
		t.Fatalf("after clearing rd: %q", got)
	}
}
