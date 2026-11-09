package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
)

// A finished AllDebrid download read by the real client (its files carry no id) is ready, not "no
// video file" and blocklisted.
func TestAllDebridFinishedIsReady(t *testing.T) {
	t.Parallel()
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
	e.pol.Versions["movie"].Providers = []string{"ad"}
	f.cached["ad:"+h("a")] = true
	res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.Provider != "ad" {
		t.Fatalf("route %+v %v", res, err)
	}
	sum, err := e.r.Poll(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var state, why string
	if err := e.st.Reader().QueryRow(`SELECT state, COALESCE(error,'') FROM download WHERE id=?`,
		res.DownloadID).Scan(&state, &why); err != nil {
		t.Fatal(err)
	}
	if state != StateReady || e.count(`SELECT COUNT(*) FROM router_blocklist`) != 0 {
		t.Fatalf("finished AllDebrid MKV: state=%s reason=%s poll=%+v blocklist=%d", state, why, sum,
			e.count(`SELECT COUNT(*) FROM router_blocklist`))
	}
}

// AllDebrid and TorBox take every file, so a finished DVD (.vob, no file ids) is a video there; only
// Real-Debrid's selection leaves .vob out.
func TestOtherProvidersKeepVOB(t *testing.T) {
	t.Parallel()
	for _, pk := range []string{"ad", "tb"} {
		t.Run(pk, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			e.exec(`INSERT INTO download (id,title_id,version_id,version_key,info_hash,release_title,indexer,provider,
				provider_torrent_id,state,created_at,updated_at) VALUES (99,1,10,'movie',?,'Movie.2024.DVD-GRP','pub',?,
				'T99','added',0,0)`, h("a"), pk)
			e.pr[pk].set("T99", provider.Torrent{ID: "T99", Hash: h("a"), Status: "downloaded",
				State: provider.StateDownloaded, Progress: 100, Files: []provider.File{
					{Path: "Movie/VIDEO_TS/VTS_01_1.VOB", Bytes: 9000000000, Selected: true},
					{Path: "Movie/VIDEO_TS/VIDEO_TS.IFO", Bytes: 9000, Selected: true}}})
			if _, err := e.r.Poll(e.ctx); err != nil {
				t.Fatal(err)
			}
			var state, why string
			if err := e.st.Reader().QueryRow(`SELECT state, COALESCE(error,'') FROM download WHERE id=99`).
				Scan(&state, &why); err != nil {
				t.Fatal(err)
			}
			if state != StateReady {
				t.Fatalf("%s DVD: state=%s reason=%s", pk, state, strings.TrimSpace(why))
			}
		})
	}
}

func TestHasVideo(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		paths []string
		want  bool
	}{
		{[]string{"a/Movie.mkv"}, true},
		{[]string{"a/VTS_01_1.VOB"}, true},
		{[]string{"Movie.iso"}, true},
		{[]string{"a/sample.mkv"}, true},
		{[]string{"a/x.nfo", "a/x.flac"}, false},
		{[]string{"RARBG.com.mp4", "x.txt"}, false},
	} {
		var fs []provider.File
		for _, p := range c.paths {
			fs = append(fs, provider.File{Path: p})
		}
		if got := HasVideo(fs); got != c.want {
			t.Errorf("HasVideo(%v) = %t, want %t", c.paths, got, c.want)
		}
	}
}

// AllDebrid in maintenance answers every read with the MAINTENANCE envelope on HTTP 200. That is the
// provider down, not an answer about the torrents: past the stall limit its downloads end unavailable, not stalled
// and blocklisted (the same as the answer on HTTP 503).
func TestAllDebridMaintenanceIsOutage(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			e, f, _ := newDebridEnv(t, true, policy.ModeActive, false)
			e.pol.Versions["movie"].Providers = []string{"ad"}
			for _, c := range []string{"a", "b", "c", "d"} {
				res, err := e.r.Route(e.ctx, grabReq(cand(h(c), "Movie.2024.2160p.WEB.H265-GRP"+c)))
				if err != nil || res.Provider != "ad" {
					t.Fatalf("route %+v %v", res, err)
				}
			}
			e.exec(`UPDATE download SET state = 'added'`)
			f.mu.Lock()
			f.adMaint = status
			f.mu.Unlock()
			for range 6 {
				e.clock = e.clock.Add(2 * time.Hour)
				if _, err := e.r.Poll(e.ctx); err != nil {
					t.Fatal(err)
				}
			}
			if n := e.count(`SELECT COUNT(*) FROM download WHERE state IN ('added', 'downloading')`); n != 0 {
				t.Fatalf("%d downloads still in flight: the outage did not reach the stall limit", n)
			}
			if n := e.count(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
				t.Errorf("%d releases blocklisted during the AllDebrid outage", n)
			}
			if n := e.count(`SELECT COUNT(*) FROM download WHERE error LIKE 'stalled%' AND state = 'failed'`); n != 0 {
				t.Errorf("%d downloads failed as stalled during the outage", n)
			}
		})
	}
}

