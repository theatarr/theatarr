package nowplaying

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/errinbox"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/plex"
)

func TestPrewarmBufferingAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, state                                        string
		provider, mountDown, full, transcode, relay, block bool
		pausedCandidate, cacheErrors                       bool
	}{
		{name: "provider", state: "buffering", provider: true, block: true},
		{name: "mount", state: "buffering", mountDown: true, block: true},
		{name: "transcoder takes precedence", state: "buffering", provider: true, transcode: true},
		{name: "network", state: "buffering", relay: true},
		{name: "unknown client", state: "buffering"},
		{name: "paused", state: "paused", provider: true},
		{name: "paused candidate", state: "playing", pausedCandidate: true},
		{name: "full cache", state: "buffering", full: true},
		{name: "cache errors", state: "buffering", cacheErrors: true, block: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &fakeMount{}
			var reports []errinbox.Event
			tr, p := cacheTracker(t, m, &reports)
			if tc.pausedCandidate {
				p.ss[0].State = "paused"
			}
			// Another viewer's playback controls admission, too.
			other := plex.Session{SessionKey: "2", RatingKey: "other", Type: "movie", State: tc.state,
				File: "/other.mkv", Local: true, Relayed: tc.relay}
			if tc.transcode {
				other.Transcode = &plex.Transcode{Speed: 0.5}
			}
			p.ss = append(p.ss, other)
			if tc.provider {
				tr.o.Zurg = &fakeZurg{stats: &library.ZurgStats{BlockedReads: []library.ZurgBlockedRead{{Filename: "other.mkv"}}}}
			}
			if tc.mountDown {
				m.healthy = errors.New("mount down")
			}
			tr.cache, tr.cacheAt = &Cache{OutOfSpace: tc.full}, tr.o.Now()
			if tc.cacheErrors {
				tr.cache.ErroredFiles = 1
			}
			tr.poll(context.Background())
			waitCache(t, tr)
			if got := len(m.cached); (got == 0) != tc.block {
				t.Fatalf("cache reads %d, block=%v, health=%+v", got, tc.block, tr.Snapshot().Sessions[1].Health)
			}
			if tc.block {
				// Deferred reads remain eligible when the foreground problem clears.
				p.ss = p.ss[:1]
				m.healthy = nil
				tr.poll(context.Background())
				waitCache(t, tr)
				if len(m.cached) != 1 {
					t.Fatal("deferred caching read did not start")
				}
			}
		})
	}
}

func TestHeadCheckWaitsForProviderBuffering(t *testing.T) {
	p := &fakePlex{ss: []plex.Session{{SessionKey: "1", RatingKey: "r2", Type: "episode", State: "buffering",
		ViewOffset: 70000, File: "/media/tv/Show/S01E02.mkv"}}}
	z := &fakeZurg{stats: &library.ZurgStats{BlockedReads: []library.ZurgBlockedRead{{Filename: "S01E02.mkv"}}}}
	m := &fakeMount{}
	var reports []errinbox.Event
	tr := newTracker(t, p, z, m, &clock{time.Now()}, &reports)
	tr.poll(context.Background())
	waitPrewarm(t, tr)
	if len(m.read) != 0 {
		t.Fatal("started a head check while the provider stalled playback")
	}
	z.stats = nil
	tr.poll(context.Background())
	waitPrewarm(t, tr)
	if len(m.read) != 1 {
		t.Fatal("head check did not start after provider recovery")
	}
}

