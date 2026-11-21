package router

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/store"
)

func TestProviderFailuresBlocklistAndResearch(t *testing.T) {
	for _, tc := range []struct {
		name, why string
		torrent   *provider.Torrent
		afterAdd  bool
	}{
		{"error", "provider status error", &provider.Torrent{State: provider.StateError, Status: "error"}, false},
		{"gone", "the torrent is no longer on rd", nil, false},
		{"no video", "no video file in the torrent; nothing selected", &provider.Torrent{
			Status: rdWaitingFiles, Files: []provider.File{{ID: "1", Path: "readme.txt"}}}, false},
		{"error after add", "provider status error", &provider.Torrent{State: provider.StateError, Status: "error"}, true},
		{"no video after add", "no video file in the torrent; nothing selected", &provider.Torrent{
			Status: rdWaitingFiles, Files: []provider.File{{ID: "1", Path: "readme.txt"}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, true)
			withQueue(e)
			e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
				provider, provider_torrent_id, state, episodes, added_at, own_add, created_at, updated_at)
				VALUES (5, 2, 20, 'series', ?, 'Show', 'pub', 'rd', 'RD5', 'added', '[7,8]', ?, 1, 0, 0)`,
				h("a"), store.Millis(e.clock.Add(-time.Hour))) // explicit failures take precedence over stalled cleanup
			if tc.torrent != nil {
				e.pr["rd"].set("RD5", *tc.torrent)
			}
			if tc.afterAdd {
				e.r.trySelect(e.ctx, 5, "series", policy.KindSeries, "rd", "RD5")
			}
			if tc.torrent == nil {
				// Gone: a first miss only counts; the release is blocklisted once the torrent stays missing.
				if sum, err := e.r.Poll(e.ctx); err != nil || sum.Failed != 0 {
					t.Fatalf("first miss: %+v, %v", sum, err)
				}
				e.clock = e.clock.Add(missingGrace)
			}
			sum, err := e.r.Poll(e.ctx)
			if err != nil || sum.Researched != 1 || (!tc.afterAdd && sum.Failed != 1) {
				t.Fatalf("poll: %+v, %v", sum, err)
			}
			if n := e.count(`SELECT COUNT(*) FROM download WHERE id = 5 AND state = 'failed' AND error = ?`, tc.why); n != 1 {
				t.Fatal("download not failed with the reason")
			}
			if n := e.count(`SELECT COUNT(*) FROM router_blocklist WHERE version_key = 'series' AND info_hash = ?
				AND title_id = 2 AND download_id = 5 AND reason = ? AND source = ? AND researched_at IS NOT NULL`,
				h("a"), tc.why, BlocklistProvider); n != 1 {
				t.Fatal("failure not blocklisted and researched")
			}
			got := researchJobs(e)
			if len(got) != 1 || got[0].Reason != ReasonProvider || got[0].TitleID != 2 || got[0].VersionID != 20 ||
				got[0].Version != "series" || got[0].DownloadID != 5 || len(got[0].Episodes) != 2 ||
				got[0].Episodes[0] != 7 || got[0].Episodes[1] != 8 {
				t.Fatalf("re-search: %+v", got)
			}
			if inFlight, err := InFlight(e.ctx, e.st.Reader(), 2, 20, []int64{7}); err != nil || inFlight {
				t.Fatalf("in flight: %v, %v", inFlight, err)
			}
			if sum, err := e.r.Poll(e.ctx); err != nil || sum.Researched != 0 || len(researchJobs(e)) != 1 {
				t.Fatalf("second poll: %+v, %v", sum, err)
			}
			res, err := e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series",
				Candidates: []Candidate{withEps(cand(h("a"), "Show"), 7, 8)}})
			if err != nil || !strings.Contains(steps(res), "blocklisted") || e.writes() != 0 {
				t.Fatalf("route: %+v, %v; provider writes %d", res, err, e.writes())
			}
		})
	}
}

func TestReadyDeadline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout time.Duration
		readyAt bool
		addedAt bool
		claim   bool
		waiting bool
	}{
		{name: "default", readyAt: true, waiting: true},
		{name: "configured", timeout: time.Hour, readyAt: true},
		{name: "legacy added_at", addedAt: true},
		{name: "legacy created_at"},
		{name: "live claim", readyAt: true, claim: true, waiting: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, true)
			withQueue(e)
			if tc.timeout != 0 {
				e.r.set.ReadyTimeout = tc.timeout
			}
			start := e.clock
			var readyAt, addedAt any
			createdAt := start
			if tc.readyAt {
				readyAt = store.Millis(start)
				addedAt = store.Millis(start.Add(-24 * time.Hour))
				createdAt = start.Add(-48 * time.Hour)
			} else if tc.addedAt {
				addedAt = store.Millis(start)
				createdAt = start.Add(-24 * time.Hour)
			}
			// No provider is needed to expire ready downloads, including a library mount outage with no import row.
			e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
				state, ready_at, added_at, created_at, updated_at) VALUES (5, 1, 10, 'movie', ?, 'Movie', 'pub', 'ready', ?, ?, ?, ?)`,
				h("a"), readyAt, addedAt, store.Millis(createdAt), store.Millis(start))
			if tc.waiting {
				e.exec(`INSERT INTO library_import (download_id, state, waiting_since, created_at, updated_at)
					VALUES (5, 'waiting_mount', ?, ?, ?)`, store.Millis(start), store.Millis(start), store.Millis(start))
			}
			e.clock = start.Add(e.r.set.ReadyTimeout - time.Millisecond)
			if sum, err := e.r.Poll(e.ctx); err != nil || sum.Failed != 0 || sum.Read != 0 {
				t.Fatalf("before deadline: %+v, %v", sum, err)
			}
			e.clock = e.clock.Add(time.Millisecond)
			if tc.claim {
				e.exec(`INSERT INTO library_claim (download_id, owner, until) VALUES (5, 'importer', ?)`,
					store.Millis(e.clock.Add(time.Minute)))
				if sum, err := e.r.Poll(e.ctx); err != nil || sum.Failed != 0 || len(researchJobs(e)) != 0 {
					t.Fatalf("live claim: %+v, %v", sum, err)
				}
				e.clock = e.clock.Add(time.Minute)
			}
			// A retry's updated_at must not extend the ready deadline.
			e.exec(`UPDATE download SET updated_at = ? WHERE id = 5`, store.Millis(e.clock))
			sum, err := e.r.Poll(e.ctx)
			if err != nil || sum.Failed != 1 || sum.Researched != 1 || sum.Read != 0 {
				t.Fatalf("deadline: %+v, %v", sum, err)
			}
			if n := e.count(`SELECT COUNT(*) FROM download WHERE id = 5 AND state = 'failed'
				AND error LIKE 'ready timeout:%'`); n != 1 {
				t.Fatal("ready download not failed")
			}
			if n := e.count(`SELECT COUNT(*) FROM router_blocklist WHERE version_key = 'movie' AND info_hash = ?
				AND download_id = 5 AND source = ? AND researched_at IS NOT NULL`, h("a"), BlocklistReadyTimeout); n != 1 {
				t.Fatal("ready timeout not blocklisted and researched")
			}
			if tc.waiting && e.count(`SELECT COUNT(*) FROM library_import WHERE download_id = 5 AND state = 'failed'
				AND last_error LIKE 'ready timeout:%'`) != 1 {
				t.Fatal("mount wait not ended")
			}
			if e.count(`SELECT COUNT(*) FROM library_claim WHERE download_id = 5`) != 0 {
				t.Fatal("expired claim not invalidated")
			}
			got := researchJobs(e)
			if len(got) != 1 || got[0].Reason != ReasonReadyTimeout || got[0].TitleID != 1 ||
				got[0].VersionID != 10 || got[0].DownloadID != 5 {
				t.Fatalf("re-search: %+v", got)
			}
			if inFlight, err := InFlight(e.ctx, e.st.Reader(), 1, 10, nil); err != nil || inFlight {
				t.Fatalf("in flight: %v, %v", inFlight, err)
			}
			if sum, err := e.r.Poll(e.ctx); err != nil || sum.Researched != 0 || len(researchJobs(e)) != 1 || e.writes() != 0 {
				t.Fatalf("second poll: %+v, %v; provider writes %d", sum, err, e.writes())
			}
		})
	}
}

