package router

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/store"
)

// A release missed on every provider opens one failed download (and one re-search) when it is first
// missed; routing it again within miss_ttl (RSS does every sync) opens no new row and asks for no new search.
func TestKnownMissOpensNoDownload(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	for k := range e.ad {
		e.ad[k].add = refuse(adder.CodeNotCached)
	}
	for i := 0; i < 4; i++ {
		e.clock = e.clock.Add(15 * time.Minute)
		res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && (res.DownloadID != 0 || !hasStep(res, "known_miss")) {
			t.Fatalf("route %d: %+v", i, res)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE info_hash = ?`, h("a")); n != 1 {
		t.Errorf("%d download rows for a known miss", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = ?`, JobResearch); n > 1 {
		t.Errorf("%d re-searches for one miss", n)
	}
	// After miss_ttl it is tried again.
	e.clock = e.clock.Add(e.r.set.MissTTL + time.Minute)
	if _, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"))); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE info_hash = ?`, h("a")); n != 2 {
		t.Errorf("%d download rows after miss_ttl", n)
	}
}

func hasStep(res *Result, code string) bool {
	for _, s := range res.Steps {
		if s.Code == code {
			return true
		}
	}
	return false
}

// putEvidence stores a provider_cache row for hash at pk checked at checked, live for five hours after it.
func (e *env) putEvidence(hash, pk, state, source string, checked time.Time) {
	e.t.Helper()
	e.exec(`INSERT OR REPLACE INTO provider_cache (info_hash, provider, state, source, checked_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`, hash, pk, state, source, store.Millis(checked), store.Millis(checked.Add(5*time.Hour)))
}

