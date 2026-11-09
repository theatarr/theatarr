package shadow

import (
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/router/routertest"
)

// withRouter rebuilds the runner with a router over logging fake adders (router.enabled true, so only the versions'
// modes keep it from writing), the given versions active, and TorBox added after Real-Debrid for every debrid
// version (TorBox has no name rule, so a WEB-DL can be placed).
func (e *env) withRouter(t *testing.T, active ...string) (*routertest.Fake, *jobs.Queue, *jobs.Worker) {
	t.Helper()
	e.pol.Router = &policy.Router{Enabled: true}
	e.pol.Providers["tb"] = &policy.Provider{Kind: provider.KindTorBox, Order: 2, URL: "https://api.torbox.app/v1",
		APIKeyEnv: "THEATARR_TB_KEY", Pacing: policy.Pacing{AddsPerMinute: 100}}
	for _, v := range e.pol.Versions {
		if v.Storage == policy.StorageDebrid {
			v.Providers = []string{"rd", "tb"}
		}
	}
	for _, k := range active {
		e.pol.Versions[k].Mode, e.pol.Versions[k].Grab = policy.ModeActive, true
	}
	q := jobs.NewQueue(e.st)
	q.SetClock(e.clock)
	w := jobs.NewWorker(q, jobs.WorkerOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	rt, err := routertest.New(e.st, e.pol, q, w, e.clock)
	if err != nil {
		t.Fatal(err)
	}
	e.build(rt)
	return rt, q, w
}

// Every shadow grab decision and RSS pick gets the router's dry-run plan, shown in the diff rows and detail; the
// router's fake adders see no call and no download is opened, though the kill switch is on.
func TestShadowPlansNeverWrite(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	rt, q, _ := e.withRouter(t)
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.prow.set(dune.imdb, item{title: duneRemux, hash: hDuneRemux, size: 70e9, pub: t0.Add(-48 * time.Hour)})
	e.prow.set(argylle.imdb, item{title: argWeb, hash: hArgWeb, size: 20e9, pub: t0.Add(-72 * time.Hour)})
	e.dmm.set(argylle.imdb, item{title: argRemux, hash: hArgRemux, size: 60e9, pub: t0.Add(-96 * time.Hour)})
	e.zurg.add(grabEvent(2, dune, t0.Add(-time.Hour), duneRemux, hDuneRemux, "Remux-2160p", 31, 1950, "Rss"))
	e.zurg.add(grabEvent(3, argylle, t0.Add(-50*time.Minute), argWeb, hArgWeb, "WEBDL-2160p", 18, 1700, "Search"))
	e.zurg.add(grabEvent(4, wonka, t0.Add(-40*time.Minute), "Wonka.2023.2160p.WEB-DL.DDP5.1-YTS", hWonkaYTS,
		"WEBDL-2160p", 18, -10000, "InteractiveSearch"))
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.prow.setRSS(item{title: alienWebRel, hash: hAlienWeb, size: 20e9, pub: t0.Add(-10 * time.Minute)})
	if rs, err := e.r.RSS(e.ctx); err != nil || rs.Picks != 2 || rs.Routed != 0 {
		t.Fatalf("rss %+v %v", rs, err)
	}

	if n := one[int](t, e.st, `SELECT COUNT(*) FROM shadow_grab WHERE state = 'done' AND router_plan_id IS NULL`); n != 0 {
		t.Errorf("%d grabs without a plan", n)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM shadow_rss_pick WHERE router_plan_id IS NULL`); n != 0 {
		t.Errorf("%d picks without a plan", n)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM router_plan WHERE mode = 'shadow'`); n != 5 {
		t.Errorf("%d shadow plans, want 3 grabs + 2 picks", n)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM router_plan WHERE mode != 'shadow'`); n != 0 {
		t.Errorf("%d plans not in shadow mode", n)
	}
	rows, _, err := ListDiffs(e.ctx, e.st.Reader(), DiffFilter{})
	if err != nil || len(rows) != 3 {
		t.Fatalf("diffs %d %v", len(rows), err)
	}
	for _, d := range rows {
		det, err := LoadDiff(e.ctx, e.st.Reader(), d.ID)
		if err != nil {
			t.Fatal(err)
		}
		p := det.Grab.Plan
		if p == nil || p.Mode != "shadow" || p.Summary == "" || d.RouterPlan != p.Summary {
			t.Fatalf("grab %d plan %+v, row %q", det.Grab.HistoryID, p, d.RouterPlan)
		}
		if det.Grab.HistoryID == 2 && p.Summary != "would add "+duneRemux+" on rd" {
			t.Errorf("dune plan %q", p.Summary)
		}
		// Real-Debrid refuses WEB-DL names; TorBox would take it.
		if det.Grab.HistoryID == 3 && !strings.Contains(p.Summary, "on tb") && !strings.Contains(p.Summary, "on rd") {
			t.Errorf("argylle plan %q", p.Summary)
		}
	}
	var pickID int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM shadow_rss_pick WHERE version_key = 'movie-web'`).Scan(&pickID); err != nil {
		t.Fatal(err)
	}
	if p, err := loadPick(e.ctx, e.st.Reader(), pickID); err != nil || p.Plan == nil ||
		p.Plan.Summary != "would add "+alienWebRel+" on tb" || !slices.ContainsFunc(p.Plan.Steps, func(s router.Step) bool {
		return s.Provider == "rd" && s.Code == "rd_name_rule"
	}) {
		t.Errorf("pick plan %+v %v", p.Plan, err)
	}

	if rt.Writes() != 0 {
		t.Errorf("%d provider writes for shadow versions", rt.Writes())
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM download`); n != 0 {
		t.Errorf("%d downloads", n)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM job WHERE kind = ?`, router.JobRoute); n != 0 || q == nil {
		t.Errorf("%d route jobs", n)
	}
}

// With movie-web active, RSS picks for it go to the router as route jobs (the router adds them); movie-remux stays
// in shadow mode and gets a pick with a plan, and no write.
func TestActiveRSSRoutes(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	rt, _, w := e.withRouter(t, "movie-web")
	if got := e.r.Versions(); slices.Contains(got, "movie-web") {
		t.Fatalf("movie-web shadowed: %v", got)
	}
	if got := e.r.ActiveVersions(); !slices.Equal(got, []string{"movie-web"}) {
		t.Fatalf("active %v", got)
	}
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.prow.setRSS(item{title: alienWebRel, hash: hAlienWeb, size: 20e9, pub: t0.Add(-10 * time.Minute)},
		item{title: "Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-NTb", hash: "7777777777777777777777777777777777777777",
			size: 25e9, pub: t0})
	rs, err := e.r.RSS(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Picks != 1 || rs.Routed != 2 {
		t.Fatalf("rss %+v", rs)
	}
	for {
		ran, err := w.RunOne(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			break
		}
	}
	adds := rt.Adders["tb"].Adds()
	if len(adds) != 2 || rt.Writes() != 2 {
		t.Fatalf("adds %v, writes %d", adds, rt.Writes())
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM download WHERE version_key = 'movie-web' AND source = 'rss' AND state = 'added'`); n != 2 {
		t.Errorf("movie-web downloads %d", n)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM download WHERE version_key != 'movie-web'`); n != 0 {
		t.Errorf("%d downloads for shadow versions", n)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM shadow_rss_pick WHERE version_key = 'movie-remux' AND router_plan_id IS NOT NULL`); n != 1 {
		t.Errorf("movie-remux picks with a plan %d", n)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM shadow_rss_pick WHERE version_key = 'movie-web'`); n != 0 {
		t.Errorf("active version picks in the shadow report: %d", n)
	}
	// The next read with the same titles in flight routes nothing more.
	e.prow.setRSS(item{title: "Alien.Romulus.2024.2160p.AMZN.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX", hash: strings.Repeat("8", 40),
		size: 20e9, pub: t0})
	if rs, err = e.r.RSS(e.ctx); err != nil || rs.Routed != 0 || rs.RouteSkipped != 1 {
		t.Errorf("in flight %+v %v", rs, err)
	}
}

func TestRSSSeriesBatchCoverage(t *testing.T) {
	t.Parallel()
	for _, active := range []bool{false, true} {
		for _, pack := range []bool{false, true} {
			t.Run(fmt.Sprintf("active=%v/pack=%v", active, pack), func(t *testing.T) {
				e := newEnv(t, false)
				newShow(e.tv)
				var keys []string
				if active {
					keys = []string{"tv"}
				}
				rt, _, w := e.withRouter(t, keys...)
				if _, err := e.r.Import(e.ctx); err != nil {
					t.Fatal(err)
				}
				items := []item{
					{title: "The.Show.S01E02.2160p.WEB-DL-GRP", hash: strings.Repeat("a", 40), size: 8e9, pub: t0, tv: true},
					{title: "The.Show.S01E03.2160p.WEB-DL-GRP", hash: strings.Repeat("b", 40), size: 8e9, pub: t0, tv: true},
				}
				want := 2
				if pack {
					items = append(items, item{title: showPack, hash: hShowPack, size: 24e9, pub: t0, tv: true})
					want = 1
				}
				e.prow.setRSS(items...)
				rs, err := e.r.RSS(e.ctx)
				if err != nil {
					t.Fatal(err)
				}
				for {
					ran, err := w.RunOne(e.ctx)
					if err != nil {
						t.Fatal(err)
					}
					if !ran {
						break
					}
				}
				if active {
					if rs.Routed != 1 || rt.Writes() != want || len(rt.Adders["tb"].Adds()) != want {
						t.Fatalf("rss %+v adds %v writes %d, want %d", rs, rt.Adders["tb"].Adds(), rt.Writes(), want)
					}
				} else {
					if rs.Picks != want || rt.Writes() != 0 {
						t.Fatalf("rss %+v writes %d, want %d picks", rs, rt.Writes(), want)
					}
					if n := one[int](t, e.st, `SELECT COUNT(DISTINCT router_plan_id) FROM shadow_rss_pick WHERE version_key='tv'`); n != 1 {
						t.Fatalf("plans %d, want one shared coverage plan", n)
					}
					var planSteps string
					if err := e.st.Reader().QueryRow(`SELECT steps FROM router_plan WHERE version_key='tv'`).Scan(&planSteps); err != nil {
						t.Fatal(err)
					}
					if n := strings.Count(planSteps, `"outcome":"would_add"`); n != want {
						t.Fatalf("plan has %d placements, want %d: %s", n, want, planSteps)
					}
				}
			})
		}
	}
}

func TestRSSSeriesPartiallyInFlight(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	newShow(e.tv)
	rt, _, w := e.withRouter(t, "tv")
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer().Exec(`INSERT INTO download (title_id, version_id, version_key, info_hash,
		release_title, indexer, episodes, state, created_at, updated_at)
		SELECT v.title_id, v.id, 'tv', ?, 'E02 in flight', 'prowlarr', json_array(e.id), 'added', 0, 0
		FROM version v JOIN episode e ON e.title_id=v.title_id WHERE v.policy_key='tv' AND e.number=2`, strings.Repeat("f", 40)); err != nil {
		t.Fatal(err)
	}
	e.prow.setRSS(
		item{title: "The.Show.S01E02.2160p.WEB-DL-GRP", hash: strings.Repeat("a", 40), size: 8e9, pub: t0, tv: true},
		item{title: "The.Show.S01E03.2160p.WEB-DL-GRP", hash: strings.Repeat("b", 40), size: 8e9, pub: t0, tv: true},
	)
	rs, err := e.r.RSS(e.ctx)
	if err != nil || rs.Routed != 1 || rs.RouteSkipped != 0 {
		t.Fatalf("rss %+v err %v", rs, err)
	}
	if ran, err := w.RunOne(e.ctx); err != nil || !ran {
		t.Fatalf("route ran=%v err=%v", ran, err)
	}
	if adds := rt.Adders["tb"].Adds(); !slices.Equal(adds, []string{strings.Repeat("b", 40)}) {
		t.Fatalf("adds %v", adds)
	}
}

func TestRSSSeriesOfflineCoverage(t *testing.T) {
	t.Parallel()
	for _, pack := range []bool{false, true} {
		t.Run(fmt.Sprint(pack), func(t *testing.T) {
			e := newEnv(t, false)
			newShow(e.tv)
			if _, err := e.r.Import(e.ctx); err != nil {
				t.Fatal(err)
			}
			items := []item{
				{title: "The.Show.S01E02.2160p.WEB-DL-GRP", hash: strings.Repeat("a", 40), size: 8e9, pub: t0, tv: true},
				{title: "The.Show.S01E03.2160p.WEB-DL-GRP", hash: strings.Repeat("b", 40), size: 8e9, pub: t0, tv: true},
			}
			want := 2
			if pack {
				items = append(items, item{title: showPack, hash: hShowPack, size: 24e9, pub: t0, tv: true})
				want = 1
			}
			e.prow.setRSS(items...)
			rs, err := e.r.RSS(e.ctx)
			if err != nil || rs.Picks != want {
				t.Fatalf("rss %+v err %v, want %d picks", rs, err, want)
			}
		})
	}
}