func TestHeldReadyImportDoesNotExpire(t *testing.T) {
	// An import rejected as not an upgrade, and one waiting for its episode title (library.TitleHoldPrefix).
	for _, held := range [][2]string{{"rejected", "not an upgrade"}, {"waiting_mount", "episode title hold: Episode has a TBA title and recently aired"}} {
		t.Run(held[0], func(t *testing.T) {
			e := newEnv(t, true)
			withQueue(e)
			start := e.clock
			e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
				ready_at, added_at, created_at, updated_at) VALUES (5, 1, 10, 'movie', ?, 'Movie', 'pub', 'ready', ?, ?, ?, ?)`,
				h("a"), store.Millis(start.Add(-24*time.Hour)), store.Millis(start.Add(-24*time.Hour)),
				store.Millis(start.Add(-24*time.Hour)), store.Millis(start.Add(-24*time.Hour)))
			e.exec(`INSERT INTO library_import (download_id, state, last_error, created_at, updated_at)
				VALUES (5, ?, ?, ?, ?)`, held[0], held[1], store.Millis(start.Add(-24*time.Hour)), store.Millis(start.Add(-24*time.Hour)))
			e.clock = start.Add(e.r.set.ReadyTimeout + time.Hour)
			sum, err := e.r.Poll(e.ctx)
			if err != nil || sum.Failed != 0 || sum.Researched != 0 {
				t.Fatalf("held ready poll: %+v, %v", sum, err)
			}
			if got := e.count(`SELECT COUNT(*) FROM download WHERE id = 5 AND state = 'ready'`); got != 1 {
				t.Fatalf("held download state count %d", got)
			}
			if got := e.count(`SELECT COUNT(*) FROM router_blocklist WHERE download_id = 5`); got != 0 {
				t.Fatalf("held blocklist rows %d", got)
			}
			if inFlight, err := InFlight(e.ctx, e.st.Reader(), 1, 10, nil); err != nil || !inFlight {
				t.Fatalf("held in flight: %v, %v", inFlight, err)
			}
		})
	}
}

// TestReadyDeadlineMismatchNotBlocklisted: a ready download past its deadline whose import waits because the mount
// and zurg disagree (ImportMismatchPrefix) fails without blocklist and without a re-search; one waiting for the
// release to show on the mount fails, blocklisted and re-searched, in the same pass.
func TestReadyDeadlineMismatchNotBlocklisted(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	withQueue(e)
	start := e.clock
	for _, w := range []struct {
		id   int64
		hash string
		err  string
	}{
		{5, h("a"), ImportMismatchPrefix + "zurg does not confirm the files are this download's (zurg: the file is 6 bytes, the mount shows 5)"},
		{6, h("b"), "release not on the mount yet"},
	} {
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
			state, ready_at, created_at, updated_at) VALUES (?, 1, 10, 'movie', ?, 'Movie', 'pub', 'ready', ?, ?, ?)`,
			w.id, w.hash, store.Millis(start), store.Millis(start), store.Millis(start))
		e.exec(`INSERT INTO library_import (download_id, state, waiting_since, last_error, created_at, updated_at)
			VALUES (?, 'waiting_mount', ?, ?, ?, ?)`, w.id, store.Millis(start), w.err, store.Millis(start), store.Millis(start))
	}
	e.clock = start.Add(e.r.set.ReadyTimeout)
	sum, err := e.r.Poll(e.ctx)
	if err != nil || sum.Failed != 2 || sum.Researched != 1 {
		t.Fatalf("deadline: %+v, %v", sum, err)
	}
	var got string
	if err := e.st.Reader().QueryRow(`SELECT state || ' ' || error FROM download WHERE id = 5`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "failed ready timeout:") ||
		!strings.Contains(got, ImportMismatchPrefix) || !strings.HasSuffix(got, "; not blocklisted") {
		t.Errorf("mismatch download: %s", got)
	}
	if n := e.count(`SELECT COUNT(*) FROM library_import WHERE download_id = 5 AND state = 'failed'`); n != 1 {
		t.Error("mismatch wait not ended")
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist WHERE download_id = 5`); n != 0 {
		t.Errorf("mismatch blocklisted (%d rows)", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist WHERE download_id = 6 AND source = ?`, BlocklistReadyTimeout); n != 1 {
		t.Errorf("mount wait blocklisted %d times", n)
	}
	if got := researchJobs(e); len(got) != 1 || got[0].DownloadID != 6 {
		t.Errorf("re-search: %+v", got)
	}
}

