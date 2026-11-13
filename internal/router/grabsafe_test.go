package router

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/store"
)

// Grab safety: in-flight titles, queue specifications and provider writes.

// A movie gets no second release while another hash of the same title and version is in flight, from any
// automatic source (Radarr's QueueSpecification): an RSS pick and a search, research or offer queued before either
// made a download row would otherwise add two torrents and import twice.
func TestMovieSecondReleaseWhileFirstInFlight(t *testing.T) {
	t.Parallel()
	for _, src := range []string{"rss", "research", "offer", "job", "test", "acquire:999", "arrview:push:movie"} {
		t.Run(src, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			freshMovie(e)
			req := movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"))
			req.Source = src
			res1, err := e.r.Route(e.ctx, req)
			if err != nil || res1.Outcome != OutcomeAdded {
				t.Fatalf("first: %+v %v", res1, err)
			}
			req2 := movieReq(cand(h("b"), "Movie.2024.2160p.WEB.H265-OTHER"))
			req2.Source = src
			res2, err := e.r.Route(e.ctx, req2)
			if err != nil {
				t.Fatal(err)
			}
			if res2.Outcome != OutcomeInFlight || res2.DownloadID != res1.DownloadID ||
				!strings.Contains(steps(res2), "bbbb//skipped/in_flight") {
				t.Errorf("second: %+v: %s", res2, steps(res2))
			}
			if n := e.adds(); n != 1 {
				t.Errorf("%d adds for one movie, want 1", n)
			}
			if n := e.count(`SELECT COUNT(*) FROM download WHERE title_id = 1`); n != 1 {
				t.Errorf("%d download rows", n)
			}
			// The dry run (Plan) says the same, without a request.
			res3, err := e.r.Plan(e.ctx, req2)
			if err != nil || res3.Outcome != OutcomeInFlight {
				t.Errorf("plan: %+v %v: %s", res3, err, steps(res3))
			}
		})
	}
}

// A queued release that is still in hand (queued, waiting for a provider) blocks too, and a better release (an
// upgrade over the queued one by the version's profile) may still be added. A manual grab is never blocked.
func TestMovieQueuedReleaseUpgradeAndManual(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	freshMovie(e)
	low := cand(h("a"), "Movie.2024.1080p.WEB.H264-GRP")
	low.Quality = "WEBDL-1080p"
	res, err := e.r.Route(e.ctx, freshReq(low, "rss"))
	if err != nil || res.Outcome != OutcomeAdded {
		t.Fatalf("first: %+v %v", res, err)
	}
	// The same quality and score: not an upgrade.
	same := cand(h("b"), "Movie.2024.1080p.WEB.H264-OTHER")
	same.Quality = "WEBDL-1080p"
	if res, err := e.r.Route(e.ctx, freshReq(same, "rss")); err != nil || res.Outcome != OutcomeInFlight {
		t.Fatalf("same quality: %+v %v: %s", res, err, steps(res))
	}
	// Better quality, below the cutoff before: an upgrade over the queued release.
	better := cand(h("c"), "Movie.2024.2160p.WEB.H265-GRP")
	res, err = e.r.Route(e.ctx, freshReq(better, "rss"))
	if err != nil || res.Outcome != OutcomeAdded || res.Hash != h("c") {
		t.Fatalf("upgrade: %+v %v: %s", res, err, steps(res))
	}
	// Now a 2160p release is in flight and meets the cutoff: a higher score is no upgrade over it.
	more := cand(h("d"), "Movie.2024.2160p.WEB.H265-MORE")
	more.Score = 500
	if res, err := e.r.Route(e.ctx, freshReq(more, "research")); err != nil || res.Outcome != OutcomeInFlight {
		t.Fatalf("cutoff met: %+v %v: %s", res, err, steps(res))
	}
	// A person's grab is theirs to make.
	if res, err := e.r.Route(e.ctx, freshReq(more, "ui:grab:admin")); err != nil || res.Outcome != OutcomeAdded {
		t.Fatalf("manual: %+v %v: %s", res, err, steps(res))
	}
	if n := e.adds(); n != 3 {
		t.Errorf("adds %d, want 3", n)
	}
}

