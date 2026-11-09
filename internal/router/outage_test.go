package router

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/store"
)

// TestTitleAll451DoesNotHoldAccount: Real-Debrid refusing every release of one title with 451
// used to hold the whole account (5, 15, 60 minutes) as the route walked its candidates. 451s on releases of one
// title are verdicts on those hashes; after title451Limit of them the title is skipped on RD, without more adds.
func TestTitleAll451DoesNotHoldAccount(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["movie"].Providers = []string{"rd"}
	e.ad["rd"].add = func(m adder.Magnet, _ adder.AddOptions) (*adder.Added, error) {
		return nil, &adder.Error{Provider: "rd", Code: adder.CodeAccountHold, Status: 451}
	}
	req := grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"),
		cand(h("b"), "Movie.2024.2160p.WEB.H265-OTH"), cand(h("c"), "Movie.2024.1080p.WEB.H265-GRP"),
		cand(h("d"), "Movie.2024.1080p.WEB.H265-OTH"), cand(h("e"), "Movie.2024.720p.WEB.H265-GRP"))
	// The route waits out RD's min_gap between adds, as its job would.
	var res *Result
	var err error
	for range 10 {
		if res, err = e.r.Route(e.ctx, req); err != nil {
			t.Fatal(err)
		}
		if res.Outcome != OutcomeDeferred {
			break
		}
		if res.RetryAt.Sub(e.clock) > time.Minute {
			t.Fatalf("deferred to %v: %s", res.RetryAt, steps(res))
		}
		e.clock = res.RetryAt
		req.DownloadID = res.DownloadID
	}
	if res.Outcome == OutcomeDeferred || res.Outcome == OutcomeAdded {
		t.Fatalf("outcome %+v %s", res, steps(res))
	}
	p, _ := loadPace(e.ctx, e.st.Reader(), "rd")
	if p.HoldLevel != 0 || !p.HoldUntil.IsZero() || strings.Contains(p.Reason, "451") {
		t.Fatalf("one title's 451s held the account: %+v", p)
	}
	if got := len(e.ad["rd"].adds); got != title451Limit {
		t.Fatalf("%d adds sent, want %d (then the title is skipped on rd): %s", got, title451Limit, steps(res))
	}
	if !strings.Contains(fullSteps(res), "refused 3 releases of this title") {
		t.Errorf("the skip does not say why: %s", fullSteps(res))
	}
	// Another title's release still goes to RD at once, and its 451 does hold the account (a different title).
	e.ad["rd"].add = func(m adder.Magnet, _ adder.AddOptions) (*adder.Added, error) {
		return &adder.Added{ID: "rd-" + m.Hash()[:4], Hash: m.Hash()}, nil
	}
	e.clock = e.clock.Add(11 * time.Minute) // past the test pacing's burst cooldown
	other := grabReq(cand(h("f"), "Other.2024.2160p.WEB.H265-GRP"))
	other.TitleID = 2
	if res, err = e.r.Route(e.ctx, other); err != nil || res.Outcome != OutcomeAdded {
		t.Fatalf("another title: %+v %v %s", res, err, steps(res))
	}
}

