package router

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// holdEnv is an env whose movie and series versions have upgrades: approve, a movie file (link) for title 1, and a
// series with two episodes of which only the first has a file.
func holdEnv(t *testing.T, enabled bool, mode policy.Mode, rule string) *env {
	t.Helper()
	e := newEnv(t, enabled, mode)
	e.pol.Versions["movie"].Upgrades = rule
	e.pol.Versions["series"].Upgrades = rule
	e.exec(`INSERT INTO episode (id, title_id, season, number, created_at, updated_at) VALUES (101, 2, 1, 1, 0, 0),
		(102, 2, 1, 2, 0, 0)`)
	e.link(1, 10, 0, "WEBDL-2160p", 1400, 1000)
	e.link(2, 20, 101, "WEBDL-2160p", 900, 1000)
	return e
}

// link gives version vid of title tid an active link (for episode eid when not 0), made at at.
func (e *env) link(tid, vid, eid int64, quality string, score int, at int64) int64 {
	e.t.Helper()
	path := fmt.Sprintf("/lib/%d", e.count(`SELECT COUNT(*) FROM link`))
	res, err := e.st.Writer().Exec(`INSERT INTO link (path, target, title_id, quality, score, state, created_at,
		updated_at) VALUES (?, '/mnt/x', ?, ?, ?, 'active', ?, ?)`, path, tid, quality, score, at, at)
	if err != nil {
		e.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (?, ?)`, id, vid)
	if eid != 0 {
		e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (?, ?)`, id, eid)
	}
	return id
}

func remux(hash, title string, score int) Candidate {
	c := cand(hash, title)
	c.Quality, c.Score, c.QualityRank, c.UpgradeReason = "Remux-2160p", score, 5, "better quality: Remux-2160p over WEBDL-2160p"
	return c
}

func (e *env) holds(state string) int {
	return e.count(`SELECT COUNT(*) FROM upgrade_hold WHERE state = ?`, state)
}

