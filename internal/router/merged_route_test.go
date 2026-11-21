package router

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/qbit/qbittest"
)

// mergedEnv is the router env with title 2's three seasons of two episodes (101, 102; 201, 202; 301, 302) and adds
// that fail everywhere for the hashes in fail.
func mergedEnv(t *testing.T, fail ...string) *env {
	e := newEnv(t, true)
	for s := 1; s <= 3; s++ {
		for n := 1; n <= 2; n++ {
			e.exec(`INSERT INTO episode (id, title_id, season, number, created_at, updated_at) VALUES (?, 2, ?, ?, 0, 0)`,
				s*100+n, s, n)
		}
	}
	for _, ad := range e.ad {
		ad.add = func(m adder.Magnet, _ adder.AddOptions) (*adder.Added, error) {
			if slices.Contains(fail, m.Hash()) {
				return nil, &adder.Error{Code: adder.CodeNotCached}
			}
			return &adder.Added{ID: m.Hash()[:4], Hash: m.Hash()}, nil
		}
	}
	return e
}

var (
	spanCand = func() Candidate { return episodeCandidate("e", 101, 102, 201, 202, 301, 302) }
	pack     = func(season int64) Candidate {
		return episodeCandidate(string(rune('0'+season)), season*100+1, season*100+2)
	}
)

func mergedReq(cs ...Candidate) Request {
	r := seriesReq(cs...)
	r.Merged = true
	return r
}

// storedEpisodes are the episodes of the download of hash.
func (e *env) storedEpisodes(hash string) []int64 {
	e.t.Helper()
	var js string
	if err := e.st.Reader().QueryRow(`SELECT episodes FROM download WHERE info_hash = ? ORDER BY id DESC LIMIT 1`, hash).
		Scan(&js); err != nil {
		e.t.Fatal(err)
	}
	var out []int64
	_ = json.Unmarshal([]byte(js), &out)
	return out
}

