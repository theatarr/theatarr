package router

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
)

// upgradesEnv is an env whose movie and series versions have the given upgrades rule, a movie file (link) for title 1,
// and a series with two episodes of which only the first has a file.
func upgradesEnv(t *testing.T, enabled bool, mode policy.Mode, rule string) *env {
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

func TestUpgradesOff(t *testing.T) {
	t.Parallel()
	e := upgradesEnv(t, true, policy.ModeActive, policy.UpgradesOff)
	res, err := e.r.Route(e.ctx, movieReq(remux(h("a"), "A", 3000)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeExhausted || e.writes() != 0 || !strings.Contains(steps(res), "skipped/upgrades_off") {
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

// A route an older build queued for an approved upgrade (payload "hold", source upgrade:N) still runs after upgrade
// approval was removed: the release is added like any other upgrade.
func TestQueuedApprovedRouteRunsAfterUpgrade(t *testing.T) {
	t.Parallel()
	e := upgradesEnv(t, true, policy.ModeActive, policy.UpgradesAuto)
	c, err := json.Marshal(remux(h("a"), "A", 3000))
	if err != nil {
		t.Fatal(err)
	}
	p := []byte(`{"title_id":1,"version_id":10,"version":"movie","candidates":[` + string(c) +
		`],"source":"upgrade:7","hold":7}`)
	if err := e.r.handleRoute(e.ctx, &jobs.Job{Kind: JobRoute, Payload: p}); err != nil {
		t.Fatal(err)
	}
	if adds := e.ad["rd"].adds; len(adds) != 1 || adds[0] != h("a") {
		t.Fatalf("adds %v", adds)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE source = 'upgrade:7'`); n != 1 {
		t.Errorf("downloads with source upgrade:7: %d", n)
	}
}