func TestUpgradeHeldNotAdded(t *testing.T) {
	t.Parallel()
	e := holdEnv(t, true, policy.ModeActive, policy.UpgradesApprove)
	res, err := e.r.Route(e.ctx, movieReq(remux(h("a"), "Movie.2160p.Remux", 3000)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeHeld || res.Hold == 0 || res.Release != "Movie.2160p.Remux" {
		t.Fatalf("outcome %s hold %d release %q: %s", res.Outcome, res.Hold, res.Release, steps(res))
	}
	if e.writes() != 0 || e.count(`SELECT COUNT(*) FROM download`) != 0 {
		t.Fatalf("a held upgrade wrote: %d writes, %d downloads", e.writes(), e.count(`SELECT COUNT(*) FROM download`))
	}
	var cq, rep, reason, src string
	var cs, at int64
	if err := e.st.Reader().QueryRow(`SELECT current_quality, current_score, current_at, replaces, reason, source
		FROM upgrade_hold WHERE id = ? AND state = 'pending'`, res.Hold).Scan(&cq, &cs, &at, &rep, &reason, &src); err != nil {
		t.Fatal(err)
	}
	if cq != "WEBDL-2160p" || cs != 1400 || at != 1000 || rep != "[]" || !strings.Contains(reason, "better quality") ||
		src != "test" {
		t.Errorf("hold evidence %q %d %d %s %q %q", cq, cs, at, rep, reason, src)
	}
	if s := PlanSummary(string(res.Outcome), res.Release, "", res.Steps); s != "held Movie.2160p.Remux for upgrade approval" {
		t.Errorf("summary %q", s)
	}
}

func TestMissingFileNeverHeld(t *testing.T) {
	t.Parallel()
	e := holdEnv(t, true, policy.ModeActive, policy.UpgradesApprove)
	e.exec(`DELETE FROM link_version WHERE version_id = 10`)
	res, err := e.r.Route(e.ctx, movieReq(remux(h("a"), "Movie.2160p.Remux", 3000)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || e.holds(HoldPending) != 0 || len(e.ad["rd"].adds) != 1 {
		t.Fatalf("outcome %s, %d holds, adds %v", res.Outcome, e.holds(HoldPending), e.ad["rd"].adds)
	}
}

// A series pack that replaces episode 1's file is held; a release that only fills episode 2 is added.
func TestSeriesHoldsOnlyReplacingReleases(t *testing.T) {
	t.Parallel()
	e := holdEnv(t, true, policy.ModeActive, policy.UpgradesApprove)
	pack := remux(h("a"), "Show.S01.Remux", 3000)
	pack.Episodes = []int64{101, 102}
	fill := cand(h("b"), "Show.S01E02.WEB")
	fill.Episodes = []int64{102}
	res, err := e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Candidates: []Candidate{pack, fill}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || res.Release != "Show.S01E02.WEB" || res.Hold == 0 {
		t.Fatalf("outcome %s release %q hold %d: %s", res.Outcome, res.Release, res.Hold, steps(res))
	}
	var eps, rep string
	if err := e.st.Reader().QueryRow(`SELECT episodes, replaces FROM upgrade_hold WHERE id = ?`, res.Hold).Scan(&eps, &rep); err != nil {
		t.Fatal(err)
	}
	if eps != "101,102" || rep != "[101]" {
		t.Errorf("hold episodes %q replaces %s", eps, rep)
	}
	if adds := e.ad["rd"].adds; len(adds) != 1 || adds[0] != h("b") {
		t.Errorf("adds %v", adds)
	}
}

func TestHoldDedupe(t *testing.T) {
	t.Parallel()
	e := holdEnv(t, true, policy.ModeActive, policy.UpgradesApprove)
	route := func(cs ...Candidate) *Result {
		t.Helper()
		res, err := e.r.Route(e.ctx, movieReq(cs...))
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	first := route(remux(h("a"), "A", 3000), remux(h("b"), "B", 2000))
	if first.Hold == 0 || e.holds(HoldPending) != 1 || !strings.Contains(steps(first), "bbbb//skipped/upgrade_waiting") {
		t.Fatalf("one route, two upgrades: %d pending: %s", e.holds(HoldPending), steps(first))
	}
	// The same release again: the same hold, seen again.
	e.clock = e.clock.Add(time.Hour)
	if again := route(remux(h("a"), "A", 3000)); again.Hold != first.Hold || again.Outcome != OutcomeHeld {
		t.Fatalf("again: hold %d outcome %s", again.Hold, again.Outcome)
	}
	if seen := e.count(`SELECT seen_at FROM upgrade_hold WHERE id = ?`, first.Hold); int64(seen) != store.Millis(e.clock) {
		t.Errorf("seen_at %d", seen)
	}
	// A worse release waits behind it.
	if worse := route(remux(h("c"), "C", 100)); worse.Hold != first.Hold || !strings.Contains(steps(worse), "upgrade_waiting") {
		t.Fatalf("worse: hold %d: %s", worse.Hold, steps(worse))
	}
	// A better one replaces it.
	better := route(remux(h("d"), "D", 9000))
	if better.Hold == first.Hold || e.holds(HoldPending) != 1 || e.holds(HoldReplaced) != 1 {
		t.Fatalf("better: hold %d, %d pending, %d replaced", better.Hold, e.holds(HoldPending), e.holds(HoldReplaced))
	}
	// A higher quality rank wins over a higher score.
	hq := remux(h("e"), "E", 10)
	hq.QualityRank = 9
	if got := route(hq); got.Hold == better.Hold || e.holds(HoldReplaced) != 2 {
		t.Fatalf("higher quality: hold %d, %d replaced", got.Hold, e.holds(HoldReplaced))
	}
	if e.writes() != 0 {
		t.Error("holding wrote to a provider")
	}
}

func (e *env) approver() (*Holds, *jobs.Worker) {
	q := jobs.NewQueue(e.st)
	q.SetClock(func() time.Time { return e.clock })
	w := jobs.NewWorker(q, jobs.WorkerOptions{})
	e.r.RegisterJobs(w, q)
	return &Holds{Store: e.st, Policy: e.pol, Queue: q, Now: func() time.Time { return e.clock }}, w
}

func TestApproveRoutesHeldRelease(t *testing.T) {
	t.Parallel()
	e := holdEnv(t, true, policy.ModeActive, policy.UpgradesApprove)
	e.exec(`INSERT INTO web_user (id, username, password_hash, created_at, updated_at, password_changed_at)
		VALUES (7, 'mgr', '', 0, 0, 0)`)
	res, err := e.r.Route(e.ctx, movieReq(remux(h("a"), "A", 3000)))
	if err != nil {
		t.Fatal(err)
	}
	hs, w := e.approver()
	if err := hs.Approve(e.ctx, res.Hold, Decider{UserID: 7, Via: "session"}); err != nil {
		t.Fatal(err)
	}
	if err := hs.Approve(e.ctx, res.Hold, Decider{UserID: 7}); !errors.Is(err, ErrHoldNotPending) {
		t.Errorf("second approve: %v", err)
	}
	if err := hs.Approve(e.ctx, 999, Decider{UserID: 7}); !errors.Is(err, ErrHoldNotFound) {
		t.Errorf("unknown hold: %v", err)
	}
	if ran, err := w.RunOne(e.ctx); err != nil || !ran {
		t.Fatalf("route job: ran %v err %v", ran, err)
	}
	if adds := e.ad["rd"].adds; len(adds) != 1 || adds[0] != h("a") {
		t.Fatalf("adds %v", adds)
	}
	var by, at int64
	var via, outcome string
	var dl int64
	if err := e.st.Reader().QueryRow(`SELECT decided_by, decided_at, decided_via, route_outcome, download_id
		FROM upgrade_hold WHERE id = ? AND state = 'approved'`, res.Hold).Scan(&by, &at, &via, &outcome, &dl); err != nil {
		t.Fatal(err)
	}
	if by != 7 || at != store.Millis(e.clock) || via != "session" || outcome != "added" || dl == 0 {
		t.Errorf("audit by %d at %d via %q outcome %q download %d", by, at, via, outcome, dl)
	}
	if src := e.text(`SELECT source FROM download WHERE id = ?`, dl); src != "upgrade:"+strconv.FormatInt(res.Hold, 10) {
		t.Errorf("download source %q", src)
	}
}

func (e *env) text(q string, args ...any) string {
	e.t.Helper()
	var s string
	if err := e.st.Reader().QueryRowContext(e.ctx, q, args...).Scan(&s); err != nil {
		e.t.Fatal(err)
	}
	return s
}

// Approval refuses what the router could not place now; the hold stays pending.
func TestApproveGates(t *testing.T) {
	t.Parallel()
	e := holdEnv(t, true, policy.ModeActive, policy.UpgradesApprove)
	res, err := e.r.Route(e.ctx, movieReq(remux(h("a"), "A", 3000)))
	if err != nil {
		t.Fatal(err)
	}
	hs, _ := e.approver()
	var refused *HoldRefused
	e.pol.Router.Enabled = false
	if err := hs.Approve(e.ctx, res.Hold, Decider{}); !errors.As(err, &refused) || !strings.Contains(err.Error(), "router.enabled") {
		t.Errorf("kill switch: %v", err)
	}
	e.pol.Router.Enabled = true
	e.pol.Versions["movie"].Mode = policy.ModeShadow
	if err := hs.Approve(e.ctx, res.Hold, Decider{}); !errors.As(err, &refused) || !strings.Contains(err.Error(), "shadow") {
		t.Errorf("shadow: %v", err)
	}
	e.pol.Versions["movie"].Mode, e.pol.Versions["movie"].Grab = policy.ModeActive, true
	e.pol.Versions["movie"].Upgrades = policy.UpgradesOff
	if err := hs.Approve(e.ctx, res.Hold, Decider{}); !errors.As(err, &refused) || !strings.Contains(err.Error(), "off") {
		t.Errorf("off: %v", err)
	}
	if e.holds(HoldPending) != 1 || e.count(`SELECT COUNT(*) FROM job`) != 0 {
		t.Errorf("a refused approval changed the hold or queued a route")
	}
}

func TestDeclinedNotOfferedAgain(t *testing.T) {
	t.Parallel()
	e := holdEnv(t, true, policy.ModeActive, policy.UpgradesApprove)
	res, err := e.r.Route(e.ctx, movieReq(remux(h("a"), "A", 3000)))
	if err != nil {
		t.Fatal(err)
	}
	hs, _ := e.approver()
	if err := hs.Decline(e.ctx, res.Hold, Decider{Via: "key k"}, " too big "); err != nil {
		t.Fatal(err)
	}
	if note := e.text(`SELECT note FROM upgrade_hold WHERE id = ? AND state = 'declined'`, res.Hold); note != "too big" {
		t.Errorf("note %q", note)
	}
	if err := hs.Decline(e.ctx, res.Hold, Decider{}, ""); !errors.Is(err, ErrHoldNotPending) {
		t.Errorf("second decline: %v", err)
	}
	again, err := e.r.Route(e.ctx, movieReq(remux(h("a"), "A", 3000), remux(h("b"), "B", 2000)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(steps(again), "aaaa//skipped/upgrade_declined") || e.holds(HoldPending) != 1 ||
		e.text(`SELECT info_hash FROM upgrade_hold WHERE state = 'pending'`) != h("b") || e.writes() != 0 {
		t.Fatalf("after decline: %s", steps(again))
	}
}

func TestExpireHolds(t *testing.T) {
	t.Parallel()
	e := holdEnv(t, true, policy.ModeActive, policy.UpgradesApprove)
	route := func(c Candidate) int64 {
		t.Helper()
		res, err := e.r.Route(e.ctx, movieReq(c))
		if err != nil || res.Hold == 0 {
			t.Fatalf("route: %v %+v", err, res)
		}
		return res.Hold
	}
	expire := func(want string) {
		t.Helper()
		n, err := ExpireHolds(e.ctx, e.st, e.pol, e.clock)
		if err != nil || n != 1 {
			t.Fatalf("expired %d, err %v", n, err)
		}
		if note := e.text(`SELECT note FROM upgrade_hold WHERE state = 'expired' ORDER BY decided_at DESC, id DESC LIMIT 1`); !strings.Contains(note, want) {
			t.Errorf("note %q, want %q", note, want)
		}
	}
	// Still standing: nothing expires.
	route(remux(h("a"), "A", 3000))
	if n, err := ExpireHolds(e.ctx, e.st, e.pol, e.clock); err != nil || n != 0 {
		t.Fatalf("expired %d, err %v", n, err)
	}
	// A newer file arrived.
	e.link(1, 10, 0, "Remux-2160p", 5000, 2000)
	expire("a newer file arrived (Remux-2160p)")
	// The release is gone: not offered again within HoldTTL.
	route(remux(h("b"), "B", 9000))
	e.clock = e.clock.Add(HoldTTL + time.Minute)
	expire("no search or RSS read offered")
	// Blocklisted for the version.
	route(remux(h("c"), "C", 9000))
	e.exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, release_title, reason, source, created_at)
		VALUES ('movie', ?, 1, 'C', 'sample', 'library', 0)`, h("c"))
	expire("blocklisted")
	// An arr can blocklist a held release by title without knowing its torrent hash.
	route(remux(h("e"), "Title.Only", 9000))
	e.exec(`INSERT INTO router_blocklist (version_key, info_hash, release_title, reason, source, created_at)
		VALUES ('movie', ?, 'Title.Only', 'arr blocklist (radarr)', 'arr:radarr', 0)`, BlocklistKey("", "Title.Only"))
	expire("blocklisted")
	// The rule changed.
	route(remux(h("d"), "D", 9000))
	e.pol.Versions["movie"].Upgrades = policy.UpgradesAuto
	expire("no longer approve")
	e.pol.Versions["movie"].Upgrades = policy.UpgradesApprove
	// The file is gone.
	route(remux(h("f"), "F", 9000))
	e.exec(`UPDATE link SET state = 'unlinked'`)
	expire("is gone")
	if e.holds(HoldPending) != 0 || e.holds(HoldExpired) != 6 {
		t.Errorf("%d pending, %d expired", e.holds(HoldPending), e.holds(HoldExpired))
	}
}

func TestUpgradesOff(t *testing.T) {
	t.Parallel()
	e := holdEnv(t, true, policy.ModeActive, policy.UpgradesOff)
	res, err := e.r.Route(e.ctx, movieReq(remux(h("a"), "A", 3000)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeExhausted || e.writes() != 0 || e.holds(HoldPending) != 0 ||
		!strings.Contains(steps(res), "skipped/upgrades_off") {
		t.Fatalf("off: %s %s", res.Outcome, steps(res))
	}
	// A missing episode is still filled.
	fill := cand(h("b"), "Show.S01E02")
	fill.Episodes = []int64{102}
	if res, err := e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Candidates: []Candidate{fill}}); err != nil ||
		res.Outcome != OutcomeAdded {
		t.Fatalf("fill: %v %+v", err, res)
	}
}

// A shadow version records what it would hold in its dry-run plan and holds nothing.
func TestShadowWouldHold(t *testing.T) {
	t.Parallel()
	e := holdEnv(t, true, policy.ModeShadow, policy.UpgradesApprove)
	res, err := e.r.Route(e.ctx, movieReq(remux(h("a"), "A", 3000)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeWouldHold || res.PlanID == 0 || e.holds(HoldPending) != 0 || e.writes() != 0 {
		t.Fatalf("shadow: %s plan %d: %s", res.Outcome, res.PlanID, steps(res))
	}
	var outcome, st string
	if err := e.st.Reader().QueryRow(`SELECT outcome, steps FROM router_plan WHERE id = ?`, res.PlanID).Scan(&outcome, &st); err != nil {
		t.Fatal(err)
	}
	if outcome != "none" || !strings.Contains(st, `"would_hold"`) {
		t.Errorf("plan %s %s", outcome, st)
	}
	if s := PlanSummary(outcome, "", "", res.Steps); s != "would hold A for upgrade approval" {
		t.Errorf("summary %q", s)
	}
}
