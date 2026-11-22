package main

import (
	"context"
	"errors"
	"github.com/theatarr/theatarr/internal/tasks"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// importLoop is serve's library loop over one ready download of an active version, with a poll interval that never
// comes during a test and the wake channel serve hands every generation. Each import pass reports on passes whether
// it armed the quick re-check. Without a mount path every import fails at once; with mountDown the mount's release
// root is missing, so a pass ends in ErrMountDown.
type importLoop struct {
	s      *serveLibrary
	st     *store.Store
	wake   chan struct{}
	passes chan bool
}

func newImportLoop(t *testing.T, mountDown bool) *importLoop {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(dir, "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, year, created_at, updated_at) VALUES (1, 'movie', 'Movie', 2001, 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (10, 1, 'movie', 0, 0)`,
		`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality, score,
			episodes, provider, name, state, created_at, updated_at, ready_at)
			VALUES (1, 1, 10, 'movie', 'abcdef0123456789abcdef0123456789abcdef01', 'Movie.2001.2160p.WEB-GRP', 'idx',
			'WEBDL-2160p', 100, '[]', 'rd', 'Movie.2001.2160p.WEB-GRP', 'ready', 0, 0, 0)`,
	} {
		if _, err := st.Writer().ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(dir, "movies")
	mount := ""
	if mountDown {
		mount = filepath.Join(dir, "gone")
	}
	pol := &policy.Policy{
		Versions: map[string]*policy.VersionPolicy{"movie": {Kind: policy.KindMovie, Library: root}},
		Library: &policy.Library{Enabled: true, Mount: policy.LibraryMount{Path: mount},
			Versions: map[string]*policy.LibraryVersion{"movie": {Roots: []string{root}}}},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	l := &importLoop{st: st, wake: make(chan struct{}, 1), passes: make(chan bool, 64)}
	l.s = &serveLibrary{pol: pol, st: st, log: log, readyWake: l.wake,
		set:             policy.LibrarySettings{PollInterval: time.Hour, RelinkInterval: time.Hour, DanglingEvery: time.Hour},
		lib:             library.New(library.Options{Store: st, Policy: pol, Logger: log}),
		afterImportPass: func(armed bool) { l.passes <- armed }}
	return l
}

// freshWait records the download as ready now and waiting for its release on the mount.
func (l *importLoop) freshWait(t *testing.T) {
	t.Helper()
	l.exec(t, `UPDATE download SET ready_at = ? WHERE id = 1`, store.Millis(time.Now()))
	l.exec(t, `INSERT INTO library_import (download_id, state, waiting_since, last_error, attempts, created_at, updated_at)
		VALUES (1, 'waiting_mount', 0, 'release not on the mount yet', 1, 0, 0)`)
}

func (l *importLoop) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := l.st.Writer().ExecContext(context.Background(), q, args...); err != nil {
		t.Fatal(err)
	}
}

func (l *importLoop) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); l.s.Run(ctx, tasks.New(ctx, nil, nil)) }()
	t.Cleanup(func() { cancel(); <-done })
}

func (l *importLoop) notify() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// pass waits for the next import pass and returns whether it armed the re-check.
func (l *importLoop) pass(t *testing.T) bool {
	t.Helper()
	select {
	case armed := <-l.passes:
		return armed
	case <-time.After(10 * time.Second):
		t.Fatal("no import pass")
		return false
	}
}

// idle checks that no pass runs: after a pass that armed nothing, with the wake channel empty and the poll interval
// an hour away, none can.
func (l *importLoop) idle(t *testing.T) {
	t.Helper()
	if len(l.wake) != 0 {
		t.Fatal("a wake-up is still pending")
	}
	select {
	case <-l.passes:
		t.Fatal("an import pass ran with nothing to run it")
	case <-time.After(50 * time.Millisecond):
	}
}

// A download turning ready wakes the import pass without waiting for the poll interval; several at once share one
// pass.
func TestServeLibraryReadyWakesImport(t *testing.T) {
	t.Parallel()
	l := newImportLoop(t, false)
	for range 3 {
		l.notify()
	}
	l.start(t)
	if l.pass(t) {
		t.Fatal("re-check armed with no download waiting")
	}
	l.idle(t)
	l.notify()
	l.pass(t)
}

// shortRecheck makes the re-check delay short for one (non-parallel) test.
func shortRecheck(t *testing.T) {
	prev := readyRecheck
	readyRecheck = 10 * time.Millisecond
	t.Cleanup(func() { readyRecheck = prev })
}

