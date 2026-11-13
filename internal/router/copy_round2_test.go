package router

import (
	"context"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
)

// The second fix round (copy.go): overlap with routes under the add lock, state and evidence, names.

// 1: a route's add of the hash that starts while the copy reads DMM is seen under the add lock: no second add.
func TestCopyRound2UnsettledRouteUnderLock(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloaded, "rd")
	e.r.availHash = func(context.Context, string, string) (bool, bool, error) {
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
			state, created_at, updated_at) VALUES (5, 1, 10, 'movie', ?, 'x', 'pub', 'rd', 'adding', 0, 0)`, h("a"))
		return false, true, nil
	}
	e.queue(h("a"), "Movie.2024.2160p.WEB-GRP")
	e.run()
	if s, _ := e.copyState(h("a"), "rd"); s != CopyQueued || len(e.ad["rd"].adds) != 0 {
		t.Fatalf("%s, adds %v", s, e.ad["rd"].adds)
	}
}

// 2: a route does not reuse a registry entry of the hash while a copy there is unsettled.
func TestCopyRound2RouteDoesNotAdoptCopy(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloaded, "rd")
	e.pol.Versions["movie"].Providers = []string{"rd"}
	e.pr["rd"].set("rd-9", provider.Torrent{Hash: h("a"), State: provider.StateDownloading})
	e.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (7, ?, 'present', 0, 0)`, h("a"))
	e.exec(`INSERT INTO content_provider (content_id, provider, torrent_id, seen_at, state, progress)
		VALUES (7, 'rd', 'rd-9', 0, 'present', 40)`)
	e.exec(`INSERT INTO provider_copy (info_hash, name, provider, torrent_id, own_add, state, created_at, updated_at)
		VALUES (?, 'x', 'rd', 'rd-9', 1, 'checking', 0, 0)`, h("a"))
	res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.Reused || len(res.Steps) == 0 || res.Steps[0].Code != codeUnsettled {
		t.Fatalf("%+v %v", res, err)
	}
}

// 3: the last "in use" check and the delete run under the add lock.
func TestCopyRound2DeleteUnderLock(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloading, "rd")
	locked := false
	e.del["rd"].during = func() {
		if e.r.mu.TryLock() {
			e.r.mu.Unlock()
		} else {
			locked = true
		}
	}
	e.queue(h("a"), "Movie.2024.2160p.WEB-GRP")
	e.run()
	if e.del["rd"].deleted() != 1 || !locked {
		t.Fatalf("%d deletes, locked %v", e.del["rd"].deleted(), locked)
	}
}

// 4: an owed delete that finds the torrent complete keeps it and replaces the uncached evidence with cached.
func TestCopyRound2CompleteAtDeleteIsCachedEvidence(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloaded, "rd")
	e.exec(`INSERT INTO provider_cache (info_hash, provider, state, source, checked_at, expires_at)
		VALUES (?, 'rd', 'uncached', 'router:add', 0, ?)`, h("a"), e.clock.Add(time.Hour).UnixMilli())
	e.seedChecking("rd", "rd-1", provider.StateDownloaded, true)
	e.run()
	if st, _ := e.r.cacheState(e.ctx, h("a"), "rd", e.clock); st != "cached" {
		t.Fatalf("provider_cache %q", st)
	}
}

// 6: Pause postpones an adding row before it is rewound; unpaused, it resumes.
func TestCopyRound2PauseKeepsAdding(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloaded, "rd")
	e.exec(`INSERT INTO provider_copy (info_hash, name, provider, own_add, state, created_at, updated_at)
		VALUES (?, 'Movie.2024.2160p.WEB-GRP', 'rd', 1, 'adding', 0, 0)`, h("a"))
	e.r.set.Enabled = false
	if _, err := e.r.ResumeCopies(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.run()
	if s, _ := e.copyState(h("a"), "rd"); s != CopyAdding {
		t.Fatalf("paused: %s", s)
	}
	e.r.set.Enabled = true
	e.clock = e.clock.Add(time.Hour)
	e.run()
	if s, _ := e.copyState(h("a"), "rd"); s != CopyAdded {
		t.Fatalf("unpaused: %s", s)
	}
}