// A release of another version, or of another title, does not block.
func TestMovieInFlightIsPerVersion(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	freshMovie(e)
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (11, 1, 'other', 0, 0)`)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		score, state, created_at, updated_at) VALUES (50, 1, 11, 'other', ?, 'x', 'pub', 'WEBDL-2160p', 100, 'added', 0, 0)`,
		h("f"))
	if res, err := e.r.Route(e.ctx, freshReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"), "rss")); err != nil ||
		res.Outcome != OutcomeAdded {
		t.Fatalf("%+v %v: %s", res, err, steps(res))
	}
}

// A torrent id from an earlier download of ours that has left the account (zurg repair, another tool) is not
// reused blind: read first, not found, so a fresh add; nothing is blocklisted.
func TestStaleDownloadIDNotReused(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, created_at, updated_at) VALUES (90, 2, 20, 'series', ?, 'x', 'pub', 'rd', 'OLD',
		'imported', 0, 0)`, h("a"))
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || res.Reused || res.TorrentID != "rd-aaaa" || e.adds() != 1 {
		t.Fatalf("route: %+v: %s; adds %d", res, steps(res), e.adds())
	}
	e.pr["rd"].set("rd-aaaa", provider.Torrent{Hash: h("a"), Status: "downloading", State: provider.StateDownloading})
	for range 4 {
		e.clock = e.clock.Add(3 * time.Minute)
		if _, err := e.r.Poll(e.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
		t.Errorf("%d blocklist rows", n)
	}
}

// A registry row whose torrent is gone or holds another hash is marked absent and the hash added fresh; a live
// one with the hash is reused, after one read.
func TestStaleRegistryIDNotReused(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		setup func(e *env)
		reuse bool
	}{
		{"gone", func(e *env) {}, false},
		{"other hash", func(e *env) {
			e.pr["rd"].set("HELD", provider.Torrent{Hash: h("b"), Status: "downloaded", State: provider.StateDownloaded})
		}, false},
		{"held", func(e *env) {
			e.pr["rd"].set("HELD", provider.Torrent{Hash: h("a"), Status: "downloaded", State: provider.StateDownloaded})
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			e.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (7, ?, 'present', 0, 0)`, h("a"))
			e.exec(`INSERT INTO content_provider (content_id, provider, torrent_id, seen_at) VALUES (7, 'rd', 'HELD', 0)`)
			tc.setup(e)
			res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
			if err != nil {
				t.Fatal(err)
			}
			state := e.count(`SELECT COUNT(*) FROM content_provider WHERE torrent_id = 'HELD' AND state = 'present'`)
			if tc.reuse {
				if !res.Reused || res.TorrentID != "HELD" || e.adds() != 0 || state != 1 {
					t.Fatalf("%+v: %s; adds %d, present %d", res, steps(res), e.adds(), state)
				}
				return
			}
			if res.Reused || res.TorrentID != "rd-aaaa" || e.adds() != 1 || state != 0 {
				t.Fatalf("%+v: %s; adds %d, present %d", res, steps(res), e.adds(), state)
			}
		})
	}
}