// A flagged request of a merged search: a pack of several seasons that overlaps what the batch covers or what is in
// flight is narrowed by whole seasons and opened for the others; the same candidates without the flag skip it.
func TestMergedRouteNarrowsBySeason(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		merged    bool
		fail      []string
		inFlight  string // episodes of another download in flight
		cands     []Candidate
		adds      []string
		spanEps   string
		narrowMsg string
	}{
		{name: "span first takes everything", merged: true, cands: []Candidate{spanCand(), pack(1), pack(2), pack(3)},
			adds: []string{h("e")}, spanEps: "[101 102 201 202 301 302]"},
		{name: "a better season 2 pack first", merged: true, cands: []Candidate{pack(2), spanCand(), pack(1), pack(3)},
			adds: []string{h("2"), h("e")}, spanEps: "[101 102 301 302]", narrowMsg: "season 2"},
		{name: "span cannot be added", merged: true, fail: []string{h("e")}, cands: []Candidate{spanCand(), pack(1), pack(2), pack(3)},
			adds: []string{h("1"), h("2"), h("3")}},
		{name: "an episode in flight", merged: true, inFlight: "[201]", cands: []Candidate{spanCand(), pack(1), pack(2), pack(3)},
			adds: []string{h("e")}, spanEps: "[101 102 301 302]", narrowMsg: "season 2"},
		{name: "without the flag", cands: []Candidate{pack(2), spanCand(), pack(1), pack(3)},
			adds: []string{h("2"), h("1"), h("3")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := mergedEnv(t, tc.fail...)
			if tc.inFlight != "" {
				e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, episodes, state,
					created_at, updated_at) VALUES (2, 20, 'series', ?, 'other', 'pub', ?, 'downloading', 0, 0)`, h("f"), tc.inFlight)
			}
			req := seriesReq(tc.cands...)
			req.Merged = tc.merged
			res, err := e.r.Route(e.ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			var placed []string
			for _, st := range res.Steps {
				if st.Outcome == "accepted" {
					placed = append(placed, st.Hash)
				}
			}
			if !slices.Equal(placed, tc.adds) {
				t.Fatalf("placed %v want %v: %s", placed, tc.adds, steps(res))
			}
			if tc.spanEps != "" {
				if got := fmt.Sprint(e.storedEpisodes(h("e"))); got != tc.spanEps {
					t.Errorf("span download episodes %v, want %s", e.storedEpisodes(h("e")), tc.spanEps)
				}
			}
			if tc.narrowMsg != "" {
				found := false
				for _, st := range res.Steps {
					found = found || st.Code == codeSeasonsCovered && strings.Contains(st.Message, tc.narrowMsg)
				}
				if !found {
					t.Errorf("no step names %s: %s", tc.narrowMsg, steps(res))
				}
			}
			if !tc.merged && !hasStep(res, "episode_covered") {
				t.Errorf("unflagged: the span is not skipped: %s", steps(res))
			}
		})
	}
}

// Failed attempts are bounded per season, with one more bucket shared by releases of several seasons; an unflagged
// route stops at max_candidates failed attempts in all.
func TestMergedRouteAttemptBuckets(t *testing.T) {
	t.Parallel()
	span2 := episodeCandidate("d", 101, 102, 201, 202)
	pack1b := episodeCandidate("c", 101, 102)
	for _, merged := range []bool{true, false} {
		e := mergedEnv(t, h("e"), h("1"))
		e.r.set.MaxCandidates = 1
		req := seriesReq(spanCand(), span2, pack(1), pack1b, pack(2))
		req.Merged = merged
		res, err := e.r.Route(e.ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{h("e"), h("1"), h("2")}
		if !merged {
			want = []string{h("e")}
		}
		if !slices.Equal(e.ad["rd"].adds, want) {
			t.Errorf("merged=%v: adds %v want %v: %s", merged, e.ad["rd"].adds, want, steps(res))
		}
	}
}

// Seasons that had a failed attempt and nothing placed or deferred are searched again, all in one re-search; a
// placed season is not.
func TestMergedRouteResearchesSeasonsLeft(t *testing.T) {
	t.Parallel()
	e := mergedEnv(t, h("1"), h("2"))
	q := jobs.NewQueue(e.st)
	e.r.RegisterJobs(jobs.NewWorker(q, jobs.WorkerOptions{}), q)
	res, err := e.r.Route(e.ctx, mergedReq(pack(1), pack(2), pack(3)))
	if err != nil || res.Outcome != OutcomeAdded {
		t.Fatalf("route %+v %v", res, err)
	}
	rows, err := e.st.Reader().Query(`SELECT payload FROM job WHERE kind = ?`, JobResearch)
	if err != nil {
		t.Fatal(err)
	}
	var got []ResearchRequest
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		var rr ResearchRequest
		_ = json.Unmarshal([]byte(p), &rr)
		got = append(got, rr)
	}
	rows.Close()
	if len(got) != 1 || !slices.Equal(got[0].Episodes, []int64{101, 102, 201, 202}) || got[0].Reason != ReasonExhausted {
		t.Fatalf("re-searches %+v", got)
	}
	// Without the flag the route was not exhausted: no re-search, as today.
	e = mergedEnv(t, h("1"), h("2"))
	q = jobs.NewQueue(e.st)
	e.r.RegisterJobs(jobs.NewWorker(q, jobs.WorkerOptions{}), q)
	if _, err := e.r.Route(e.ctx, seriesReq(pack(1), pack(2), pack(3))); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = ?`, JobResearch); n != 0 {
		t.Fatalf("unflagged re-searches %d", n)
	}
}

// A better release for season 2 that has to wait counts as covering its season: the span behind it is opened for
// the other seasons.
func TestMergedRouteDeferredSeason(t *testing.T) {
	t.Parallel()
	e := mergedEnv(t)
	e.pol.Versions["series"].Providers = []string{"rd"}
	e.pol.Providers["rd"].Pacing = policy.Pacing{AddsPerMinute: 1}
	// One add a minute: the first add (season 2's pack) is placed, the span waits. Make season 2's pack wait instead:
	// it goes second after a placement that takes the minute's add.
	first := episodeCandidate("b", 999)
	q := jobs.NewQueue(e.st)
	q.SetClock(func() time.Time { return e.clock })
	w := jobs.NewWorker(q, jobs.WorkerOptions{})
	e.r.RegisterJobs(w, q)
	res, err := e.r.Route(e.ctx, mergedReq(first, pack(2), spanCand()))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeDeferred || res.Hash != h("2") {
		t.Fatalf("result %+v: %s", res, steps(res))
	}
	var js string
	if err := e.st.Reader().QueryRow(`SELECT episodes FROM download WHERE info_hash = ?`, h("e")).Scan(&js); err != nil {
		t.Fatalf("span not opened: %v: %s", err, steps(res))
	}
	if js != "[101,102,301,302]" {
		t.Fatalf("span download episodes %s", js)
	}
	// The retries: season 2's pack and the span are added for their own seasons, nothing else.
	for i := 0; i < 6; i++ {
		e.clock = e.clock.Add(time.Minute)
		if _, err := w.RunOne(e.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(e.ad["rd"].adds, []string{h("b"), h("2"), h("e")}) ||
		e.count(`SELECT COUNT(*) FROM download WHERE state = 'added'`) != 3 || e.count(`SELECT COUNT(*) FROM download`) != 3 {
		t.Fatalf("adds %v, downloads %d", e.ad["rd"].adds, e.count(`SELECT COUNT(*) FROM download`))
	}
	if got := fmt.Sprint(e.storedEpisodes(h("e"))); got != "[101 102 301 302]" {
		t.Fatalf("span episodes after the retry %s", got)
	}
	if got := fmt.Sprint(e.storedEpisodes(h("2"))); got != "[201 202]" {
		t.Fatalf("season 2 pack episodes after the retry %s", got)
	}
}

// A season held by another download stays covered when the same pack also overlaps what the batch placed: season 1
// placed, S02E01 downloading elsewhere and S02E02 missed, the span narrowed to season 3 leaves no season to search
// again.
func TestMergedRouteHeldSeasonWithBatchOverlap(t *testing.T) {
	t.Parallel()
	e := mergedEnv(t, h("c"))
	q := jobs.NewQueue(e.st)
	e.r.RegisterJobs(jobs.NewWorker(q, jobs.WorkerOptions{}), q)
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, episodes, state,
		created_at, updated_at) VALUES (2, 20, 'series', ?, 'other', 'pub', '[201]', 'downloading', 0, 0)`, h("f"))
	res, err := e.r.Route(e.ctx, mergedReq(pack(1), episodeCandidate("c", 202), spanCand()))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(e.storedEpisodes(h("e"))); got != "[301 302]" {
		t.Fatalf("span episodes %s: %s", got, steps(res))
	}
	if got := e.researches(); len(got) != 0 {
		t.Fatalf("re-searches %v: %s", got, steps(res))
	}
}

// A pack of several seasons scope left with one season's episodes is still charged to the packs' bucket: its miss
// does not use up that season's attempts.
func TestMergedRouteBucketByReleaseShape(t *testing.T) {
	t.Parallel()
	e := mergedEnv(t, h("e"))
	e.r.set.MaxCandidates = 1
	span := episodeCandidate("e", 101, 102)
	span.Release.Title = "Show.S01-S02.2160p.WEB-GRP"
	res, err := e.r.Route(e.ctx, mergedReq(span, pack(1)))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.ad["rd"].adds, []string{h("e"), h("1")}) {
		t.Fatalf("adds %v: %s", e.ad["rd"].adds, steps(res))
	}
}

// On an anime series a candidate is a pack of several seasons by its stored claim, not by its name: "S01-S02"
// without a claim narrowed to one season takes that season's bucket; with a span claim it takes the span bucket.
func TestMergedRouteAnimeBucketByClaim(t *testing.T) {
	t.Parallel()
	for _, claimed := range []bool{false, true} {
		e := mergedEnv(t, h("e"))
		e.exec(`UPDATE title SET series_type = 'anime' WHERE id = 2`)
		e.r.set.MaxCandidates = 1
		span := episodeCandidate("e", 101, 102)
		span.Release.Title = "Show.S01-S02.2160p.WEB-GRP"
		if claimed {
			span.EpisodeMap = &decide.EpisodeMap{Pack: &decide.PackClaim{Mode: decide.PackSeasons, Seasons: []int{1, 2},
				Episodes: []int64{101, 102, 201, 202}}}
		}
		res, err := e.r.Route(e.ctx, mergedReq(span, pack(1)))
		if err != nil {
			t.Fatal(err)
		}
		want := []string{h("e")} // season 1's one attempt is used up by the unclaimed "span"
		if claimed {
			want = append(want, h("1"))
		}
		if !slices.Equal(e.ad["rd"].adds, want) {
			t.Fatalf("claimed=%v: adds %v: %s", claimed, e.ad["rd"].adds, steps(res))
		}
	}
}

// researches are the re-search jobs' episodes.
func (e *env) researches() [][]int64 {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT payload FROM job WHERE kind = ? ORDER BY id`, JobResearch)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out [][]int64
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			e.t.Fatal(err)
		}
		var rr ResearchRequest
		_ = json.Unmarshal([]byte(p), &rr)
		out = append(out, rr.Episodes)
	}
	return out
}

