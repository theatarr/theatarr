package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func openTest(t *testing.T, opts Options) *Store {
	t.Helper()
	s, err := Open(context.Background(), migratedPath(t, 0), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPragmas(t *testing.T) {
	t.Parallel()
	s := openTest(t, Options{})
	ctx := context.Background()
	check := func(db *sql.DB, pragma, want string) {
		t.Helper()
		var got string
		if err := db.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("PRAGMA %s = %q, want %q", pragma, got, want)
		}
	}
	for _, db := range []*sql.DB{s.Writer(), s.Reader()} {
		check(db, "journal_mode", "wal")
		check(db, "foreign_keys", "1")
		check(db, "synchronous", "1")
		check(db, "busy_timeout", "5000")
	}
	check(s.Reader(), "query_only", "1")
	if _, err := s.Reader().ExecContext(ctx, "INSERT INTO gateway_backoff VALUES ('x' || 1, 1, 0, '', 0)"); err == nil {
		t.Error("reader pool accepted a write")
	}
}

func TestMigrateFromEmpty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "sub", "theatarr.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, _, err := s.SchemaVersion(ctx); err == nil {
		t.Error("SchemaVersion on an unmigrated database: want an error")
	}
	res, err := s.Migrate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 {
		t.Fatal("no migrations applied")
	}
	cur, target, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cur != target || cur == 0 {
		t.Errorf("schema version %d, target %d", cur, target)
	}
	// Idempotent.
	res, err = s.Migrate(ctx)
	if err != nil || len(res) != 0 {
		t.Errorf("second migrate: %d applied, err %v", len(res), err)
	}
	st, err := s.MigrationStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range st {
		if m.State != "applied" {
			t.Errorf("migration %d: state %s", m.Source.Version, m.State)
		}
	}
	var fkProblems int
	if err := s.Reader().QueryRowContext(ctx, "SELECT count(*) FROM pragma_foreign_key_check").Scan(&fkProblems); err != nil {
		t.Fatal(err)
	}
	if fkProblems != 0 {
		t.Errorf("%d foreign key problems", fkProblems)
	}
}

// The schema's rules hold: an expression unique index, a partial unique index and foreign keys.
func TestSchemaRules(t *testing.T) {
	t.Parallel()
	s := openTest(t, Options{})
	ctx := context.Background()
	now := Millis(time.Now())
	err := s.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO title (id, kind, name, tvdb_id, created_at, updated_at)
			VALUES (1, 'series', 'Show', 100, ?, ?)`, now, now)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO episode (id, title_id, season, number, created_at, updated_at)
			VALUES (1, 1, 1, 1, ?, ?)`, now, now)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO episode_number (episode_id, title_id, scheme, season, number)
			VALUES (1, 1, 'absolute', NULL, 1)`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// Same absolute number again: the COALESCE(season, -1) index must refuse it.
	err = s.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO episode_number (episode_id, title_id, scheme, season, number)
			VALUES (1, 1, 'absolute', NULL, 1)`)
		return err
	})
	if !IsUniqueViolation(err) {
		t.Errorf("duplicate absolute number: got %v, want a unique violation", err)
	}
	// Second series with the same TVDB id.
	err = s.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO title (kind, name, tvdb_id, created_at, updated_at)
			VALUES ('series', 'Dup', 100, ?, ?)`, now, now)
		return err
	})
	if !IsUniqueViolation(err) {
		t.Errorf("duplicate tvdb id: got %v, want a unique violation", err)
	}
	// Foreign keys are on.
	err = s.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO version (title_id, policy_key, created_at, updated_at) VALUES (99, 'x', 0, 0)`)
		return err
	})
	if err == nil {
		t.Error("version for a missing title was accepted")
	}
	// A bad enum.
	err = s.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO version (title_id, policy_key, state, created_at, updated_at)
			VALUES (1, 'x', 'bogus', 0, 0)`)
		return err
	})
	if err == nil {
		t.Error("bad version state was accepted")
	}
}

func TestWriteTxRollback(t *testing.T) {
	t.Parallel()
	s := openTest(t, Options{})
	ctx := context.Background()
	boom := errors.New("boom")
	err := s.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO gateway_backoff VALUES ('i' || 1, 1, 0, '', 0)`); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	var n int
	if err := s.Reader().QueryRow(`SELECT count(*) FROM gateway_backoff`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("rolled-back row is visible")
	}
}

