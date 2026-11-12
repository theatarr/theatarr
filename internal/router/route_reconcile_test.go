package router

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
)

// Preparing the next generation (New with Previous), wiring its mount check and publishing provider changes
// never wait for a running route. A save turning Pause on while a route is mid-send is published at once and the
// route sends nothing more.
func TestPausePublishedDuringRunningRoute(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
	var published atomic.Pointer[Router]
	published.Store(e.r)
	e.r.SetCurrentRuntime(func() *Router { return published.Load() })

	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	e.ad["rd"].observe = func(context.Context) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
	}
	done := make(chan *Result, 1)
	go func() {
		res, err := e.r.Route(e.ctx, seriesReq(episodeCandidate("a", 101), episodeCandidate("b", 102),
			episodeCandidate("c", 103)))
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	<-started

	pol := testPolicy(false, policy.ModeActive)
	prepared := make(chan *Router, 1)
	go func() {
		cur, err := New(Config{Store: e.st, Policy: pol, Previous: e.r,
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return e.clock }})
		if err != nil {
			t.Error(err)
			close(prepared)
			return
		}
		cur.SetMountCheck(func(context.Context) error { return nil })
		cur.PublishProviderChanges([]string{"rd"})
		published.Store(cur)
		prepared <- cur
	}()
	select {
	case <-prepared:
	case <-time.After(5 * time.Second):
		close(release)
		<-done
		t.Fatal("the next runtime was not prepared while a route ran: New waits for the route lock")
	}
	close(release)
	res := <-done
	if n := calls.Load(); n != 1 {
		t.Errorf("Pause turned on mid-route, but the route sent %d adds (steps %s)", n, steps(res))
	}
}