// A reused torrent that leaves the account after the reuse: the route is queued again (a fresh add), not failed
// and blocklisted.
func TestReusedTorrentGoneRequeues(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	q := jobs.NewQueue(e.st)
	q.SetClock(func() time.Time { return e.clock })
	e.r.RegisterJobs(jobs.NewWorker(q, jobs.WorkerOptions{}), q)
	e.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (7, ?, 'present', 0, 0)`, h("a"))
	e.exec(`INSERT INTO content_provider (content_id, provider, torrent_id, seen_at) VALUES (7, 'rd', 'HELD', 0)`)
	e.pr["rd"].set("HELD", provider.Torrent{Hash: h("a"), Status: "downloading", State: provider.StateDownloading})
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || !res.Reused || res.TorrentID != "HELD" {
		t.Fatalf("reuse: %+v %v: %s", res, err, steps(res))
	}
	e.pr["rd"].mu.Lock()
	delete(e.pr["rd"].torrents, "HELD")
	e.pr["rd"].mu.Unlock()
	for range 4 {
		e.clock = e.clock.Add(3 * time.Minute)
		if _, err := e.r.Poll(e.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
		t.Errorf("%d blocklist rows", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'queued' AND provider IS NULL
		AND provider_torrent_id IS NULL AND reused = 0`, res.DownloadID); n != 1 {
		t.Fatalf("download not queued again: %v", e.count(`SELECT COUNT(*) FROM download WHERE state = 'failed'`))
	}
	var raw string
	if err := e.st.Reader().QueryRow(`SELECT payload FROM job WHERE kind = ? AND state = 'queued'`, JobRoute).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var p routePayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil || p.DownloadID != res.DownloadID || len(p.Candidates) != 1 ||
		p.Candidates[0].Release.InfoHash != h("a") || p.Source != "test" {
		t.Fatalf("payload %+v %v", p, err)
	}
	// The route runs again: the registry row is stale, so a fresh add on the same download.
	res2, err := e.r.Route(e.ctx, p.Request)
	if err != nil || res2.Reused || res2.DownloadID != res.DownloadID || e.adds() != 1 {
		t.Fatalf("again: %+v %v: %s", res2, err, steps(res2))
	}
}

// An uncached AllDebrid add of ours that stays there (alldebrid_delete_uncached off: uncached_kept) or
// whose ownership cannot be proven (uncached_kept_shared) ends AllDebrid for the rest of the route: a season of
// single-episode releases must not leave one untracked magnet per release on the shared account.
func TestAllDebridUncachedKeptOncePerRoute(t *testing.T) {
	t.Parallel()
	series := func() Request {
		title := "Show.S01E0%d.2160p.WEB-DL.DDP5.1-GRP" // RD's name rule refuses it: AD is tried first
		a, b := cand(h("a"), strings.Replace(title, "%d", "1", 1)), cand(h("b"), strings.Replace(title, "%d", "2", 1))
		a.Episodes, b.Episodes = []int64{101}, []int64{102}
		return Request{TitleID: 2, VersionID: 20, Version: "series", Candidates: []Candidate{a, b}, Source: "test"}
	}
	t.Run("kept", func(t *testing.T) {
		e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
		e.r.set.AllDebridDeleteUncached = false
		res, err := e.r.Route(e.ctx, series())
		if err != nil {
			t.Fatal(err)
		}
		if got := steps(res); got != "aaaa/rd/skipped/rd_name_rule; aaaa/ad/refused/uncached_kept; aaaa/tb/accepted/added; "+
			"bbbb/rd/skipped/rd_name_rule; bbbb/ad/skipped/provider_done; bbbb/tb/accepted/added; " {
			t.Errorf("steps %s", got)
		}
		if n := f.count("ad.test POST /v4.1/magnet/upload"); n != 1 {
			t.Errorf("%d AllDebrid uploads in one route, want 1", n)
		}
	})
	t.Run("not provably ours", func(t *testing.T) {
		e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
		e.r.set.AllDebridDeleteUncached = true
		f.adListFails = true
		res, err := e.r.Route(e.ctx, series())
		if err != nil {
			t.Fatal(err)
		}
		if got := steps(res); got != "aaaa/rd/skipped/rd_name_rule; aaaa/ad/accepted/uncached_kept_shared; "+
			"bbbb/rd/skipped/rd_name_rule; bbbb/ad/skipped/provider_done; bbbb/tb/accepted/added; " {
			t.Errorf("steps %s", got)
		}
		if n := f.count("ad.test POST /v4.1/magnet/upload"); n != 1 {
			t.Errorf("%d AllDebrid uploads in one route, want 1", n)
		}
	})
	t.Run("deletes on", func(t *testing.T) {
		// With the switch on, our own uncached add is deleted and AllDebrid stays usable for the next release.
		e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
		e.r.set.AllDebridDeleteUncached = true
		if _, err := e.r.Route(e.ctx, series()); err != nil {
			t.Fatal(err)
		}
		if n, d := f.count("ad.test POST /v4.1/magnet/upload"), f.count("ad.test POST /v4/magnet/delete"); n != 2 || d != 2 {
			t.Errorf("uploads %d, deletes %d", n, d)
		}
	})
}