// Readers keep reading while the writer commits; nobody sees SQLITE_BUSY.
func TestConcurrentReadersDuringWrites(t *testing.T) {
	s := openTest(t, Options{Readers: 8})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	const writes = 300
	var wg sync.WaitGroup
	var reads atomic.Int64
	done := make(chan struct{})
	errs := make(chan error, 16)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			last := -1
			for {
				select {
				case <-done:
					return
				default:
				}
				var n int
				if err := s.Reader().QueryRowContext(ctx, `SELECT count(*) FROM gateway_backoff`).Scan(&n); err != nil {
					errs <- err
					return
				}
				if n < last {
					errs <- errors.New("row count went backwards")
					return
				}
				last = n
				reads.Add(1)
			}
		}()
	}
	for i := range writes {
		err := s.WriteTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO gateway_backoff VALUES ('i' || ?, ?, 0, '', 0)`, i, i)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	close(done)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if reads.Load() == 0 {
		t.Error("no reads happened")
	}
	var n int
	s.Reader().QueryRow(`SELECT count(*) FROM gateway_backoff`).Scan(&n)
	if n != writes {
		t.Errorf("%d rows, want %d", n, writes)
	}
}

// Concurrent WriteTx calls from many goroutines serialise on the one writer connection.
func TestConcurrentWriters(t *testing.T) {
	s := openTest(t, Options{})
	ctx := context.Background()
	var wg sync.WaitGroup
	for g := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 20 {
				err := s.WriteTx(ctx, func(tx *sql.Tx) error {
					_, err := tx.Exec(`INSERT INTO gateway_backoff VALUES ('i' || ?, 0, 0, '', 0)`, g*100+i)
					return err
				})
				if err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	var n int
	s.Reader().QueryRow(`SELECT count(*) FROM gateway_backoff`).Scan(&n)
	if n != 200 {
		t.Errorf("%d rows, want 200", n)
	}
}

// Another process (here a second Store on the same file) holds the write lock past busy_timeout. WriteTx retries
// and succeeds once the lock is released.
func TestWriteTxRetriesBusy(t *testing.T) {
	path := migratedPath(t, 0)
	ctx := context.Background()
	var retries atomic.Int64
	s, err := Open(ctx, path, Options{BusyTimeout: 20 * time.Millisecond, RetryBackoff: 20 * time.Millisecond,
		MaxRetries: 10, OnBusyRetry: func() { retries.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, path, Options{BusyTimeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()

	tx, err := other.Writer().BeginTx(ctx, nil) // BEGIN IMMEDIATE: takes the write lock now.
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO gateway_backoff VALUES ('other' || 1, 1, 0, '', 0)`); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(150 * time.Millisecond)
		tx.Commit()
		close(released)
	}()

	err = s.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO gateway_backoff VALUES ('mine' || 1, 1, 0, '', 0)`)
		return err
	})
	<-released
	if err != nil {
		t.Fatalf("WriteTx: %v", err)
	}
	if retries.Load() == 0 {
		t.Error("expected at least one BUSY retry")
	}
	var n int
	s.Reader().QueryRow(`SELECT count(*) FROM gateway_backoff`).Scan(&n)
	if n != 2 {
		t.Errorf("%d rows, want 2", n)
	}
}

// Without enough retries the BUSY error surfaces, and IsBusy recognises it.
func TestWriteTxGivesUp(t *testing.T) {
	path := migratedPath(t, 0)
	ctx := context.Background()
	s, err := Open(ctx, path, Options{BusyTimeout: 10 * time.Millisecond, RetryBackoff: 5 * time.Millisecond, MaxRetries: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Migrate(ctx)
	other, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	tx, err := other.Writer().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	err = s.WriteTx(ctx, func(tx *sql.Tx) error { return nil })
	if !IsBusy(err) {
		t.Fatalf("got %v, want SQLITE_BUSY", err)
	}
}

func TestBackup(t *testing.T) {
	t.Parallel()
	s := openTest(t, Options{})
	ctx := context.Background()
	for i := range 50 {
		if err := s.WriteTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO gateway_backoff VALUES ('i' || ?, ?, 0, '', 0)`, i, i)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	dest := filepath.Join(t.TempDir(), "backups", "theatarr-backup.db")
	// Writes keep going during the backup.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 50; i < 100; i++ {
			s.WriteTx(ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO gateway_backoff VALUES ('i' || ?, ?, 0, '', 0)`, i, i)
				return err
			})
		}
	}()
	if err := s.Backup(ctx, dest); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	// Backing up over an existing file replaces it.
	if err := s.Backup(ctx, dest); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(ctx, s.Path()); err == nil {
		t.Error("backup onto the live database was accepted")
	}

	b, err := Open(ctx, dest, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var ok string
	if err := b.Reader().QueryRow(`PRAGMA integrity_check`).Scan(&ok); err != nil || ok != "ok" {
		t.Fatalf("integrity_check: %q %v", ok, err)
	}
	var n int
	b.Reader().QueryRow(`SELECT count(*) FROM gateway_backoff`).Scan(&n)
	if n != 100 {
		t.Errorf("backup has %d rows, want 100", n)
	}
	cur, target, err := b.SchemaVersion(ctx)
	if err != nil || cur != target {
		t.Errorf("backup schema %d/%d %v", cur, target, err)
	}
}

func TestMillis(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 25, 12, 0, 0, 123_000_000, time.FixedZone("x", 3600))
	if got := FromMillis(Millis(now)); !got.Equal(now) || got.Location() != time.UTC {
		t.Errorf("round trip: %v", got)
	}
	if !NullMillis(sql.NullInt64{}).IsZero() {
		t.Error("NULL should be the zero time")
	}
}