func TestReadyDeadlineKeepsOtherStates(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	withQueue(e)
	for i, state := range []string{StateQueued, StateAdding, StateImporting, StateImported, StateFailed} {
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
			state, ready_at, created_at, updated_at) VALUES (?, 1, 10, 'movie', ?, 'Movie', 'pub', ?, 0, 0, ?)`,
			i+1, h("a"), state, store.Millis(e.clock))
	}
	if sum, err := e.r.Poll(e.ctx); err != nil || sum.Failed != 0 || len(researchJobs(e)) != 0 {
		t.Fatalf("poll: %+v, %v", sum, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE state IN ('queued', 'adding', 'importing', 'imported')`); n != 4 {
		t.Fatal("a non-ready download changed state")
	}
}

func TestReadyDeadlineWaitsForMount(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	withQueue(e)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
		state, ready_at, created_at, updated_at) VALUES (1, 1, 10, 'movie', ?, 'Movie', 'pub', ?, 0, 0, 0)`,
		h("a"), StateReady)
	down := errors.New("mount down")
	e.r.SetMountCheck(func(context.Context) error { return down })
	if sum, err := e.r.Poll(e.ctx); err != nil || sum.Failed != 0 {
		t.Fatalf("poll with the mount down: %+v, %v", sum, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
		t.Fatal("a ready download was blocklisted while the mount was down")
	}
	down = nil
	if sum, err := e.r.Poll(e.ctx); err != nil || sum.Failed != 1 {
		t.Fatalf("poll with the mount back: %+v, %v", sum, err)
	}
}

func TestFailureAndBlocklistAtomic(t *testing.T) {
	t.Parallel()
	for _, state := range []string{StateAdded, StateDownloading, StateReady} {
		t.Run(state, func(t *testing.T) {
			e := newEnv(t, true)
			withQueue(e)
			// Missing once already, a grace ago: this poll's miss is the one that blocklists.
			e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
				provider, provider_torrent_id, state, ready_at, created_at, updated_at, missing_polls, missing_since)
				VALUES (5, 1, 10, 'movie', ?, 'Movie', 'pub', 'rd', 'RD5', ?, 0, 0, 0, 1, ?)`, h("a"), state,
				store.Millis(e.clock.Add(-missingGrace)))
			if state == StateDownloading {
				e.pr["rd"].set("RD5", provider.Torrent{State: provider.StateError, Status: "error"})
			}
			e.exec(`CREATE TRIGGER fail_blocklist BEFORE INSERT ON router_blocklist BEGIN
				SELECT RAISE(ABORT, 'blocklist unavailable'); END`)
			sum, err := e.r.Poll(e.ctx)
			if err == nil && sum.Errors == 0 {
				t.Fatal("blocklist failure not reported")
			}
			if n := e.count(`SELECT COUNT(*) FROM download WHERE id = 5 AND state = ? AND error IS NULL`, state); n != 1 {
				t.Fatal("download failed without its blocklist row")
			}
			if len(researchJobs(e)) != 0 {
				t.Fatal("rolled-back failure triggered re-search")
			}
		})
	}
}