// uncached_adds_per_hour caps Real-Debrid and AllDebrid too: once used, the provider is paced until the next
// clock hour and the route moves on (or waits) without another uncached add there.
func TestUncachedCapOnRealDebridAndAllDebrid(t *testing.T) {
	t.Parallel()
	for _, pk := range []string{"rd", "ad"} {
		t.Run(pk, func(t *testing.T) {
			e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
			e.r.set.AllDebridDeleteUncached = true
			for _, k := range []string{"rd", "ad"} {
				e.pol.Providers[k].Pacing.UncachedPerHr = 1
				e.pol.Providers[k].Pacing.Cooldown = 0
			}
			title := "Movie.2024.2160p.WEB.H265-GRP"
			if pk == "ad" {
				title = "Movie.2024.2160p.WEB-DL.DDP5.1-GRP" // RD's name rule refuses it
			}
			host := map[string]string{"rd": "rd.test POST /rest/1.0/torrents/addMagnet", "ad": "ad.test POST /v4.1/magnet/upload"}[pk]
			res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), title)))
			if err != nil || res.Provider != "tb" || !strings.Contains(steps(res), pk+"/refused/uncached_deleted") {
				t.Fatalf("first: %+v %v: %s", res, err, steps(res))
			}
			if n := f.count(host); n != 1 {
				t.Fatalf("%d adds on %s", n, pk)
			}
			res, err = e.r.Route(e.ctx, grabReq(cand(h("b"), title)))
			if err != nil || !strings.Contains(steps(res), pk+"/deferred/paced") {
				t.Fatalf("second: %+v %v: %s", res, err, steps(res))
			}
			if n := f.count(host); n != 1 {
				t.Errorf("%d adds on %s with uncached_adds_per_hour 1 used", n, pk)
			}
			p, _ := loadPace(e.ctx, e.st.Reader(), pk)
			if until, why := p.blocked(e.clock, e.pol.Providers[pk].Pacing); !strings.Contains(why, "uncached_adds_per_hour 1 used") ||
				!until.Equal(e.clock.Truncate(time.Hour).Add(time.Hour)) {
				t.Errorf("pacing %+v: %v %q", p, until, why)
			}
		})
	}
}

