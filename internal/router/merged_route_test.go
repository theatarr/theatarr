package router

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider/adder"
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
}