// TestReadyDeadlineMismatchAfterPartial: a download left importing by a partial import whose next pass waits on a
// mount mismatch still meets its deadline, failed without blocklist.
func TestReadyDeadlineMismatchAfterPartial(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	withQueue(e)
	start := e.clock
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
		state, ready_at, created_at, updated_at) VALUES (5, 1, 10, 'movie', ?, 'Movie', 'pub', 'importing', ?, ?, ?)`,
		h("a"), store.Millis(start), store.Millis(start), store.Millis(start))
	e.exec(`INSERT INTO library_import (download_id, state, waiting_since, last_error, created_at, updated_at)
		VALUES (5, 'waiting_mount', ?, ?, ?, ?)`, store.Millis(start), ImportMismatchPrefix+"zurg does not confirm (x)",
		store.Millis(start), store.Millis(start))
	e.clock = start.Add(e.r.set.ReadyTimeout)
	if sum, err := e.r.Poll(e.ctx); err != nil || sum.Failed != 1 || sum.Researched != 0 {
		t.Fatalf("deadline: %+v, %v", sum, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
		t.Errorf("blocklisted (%d rows)", n)
	}
}

// C8: an add the provider takes whose download fails in the reads right after it (a provider error status, or files
// rejected at selection) ends that release only: the route goes on with the next ranked release. The failed download
// is offered to no other provider, no miss is cached for it, and its blocklist row starts no re-search when the next
// release covers it. With cached-only on (settle) and off (one read for the selection).
func TestFailedAtOnceTriesNextRelease(t *testing.T) {
	t.Parallel()
	failing := map[string]provider.Torrent{
		"provider_error": {Hash: h("a"), Status: "magnet_error", State: provider.StateError},
		"files_rejected": {Hash: h("a"), Status: rdWaitingFiles, State: provider.StateQueued,
			Files: []provider.File{{ID: "1", Path: "Movie/Movie.mkv", Bytes: 9000}, {ID: "2", Path: "Movie/setup.exe", Bytes: 1}}},
	}
	for way, bad := range failing {
		for _, cachedOnly := range []bool{true, false} {
			t.Run(way+map[bool]string{true: "/cached_only", false: "/not_cached_only"}[cachedOnly], func(t *testing.T) {
				t.Parallel()
				e := newEnv(t, true)
				e.r.set.CachedOnly = cachedOnly
				e.pr["rd"].set("rd-aaaa", bad)
				e.pr["rd"].set("rd-bbbb", provider.Torrent{Hash: h("b"), Status: "downloaded", State: provider.StateDownloaded,
					Files: []provider.File{{ID: "1", Path: "Movie/Movie.mkv", Bytes: 9000, Selected: true}}})
				res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-BAD"),
					cand(h("b"), "Movie.2024.2160p.WEB.H265-GOOD")))
				if err != nil || res.Outcome != OutcomeAdded || res.Release != "Movie.2024.2160p.WEB.H265-GOOD" {
					t.Fatalf("route: %+v %v steps=%s", res, err, steps(res))
				}
				if !strings.Contains(steps(res), "aaaa/rd/error/"+codeFailedAtOnce) {
					t.Errorf("steps %s", steps(res))
				}
				if len(e.ad["rd"].adds) != 2 || len(e.ad["ad"].adds)+len(e.ad["tb"].adds) != 0 {
					t.Errorf("adds rd=%v ad=%v tb=%v", e.ad["rd"].adds, e.ad["ad"].adds, e.ad["tb"].adds)
				}
				if n := e.count(`SELECT COUNT(*) FROM download WHERE info_hash = ? AND state = 'failed'`, h("a")); n != 1 {
					t.Error("the failed release's download is not failed")
				}
				if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE info_hash = ? AND state IN ('miss', 'uncached')`,
					h("a")); n != 0 {
					t.Errorf("%d miss rows cached for the failed release", n)
				}
				if n := e.count(`SELECT COUNT(*) FROM router_blocklist WHERE info_hash = ? AND researched_at IS NOT NULL`,
					h("a")); n != 1 {
					t.Error("the covered failure would start a re-search of its own")
				}
			})
		}
	}
}

