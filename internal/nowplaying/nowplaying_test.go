package nowplaying

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/errinbox"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/plex"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func TestParseZurgLine(t *testing.T) {
	at := time.Unix(100, 0)
	files := []string{"Show.S01E02.mkv"}
	cases := []struct {
		line    string
		ok      bool
		kind    string
		account string
	}{
		{"INFO Large transfer: 512 MiB of __all__/Rel/Show.S01E02.mkv from rd-main for 30s", true, kindTransfer, "rd-main"},
		{"WARN Cut a read of Show.S01E02.mkv on ad-2 for 2s (starved)", true, kindStarved, "ad-2"},
		{"WARN Source state changed: Rel/Show.S01E02.mkv on rd-main [ok_file → broken_file]", true, kindBroken, "rd-main"},
		{"ERROR Rel/Show.S01E02.mkv is broken; read retry window elapsed", true, kindBroken, ""},
		{"WARN Download link broken (attempt 1/3), re-unrestricting file Rel/Show.S01E02.mkv", true, kindLink, ""},
		{"WARN Provider rate limited while unrestricting Show.S01E02.mkv; retry in 5s", true, kindLimited, ""},
		{"WARN Account rd-main is cooling down for 60s", true, kindCooldown, "rd-main"},
		{"WARN Cut a read of Other.mkv on ad-2 for 2s", false, "", ""},
		{"INFO something else about Show.S01E02.mkv", false, "", ""},
	}
	for _, c := range cases {
		ev, ok := parseZurgLine(c.line, files, at)
		if ok != c.ok || ev.Kind != c.kind || ev.Account != c.account {
			t.Errorf("%q: %v %+v", c.line, ok, ev)
		}
		if ok && c.kind != kindCooldown && ev.File != "Show.S01E02.mkv" {
			t.Errorf("%q: file %q", c.line, ev.File)
		}
	}
	evs := []zurgEvent{{Kind: kindTransfer, File: "a.mkv", Account: "rd"}, {Kind: kindStarved, File: "a.mkv", Account: "rd"},
		{Kind: kindCooldown, Account: "other"}}
	if got := issue(evs, "a.mkv", "rd"); !strings.Contains(got, "starved read on rd") {
		t.Errorf("issue %q", got)
	}
	evs = append(evs, zurgEvent{Kind: kindCooldown, Account: "rd"})
	if got := issue(evs, "a.mkv", "rd"); got != "rd is cooling down" {
		t.Errorf("issue %q", got)
	}
	if got := issue(evs, "b.mkv", ""); got != "" {
		t.Errorf("issue for another file %q", got)
	}
}