// TestHeld451DoesNotBlockHash: a 451 judged an account hold used to cache its hash as a miss for
// miss_ttl, so the resumed route skipped that release on RD for six hours. Now the hold explains it, and the earlier
// 451 in the window that the hold also explains loses its miss.
func TestHeld451DoesNotBlockHash(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["movie"].Providers = []string{"rd"}
	e.pol.Providers["rd"].Pacing = policy.Pacing{}
	e.ad["rd"].add = refuse(adder.CodeAccountHold)
	// Title 2's release got a 451 a minute ago (a per-hash verdict then).
	e.exec(`INSERT INTO router_refusal_451 (provider, info_hash, refused_at, title_id) VALUES ('rd', ?, ?, 2)`, h("z"),
		store.Millis(e.clock.Add(-time.Minute)))
	e.exec(`INSERT INTO provider_cache (info_hash, provider, state, source, checked_at, expires_at)
		VALUES (?, 'rd', 'miss', 'router:451', 0, ?)`, h("z"), store.Millis(e.clock.Add(time.Hour)))
	res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.Outcome != OutcomeDeferred {
		t.Fatalf("%+v %v %s", res, err, steps(res))
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE provider = 'rd' AND state = 'miss'`); n != 0 {
		t.Fatalf("%d hashes blocked on rd by 451s the account hold explains", n)
	}
	// After the hold the same release is added.
	e.clock = res.RetryAt.Add(time.Second)
	e.ad["rd"].add = func(m adder.Magnet, _ adder.AddOptions) (*adder.Added, error) {
		return &adder.Added{ID: "rd-" + m.Hash()[:4], Hash: m.Hash()}, nil
	}
	req := grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"))
	req.DownloadID = res.DownloadID
	if res, err = e.r.Route(e.ctx, req); err != nil || res.Outcome != OutcomeAdded || res.Hash != h("a") {
		t.Fatalf("after the hold: %+v %v %s", res, err, steps(res))
	}
	// The held 451s do not count against their titles.
	if n, _ := title451s(e.ctx, e.st.Reader(), "rd", 2, e.clock.Add(-time.Hour)); n != 0 {
		t.Errorf("title 2 counts %d 451s the hold explained", n)
	}
}

func fullSteps(res *Result) string {
	var b strings.Builder
	for _, s := range res.Steps {
		b.WriteString(s.Code + ": " + s.Message + "\n")
	}
	return b.String()
}

// TestProviderOutageEndsUnavailable: a provider outage of about 16 minutes
// must not fail, blocklist or orphan a download added just before it. The stall clock runs on through the
// outage (zurg-style), and a download that reaches stalled_after while its
// provider is down ends as provider unavailable: not blocklisted, routed again, its provider's adds backed off, and
// nothing deleted (the torrent was not seen).
func TestProviderOutageEndsUnavailable(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	stall := e.r.stallAfter("rd")
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, own_add, added_at, created_at, updated_at) VALUES (1, 1, 10, 'movie', ?, 'Movie',
		'pub', 'rd', 't1', 'added', 1, ?, 0, 0)`, h("a"), store.Millis(e.clock))
	e.pr["rd"].set("t1", provider.Torrent{Hash: h("a"), State: provider.StateQueued, Status: "magnet_conversion"})
	added := e.clock
	e.clock = e.clock.Add(30 * time.Second)
	e.pr["rd"].readErr = map[string]error{"t1": errors.New("rd: connection refused")}
	for e.clock.Before(added.Add(stall - time.Minute)) {
		if _, err := e.r.Poll(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.clock = e.clock.Add(time.Minute)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = 1 AND state = 'added'`); n != 1 {
		t.Fatal("the outage ended the download before stalled_after")
	}
	for e.clock.Before(added.Add(stall + 10*time.Minute)) {
		if _, err := e.r.Poll(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.clock = e.clock.Add(time.Minute)
	}
	var state, why string
	_ = e.st.Reader().QueryRow(`SELECT state, COALESCE(error, '') FROM download WHERE id = 1`).Scan(&state, &why)
	if state != "queued" || !strings.HasPrefix(why, "provider unavailable") {
		t.Fatalf("after stalled_after of outage: %s %q, want queued as provider unavailable", state, why)
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
		t.Fatalf("%d blocklisted for a provider outage", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = ? AND state = 'queued'`, JobRoute); n != 1 {
		t.Fatalf("%d route jobs queued, want 1", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM router_pacing WHERE provider = 'rd' AND backoff_until > ?`,
		store.Millis(e.clock.Add(-10*time.Minute))); n != 1 {
		t.Fatal("rd's adds were not backed off")
	}
	if len(e.ad["rd"].deletes) != 0 {
		t.Fatalf("deleted %v without seeing the torrent", e.ad["rd"].deletes)
	}
}

// TestPollCutKeepsFailedStreak: a read cut by the pass deadline used to clear the failed-read
// streak, so a torrent whose reads alternate between hanging and failing never reached a blind stall.
func TestPollCutKeepsFailedStreak(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	old := pollPassLimit
	pollPassLimit = 50 * time.Millisecond
	t.Cleanup(func() { pollPassLimit = old })
	since := e.clock.Add(-5 * time.Minute)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, own_add, added_at, created_at, updated_at, poll_errors, poll_error_since)
		VALUES (1, 1, 10, 'movie', ?, 'Movie', 'pub', 'rd', 't1', 'added', 1, ?, 0, 0, 2, ?)`, h("a"),
		store.Millis(e.clock), store.Millis(since))
	e.pr["rd"].set("t1", provider.Torrent{Hash: h("a"), State: provider.StateQueued, Status: "magnet_conversion"})
	e.pr["rd"].read = func(ctx context.Context, _ string) { <-ctx.Done() }
	e.pr["rd"].readErr = map[string]error{"t1": context.DeadlineExceeded}
	e.clock = e.clock.Add(time.Hour) // past the read's back-off
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	var errs, at int64
	if err := e.st.Reader().QueryRow(`SELECT poll_errors, COALESCE(poll_error_since, 0) FROM download WHERE id = 1`).
		Scan(&errs, &at); err != nil {
		t.Fatal(err)
	}
	if errs != 2 || at != store.Millis(since) {
		t.Fatalf("after a cut read: %d failed reads since %d, want 2 since %d", errs, at, store.Millis(since))
	}
}

// Unlike a per-hash treatment, an unrecognised envelope and a bare 4xx both back off the provider.
func TestUnrecognisedRefusalBacksProviderOff(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		status  int
		code    string
		refused bool
	}{
		{"unknown envelope", 200, "MAGNET_SOMETHING_NEW", true},
		{"bare bad request", 400, "", false},
		{"bare not found", 404, "", false},
		{"server error", 502, "NEW", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			e.pol.Versions["movie"].Providers = []string{"ad"}
			e.ad["ad"].add = func(adder.Magnet, adder.AddOptions) (*adder.Added, error) {
				return nil, &adder.Error{Provider: "ad", Code: adder.CodeUnknown, Status: tc.status, ProviderCode: tc.code, Refused: tc.refused}
			}
			res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"), cand(h("b"), "Movie.2024.1080p.WEB.H265-GRP")))
			if err != nil || res.Outcome != OutcomeDeferred {
				t.Fatalf("route %+v %v", res, err)
			}
			p, _ := loadPace(e.ctx, e.st.Reader(), "ad")
			if p.Failures == 0 || !p.BackoffUntil.After(e.clock) {
				t.Fatalf("provider not backed off: %+v", p)
			}
			if e.count(`SELECT COUNT(*) FROM provider_cache WHERE state='miss'`) != 0 {
				t.Fatal("unknown refusal poisoned a hash")
			}
			if len(e.ad["ad"].adds) != 1 {
				t.Fatal("kept adding after unknown refusal")
			}
		})
	}
}