// Only a failed row ends a release at once. A selection the provider answers with an error leaves the download added:
// the route waits for it as today and adds nothing else (two releases would land for one movie).
func TestSelectionErrorStillWaits(t *testing.T) {
	t.Parallel()
	for _, cachedOnly := range []bool{true, false} {
		e := newEnv(t, true)
		e.r.set.CachedOnly = cachedOnly
		e.ad["rd"].onSelect = func(context.Context) error { return errors.New("503 service unavailable") }
		e.pr["rd"].set("rd-aaaa", provider.Torrent{Hash: h("a"), Status: rdWaitingFiles, State: provider.StateQueued,
			Files: []provider.File{{ID: "1", Path: "Movie/Movie.mkv", Bytes: 9000}}})
		res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-BAD"),
			cand(h("b"), "Movie.2024.2160p.WEB.H265-GOOD")))
		if err != nil || res.Outcome != OutcomeAdded || res.Release != "Movie.2024.2160p.WEB.H265-BAD" {
			t.Fatalf("cached_only=%v route: %+v %v steps=%s", cachedOnly, res, err, steps(res))
		}
		if len(e.ad["rd"].adds) != 1 || e.count(`SELECT COUNT(*) FROM download WHERE state = 'added'`) != 1 {
			t.Errorf("cached_only=%v: adds %v", cachedOnly, e.ad["rd"].adds)
		}
	}
}