// A flagged route that ends exhausted searches again only the seasons that freshly missed, not those whose release
// was only skipped (here blocklisted).
func TestMergedRouteExhaustedResearchesMissedSeasons(t *testing.T) {
	t.Parallel()
	e := mergedEnv(t, h("1"))
	q := jobs.NewQueue(e.st)
	e.r.RegisterJobs(jobs.NewWorker(q, jobs.WorkerOptions{}), q)
	e.exec(`INSERT INTO router_blocklist (version_key, info_hash, release_title, reason, source, created_at)
		VALUES ('series', ?, 'pack', 'rejected', 'library', 0)`, h("2"))
	res, err := e.r.Route(e.ctx, mergedReq(pack(1), pack(2)))
	if err != nil || res.Outcome != OutcomeExhausted {
		t.Fatalf("route %+v %v", res, err)
	}
	if got := e.researches(); len(got) != 1 || !slices.Equal(got[0], []int64{101, 102}) {
		t.Fatalf("re-searches %v", got)
	}
}

// A client's cache miss (a skipped step, no attempt counted) still marks its season for the re-search when another
// season is placed.
func TestMergedRouteClientMissResearched(t *testing.T) {
	t.Parallel()
	e, f := clientEnv(t, true)
	for s := 1; s <= 2; s++ {
		for n := 1; n <= 2; n++ {
			e.exec(`INSERT INTO episode (id, title_id, season, number, created_at, updated_at) VALUES (?, 2, ?, ?, 0, 0)`,
				s*100+n, s, n)
		}
	}
	v := e.pol.Versions["series"]
	v.AddVia, v.Client, v.Providers = policy.AddViaClient, "dc", nil
	q := jobs.NewQueue(e.st)
	e.r.RegisterJobs(jobs.NewWorker(q, jobs.WorkerOptions{}), q)
	f.AddErr[h("1")] = []qbittest.AddError{{Status: 404, Code: "torrent_not_cached", Retryable: true}}
	res, err := e.r.Route(e.ctx, mergedReq(pack(1), pack(2)))
	if err != nil || res.Outcome != OutcomeAdded || !hasStep(res, codeClientMiss) {
		t.Fatalf("route %+v %v: %s", res, err, steps(res))
	}
	if got := e.researches(); len(got) != 1 || !slices.Equal(got[0], []int64{101, 102}) {
		t.Fatalf("re-searches %v", got)
	}
}

