package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/store"
)

// The gate: a shadow version never gets a provider write, even with the switch on; it gets a recorded plan.
func TestShadowVersionNeverWrites(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeShadow)
	// Were the add sent, RD would show it uncached: with cached_only that is the one case the router deletes. The
	// fakes count deletes as writes.
	e.pr["rd"].set("rd-aaaa", provider.Torrent{Status: "downloading", State: provider.StateDownloading})
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if e.writes() != 0 {
		t.Fatalf("%d provider writes for a shadow version", e.writes())
	}
	if res.Mode != ModeShadow || res.Outcome != OutcomeWouldAdd || res.Provider != "rd" || res.PlanID == 0 ||
		!strings.Contains(res.Why, "shadow") {
		t.Errorf("result %+v", res)
	}
	if n := e.count(`SELECT COUNT(*) FROM download`); n != 0 {
		t.Errorf("%d download rows for a dry run", n)
	}
	var mode, outcome, prov, stepsJSON string
	if err := e.st.Reader().QueryRow(`SELECT mode, outcome, provider, steps FROM router_plan WHERE id = ?`,
		res.PlanID).Scan(&mode, &outcome, &prov, &stepsJSON); err != nil {
		t.Fatal(err)
	}
	if mode != "shadow" || outcome != "would_add" || prov != "rd" || !strings.Contains(stepsJSON, "would_add") {
		t.Errorf("plan %s %s %s %s", mode, outcome, prov, stepsJSON)
	}
	// The poller doesn't select files for a shadow version either.
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, created_at, updated_at) VALUES (5, 1, 10, 'movie', ?, 'x', 'pub', 'rd', 'T1',
		'added', 0, 0)`, h("b"))
	e.pr["rd"].set("T1", provider.Torrent{Status: "waiting_files_selection", State: provider.StateQueued,
		Files: []provider.File{{ID: "1", Path: "a.mkv", Bytes: 10}}})
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if e.writes() != 0 {
		t.Errorf("the poller wrote for a shadow version")
	}
}

// The kill switch: router.enabled false blocks every write, for active versions too.
func TestKillSwitchBlocksWrites(t *testing.T) {
	t.Parallel()
	for _, pol := range []*policy.Router{nil, {Enabled: false}} {
		e := newEnv(t, false, policy.ModeActive)
		e.pr["rd"].set("rd-aaaa", provider.Torrent{Status: "downloading", State: provider.StateDownloading})
		e.pol.Router = pol
		e.r.set = e.pol.RouterSettings()
		res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
		if err != nil {
			t.Fatal(err)
		}
		if e.writes() != 0 || res.Mode != ModeDisabled || res.Outcome != OutcomeWouldAdd {
			t.Errorf("writes %d, result %+v", e.writes(), res)
		}
		if _, err := e.r.Gate("movie"); !errors.Is(err, ErrDisabled) {
			t.Errorf("gate: %v", err)
		}
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
			provider, provider_torrent_id, state, created_at, updated_at) VALUES (5, 1, 10, 'movie', ?, 'x', 'pub',
			'rd', 'T1', 'added', 0, 0)`, h("b"))
		e.pr["rd"].set("T1", provider.Torrent{Status: "waiting_files_selection", State: provider.StateQueued,
			Files: []provider.File{{ID: "1", Path: "a.mkv", Bytes: 10}}})
		if _, err := e.r.Poll(e.ctx); err != nil {
			t.Fatal(err)
		}
		if e.writes() != 0 {
			t.Error("the poller wrote with the switch off")
		}
		if n := e.count(`SELECT COUNT(*) FROM router_plan WHERE mode = 'disabled'`); n != 1 {
			t.Errorf("%d disabled plans", n)
		}
	}
	// Plan never writes, whatever the gate says.
	e := newEnv(t, true, policy.ModeActive)
	res, err := e.r.Plan(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || e.writes() != 0 || res.Mode != ModeActive || res.Outcome != OutcomeWouldAdd {
		t.Errorf("plan: writes %d, %+v %v", e.writes(), res, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM router_plan`) + e.count(`SELECT COUNT(*) FROM download`); n != 0 {
		t.Errorf("Plan recorded %d rows", n)
	}
}

func TestRouteAddsAndPolls(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != ModeActive || res.Outcome != OutcomeAdded || res.Provider != "rd" || res.DownloadID == 0 ||
		res.TorrentID != "rd-aaaa" {
		t.Fatalf("result %+v", res)
	}
	if len(e.ad["rd"].adds) != 1 || e.ad["ad"].writes()+e.ad["tb"].writes() != 0 {
		t.Errorf("adds rd %v ad %v tb %v", e.ad["rd"].adds, e.ad["ad"].adds, e.ad["tb"].adds)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE state = 'added' AND provider = 'rd'
		AND provider_torrent_id = 'rd-aaaa' AND version_id = 10 AND quality = 'WEBDL-2160p'`); n != 1 {
		t.Error("download row")
	}
	if n := e.count(`SELECT COUNT(*) FROM download_attempt WHERE outcome = 'accepted' AND provider = 'rd'`); n != 1 {
		t.Error("attempt row")
	}
	// The same release again: in flight, nothing added.
	res, err = e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.Outcome != OutcomeInFlight || len(e.ad["rd"].adds) != 1 {
		t.Errorf("second route: %+v %v", res, err)
	}
	// Poll: RD converting, then waiting for files: every playable file is selected (not the sample or the nfo);
	// then downloaded: ready, which calls the ready notifier once, after the write committed (another connection
	// already reads the ready row).
	var woken atomic.Int32
	var seen atomic.Value
	e.r.SetReadyNotify(func() {
		woken.Add(1)
		var state string
		_ = e.st.Reader().QueryRow(`SELECT state FROM download WHERE provider_torrent_id = 'rd-aaaa'`).Scan(&state)
		seen.Store(state)
	})
	e.pr["rd"].set("rd-aaaa", provider.Torrent{Status: "magnet_conversion", State: provider.StateQueued})
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE state = 'added'`); n != 1 {
		t.Error("magnet_conversion should stay added")
	}
	e.pr["rd"].set("rd-aaaa", provider.Torrent{Status: "waiting_files_selection", State: provider.StateQueued,
		Name: "Movie.2024.2160p.WEB.H265-GRP", Files: []provider.File{{ID: "1", Path: "Movie/sample.mkv", Bytes: 50},
			{ID: "2", Path: "Movie/Movie.mkv", Bytes: 9000}, {ID: "3", Path: "Movie/extra.mkv", Bytes: 800},
			{ID: "4", Path: "Movie/info.nfo", Bytes: 1}}})
	sum, err := e.r.Poll(e.ctx)
	if err != nil || sum.Selected != 1 {
		t.Fatalf("poll %+v %v", sum, err)
	}
	if got := e.ad["rd"].selects; len(got) != 1 || strings.Join(got[0], ",") != "rd-aaaa,2,3" {
		t.Errorf("selected %v", got)
	}
	e.pr["rd"].set("rd-aaaa", provider.Torrent{Status: "downloaded", State: provider.StateDownloaded, Progress: 100,
		Name: "Movie.2024.2160p.WEB.H265-GRP"})
	if n := woken.Load(); n != 0 {
		t.Errorf("notified %d times before ready", n)
	}
	if sum, err := e.r.Poll(e.ctx); err != nil || sum.Ready != 1 {
		t.Fatalf("poll %+v %v", sum, err)
	}
	if n := woken.Load(); n != 1 || seen.Load() != StateReady {
		t.Errorf("ready notified %d times, the notifier read %v", n, seen.Load())
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE state = 'ready' AND cached = 1 AND ready_at IS NOT NULL
		AND name = 'Movie.2024.2160p.WEB.H265-GRP' AND files_selected = 1`); n != 1 {
		var s string
		e.st.Reader().QueryRow(`SELECT state || ' ' || COALESCE(cached, 'null') || ' ' || name FROM download`).Scan(&s)
		t.Errorf("ready row: %s", s)
	}
	// Ready rows are the library's: the poller leaves them.
	e.pr["rd"].set("rd-aaaa", provider.Torrent{Status: "error", State: provider.StateError})
	if sum, _ := e.r.Poll(e.ctx); sum.Read != 0 || woken.Load() != 1 {
		t.Errorf("polled a ready download: %+v, notified %d times", sum, woken.Load())
	}
}

