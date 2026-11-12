package router

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider/adder"
)

func episodeCandidate(hash string, episodes ...int64) Candidate {
	c := cand(h(hash), "Show."+hash+".2160p.WEB")
	c.Episodes = episodes
	return c
}

func seriesReq(cs ...Candidate) Request {
	return Request{TitleID: 2, VersionID: 20, Version: "series", Candidates: cs, Source: "test"}
}

func TestSeriesCoverage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		candidates []Candidate
		reject     string
		want       []string
	}{
		{"singles", []Candidate{episodeCandidate("a", 101), episodeCandidate("b", 102), episodeCandidate("c", 103)}, "", []string{h("a"), h("b"), h("c")}},
		{"pack", []Candidate{episodeCandidate("a", 101, 102, 103), episodeCandidate("b", 101), episodeCandidate("c", 102), episodeCandidate("d", 103)}, "", []string{h("a")}},
		{"rejected pack", []Candidate{episodeCandidate("a", 101, 102, 103), episodeCandidate("b", 101), episodeCandidate("c", 102), episodeCandidate("d", 103)}, h("a"), []string{h("b"), h("c"), h("d")}},
		{"overlapping multi episode", []Candidate{episodeCandidate("a", 101, 102), episodeCandidate("b", 102, 103), episodeCandidate("c", 103)}, "", []string{h("a"), h("c")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, dry := range []bool{false, true} {
				e := newEnv(t, true, policy.ModeActive)
				if tc.reject != "" {
					e.exec(`INSERT INTO router_blocklist (version_key, info_hash, release_title, reason, source, created_at)
						VALUES ('series', ?, 'pack', 'rejected', 'library', 0)`, tc.reject)
				}
				var res *Result
				var err error
				if dry {
					res, err = e.r.Plan(e.ctx, seriesReq(tc.candidates...))
				} else {
					res, err = e.r.Route(e.ctx, seriesReq(tc.candidates...))
				}
				if err != nil {
					t.Fatal(err)
				}
				var got []string
				for _, st := range res.Steps {
					if st.Outcome == "accepted" || st.Outcome == "would_add" {
						got = append(got, st.Hash)
					}
				}
				if !slices.Equal(got, tc.want) {
					t.Fatalf("dry=%v placements %v, want %v: %s", dry, got, tc.want, steps(res))
				}
				if dry && (e.writes() != 0 || e.count(`SELECT COUNT(*) FROM download`) != 0) {
					t.Fatal("plan wrote")
				}
				if !dry && e.count(`SELECT COUNT(*) FROM download WHERE state = 'added'`) != len(tc.want) {
					t.Fatal("placements not persisted")
				}
			}
		})
	}
}

func TestSeriesCoverageInFlight(t *testing.T) {
	t.Parallel()
	for _, state := range []string{StateQueued, StateAdding, StateAdded, StateDownloading, StateReady, StateImporting, StateFailed, StateImported} {
		t.Run(state, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, episodes, state,
				created_at, updated_at) VALUES (2, 20, 'series', ?, 'old E01', 'pub', '[101]', ?, 0, 0)`, h("f"), state)
			res, err := e.r.Route(e.ctx, seriesReq(episodeCandidate("a", 101), episodeCandidate("b", 102)))
			if err != nil {
				t.Fatal(err)
			}
			want := []string{h("b")}
			if state == StateFailed || state == StateImported {
				want = []string{h("a"), h("b")}
			}
			if !slices.Equal(e.ad["rd"].adds, want) || res.Outcome != OutcomeAdded {
				t.Fatalf("adds %v want %v: %+v", e.ad["rd"].adds, want, res)
			}
		})
	}
}

func TestSeriesCoverageProviderMissFallsBack(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	for _, ad := range e.ad {
		ad.add = func(m adder.Magnet, _ adder.AddOptions) (*adder.Added, error) {
			if m.Hash() == h("a") {
				return nil, &adder.Error{Code: adder.CodeNotCached}
			}
			return &adder.Added{ID: m.Hash()[:4], Hash: m.Hash()}, nil
		}
	}
	res, err := e.r.Route(e.ctx, seriesReq(episodeCandidate("a", 101), episodeCandidate("b", 102), episodeCandidate("c", 101)))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.ad["rd"].adds, []string{h("a"), h("b"), h("c")}) ||
		!slices.Equal(e.ad["ad"].adds, []string{h("a")}) || !slices.Equal(e.ad["tb"].adds, []string{h("a")}) ||
		e.count(`SELECT COUNT(*) FROM download WHERE state = 'added'`) != 2 || res.Release != "Show.b.2160p.WEB" {
		t.Fatalf("adds rd=%v ad=%v tb=%v: %+v", e.ad["rd"].adds, e.ad["ad"].adds, e.ad["tb"].adds, res)
	}
}

func TestSeriesCoverageDeferredBatch(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["series"].Providers = []string{"rd"}
	e.pol.Providers["rd"].Pacing = policy.Pacing{AddsPerMinute: 1}
	q := jobs.NewQueue(e.st)
	q.SetClock(func() time.Time { return e.clock })
	w := jobs.NewWorker(q, jobs.WorkerOptions{})
	e.r.RegisterJobs(w, q)
	req := seriesReq(episodeCandidate("a", 101), episodeCandidate("b", 102), episodeCandidate("c", 103), episodeCandidate("d", 102))
	plan, err := e.r.Plan(e.ctx, req)
	if err != nil || plan.Outcome != OutcomeDeferred || plan.Hash != h("b") {
		t.Fatalf("plan %+v %v", plan, err)
	}
	if _, err := e.r.Enqueue(e.ctx, req, time.Time{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		ran, err := w.RunOne(e.ctx)
		if err != nil || !ran {
			t.Fatalf("round %d ran=%v err=%v", i, ran, err)
		}
		e.clock = e.clock.Add(time.Minute)
	}
	if !slices.Equal(e.ad["rd"].adds, []string{h("a"), h("b"), h("c")}) ||
		e.count(`SELECT COUNT(*) FROM download`) != 3 || e.count(`SELECT COUNT(*) FROM download WHERE state = 'added'`) != 3 {
		t.Fatalf("adds %v downloads %d", e.ad["rd"].adds, e.count(`SELECT COUNT(*) FROM download`))
	}
	if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = ? AND state = 'queued'`, JobRoute); n != 0 {
		t.Fatalf("pending %d", n)
	}
}