// 7: a Real-Debrid trial with nothing playable to select ends failed, and our trial add is cleaned up.
func TestCopyRound2NoPlayableFile(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloaded, "rd")
	e.ad["rd"].add = func(m adder.Magnet, _ adder.AddOptions) (*adder.Added, error) {
		e.pr["rd"].set("rd-1", provider.Torrent{Hash: m.Hash(), State: provider.StateQueued, Status: rdWaitingFiles,
			Files: []provider.File{{ID: "1", Path: "/Movie/VIDEO_TS/disc.iso", Bytes: 8 << 30}}})
		return &adder.Added{ID: "rd-1", Hash: m.Hash()}, nil
	}
	e.queue(h("a"), "Movie.2024.DVD-GRP")
	e.run()
	if s, msg := e.copyState(h("a"), "rd"); s != CopyFailed || msg != "Real-Debrid cannot serve this file type" ||
		e.del["rd"].deleted() != 1 {
		t.Fatalf("%s %q, %d deletes", s, msg, e.del["rd"].deleted())
	}
}

// 8: the poller resumes an unfinished copy left without a job.
func TestCopyRound2PollResumes(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloaded, "rd")
	e.exec(`INSERT INTO provider_copy (info_hash, name, provider, torrent_id, own_add, state, created_at, updated_at)
		VALUES (?, 'x', 'rd', 'rd-1', 1, 'checking', 0, 0)`, h("a"))
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = ? AND state = 'queued'`, JobCopy); n != 1 {
		t.Fatalf("%d jobs", n)
	}
}

// 9: the uncached verdict is counted with the owed delete in one transaction: a failed write counts nothing, and the
// retry counts once.
func TestCopyRound2CountWithVerdict(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloading, "rd")
	e.exec(`CREATE TRIGGER copy_owed_fails BEFORE UPDATE OF message ON provider_copy
		WHEN NEW.message LIKE 'deleting our add %' BEGIN SELECT RAISE(ABORT, 'interrupted'); END`)
	e.queue(h("a"), "Movie.2024.2160p.WEB-GRP")
	e.run()
	e.exec(`DROP TRIGGER copy_owed_fails`)
	e.clock = e.clock.Add(2 * time.Minute) // the same clock hour
	e.run()
	if p, _ := loadPace(e.ctx, e.st.Writer(), "rd"); p.HourUncached != 1 {
		t.Fatalf("counted %d times", p.HourUncached)
	}
}

// 10: the name rule sees a download's original name and an arr file found by its download id; a second request that
// brings a refused name turns a queued row refused.
func TestCopyRound2Names(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloaded, "rd")
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, original_name, indexer,
		provider, state, created_at, updated_at) VALUES (5, 1, 10, 'movie', ?, 'Movie 2024', 'Movie.2024.1080p.WEB.x264-YIFY',
		'pub', 'ad', 'imported', 0, 0)`, h("a"))
	e.exec(`INSERT INTO version_file (version_id, title_id, instance, arr_file_id, path, size_bytes, quality, languages,
		release_title, download_id) VALUES (10, 1, 'radarr', 9, '/m/b.mkv', 1, 'x', '[]', 'Other.2024.1080p.WEB.x264-YIFY', ?)`,
		h("b"))
	e.queue(h("a"), "Movie 2024")
	e.queue(h("b"), "Other 2024")
	e.queue(h("c"), "Third 2024")
	if _, err := e.r.QueueCopies(e.ctx, CopyRequest{Hash: h("c"), Names: []string{"Third 2024", "Third.2024.WEB.x264-YIFY"}}); err != nil {
		t.Fatal(err)
	}
	for _, x := range []string{"a", "b", "c"} {
		if s, _ := e.copyState(h(x), "rd"); s != CopyRefused {
			t.Errorf("%s: %s", x, s)
		}
	}
	e.run()
	if len(e.ad["rd"].adds) != 0 {
		t.Fatalf("adds %v", e.ad["rd"].adds)
	}
}