func TestFallbackAcrossProviders(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.ad["ad"].add = refuse(adder.CodeTooManyActive)
	// RD's name rule refuses WEB-DL: skipped without a request; AD has no slot; TorBox takes it.
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB-DL.DDP5.1-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || res.Provider != "tb" {
		t.Fatalf("%+v: %s", res, steps(res))
	}
	if len(e.ad["rd"].adds) != 0 || len(e.ad["ad"].adds) != 1 || len(e.ad["tb"].adds) != 1 {
		t.Errorf("adds rd %d ad %d tb %d", len(e.ad["rd"].adds), len(e.ad["ad"].adds), len(e.ad["tb"].adds))
	}
	if got := steps(res); got != "aaaa/rd/skipped/rd_name_rule; aaaa/ad/refused/too_many_active_downloads; aaaa/tb/accepted/added; " {
		t.Errorf("steps %s", got)
	}
	// AD backs off: the next route skips it until then.
	p, _ := loadPace(e.ctx, e.st.Reader(), "ad")
	if p.Failures != 1 || !p.BackoffUntil.Equal(e.clock.Add(BackoffBase)) {
		t.Errorf("ad pacing %+v", p)
	}
	res, _ = e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series",
		Candidates: []Candidate{cand(h("b"), "Show.S01.2160p.WEB-DL-GRP")}})
	if len(e.ad["ad"].adds) != 1 || !strings.Contains(steps(res), "ad/deferred/paced") || res.Provider != "tb" {
		t.Errorf("ad not skipped while backing off: %s", steps(res))
	}
}