// While a download that turned ready within the window waits for its release on the mount, the next pass runs
// readyRecheck later; once it is older than the window, passes wait for the poll interval again.
func TestServeLibraryRechecksFreshWait(t *testing.T) {
	shortRecheck(t)
	l := newImportLoop(t, false)
	l.freshWait(t)
	l.start(t)
	l.notify()
	for range 3 {
		if !l.pass(t) {
			t.Fatal("no re-check while the download is fresh")
		}
	}
	l.exec(t, `UPDATE download SET ready_at = ? WHERE id = 1`, store.Millis(time.Now().Add(-library.ReadyRecheckWindow)))
	for l.pass(t) { // a pass in flight may still have seen it fresh
	}
	l.idle(t)
}

// A loop that starts (or restarts after a settings save) while a fresh download waits arms the re-check at once.
func TestServeLibraryRecheckArmedAtStart(t *testing.T) {
	shortRecheck(t)
	l := newImportLoop(t, false)
	l.freshWait(t)
	l.start(t)
	if !l.pass(t) {
		t.Error("the re-check armed at start did not arm again")
	}
}

// A pass that ends with the mount down arms no re-check, even with a fresh download waiting.
func TestServeLibraryMountDownNoRecheck(t *testing.T) {
	shortRecheck(t)
	l := newImportLoop(t, true)
	l.freshWait(t)
	l.start(t) // armed at start: the fresh row is in the database
	if l.pass(t) {
		t.Fatal("a mount-down pass armed the re-check")
	}
	l.idle(t)
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

// The dangling summary names the report's total and the links this scan saw apart: rows kept from earlier reports
// (a root that could not be read) count in the total without being seen.
func TestPrintDanglingTotals(t *testing.T) {
	var b strings.Builder
	sum := &library.DanglingSummary{At: time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC), Dangling: 1, Links: 0,
		ByRoot: map[string]int{}}
	if err := printDangling(&b, nil, sum, false, false); err != nil {
		t.Fatal(err)
	}
	if out := b.String(); strings.Contains(out, "dangling of") ||
		!strings.Contains(out, "1 dangling; 0 links into the mount seen in this scan") {
		t.Errorf("summary line %q", out)
	}
	// zurg did not answer, but kept rows carry the cause found when last checked.
	b.Reset()
	sum.Zurg = &library.DanglingZurg{Error: "zurg down", NoVideo: 1, NoVideoTorrents: 0}
	if err := printDangling(&b, nil, sum, false, false); err != nil {
		t.Fatal(err)
	}
	if out := b.String(); !strings.Contains(out, "zurg API not read: zurg down") ||
		!strings.Contains(out, "1 of them: torrent had no video upstream when last checked") ||
		strings.Contains(out, "torrents in zurg") {
		t.Errorf("summary with zurg down %q", out)
	}
}

// The startup line for an HTTP-followed zurg log names the scheme and host only: a credential in the path or query
// never reaches the log.
func TestZurgLogURLLogsOriginOnly(t *testing.T) {
	for raw, want := range map[string]string{
		"http://zurg:9999/s3cr3t-token/logs?key=s3cr3t-q": "http://zurg:9999",
		"https://zurg.example/logs":                       "https://zurg.example",
		"http://zurg:9999":                                "http://zurg:9999",
	} {
		if got := logOrigin(raw); got != want {
			t.Errorf("%s: %q, want %q", raw, got, want)
		}
	}
}