// A flagged request's season pack that freshness left one episode is decided again (the settings changed while it
// waited) with the criteria it was found with: still accepted, and still for that one episode.
func TestMergedRouteRedecideKeepsCriteria(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.pol.Profiles = map[string]*policy.Profile{"tv": {Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p"}}
	e.pol.Versions["series"].Profile = "tv"
	e.exec(`UPDATE title SET runtime_minutes = 45 WHERE id = 2`)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, air_date, air_date_utc_at, created_at, updated_at) VALUES
		(101, 2, 1, 1, 'Pilot', '2024-01-01', 1000, 0, 0), (102, 2, 1, 2, 'Second', '2024-01-02', 1000, 0, 0),
		(201, 2, 2, 1, 'Return', '2024-02-01', 1000, 0, 0), (202, 2, 2, 2, 'Again', '2024-02-02', 1000, 0, 0)`)
	eng, err := decide.New(e.pol, decide.Options{Now: func() time.Time { return e.clock }})
	if err != nil {
		t.Fatal(err)
	}
	e.r.SetProfiles(eng.Profile)
	e.r.SetDecider(eng.Decide, eng.DecideSearch)
	c := Candidate{Release: indexer.Release{Indexer: "pub", Title: "Show.S01.2160p.WEB-DL-NEW", InfoHash: h("a"),
		Size: 20 << 30, Seeders: 10, Peers: 5}, Quality: "WEBDL-2160p", Episodes: []int64{101}, Search: true,
		Origin: OriginSearch, Identity: RouteIdentityVersion,
		Criteria: []*decide.SeriesSearch{{Kind: profile.SearchSeason, Season: 1, Episodes: []int64{101, 102},
			MonitoredOnly: true, Seasons: []int{1, 2}}}}
	req := Request{TitleID: 2, VersionID: 20, Version: "series", Source: "job", Candidates: []Candidate{c},
		DecisionRev: "an older revision", Merged: true}
	res, err := e.r.Route(e.ctx, req)
	if err != nil || res.Outcome != OutcomeAdded {
		t.Fatalf("route %+v %v: %s", res, err, steps(res))
	}
	if got := e.storedEpisodes(h("a")); !slices.Equal(got, []int64{101}) {
		t.Fatalf("download episodes %v", got)
	}
}

// One SeaDex torrent under two searched seasons, on a library that prefers SeaDex: the merged search keeps season 1's
// and season 2's decision of the same hash. Season 1's opens the download; season 2's finds it held for season 1
// only. Season 2 then waits for that torrent (its episodes count as covered) instead of taking a worse release, and
// its own next search takes the torrent once the first download is done. Without SeaDex standing part 1's rule
// stays: the season falls to the next candidate.
func TestMergedSeaDexTorrentForTwoSeasons(t *testing.T) {
	t.Parallel()
	for _, standing := range []bool{true, false} {
		t.Run(fmt.Sprintf("standing=%v", standing), func(t *testing.T) {
			e := mergedEnv(t)
			e.pol.Versions["series"].PreferSeaDex = true
			if standing {
				e.exec(`INSERT INTO title_external_id VALUES(2,'anilist','1')`)
				e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','1',200,?,0,253402214400000)`,
					fmt.Sprintf(`{"anilist":1,"torrents":[{"info_hash":"%s","preference":2,"is_best":true,"facts":{"isBest":true}}]}`, h("a")))
			}
			s1, s2 := episodeCandidate("a", 101, 102), episodeCandidate("a", 201, 202)
			s1.Release.Title, s2.Release.Title = "Show S01 Bluray 1080p (sam1)", "Show S02 Bluray 1080p (sam1)"
			res, err := e.r.Route(e.ctx, mergedReq(s1, s2, pack(2)))
			if err != nil {
				t.Fatal(err)
			}
			want := []string{h("a")}
			if !standing {
				want = append(want, h("2"))
			}
			if !slices.Equal(e.ad["rd"].adds, want) {
				t.Fatalf("adds %v want %v: %s", e.ad["rd"].adds, want, steps(res))
			}
			if !standing {
				return
			}
			// Season 2's own search after the first download is done takes the torrent for season 2.
			e.exec(`UPDATE download SET state = 'imported' WHERE info_hash = ?`, h("a"))
			if res, err = e.r.Route(e.ctx, seriesReq(s2, pack(2))); err != nil {
				t.Fatal(err)
			}
			if want := []string{h("a"), h("a")}; !slices.Equal(e.ad["rd"].adds, want) {
				t.Fatalf("season 2 alone: adds %v want %v: %s", e.ad["rd"].adds, want, steps(res))
			}
			if got := e.storedEpisodes(h("a")); !slices.Equal(got, []int64{201, 202}) {
				t.Fatalf("season 2's download holds %v", got)
			}
		})
	}
}

// A deferred route's continuation resumes from its own candidate: two rows of one SeaDex torrent ("Show S01",
// "Show S02") are told apart by their release title, so season 2 does not resume with season 1's mapping.
func TestContinuationFindsItsOwnRowOfOneTorrent(t *testing.T) {
	t.Parallel()
	s1, s2 := episodeCandidate("a", 101, 102), episodeCandidate("a", 201, 202)
	s1.Release.Title, s2.Release.Title = "Show S01 Bluray 1080p (sam1)", "Show S02 Bluray 1080p (sam1)"
	got := continueFrom([]Candidate{s1, s2, pack(3)}, &s2)
	if len(got) != 2 || got[0].Release.Title != s2.Release.Title || !slices.Equal(got[0].Episodes, []int64{201, 202}) {
		t.Fatalf("continuation starts at %q %v", got[0].Release.Title, got[0].Episodes)
	}
}