// A grab by hand (Request.ManualSince) asks the providers again past stored negative evidence checked before the
// click: the same request without it is skipped as before and asks nothing; with it each provider is really tried
// (RD and AD by an add whose read-back decides, our uncached adds deleted; TorBox by its cache check), and a later
// run of the same click honours the answers it got, so it asks no provider twice.
func TestManualGrabAsksAgain(t *testing.T) {
	t.Parallel()
	e, f, _ := newDebridEnv(t, true, true)
	e.pr["tb"].cache = map[string]bool{}
	old := e.clock.Add(-30 * time.Minute)
	e.putEvidence(h("a"), "rd", "miss", "router:miss", old)
	e.putEvidence(h("a"), "ad", "uncached", "router:add", old)
	e.putEvidence(h("a"), "tb", "uncached", "router:check", old)
	c := cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")
	res, err := e.r.Route(e.ctx, grabReq(c))
	if err != nil {
		t.Fatal(err)
	}
	if res.DownloadID != 0 || !hasStep(res, "known_miss") || f.requests() != 0 || len(e.pr["tb"].checked) != 0 ||
		e.ad["tb"].writes() != 0 {
		t.Fatalf("without ManualSince: %+v: %s; requests %v", res, steps(res), f.list())
	}
	manual := grabReq(c)
	manual.ManualSince = e.clock
	if res, err = e.r.Route(e.ctx, manual); err != nil {
		t.Fatal(err)
	}
	if got := steps(res); got != "aaaa/rd/refused/uncached_deleted; aaaa/ad/refused/uncached_deleted; "+
		"aaaa/tb/skipped/known_uncached; aaaa//miss/miss; " {
		t.Errorf("manual steps %s", got)
	}
	if f.deletes() != 2 || len(e.pr["tb"].checked) != 1 || e.ad["tb"].writes() != 0 {
		t.Errorf("deletes %d, TorBox checks %d, TorBox writes %d", f.deletes(), len(e.pr["tb"].checked),
			e.ad["tb"].writes())
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE info_hash = ? AND checked_at = ?`, h("a"),
		store.Millis(e.clock)); n != 3 {
		t.Errorf("%d fresh evidence rows, want 3", n)
	}
	// The same click again (a continuation, the selection retry): the fresh answers stand; it opens its download,
	// which fails with each provider's answer, and asks no provider.
	e.clock = e.clock.Add(time.Minute)
	n := f.requests()
	if res, err = e.r.Route(e.ctx, manual); err != nil {
		t.Fatal(err)
	}
	if res.DownloadID == 0 || hasStep(res, "known_miss") || f.requests() != n || len(e.pr["tb"].checked) != 1 ||
		e.ad["tb"].writes() != 0 {
		t.Errorf("same click again: %+v: %s", res, steps(res))
	}
	if got := attempts(e, res.DownloadID); got != "rd/skipped/miss; ad/skipped/miss; tb/skipped/known_uncached; " {
		t.Errorf("same click again: attempts %s", got)
	}
}

// A grab by hand does not ask past what asking cannot change: a single-hash 451 and a file name Real-Debrid refused
// (their rows stand), Real-Debrid's release-name rule, pacing, the blocklist and a download in flight.
func TestManualGrabKeepsStandingRefusals(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	old := e.clock.Add(-30 * time.Minute)
	e.putEvidence(h("a"), "rd", "miss", "router:451", old)
	e.putEvidence(h("a"), "ad", "miss", "router:rd_name", old)
	e.putEvidence(h("a"), "tb", "miss", "router:miss", old)
	manual := func(c Candidate) *Result {
		t.Helper()
		req := grabReq(c)
		req.ManualSince = e.clock
		res, err := e.r.Route(e.ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := manual(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"))
	if got := steps(res); got != "aaaa/rd/skipped/miss; aaaa/ad/skipped/miss; aaaa/tb/accepted/added; " {
		t.Errorf("steps %s", got)
	}
	if len(e.ad["rd"].adds) != 0 || len(e.ad["ad"].adds) != 0 || len(e.ad["tb"].adds) != 1 {
		t.Errorf("adds rd %v ad %v tb %v", e.ad["rd"].adds, e.ad["ad"].adds, e.ad["tb"].adds)
	}
	// Its download, rebuilt after a restart, keeps honouring what it asked since it was opened.
	if req, err := DownloadRequest(e.ctx, e.st.Reader(), e.pol, res.DownloadID); err != nil ||
		!req.ManualSince.Equal(e.clock) {
		t.Errorf("rebuilt request: %+v %v", req, err)
	}
	// In flight: nothing more is added.
	if res = manual(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")); res.Outcome != OutcomeInFlight ||
		len(e.ad["tb"].adds) != 1 {
		t.Errorf("in flight: %+v: %s", res, steps(res))
	}
	// Real-Debrid's name rule and pacing stand; AD is asked again past its old miss.
	e.ad["ad"].add = refuse(adder.CodeNotCached)
	e.putEvidence(h("b"), "rd", "miss", "router:miss", old)
	e.putEvidence(h("b"), "ad", "miss", "router:miss", old)
	e.putEvidence(h("b"), "tb", "miss", "router:miss", old)
	e.exec(`INSERT OR REPLACE INTO router_pacing (provider, backoff_until, failures, reason, updated_at) VALUES ('tb', ?, 1, 'x', 0)`,
		e.clock.Add(time.Minute).UnixMilli())
	res = manual(cand(h("b"), "Movie.2024.2160p.WEB-DL.DDP5.1-GRP"))
	if got := steps(res); res.Outcome != OutcomeDeferred ||
		got != "bbbb/rd/skipped/rd_name_rule; bbbb/ad/refused/torrent_not_cached; bbbb/tb/deferred/paced; " {
		t.Errorf("name rule and pacing: %s: %s", res.Outcome, got)
	}
	// The blocklist.
	e.exec(`INSERT INTO router_blocklist (version_key, info_hash, release_title, reason, source, created_at)
		VALUES ('movie', ?, 'x', 'rejected', 'library', 0)`, h("c"))
	if res = manual(cand(h("c"), "Movie.2024.2160p.WEB.H265-GRP")); res.DownloadID != 0 || !hasStep(res, "blocklisted") {
		t.Errorf("blocklisted: %+v: %s", res, steps(res))
	}
}

// A grab by hand always opens its download, even when no provider can be asked: an RD-only library and a release
// name RD refuses fails the download with RD's answer as its attempt, where an automatic route skips it as before.
func TestManualGrabOpensDownloadWhenNothingCanBeAsked(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.pol.Versions["movie"].Providers = []string{"rd"}
	c := cand(h("a"), "Movie.2024.2160p.WEB-DL.DDP5.1-GRP")
	e.putEvidence(h("a"), "rd", "miss", "router:miss", e.clock.Add(-time.Minute))
	res, err := e.r.Route(e.ctx, grabReq(c))
	if err != nil || res.DownloadID != 0 || !hasStep(res, "known_miss") {
		t.Fatalf("automatic: %+v %v: %s", res, err, steps(res))
	}
	manual := grabReq(c)
	manual.ManualSince = e.clock
	if res, err = e.r.Route(e.ctx, manual); err != nil || res.DownloadID == 0 {
		t.Fatalf("manual: %+v %v: %s", res, err, steps(res))
	}
	if got := attempts(e, res.DownloadID); got != "rd/skipped/rd_name_rule; " {
		t.Errorf("attempts %s", got)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'failed' AND error = ?`, res.DownloadID,
		MissOnEveryProvider); n != 1 {
		t.Error("the download did not fail as a miss on every provider")
	}
	if e.writes() != 0 {
		t.Errorf("%d provider writes", e.writes())
	}
}

// ScreenManual lets a grab of a hash through when its queued download is stranded (no route job will pick it up): the
// route resumes that row. One a route job holds is in flight.
func TestScreenManualResumesStranded(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		score, episodes, state, created_at, updated_at) VALUES (5, 1, 10, 'movie', ?, 'Movie.HELD', 'pub', 'WEBDL-2160p',
		100, '[]', 'queued', 0, 0)`, h("a"))
	screen := func() *Unstartable {
		t.Helper()
		u, err := ScreenManual(e.ctx, e.st, e.pol, nil, func() time.Time { return e.clock },
			grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	if u := screen(); u != nil {
		t.Errorf("stranded queued row: %+v", u)
	}
	e.exec(`INSERT INTO job (kind, payload, dedupe_key, state, run_at, created_at, updated_at) VALUES (?, '{}', ?, 'queued', 0, 0, 0)`,
		JobRoute, routeJobKey(5))
	if u := screen(); u == nil || u.Code != "in_flight" || u.Release != "Movie.HELD" {
		t.Errorf("queued row with its route job: %+v", u)
	}
}
