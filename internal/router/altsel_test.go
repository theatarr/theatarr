package router

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// altEnv is newDebridEnv with cached_only on, a Real-Debrid movie of a main video, an extra and an nfo, and a sleep
// that moves the clock (altRetry waits for min_gap on it). It returns the sleeps.
func altEnv(t *testing.T) (*env, *fakeDebrid, *[]time.Duration) {
	t.Helper()
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
	f.rdFiles = []rdFile{{1, "/Movie/Movie.2024.2160p.WEB.H265-GRP.nfo", 2},
		{2, "/Movie/Movie.2024.2160p.WEB.H265-GRP.mkv", 9000}, {3, "/Movie/Featurettes/Making.Of.mkv", 800}}
	f.rdCachedSel = map[string]bool{}
	slept := &[]time.Duration{}
	e.r.sleep = func(_ context.Context, d time.Duration) error {
		*slept = append(*slept, d)
		e.clock = e.clock.Add(d)
		return nil
	}
	return e, f, slept
}

// Real-Debrid caches per selected set: a movie's every playable file (2,3) uncached and its largest video alone
// (2) cached is added a second time with that set, after the provider's min_gap, and accepted. The first add was
// deleted (ours) and counted uncached; the nfo is never selected.
func TestCachedOnlyAltSelection(t *testing.T) {
	t.Parallel()
	e, f, slept := altEnv(t)
	gap := policy.Duration(20 * time.Second)
	e.pol.Providers["rd"].Pacing.MinGap = &gap
	f.rdCachedSel[h("a")+"|2"] = true
	res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || res.Provider != "rd" || res.TorrentID != "RD1002" {
		t.Fatalf("%+v: %s", res, steps(res))
	}
	if got := steps(res); got != "aaaa/rd/refused/uncached_deleted; aaaa/rd/accepted/added; " {
		t.Errorf("steps %s", got)
	}
	if !strings.Contains(res.Steps[0].Message, "largest video alone") {
		t.Errorf("first step: %s", res.Steps[0].Message)
	}
	if f.count("rd.test POST /rest/1.0/torrents/addMagnet") != 2 ||
		f.count("rd.test DELETE /rest/1.0/torrents/delete/RD1001") != 1 || len(f.rd) != 1 {
		t.Errorf("rd requests %v", f.list())
	}
	if got := f.rd["RD1002"].sel; got != "2" {
		t.Errorf("second selection %q", got)
	}
	// The second add waited out min_gap since the first (the 2s graces count toward it).
	var waited time.Duration
	for _, d := range *slept {
		if d != 2*time.Second {
			waited += d
		}
	}
	if waited <= 0 || waited > 20*time.Second {
		t.Errorf("sleeps %v", *slept)
	}
	if p, _ := loadPace(e.ctx, e.st.Reader(), "rd"); p.HourUncached != 1 || p.WindowAdds != 2 {
		t.Errorf("rd pacing: uncached %d, adds %d", p.HourUncached, p.WindowAdds)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'ready' AND cached = 1 AND provider = 'rd'
		AND provider_torrent_id = 'RD1002'`, res.DownloadID); n != 1 {
		t.Error("download not on the second add")
	}
	if st, _ := e.r.cacheState(e.ctx, h("a"), "rd", e.clock); st != "cached" {
		t.Errorf("rd cache state %q", st)
	}
}

// Both sets uncached: two adds, both deleted and counted, then the next provider; never a third add. A series
// (no other set holds its episodes) and a movie with one playable file get no second add.
func TestCachedOnlyAltSelectionOnce(t *testing.T) {
	t.Parallel()
	e, f, _ := altEnv(t)
	f.cached["ad:"+h("a")] = true
	res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(res); res.Provider != "ad" ||
		got != "aaaa/rd/refused/uncached_deleted; aaaa/rd/refused/uncached_deleted; aaaa/ad/accepted/added; " {
		t.Fatalf("%+v: %s", res, got)
	}
	if strings.Contains(res.Steps[1].Message, "largest video alone") {
		t.Errorf("a third add was announced: %s", res.Steps[1].Message)
	}
	if f.count("rd.test POST /rest/1.0/torrents/addMagnet") != 2 || f.count("rd.test DELETE") != 2 || len(f.rd) != 0 {
		t.Errorf("rd requests %v", f.list())
	}
	if p, _ := loadPace(e.ctx, e.st.Reader(), "rd"); p.HourUncached != 2 {
		t.Errorf("rd uncached adds %d", p.HourUncached)
	}
	if st, _ := e.r.cacheState(e.ctx, h("a"), "rd", e.clock); st != "uncached" {
		t.Errorf("rd cache state %q", st)
	}

	e.clock = e.clock.Add(15 * time.Minute) // past adds_per_minute and its cooldown
	f.cached["ad:"+h("b")] = true
	res, err = e.r.Route(e.ctx, seriesReq(cand(h("b"), "Show.S01.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(res); got != "bbbb/rd/refused/uncached_deleted; bbbb/ad/accepted/added; " {
		t.Errorf("series: %s", got)
	}
	f.rdFiles = f.rdFiles[:2] // the nfo and the main video
	e.clock = e.clock.Add(15 * time.Minute)
	f.cached["ad:"+h("c")] = true
	res, err = e.r.Route(e.ctx, grabReq(cand(h("c"), "Movie.2024.2160p.WEB.H265-OTHER")))
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(res); got != "cccc/rd/refused/uncached_deleted; cccc/ad/accepted/added; " {
		t.Errorf("one playable file: %s", got)
	}
}

// A provider paced past altPaceWait (here uncached_adds_per_hour used up by the first add) gets no second add: the
// route says so and moves on.
func TestCachedOnlyAltSelectionPaced(t *testing.T) {
	t.Parallel()
	e, f, _ := altEnv(t)
	e.pol.Providers["rd"].Pacing.UncachedPerHr = 1
	f.rdCachedSel[h("a")+"|2"] = true
	f.cached["ad:"+h("a")] = true
	res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(res); res.Provider != "ad" ||
		got != "aaaa/rd/refused/uncached_deleted; aaaa/rd/skipped/alt_selection_skipped; aaaa/ad/accepted/added; " {
		t.Fatalf("%+v: %s", res, got)
	}
	if f.count("rd.test POST /rest/1.0/torrents/addMagnet") != 1 {
		t.Errorf("rd requests %v", f.list())
	}
}
