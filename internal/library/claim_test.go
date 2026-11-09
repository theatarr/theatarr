package library

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"

	"github.com/theatarr/theatarr/internal/library/probe"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// An import where some links are made and others fail is not terminal: the download stays importing, the import is
// partial, and a later pass finishes it once the failing path is fixed.
func TestPartialImportRetried(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	rel := "Show.S01.2160p.WEB-DL.DDP5.1.H.265-GRP"
	e.release(rel, map[string]float64{"Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2700,
		"Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2650})
	e.download(1, "tv", 2, 20, rel, rel, "[201, 202]")
	// A regular file where E02's link goes: that link fails, E01's is made.
	blocker := filepath.Join(e.shows, "Show (2020)", "Season 01",
		"Show (2020) - S01E02 - Second Part [WEBDL-2160p]-GRP.mkv")
	if err := os.MkdirAll(filepath.Dir(blocker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.lib.Import(e.ctx, 1, false); err == nil {
		t.Error("a partial import reported no error")
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "importing" {
		t.Errorf("download %s after a partial import, want importing", got)
	}
	if got := e.one(`SELECT state FROM library_import WHERE download_id = 1`); got != "partial" {
		t.Errorf("library_import %s, want partial", got)
	}
	// Not retried at once by the periodic pass (back-off), then retried once the path is fixed.
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if _, err := e.lib.Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "importing" {
		t.Errorf("retried before its back-off: %s", got)
	}
	e.now = e.now.Add(time.Hour)
	if n, err := e.lib.Run(e.ctx); err != nil || n != 1 {
		t.Fatalf("run %d %v", n, err)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "imported" {
		t.Errorf("download %s after the repair", got)
	}
	if got := e.one(`SELECT state FROM library_import WHERE download_id = 1`); got != "linked" {
		t.Errorf("library_import %s after the repair", got)
	}
	if got := e.one(`SELECT count(*) FROM link WHERE state = 'active'`); got != "2" {
		t.Errorf("links %s", got)
	}
}

// Two importers of the same download (the periodic pass and a manual import): the second finds it claimed and stops
// before planning or writing anything.
func TestConcurrentImportsClaimed(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	inner := e.lib.probe
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.lib.probe = func(p string, n int64) (*probe.Result, error) {
		first := false
		once.Do(func() { first = true })
		if first {
			close(entered)
			<-release
		}
		return inner(p, n)
	}
	errc := make(chan error, 1)
	go func() {
		_, err := e.lib.Import(context.Background(), 1, false)
		errc <- err
	}()
	<-entered
	_, err2 := e.lib.Import(e.ctx, 1, false)
	close(release)
	if err := <-errc; err != nil {
		t.Fatalf("first import: %v", err)
	}
	if !errors.Is(err2, ErrClaimed) {
		t.Errorf("second import: %v, want ErrClaimed", err2)
	}
	if got := e.one(`SELECT count(*) FROM library_action WHERE kind = 'link' AND mode = 'applied'`); got != "1" {
		t.Errorf("%s link actions, want 1", got)
	}
	if got := e.one(`SELECT attempts FROM library_import WHERE download_id = 1`); got != "1" {
		t.Errorf("%s import attempts, want 1", got)
	}
	if got := e.one(`SELECT count(*) FROM library_claim`); got != "0" {
		t.Errorf("claim left behind: %s", got)
	}
	// An abandoned claim (a crash) is taken over once it expires.
	e.exec(`UPDATE download SET state = 'importing' WHERE id = 1`)
	e.exec(`INSERT INTO library_claim (download_id, owner, until) VALUES (1, 'dead', ?)`, e.now.Add(time.Minute).UnixMilli())
	if _, err := e.lib.Import(e.ctx, 1, false); !errors.Is(err, ErrClaimed) {
		t.Errorf("live claim: %v", err)
	}
	e.now = e.now.Add(2 * time.Minute)
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Errorf("expired claim: %v", err)
	}
}

// Migration 00027 rebuilds library_import with the partial state and goes down and up cleanly, keeping the rows.
func TestMigration27(t *testing.T) {
	t.Parallel()
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
	if _, err := p.UpTo(ctx, 26); err != nil {
		t.Fatal(err)
	}
	w := st.Writer()
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'M', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'v', 0, 0)`,
		`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
			created_at, updated_at) VALUES (1, 1, 1, 'v', 'h', 'r', 'i', 'importing', 0, 0)`,
		`INSERT INTO library_import (download_id, state, attempts, created_at, updated_at) VALUES (1, 'linked', 2, 0, 0)`,
	} {
		if _, err := w.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if _, err := p.UpTo(ctx, 27); err != nil {
			t.Fatal(err)
		}
		if _, err := w.ExecContext(ctx, `UPDATE library_import SET state = 'partial'`); err != nil {
			t.Errorf("partial refused: %v", err)
		}
		if _, err := w.ExecContext(ctx, `INSERT INTO library_claim (download_id, owner, until) VALUES (1, 'x', 0)`); err != nil {
			t.Error(err)
		}
		if _, err := w.ExecContext(ctx, `UPDATE library_import SET state = 'bogus'`); err == nil {
			t.Error("state CHECK missing")
		}
		var n int
		w.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&n)
		if n != 0 {
			t.Errorf("%d foreign key problems", n)
		}
		if _, err := p.DownTo(ctx, 26); err != nil {
			t.Fatal(err)
		}
		var state string
		var attempts int
		if err := w.QueryRowContext(ctx, `SELECT state, attempts FROM library_import WHERE download_id = 1`).
			Scan(&state, &attempts); err != nil || state != "linked" || attempts != 2 {
			t.Errorf("down: %s %d %v", state, attempts, err)
		}
	}
}

// A stale import (its claim expired and another run took the download over and imported it) records nothing: its
// rejection does not fail the download the newer run imported, nor blocklist its hash.
func TestStaleImportRecordsNoOutcome(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 40 * 60}) // a sample: rejected
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	inner := e.lib.probe
	e.lib.probe = func(p string, n int64) (*probe.Result, error) {
		// While this run probes, its claim lapses and a newer run takes over and imports the download.
		e.exec(`UPDATE library_claim SET owner = 'newer' WHERE download_id = 1`)
		e.exec(`UPDATE download SET state = 'imported' WHERE id = 1`)
		return inner(p, n)
	}
	if _, err := e.lib.Import(e.ctx, 1, false); !errors.Is(err, ErrClaimed) {
		t.Fatalf("stale import: %v, want ErrClaimed", err)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "imported" {
		t.Errorf("download %s, want the newer run's imported", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM router_blocklist`) + e.one(`SELECT COUNT(*) FROM library_import`); got != "00" {
		t.Errorf("blocklist and library_import rows %s, want none", got)
	}
}