// The second selection's intent is recorded with its torrent. When the route's own selection of the
// second add fails once (a 503), the poller selects the largest video alone (2), never every playable file (2,3)
// Real-Debrid just answered uncached for; so does a new router over the same database (a restart).
func TestAltSelectionSurvivesFailedSelect(t *testing.T) {
	t.Parallel()
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprint("restart=", restart), func(t *testing.T) {
			e, f, _ := altEnv(t)
			f.rdCachedSel[h("a")+"|2"] = true
			failed := false
			f.serve("rd.test", func(w http.ResponseWriter, r *http.Request) {
				if !failed && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/selectFiles/RD1002") {
					failed = true
					writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "service_unavailable",
						"error_code": 5})
					return
				}
				f.realDebrid(w, r)
			})
			res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
			if err != nil {
				t.Fatal(err)
			}
			if !failed || res.TorrentID != "RD1002" {
				t.Fatalf("not on the second add: %+v failed=%t: %s", res, failed, steps(res))
			}
			if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND select_alt_torrent = 'RD1002'`,
				res.DownloadID); n != 1 {
				t.Fatal("the second add's selection is not recorded with its torrent")
			}
			r := e.r
			if restart {
				var err error
				if r, err = New(Config{Store: e.st, Policy: e.pol, Adders: e.r.adders, Providers: e.r.provs,
					Logger: e.r.log, Now: func() time.Time { return e.clock }}); err != nil {
					t.Fatal(err)
				}
				r.sleep = e.r.sleep
			}
			for range 2 {
				if _, err := r.Poll(e.ctx); err != nil {
					t.Fatal(err)
				}
			}
			if got := f.rd["RD1002"].sel; got != "2" {
				state, _ := r.downloadState(e.ctx, res.DownloadID)
				t.Fatalf("the poller selected %q, want the second selection 2 (download %s)", got, state)
			}
		})
	}
}

// altRetry waits for the provider's pacing without the add lock.
func TestAltRetryWaitsWithoutLock(t *testing.T) {
	t.Parallel()
	e, f, _ := altEnv(t)
	gap := policy.Duration(20 * time.Second)
	e.pol.Providers["rd"].Pacing.MinGap = &gap
	f.rdCachedSel[h("a")+"|2"] = true
	free := false
	e.r.sleep = func(_ context.Context, d time.Duration) error {
		if d != 2*time.Second { // not a cached grace: the min_gap wait
			if e.r.mu.TryLock() {
				free = true
				e.r.mu.Unlock()
			}
		}
		e.clock = e.clock.Add(d)
		return nil
	}
	res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.TorrentID != "RD1002" {
		t.Fatalf("%+v %v", res, err)
	}
	if !free {
		t.Error("the add lock was held during the second selection's pacing wait")
	}
}

// Rd holds the release (a registry entry) and its read of that torrent is paced (a 429, the gateway's
// budget): the whole release waits instead of being added to ad, which would leave rd's torrent unreferenced. A read
// that fails as the provider being down still defers rd alone and the route goes on to ad.
func TestPacedHeldReadDefersRelease(t *testing.T) {
	for _, c := range []struct {
		name    string
		err     error
		wantAdd bool
	}{
		{"429", &provider.APIError{Provider: "rd", Status: http.StatusTooManyRequests}, false},
		{"budget", &gateway.Refused{Reason: gateway.ReasonBudget}, false},
		{"down", errors.New("rd: connection refused"), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			e.pol.Versions["movie"].Providers = []string{"rd", "ad"}
			e.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (7, ?, 'present', 0, 0)`,
				h("a"))
			e.exec(`INSERT INTO content_provider (content_id, provider, torrent_id, seen_at) VALUES (7, 'rd', 't1', 0)`)
			e.pr["rd"].set("t1", provider.Torrent{Hash: h("a"), State: provider.StateDownloaded, Status: "downloaded"})
			e.pr["rd"].readErr = map[string]error{"t1": c.err}
			res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
			if err != nil {
				t.Fatal(err)
			}
			if got := len(e.ad["ad"].adds) > 0; got != c.wantAdd {
				t.Fatalf("added to ad: %t, want %t: %s", got, c.wantAdd, steps(res))
			}
			if !c.wantAdd && e.count(`SELECT COUNT(*) FROM download WHERE state = 'queued'`) != 1 {
				t.Fatalf("the release does not wait: %s", steps(res))
			}
		})
	}
}

// An add cut by a cancelled context records no provider failure, so no
// down back-off blocks reuse on that provider afterwards. The route returns before classify (router.go: "Interrupted
// mid-add").
func TestCancelledAddNoBackoff(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	ctx, cancel := context.WithCancel(e.ctx)
	e.ad["rd"].add = func(adder.Magnet, adder.AddOptions) (*adder.Added, error) {
		cancel()
		return nil, context.Canceled
	}
	if _, err := e.r.Route(ctx, movieReq(cand(h("a"), "A.2160p"))); err == nil {
		t.Fatal("an interrupted add reported success")
	}
	p, err := loadPace(e.ctx, e.st.Reader(), "rd")
	if err != nil {
		t.Fatal(err)
	}
	if p.BackoffDown || !p.BackoffUntil.IsZero() || p.Failures != 0 {
		t.Errorf("rd backed off after a cancelled add: %+v", p)
	}
}