func TestNextEpisodeSeriesMonitoring(t *testing.T) {
	st := openStore(t)
	if _, err := st.Writer().Exec(`UPDATE version SET monitored = 0 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	tr := New(Options{Store: st})
	if ep, err := tr.nextEpisode(context.Background(), 1, 1, []int64{2}, time.Now().UnixMilli()); !errors.Is(err, errNoNext) || ep != nil {
		t.Fatalf("unmonitored series: episode=%+v err=%v", ep, err)
	}
}

func TestPickNextAnimeMissingAbsolute(t *testing.T) {
	n := func(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
	eps := []episodeRow{
		{ID: 1, Season: 1, Number: 1, Absolute: n(1)},
		{ID: 2, Season: 1, Number: 2},
		{ID: 3, Season: 1, Number: 3, Absolute: n(3)},
		{ID: 4, Season: 0, Number: 1},
	}
	for _, cur := range [][]int64{{1}, {2}} {
		want := cur[0] + 1
		if ep := pickNext(eps, cur, true); ep == nil || ep.ID != want {
			t.Fatalf("current=%v next=%+v, want episode %d", cur, ep, want)
		}
	}
}

func TestSourceRefreshAfterAdoption(t *testing.T) {
	for _, initial := range []string{"content", "missing"} {
		t.Run(initial, func(t *testing.T) {
			m := &fakeMount{}
			var reports []errinbox.Event
			p := &fakePlex{ss: []plex.Session{{SessionKey: "1", RatingKey: "r2", Type: "episode", State: "playing",
				ViewOffset: 70000, File: "/plex/tv/Show/S01E02.mkv"}}}
			c := &clock{time.Now()}
			tr := newTracker(t, p, nil, m, c, &reports)
			if _, err := tr.o.Store.Writer().Exec(`UPDATE link SET state = 'unlinked' WHERE id = 10`); err != nil {
				t.Fatal(err)
			}
			if initial == "missing" {
				if _, err := tr.o.Store.Writer().Exec(`UPDATE content SET mount_folder = NULL WHERE id = 50`); err != nil {
					t.Fatal(err)
				}
			}
			tr.poll(context.Background())
			if src := tr.Snapshot().Sessions[0].Source; src.LinkID != 0 {
				t.Fatalf("unexpected initial source %+v", src)
			}
			if _, err := tr.o.Store.Writer().Exec(`UPDATE link SET state = 'active' WHERE id = 10`); err != nil {
				t.Fatal(err)
			}
			// An unlinked source is looked up again within resolveEvery (not on every poll).
			c.t = c.t.Add(resolveEvery)
			tr.poll(context.Background())
			waitPrewarm(t, tr)
			se := tr.Snapshot().Sessions[0]
			if se.Source.LinkID != 10 || se.Prewarm == nil || se.Prewarm.State != "ok" || len(m.read) != 1 {
				t.Fatalf("adopted source not picked up: %+v prewarm=%+v reads=%v", se.Source, se.Prewarm, m.read)
			}
		})
	}
}

func TestCauseWithoutEvidence(t *testing.T) {
	tr := New(Options{})
	for _, local := range []bool{true, false} {
		se := &session{s: plex.Session{State: "buffering", File: "/tv/show.mkv", Local: local}}
		if cause, detail := tr.cause(se, nil); cause != "unknown" || detail == "" {
			t.Errorf("local=%v: cause=%q detail=%q", local, cause, detail)
		}
	}
}

func TestCacheProgressSharedAfterViewerLeaves(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "remaining viewer", true: "new viewer"}[late], func(t *testing.T) {
			testCacheProgressShared(t, late)
		})
	}
}

func testCacheProgressShared(t *testing.T, late bool) {
	t.Helper()
	m := &fakeMount{block: make(chan struct{})}
	var reports []errinbox.Event
	tr, p := cacheTracker(t, m, &reports)
	remaining := p.ss[0]
	remaining.SessionKey = "2"
	if !late {
		p.ss = append(p.ss, remaining)
	}
	tr.poll(context.Background())
	var release sync.Once
	finish := func() { release.Do(func() { close(m.block) }); waitCache(t, tr) }
	t.Cleanup(finish)
	deadline := time.Now().Add(2 * time.Second)
	for {
		pw := tr.Snapshot().Sessions[0].Prewarm
		if pw != nil && pw.State == "caching" && pw.Progress == 0.5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cache read did not reach halfway")
		}
		time.Sleep(time.Millisecond)
	}
	waitPrewarm(t, tr)
	// The initiating viewer leaves; the other viewer can already be playing or join on this poll.
	p.ss = []plex.Session{remaining, {SessionKey: "3", RatingKey: "movie", Type: "movie", State: "playing"}}
	tr.poll(context.Background())
	waitPrewarm(t, tr)
	ss := tr.Snapshot().Sessions
	if pw := ss[0].Prewarm; pw == nil || pw.State != "caching" || pw.Progress != 0.5 {
		t.Fatalf("remaining viewer lost progress: %+v", pw)
	}
	if ss[1].Prewarm != nil {
		t.Fatalf("unrelated playback got progress: %+v", ss[1].Prewarm)
	}
	finish()
	if pw := tr.Snapshot().Sessions[0].Prewarm; pw == nil || pw.State != "cached" || pw.Progress != 1 {
		t.Fatalf("remaining viewer lost completion: %+v", pw)
	}
	tr.poll(context.Background())
	waitCache(t, tr)
	if len(m.cached) != 1 {
		t.Fatalf("the remaining viewer read the same cached file again: %v", m.cached)
	}
}

func TestCacheProgressVersionScope(t *testing.T) {
	m := &fakeMount{block: make(chan struct{})}
	var reports []errinbox.Event
	tr, _ := cacheTracker(t, m, &reports)
	tr.poll(context.Background())
	t.Cleanup(func() { close(m.block); waitCache(t, tr) })
	deadline := time.Now().Add(2 * time.Second)
	for {
		pw := tr.Snapshot().Sessions[0].Prewarm
		if pw != nil && pw.Progress == 0.5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cache read did not reach halfway")
		}
		time.Sleep(time.Millisecond)
	}
	tr.mu.Lock()
	tr.sess["2"] = &session{s: plex.Session{SessionKey: "2", Type: "episode"},
		src: &Source{TitleID: 1, versionIDs: []int64{2}}}
	tr.mu.Unlock()
	ss := tr.Snapshot().Sessions
	if ss[0].Prewarm == nil || ss[0].Prewarm.Progress != 0.5 || ss[1].Prewarm != nil {
		t.Fatalf("progress crossed versions: same=%+v other=%+v", ss[0].Prewarm, ss[1].Prewarm)
	}
}
