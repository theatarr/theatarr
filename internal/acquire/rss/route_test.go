package rss

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
	"github.com/theatarr/theatarr/internal/router/routertest"
)

// withRouter rebuilds the runner with a router over logging fake adders (router.enabled true, so only the versions'
// modes keep it from writing), only the given versions Live (with grab on; the others Watch only), and TorBox added
// after Real-Debrid for every debrid version (TorBox has no name rule, so a WEB-DL can be placed).
func (e *env) withRouter(t *testing.T, active ...string) (*routertest.Fake, *jobs.Queue, *jobs.Worker) {
	t.Helper()
	e.pol.Router = &policy.Router{Enabled: true}
	e.pol.Providers["tb"] = &policy.Provider{Kind: provider.KindTorBox, Order: 2, URL: "https://api.torbox.app/v1",
		APIKeyEnv: "THEATARR_TB_KEY", Pacing: policy.Pacing{AddsPerMinute: 100}}
	for _, v := range e.pol.Versions {
		v.Mode = ""
		if v.Storage == policy.StorageDebrid {
			v.Providers = []string{"rd", "tb"}
		}
	}
	for _, k := range active {
		e.pol.Versions[k].Mode = policy.ModeActive
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

// With movie-web Live, RSS picks for it go to the router as route jobs (the router adds them); movie-remux is Watch
// only and gets nothing.
func TestActiveRSSRoutes(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	rt, _, w := e.withRouter(t, "movie-web")
	if got := e.r.ActiveVersions(); !slices.Equal(got, []string{"movie-web"}) {
		t.Fatalf("active %v", got)
	}
	e.seed(t)
	e.prow.setRSS(item{title: alienWebRel, hash: hAlienWeb, size: 20e9, pub: t0.Add(-10 * time.Minute)},
		item{title: "Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-NTb", hash: "7777777777777777777777777777777777777777",
			size: 25e9, pub: t0})
	rs, err := e.r.RSS(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Routed != 2 {
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
		t.Errorf("%d downloads for Watch-only versions", n)
	}
	// The next read with the same titles in flight routes nothing more.
	e.prow.setRSS(item{title: "Alien.Romulus.2024.2160p.AMZN.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX", hash: strings.Repeat("8", 40),
		size: 20e9, pub: t0})
	if rs, err = e.r.RSS(e.ctx); err != nil || rs.Routed != 0 || rs.RouteSkipped != 1 {
		t.Errorf("in flight %+v %v", rs, err)
	}
}

// A Live series version gets one route for a read's items of a title: the episodes, or the pack that covers them.
func TestRSSSeriesBatchCoverage(t *testing.T) {
	t.Parallel()
	for _, pack := range []bool{false, true} {
		t.Run(fmt.Sprintf("pack=%v", pack), func(t *testing.T) {
			e := newEnv(t, false)
			rt, _, w := e.withRouter(t, "tv")
			e.seed(t)
			e.seedShow(t)
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
			if rs.Routed != 1 || rt.Writes() != want || len(rt.Adders["tb"].Adds()) != want {
				t.Fatalf("rss %+v adds %v writes %d, want %d", rs, rt.Adders["tb"].Adds(), rt.Writes(), want)
			}
		})
	}
}

func TestRSSSeriesPartiallyInFlight(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	rt, _, w := e.withRouter(t, "tv")
	e.seed(t)
	e.seedShow(t)
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
