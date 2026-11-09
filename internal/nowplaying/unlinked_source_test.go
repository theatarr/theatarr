package nowplaying

import (
	"context"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/errinbox"
	"github.com/theatarr/theatarr/internal/plex"
)

// A playback whose file resolves to no Theatarr link is looked up again only every resolveEvery, not
// on every poll.
func TestUnlinkedSourceResolvedAtMostEveryFewMinutes(t *testing.T) {
	ctx := context.Background()
	c := &clock{time.Now()}
	p := &fakePlex{ss: []plex.Session{{SessionKey: "1", RatingKey: "m", Type: "movie", State: "playing",
		File: "/elsewhere/Movie/movie.mkv"}}}
	var reports []errinbox.Event
	tr := newTracker(t, p, nil, &fakeMount{}, c, &reports)
	tr.poll(ctx)
	tr.mu.Lock()
	first := tr.sess["1"].src
	tr.mu.Unlock()
	if first == nil || first.LinkID != 0 {
		t.Fatalf("fixture: source %+v", first)
	}
	c.t = c.t.Add(10 * time.Second)
	tr.poll(ctx)
	tr.mu.Lock()
	again := tr.sess["1"].src
	tr.mu.Unlock()
	if again != first {
		t.Fatal("an unlinked source was resolved again on the next poll")
	}
	c.t = c.t.Add(resolveEvery)
	tr.poll(ctx)
	tr.mu.Lock()
	later := tr.sess["1"].src
	tr.mu.Unlock()
	if later == first {
		t.Fatal("an unlinked source was never resolved again")
	}
}

// Cache progress shows only on sessions whose next episode is the file being cached, not on another
// viewer of the same series version watching another episode.
func TestCacheProgressOnlyForNextEpisode(t *testing.T) {
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
	cur := tr.sess["1"]
	tr.sess["2"] = &session{s: plex.Session{SessionKey: "2", Type: "episode"}, src: cur.src,
		next: &nextFile{linkID: cur.next.linkID, target: "/mnt/zurg/__all__/Rel9/S01E09.mkv"}}
	tr.mu.Unlock()
	ss := tr.Snapshot().Sessions
	if ss[0].Prewarm == nil || ss[0].Prewarm.Progress != 0.5 || ss[1].Prewarm != nil {
		t.Fatalf("progress on another episode's viewer: same=%+v other=%+v", ss[0].Prewarm, ss[1].Prewarm)
	}
}