func TestMovieCoverageStillOnePlacement(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.A"), cand(h("b"), "Movie.B")))
	if err != nil || res.Outcome != OutcomeAdded || !slices.Equal(e.ad["rd"].adds, []string{h("a")}) {
		t.Fatalf("adds %v result %+v err %v", e.ad["rd"].adds, res, err)
	}
}

func TestShadowSeriesCoverageRecorded(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeShadow)
	res, err := e.r.Route(e.ctx, seriesReq(episodeCandidate("a", 101), episodeCandidate("b", 102), episodeCandidate("c", 103)))
	if err != nil || res.PlanID == 0 || e.writes() != 0 {
		t.Fatalf("result %+v err %v writes %d", res, err, e.writes())
	}
	var js string
	if err := e.st.Reader().QueryRow(`SELECT steps FROM router_plan WHERE id = ?`, res.PlanID).Scan(&js); err != nil {
		t.Fatal(err)
	}
	var got []Step
	if err := json.Unmarshal([]byte(js), &got); err != nil {
		t.Fatal(err)
	}
	if n := len(slices.DeleteFunc(got, func(s Step) bool { return s.Outcome != "would_add" })); n != 3 {
		t.Fatalf("planned %d placements: %s", n, js)
	}
}

// Placements do not count against max_candidates: a season of single-episode releases fills in one route.
func TestSeriesCoverageCandidateBudget(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.r.set.MaxCandidates = 2
	res, err := e.r.Route(e.ctx, seriesReq(episodeCandidate("a", 101), episodeCandidate("b", 102), episodeCandidate("c", 103)))
	if err != nil || res.Outcome != OutcomeAdded || !slices.Equal(e.ad["rd"].adds, []string{h("a"), h("b"), h("c")}) {
		t.Fatalf("adds %v result %+v err %v", e.ad["rd"].adds, res, err)
	}
}

func TestSeriesCoverageUnsettledAddReservesEpisodes(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	// A transport failure does not prove the provider refused the add.
	e.ad["rd"].add = func(adder.Magnet, adder.AddOptions) (*adder.Added, error) {
		return nil, &adder.Error{Code: adder.CodeUnavailable, Status: 503}
	}
	res, err := e.r.Route(e.ctx, seriesReq(episodeCandidate("a", 101), episodeCandidate("b", 101), episodeCandidate("c", 102)))
	if err != nil || res.Outcome != OutcomeDeferred || res.Hash != h("a") {
		t.Fatalf("result %+v err %v", res, err)
	}
	if !slices.Equal(e.ad["rd"].adds, []string{h("a")}) || !slices.Equal(e.ad["ad"].adds, []string{h("c")}) ||
		e.count(`SELECT COUNT(*) FROM download WHERE state='adding'`) != 1 || e.count(`SELECT COUNT(*) FROM download WHERE state='added'`) != 1 {
		t.Fatalf("rd adds %v ad adds %v: %s", e.ad["rd"].adds, e.ad["ad"].adds, steps(res))
	}
}
