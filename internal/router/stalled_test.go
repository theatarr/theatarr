package router

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider/adder"
)

func withQueue(e *env) {
	q := jobs.NewQueue(e.st)
	q.SetClock(func() time.Time { return e.clock })
	e.r.RegisterJobs(jobs.NewWorker(q, jobs.WorkerOptions{}), q)
}

func researchJobs(e *env) []ResearchRequest {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT payload FROM job WHERE kind = ? ORDER BY id`, JobResearch)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []ResearchRequest
	for rows.Next() {
		var p string
		_ = rows.Scan(&p)
		var r ResearchRequest
		_ = json.Unmarshal([]byte(p), &r)
		out = append(out, r)
	}
	return out
}

// A release a person added by magnet or hash that stalls fails and is blocklisted as any other, but no search is
// started for the title: the blocklist row is marked researched without a re-search job.
func TestStalledManualAddStartsNoSearch(t *testing.T) {
	t.Parallel()
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, false)
	withQueue(e)
	f.rdStuck = true
	res, err := e.r.Route(e.ctx, grabReq(manualCand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.Provider != "rd" {
		t.Fatalf("%+v %v", res, err)
	}
	e.clock = e.clock.Add(10 * time.Minute)
	sum, err := e.r.Poll(e.ctx)
	if err != nil || sum.Stalled != 1 || sum.Researched != 0 {
		t.Fatalf("%+v %v", sum, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist WHERE info_hash = ? AND source = 'stalled'
		AND version_key = 'movie' AND researched_at IS NOT NULL`, h("a")); n != 1 {
		t.Error("not blocklisted, or left for a later pass")
	}
	if got := researchJobs(e); len(got) != 0 {
		t.Errorf("research jobs %+v", got)
	}
}