// Every release failing at once: the route ends as one whose releases all missed does (exhausted, not added), each
// failure counted as one attempt, and the blocklist rows still start their re-search. A series release that fails at
// once is not covered by a later release of fewer episodes.
func TestFailedAtOnceEverywhere(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.pr["rd"].set("rd-aaaa", provider.Torrent{Hash: h("a"), Status: "magnet_error", State: provider.StateError})
	e.pr["rd"].set("rd-bbbb", provider.Torrent{Hash: h("b"), Status: "virus", State: provider.StateError})
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-A"), cand(h("b"), "Movie.2024.2160p.WEB.H265-B")))
	if err != nil || res.Outcome != OutcomeExhausted {
		t.Fatalf("route: %+v %v steps=%s", res, err, steps(res))
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist WHERE researched_at IS NULL`); n != 2 {
		t.Errorf("%d blocklist rows left to re-search, want 2", n)
	}
	e.r.set.MaxCandidates = 1
	e.pr["rd"].set("rd-cccc", provider.Torrent{Hash: h("c"), Status: "magnet_error", State: provider.StateError})
	res, err = e.r.Route(e.ctx, grabReq(cand(h("c"), "Movie.2024.2160p.WEB.H265-C"), cand(h("d"), "Movie.2024.2160p.WEB.H265-D")))
	if err != nil || res.Outcome != OutcomeExhausted || len(e.ad["rd"].adds) != 3 {
		t.Fatalf("one attempt allowed: %+v %v adds=%v steps=%s", res, err, e.ad["rd"].adds, steps(res))
	}

	s := newEnv(t, true)
	s.pr["rd"].set("rd-eeee", provider.Torrent{Hash: h("e"), Status: "magnet_error", State: provider.StateError})
	s.pr["rd"].set("rd-ffff", provider.Torrent{Hash: h("f"), Status: "downloaded", State: provider.StateDownloaded,
		Files: []provider.File{{ID: "1", Path: "Show/Show.S01E01.mkv", Bytes: 9000, Selected: true}}})
	pack, one := cand(h("e"), "Show.S01.2160p.WEB.H265-A"), cand(h("f"), "Show.S01E01.2160p.WEB.H265-B")
	pack.Episodes, one.Episodes = []int64{101, 102}, []int64{101}
	res, err = s.r.Route(s.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Source: "test",
		Candidates: []Candidate{pack, one}})
	if err != nil || res.Outcome != OutcomeAdded || res.Release != one.Release.Title {
		t.Fatalf("series route: %+v %v steps=%s", res, err, steps(res))
	}
	if n := s.count(`SELECT COUNT(*) FROM router_blocklist WHERE info_hash = ? AND researched_at IS NULL`, h("e")); n != 1 {
		t.Error("a failure only partly covered starts no re-search")
	}
}