// The library's own loop finishes files that broke while playing, with no now playing tracker: the settle pass runs on
// its short interval (an incident whose search ended is reported), and each relink sweep is followed by the come-back
// pass (a replaced row older than 30 days is kept aside unread).
func TestServeLibraryFinishesPlayingIncidents(t *testing.T) {
	t.Parallel()
	l := newImportLoop(t, false)
	dir := t.TempDir()
	mount := filepath.Join(dir, "zurg")
	root := l.s.pol.Versions["movie"].Library
	for _, d := range []string{filepath.Join(mount, "__all__", "Other"), root} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	l.s.pol.Library.Mount.Path = mount
	l.s.pol.Router = &policy.Router{Enabled: true}
	l.s.set = l.s.pol.LibrarySettings()
	l.s.set.PollInterval, l.s.set.RelinkInterval, l.s.set.DanglingEvery = time.Hour, time.Hour, time.Hour
	l.s.lib = library.New(library.Options{Store: l.st, Policy: l.s.pol, Logger: l.s.log})
	l.s.playingSettle = 20 * time.Millisecond
	l.exec(t, `INSERT INTO title (id, kind, name, year, created_at, updated_at) VALUES (2, 'movie', 'Film', 2002, 0, 0)`)
	l.exec(t, `INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (20, 2, 'movie', 0, 0)`)
	now := store.Millis(time.Now())
	// Incident 1: its search ended with nothing imported. Incident 2: replaced 40 days ago.
	l.exec(t, `INSERT INTO playing_break (id, incident, watched, link_id, title_id, version_key, path, old_target, broken_at,
		searched, updated_at) VALUES (1, 1, 1, 7, 2, 'movie', ?, ?, ?, 1, ?)`, filepath.Join(root, "a.mkv"),
		filepath.Join(mount, "__all__", "A", "a.mkv"), now, now)
	old := store.Millis(time.Now().Add(-40 * 24 * time.Hour))
	l.exec(t, `INSERT INTO playing_break (id, incident, watched, link_id, title_id, version_key, path, old_target, broken_at,
		fixed_by, reported, updated_at) VALUES (2, 2, 1, 8, 2, 'movie', ?, ?, ?, 'replaced', 'replaced', ?)`,
		filepath.Join(root, "b.mkv"), filepath.Join(mount, "__all__", "B", "b.mkv"), old, old)
	ctx, cancel := context.WithCancel(context.Background())
	tb := tasks.New(ctx, nil, nil)
	done := make(chan struct{})
	go func() { defer close(done); l.s.Run(ctx, tb) }()
	t.Cleanup(func() { cancel(); <-done })
	one := func(q string) string {
		var s string
		if err := l.st.Reader().QueryRowContext(ctx, q).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	deadline := time.Now().Add(10 * time.Second)
	for one(`SELECT reported FROM playing_break WHERE id = 1`) != "unavailable" {
		if time.Now().After(deadline) {
			t.Fatal("the settle pass did not run in the library's loop")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for {
		if ran, err := tb.Run(ctx, "library.relink", time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		} else if ran {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the relink task did not run")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for one(`SELECT settled FROM playing_break WHERE id = 2`) != "aside" { // the pass runs beside the loop
		if time.Now().After(deadline) {
			t.Fatal("after a sweep the come-back pass did not run")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The come-back pass after a sweep runs beside the library's loop: a hung read (up to its time limit) holds up
// neither the sweep's task nor the import wake-ups.
func TestServeLibraryComeBackBesideTheLoop(t *testing.T) {
	t.Parallel()
	l := newImportLoop(t, false)
	dir := t.TempDir()
	mount := filepath.Join(dir, "zurg")
	root := l.s.pol.Versions["movie"].Library
	hung := filepath.Join(mount, "__all__", "A", "a.mkv")
	for _, d := range []string{filepath.Dir(hung), root} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(hung, 0o644); err != nil {
		t.Skip("mkfifo:", err)
	}
	t.Cleanup(func() { // let the hung read return
		if w, err := os.OpenFile(hung, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			w.Close()
		}
	})
	path := filepath.Join(root, "a.mkv")
	if err := os.Symlink(hung, path); err != nil {
		t.Fatal(err)
	}
	l.s.pol.Library.Mount.Path = mount
	l.s.pol.Router = &policy.Router{Enabled: true}
	l.s.set = l.s.pol.LibrarySettings()
	l.s.set.PollInterval, l.s.set.RelinkInterval, l.s.set.DanglingEvery = time.Hour, time.Hour, time.Hour
	l.s.lib = library.New(library.Options{Store: l.st, Policy: l.s.pol, Logger: l.s.log})
	l.exec(t, `INSERT INTO link (id, path, target, title_id, state, created_at, updated_at) VALUES (7, ?, ?, 1, 'dead', 0, 0)`,
		path, hung)
	l.exec(t, `INSERT INTO link_version (link_id, version_id) VALUES (7, 10)`)
	l.exec(t, `INSERT INTO playing_break (id, incident, watched, link_id, title_id, version_key, path, old_target, broken_at,
		searched, reported, updated_at) VALUES (1, 1, 1, 7, 1, 'movie', ?, ?, 0, 1, 'unavailable', 0)`, path, hung)
	ctx, cancel := context.WithCancel(context.Background())
	tb := tasks.New(ctx, nil, nil)
	done := make(chan struct{})
	go func() { defer close(done); l.s.Run(ctx, tb) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(10 * time.Second)
	start := time.Now()
	for {
		if ran, err := tb.Run(ctx, "library.relink", time.Now().Add(time.Hour)); err != nil && !errors.Is(err, tasks.ErrUnknown) {
			t.Fatal(err)
		} else if ran {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the relink task did not run")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the sweep's task waited %v for the come-back's hung read", took)
	}
	l.notify()
	if l.pass(t); time.Since(start) > 5*time.Second {
		t.Errorf("an import wake-up waited %v behind the come-back", time.Since(start))
	}
}
