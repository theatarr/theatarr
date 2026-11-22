package router

import (
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// playingRun records the acquire run of a search for a file that broke while playing (research playing) and the
// incident's row of the title's version.
func (e *env) playingRun(run, title, version int64, key string, runEps, incidentEps, out string) {
	e.t.Helper()
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, episodes, research, state,
		created_at, updated_at) VALUES (?, 'wanted', ?, ?, ?, 's01', ?, 'playing', 'routed', 0, 0)`, run, title, version,
		key, runEps)
	e.exec(`INSERT INTO playing_break (incident, watched, link_id, title_id, version_key, path, old_target, episodes,
		broken_at, out_account, searched, updated_at) VALUES (1, 1, 1, ?, ?, '/lib/x.mkv', '/mnt/x.mkv', ?, ?, ?, 1, 0)`,
		title, key, incidentEps, e.clock.UnixMilli(), out)
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
// account at once, and no miss is recorded on the out account. (Real-Debrid refuses this release's name, so AllDebrid
// would take it first; the incident found AllDebrid out.)
func TestPlayingRouteSkipsOutAccount(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	freshMovie(e)
	e.exec(`UPDATE version SET state = 'wanted' WHERE id = 10`)
	e.playingRun(7, 1, 10, "movie", "[]", "[]", "ad")
	res, err := e.r.Route(e.ctx, freshReq(cand(h("a"), "Movie.2024.2160p.WEB-DL-GRP"), "acquire:7"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || res.Provider != "tb" || e.ad["ad"].writes() != 0 ||
		!strings.Contains(steps(res), "aaaa/ad/skipped/"+codeProviderDone) {
		t.Errorf("route %+v: %s", res, steps(res))
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE provider = 'ad' AND state = 'miss'`); n != 0 {
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

// Once the watched file's search has ended (its outcome reported), the release's other files' search takes only a
// release holding one of their episodes, not the watched episode again.
func TestPlayingSiblingRouteSkipsWatchedEpisode(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.exec(`INSERT INTO episode (id, title_id, season, number, created_at, updated_at)
		VALUES (101, 2, 1, 1, 0, 0), (103, 2, 1, 3, 0, 0)`)
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at)
		VALUES (20, 101, 1, 'wanted', 0), (20, 103, 1, 'wanted', 0)`)
	e.playingRun(8, 2, 20, "series", "[101,103]", "[101]", "")
	e.exec(`UPDATE playing_break SET reported = 'unavailable'`)
	e.exec(`INSERT INTO playing_break (incident, watched, link_id, title_id, version_key, path, old_target, episodes,
		broken_at, searched, updated_at) VALUES (1, 0, 2, 2, 'series', '/lib/y.mkv', '/mnt/y.mkv', '[103]', 0, 1, 0)`)
	watched := cand(h("a"), "Show.S01E01.2160p.WEB-DL-GRP")
	watched.Episodes = []int64{101}
	sibling := cand(h("b"), "Show.S01E03.2160p.WEB-DL-GRP")
	sibling.Episodes = []int64{103}
	res, err := e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Source: "acquire:8",
		Candidates: []Candidate{watched, sibling}})
	if err != nil {
		t.Fatal(err)
	}
	if e.writes() != 1 || res.Hash != h("b") || !strings.Contains(steps(res), "aaaa//skipped/not_wanted") {
		t.Errorf("route %+v: %s; adds %d", res, steps(res), e.writes())
	}
}

// A release skipped for the miss of a file that broke while playing says so, in the provider's step and the client's.
func TestPlayingMissWorded(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	freshMovie(e)
	tx, err := e.st.Writer().BeginTx(e.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"ad", ClientCacheKey("dc")} {
		if err := RecordPlayingMiss(e.ctx, tx, h("a"), k, e.clock); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB-DL-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	var msg string
	for _, st := range res.Steps {
		if st.Provider == "ad" {
			msg = st.Message
		}
	}
	if msg != "it stopped working here while it was playing; kept off this account for 7 days" {
		t.Errorf("ad step %q: %s", msg, steps(res))
	}
	s := &routeState{vp: &policy.VersionPolicy{AddVia: policy.AddViaClient, Client: "dc"}}
	if _, msg, err := e.r.clientSkip(e.ctx, s, &indexer.Release{}, h("a")); err != nil ||
		msg != "it stopped working on client dc while it was playing; kept off it for 7 days" {
		t.Errorf("client skip %q %v", msg, err)
	}
}

// GOAL e: the search for a broken file does not take the release again on the account it broke on (its router:playing
// miss), and takes it on another account. (Real-Debrid refuses this release's name, so AllDebrid, where it broke, would
// take it first.)
func TestPlayingRouteSkipsBrokenReleaseOnItsAccount(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	freshMovie(e)
	e.exec(`UPDATE version SET state = 'wanted' WHERE id = 10`)
	e.playingRun(7, 1, 10, "movie", "[]", "[]", "")
	tx, err := e.st.Writer().BeginTx(e.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordPlayingMiss(e.ctx, tx, h("a"), "ad", e.clock); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	res, err := e.r.Route(e.ctx, freshReq(cand(h("a"), "Movie.2024.2160p.WEB-DL-GRP"), "acquire:7"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || res.Provider != "tb" || e.ad["ad"].writes() != 0 ||
		!strings.Contains(steps(res), "aaaa/ad/skipped/miss") {
		t.Errorf("route %+v: %s", res, steps(res))
	}
}

// An account found out by an incident older than a day steers nothing: a later search of the title takes it again.
func TestPlayingRouteOldOutAccountIgnored(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	freshMovie(e)
	e.exec(`UPDATE version SET state = 'wanted' WHERE id = 10`)
	e.playingRun(7, 1, 10, "movie", "[]", "[]", "ad")
	e.exec(`UPDATE playing_break SET broken_at = ?`, e.clock.Add(-25*time.Hour).UnixMilli())
	res, err := e.r.Route(e.ctx, freshReq(cand(h("a"), "Movie.2024.2160p.WEB-DL-GRP"), "acquire:7"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || res.Provider != "ad" || strings.Contains(steps(res), codeProviderDone) {
		t.Errorf("route %+v: %s", res, steps(res))
	}
}

// A playing search never takes the incident's own release again, on any account (E4, E12): the route leaves that hash
// out for this search only, with no miss written, so other libraries may still use it. Hashes of older unsettled
// incidents of the title are not left out.
func TestPlayingRouteLeavesOutIncidentRelease(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	freshMovie(e)
	e.exec(`UPDATE version SET state = 'wanted' WHERE id = 10`)
	e.playingRun(7, 1, 10, "movie", "[]", "[]", "")
	e.exec(`UPDATE playing_break SET info_hash = ?`, h("a"))
	res, err := e.r.Route(e.ctx, Request{TitleID: 1, VersionID: 10, Version: "movie", Source: "acquire:7",
		Candidates: []Candidate{cand(h("a"), "Movie.2024.2160p.WEB-DL-GRP"), cand(h("b"), "Movie.2024.2160p.WEB-DL-OTHER")}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || res.Hash != h("b") || !strings.Contains(steps(res), "aaaa//skipped/playing_same_release") {
		t.Errorf("route %+v: %s", res, steps(res))
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE info_hash = ?`, h("a")); n != 0 {
		t.Errorf("%d cache rows for the incident's release", n)
	}
	// An older open incident of the title (another release) is not this search's: its hash may be taken.
	e2 := newEnv(t, true)
	freshMovie(e2)
	e2.exec(`UPDATE version SET state = 'wanted' WHERE id = 10`)
	e2.playingRun(7, 1, 10, "movie", "[]", "[]", "")
	e2.exec(`UPDATE playing_break SET info_hash = ?, broken_at = ?`, h("b"), e2.clock.Add(-72*time.Hour).UnixMilli())
	e2.exec(`INSERT INTO playing_break (incident, watched, link_id, title_id, version_key, path, old_target, info_hash,
		episodes, broken_at, searched, updated_at) VALUES (2, 1, 1, 1, 'movie', '/lib/x.mkv', '/mnt/y.mkv', ?, '[]', ?, 1, 0)`,
		h("a"), e2.clock.UnixMilli())
	res, err = e2.r.Route(e2.ctx, Request{TitleID: 1, VersionID: 10, Version: "movie", Source: "acquire:7",
		Candidates: []Candidate{cand(h("b"), "Movie.2024.2160p.WEB-DL-OTHER")}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || res.Hash != h("b") {
		t.Errorf("older incident's release: route %+v: %s", res, steps(res))
	}
}
