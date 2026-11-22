package router

import (
	"strings"
	"testing"
	"time"
)

// playingRun records the acquire run of a search for a file that broke while playing (research playing) and the
// incident's row of the title's version.
func (e *env) playingRun(run, title, version int64, key string, runEps, incidentEps, out string) {
	e.t.Helper()
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, episodes, research, state,
		created_at, updated_at) VALUES (?, 'wanted', ?, ?, ?, 's01', ?, 'playing', 'routed', 0, 0)`, run, title, version,
		key, runEps)
	e.exec(`INSERT INTO playing_break (incident, watched, link_id, title_id, version_key, path, old_target, episodes,
		broken_at, out_account, searched, updated_at) VALUES (1, 1, 1, ?, ?, '/lib/x.mkv', '/mnt/x.mkv', ?, 0, ?, 1, 0)`,
		title, key, incidentEps, out)
}

// The search for a broken watched episode runs for its season (the run's episodes), yet its route takes only a
// release holding an episode of the incident: a release of another missing episode of the season is not grabbed in
// its place.
func TestPlayingRouteHoldsToIncidentEpisodes(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.exec(`INSERT INTO episode (id, title_id, season, number, created_at, updated_at)
		VALUES (101, 2, 1, 1, 0, 0), (103, 2, 1, 3, 0, 0)`)
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at)
		VALUES (20, 101, 1, 'wanted', 0), (20, 103, 1, 'wanted', 0)`)
	e.playingRun(7, 2, 20, "series", "[101,103]", "[101]", "")
	other := cand(h("b"), "Show.S01E03.2160p.WEB-DL-GRP")
	other.Episodes = []int64{103}
	watched := cand(h("a"), "Show.S01E01.2160p.WEB-DL-GRP")
	watched.Episodes = []int64{101}
	res, err := e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Source: "acquire:7",
		Candidates: []Candidate{other, watched}})
	if err != nil {
		t.Fatal(err)
	}
	if e.writes() != 1 || res.Hash != h("a") || !strings.Contains(steps(res), "bbbb//skipped/not_wanted") {
		t.Errorf("route %+v: %s; adds %d", res, steps(res), e.writes())
	}
}

// An account the incident found out answers as not holding the release for this route only: the add goes to the next
// account at once, and no miss is recorded on the out account.
func TestPlayingRouteSkipsOutAccount(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	freshMovie(e)
	e.exec(`UPDATE version SET state = 'wanted' WHERE id = 10`)
	e.playingRun(7, 1, 10, "movie", "[]", "[]", "rd")
	res, err := e.r.Route(e.ctx, freshReq(cand(h("a"), "Movie.2024.2160p.WEB-DL-GRP"), "acquire:7"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || res.Provider != "ad" || e.ad["rd"].writes() != 0 {
		t.Errorf("route %+v: %s", res, steps(res))
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE provider = 'rd' AND state = 'miss'`); n != 0 {
		t.Errorf("%d misses on the out account", n)
	}
}

// The miss of a release on the account it broke on (router:playing) outlives the router's own all-provider miss,
// which would otherwise replace it with a shorter one that a cached listing reopens.
func TestPlayingMissSurvivesAllProviderMiss(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	tx, err := e.st.Writer().BeginTx(e.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordPlayingMiss(e.ctx, tx, h("A"), "rd", e.clock); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := e.r.recordMiss(e.ctx, h("a"), []string{"rd", "ad", "tb"}, nil, e.clock.Add(time.Minute), time.Hour); err != nil {
		t.Fatal(err)
	}
	var source string
	var expires int64
	if err := e.st.Reader().QueryRow(`SELECT source, expires_at FROM provider_cache WHERE info_hash = ? AND provider = 'rd'`,
		h("a")).Scan(&source, &expires); err != nil {
		t.Fatal(err)
	}
	if source != PlayingMissSource || expires != e.clock.Add(PlayingMissTTL).UnixMilli() {
		t.Errorf("rd row %s until %d", source, expires)
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE info_hash = ? AND source = 'router:miss'`, h("a")); n != 2 {
		t.Errorf("%d all-provider misses on the other accounts, want 2", n)
	}
}

// GOAL e: the release that broke is not taken again on the account it broke on by this search (its router:playing
// miss); on another account it may be.
func TestPlayingRouteSkipsFailedReleaseOnFailedAccount(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	freshMovie(e)
	e.exec(`UPDATE version SET state = 'wanted' WHERE id = 10`)
	e.playingRun(7, 1, 10, "movie", "[]", "[]", "")
	tx, err := e.st.Writer().BeginTx(e.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordPlayingMiss(e.ctx, tx, h("a"), "rd", e.clock); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	res, err := e.r.Route(e.ctx, freshReq(cand(h("a"), "Movie.2024.2160p.WEB-DL-GRP"), "acquire:7"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || res.Provider != "ad" || e.ad["rd"].writes() != 0 {
		t.Errorf("route %+v: %s", res, steps(res))
	}
}