// TestPollRecoveryIsBounded: poll recovery listed the provider's account once per interrupted
// add, under the router lock, with no deadline, so a slow provider held every route up for minutes. Now the account is
// listed once per pass, without the lock, within reconcilePassLimit, and a pass checks at most reconcileBatch adds.
func TestPollRecoveryIsBounded(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
	for i := range reconcileBatch + 5 {
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
			provider, created_at, updated_at) VALUES (?, 1, 10, 'movie', ?, 'R', 'pub', 'adding', 'rd', 0, ?)`,
			i+1, h(string(rune('a'+i%26)))+string(rune('0'+i/26)), e.clock.Add(-addSettle-time.Minute).UnixMilli())
	}
	var deadline, locked bool
	e.pr["rd"].onList = func(ctx context.Context) {
		_, deadline = ctx.Deadline()
		if e.r.mu.TryLock() {
			e.r.mu.Unlock()
		} else {
			locked = true
		}
	}
	sum, err := e.r.Poll(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if e.pr["rd"].listReads != 1 || !deadline || locked {
		t.Fatalf("account listed %d times, deadline %v, under the router lock %v", e.pr["rd"].listReads, deadline,
			locked)
	}
	if sum.Reconciled != reconcileBatch {
		t.Fatalf("settled %d, want a batch of %d", sum.Reconciled, reconcileBatch)
	}
	// The rest are checked next pass, before the ones checked already.
	if sum, err = e.r.Poll(e.ctx); err != nil || sum.Reconciled != 5 {
		t.Fatalf("second pass %+v %v", sum, err)
	}
	// A listing that hangs is cut at the deadline, and nothing is settled from it.
	old := reconcilePassLimit
	reconcilePassLimit = 50 * time.Millisecond
	t.Cleanup(func() { reconcilePassLimit = old })
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
		provider, created_at, updated_at) VALUES (100, 1, 10, 'movie', ?, 'R', 'pub', 'adding', 'rd', 0, ?)`,
		h("z"), e.clock.Add(-addSettle-time.Minute).UnixMilli())
	e.pr["rd"].onList = func(ctx context.Context) { <-ctx.Done() }
	e.pr["rd"].listErr = context.DeadlineExceeded
	if sum, err = e.r.Poll(e.ctx); err != nil || sum.Reconciled != 0 {
		t.Fatalf("hung listing: %+v %v", sum, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = 100 AND state = 'adding' AND error IS NULL`); n != 1 {
		t.Fatal("a cut lookup changed the download")
	}
}

// TestFreshnessRescoresCurrentFile: the router's final upgrade check compared a candidate with
// the score stored at import (link.score), while the decision engine rescores the current file under the running
// profile. After a profile change the two disagreed: the decision accepted an upgrade and the router cancelled it as
// no_longer_upgrade. The router rescores the current file now.
func TestFreshnessRescoresCurrentFile(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	freshMovie(e)
	engPol := &policy.Policy{Profiles: map[string]*policy.Profile{"fresh": {
		Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}, {Name: "WEBDL-1080p"}}, Cutoff: "WEBDL-2160p",
		CutoffScore: 10000}}, Indexers: e.pol.Indexers,
		Versions: map[string]*policy.VersionPolicy{"movie": e.pol.Versions["movie"]}}
	eng, err := decide.New(engPol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.r.SetProfiles(eng.Profile)
	// Imported when the profile scored it 1000; the running profile has no formats, so it scores 0 now.
	e.link(1, 10, 0, "WEBDL-2160p", 1000, 1000)
	c := cand(h("a"), "Movie.2024.2160p.WEB-DL-GRP")
	c.Score = 100
	res, err := e.r.Route(e.ctx, freshReq(c, "wanted"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded {
		t.Fatalf("outcome %s: %s", res.Outcome, steps(res))
	}
}

// slowDeleter is a Real-Debrid adder whose stalled delete hangs until its context ends.
type slowDeleter struct{ *fakeAdder }

func (f slowDeleter) DeleteOwnStalledAdd(ctx context.Context, _ string, _ adder.OwnAdd) error {
	<-ctx.Done()
	return ctx.Err()
}

// The provider writes a poll makes under the router lock (Real-Debrid's file selection, the delete of our own
// stalled add) are bounded by providerWriteLimit: a provider that never answers them cannot hold the lock, and the
// write's own checks and outcome stay as they were.
func TestPollProviderWritesAreBounded(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	old := providerWriteLimit
	providerWriteLimit = 50 * time.Millisecond
	t.Cleanup(func() { providerWriteLimit = old })
	e.ad["rd"].onSelect = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	e.r.adders["rd"] = slowDeleter{e.ad["rd"]}
	added := e.clock.Add(-2 * e.r.stallAfter("rd"))
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, own_add, added_at, created_at, updated_at) VALUES
		(1, 1, 10, 'movie', ?, 'Movie.A', 'pub', 'rd', 't1', 'added', 1, ?, 0, 0),
		(2, 1, 10, 'movie', ?, 'Movie.B', 'pub', 'rd', 't2', 'added', 1, ?, 0, 0)`,
		h("a"), store.Millis(e.clock), h("b"), store.Millis(added))
	e.pr["rd"].set("t1", provider.Torrent{Hash: h("a"), State: provider.StateQueued, Status: rdWaitingFiles,
		Files: []provider.File{{ID: "1", Path: "Movie.A.mkv", Bytes: 1 << 30}}})
	e.pr["rd"].set("t2", provider.Torrent{Hash: h("b"), State: provider.StateQueued, Status: "magnet_conversion"})
	done := make(chan error, 1)
	go func() { _, err := e.r.Poll(e.ctx); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a provider write that never answers held the poll")
	}
	if !e.r.mu.TryLock() {
		t.Fatal("router lock still held")
	}
	e.r.mu.Unlock()
	var why string
	if err := e.st.Reader().QueryRow(`SELECT COALESCE(error, '') FROM download WHERE id = 1 AND state = 'added'
		AND files_selected = 0`).Scan(&why); err != nil || !strings.Contains(why, "selecting files") {
		t.Errorf("cut selection: %q %v", why, err)
	}
	if err := e.st.Reader().QueryRow(`SELECT error FROM download WHERE id = 2 AND state = 'failed'`).Scan(&why); err != nil ||
		!strings.Contains(why, "kept on rd: the delete failed") {
		t.Errorf("cut stalled delete: %q %v", why, err)
	}
}