func TestPickNext(t *testing.T) {
	n := func(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
	eps := []episodeRow{
		{ID: 1, Season: 1, Number: 1, Absolute: n(1)},
		{ID: 2, Season: 1, Number: 2, Absolute: n(2)},
		{ID: 3, Season: 2, Number: 1, Absolute: n(3)},
		{ID: 4, Season: 0, Number: 1},
		{ID: 5, Season: 0, Number: 2},
		{ID: 6, Season: 1, Number: 3, Absolute: n(4)}, // a late-added season 1 episode, aired after S02E01
	}
	cases := []struct {
		cur   []int64
		anime bool
		want  int64
	}{
		{[]int64{1}, false, 2},
		{[]int64{2}, false, 6},
		{[]int64{2}, true, 3}, // absolute order
		{[]int64{1, 2}, false, 6},
		{[]int64{3}, false, 0},
		{[]int64{4}, false, 5}, // specials among themselves
		{[]int64{5}, false, 0},
		{[]int64{4}, true, 5}, // no absolute number: season order
	}
	for _, c := range cases {
		got := pickNext(eps, c.cur, c.anime)
		var id int64
		if got != nil {
			id = got.ID
		}
		if id != c.want {
			t.Errorf("%v anime=%v: %d, want %d", c.cur, c.anime, id, c.want)
		}
	}
}

// fixture: one series, one version, three regular episodes; S01E02 and S01E03 linked, S01E04 not aired yet.
func openStore(t *testing.T) *store.Store {
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
	day := int64(24 * 3600 * 1000)
	now := store.Millis(time.Now())
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'series', 'Show', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'tv-hd', 0, 0)`,
		`INSERT INTO episode (id, title_id, season, number, name, air_date_utc_at, created_at, updated_at) VALUES
			(1, 1, 1, 1, 'One', 1, 0, 0), (2, 1, 1, 2, 'Two', 2, 0, 0), (3, 1, 1, 3, 'Three', 3, 0, 0),
			(4, 1, 1, 4, 'Four', ` + strconv.FormatInt(now+day, 10) + `, 0, 0)`,
		`INSERT INTO content (id, info_hash, name, mount_folder, first_seen_at, last_seen_at) VALUES
			(50, 'aaaa', 'Rel', 'Rel', 0, 0), (51, 'bbbb', 'Rel3', 'Rel3', 0, 0)`,
		`INSERT INTO content_provider (content_id, provider, seen_at) VALUES (50, 'realdebrid', 0), (51, 'realdebrid', 0),
			(51, 'alldebrid', 0)`,
		`INSERT INTO content_file (id, content_id, path, size_bytes) VALUES (100, 50, 'S01E02.mkv', 1), (101, 51, 'S01E03.mkv', 1)`,
		`INSERT INTO link (id, path, target, content_file_id, title_id, release_title, quality, state, created_at, updated_at) VALUES
			(10, '/media/tv/Show/S01E02.mkv', '/mnt/zurg/__all__/Rel/S01E02.mkv', 100, 1, 'Show.S01.1080p', 'WEBDL-1080p', 'active', 0, 0),
			(11, '/media/tv/Show/S01E03.mkv', '/mnt/zurg/__all__/Rel3/S01E03.mkv', 101, 1, 'Show.S01E03', 'WEBDL-1080p', 'active', 0, 0)`,
		`INSERT INTO link_version (link_id, version_id) VALUES (10, 1), (11, 1)`,
		`INSERT INTO link_episode (link_id, episode_id) VALUES (10, 2), (11, 3)`,
	} {
		if _, err := st.Writer().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return st
}

type fakePlex struct {
	mu sync.Mutex
	ss []plex.Session
}

func (f *fakePlex) Sessions(context.Context) ([]plex.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]plex.Session(nil), f.ss...), nil
}

func (f *fakePlex) Notifications(ctx context.Context, _ func(plex.PlayEvent)) error {
	<-ctx.Done()
	return nil
}

type fakeZurg struct {
	missing map[string]bool
	stats   *library.ZurgStats
}

func (z *fakeZurg) Entry(_ context.Context, folder, rel string) (*library.ZurgEntry, error) {
	if z.missing[folder+"/"+rel] {
		return nil, nil
	}
	return &library.ZurgEntry{Hash: "ZZZZ"}, nil
}

func (z *fakeZurg) Stats(context.Context) (*library.ZurgStats, error) { return z.stats, nil }

type fakeMount struct {
	mu       sync.Mutex
	healthy  error
	read     []string
	readErr  error
	size     int64 // 0: 1 GiB
	cached   []string
	rate     int64
	cacheErr error
	stopped  bool          // a CacheFile call ended by its context
	block    chan struct{} // the caching read waits for it (or its context)
}

func (m *fakeMount) MountHealthy(context.Context) error { return m.healthy }

func (m *fakeMount) Prewarm(_ context.Context, target string, max int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if max != prewarmBytes {
		return errors.New("wrong size")
	}
	m.read = append(m.read, target)
	return m.readErr
}

func (m *fakeMount) MountFileSize(context.Context, string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.size == 0 {
		return 1 << 30, nil
	}
	return m.size, nil
}

func (m *fakeMount) CacheFile(ctx context.Context, target string, rate int64, progress func(done, size int64)) (int64, error) {
	m.mu.Lock()
	m.cached = append(m.cached, target)
	m.rate = rate
	block, err := m.block, m.cacheErr
	m.mu.Unlock()
	progress(1<<29, 1<<30)
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			m.mu.Lock()
			m.stopped = true
			m.mu.Unlock()
			return 1 << 29, ctx.Err()
		}
	}
	if err != nil {
		return 1 << 29, err
	}
	progress(1<<30, 1<<30)
	return 1 << 30, nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTracker(t *testing.T, p *fakePlex, z *fakeZurg, m *fakeMount, c *clock, reports *[]errinbox.Event) *Tracker {
	var mu sync.Mutex
	var za ZurgAPI
	if z != nil {
		za = z
	}
	return New(Options{Plex: p, Store: openStore(t), Zurg: za, Mount: func() Mount { return m },
		LinkPath: "/mnt/zurg", MountPath: "/zurg", Prewarm: true, Now: c.now,
		Readlink: func(p string) (string, error) {
			if strings.HasPrefix(p, "/plex/tv/Show/") {
				return "../../../mnt/zurg/__all__/Rel/" + filepath.Base(p), nil
			}
			return "", errors.New("not a link")
		},
		Report: func(e errinbox.Event) { mu.Lock(); *reports = append(*reports, e); mu.Unlock() }})
}

func waitPrewarm(t *testing.T, tr *Tracker) {
	t.Helper()
	for range 200 {
		tr.mu.Lock()
		busy := tr.warming
		tr.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("pre-warm did not finish")
}

func TestTrackerSourceAndPrewarm(t *testing.T) {
	ctx := context.Background()
	c := &clock{time.Now()}
	p := &fakePlex{ss: []plex.Session{{SessionKey: "1", RatingKey: "r2", Type: "episode", GrandparentTitle: "Show",
		ParentIndex: 1, Index: 2, State: "playing", ViewOffset: 30000, File: "/plex/tv/Show/S01E02.mkv", UserID: "7",
		UserName: "alice", Bitrate: 8000, Local: true}}}
	z, m := &fakeZurg{missing: map[string]bool{}}, &fakeMount{}
	var reports []errinbox.Event
	tr := newTracker(t, p, z, m, c, &reports)
	tr.poll(ctx)
	snap := tr.Snapshot()
	if len(snap.Sessions) != 1 {
		t.Fatalf("%+v", snap)
	}
	src := snap.Sessions[0].Source
	if src == nil || src.Matched != "link" || src.LinkID != 10 || src.Target != "/mnt/zurg/__all__/Rel/S01E02.mkv" ||
		src.Title != "Show" || len(src.Versions) != 1 || src.Versions[0] != "tv-hd" || src.Hash != "aaaa" ||
		len(src.Providers) != 1 || !src.SingleCopy {
		t.Fatalf("source %+v", src)
	}
	if snap.Sessions[0].Prewarm != nil {
		t.Error("pre-warm before 60s")
	}
	// Past a minute: S01E03 is read once through the mount.
	c.t = c.t.Add(40 * time.Second)
	p.ss[0].ViewOffset = 70000
	tr.poll(ctx)
	waitPrewarm(t, tr)
	tr.poll(ctx)
	waitPrewarm(t, tr)
	if len(m.read) != 1 || m.read[0] != "/mnt/zurg/__all__/Rel3/S01E03.mkv" {
		t.Errorf("reads %v", m.read)
	}
	if pw := tr.Snapshot().Sessions[0].Prewarm; pw == nil || pw.State != "ok" || pw.Episode != "S01E03" {
		t.Errorf("prewarm %+v", pw)
	}
	if len(reports) != 0 {
		t.Errorf("reports %+v", reports)
	}

	// Playing S01E03 (its source found by path: Plex's file is the link itself): S01E04 has not aired.
	p.ss[0].RatingKey, p.ss[0].File, p.ss[0].ViewOffset = "r3", "/media/tv/Show/S01E03.mkv", 90000
	tr.poll(ctx)
	waitPrewarm(t, tr)
	se := tr.Snapshot().Sessions[0]
	if se.Source == nil || se.Source.LinkID != 11 || se.Source.SingleCopy || len(se.Source.Providers) != 2 {
		t.Errorf("source %+v", se.Source)
	}
	if se.Prewarm == nil || se.Prewarm.State != "skipped" || len(m.read) != 1 {
		t.Errorf("prewarm %+v reads %v", se.Prewarm, m.read)
	}
}

func TestPrewarmMissing(t *testing.T) {
	ctx := context.Background()
	c := &clock{time.Now()}
	p := &fakePlex{ss: []plex.Session{{SessionKey: "1", RatingKey: "r2", Type: "episode", State: "playing",
		ViewOffset: 70000, File: "/media/tv/Show/S01E02.mkv"}}}
	z, m := &fakeZurg{missing: map[string]bool{"Rel3/S01E03.mkv": true}}, &fakeMount{}
	var reports []errinbox.Event
	tr := newTracker(t, p, z, m, c, &reports)
	tr.poll(ctx)
	waitPrewarm(t, tr)
	tr.poll(ctx)
	waitPrewarm(t, tr)
	if len(reports) != 1 || reports[0].Key != "1:3" || reports[0].Component != "nowplaying.prewarm" ||
		!strings.Contains(reports[0].Message, "zurg has no entry") || len(m.read) != 0 {
		t.Fatalf("reports %+v reads %v", reports, m.read)
	}
	// Unreadable through the mount.
	z.missing = nil
	m.readErr = errors.New("input/output error")
	p.ss[0].SessionKey = "2"
	tr.poll(ctx)
	waitPrewarm(t, tr)
	if len(reports) != 2 || !strings.Contains(reports[1].Message, "unreadable: input/output error") || len(m.read) != 1 {
		t.Fatalf("reports %+v reads %v", reports, m.read)
	}
}

func TestVerdicts(t *testing.T) {
	ctx := context.Background()
	c := &clock{time.Now()}
	sess := plex.Session{SessionKey: "1", RatingKey: "r", Type: "movie", State: "playing", ViewOffset: 1000,
		File: "/elsewhere/Movie.mkv", Location: "wan", Relayed: true, Bitrate: 8000}
	p := &fakePlex{ss: []plex.Session{sess}}
	m := &fakeMount{}
	z := &fakeZurg{}
	var reports []errinbox.Event
	tr := newTracker(t, p, z, m, c, &reports)
	step := func(off int64, state string) Health {
		c.t = c.t.Add(10 * time.Second)
		p.ss[0].ViewOffset, p.ss[0].State = off, state
		tr.poll(ctx)
		return tr.Snapshot().Sessions[0].Health
	}
	tr.poll(ctx)
	if h := step(11000, "playing"); h.Verdict != "ok" {
		t.Errorf("%+v", h)
	}
	// 3 s of video in 10 s: slow; nothing else to blame, a relayed remote client.
	if h := step(14000, "playing"); h.Verdict != "slow" || h.Cause != "network" || !strings.Contains(h.Detail, "relayed") {
		t.Errorf("%+v", h)
	}
	// zurg logs a starved read of the file: the provider.
	tr.ZurgLine("WARN Cut a read of Movie.mkv on rd-main for 2s")
	if h := step(16000, "playing"); h.Cause != "provider" || !strings.Contains(h.Detail, "rd-main") {
		t.Errorf("%+v", h)
	}
	// The transcoder is slower than playback: it comes first.
	p.ss[0].Transcode = &plex.Transcode{Speed: 0.6}
	if h := step(18000, "playing"); h.Cause != "transcode" {
		t.Errorf("%+v", h)
	}
	// A pause is a pause, and a seek is no measure.
	p.ss[0].Transcode = nil
	if h := step(18000, "paused"); h.Verdict != "paused" {
		t.Errorf("%+v", h)
	}
	step(18000, "playing")
	if h := step(28000, "playing"); h.Verdict != "ok" {
		t.Errorf("%+v", h)
	}
	if h := step(900000, "playing"); h.Verdict != "ok" {
		t.Errorf("seek: %+v", h)
	}
	// Buffering through the websocket; zurg blocks reads of the file.
	tr.event(plex.PlayEvent{SessionKey: "1", RatingKey: "r", State: "buffering", ViewOffset: 900500})
	tr.mu.Lock()
	tr.events = nil
	tr.mu.Unlock()
	z.stats = &library.ZurgStats{ActiveStreams: 2, BlockedReads: []library.ZurgBlockedRead{{Filename: "Movie.mkv", Reason: "starved"}}}
	h := step(901000, "buffering")
	snap := tr.Snapshot()
	if h.Verdict != "buffering" || h.Cause != "provider" || !strings.Contains(h.Detail, "starved") ||
		snap.Sessions[0].Buffering != 1 || snap.ZurgStreams == nil || *snap.ZurgStreams != 2 {
		t.Errorf("%+v %+v", h, snap)
	}
	// The mount is down.
	z.stats = nil
	m.healthy = errors.New("mount down")
	if h := step(902000, "buffering"); h.Cause != "mount" || h.Detail != "mount down" {
		t.Errorf("%+v", h)
	}
	// The session ends.
	p.ss = nil
	tr.poll(ctx)
	if n := len(tr.Snapshot().Sessions); n != 0 {
		t.Errorf("%d sessions", n)
	}
}

type httpDoer struct{}

func (httpDoer) Do(_ context.Context, tg gateway.Target, req *http.Request) (*http.Response, error) {
	if tg != RCTarget {
		return nil, errors.New("wrong target")
	}
	return http.DefaultClient.Do(req)
}

func TestRC(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, pw, ok := r.BasicAuth()
		if r.Method != http.MethodPost || !ok || u != "rcuser" || pw != "rc-secret-1" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/core/stats":
			_, _ = io.WriteString(w, `{"transferring":[{"name":"__all__/Rel/Movie.mkv","size":100,"bytes":40,"speed":500000,"speedAvg":400000}]}`)
		case "/vfs/stats":
			_, _ = io.WriteString(w, `{"diskCache":{"bytesUsed":10,"files":2,"erroredFiles":1,"outOfSpace":false}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	rc := &RC{URL: srv.URL + "/", User: "rcuser", Password: "rc-secret-1", GW: httpDoer{}}
	trs, err := rc.Transfers(context.Background())
	if err != nil || len(trs) != 1 || trs[0].Speed != 500000 {
		t.Fatalf("%v %+v", err, trs)
	}
	f := matchFetch(trs, &session{s: plex.Session{File: "/x/Movie.mkv", Bitrate: 8000}})
	if f == nil || f.Ratio != 0.5 || f.Bytes != 40 {
		t.Errorf("fetch %+v", f)
	}
	cache, err := rc.Cache(context.Background())
	if err != nil || cache.ErroredFiles != 1 || cache.Files != 2 {
		t.Errorf("%v %+v", err, cache)
	}
	bad := &RC{URL: srv.URL, User: "rcuser", Password: "wrong", GW: httpDoer{}}
	if _, err := bad.Transfers(context.Background()); err == nil || strings.Contains(err.Error(), "wrong") {
		t.Errorf("err %v", err)
	}
}

func TestSnapshotSpeedsUpPolling(t *testing.T) {
	c := &clock{time.Now()}
	var reports []errinbox.Event
	tr := newTracker(t, &fakePlex{}, nil, &fakeMount{}, c, &reports)
	if d := tr.interval(); d != idleEvery {
		t.Errorf("idle %v", d)
	}
	tr.Snapshot()
	if d := tr.interval(); d != activeEvery {
		t.Errorf("viewed %v", d)
	}
	select {
	case <-tr.wake:
	default:
		t.Error("a first look does not refresh")
	}
	c.t = c.t.Add(2 * time.Minute)
	if d := tr.interval(); d != idleEvery {
		t.Errorf("later %v", d)
	}
}

// Anime goes by absolute number through the regular episodes: a special numbered between two of them is skipped
// (One-Punch Man: S01E02 abs 2, a special abs 3, S01E03 abs 4).
func TestPickNextAnimeSkipsSpecials(t *testing.T) {
	n := func(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
	eps := []episodeRow{
		{ID: 1, Season: 1, Number: 2, Absolute: n(2)},
		{ID: 2, Season: 0, Number: 1, Absolute: n(3)},
		{ID: 3, Season: 1, Number: 3, Absolute: n(4)},
	}
	if got := pickNext(eps, []int64{1}, true); got == nil || got.ID != 3 {
		t.Errorf("next %+v, want S01E03", got)
	}
}

// Only providers the registry has the content present on count: an absent one is neither shown nor a second copy.
func TestAbsentProviderNotShown(t *testing.T) {
	st := openStore(t)
	if _, err := st.Writer().Exec(`UPDATE content_provider SET state = 'absent' WHERE content_id = 51 AND provider = 'alldebrid'`); err != nil {
		t.Fatal(err)
	}
	tr := New(Options{Store: st, MountPath: "/mnt/zurg"})
	src := tr.resolve(context.Background(), "/mnt/zurg/__all__/Rel3/S01E03.mkv")
	if len(src.Providers) != 1 || src.Providers[0] != "realdebrid" || !src.SingleCopy {
		t.Errorf("providers %v single %v", src.Providers, src.SingleCopy)
	}
}

// A target several versions link: the version whose root (or followed root) holds Plex's path is the one playing;
// undecided, every version sharing the target is shown (Happy Feet under the web root, linked by remux and web).
func TestSharedTargetVersion(t *testing.T) {
	st := openStore(t)
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (2, 'movie', 'Happy Feet', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES
			(2, 2, 'movie-remux-theatarr', 0, 0), (3, 2, 'movie-web-theatarr', 0, 0), (4, 2, 'movie-web', 0, 0)`,
		`INSERT INTO version_follow (version_id, follows_id) VALUES (3, 4)`,
		`INSERT INTO link (id, path, target, title_id, release_title, state, created_at, updated_at) VALUES
			(20, '/media/remux-theatarr/Happy Feet/hf.mkv', '/mnt/zurg/__all__/HF/hf.mkv', 2, 'HF', 'active', 0, 0),
			(21, '/media/web-theatarr/Happy Feet/hf.mkv', '/mnt/zurg/__all__/HF/hf.mkv', 2, 'HF', 'active', 0, 0)`,
		`INSERT INTO link_version (link_id, version_id) VALUES (20, 2), (21, 3)`,
	} {
		if _, err := st.Writer().Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	roots := map[string][]string{"movie-remux-theatarr": {"/media/remux-theatarr"}, "movie-web-theatarr": {"/media/web-theatarr"},
		"movie-web": {"/media/movies-web"}}
	link := func(string) (string, error) { return "/mnt/zurg/__all__/HF/hf.mkv", nil }
	tr := New(Options{Store: st, LinkPath: "/mnt/zurg", AllDir: "__all__", Readlink: link,
		Roots: func(k string) []string { return roots[k] }})
	// The arr's web root, which movie-web-theatarr follows.
	src := tr.resolve(context.Background(), "/media/movies-web/Happy Feet/hf.mkv")
	if src.LinkID != 21 || !slices.Equal(src.Versions, []string{"movie-web-theatarr"}) {
		t.Errorf("web root: link %d versions %v", src.LinkID, src.Versions)
	}
	// Undecided: both versions.
	src = tr.resolve(context.Background(), "/elsewhere/Happy Feet/hf.mkv")
	if !slices.Equal(src.Versions, []string{"movie-remux-theatarr", "movie-web-theatarr"}) {
		t.Errorf("undecided: versions %v", src.Versions)
	}
}

// The pre-warm finds the registry's content as source mapping does: a live link has no content file, so by the
// release folder or zurg's hash. Content gone or absent is a missing next episode: an inbox item, no read.
func TestPrewarmRegistryLookup(t *testing.T) {
	for _, c := range []struct {
		name string
		sql  []string
	}{
		{"gone by hash", []string{`UPDATE link SET content_file_id = NULL WHERE id = 11`,
			`UPDATE content SET state = 'gone', mount_folder = NULL WHERE id = 51`}},
		{"absent by folder", []string{`UPDATE link SET content_file_id = NULL WHERE id = 11`,
			`UPDATE content SET state = 'absent' WHERE id = 51`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &fakePlex{ss: []plex.Session{{SessionKey: "1", RatingKey: "r2", Type: "episode", State: "playing",
				ViewOffset: 70000, File: "/media/tv/Show/S01E02.mkv"}}}
			var reports []errinbox.Event
			m := &fakeMount{}
			tr := newTracker(t, p, nil, m, &clock{time.Now()}, &reports)
			tr.o.Zurg = hashZurg{"BBBB"}
			for _, q := range c.sql {
				if _, err := tr.o.Store.Writer().Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			tr.poll(context.Background())
			waitPrewarm(t, tr)
			if len(reports) != 1 || !strings.Contains(reports[0].Message, "gone") || len(m.read) != 0 {
				t.Errorf("reports %+v reads %v", reports, m.read)
			}
		})
	}
}

type hashZurg struct{ hash string }

func (z hashZurg) Entry(context.Context, string, string) (*library.ZurgEntry, error) {
	return &library.ZurgEntry{Hash: z.hash}, nil
}
func (hashZurg) Stats(context.Context) (*library.ZurgStats, error) { return nil, nil }

// A missing next episode raises an inbox item: aired and monitored but not linked, or linked off the mount.
func TestPrewarmMissingNextReported(t *testing.T) {
	for _, c := range []struct {
		name    string
		sql     string
		readErr error
		want    string
	}{
		{"not linked", `UPDATE link SET state = 'unlinked' WHERE id = 11`, nil, "not in the library"},
		{"no episode link", `DELETE FROM link_episode WHERE link_id = 11`, nil, "not in the library"},
		{"not on the mount", ``, library.ErrNotOnMount, "not on the mount"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &fakePlex{ss: []plex.Session{{SessionKey: "1", RatingKey: "r2", Type: "episode", State: "playing",
				ViewOffset: 70000, File: "/media/tv/Show/S01E02.mkv"}}}
			var reports []errinbox.Event
			m := &fakeMount{readErr: c.readErr}
			tr := newTracker(t, p, &fakeZurg{missing: map[string]bool{}}, m, &clock{time.Now()}, &reports)
			if c.sql != "" {
				if _, err := tr.o.Store.Writer().Exec(c.sql); err != nil {
					t.Fatal(err)
				}
			}
			tr.poll(context.Background())
			waitPrewarm(t, tr)
			pw := tr.Snapshot().Sessions[0].Prewarm
			if len(reports) != 1 || reports[0].Key != "1:3" || !strings.Contains(reports[0].Message, c.want) ||
				pw == nil || pw.State != "failed" {
				t.Errorf("reports %+v prewarm %+v", reports, pw)
			}
		})
	}
}

// A websocket state change sets the verdict at once and wakes the poller: never buffering with verdict ok.
func TestEventUpdatesHealth(t *testing.T) {
	now := time.Now()
	tr := New(Options{Now: func() time.Time { return now }})
	tr.sess["1"] = &session{s: plex.Session{SessionKey: "1", State: "playing"}, health: Health{Verdict: "ok"}}
	tr.event(plex.PlayEvent{SessionKey: "1", State: "buffering"})
	select {
	case <-tr.wake:
	default:
		t.Error("the poller was not woken")
	}
	if got := tr.Snapshot().Sessions[0]; got.State != "buffering" || got.Health.Verdict != "buffering" {
		t.Errorf("state %s health %+v", got.State, got.Health)
	}
}

// A definite mount problem comes before the provider cause a slow fetch suggests.
func TestCauseMountBeforeSlowFetch(t *testing.T) {
	se := &session{s: plex.Session{State: "buffering", Bitrate: 8000, Local: true}, fetch: &Fetch{Ratio: 0.2}}
	tr := New(Options{})
	tr.cache = &Cache{ErroredFiles: 1}
	if cause, detail := tr.cause(se, nil); cause != "mount" {
		t.Errorf("cache errors: %s %s", cause, detail)
	}
	tr.cache = nil
	if cause, detail := tr.cause(se, library.ErrMountDown); cause != "mount" {
		t.Errorf("mount down: %s %s", cause, detail)
	}
	if cause, _ := tr.cause(se, nil); cause != "provider" {
		t.Errorf("slow fetch alone: %s", cause)
	}
}

func waitCache(t *testing.T, tr *Tracker) {
	t.Helper()
	for range 300 {
		tr.mu.Lock()
		busy := tr.caching != nil
		tr.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("caching read did not finish")
}

// cacheTracker: S01E02 playing a minute in (its head check of S01E03 passes), then past half of it.
func cacheTracker(t *testing.T, m *fakeMount, reports *[]errinbox.Event) (*Tracker, *fakePlex) {
	t.Helper()
	p := &fakePlex{ss: []plex.Session{{SessionKey: "1", RatingKey: "r2", Type: "episode", GrandparentTitle: "Show",
		State: "playing", ViewOffset: 70000, Duration: 3_000_000, File: "/media/tv/Show/S01E02.mkv"}}}
	tr := newTracker(t, p, &fakeZurg{missing: map[string]bool{}}, m, &clock{time.Now()}, reports)
	tr.o.PrewarmRate = 7e6
	tr.poll(context.Background())
	waitPrewarm(t, tr)
	if pw := tr.Snapshot().Sessions[0].Prewarm; pw == nil || pw.State != "ok" || len(m.cached) != 0 {
		t.Fatalf("head check %+v cached %v", pw, m.cached)
	}
	p.mu.Lock()
	p.ss[0].ViewOffset = 1_600_000
	p.mu.Unlock()
	return tr, p
}

// Past half of the episode, the next one is read whole through the mount at the rate cap, its progress shown.
func TestCacheNextEpisode(t *testing.T) {
	m := &fakeMount{block: make(chan struct{})}
	var reports []errinbox.Event
	tr, _ := cacheTracker(t, m, &reports)
	tr.poll(context.Background())
	for range 200 {
		if pw := tr.Snapshot().Sessions[0].Prewarm; pw != nil && pw.State == "caching" {
			if pw.Detail != "caching 50%" || pw.Progress != 0.5 {
				t.Errorf("progress %+v", pw)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	tr.poll(context.Background()) // running: not started again
	close(m.block)
	waitCache(t, tr)
	pw := tr.Snapshot().Sessions[0].Prewarm
	if pw == nil || pw.State != "cached" || pw.Progress != 1 || len(m.cached) != 1 ||
		m.cached[0] != "/mnt/zurg/__all__/Rel3/S01E03.mkv" || m.rate != 7e6 || len(reports) != 0 {
		t.Errorf("prewarm %+v cached %v rate %d reports %v", pw, m.cached, m.rate, reports)
	}
	tr.poll(context.Background()) // once per session and link
	waitCache(t, tr)
	if len(m.cached) != 1 {
		t.Errorf("read again: %v", m.cached)
	}
}

// Before half of the episode, nothing is read whole; a file over prewarm_max_size never is.
func TestCacheNextSkips(t *testing.T) {
	m := &fakeMount{}
	var reports []errinbox.Event
	tr, p := cacheTracker(t, m, &reports)
	p.mu.Lock()
	p.ss[0].ViewOffset = 1_400_000
	p.mu.Unlock()
	tr.poll(context.Background())
	waitCache(t, tr)
	if len(m.cached) != 0 {
		t.Fatalf("read before half: %v", m.cached)
	}
	p.mu.Lock()
	p.ss[0].ViewOffset = 1_600_000
	p.mu.Unlock()
	tr.o.PrewarmMaxSize = 1 << 29
	tr.poll(context.Background())
	waitCache(t, tr)
	pw := tr.Snapshot().Sessions[0].Prewarm
	if len(m.cached) != 0 || pw == nil || pw.State != "ok" || !strings.Contains(pw.Detail, "over prewarm_max_size") {
		t.Errorf("over the cap: cached %v prewarm %+v", m.cached, pw)
	}
}

// A read error mid-file raises the inbox item and stops.
func TestCacheNextError(t *testing.T) {
	m := &fakeMount{cacheErr: errors.New("input/output error")}
	var reports []errinbox.Event
	tr, _ := cacheTracker(t, m, &reports)
	tr.poll(context.Background())
	waitCache(t, tr)
	pw := tr.Snapshot().Sessions[0].Prewarm
	if len(reports) != 1 || reports[0].Key != "1:3" || !strings.Contains(reports[0].Message, "unreadable at 50%") ||
		pw == nil || pw.State != "failed" {
		t.Errorf("reports %+v prewarm %+v", reports, pw)
	}
}

// Once started, the caching read runs to the end: the session ending (or moving on to a movie) does not stop it.
// One runs at a time.
func TestCacheNextFinishesAfterStop(t *testing.T) {
	for _, after := range [][]plex.Session{nil, {{SessionKey: "1", RatingKey: "movie", Type: "movie",
		State: "playing", File: "/media/movie/movie.mkv"}}} {
		m := &fakeMount{block: make(chan struct{})}
		var reports []errinbox.Event
		tr, p := cacheTracker(t, m, &reports)
		tr.poll(context.Background())
		for range 200 {
			m.mu.Lock()
			n := len(m.cached)
			m.mu.Unlock()
			if n == 1 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		p.mu.Lock()
		p.ss = after
		p.mu.Unlock()
		tr.poll(context.Background())
		tr.mu.Lock()
		j := tr.caching
		tr.mu.Unlock()
		if j == nil {
			t.Fatalf("after %v: the caching read stopped", after)
		}
		close(m.block)
		waitCache(t, tr)
		if len(m.cached) != 1 || len(reports) != 0 || m.stopped {
			t.Errorf("after %v: cached %v reports %v stopped %v", after, m.cached, reports, m.stopped)
		}
	}
}