// A crash in the middle of a Real-Debrid add. Reconciliation finds the torrent on the
// account, created after the add was sent, the only entry with the hash, known to no other row. Timing and uniqueness
// do not prove our request created it (another client may have added the hash while ours never landed), so own_add
// stays unset and the stalled cleanup never deletes it: it stays on the account. No other download holds it, so it is
// adopted as our add (reused 0) for the file selection.
func TestCrashedAddTorrentNeverDeletedAsStalled(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	sent := e.clock
	ctx, cancel := context.WithCancel(e.ctx)
	e.ad["rd"].add = func(m adder.Magnet, _ adder.AddOptions) (*adder.Added, error) {
		e.landOnAccount(m)
		e.pr["rd"].list[0].Added = sent.Add(time.Second)
		cancel() // the crash
		return nil, context.Canceled
	}
	if _, err := e.r.Route(ctx, movieReq(cand(h("a"), "A.2160p"))); err == nil {
		t.Fatal("an interrupted add reported success")
	}
	e.ad["rd"].add = nil
	e.clock = e.clock.Add(2 * time.Minute)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM download WHERE info_hash = ? AND version_id = 10`, h("a")).
		Scan(&id); err != nil {
		t.Fatal(err)
	}
	state, prov, tid, reused, own := e.download(id)
	if state != StateAdded || prov != "rd" || tid != "rd-aaaa" || reused != 0 || own != nil {
		t.Fatalf("download %s %s %s reused %d own_add %v, want an adopted add, own_add unset", state, prov, tid,
			reused, own)
	}
	e.pr["rd"].set("rd-aaaa", provider.Torrent{Hash: h("a"), Status: "magnet_conversion", State: provider.StateQueued})
	e.clock = e.clock.Add(11 * time.Minute)
	if sum, err := e.r.Poll(e.ctx); err != nil || sum.Stalled != 1 {
		t.Fatalf("stalled poll %+v %v", sum, err)
	}
	var msg string
	_ = e.st.Reader().QueryRow(`SELECT error FROM download WHERE id = ?`, id).Scan(&msg)
	if !strings.Contains(msg, "kept on rd: the add was not provably a new entry of ours") {
		t.Errorf("stalled: %q, want the shared torrent kept", msg)
	}
	if e.adds() != 1 || e.deletes() != 0 {
		t.Errorf("adds %d deletes %d", e.adds(), e.deletes())
	}
}

// TestDeletedVersionDownloadsEnd: downloads in flight for a version deleted from settings are
// ended in Theatarr's records on the next poll: failed with the reason, nothing blocklisted, nothing sent to or deleted on
// any provider. Unsettled adds, imports and downloads a live import claim holds are left alone; downloads of a version
// still in settings poll as before.
func TestDeletedVersionDownloadsEnd(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (11, 1, 'gone', 0, 0)`)
	ins := func(id int64, key, state, tid string) {
		var pk, ptid any
		if tid != "" {
			pk, ptid = "rd", tid
		}
		vid := 11
		if key == "movie" {
			vid = 10
		}
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
			provider_torrent_id, state, own_add, ready_at, added_at, created_at, updated_at)
			VALUES (?, 1, ?, ?, ?, 'Movie', 'pub', ?, ?, ?, 1, 0, 0, 0, 0)`,
			id, vid, key, h(fmt.Sprint(id)), pk, ptid, state)
	}
	ins(1, "gone", StateQueued, "")
	ins(2, "gone", StateAdded, "RD2")
	ins(3, "gone", StateDownloading, "RD3")
	ins(4, "gone", StateReady, "RD4")
	ins(5, "gone", StateReady, "RD5")
	e.exec(`INSERT INTO library_claim (download_id, owner, until) VALUES (5, 'importer', ?)`,
		store.Millis(e.clock.Add(time.Hour)))
	ins(6, "gone", StateImporting, "RD6")
	ins(7, "movie", StateDownloading, "RD7")
	for _, id := range []string{"RD2", "RD3", "RD7"} {
		e.pr["rd"].set(id, provider.Torrent{State: provider.StateDownloading, Status: "downloading", Progress: 10})
	}
	sum, err := e.r.Poll(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	rows, err := e.st.Writer().QueryContext(e.ctx, `SELECT id, state, COALESCE(error, '') FROM download ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		var state, why string
		if err := rows.Scan(&id, &state, &why); err != nil {
			t.Fatal(err)
		}
		if state == StateFailed && !strings.Contains(why, `version "gone" is no longer in settings`) {
			t.Errorf("download %d failed with %q", id, why)
		}
		got = append(got, fmt.Sprintf("%d:%s", id, state))
	}
	rows.Close()
	if want := "1:failed 2:failed 3:failed 4:failed 5:ready 6:importing 7:downloading"; strings.Join(got, " ") != want {
		t.Fatalf("states %s, want %s (summary %+v)", strings.Join(got, " "), want, sum)
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
		t.Fatalf("%d releases blocklisted", n)
	}
	if e.writes() != 0 {
		t.Fatalf("%d provider writes", e.writes())
	}
}

// TestLastVersionRemovedEndsDownloads: with no versions left in settings every version-bound
// download is a deleted version's. The empty version map used to skip the clean-up, so the next poll read the
// provider torrent and could mark it ready.
func TestLastVersionRemovedEndsDownloads(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, own_add, ready_at, added_at, created_at, updated_at)
		VALUES (1, 1, 10, 'movie', ?, 'Movie', 'pub', 'rd', 'RD1', 'downloading', 1, 0, 0, 0, 0)`, h("1"))
	e.pr["rd"].set("RD1", provider.Torrent{State: provider.StateDownloaded, Status: "downloaded", Progress: 100})
	e.r.pol.Versions = nil
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	var state, why string
	_ = e.st.Reader().QueryRow(`SELECT state, COALESCE(error, '') FROM download WHERE id = 1`).Scan(&state, &why)
	if state != StateFailed || !strings.Contains(why, `version "movie" is no longer in settings`) {
		t.Fatalf("download %s %q, want failed as a deleted version's", state, why)
	}
	if e.writes() != 0 {
		t.Fatalf("%d provider writes", e.writes())
	}
}