// Real-Debrid took our add but the answer was lost (a 502). The continuation adopts the entry from the account as
// an add of ours (no other download holds it): the poller selects its files, it downloads, nothing is blocklisted and
// nothing is deleted (own_add stays unset).
func TestReconciledAddGetsFilesSelected(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	var tid string
	e.ad["rd"].add = func(m adder.Magnet, _ adder.AddOptions) (*adder.Added, error) {
		e.landOnAccount(m)
		tid = "rd-" + m.Hash()[:4]
		e.pr["rd"].set(tid, provider.Torrent{Hash: m.Hash(), State: provider.StateQueued,
			Status: rdWaitingFiles, Files: []provider.File{{ID: "1", Path: "Movie.mkv", Bytes: 10 << 30}}})
		return nil, &adder.Error{Provider: "rd", Code: adder.CodeUnavailable, Status: 502}
	}
	e.ad["rd"].onSelect = func(context.Context) error {
		e.pr["rd"].set(tid, provider.Torrent{ID: tid, Hash: h("a"), State: provider.StateDownloading,
			Status: "downloading", Progress: 10, Files: []provider.File{{ID: "1", Path: "Movie.mkv", Bytes: 10 << 30}}})
		return nil
	}
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	e.ad["rd"].add = nil
	e.clock = e.clock.Add(addSettle + time.Second)
	next := routeJobs(e, res.DownloadID)
	if len(next) != 1 {
		t.Fatalf("continuation %+v", next)
	}
	e.exec(`UPDATE job SET state = 'done' WHERE kind = ?`, JobRoute)
	if _, err := e.r.Route(e.ctx, next[0].Request); err != nil {
		t.Fatal(err)
	}
	for range 12 {
		if _, err := e.r.Poll(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.clock = e.clock.Add(time.Minute)
	}
	state, _, _, reused, own := e.download(res.DownloadID)
	if reused != 0 || own != nil {
		t.Errorf("adopted add: reused %d own_add %v, want 0 and NULL", reused, own)
	}
	if len(e.ad["rd"].selects) != 1 {
		t.Errorf("selections %v, want one", e.ad["rd"].selects)
	}
	if state != StateDownloading {
		t.Errorf("state %s, want downloading", state)
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist WHERE info_hash = ?`, h("a")); n != 0 {
		t.Errorf("%d blocklist rows", n)
	}
	if len(e.ad["rd"].deletes) != 0 {
		t.Errorf("deleted %v on the reconcile path", e.ad["rd"].deletes)
	}
}

// manualCand is cand as arrview.AddRelease hands it over: a release a person added by magnet or hash.
func manualCand(hash, title string) Candidate {
	c := cand(hash, title)
	c.Release.Indexer, c.Release.Kind, c.Release.Privacy, c.Release.Size = policy.ManualIndexer, "", "", 0
	return c
}

// A person adds a hash another tool on the account already holds, waiting for that tool's file selection, and
// Real-Debrid's answer to the add is lost (a 502). The entry the account shows is adopted as reused: no files are
// selected on it and nothing is deleted, also when it then stalls. (TestReconciledAddGetsFilesSelected is the same for a searched release.)
func TestReconciledManualAddIsReused(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	const tid = "rd-foreign"
	foreign := provider.Torrent{ID: tid, Hash: h("a"), State: provider.StateQueued, Status: rdWaitingFiles,
		Files: []provider.File{{ID: "1", Path: "Movie.mkv", Bytes: 10 << 30}}}
	e.ad["rd"].add = func(m adder.Magnet, _ adder.AddOptions) (*adder.Added, error) {
		// The other tool's torrent, as the account lists it by the time the add is settled.
		p := e.pr["rd"]
		p.mu.Lock()
		p.list = append(p.list, provider.Torrent{ID: tid, Hash: m.Hash(), Status: rdWaitingFiles})
		p.mu.Unlock()
		p.set(tid, foreign)
		return nil, &adder.Error{Provider: "rd", Code: adder.CodeUnavailable, Status: 502}
	}
	res, err := e.r.Route(e.ctx, grabReq(manualCand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	e.ad["rd"].add = nil
	e.clock = e.clock.Add(addSettle + time.Second)
	next := routeJobs(e, res.DownloadID)
	if len(next) != 1 {
		t.Fatalf("continuation %+v", next)
	}
	e.exec(`UPDATE job SET state = 'done' WHERE kind = ?`, JobRoute)
	if _, err := e.r.Route(e.ctx, next[0].Request); err != nil {
		t.Fatal(err)
	}
	for range 90 {
		if _, err := e.r.Poll(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.clock = e.clock.Add(time.Minute)
	}
	state, _, got, reused, own := e.download(res.DownloadID)
	// Nobody selects its files, so it stalls and fails; the other tool's torrent stays as it was.
	if state != StateFailed {
		t.Errorf("state %s, want failed (stalled, kept)", state)
	}
	if got != tid || reused != 1 || own != nil {
		t.Errorf("adopted torrent %q reused %d own_add %v, want %s reused and own_add NULL", got, reused, own, tid)
	}
	if len(e.ad["rd"].selects) != 0 {
		t.Errorf("selected files on another tool's torrent: %v", e.ad["rd"].selects)
	}
	if len(e.ad["rd"].deletes) != 0 {
		t.Errorf("deleted %v", e.ad["rd"].deletes)
	}
}

// A person adds a hash whose stranded queued row came from a search. The add takes the row over as a manual add
// before anything is added, so when Real-Debrid's answer is lost and the account shows the hash on another tool's
// torrent, it is adopted as reused: no files are selected on it and nothing is deleted.
func TestManualAddTakesOverSearchedRowIsReused(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	id := strandQueued(e, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	e.clock = e.clock.Add(5 * time.Minute)
	const tid = "rd-foreign"
	foreign := provider.Torrent{ID: tid, Hash: h("a"), State: provider.StateQueued, Status: rdWaitingFiles,
		Files: []provider.File{{ID: "1", Path: "Movie.mkv", Bytes: 10 << 30}}}
	e.ad["rd"].add = func(m adder.Magnet, _ adder.AddOptions) (*adder.Added, error) {
		p := e.pr["rd"]
		p.mu.Lock()
		p.list = append(p.list, provider.Torrent{ID: tid, Hash: m.Hash(), Status: rdWaitingFiles})
		p.mu.Unlock()
		p.set(tid, foreign)
		return nil, &adder.Error{Provider: "rd", Code: adder.CodeUnavailable, Status: 502}
	}
	res, err := e.r.Route(e.ctx, grabReq(manualCand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.DownloadID != id {
		t.Fatalf("%+v %v", res, err)
	}
	if ix, _, source, _ := provenance(e, id); !policy.IsManualAdd(ix) || source != "ui:grab:test" {
		t.Errorf("row after the takeover: indexer %q source %q", ix, source)
	}
	e.ad["rd"].add = nil
	e.clock = e.clock.Add(addSettle + time.Second)
	next := routeJobs(e, id)
	if len(next) != 1 {
		t.Fatalf("continuation %+v", next)
	}
	e.exec(`UPDATE job SET state = 'done' WHERE kind = ?`, JobRoute)
	if _, err := e.r.Route(e.ctx, next[0].Request); err != nil {
		t.Fatal(err)
	}
	for range 90 {
		if _, err := e.r.Poll(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.clock = e.clock.Add(time.Minute)
	}
	_, _, got, reused, own := e.download(id)
	if got != tid || reused != 1 || own != nil {
		t.Errorf("adopted torrent %q reused %d own_add %v, want %s reused and own_add NULL", got, reused, own, tid)
	}
	if len(e.ad["rd"].selects) != 0 {
		t.Errorf("selected files on another tool's torrent: %v", e.ad["rd"].selects)
	}
	if len(e.ad["rd"].deletes) != 0 {
		t.Errorf("deleted %v", e.ad["rd"].deletes)
	}
}

// An interrupted add whose hash another download of ours holds is settled as a reuse, as at the add: no
// selection of ours on the shared torrent.
func TestReconciledAddHeldByAnotherDownloadIsReuse(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	ctx, cancel := context.WithCancel(e.ctx)
	e.ad["rd"].add = func(m adder.Magnet, _ adder.AddOptions) (*adder.Added, error) {
		e.landOnAccount(m)
		cancel() // the crash
		return nil, context.Canceled
	}
	req := movieReq(cand(h("a"), "A.2160p"))
	if _, err := e.r.Route(ctx, req); err == nil {
		t.Fatal("an interrupted add reported success")
	}
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, added_at, created_at, updated_at)
		VALUES (99, 1, 10, 'movie', ?, 'Movie', 'pub', 'rd', 'rd-aaaa', 'downloading', 0, 0, 0)`, h("a"))
	e.ad["rd"].add = nil
	res, err := e.r.Route(e.ctx, req)
	if err != nil || !strings.Contains(steps(res), "rd/accepted/reconciled") {
		t.Fatalf("%+v %v", res, err)
	}
	if _, _, tid, reused, own := e.download(res.DownloadID); tid != "rd-aaaa" || reused != 1 || own != nil {
		t.Errorf("download %s reused %d own_add %v, want a reuse", tid, reused, own)
	}
}

// A listing that fails at once (a refused key) is recorded with its own error, not as a listing cut short by
// its share of the recovery pass.
func TestAccountListErrorIsNotACut(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	want := &provider.APIError{Provider: "rd", Status: 401, Code: "bad_token", Message: "bad token"}
	e.pr["rd"].listErr = want
	lists := &accountLists{m: map[string]accountList{}, slice: time.Minute}
	_, err := e.r.accountList(e.ctx, e.pr["rd"], "rd", lists)
	if errors.Is(err, errListingCut) || !errors.Is(err, want) {
		t.Fatalf("listing error %v, want the provider's 401", err)
	}
	if l := lists.m["rd"]; !errors.Is(l.err, want) {
		t.Errorf("recorded %v", l.err)
	}
}

// LM1: a release push (arrview:push:<view>) that waited for a paced provider is checked again when it resumes, as an
// RSS route is: the version got an equal file meanwhile, so it is cancelled and nothing is added.
func TestDeferredPushRechecksFreshness(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"rss", "arrview:push:radarr"} {
		t.Run(source, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			freshMovie(e)
			for _, k := range []string{"rd", "ad", "tb"} {
				e.exec(`INSERT INTO router_pacing (provider, backoff_until, failures, reason, updated_at)
					VALUES (?, ?, 1, 'x', 0)`, k, e.clock.Add(time.Minute).UnixMilli())
			}
			c := cand(h("a"), "Movie.2024.2160p.WEB-DL-GRP")
			c.Score = 100
			req := freshReq(c, source)
			first, err := e.r.Route(e.ctx, req)
			if err != nil || first.Outcome != OutcomeDeferred {
				t.Fatalf("first route %+v: %v", first, err)
			}
			e.link(1, 10, 0, "WEBDL-2160p", 100, 1000)
			e.clock = e.clock.Add(2 * time.Minute)
			req.DownloadID = first.DownloadID
			res, err := e.r.Route(e.ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != OutcomeCancelled || e.writes() != 0 {
				t.Fatalf("resumed %s route: outcome %s, writes %d: %s", source, res.Outcome, e.writes(), steps(res))
			}
		})
	}
}