// A selection request that failed on our side but reached Real-Debrid: the next read shows our own add past the
// selection wait, so it is recorded as selected and goes on, instead of stalling at added. A reused torrent, or an add
// with no selection request, is never inferred. Deletion proof is independent of selection inference.
func TestSelectionInferredForOwnAdd(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	now := store.Millis(e.clock)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, own_add, reused, error, added_at, created_at, updated_at) VALUES
		(1, 1, 10, 'movie', ?, 'Movie.A', 'pub', 'rd', 't1', 'added', 1, 0, 'selecting files: context deadline exceeded', ?, 0, 0),
		(2, 1, 10, 'movie', ?, 'Movie.B', 'pub', 'rd', 't2', 'added', 0, 0, 'selecting files: context deadline exceeded', ?, 0, 0),
		(3, 1, 10, 'movie', ?, 'Movie.C', 'pub', 'rd', 't3', 'added', 1, 1, 'selecting files: context deadline exceeded', ?, 0, 0),
		(4, 1, 10, 'movie', ?, 'Movie.D', 'pub', 'rd', 't4', 'added', 1, 0, NULL, ?, 0, 0)`,
		h("a"), now, h("b"), now, h("c"), now, h("d"), now)
	for i, x := range []string{"a", "b", "c", "d"} {
		e.pr["rd"].set(fmt.Sprintf("t%d", i+1), provider.Torrent{Hash: h(x), State: provider.StateDownloading,
			Status: "downloading", Progress: 10})
	}
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id IN (1, 2) AND files_selected = 1 AND error IS NULL
		AND state = 'downloading'`); n != 2 {
		t.Error("our own add past the selection wait was not recorded as selected")
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id IN (3, 4) AND files_selected = 1`); n != 0 {
		t.Errorf("%d selections inferred for a reused torrent or no selection sent", n)
	}
}
