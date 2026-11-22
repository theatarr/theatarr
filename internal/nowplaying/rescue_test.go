package nowplaying

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/outcome"
	"github.com/theatarr/theatarr/internal/plex"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// testLib is the real library over a temporary folder as the mount, with reads of some files made to fail (a file
// whose debrid copy broke reads nothing through the mount) and the mount's listing made to fail on demand.
type testLib struct {
	*library.Library
	mu sync.Mutex
	// broken: base name -> failed reads left (-1: every read fails).
	broken map[string]int
	listed error
	reads  []string
	// hang, when set, makes the next read wait for it (or its context) and then fail as a time-out.
	hang chan struct{}
}

func (l *testLib) ReadPiece(ctx context.Context, target string, off, n int64, limit time.Duration) (int64, error) {
	l.mu.Lock()
	b := path.Base(target)
	l.reads = append(l.reads, b)
	left, broken := l.broken[b]
	if broken && left > 0 {
		l.broken[b]--
	}
	hang := l.hang
	l.hang = nil
	l.mu.Unlock()
	if hang != nil {
		select {
		case <-hang:
		case <-ctx.Done():
		}
		return 0, fmt.Errorf("%w: no answer in %v", library.ErrPieceUnreadable, limit)
	}
	if broken && left != 0 {
		return 0, fmt.Errorf("%w: input/output error", library.ErrPieceUnreadable)
	}
	return l.Library.ReadPiece(ctx, target, off, n, limit)
}

func (l *testLib) MountListed(ctx context.Context) error {
	l.mu.Lock()
	err := l.listed
	l.mu.Unlock()
	if err != nil {
		return err
	}
	return l.Library.MountListed(ctx)
}

func (l *testLib) setBroken(name string, n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.broken[name] = n
}

func (l *testLib) readsOf(name string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, r := range l.reads {
		if r == name {
			n++
		}
	}
	return n
}

// stoppingPlex is the fake Plex with the terminate call: it records each stop (session id and reason).
type stoppingPlex struct {
	*fakePlex
	mu    sync.Mutex
	stops []string
	err   error
}

func (p *stoppingPlex) Terminate(_ context.Context, id, reason string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stops = append(p.stops, id+": "+reason)
	return p.err
}

func (p *stoppingPlex) stopped() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.stops...)
}

type rescueEnv struct {
	t     *testing.T
	stop  *stoppingPlex
	ctx   context.Context
	st    *store.Store
	lib   *testLib
	tr    *Tracker
	plex  *fakePlex
	zurg  *fakeZurg
	mount string
	pol   *policy.Policy
	mu    sync.Mutex
	clock time.Time
	// copyOK says which accounts the release can be added to cached; copies are the accounts asked.
	copyOK   map[string]bool
	copies   []string
	outcomes []outcome.Outcome
}

const fileSize = 4 << 20