func TestMissOnEveryProvider(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	missed := map[string]bool{h("a"): true}
	for k, code := range map[string]adder.Code{"rd": adder.CodeBlocked, "ad": adder.CodeInvalid, "tb": adder.CodeNotCached} {
		e.ad[k].add = func(m adder.Magnet, o adder.AddOptions) (*adder.Added, error) {
			if k == "tb" && !o.OnlyIfCached {
				t.Error("TorBox add without add_only_if_cached while cached_only is on")
			}
			if missed[m.Hash()] {
				return refuse(code)(m, o)
			}
			return &adder.Added{ID: k + "-x"}, nil
		}
	}
	req := movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"), cand(h("b"), "Movie.2024.1080p.WEB.H265-GRP"))
	res, err := e.r.Route(e.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || res.Hash != h("b") || res.Provider != "rd" {
		t.Fatalf("%+v: %s", res, steps(res))
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE info_hash = ? AND state = 'miss'
		AND expires_at = ?`, h("a"), e.clock.Add(6*time.Hour).UnixMilli()); n != 3 {
		t.Errorf("%d miss rows, want 3", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE info_hash = ? AND state = 'failed'
		AND error = 'miss on every provider'`, h("a")); n != 1 {
		t.Error("the missed release's download should fail with the reason")
	}
	if n := e.count(`SELECT COUNT(*) FROM download_attempt a JOIN download d ON d.id = a.download_id
		WHERE d.info_hash = ? AND a.outcome = 'refused'`, h("a")); n != 3 {
		t.Errorf("%d refused attempts recorded, want 3", n)
	}
	// Within miss_ttl the hash is skipped everywhere with no request.
	before := e.writes()
	e.clock = e.clock.Add(2 * time.Minute)
	res, _ = e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series",
		Candidates: []Candidate{cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")}})
	if e.writes() != before || res.Outcome != OutcomeExhausted {
		t.Errorf("a cached miss was asked again: %+v %s", res, steps(res))
	}
	// After it expires, it is asked again.
	e.clock = e.clock.Add(7 * time.Hour)
	_, _ = e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series",
		Candidates: []Candidate{cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")}})
	if e.writes() == before {
		t.Error("an expired miss was not asked again")
	}
}

// Privacy is a label only: a private-labelled release goes to the version's providers, narrowed by its indexer's
// providers allowlist (indexers.K.providers); an allowlist that leaves none fails the route with a clear reason.
func TestPrivateLabelFollowsIndexerProviders(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	priv := cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")
	priv.Release.Indexer, priv.Release.Privacy = "priv", policy.PrivacyPrivate
	res, err := e.r.Route(e.ctx, movieReq(priv))
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != "rd" || len(e.ad["rd"].adds) != 1 || e.ad["rd"].adds[0] != h("a") {
		t.Fatalf("private-labelled: %+v %s", res, steps(res))
	}
	e = newEnv(t, true, policy.ModeActive)
	e.pol.Indexers["priv"].Providers = []string{"tb"}
	only := cand(h("b"), "Movie.2024.1080p.WEB.H265-GRP")
	only.Release.Indexer = "priv"
	if res, err = e.r.Route(e.ctx, movieReq(only)); err != nil {
		t.Fatal(err)
	}
	if res.Provider != "tb" || len(e.ad["rd"].adds) != 0 || len(e.ad["ad"].adds) != 0 {
		t.Fatalf("allowlist [tb]: %+v %s", res, steps(res))
	}
	e = newEnv(t, true, policy.ModeActive)
	e.pol.Indexers["priv"].Providers = []string{"elsewhere"}
	none := cand(h("c"), "Movie.2024.720p.WEB.H265-GRP")
	none.Release.Indexer = "priv"
	if res, err = e.r.Route(e.ctx, movieReq(none)); err != nil {
		t.Fatal(err)
	}
	if e.writes() != 0 || !strings.Contains(fmt.Sprint(res.Steps), "allows none of the version's providers") {
		t.Fatalf("empty allowlist: writes %d %+v", e.writes(), res.Steps)
	}
	// A seedbox version is not routed to debrid at all.
	res, _ = e.r.Route(e.ctx, Request{TitleID: 1, VersionID: 10, Version: "seed",
		Candidates: []Candidate{cand(h("e"), "x")}})
	if e.writes() != 0 || res.Steps[0].Code != "not_debrid" {
		t.Errorf("seedbox version: %s", steps(res))
	}
	// No hash: skipped.
	res, _ = e.r.Route(e.ctx, movieReq(cand("", "x")))
	if e.writes() != 0 || res.Steps[0].Code != "no_hash" {
		t.Errorf("no hash: %s", steps(res))
	}
}

func TestDeferredWhenPaced(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	// RD: 3 adds a minute with a 10-minute cooldown after the third (a burst).
	for i, c := range []string{"a", "b", "c"} {
		res, _ := e.r.Route(e.ctx, Request{TitleID: 1, VersionID: 10, Version: "movie", Source: "ui:grab:test", Candidates: []Candidate{
			cand(h(c), "Movie.2024.2160p.WEB.H265-GRP")}})
		if res.Provider != "rd" {
			t.Fatalf("add %d went to %s", i, res.Provider)
		}
	}
	p, _ := loadPace(e.ctx, e.st.Reader(), "rd")
	if !p.CooldownUntil.Equal(e.clock.Add(10*time.Minute)) || !strings.Contains(p.Reason, "burst") {
		t.Errorf("rd pacing %+v", p)
	}
	// AD and TB unavailable too: the route is deferred to the earliest free time.
	e.exec(`INSERT INTO router_pacing (provider, backoff_until, failures, reason, updated_at) VALUES
		('ad', ?, 2, 'too_many_active_downloads', 0), ('tb', ?, 1, 'rate_limited', 0)`,
		e.clock.Add(30*time.Minute).UnixMilli(), e.clock.Add(5*time.Minute).UnixMilli())
	res, err := e.r.Route(e.ctx, grabReq(cand(h("d"), "Movie.2024.2160p.WEB.H265-GRP"),
		cand(h("e"), "Movie.2024.1080p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeDeferred || !res.RetryAt.Equal(e.clock.Add(5*time.Minute)) || res.Hash != h("d") {
		t.Fatalf("%+v %s", res, steps(res))
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'queued'`, res.DownloadID); n != 1 {
		t.Error("deferred download should be queued")
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE info_hash = ?`, h("e")); n != 0 {
		t.Error("a paced route must not fall to the next release")
	}
	// Resumed after the back-off: the queued row is reused.
	e.clock = e.clock.Add(6 * time.Minute)
	req := grabReq(cand(h("d"), "Movie.2024.2160p.WEB.H265-GRP"))
	req.DownloadID = res.DownloadID
	res2, err := e.r.Route(e.ctx, req)
	if err != nil || res2.Outcome != OutcomeAdded || res2.Provider != "tb" || res2.DownloadID != res.DownloadID {
		t.Errorf("resume: %+v %v %s", res2, err, steps(res2))
	}
}

func TestReuseHeldTorrent(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (7, ?, 'present', 0, 0)`, h("a"))
	e.exec(`INSERT INTO content_provider (content_id, provider, torrent_id, seen_at) VALUES (7, 'rd', 'HELD', 0)`)
	e.pr["rd"].set("HELD", provider.Torrent{Hash: h("a"), Status: "downloaded", State: provider.StateDownloaded})
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if e.writes() != 0 || res.Outcome != OutcomeAdded || !res.Reused || res.TorrentID != "HELD" {
		t.Errorf("writes %d, %+v", e.writes(), res)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE reused = 1 AND provider_torrent_id = 'HELD' AND state = 'added'`); n != 1 {
		t.Error("reused row")
	}
}

// A 451 on a hash holds the account only when another title's hash got one within holdWindow.
func TestAccountHoldCoolsDown(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.ad["rd"].add = refuse(adder.CodeAccountHold)
	e.exec(`INSERT INTO router_refusal_451 (provider, info_hash, refused_at) VALUES ('rd', ?, ?)`, h("z"),
		store.Millis(e.clock.Add(-time.Minute)))
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.Provider != "ad" {
		t.Fatalf("%+v %v", res, err)
	}
	p, _ := loadPace(e.ctx, e.st.Reader(), "rd")
	if !p.CooldownUntil.Equal(e.clock.Add(10*time.Minute)) || p.Failures != 0 {
		t.Errorf("rd %+v", p)
	}
	// The hold explains the 451: the hash is not blocked, it is tried again after the hold.
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE provider = 'rd' AND state = 'miss'`); n != 0 {
		t.Error("a 451 the account hold explains blocked its hash on rd")
	}
}

// TestOne451BlocksTheHashNotTheAccount: Real-Debrid refusing one release with 451 every time used to hold
// the account, and the resumed route sent the same hash first after every hold, so RD sat on the 60-minute step and
// the next release was never tried. Now the 451 blocks that hash on RD (miss_ttl) and the route moves on; the same
// hash refused again is no sign of an account hold.
func TestOne451BlocksTheHashNotTheAccount(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["movie"].Providers = []string{"rd"}
	e.ad["rd"].add = func(m adder.Magnet, _ adder.AddOptions) (*adder.Added, error) {
		if m.Hash() == h("a") {
			return nil, &adder.Error{Provider: "rd", Code: adder.CodeAccountHold, Status: 451}
		}
		return &adder.Added{ID: "rd-" + m.Hash()[:4], Hash: m.Hash()}, nil
	}
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"),
		cand(h("b"), "Movie.2024.1080p.WEB.H265-GRP")))
	if err != nil || res.Outcome != OutcomeAdded || res.Hash != h("b") {
		t.Fatalf("%+v %v %s", res, err, steps(res))
	}
	p, _ := loadPace(e.ctx, e.st.Reader(), "rd")
	if !p.CooldownUntil.IsZero() || p.HoldLevel != 0 || !p.HoldUntil.IsZero() {
		t.Fatalf("one hash's 451 held the account: %+v", p)
	}
	// The same release again (a later search): skipped on RD without an add, still no hold.
	e.clock = e.clock.Add(time.Minute)
	adds := len(e.ad["rd"].adds)
	res, err = e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.Outcome == OutcomeAdded || res.Outcome == OutcomeDeferred || len(e.ad["rd"].adds) != adds {
		t.Fatalf("blocked hash routed again: %+v %v adds %d", res, err, len(e.ad["rd"].adds)-adds)
	}
	// Its 451 memory expired, the same hash refused again still holds nothing.
	e.exec(`DELETE FROM provider_cache`)
	e.clock = e.clock.Add(time.Minute)
	if _, err = e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"))); err != nil {
		t.Fatal(err)
	}
	if p, _ = loadPace(e.ctx, e.st.Reader(), "rd"); !p.CooldownUntil.IsZero() {
		t.Fatalf("the same hash's second 451 held the account: %+v", p)
	}
}

func TestTorBoxCacheCheckFirst(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pr["tb"].cache = map[string]bool{h("b"): true}
	e.ad["rd"].add = refuse(adder.CodeBlocked)
	e.ad["ad"].add = refuse(adder.CodeBlocked)
	res, _ := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"),
		cand(h("b"), "Movie.2024.1080p.WEB.H265-GRP")))
	if len(e.ad["tb"].adds) != 1 || e.ad["tb"].adds[0] != h("b") || res.Hash != h("b") {
		t.Errorf("tb adds %v: %s", e.ad["tb"].adds, steps(res))
	}
	if len(e.pr["tb"].checked) != 2 {
		t.Errorf("checks %+v", e.pr["tb"].checked)
	}
}

func TestNoKeyProviders(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.r.adders = map[string]adder.Adder{}
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "x")))
	if err != nil || res.Outcome != OutcomeExhausted || !strings.Contains(steps(res), "no_provider") {
		t.Errorf("%+v %v %s", res, err, steps(res))
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache`); n != 0 {
		t.Error("no key is not a miss")
	}
}

func TestPollStatuses(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	for i, tid := range []string{"A1", "A2", "A3", "A4"} {
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
			provider, provider_torrent_id, state, created_at, updated_at) VALUES (?, 1, 10, 'movie', ?, 'x', 'pub',
			'ad', ?, 'added', 0, 0)`, i+1, h(string(rune('a'+i))), tid)
	}
	e.pr["ad"].set("A1", provider.Torrent{Status: "Downloading", State: provider.StateDownloading, Progress: 40})
	e.pr["ad"].set("A2", provider.Torrent{Status: "Ready", State: provider.StateDownloaded, Progress: 100})
	e.pr["ad"].set("A3", provider.Torrent{Status: "Error", State: provider.StateError})
	// A4 is gone from the provider.
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at) VALUES (9, 1, 10, 'movie', ?, 'x', 'pub', 'adding', 0, 0)`, h("f"))
	sum, err := e.r.Poll(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Read != 4 || sum.Ready != 1 || sum.Failed != 2 {
		t.Errorf("summary %+v", sum)
	}
	// A4 missing once only counts; it fails when it is still missing after missingGrace.
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = 4 AND state = 'added' AND missing_polls = 1`); n != 1 {
		t.Error("download 4 after one miss")
	}
	e.clock = e.clock.Add(missingGrace)
	if sum, err = e.r.Poll(e.ctx); err != nil || sum.Failed != 1 {
		t.Errorf("second poll %+v %v", sum, err)
	}
	for id, want := range map[int]string{1: "downloading/0", 2: "ready/1", 3: "failed/", 4: "failed/", 9: "failed/"} {
		var s string
		e.st.Reader().QueryRow(`SELECT state || '/' || COALESCE(cached, '') FROM download WHERE id = ?`, id).Scan(&s)
		if s != want {
			t.Errorf("download %d: %s, want %s", id, s, want)
		}
	}
	if p, _ := loadPace(e.ctx, e.st.Reader(), "ad"); p.HourUncached != 1 {
		t.Errorf("an uncached add should count: %+v", p)
	}
	if e.writes() != 0 {
		t.Error("polling AllDebrid wrote")
	}
}

func TestPacing(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 26, 12, 30, 0, 0, time.UTC)
	pc := policy.Pacing{AddsPerMinute: 2, UncachedPerHr: 2, BackoffMax: policy.Duration(10 * time.Minute)}
	var p pace
	if u, _ := p.blocked(now, pc); !u.IsZero() {
		t.Error("fresh pacing blocks")
	}
	p.added(now, pc)
	p.added(now.Add(time.Second), pc)
	if u, why := p.blocked(now.Add(2*time.Second), pc); !u.Equal(now.Add(time.Minute)) || !strings.Contains(why, "adds_per_minute") {
		t.Errorf("window: %v %s", u, why)
	}
	if u, _ := p.blocked(now.Add(time.Minute), pc); !u.IsZero() {
		t.Error("the window should end after a minute")
	}
	p.uncached(now)
	p.uncached(now)
	if u, _ := p.blocked(now.Add(2*time.Minute), pc); !u.Equal(time.Date(2026, 10, 26, 13, 0, 0, 0, time.UTC)) {
		t.Errorf("uncached cap: %v", u)
	}
	var q pace
	for i, want := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 10 * time.Minute, 10 * time.Minute} {
		if u := q.fail(now, pc, "x", time.Time{}, false, false); !u.Equal(now.Add(want)) {
			t.Errorf("failure %d: %v, want +%v", i+1, u.Sub(now), want)
		}
	}
	if u := q.fail(now, pc, "x", now.Add(time.Hour), false, false); !u.Equal(now.Add(time.Hour)) {
		t.Error("Retry-After should win when later")
	}
	var r pace
	if u := r.fail(now, policy.Pacing{}, "auth", time.Time{}, true, false); !u.Equal(now.Add(DefaultBackoffMax)) {
		t.Errorf("to max: %v", u)
	}
	// Burst cooldown.
	pc.Cooldown = policy.Duration(7 * time.Minute)
	var b pace
	b.added(now, pc)
	b.added(now, pc)
	if !b.CooldownUntil.Equal(now.Add(7 * time.Minute)) {
		t.Errorf("burst cooldown %v", b.CooldownUntil)
	}
}

func TestChooseFiles(t *testing.T) {
	t.Parallel()
	files := []provider.File{{ID: "1", Path: "S/Show.S01E01.mkv", Bytes: 100}, {ID: "2", Path: "S/Show.S01E02.mkv", Bytes: 300},
		{ID: "3", Path: "S/Sample/Show.S01E01.sample.mkv", Bytes: 5}, {ID: "4", Path: "S/Show.nfo", Bytes: 1},
		{ID: "5", Path: "S/Subs/eng.srt", Bytes: 1}}
	if got := strings.Join(ChooseFiles(policy.KindSeries, files), ","); got != "1,2" {
		t.Errorf("series %s", got)
	}
	if got := strings.Join(ChooseFiles(policy.KindMovie, files), ","); got != "1,2" {
		t.Errorf("movie %s", got)
	}
	if got := ChooseFiles(policy.KindMovie, files[3:]); got != nil {
		t.Errorf("no video %v", got)
	}
	if got := ChooseFiles(policy.KindMovie, files[2:3]); len(got) != 1 {
		t.Errorf("only a sample: %v", got)
	}
}

func TestCandidatesAndPayload(t *testing.T) {
	t.Parallel()
	acc := &decide.Decision{Accepted: true, Quality: "Remux-2160p", Score: 50}
	rej := &decide.Decision{Accepted: false}
	none := &decide.Decision{Accepted: true}
	rel := indexer.Release{Indexer: "pub", Title: "A", InfoHash: h("a"), Link: indexer.NewLink("https://x.test/dl?apikey=SECRET"),
		Magnet: indexer.NewLink("magnet:?xt=urn:btih:" + h("a") + "&tr=http://t.test/SECRETPASSKEY0123456789/announce")}
	cs := Candidates([]*decide.Decision{acc, rej, none}, map[*decide.Decision]indexer.Release{acc: rel, rej: rel})
	if len(cs) != 1 || cs[0].Quality != "Remux-2160p" || cs[0].Score != 50 {
		t.Fatalf("candidates %+v", cs)
	}
	b, _ := json.Marshal(routePayload{Request: Request{Candidates: cs}})
	if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "magnet:") {
		t.Errorf("a link reached the job payload: %s", b)
	}
}

func TestLogState(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		enabled bool
		mode    policy.Mode
		want    string
	}{{false, policy.ModeActive, "ROUTER DISABLED"}, {true, policy.ModeShadow, "no version is active"},
		{true, policy.ModeActive, "ROUTER ENABLED: adds go"}} {
		e := newEnv(t, tc.enabled, tc.mode)
		var buf strings.Builder
		e.r.log = slogTo(&buf)
		e.r.LogState()
		if !strings.Contains(buf.String(), tc.want) || !strings.Contains(buf.String(), "level=WARN") {
			t.Errorf("%v %s: %s", tc.enabled, tc.mode, buf.String())
		}
	}
}

// A request the gateway refused before sending it is no add and no provider failure: pacing
// is left as it was and the route waits for the refusal's RetryAt, counting no deferral.
func TestGatewayRefusalIsNotAnAdd(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	e.pol.Versions["movie"].Providers = []string{"rd"}
	g := policy.Duration(20 * time.Second)
	e.pol.Providers["rd"].Pacing = policy.Pacing{MinGap: &g, AddsPerMinute: 2, Cooldown: policy.Duration(10 * time.Minute)}
	retry := e.clock.Add(7 * time.Minute)
	e.ad["rd"].add = func(adder.Magnet, adder.AddOptions) (*adder.Added, error) {
		return nil, &adder.Error{Provider: "rd", Code: adder.CodeUnavailable,
			Err: &gateway.Refused{Reason: gateway.ReasonBudget, Target: "provider:rd", Detail: "daily cap", RetryAt: retry}}
	}
	res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.Outcome != OutcomeDeferred || !res.RetryAt.Equal(retry) {
		t.Fatalf("%+v %v %s", res, err, steps(res))
	}
	p, _ := loadPace(e.ctx, e.st.Reader(), "rd")
	if p.WindowAdds != 0 || !p.LastAdd.IsZero() || p.Failures != 0 || !p.BackoffUntil.IsZero() ||
		!p.CooldownUntil.IsZero() {
		t.Fatalf("a refused request counted: %+v", p)
	}
	if next := routeJobs(e, res.DownloadID); len(next) != 1 || next[0].Deferrals != 0 {
		t.Fatalf("continuation %+v, want no deferral counted", next)
	}
}

// An add the torrent manager's restore reserves while a route's add is out is kept: the route
// reserves and records in transactions instead of saving a copy read before its add.
func TestRouteKeepsConcurrentRestoreReservation(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["movie"].Providers = []string{"rd"}
	pc := policy.Pacing{AddsPerMinute: 10}
	e.pol.Providers["rd"].Pacing = pc
	inner := e.ad["rd"].add
	e.ad["rd"].add = func(m adder.Magnet, o adder.AddOptions) (*adder.Added, error) {
		if until, why, err := ReserveAdd(context.Background(), e.st, "rd", pc, e.clock); err != nil || !until.IsZero() {
			t.Errorf("restore reservation: %v %s %v", until, why, err)
		}
		if inner != nil {
			return inner(m, o)
		}
		return &adder.Added{ID: "T1", Hash: m.Hash()}, nil
	}
	res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.Outcome != OutcomeAdded {
		t.Fatalf("%+v %v %s", res, err, steps(res))
	}
	if p, _ := loadPace(e.ctx, e.st.Reader(), "rd"); p.WindowAdds != 2 {
		t.Fatalf("window adds %d, want 2 (the route's and the restore's)", p.WindowAdds)
	}
}