// A release added by hand that is a miss on every provider queues no re-search; a release grabbed by hand from a
// search does, as before.
func TestMissedManualAddStartsNoSearch(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	for k := range e.ad {
		e.ad[k].add = refuse(adder.CodeNotCached)
	}
	res, err := e.r.Route(e.ctx, grabReq(manualCand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.Outcome != OutcomeExhausted || !missedEverywhere(res.Steps) {
		t.Fatalf("%+v %v", res, err)
	}
	if got := researchJobs(e); len(got) != 0 {
		t.Errorf("research jobs after a manual add missed everywhere %+v", got)
	}
	res, err = e.r.Route(e.ctx, grabReq(cand(h("b"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.Outcome != OutcomeExhausted {
		t.Fatalf("%+v %v", res, err)
	}
	if got := researchJobs(e); len(got) != 1 || got[0].Reason != ReasonExhausted {
		t.Errorf("research jobs after a manual grab missed everywhere %+v", got)
	}
}

// A download still in added stalled_after past its add fails as stalled, is blocklisted, our own Real-Debrid add is
// deleted, and the title is searched again.
func TestStalledDownload(t *testing.T) {
	t.Parallel()
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, false)
	withQueue(e)
	f.rdStuck = true
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.Provider != "rd" {
		t.Fatalf("%+v %v", res, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE own_add = 1 AND state = 'added'`); n != 1 {
		t.Fatal("own_add not recorded")
	}
	e.clock = e.clock.Add(9 * time.Minute)
	if sum, _ := e.r.Poll(e.ctx); sum.Stalled != 0 {
		t.Fatal("stalled before stalled_after")
	}
	e.clock = e.clock.Add(time.Minute)
	sum, err := e.r.Poll(e.ctx)
	if err != nil || sum.Stalled != 1 || sum.Researched != 1 {
		t.Fatalf("%+v %v", sum, err)
	}
	if f.count("rd.test DELETE /rest/1.0/torrents/delete/RD1001") != 1 || len(f.rd) != 0 {
		t.Error("our stalled add was not deleted")
	}
	var msg string
	_ = e.st.Reader().QueryRow(`SELECT error FROM download WHERE id = ? AND state = 'failed'`, res.DownloadID).Scan(&msg)
	if !strings.HasPrefix(msg, "stalled: not ready 10m0s after the add") || !strings.Contains(msg, "our add was deleted") {
		t.Errorf("error %q", msg)
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist WHERE info_hash = ? AND source = 'stalled'
		AND version_key = 'movie' AND researched_at IS NOT NULL`, h("a")); n != 1 {
		t.Error("not blocklisted and researched")
	}
	if got := researchJobs(e); len(got) != 1 || got[0].Reason != ReasonStalled || got[0].TitleID != 1 ||
		got[0].VersionID != 10 || got[0].DownloadID != res.DownloadID {
		t.Errorf("research jobs %+v", got)
	}
	// Polled again: nothing more.
	if sum, _ := e.r.Poll(e.ctx); sum.Researched != 0 || len(researchJobs(e)) != 1 {
		t.Error("researched twice")
	}
	// The same release is never routed again for the version.
	res, _ = e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if !strings.Contains(steps(res), "blocklisted") {
		t.Errorf("steps %s", steps(res))
	}
}

// The stall delete keeps everything that is not provably our own Real-Debrid add.
func TestStalledNeverDeletesShared(t *testing.T) {
	t.Parallel()
	insert := func(e *env, id int64, pk, tid, hash string, ownAdd any) {
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
			provider, provider_torrent_id, state, own_add, added_at, created_at, updated_at) VALUES (?, 1, 10, 'movie', ?,
			'x', 'pub', ?, ?, 'added', ?, ?, 0, 0)`, id, hash, pk, tid, ownAdd, e.clock.Add(-time.Hour).UnixMilli())
	}
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, false)
	// Reused (own_add NULL); not provably new (0); own but another download knows the id; AllDebrid.
	f.rd["RDR"] = &fakeRD{hash: h("a"), status: "magnet_conversion"}
	insert(e, 1, "rd", "RDR", h("a"), nil)
	f.rd["RDZ"] = &fakeRD{hash: h("b"), status: "magnet_conversion"}
	insert(e, 2, "rd", "RDZ", h("b"), 0)
	f.rd["RDS"] = &fakeRD{hash: h("c"), status: "magnet_conversion"}
	insert(e, 3, "rd", "RDS", h("c"), 1)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, created_at, updated_at) VALUES (4, 2, 20, 'series', ?, 'y', 'pub', 'rd', 'RDS',
		'imported', 0, 0)`, h("c"))
	f.ad[55] = &fakeAD{hash: h("d"), code: 0}
	insert(e, 5, "ad", "55", h("d"), 1)
	sum, err := e.r.Poll(e.ctx)
	if err != nil || sum.Stalled != 4 {
		t.Fatalf("%+v %v", sum, err)
	}
	if f.deletes() != 0 || len(f.rd) != 3 || len(f.ad) != 1 {
		t.Errorf("deleted: %v", f.list())
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE state = 'failed' AND error LIKE 'stalled:%kept on%'`); n != 4 {
		t.Errorf("%d stalled kept", n)
	}
	// The kill switch: our own stalled add fails, but nothing is deleted and nothing searched.
	e, f, _ = newDebridEnv(t, false, policy.ModeActive, true)
	withQueue(e)
	f.rd["RDK"] = &fakeRD{hash: h("a"), status: "magnet_conversion"}
	insert(e, 1, "rd", "RDK", h("a"), 1)
	if sum, _ := e.r.Poll(e.ctx); sum.Stalled != 1 || f.deletes() != 0 || len(researchJobs(e)) != 0 {
		t.Errorf("kill switch: %+v, %v", sum, f.list())
	}
	// Off: nothing stalls.
	e, f, _ = newDebridEnv(t, true, policy.ModeActive, false)
	off := policy.Duration(0)
	e.pol.Router.StalledAfter = &off
	e.r.set = e.pol.RouterSettings()
	f.rd["RDX"] = &fakeRD{hash: h("a"), status: "magnet_conversion"}
	insert(e, 1, "rd", "RDX", h("a"), 1)
	if sum, _ := e.r.Poll(e.ctx); sum.Stalled != 0 {
		t.Error("stalled with stalled_after 0s")
	}
}

// Every candidate missed on every provider: the title is searched again at once (active only). Releases skipped
// without a request do not count against max_candidates.
func TestExhaustedRouteResearches(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	for _, k := range []string{"rd", "ad", "tb"} {
		e.ad[k].add = refusePlain
	}
	res, err := e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Candidates: []Candidate{
		withEps(cand(h("a"), "Show.S01.2160p.WEB.H265-GRP"), 7, 8), withEps(cand(h("b"), "Show.S01E09.2160p.WEB.H265-GRP"), 9)}})
	if err != nil || res.Outcome != OutcomeExhausted {
		t.Fatalf("%+v %v", res, err)
	}
	got := researchJobs(e)
	if len(got) != 1 || got[0].Reason != ReasonExhausted || got[0].VersionID != 20 ||
		len(got[0].Episodes) != 3 {
		t.Errorf("research %+v", got)
	}
	// max_candidates 5: five known misses are skipped without a request and don't count; the sixth is tried.
	e.r.set.MaxCandidates = 2
	adds := len(e.ad["rd"].adds)
	cs := []Candidate{cand(h("a"), "A.2160p.WEB.H265-GRP"), cand(h("b"), "B.2160p.WEB.H265-GRP"),
		cand(h("c"), "C.2160p.WEB.H265-GRP")}
	res, _ = e.r.Route(e.ctx, movieReq(cs...))
	if len(e.ad["rd"].adds) != adds+1 || !strings.Contains(steps(res), "cccc/rd/refused") {
		t.Errorf("steps %s", steps(res))
	}
	// A shadow version: no re-search.
	e.pol.Versions["movie"].Mode = policy.ModeShadow
	n := len(researchJobs(e))
	_, _ = e.r.Route(e.ctx, movieReq(cand(h("e"), "E.2160p.WEB.H265-GRP")))
	if len(researchJobs(e)) != n {
		t.Error("a shadow route asked for a re-search")
	}
}

// Dangerous files and the library's guard: blocklisted failures are searched again from the poll.
func TestBlocklistedFailuresResearch(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
		episodes, created_at, updated_at) VALUES (5, 2, 20, 'series', ?, 'x', 'pub', 'failed', '[7]', 0, 0)`, h("a"))
	e.exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, reason, source, download_id, created_at)
		VALUES ('series', ?, 2, 'sample', 'library', 5, 0)`, h("a"))
	e.exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, reason, source, download_id, created_at)
		VALUES ('series', ?, 2, 'arr', 'queue', 5, 0)`, h("b"))
	sum, err := e.r.Poll(e.ctx)
	if err != nil || sum.Researched != 1 {
		t.Fatalf("%+v %v", sum, err)
	}
	if got := researchJobs(e); len(got) != 1 || got[0].Reason != ReasonLibrary || len(got[0].Episodes) != 1 ||
		got[0].Episodes[0] != 7 {
		t.Errorf("%+v", got)
	}
}

var refusePlain = refuse(adder.CodeBlocked)

func withEps(c Candidate, eps ...int64) Candidate {
	c.Episodes = eps
	return c
}
