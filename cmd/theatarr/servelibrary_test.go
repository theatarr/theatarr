package main

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// passLog counts the library's import passes by their log lines: the fixture's download fails every import (its
// version is not in the policy), and each pass logs that once.
type passLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *passLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *passLog) passes() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Count(l.buf.String(), "library: import failed")
}

// importLoopFixture is serve's library loop with a poll interval that never comes during a test, over one ready
// download, and the wake channel serve hands every generation.
func importLoopFixture(t *testing.T) (*serveLibrary, *store.Store, *passLog, func()) {
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
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, year, created_at, updated_at) VALUES (1, 'movie', 'Movie', 2001, 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (10, 1, 'gone', 0, 0)`,
		`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality, score,
			episodes, provider, name, state, created_at, updated_at, ready_at)
			VALUES (1, 1, 10, 'gone', 'abcdef0123456789abcdef0123456789abcdef01', 'Movie.2001.2160p.WEB-GRP', 'idx',
			'WEBDL-2160p', 100, '[]', 'rd', 'Movie.2001.2160p.WEB-GRP', 'ready', 0, 0, 0)`,
	} {
		if _, err := st.Writer().ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	logs := &passLog{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	pol := &policy.Policy{Library: &policy.Library{}}
	wake := make(chan struct{}, 1)
	s := &serveLibrary{pol: pol, st: st, log: log, readyWake: wake,
		set: policy.LibrarySettings{PollInterval: time.Hour, RelinkInterval: time.Hour, DanglingEvery: time.Hour},
		lib: library.New(library.Options{Store: st, Policy: pol, Logger: log})}
	notify := func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	return s, st, logs, notify
}

func waitPasses(t *testing.T, logs *passLog, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for logs.passes() < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d import passes, want %d", logs.passes(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A download turning ready wakes the import pass without waiting for the poll interval; several at once share one
// pass.
func TestServeLibraryReadyWakesImport(t *testing.T) {
	t.Parallel()
	s, _, logs, notify := importLoopFixture(t)
	for range 3 {
		notify()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	defer func() { cancel(); <-done }()
	waitPasses(t, logs, 1)
	time.Sleep(100 * time.Millisecond)
	if n := logs.passes(); n != 1 {
		t.Fatalf("three ready downloads ran %d passes", n)
	}
	notify()
	waitPasses(t, logs, 2)
}

// While a download that turned ready within the window waits for its release on the mount, the next pass runs
// readyRecheck later; once it is older than the window, passes wait for the poll interval again.
func TestServeLibraryRechecksFreshWait(t *testing.T) {
	prev := readyRecheck
	readyRecheck = 20 * time.Millisecond
	t.Cleanup(func() { readyRecheck = prev })
	s, st, logs, notify := importLoopFixture(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.Writer().ExecContext(context.Background(), q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE download SET ready_at = ? WHERE id = 1`, store.Millis(time.Now()))
	exec(`INSERT INTO library_import (download_id, state, waiting_since, last_error, attempts, created_at, updated_at)
		VALUES (1, 'waiting_mount', 0, 'release not on the mount yet', 1, 0, 0)`)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	defer func() { cancel(); <-done }()
	notify()
	waitPasses(t, logs, 4)
	exec(`UPDATE download SET ready_at = ? WHERE id = 1`, store.Millis(time.Now().Add(-library.ReadyRecheckWindow)))
	time.Sleep(100 * time.Millisecond) // a pass in flight may still schedule one more
	n := logs.passes()
	time.Sleep(200 * time.Millisecond)
	if m := logs.passes(); m != n {
		t.Errorf("passes went on past the window: %d, then %d", n, m)
	}
}

// The import clears rclone's cached release root only with library.mount.rclone_rc.forget_release_root on.
func TestRcloneForgetSetting(t *testing.T) {
	t.Parallel()
	pol := &policy.Policy{Library: &policy.Library{Mount: policy.LibraryMount{
		RcloneRC: &policy.RcloneRC{URL: "http://rclone.invalid:5572"}}}}
	gw, err := gateway.New(context.Background(), pol, gateway.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if f := newRcloneForget(pol, pol.LibrarySettings(), gw); f != nil {
		t.Error("forget wired with the setting off")
	}
	pol.Library.Mount.RcloneRC.ForgetReleaseRoot = true
	if f := newRcloneForget(pol, pol.LibrarySettings(), gw); f == nil {
		t.Error("forget not wired with the setting on")
	}
	pol.Library.Mount.RcloneRC = nil
	if f := newRcloneForget(pol, pol.LibrarySettings(), gw); f != nil {
		t.Error("forget wired without rclone_rc")
	}
}