// newRescueEnv: series Show (version tv, providers rd then ad) with a three-episode release Rel (hash a…, held
// complete by rd) linked by Theatarr; two other releases on the mount, Other (held only by rd, a film) and OtherAD
// (held only by ad, another film). client makes tv a client-routed (decypharr) version; zurg gives the tracker a zurg
// API.
func newRescueEnv(t *testing.T, client, zurg bool) *rescueEnv {
	t.Helper()
	e := &rescueEnv{t: t, ctx: context.Background(), clock: time.Date(2026, 11, 21, 20, 0, 0, 0, time.UTC),
		copyOK: map[string]bool{}}
	dir := t.TempDir()
	e.mount = filepath.Join(dir, "zurg")
	shows := filepath.Join(dir, "media", "tv")
	for rel, names := range map[string][]string{"Rel": {"S01E01.mkv", "S01E02.mkv", "S01E03.mkv"},
		"Other": {"film.mkv"}, "OtherAD": {"film2.mkv"}} {
		if err := os.MkdirAll(filepath.Join(e.mount, "__all__", rel), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			p := filepath.Join(e.mount, "__all__", rel, n)
			if err := os.WriteFile(p, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(p, fileSize); err != nil {
				t.Fatal(err)
			}
		}
	}
	st, err := store.Open(e.ctx, storetest.Seed(t, filepath.Join(dir, "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.st = st
	tgt := func(rel string) string { return filepath.Join(e.mount, "__all__", rel) }
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, year, created_at, updated_at) VALUES (1, 'series', 'Show', 2020, 0, 0),
			(2, 'movie', 'Film', 2019, 0, 0), (3, 'movie', 'Film Two', 2018, 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (1, 1, 'tv', 'filled', 0, 0),
			(2, 2, 'movie', 'filled', 0, 0), (3, 3, 'movie', 'filled', 0, 0)`,
		`INSERT INTO episode (id, title_id, season, number, name, air_date_utc_at, created_at, updated_at) VALUES
			(1, 1, 1, 1, 'One', 1, 0, 0), (2, 1, 1, 2, 'Two', 2, 0, 0), (3, 1, 1, 3, 'Three', 3, 0, 0)`,
		`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, cutoff_met, updated_at)
			VALUES (1, 1, 1, 'filled', 0, 0), (1, 2, 1, 'filled', 0, 0), (1, 3, 1, 'filled', 0, 0)`,
		`INSERT INTO content (id, info_hash, name, mount_folder, first_seen_at, last_seen_at) VALUES
			(50, '` + strings.Repeat("a", 40) + `', 'Rel', 'Rel', 0, 0),
			(60, '` + strings.Repeat("b", 40) + `', 'Other', 'Other', 0, 0),
			(70, '` + strings.Repeat("c", 40) + `', 'OtherAD', 'OtherAD', 0, 0)`,
		`INSERT INTO provider_torrent (provider, torrent_id, info_hash, progress, state) VALUES
			('rd', 'r50', '` + strings.Repeat("a", 40) + `', 100, 'downloaded'),
			('rd', 'r60', '` + strings.Repeat("b", 40) + `', 100, 'downloaded'),
			('ad', 'a70', '` + strings.Repeat("c", 40) + `', 100, 'downloaded')`,
		`INSERT INTO content_file (id, content_id, path, size_bytes) VALUES (100, 50, 'S01E01.mkv', 1),
			(101, 50, 'S01E02.mkv', 1), (102, 50, 'S01E03.mkv', 1), (200, 60, 'film.mkv', 1), (300, 70, 'film2.mkv', 1)`,
	} {
		e.exec(q)
	}
	for i, ep := range []int64{1, 2, 3} {
		name := fmt.Sprintf("S01E0%d.mkv", ep)
		e.exec(`INSERT INTO link (id, path, target, content_file_id, title_id, release_title, quality, state, created_at,
			updated_at) VALUES (?, ?, ?, ?, 1, 'Show.S01.1080p.WEB-DL-GRP', 'WEBDL-1080p', 'active', 0, 0)`,
			10+i, filepath.Join(shows, "Show", name), tgt("Rel/"+name), 100+i)
		e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (?, 1)`, 10+i)
		if err := os.MkdirAll(filepath.Join(shows, "Show"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(tgt("Rel/"+name), filepath.Join(shows, "Show", name)); err != nil {
			t.Fatal(err)
		}
		e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (?, ?)`, 10+i, ep)
	}
	e.exec(`INSERT INTO link (id, path, target, content_file_id, title_id, release_title, state, created_at, updated_at)
		VALUES (20, '/media/movies/Film.mkv', ?, 200, 2, 'Film', 'active', 0, 0),
		(21, '/media/movies/Film Two.mkv', ?, 300, 3, 'Film Two', 'active', 0, 0)`, tgt("Other/film.mkv"),
		tgt("OtherAD/film2.mkv"))
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (20, 2), (21, 3)`)
	vp := &policy.VersionPolicy{Kind: policy.KindSeries, Profile: "p", Library: shows, Storage: policy.StorageDebrid,
		Providers: []string{"rd", "ad"}}
	if client {
		vp.AddVia, vp.Client, vp.Providers = policy.AddViaClient, "dc", nil
	}
	e.pol = &policy.Policy{
		Versions: map[string]*policy.VersionPolicy{"tv": vp},
		Profiles: map[string]*policy.Profile{"p": {Qualities: []policy.QualityItem{{Name: "WEBDL-1080p"}},
			Cutoff: "WEBDL-1080p"}},
		Library: &policy.Library{Enabled: true, Mount: policy.LibraryMount{Path: e.mount},
			Versions: map[string]*policy.LibraryVersion{"tv": {Roots: []string{shows}}}},
		Router: &policy.Router{Enabled: true},
	}
	q := jobs.NewQueue(st)
	q.SetClock(e.now)
	lib := library.New(library.Options{Store: st, Policy: e.pol, Queue: q, Now: e.now,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	lib.OnPlayingOutcome(func(o outcome.Outcome) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.outcomes = append(e.outcomes, o)
	})
	e.lib = &testLib{Library: lib, broken: map[string]int{}}
	e.plex = &fakePlex{}
	e.stop = &stoppingPlex{fakePlex: e.plex}
	o := Options{Plex: e.stop, Store: st, Mount: func() Mount { return e.lib }, LinkPath: e.mount, MountPath: e.mount,
		Now: e.now, Readlink: func(p string) (string, error) { // the symlinks on disk, under the library root only
			if strings.HasPrefix(p, shows+"/") {
				return os.Readlink(p)
			}
			return "", errors.New("not a link")
		},
		Roots: func(k string) []string {
			if k == "tv" {
				return []string{shows}
			}
			return nil
		},
		Copy: func(_ context.Context, hash, pk string) (bool, error) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.copies = append(e.copies, pk)
			return e.copyOK[pk], nil
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if zurg {
		e.zurg = &fakeZurg{stats: &library.ZurgStats{}}
		o.Zurg = e.zurg
	}
	e.tr = New(o)
	e.tr.sleep = func(ctx context.Context, d time.Duration) error {
		e.advance(d)
		return ctx.Err()
	}
	return e
}

func (e *rescueEnv) now() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.clock
}

// settle runs the library's settle pass, as its maintenance does every 30 s.
func (e *rescueEnv) settle() {
	e.t.Helper()
	if err := e.lib.SettlePlayingRuns(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}

func (e *rescueEnv) advance(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.clock = e.clock.Add(d)
}

func (e *rescueEnv) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.st.Writer().ExecContext(e.ctx, q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *rescueEnv) one(q string, args ...any) string {
	e.t.Helper()
	var s sql.NullString
	if err := e.st.Reader().QueryRowContext(e.ctx, q, args...).Scan(&s); err != nil && !errors.Is(err, sql.ErrNoRows) {
		e.t.Fatalf("%s: %v", q, err)
	}
	return s.String
}

// play makes Plex report ann watching episode ep of Show on the TV, at 20% of it, in state.
func (e *rescueEnv) play(ep int, state string) {
	e.plex.mu.Lock()
	defer e.plex.mu.Unlock()
	e.plex.ss = []plex.Session{{SessionKey: "s1", ID: "pid1", UserID: "7", RatingKey: fmt.Sprint(ep), Type: "episode", Title: "Ep",
		GrandparentTitle: "Show", ParentIndex: 1, Index: ep, File: e.linkPath(ep), Size: fileSize,
		Duration: 3_000_000, ViewOffset: 600_000, State: state, UserName: "ann", Player: "TV"}}
}

func (e *rescueEnv) stopPlaying() {
	e.plex.mu.Lock()
	defer e.plex.mu.Unlock()
	e.plex.ss = nil
}

func (e *rescueEnv) linkPath(ep int) string {
	return e.one(`SELECT path FROM link WHERE id = ?`, 9+ep)
}

// poll runs one poll and waits for the job it gave the worker.
func (e *rescueEnv) poll() {
	e.t.Helper()
	e.tr.poll(e.ctx)
	e.waitIdle()
}

func (e *rescueEnv) waitIdle() {
	e.t.Helper()
	for range 1000 {
		e.tr.mu.Lock()
		busy := e.tr.rescuing
		e.tr.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	e.t.Fatal("the check did not end")
}

func (e *rescueEnv) playingJobs() []router.ResearchRequest {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT payload FROM job WHERE kind = ? ORDER BY id`, router.JobResearch)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []router.ResearchRequest
	for rows.Next() {
		var s string
		var p router.ResearchRequest
		if rows.Scan(&s) == nil && json.Unmarshal([]byte(s), &p) == nil {
			out = append(out, p)
		}
	}
	return out
}

func (e *rescueEnv) kinds() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var ks []string
	for _, o := range e.outcomes {
		ks = append(ks, o.Kind)
	}
	return strings.Join(ks, ",")
}

// GOAL a: on a zurg mount the playing file's reads fail, another file of its account reads, no account has the same
// release cached: the file is marked dead and searched for within two minutes of the sign, without the repair grace
// or the hourly sweep; the release's miss goes on the account that served it.
func TestRescueReplacesBrokenWatchedFile(t *testing.T) {
	e := newRescueEnv(t, false, true)
	e.tr.ZurgLine("INFO Large transfer: 512 MiB of __all__/Rel/S01E02.mkv from rd for 30s")
	e.lib.setBroken("S01E02.mkv", -1)
	e.play(2, "playing")
	e.zurg.stats.BlockedReads = []library.ZurgBlockedRead{{Filename: "S01E02.mkv", Reason: "broken"}}
	start := e.now()
	e.poll()
	if got := e.one(`SELECT state FROM link WHERE id = 11`); got != "dead" {
		t.Fatalf("watched link %s, want dead", got)
	}
	var broken int64
	_ = e.st.Reader().QueryRow(`SELECT broken_at FROM playing_break WHERE link_id = 11`).Scan(&broken)
	if took := time.UnixMilli(broken).Sub(start); took > 2*time.Minute || took <= 0 {
		t.Errorf("decided %v after the sign", took)
	}
	if jobs := e.playingJobs(); len(jobs) != 1 || jobs[0].Reason != router.ReasonPlaying || fmt.Sprint(jobs[0].Episodes) != "[2]" {
		t.Errorf("searches %+v", jobs)
	}
	if got := e.one(`SELECT provider || ' ' || source FROM provider_cache WHERE info_hash = ?`, strings.Repeat("a", 40)); got !=
		"rd router:playing" {
		t.Errorf("miss %s", got)
	}
	if got := e.one(`SELECT out_account || '|' || viewer || '|' || player FROM playing_break WHERE link_id = 11`); got != "|ann|TV" {
		t.Errorf("incident %s", got)
	}
	if e.lib.readsOf("film.mkv") != 1 {
		t.Errorf("control reads of the account's other release: %d", e.lib.readsOf("film.mkv"))
	}
	if got := e.one(`SELECT group_concat(state) FROM link WHERE id IN (20, 21)`); got != "active,active" {
		t.Errorf("titles nobody watches: %s", got)
	}
	if fmt.Sprint(e.copies) != "[ad]" {
		t.Errorf("step 1 asked %v", e.copies)
	}
}

// Step 1: the same release cached on another account: added there, the file reads again, nothing is marked, the
// incident settles "copy" and its outcome is same_release.
func TestRescueSameReleaseFromOtherAccount(t *testing.T) {
	e := newRescueEnv(t, false, true)
	e.tr.ZurgLine("INFO Large transfer: 512 MiB of __all__/Rel/S01E02.mkv from rd for 30s")
	e.lib.setBroken("S01E02.mkv", 3)
	e.copyOK["ad"] = true
	e.play(2, "buffering")
	e.poll()
	if got := e.one(`SELECT state FROM link WHERE id = 11`); got != "active" {
		t.Errorf("watched link %s", got)
	}
	if got := e.one(`SELECT settled || ' ' || fixed_by || ' ' || reported FROM playing_break WHERE link_id = 11`); got !=
		"copy copy same_release" {
		t.Errorf("incident %s", got)
	}
	if e.kinds() != outcome.SameRelease || len(e.playingJobs()) != 0 {
		t.Errorf("outcomes %s, searches %+v", e.kinds(), e.playingJobs())
	}
	e.mu.Lock()
	o := e.outcomes[0]
	e.mu.Unlock()
	if o.Title != "Show (2020)" || fmt.Sprint(o.Episodes) != "[S01E02]" || o.Viewer != "ann" || o.Account != "" ||
		o.OldRelease != "Show.S01.1080p.WEB-DL-GRP" {
		t.Errorf("outcome %+v", o)
	}
}

// GOAL b: a mount with no zurg API and no zurg log, on a version that adds through a client (decypharr): Plex's
// buffering is the only sign; no step 1 (Theatarr does not choose the client's account), the miss goes on the
// client's key, and the file is searched for at once.
func TestRescueWithoutZurgOnClientVersion(t *testing.T) {
	e := newRescueEnv(t, true, false)
	e.lib.setBroken("S01E02.mkv", -1)
	e.play(2, "buffering")
	e.poll()
	if got := e.one(`SELECT state FROM link WHERE id = 11`); got != "dead" {
		t.Fatalf("watched link %s, want dead", got)
	}
	if len(e.copies) != 0 {
		t.Errorf("step 1 on a client version: %v", e.copies)
	}
	if got := e.one(`SELECT provider FROM provider_cache WHERE info_hash = ? AND source = 'router:playing'`,
		strings.Repeat("a", 40)); got != "client:dc" {
		t.Errorf("miss on %q, want client:dc", got)
	}
	if jobs := e.playingJobs(); len(jobs) != 1 || jobs[0].Reason != router.ReasonPlaying {
		t.Errorf("searches %+v", jobs)
	}
}

// GOAL k: a file that reads fine after a buffering sign (slow network, transcode): nothing is recorded or marked, and it
// is not read again within two minutes.
func TestRescueReadsFineNothingDone(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.play(2, "buffering")
	e.poll()
	e.poll()
	if got := e.one(`SELECT COUNT(*) FROM playing_break`); got != "0" {
		t.Errorf("%s incidents", got)
	}
	if got := e.one(`SELECT state FROM link WHERE id = 11`); got != "active" {
		t.Errorf("link %s", got)
	}
	if n := e.lib.readsOf("S01E02.mkv"); n != 1 {
		t.Errorf("%d reads, want 1 within two minutes", n)
	}
	e.advance(readFineFor)
	e.poll()
	if n := e.lib.readsOf("S01E02.mkv"); n != 2 {
		t.Errorf("%d reads after two minutes, want 2", n)
	}
}

// A restart-length outage (reads 1 and 2 fail, read 3 passes after the control read) changes nothing.
func TestRescueRestartLengthOutage(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.lib.setBroken("S01E02.mkv", 2)
	e.play(2, "buffering")
	e.poll()
	if got := e.one(`SELECT COUNT(*) FROM playing_break`); got != "0" {
		t.Errorf("%s incidents", got)
	}
	if n := e.lib.readsOf("S01E02.mkv"); n != 3 {
		t.Errorf("%d reads, want 3", n)
	}
	if got := e.one(`SELECT state FROM link WHERE id = 11`); got != "active" {
		t.Errorf("link %s", got)
	}
}

// GOAL d: the mount not healthy (its listing fails, or the control reads fail too): nothing is recorded or replaced.
func TestRescueMountUnhealthy(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.lib.setBroken("S01E02.mkv", -1)
	e.lib.listed = library.ErrMountDown
	e.play(2, "buffering")
	e.poll()
	e2 := newRescueEnv(t, false, false)
	for _, f := range []string{"S01E02.mkv", "film.mkv", "film2.mkv"} {
		e2.lib.setBroken(f, -1)
	}
	e2.play(2, "buffering")
	e2.poll()
	for i, x := range []*rescueEnv{e, e2} {
		if got := x.one(`SELECT COUNT(*) FROM playing_break`) + " " + x.one(`SELECT state FROM link WHERE id = 11`); got != "0 active" {
			t.Errorf("case %d: %s", i, got)
		}
	}
	if len(e2.copies) != 0 {
		t.Errorf("copied while the mount is down: %v", e2.copies)
	}
}

// GOAL d: one provider out (another release only it holds fails, one only another account holds reads): the incident
// names it, the same release is tried on the other account, then the file is searched for with that account skipped
// by its route (internal/router). A title nobody watches on the out account is not touched.
func TestRescueProviderOut(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.lib.setBroken("S01E02.mkv", -1)
	e.lib.setBroken("film.mkv", -1)
	e.play(2, "buffering")
	e.poll()
	if got := e.one(`SELECT out_account FROM playing_break WHERE link_id = 11`); got != "rd" {
		t.Errorf("out account %q", got)
	}
	if fmt.Sprint(e.copies) != "[ad]" || e.one(`SELECT state FROM link WHERE id = 11`) != "dead" {
		t.Errorf("copies %v, link %s", e.copies, e.one(`SELECT state FROM link WHERE id = 11`))
	}
	if got := e.one(`SELECT state FROM link WHERE id = 20`); got != "active" {
		t.Errorf("the out account's other title %s", got)
	}
	// An outage writes no miss: the incident's out account keeps the repair off it, and other libraries may use the
	// release there once the account is back.
	if got := e.one(`SELECT COUNT(*) FROM provider_cache WHERE source = 'router:playing'`); got != "0" {
		t.Errorf("%s misses after an outage", got)
	}
	// The library has no other account: nothing is replaced; the incident settles none, outcome unavailable.
	e2 := newRescueEnv(t, false, false)
	e2.pol.Versions["tv"].Providers = []string{"rd"}
	e2.lib.setBroken("S01E02.mkv", -1)
	e2.lib.setBroken("film.mkv", -1)
	e2.play(2, "buffering")
	e2.poll()
	if got := e2.one(`SELECT state FROM link WHERE id = 11`) + " " + e2.one(`SELECT settled FROM playing_break`); got != "active none" {
		t.Errorf("no other account: %s", got)
	}
	if e2.kinds() != outcome.Unavailable || len(e2.playingJobs()) != 0 {
		t.Errorf("outcomes %s, searches %d", e2.kinds(), len(e2.playingJobs()))
	}
}

// GOAL f: a pack. The release's other files are read once the watched one is handled; a broken one (read and retry
// both fail) is marked as found, a readable one is not; their search is queued together only when the watched file's
// search has ended, and then the outcome (unavailable: nothing imported) is sent once.
func TestRescuePackSiblings(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.lib.setBroken("S01E02.mkv", -1)
	e.lib.setBroken("S01E03.mkv", -1)
	e.lib.setBroken("S01E01.mkv", 1) // one failed read, then fine: not marked
	e.play(2, "buffering")
	e.poll()
	if got := e.one(`SELECT group_concat(state) FROM (SELECT state FROM link WHERE id IN (10, 11, 12) ORDER BY id)`); got !=
		"active,dead,dead" {
		t.Fatalf("links %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM playing_break WHERE incident = (SELECT id FROM playing_break WHERE link_id = 11)`); got != "2" {
		t.Errorf("incident rows %s", got)
	}
	if jobs := e.playingJobs(); len(jobs) != 1 {
		t.Fatalf("searches before the watched file's ended: %+v", jobs)
	}
	e.poll() // settling: the watched file's search is still queued
	if jobs := e.playingJobs(); len(jobs) != 1 || e.kinds() != "" {
		t.Fatalf("searches %+v, outcomes %s while the first search is pending", jobs, e.kinds())
	}
	e.exec(`UPDATE job SET state = 'done'`)
	e.settle() // the library's maintenance
	jobs := e.playingJobs()
	if len(jobs) != 2 || fmt.Sprint(jobs[1].Episodes) != "[3]" || jobs[1].Reason != router.ReasonPlaying {
		t.Errorf("searches %+v", jobs)
	}
	if e.kinds() != outcome.Unavailable {
		t.Errorf("outcomes %q", e.kinds())
	}
	e.exec(`UPDATE job SET state = 'done'`)
	e.settle()
	if len(e.playingJobs()) != 2 || e.kinds() != outcome.Unavailable {
		t.Errorf("settled again: %d searches, outcomes %q", len(e.playingJobs()), e.kinds())
	}
}

// GOAL g: the bound. A second break of the same title and library the same day takes the normal path: nothing is
// recorded or marked.
func TestRescueBound(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.lib.setBroken("S01E02.mkv", -1)
	e.play(2, "buffering")
	e.poll()
	e.advance(time.Hour)
	e.lib.setBroken("S01E03.mkv", -1)
	e.exec(`DELETE FROM playing_break WHERE link_id = 12`) // E03 was found with E02: start from a fresh one
	e.exec(`UPDATE link SET state = 'active' WHERE id = 12`)
	e.tr.mu.Lock()
	delete(e.tr.quiet, copyKey(12, filepath.Join(e.mount, "__all__", "Rel", "S01E03.mkv")))
	e.tr.mu.Unlock()
	e.play(3, "buffering")
	e.poll()
	if got := e.one(`SELECT state FROM link WHERE id = 12`); got != "active" {
		t.Errorf("second break the same day: link %s", got)
	}
	if got := e.one(`SELECT COUNT(DISTINCT incident) FROM playing_break`); got != "1" {
		t.Errorf("%s incidents", got)
	}
}

// GOAL c: a read that hangs is a failure after its time limit, and the tracker is not blocked meanwhile: polls and
// the page go on.
func TestRescueHangDoesNotBlockTheTracker(t *testing.T) {
	e := newRescueEnv(t, false, false)
	hang := make(chan struct{})
	e.lib.hang = hang
	e.lib.setBroken("S01E02.mkv", 2) // the hung read counts as one of these
	e.play(2, "buffering")
	e.tr.poll(e.ctx)
	done := make(chan struct{})
	go func() {
		e.tr.poll(e.ctx)
		_ = e.tr.Snapshot()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the poll waited for the hung read")
	}
	close(hang)
	e.waitIdle()
	// The hung read counted as the first failure; the second failed too; the third read fine: nothing done.
	if n := e.lib.readsOf("S01E02.mkv"); n != 3 {
		t.Errorf("%d reads, want 3", n)
	}
}

// A playback that ends early is a sign; a live session's check goes first; a file Plex plays through a path that is
// not Theatarr's link (an arr's own symlink) is never checked.
func TestRescueSignsAndCertainty(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.lib.setBroken("S01E03.mkv", -1)
	e.play(3, "playing")
	e.tr.poll(e.ctx)
	e.waitIdle()
	e.stopPlaying()
	e.poll() // ended at 20%: checked
	if got := e.one(`SELECT state FROM link WHERE id = 12`); got != "dead" {
		t.Errorf("after an early stop: %s", got)
	}
	e2 := newRescueEnv(t, false, false)
	e2.lib.setBroken("S01E02.mkv", -1)
	e2.play(2, "buffering")
	e2.plex.mu.Lock()
	e2.plex.ss[0].File = "/arr/tv/Show/S01E02.mkv"
	e2.plex.mu.Unlock()
	e2.tr.o.Readlink = func(string) (string, error) { return filepath.Join(e2.mount, "__all__", "Rel", "S01E02.mkv"), nil }
	e2.poll()
	if n := e2.lib.readsOf("S01E02.mkv"); n != 0 {
		t.Errorf("an arr's symlink was checked: %d reads", n)
	}
}

// The old copy comes back through the library's own maintenance, never through the worker (a live check or an owed
// message never waits behind come-back reads): polls after sweeps read nothing of it; the library's come-back pass,
// read on two sweeps an hour apart with nothing else holding its episode, revives it (the uncovered case).
func TestRescueComeBackAfterSweep(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.lib.setBroken("S01E02.mkv", -1)
	e.play(2, "buffering")
	e.poll()
	if got := e.one(`SELECT state FROM link WHERE id = 11`); got != "dead" {
		t.Fatalf("link %s", got)
	}
	e.stopPlaying()
	e.lib.setBroken("S01E02.mkv", 0) // zurg repaired it
	for range 2 {
		if _, err := e.lib.Relink(e.ctx, "", false); err != nil {
			t.Fatal(err)
		}
		if got := e.one(`SELECT state FROM link WHERE id = 11`); got != "dead" {
			t.Fatalf("the sweep revived it by listing: %s", got)
		}
		e.advance(time.Minute)
		e.poll()
		e.advance(time.Hour)
	}
	if got := e.one(`SELECT state FROM link WHERE id = 11`) + " " + e.one(`SELECT back_since IS NULL FROM playing_break WHERE link_id = 11`); got != "dead 1" {
		t.Fatalf("the worker read the old copy after the sweeps: %s", got)
	}
	for range 2 {
		if err := e.lib.ComeBack(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.advance(time.Hour)
	}
	if got := e.one(`SELECT state FROM link WHERE id = 11`) + " " + e.one(`SELECT settled FROM playing_break WHERE link_id = 11`); got != "active revived" {
		t.Errorf("after two sweeps: %s", got)
	}
}

// GOAL i: with the session open, the player is stopped once the break is confirmed, with the message that promises
// nothing, and the incident keeps what was said.
func TestRescueStopsTheStreamWithAMessage(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.lib.setBroken("S01E02.mkv", -1)
	e.play(2, "buffering")
	e.poll()
	if got := e.stop.stopped(); len(got) != 1 || got[0] != "pid1: "+msgLooking {
		t.Errorf("stops %q", got)
	}
	if got := e.one(`SELECT message FROM playing_break WHERE link_id = 11`); got != "the player was stopped with the message: "+msgLooking {
		t.Errorf("incident message %q", got)
	}
	// A reason for not stopping never replaces what was done.
	id, _ := strconv.ParseInt(e.one(`SELECT id FROM playing_break WHERE link_id = 11`), 10, 64)
	e.tr.notStopped(context.Background(), id, notStoppedEnded)
	if got := e.one(`SELECT message FROM playing_break WHERE link_id = 11`); got != StoppedNote+msgLooking {
		t.Errorf("stop result overwritten: %q", got)
	}
}

// Step 1 with a candidate is tried for 30 s before the message: a file that reads again within it gets no message at
// all; one still failing then gets the message once.
func TestRescueMessageWaitsForStepOne(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.lib.setBroken("S01E02.mkv", 3)
	e.copyOK["ad"] = true
	e.play(2, "buffering")
	e.poll()
	if got := e.stop.stopped(); len(got) != 0 {
		t.Errorf("readable again after the copy, yet stopped: %q", got)
	}
	if got := e.one(`SELECT message FROM playing_break WHERE link_id = 11`); got != NotStoppedNote+notStoppedRead {
		t.Errorf("readable again: note %q", got)
	}
	e2 := newRescueEnv(t, false, false)
	e2.lib.setBroken("S01E02.mkv", -1)
	e2.copyOK["ad"] = true
	e2.play(2, "buffering")
	e2.poll()
	if got := e2.stop.stopped(); len(got) != 1 || got[0] != "pid1: "+msgLooking {
		t.Errorf("stops %q", got)
	}
	if got := e2.one(`SELECT state FROM link WHERE id = 11`); got != "dead" {
		t.Errorf("link %s after step 1 failed", got)
	}
}

// The setting off: nothing is ever sent to Plex. No session left (an early stop): nothing is sent either. A paused
// playback is never stopped.
func TestRescueMessageSettingAndSession(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.tr.o.StopWithMessage = func() bool { return false }
	e.lib.setBroken("S01E02.mkv", -1)
	e.play(2, "buffering")
	e.poll()
	if got := e.stop.stopped(); len(got) != 0 || e.one(`SELECT state FROM link WHERE id = 11`) != "dead" {
		t.Errorf("setting off: stops %q", got)
	}
	if got := e.one(`SELECT message FROM playing_break WHERE link_id = 11`); got != NotStoppedNote+notStoppedOff {
		t.Errorf("setting off: note %q", got)
	}
	// A later reason (the file read again) does not replace the first.
	id, _ := strconv.ParseInt(e.one(`SELECT id FROM playing_break WHERE link_id = 11`), 10, 64)
	e.tr.notStopped(context.Background(), id, notStoppedRead)
	if got := e.one(`SELECT message FROM playing_break WHERE link_id = 11`); got != NotStoppedNote+notStoppedOff {
		t.Errorf("first reason replaced: %q", got)
	}
	e2 := newRescueEnv(t, false, false)
	e2.lib.setBroken("S01E03.mkv", -1)
	e2.play(3, "playing")
	e2.poll()
	e2.stopPlaying()
	e2.poll()
	if got := e2.stop.stopped(); len(got) != 0 || e2.one(`SELECT state FROM link WHERE id = 12`) != "dead" {
		t.Errorf("no session: stops %q", got)
	}
	if got := e2.one(`SELECT message FROM playing_break WHERE link_id = 12`); got != NotStoppedNote+notStoppedEnded {
		t.Errorf("no session: note %q", got)
	}
	e3 := newRescueEnv(t, false, false)
	e3.lib.setBroken("S01E02.mkv", -1)
	e3.play(2, "buffering")
	e3.tr.sleep = func(ctx context.Context, d time.Duration) error { // paused while the check reads
		e3.advance(d)
		e3.plex.mu.Lock()
		e3.plex.ss[0].State = "paused"
		e3.plex.mu.Unlock()
		return ctx.Err()
	}
	e3.poll()
	if got := e3.stop.stopped(); len(got) != 0 {
		t.Errorf("paused: stops %q", got)
	}
	if got := e3.one(`SELECT message FROM playing_break WHERE link_id = 11`); got != NotStoppedNote+notStoppedPaused {
		t.Errorf("paused: note %q", got)
	}
}

// An import that replaces the link during step 1, with a message owed: the incident reads replaced, and nothing says
// the file read again.
func TestRescueReplacedDuringStepOneNoReadAgainNote(t *testing.T) {
	e := newRescueEnv(t, false, true)
	e.lib.setBroken("S01E02.mkv", -1)
	e.copyOK["ad"] = true
	e.play(2, "playing")
	e.zurg.stats.BlockedReads = []library.ZurgBlockedRead{{Filename: "S01E02.mkv", Reason: "broken"}}
	imported := false
	e.tr.sleep = func(ctx context.Context, d time.Duration) error {
		e.advance(d)
		e.plex.mu.Lock()
		e.plex.ss[0].ViewOffset += d.Milliseconds()
		e.plex.mu.Unlock()
		e.tr.mu.Lock()
		owed := len(e.tr.pending) > 0
		e.tr.mu.Unlock()
		if owed && !imported { // the message is owed: an import takes the link's place meanwhile
			imported = true
			e.exec(`UPDATE link SET target = '/mnt/zurg/__all__/Other.Release/S01E02.mkv' WHERE id = 11`)
			e.exec(`UPDATE playing_break SET fixed_by = 'replaced', new_release = 'Other.Release' WHERE link_id = 11`)
		}
		return ctx.Err()
	}
	e.poll()
	if !imported {
		t.Fatal("no message was owed during step 1")
	}
	in := e.tr.Incidents()
	if len(in) != 1 || in[0].Status != "Replaced with another release" || strings.Contains(in[0].Message, notStoppedRead) {
		t.Errorf("incidents %+v", in)
	}
}

// A playback that ends while its message is owed is not stopped, and the incident says why.
func TestRescueOwedMessagePlaybackEnds(t *testing.T) {
	e := newRescueEnv(t, false, true)
	e.lib.setBroken("S01E02.mkv", -1)
	e.play(2, "playing")
	e.zurg.stats.BlockedReads = []library.ZurgBlockedRead{{Filename: "S01E02.mkv", Reason: "broken"}}
	e.tr.sleep = func(ctx context.Context, d time.Duration) error {
		e.advance(d)
		e.plex.mu.Lock()
		e.plex.ss[0].ViewOffset += d.Milliseconds()
		e.plex.mu.Unlock()
		return ctx.Err()
	}
	e.poll() // the message is owed
	if got := e.one(`SELECT message FROM playing_break WHERE link_id = 11`); got != "" {
		t.Fatalf("owed message already noted: %q", got)
	}
	e.stopPlaying()
	e.poll()
	if got := e.stop.stopped(); len(got) != 0 {
		t.Errorf("stopped %q", got)
	}
	if got := e.one(`SELECT message FROM playing_break WHERE link_id = 11`); got != NotStoppedNote+notStoppedEnded {
		t.Errorf("ended: note %q", got)
	}
}

// A playback still advancing when the message is due (what it cached plays on) is told at its next sign.
func TestRescueMessageAtNextSign(t *testing.T) {
	e := newRescueEnv(t, false, true)
	e.lib.setBroken("S01E02.mkv", -1)
	e.play(2, "playing")
	e.zurg.stats.BlockedReads = []library.ZurgBlockedRead{{Filename: "S01E02.mkv", Reason: "broken"}}
	e.tr.sleep = func(ctx context.Context, d time.Duration) error {
		e.advance(d)
		e.plex.mu.Lock()
		e.plex.ss[0].ViewOffset += d.Milliseconds()
		e.plex.mu.Unlock()
		return ctx.Err()
	}
	e.poll()
	if got := e.stop.stopped(); len(got) != 0 {
		t.Fatalf("stopped while advancing: %q", got)
	}
	// Still playing and advancing, with zurg's blocked read and its log line still remembered: no stop.
	for range 2 {
		e.plex.mu.Lock()
		e.plex.ss[0].ViewOffset += 10_000
		e.plex.mu.Unlock()
		e.advance(10 * time.Second)
		e.poll()
	}
	if got := e.stop.stopped(); len(got) != 0 {
		t.Fatalf("stopped a stream still advancing: %q", got)
	}
	// A rewind is movement too, not a stuck stream.
	e.plex.mu.Lock()
	e.plex.ss[0].ViewOffset -= 300_000
	e.plex.mu.Unlock()
	e.advance(10 * time.Second)
	e.poll()
	if got := e.stop.stopped(); len(got) != 0 {
		t.Fatalf("stopped a stream that was rewound: %q", got)
	}
	e.plex.mu.Lock()
	e.plex.ss[0].State = "buffering"
	e.plex.mu.Unlock()
	e.poll()
	if got := e.stop.stopped(); len(got) != 1 || got[0] != "pid1: "+msgLooking {
		t.Errorf("at the next sign: stops %q", got)
	}
}

// When nothing will be done (the day's bound reached) the message says to try later; a refusal by Plex is kept on the
// incident; nothing is sent when the mount is the problem or the file reads.
func TestRescueMessageLaterAndRefusal(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.stop.err = errors.New("plex: GET /status/sessions/terminate: HTTP 401")
	e.lib.setBroken("S01E02.mkv", -1)
	e.play(2, "buffering")
	e.poll()
	if got := e.one(`SELECT message FROM playing_break WHERE link_id = 11`); !strings.HasPrefix(got, "Plex refused to stop the player: ") {
		t.Errorf("incident message %q", got)
	}
	e.stop.err = nil
	e.exec(`DELETE FROM playing_break WHERE link_id = 12`)
	e.exec(`UPDATE link SET state = 'active' WHERE id = 12`)
	e.tr.mu.Lock()
	delete(e.tr.quiet, copyKey(12, filepath.Join(e.mount, "__all__", "Rel", "S01E03.mkv")))
	e.tr.mu.Unlock()
	e.lib.setBroken("S01E03.mkv", -1)
	e.advance(time.Hour)
	e.play(3, "buffering")
	e.poll()
	if got := e.stop.stopped(); len(got) != 2 || got[1] != "pid1: "+msgLater {
		t.Errorf("bound reached: stops %q", got)
	}
	e2 := newRescueEnv(t, false, false)
	e2.lib.setBroken("S01E02.mkv", -1)
	e2.lib.listed = library.ErrMountDown
	e2.play(2, "buffering")
	e2.poll()
	e3 := newRescueEnv(t, false, false)
	e3.play(2, "buffering")
	e3.poll()
	if len(e2.stop.stopped())+len(e3.stop.stopped()) != 0 {
		t.Errorf("mount down %q, file reads %q", e2.stop.stopped(), e3.stop.stopped())
	}
}

// Now playing lists the last day's incidents with what came of them.
func TestIncidents(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.lib.setBroken("S01E02.mkv", -1)
	e.lib.setBroken("S01E03.mkv", -1)
	e.play(2, "buffering")
	e.poll()
	in := e.tr.Incidents()
	if len(in) != 1 || in[0].Title != "Show (2020)" || fmt.Sprint(in[0].Episodes) != "[S01E02]" || in[0].Files != 2 ||
		in[0].Status != "Looking for another copy" || in[0].Viewer != "ann" || in[0].Player != "TV" ||
		in[0].OldRelease != "Show.S01.1080p.WEB-DL-GRP" || in[0].Message == "" {
		t.Fatalf("incidents %+v", in)
	}
	e.exec(`UPDATE playing_break SET episodes = '[3,1]' WHERE link_id = 11`)
	if in := e.tr.Incidents(); fmt.Sprint(in[0].Episodes) != "[S01E03 S01E01]" {
		t.Errorf("labels in the row's order: %v", in[0].Episodes)
	}
	e.exec(`UPDATE playing_break SET episodes = '[2]' WHERE link_id = 11`)
	e.exec(`UPDATE playing_break SET fixed_by = 'replaced', new_release = 'Show.S01E02.2160p' WHERE link_id = 11`)
	if in := e.tr.Incidents(); in[0].Status != "Replaced with another release" || in[0].NewRelease != "Show.S01E02.2160p" {
		t.Errorf("replaced %+v", in[0])
	}
	e.advance(25 * time.Hour)
	if in := e.tr.Incidents(); len(in) != 0 {
		t.Errorf("a day later %+v", in)
	}
	for _, c := range []struct{ settled, fixed, reported, want string }{
		{"copy", "copy", "same_release", "Readable again"}, {"none", "", "unavailable", "No other copy yet"},
		{"", "", "unavailable", "No other copy yet"}, {"restored", "replaced", "restored", "Earlier copy put back"},
	} {
		if _, got := incidentState(c.settled, c.fixed, c.reported); got != c.want {
			t.Errorf("%+v: %s", c, got)
		}
	}
}

// pauseDuring makes the settings in force Pause on while the check waits between its reads: a save builds a new
// library generation, which the tracker's Mount then returns.
func (e *rescueEnv) pauseDuring() {
	paused := *e.pol
	paused.Router = &policy.Router{Enabled: false}
	lib := library.New(library.Options{Store: e.st, Policy: &paused, Queue: jobs.NewQueue(e.st), Now: e.now,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	next := &testLib{Library: lib, broken: e.lib.broken}
	e.tr.sleep = func(ctx context.Context, d time.Duration) error {
		e.advance(d)
		e.tr.o.Mount = func() Mount { return next }
		return ctx.Err()
	}
}

// Pause saved during the confirmation: the check ends with nothing recorded, marked or said.
func TestRescuePauseDuringConfirmation(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.lib.setBroken("S01E02.mkv", -1)
	e.pauseDuring()
	e.play(2, "buffering")
	e.poll()
	if got := e.one(`SELECT state FROM link WHERE id = 11`) + " " + e.one(`SELECT COUNT(*) FROM playing_break`); got != "active 0" {
		t.Errorf("paused meanwhile: %s", got)
	}
	if got := e.stop.stopped(); len(got) != 0 {
		t.Errorf("stops %q", got)
	}
}

// A message still owed to an advancing playback is dropped when the file reads again (the same release from another
// account): a later buffering sign stops nothing.
func TestRescuePendingMessageDroppedWhenReadable(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.lib.setBroken("S01E02.mkv", 6) // reads 1-3, then the re-reads at 10, 20 and 30 s; the one at 40 s passes
	e.copyOK["ad"] = true
	e.play(2, "playing")
	e.plex.mu.Lock()
	e.plex.ss[0].State = "buffering"
	e.plex.mu.Unlock()
	e.tr.sleep = func(ctx context.Context, d time.Duration) error {
		e.advance(d)
		e.plex.mu.Lock()
		e.plex.ss[0].State = "playing"
		e.plex.ss[0].ViewOffset += d.Milliseconds()
		e.plex.mu.Unlock()
		return ctx.Err()
	}
	e.poll()
	if got := e.one(`SELECT settled FROM playing_break WHERE link_id = 11`); got != "copy" {
		t.Fatalf("incident %q", got)
	}
	e.plex.mu.Lock()
	e.plex.ss[0].State = "buffering"
	e.plex.mu.Unlock()
	e.poll()
	e.poll()
	if got := e.stop.stopped(); len(got) != 0 {
		t.Errorf("a readable file's stream was stopped: %q", got)
	}
}

// A replacement at the same path that breaks the same day is a new copy: checked again, and with the day's bound
// reached the viewer is told to try later, nothing else.
func TestRescueReplacementBreaksSameDay(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.lib.setBroken("S01E02.mkv", -1)
	e.play(2, "buffering")
	e.poll()
	e.stopPlaying()
	e.poll()
	// The replacement: the same path, now a file of another release, active.
	repl := filepath.Join(e.mount, "__all__", "OtherAD", "S01E02.mkv")
	if err := os.WriteFile(repl, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(repl, fileSize); err != nil {
		t.Fatal(err)
	}
	link := e.linkPath(2)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(repl, link); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE link SET target = ?, state = 'active' WHERE id = 11`, repl)
	e.advance(time.Hour)
	e.play(2, "buffering")
	e.poll()
	got := e.stop.stopped()
	if len(got) != 2 || got[1] != "pid1: "+msgLater {
		t.Errorf("stops %q", got)
	}
	if got := e.one(`SELECT state FROM link WHERE id = 11`) + " " + e.one(`SELECT COUNT(DISTINCT incident) FROM playing_break`); got != "active 1" {
		t.Errorf("second break: %s", got)
	}
}

// A path inside the library's root that is not Theatarr's tracked link (an arr's or an untracked symlink to the same
// file) is never checked: a match by target alone does not start the fast path.
func TestRescueUntrackedSymlinkInRoot(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.lib.setBroken("S01E02.mkv", -1)
	extra := filepath.Join(filepath.Dir(e.linkPath(2)), "Show - S01E02 (arr).mkv")
	if err := os.Symlink(filepath.Join(e.mount, "__all__", "Rel", "S01E02.mkv"), extra); err != nil {
		t.Fatal(err)
	}
	e.play(2, "buffering")
	e.plex.mu.Lock()
	e.plex.ss[0].File = extra
	e.plex.mu.Unlock()
	e.poll()
	if n := e.lib.readsOf("S01E02.mkv"); n != 0 || e.one(`SELECT state FROM link WHERE id = 11`) != "active" {
		t.Errorf("an untracked symlink was checked: %d reads", n)
	}
}

// zurg names the serving account and the release is complete on two accounts, with no other release held only by the
// serving one: the serving account is kept. Another release reads, so only this release on that account is broken;
// the other holder stays a candidate for the same release (step 1), and only the serving account gets the miss.
func TestRescueKeepsKnownServingAccount(t *testing.T) {
	e := newRescueEnv(t, false, true)
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	e.exec(`INSERT INTO provider_torrent (provider, torrent_id, info_hash, progress, state) VALUES
		('ad', 'a50', ?, 100, 'downloaded'), ('ad', 'a60', ?, 100, 'downloaded'),
		('tb', 't50', ?, 100, 'downloading')`, a, b, a)
	e.pol.Versions["tv"].Providers = []string{"rd", "ad", "tb"}
	e.play(2, "playing")
	e.poll()
	e.tr.ZurgLine("INFO Large transfer: 512 MiB of __all__/Rel/S01E02.mkv from rd for 30s")
	e.lib.setBroken("S01E02.mkv", -1)
	e.play(2, "buffering")
	e.poll()
	if fmt.Sprint(e.copies) != "[ad tb]" {
		t.Errorf("step 1 asked %v, want the other holder and the account still downloading it", e.copies)
	}
	if got := e.one(`SELECT group_concat(provider) FROM provider_cache WHERE info_hash = ? AND source = 'router:playing'`, a); got != "rd" {
		t.Errorf("misses on %q", got)
	}
	if got := e.one(`SELECT state FROM link WHERE id = 11`); got != "dead" {
		t.Errorf("link %s", got)
	}
}

// A same-path import replaces the file while step 1 waits: the copy that was read is no longer the link's, so nothing
// is marked, no miss is written and the viewer is told nothing.
func TestRescueCopyReplacedDuringStepOne(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.lib.setBroken("S01E02.mkv", -1)
	e.copyOK["ad"] = true
	repl := filepath.Join(e.mount, "__all__", "OtherAD", "S01E02.mkv")
	if err := os.WriteFile(repl, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(repl, fileSize); err != nil {
		t.Fatal(err)
	}
	waits := 0
	e.tr.sleep = func(ctx context.Context, d time.Duration) error {
		e.advance(d)
		if waits++; waits == 2 { // the first wait of step 1: the import lands
			e.exec(`UPDATE link SET target = ? WHERE id = 11`, repl)
		}
		return ctx.Err()
	}
	e.play(2, "buffering")
	e.poll()
	if got := e.one(`SELECT state FROM link WHERE id = 11`) + " " +
		e.one(`SELECT COUNT(*) FROM provider_cache WHERE source = 'router:playing'`) + " " +
		e.one(`SELECT COUNT(*) FROM library_action WHERE kind = 'dead'`); got != "active 0 0" {
		t.Errorf("after the replacement: %s", got)
	}
	if got := e.stop.stopped(); len(got) != 0 {
		t.Errorf("stops %q", got)
	}
	if len(e.playingJobs()) != 0 {
		t.Errorf("searches %+v", e.playingJobs())
	}
}

// Other releases without a stored hash (taken-over files) still serve the control read: their hash is the one the
// registry records for their folder.
func TestRescueControlReadByRegisteredFolder(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.exec(`UPDATE link SET content_file_id = NULL WHERE id IN (20, 21)`)
	e.lib.setBroken("S01E02.mkv", -1)
	e.play(2, "buffering")
	e.poll()
	if got := e.one(`SELECT state FROM link WHERE id = 11`); got != "dead" || e.lib.readsOf("film.mkv") != 1 {
		t.Errorf("link %s, control reads %d", got, e.lib.readsOf("film.mkv"))
	}
}

// No release is held by the serving account alone, so any other release is read; when that one fails the check ends
// without a verdict. An outage is never concluded from it, even when a release only another account holds reads.
// (The other release is picked at random: several runs cover both picks.)
func TestRescueNoFalseOutage(t *testing.T) {
	for i := range 12 {
		e := newRescueEnv(t, false, true)
		b := strings.Repeat("b", 40)
		e.exec(`INSERT INTO provider_torrent (provider, torrent_id, info_hash, progress, state) VALUES
			('ad', 'a60', ?, 100, 'downloaded')`, b)
		e.play(2, "playing")
		e.poll()
		e.tr.ZurgLine("INFO Large transfer: 512 MiB of __all__/Rel/S01E02.mkv from rd for 30s")
		e.lib.setBroken("S01E02.mkv", -1)
		e.lib.setBroken("film.mkv", -1) // the release on rd and ad fails; the AD-only one reads
		e.play(2, "buffering")
		e.poll()
		out := e.one(`SELECT out_account FROM playing_break WHERE link_id = 11`)
		if out != "" {
			t.Fatalf("run %d: outage concluded on %q", i, out)
		}
		if e.lib.readsOf("film.mkv") > 0 && e.lib.readsOf("film2.mkv") > 0 {
			t.Fatalf("run %d: a second control read after the fallback failed", i)
		}
		if e.lib.readsOf("film.mkv") > 0 && e.one(`SELECT COUNT(*) FROM playing_break`) != "0" {
			t.Fatalf("run %d: an incident after the fallback control failed", i)
		}
	}
}

// A same-path import lands between the confirming reads: the check read the old copy, so it ends with nothing done,
// no incident against the new file, and no copy of the new release.
func TestRescueImportBetweenReads(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.lib.setBroken("S01E02.mkv", -1)
	e.copyOK["ad"] = true
	repl := filepath.Join(e.mount, "__all__", "OtherAD", "S01E02.new.mkv")
	if err := os.WriteFile(repl, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(repl, fileSize); err != nil {
		t.Fatal(err)
	}
	e.tr.sleep = func(ctx context.Context, d time.Duration) error {
		e.advance(d)
		e.exec(`UPDATE link SET target = ? WHERE id = 11`, repl) // during the 20 s between reads 1 and 2
		return ctx.Err()
	}
	e.play(2, "buffering")
	e.poll()
	if got := e.one(`SELECT COUNT(*) FROM playing_break`) + " " + e.one(`SELECT state FROM link WHERE id = 11`); got != "0 active" {
		t.Errorf("after an import between the reads: %s", got)
	}
	if len(e.copies) != 0 || len(e.stop.stopped()) != 0 {
		t.Errorf("copies %v, stops %q", e.copies, e.stop.stopped())
	}
}

// The account is named only with evidence of who served the file again: zurg logging a transfer of that very file
// (its release folder and name) after the copy, here from the original account, recovered. A transfer of another
// release's file of the same name is no evidence.
func TestRescueSameReleaseAccountEvidence(t *testing.T) {
	for _, c := range []struct{ line, want string }{
		{"INFO Large transfer: 512 MiB of __all__/Rel/S01E02.mkv from rd for 30s", "rd"},
		{"INFO Large transfer: 512 MiB of __all__/Another.Show/S01E02.mkv from tb for 30s", ""},
		{"INFO Large transfer: 512 MiB of __all__/Other/Rel/S01E02.mkv from tb for 30s", ""},
		{"INFO Large transfer: 512 MiB of Rel/S01E02.mkv from rd for 30s", "rd"},
	} {
		e := newRescueEnv(t, false, true)
		e.lib.setBroken("S01E02.mkv", 4) // reads 1-3 and the first re-read; the next passes
		e.copyOK["ad"] = true
		waits := 0
		e.tr.sleep = func(ctx context.Context, d time.Duration) error {
			e.advance(d)
			if waits++; waits == 3 { // during step 1
				e.tr.ZurgLine(c.line)
			}
			return ctx.Err()
		}
		e.play(2, "buffering")
		e.poll()
		e.mu.Lock()
		if len(e.outcomes) != 1 || e.outcomes[0].Kind != outcome.SameRelease || e.outcomes[0].Account != c.want {
			t.Errorf("%s: outcomes %+v, want account %q", c.line, e.outcomes, c.want)
		}
		e.mu.Unlock()
	}
}

// A holder counts only with a complete record (state downloaded at 100 %): an account still downloading the release
// at 100 % is not one, so the release's only complete holder is the serving account, and the other account stays a
// candidate for step 1.
func TestRescueCompleteHolderOnly(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.exec(`INSERT INTO provider_torrent (provider, torrent_id, info_hash, progress, state) VALUES
		('tb', 't50', ?, 100, 'downloading')`, strings.Repeat("a", 40))
	e.pol.Versions["tv"].Providers = []string{"rd", "ad", "tb"}
	e.lib.setBroken("S01E02.mkv", -1)
	e.play(2, "buffering")
	e.poll()
	if fmt.Sprint(e.copies) != "[ad tb]" {
		t.Errorf("step 1 asked %v", e.copies)
	}
	if got := e.one(`SELECT group_concat(provider) FROM provider_cache WHERE source = 'router:playing'`); got != "rd" {
		t.Errorf("misses on %q", got)
	}
}

// A check cut short (here Pause saved during step 1) leaves nothing behind: no incident row, nothing in the list, the
// day's bound unused, and the file checked again at its next sign once Pause is off.
func TestRescueCutShortLeavesNothing(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.lib.setBroken("S01E02.mkv", -1)
	e.copyOK["ad"] = true
	live := e.tr.o.Mount
	e.pauseDuring()
	paused := e.tr.sleep
	waits := 0
	e.tr.sleep = func(ctx context.Context, d time.Duration) error {
		if waits++; waits < 3 { // Pause comes on during step 1, after the incident was recorded
			e.advance(d)
			return ctx.Err()
		}
		return paused(ctx, d)
	}
	e.play(2, "buffering")
	e.poll()
	if got := e.one(`SELECT COUNT(*) FROM playing_break`); got != "0" || len(e.tr.Incidents()) != 0 {
		t.Fatalf("%s rows, incidents %+v", got, e.tr.Incidents())
	}
	e.tr.o.Mount = live
	e.tr.sleep = func(ctx context.Context, d time.Duration) error { e.advance(d); return ctx.Err() }
	e.copyOK["ad"] = false
	e.poll()
	if got := e.one(`SELECT state FROM link WHERE id = 11`) + " " + e.one(`SELECT COUNT(*) FROM playing_break`); got != "dead 1" {
		t.Errorf("the next break after Pause: %s", got)
	}
}

// Plex reports a playing position only every ten seconds or so: an owed message is not sent on a second sample taken
// too soon after the first that shows the same position; only once the position has stayed the same for
// stillFor. A buffering sample sends it at once (TestRescueMessageAtNextSign).
func TestRescueOwedMessageWaitsForReportingInterval(t *testing.T) {
	e := newRescueEnv(t, false, true)
	e.lib.setBroken("S01E02.mkv", -1)
	e.play(2, "playing")
	e.zurg.stats.BlockedReads = []library.ZurgBlockedRead{{Filename: "S01E02.mkv", Reason: "broken"}}
	e.tr.sleep = func(ctx context.Context, d time.Duration) error {
		e.advance(d)
		e.plex.mu.Lock()
		e.plex.ss[0].ViewOffset += d.Milliseconds()
		e.plex.mu.Unlock()
		return ctx.Err()
	}
	e.poll() // the check: the message is owed, the stream still advancing
	e.poll() // a poke right after: the same position, too soon to say
	e.advance(6 * time.Second)
	e.poll()
	if got := e.stop.stopped(); len(got) != 0 {
		t.Fatalf("stopped on samples too close together: %q", got)
	}
	e.advance(stillFor)
	e.poll()
	if got := e.stop.stopped(); len(got) != 1 {
		t.Errorf("not stopped after the position stayed the same for %v: %q", stillFor, got)
	}
}

// A plain mount with no stored hashes and no registry rows: the control read tells releases apart by their folder on
// the mount, so the break is still judged (no copy step without a hash).
func TestRescueControlByFolderWithoutHashes(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.exec(`UPDATE link SET content_file_id = NULL`)
	e.exec(`DELETE FROM provider_torrent`)
	e.exec(`DELETE FROM content_file`)
	e.exec(`DELETE FROM content`)
	e.lib.setBroken("S01E02.mkv", -1)
	e.play(2, "buffering")
	e.poll()
	if got := e.one(`SELECT state FROM link WHERE id = 11`); got != "dead" {
		t.Errorf("link %s", got)
	}
	if len(e.copies) != 0 {
		t.Errorf("copied without a hash: %v", e.copies)
	}
}

// Time paused does not count toward stillFor: a stream that resumes at the same position after a pause is not
// stopped on its first sample, only after stillFor of playing without moving.
func TestRescueOwedMessagePauseRestarts(t *testing.T) {
	e := newRescueEnv(t, false, true)
	e.lib.setBroken("S01E02.mkv", -1)
	e.play(2, "playing")
	e.zurg.stats.BlockedReads = []library.ZurgBlockedRead{{Filename: "S01E02.mkv", Reason: "broken"}}
	e.tr.sleep = func(ctx context.Context, d time.Duration) error {
		e.advance(d)
		e.plex.mu.Lock()
		e.plex.ss[0].ViewOffset += d.Milliseconds()
		e.plex.mu.Unlock()
		return ctx.Err()
	}
	e.poll() // the message is owed
	state := func(s string) {
		e.plex.mu.Lock()
		e.plex.ss[0].State = s
		e.plex.mu.Unlock()
	}
	state("paused")
	e.poll()
	e.advance(30 * time.Second)
	e.poll()
	state("playing")
	e.advance(time.Second)
	e.poll()
	if got := e.stop.stopped(); len(got) != 0 {
		t.Fatalf("stopped on resume after a pause: %q", got)
	}
	e.advance(stillFor)
	e.poll()
	if got := e.stop.stopped(); len(got) != 1 {
		t.Errorf("not stopped after %v playing without moving: %q", stillFor, got)
	}
}

// Pause saved during step 1's wait, then a read that passes: the check is cut short (nothing kept), not settled as
// readable again.
func TestRescuePauseDuringStepOneWait(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.lib.setBroken("S01E02.mkv", 3) // reads 1-3 fail; the re-read after step 1's first wait passes
	e.copyOK["ad"] = true
	e.pauseDuring()
	paused := e.tr.sleep
	waits := 0
	e.tr.sleep = func(ctx context.Context, d time.Duration) error {
		if waits++; waits < 2 {
			e.advance(d)
			return ctx.Err()
		}
		return paused(ctx, d) // Pause comes on during step 1's wait
	}
	e.play(2, "buffering")
	e.poll()
	if got := e.one(`SELECT COUNT(*) FROM playing_break`); got != "0" || e.kinds() != "" {
		t.Errorf("%s rows, outcomes %q", got, e.kinds())
	}
}

// A message that came due while the worker was busy is judged again at every sample: after a pause, playing samples
// that advance make it not due, and nothing is stopped.
func TestRescueOwedMessageDueIsNotSticky(t *testing.T) {
	e := newRescueEnv(t, false, true)
	e.lib.setBroken("S01E02.mkv", -1)
	e.play(2, "playing")
	e.zurg.stats.BlockedReads = []library.ZurgBlockedRead{{Filename: "S01E02.mkv", Reason: "broken"}}
	e.tr.sleep = func(ctx context.Context, d time.Duration) error {
		e.advance(d)
		e.plex.mu.Lock()
		e.plex.ss[0].ViewOffset += d.Milliseconds()
		e.plex.mu.Unlock()
		return ctx.Err()
	}
	e.poll() // the message is owed
	sample := func(state string, move int64) {
		e.plex.mu.Lock()
		e.plex.ss[0].State = state
		e.plex.ss[0].ViewOffset += move
		e.plex.mu.Unlock()
		e.advance(10 * time.Second)
		e.tr.poll(e.ctx)
	}
	e.tr.mu.Lock()
	e.tr.rescuing = true // the worker is busy with something else
	e.tr.mu.Unlock()
	sample("buffering", 0) // due now, but not delivered
	sample("paused", 0)
	sample("playing", 10_000)
	sample("playing", 10_000)
	e.tr.mu.Lock()
	e.tr.rescuing = false
	e.tr.mu.Unlock()
	sample("playing", 10_000)
	e.waitIdle()
	if got := e.stop.stopped(); len(got) != 0 {
		t.Errorf("an advancing stream was stopped: %q", got)
	}
}

// An incident that could not be replaced because its debrid account did not answer says so in the list, naming no
// account (the same words as History's).
func TestIncidentsOutage(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.lib.setBroken("S01E02.mkv", -1)
	e.play(2, "buffering")
	e.poll()
	e.exec(`UPDATE playing_break SET settled = 'none', reported = 'unavailable', out_account = 'realdebrid'
		WHERE link_id = 11`)
	if in := e.tr.Incidents(); len(in) != 1 || in[0].State != "unavailable" ||
		in[0].Status != "No other copy yet: the debrid account did not answer" {
		t.Errorf("incidents %+v", in)
	}
}

// A watched copy removed from the library before its search ended has its own plain state.
func TestIncidentStateRemoved(t *testing.T) {
	if state, words := incidentState("aside", "", "removed"); state != "removed" || words != "Removed from the library" {
		t.Errorf("%s %s", state, words)
	}
}

// An outage touches only the watched file: the release's other files are not read, marked or searched (the hourly
// sweep sees them later).
func TestRescueOutageLeavesSiblings(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.lib.setBroken("S01E02.mkv", -1)
	e.lib.setBroken("S01E03.mkv", -1)
	e.lib.setBroken("film.mkv", -1)
	e.play(2, "buffering")
	e.poll()
	if got := e.one(`SELECT out_account FROM playing_break WHERE link_id = 11`); got != "rd" {
		t.Fatalf("out account %q", got)
	}
	if got := e.one(`SELECT group_concat(state) FROM (SELECT state FROM link WHERE id IN (10, 11, 12) ORDER BY id)`); got !=
		"active,dead,active" {
		t.Errorf("links %s", got)
	}
	if n := e.lib.readsOf("S01E01.mkv") + e.lib.readsOf("S01E03.mkv"); n != 0 {
		t.Errorf("%d reads of the release's other files", n)
	}
	if got := e.one(`SELECT COUNT(*) FROM playing_break`); got != "1" {
		t.Errorf("%s incident rows", got)
	}
}

// A zurg cooldown line of the serving account counts as an outage only when logged after the check's first failed
// read; an older one means nothing, and the control read decides.
func TestRescueCooldownOnlySinceFirstFailure(t *testing.T) {
	// Logged before the sign: the account's other release reads, so only this release is broken.
	e := newRescueEnv(t, false, true)
	e.play(2, "playing")
	e.poll()
	e.tr.ZurgLine("WARN Account rd is cooling down for 5m")
	e.advance(5 * time.Minute)
	e.tr.ZurgLine("INFO Large transfer: 512 MiB of __all__/Rel/S01E02.mkv from rd for 30s")
	e.lib.setBroken("S01E02.mkv", -1)
	e.play(2, "buffering")
	e.poll()
	if got := e.one(`SELECT out_account FROM playing_break WHERE link_id = 11`); got != "" {
		t.Errorf("an old cooldown line made %q out", got)
	}
	if e.lib.readsOf("film.mkv") != 1 {
		t.Errorf("control reads %d, want 1", e.lib.readsOf("film.mkv"))
	}
	if got := e.one(`SELECT group_concat(provider) FROM provider_cache WHERE source = 'router:playing'`); got != "rd" {
		t.Errorf("misses on %q", got)
	}
	// Logged during the check: the account is out, with no control read.
	e2 := newRescueEnv(t, false, true)
	e2.play(2, "playing")
	e2.poll()
	e2.tr.ZurgLine("INFO Large transfer: 512 MiB of __all__/Rel/S01E02.mkv from rd for 30s")
	e2.lib.setBroken("S01E02.mkv", -1)
	e2.tr.sleep = func(ctx context.Context, d time.Duration) error {
		e2.advance(d)
		e2.tr.ZurgLine("WARN Account rd is cooling down for 5m")
		return ctx.Err()
	}
	e2.play(2, "buffering")
	e2.poll()
	if got := e2.one(`SELECT out_account FROM playing_break WHERE link_id = 11`); got != "rd" {
		t.Errorf("a cooldown during the check: out account %q", got)
	}
	if e2.lib.readsOf("film.mkv") != 0 {
		t.Errorf("control reads %d during a cooldown", e2.lib.readsOf("film.mkv"))
	}
}

// A verdict from a fallback control (no release held by the serving account alone, so a release on any account was
// read) repairs the watched file only: the release's other files are not read.
func TestRescueFallbackControlLeavesSiblings(t *testing.T) {
	e := newRescueEnv(t, false, true)
	e.exec(`INSERT INTO provider_torrent (provider, torrent_id, info_hash, progress, state) VALUES
		('ad', 'a60', ?, 100, 'downloaded')`, strings.Repeat("b", 40))
	e.play(2, "playing")
	e.poll()
	e.tr.ZurgLine("INFO Large transfer: 512 MiB of __all__/Rel/S01E02.mkv from rd for 30s")
	e.lib.setBroken("S01E02.mkv", -1)
	e.lib.setBroken("S01E03.mkv", -1)
	e.play(2, "buffering")
	e.poll()
	if got := e.one(`SELECT group_concat(state) FROM (SELECT state FROM link WHERE id IN (10, 11, 12) ORDER BY id)`); got !=
		"active,dead,active" {
		t.Errorf("links %s", got)
	}
	if n := e.lib.readsOf("S01E01.mkv") + e.lib.readsOf("S01E03.mkv"); n != 0 {
		t.Errorf("%d reads of the release's other files", n)
	}
}

// An owed stop is checked before it is delivered: one read of the file the link points at now, at the player's
// position. The old copy reads again, or a same-path import put a readable file there: nothing is stopped and the
// incident says the file read again. Still failing: the player is stopped (TestRescueMessageAtNextSign).
func TestRescueOwedStopReadsFirst(t *testing.T) {
	for _, c := range []string{"recovered", "imported"} {
		e := newRescueEnv(t, false, true)
		e.lib.setBroken("S01E02.mkv", -1)
		e.play(2, "playing")
		e.zurg.stats.BlockedReads = []library.ZurgBlockedRead{{Filename: "S01E02.mkv", Reason: "broken"}}
		e.tr.sleep = func(ctx context.Context, d time.Duration) error {
			e.advance(d)
			e.plex.mu.Lock()
			e.plex.ss[0].ViewOffset += d.Milliseconds()
			e.plex.mu.Unlock()
			return ctx.Err()
		}
		e.poll() // the message is owed
		e.tr.mu.Lock()
		owed := len(e.tr.pending)
		e.tr.mu.Unlock()
		if owed != 1 {
			t.Fatalf("%s: %d messages owed", c, owed)
		}
		switch c {
		case "recovered":
			e.lib.setBroken("S01E02.mkv", 0)
		case "imported":
			repl := filepath.Join(e.mount, "__all__", "OtherAD", "S01E02.new.mkv")
			if err := os.WriteFile(repl, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(repl, fileSize/2); err != nil {
				t.Fatal(err)
			}
			e.exec(`UPDATE link SET target = ?, state = 'active' WHERE id = 11`, repl)
		}
		e.plex.mu.Lock()
		e.plex.ss[0].State = "buffering"
		e.plex.mu.Unlock()
		e.poll()
		if got := e.stop.stopped(); len(got) != 0 {
			t.Errorf("%s: stopped %q", c, got)
		}
		if got := e.one(`SELECT message FROM playing_break WHERE link_id = 11`); got != NotStoppedNote+notStoppedRead {
			t.Errorf("%s: note %q", c, got)
		}
	}
}

// Plex's size may be an older file's at the same path: after the first failed read the range is taken from the mounted
// file's size, so a readable shorter replacement stopped early is not judged broken.
func TestRescueStaleSizeRecomputedFromMount(t *testing.T) {
	e := newRescueEnv(t, false, false)
	if err := os.Truncate(filepath.Join(e.mount, "__all__", "Rel", "S01E02.mkv"), fileSize/2); err != nil {
		t.Fatal(err)
	}
	e.play(2, "buffering")
	e.plex.mu.Lock()
	e.plex.ss[0].ViewOffset = 2_100_000 // 70 %: Plex's 4 MiB size puts the read past the 2 MiB file's end
	e.plex.mu.Unlock()
	e.poll()
	if got := e.one(`SELECT COUNT(*) FROM playing_break`) + " " + e.one(`SELECT state FROM link WHERE id = 11`); got != "0 active" {
		t.Errorf("a readable shorter file: %s", got)
	}
	if n := e.lib.readsOf("S01E02.mkv"); n != 2 {
		t.Errorf("%d reads, want 2 (the stale range, then the mounted one after the gap)", n)
	}
}

// A session listing several media or parts without saying which plays gets no automatic check; it still shows in Now
// playing.
func TestRescueAmbiguousSessionNotChecked(t *testing.T) {
	e := newRescueEnv(t, false, false)
	e.lib.setBroken("S01E02.mkv", -1)
	e.play(2, "buffering")
	e.plex.mu.Lock()
	e.plex.ss[0].Ambiguous = true
	e.plex.mu.Unlock()
	e.poll()
	if n := e.lib.readsOf("S01E02.mkv"); n != 0 || e.one(`SELECT state FROM link WHERE id = 11`) != "active" {
		t.Errorf("an ambiguous session was checked: %d reads", n)
	}
	if got := e.tr.Snapshot(); len(got.Sessions) != 1 {
		t.Errorf("now playing lists %d sessions", len(got.Sessions))
	}
}

// The serving account is matched by the file's whole path under the release root: a transfer of another release's
// file of the same name, from another account, does not make that account the serving one (E9).
func TestRescueServedByWholePath(t *testing.T) {
	e := newRescueEnv(t, false, true)
	e.play(2, "playing")
	e.poll()
	e.tr.ZurgLine("INFO Large transfer: 512 MiB of __all__/Rel/S01E02.mkv from rd for 30s")
	e.tr.ZurgLine("INFO Large transfer: 512 MiB of __all__/Another.Show/S01E02.mkv from ad for 30s")
	e.lib.setBroken("S01E02.mkv", -1)
	e.play(2, "buffering")
	e.poll()
	if e.lib.readsOf("film.mkv") != 1 || e.lib.readsOf("film2.mkv") != 0 {
		t.Errorf("control reads: rd's %d, ad's %d", e.lib.readsOf("film.mkv"), e.lib.readsOf("film2.mkv"))
	}
	if got := e.one(`SELECT group_concat(provider) FROM provider_cache WHERE source = 'router:playing'`); got != "rd" {
		t.Errorf("misses on %q", got)
	}
}