// A crash between our uncached delete and its record leaves the download added, journaled as deleting
// (noteDeleting). The poller that finds the torrent gone fails it without blocklisting the release: our own delete
// took it, not the provider.
func TestCrashAfterUncachedDeleteNotBlocklisted(t *testing.T) {
	t.Parallel()
	for _, journaled := range []bool{true, false} {
		t.Run(map[bool]string{true: "journaled", false: "plain"}[journaled], func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			withQueue(e)
			why := any(nil)
			if journaled {
				why = deletingPrefix + "RD5 on rd"
			}
			e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
				provider, provider_torrent_id, state, added_at, error, created_at, updated_at)
				VALUES (5, 1, 10, 'movie', ?, 'Movie', 'pub', 'rd', 'RD5', 'added', ?, ?, 0, 0)`,
				h("a"), e.clock.UnixMilli(), why)
			for range 3 {
				if _, err := e.r.Poll(e.ctx); err != nil {
					t.Fatal(err)
				}
				e.clock = e.clock.Add(3 * time.Minute)
			}
			if n := e.count(`SELECT COUNT(*) FROM download WHERE id = 5 AND state = 'failed'`); n != 1 {
				t.Fatalf("download not failed")
			}
			bl := e.count(`SELECT COUNT(*) FROM router_blocklist WHERE download_id = 5`)
			if journaled && bl != 0 || !journaled && bl != 1 {
				t.Errorf("journaled %v: %d blocklist rows", journaled, bl)
			}
		})
	}
}

// Same hash in two versions: while another version's add of the hash to a provider is unsettled (adding), a route
// does not add it there again; it waits for that add to settle.
func TestUnsettledAddOfSameHashDefers(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (11, 1, 'other', 0, 0)`)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		state, add_attempt, created_at, updated_at) VALUES (7, 1, 11, 'other', ?, 'Movie', 'pub', 'rd', 'adding', 'x',
		0, ?)`, h("a"), e.clock.UnixMilli())
	e.pol.Versions["movie"].Providers = []string{"rd"}
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if e.writes() != 0 || res.Outcome != OutcomeDeferred || !strings.Contains(steps(res), "rd/deferred/add_unsettled") {
		t.Fatalf("writes %d, %+v: %s", e.writes(), res.Outcome, steps(res))
	}
}

// A deferred route that does not record a continuation leaves no retry slot behind: the next route deferred on the
// provider is not pushed back by a slot nobody uses.
func TestDeferSlotTakenOnlyWhenUsed(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	now := e.clock
	at := now.Add(time.Minute)
	first, _ := e.r.deferSlot("rd", at, now) // computed, never taken (the route gave up)
	second, take := e.r.deferSlot("rd", at, now)
	if !first.Equal(at) || !second.Equal(at) {
		t.Fatalf("slots %v %v, want both %v", first, second, at)
	}
	take()
	if third, _ := e.r.deferSlot("rd", at, now); !third.After(at) {
		t.Errorf("a taken slot does not stagger the next: %v", third)
	}
}
