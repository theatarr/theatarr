package router

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/store"
)

func (p *fakeProvider) remove(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.torrents, id)
}

// One poll that does not find a torrent is not enough to blocklist its release: it takes consecutive not-found
// polls spanning missingGrace, and a poll that finds it again starts over.
func TestNotFoundNeedsRepeatedPolls(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, added_at, own_add, created_at, updated_at)
		VALUES (5, 1, 10, 'movie', ?, 'Movie', 'pub', 'rd', 'RD5', 'downloading', ?, 1, 0, 0)`,
		h("a"), store.Millis(e.clock))
	failed := func() int { return e.count(`SELECT COUNT(*) FROM download WHERE id = 5 AND state = 'failed'`) }
	blocked := func() int { return e.count(`SELECT COUNT(*) FROM router_blocklist WHERE download_id = 5`) }
	for i, step := range []time.Duration{0, time.Minute, 2 * time.Minute} {
		e.clock = e.clock.Add(step)
		if sum, err := e.r.Poll(e.ctx); err != nil || sum.Failed != 0 {
			t.Fatalf("poll %d: %+v %v", i, sum, err)
		}
		if failed() != 0 || blocked() != 0 {
			t.Fatalf("poll %d failed the download after %s", i, step)
		}
	}
	// Found again: the count starts over.
	e.pr["rd"].set("RD5", provider.Torrent{State: provider.StateDownloading, Status: "downloading"})
	e.clock = e.clock.Add(time.Minute)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.pr["rd"].remove("RD5")
	e.clock = e.clock.Add(time.Minute)
	if _, err := e.r.Poll(e.ctx); err != nil || failed() != 0 {
		t.Fatalf("first miss after a find failed the download: %v", err)
	}
	e.clock = e.clock.Add(missingGrace)
	sum, err := e.r.Poll(e.ctx)
	if err != nil || sum.Failed != 1 || failed() != 1 || blocked() != 1 {
		t.Fatalf("repeated misses: %+v %v failed %d blocked %d", sum, err, failed(), blocked())
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = 5 AND error = 'the torrent is no longer on rd'`); n != 1 {
		t.Error("reason")
	}
}

// A reused or finished torrent whose files are known and hold no video fails at once, blocklisted on our side;
// nothing is deleted on the provider.
func TestNoVideoReusedOrFinished(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		pk     string
		reused int
		state  provider.State
	}{
		{"reused downloading", "rd", 1, provider.StateDownloading},
		{"reused downloaded", "rd", 1, provider.StateDownloaded},
		{"finished own add", "ad", 0, provider.StateDownloaded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			withQueue(e)
			e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
				provider, provider_torrent_id, state, added_at, own_add, reused, created_at, updated_at)
				VALUES (5, 1, 10, 'movie', ?, 'Movie', 'pub', ?, 'T5', 'added', ?, ?, ?, 0, 0)`,
				h("a"), tc.pk, store.Millis(e.clock), 1-tc.reused, tc.reused)
			e.pr[tc.pk].set("T5", provider.Torrent{State: tc.state, Status: "downloading",
				Files: []provider.File{{ID: "1", Path: "Movie/readme.txt", Bytes: 10}, {ID: "2", Path: "Movie/cover.jpg"}}})
			sum, err := e.r.Poll(e.ctx)
			if err != nil || sum.Failed != 1 {
				t.Fatalf("poll %+v %v", sum, err)
			}
			if n := e.count(`SELECT COUNT(*) FROM download WHERE id = 5 AND state = 'failed'
				AND error LIKE 'no video file in the torrent%'`); n != 1 {
				t.Fatal("not failed with the reason")
			}
			if n := e.count(`SELECT COUNT(*) FROM router_blocklist WHERE download_id = 5`); n != 1 {
				t.Fatal("not blocklisted")
			}
			if w := e.writes(); w != 0 {
				t.Fatalf("provider writes %d", w)
			}
		})
	}
}
