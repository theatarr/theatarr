package router

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/store"
)

// rdServer is a Real-Debrid API on loopback: broken torrents answer 503, the others downloading; the account
// endpoint answers unless accountDown.
type rdServer struct {
	mu          sync.Mutex
	broken      map[string]bool
	accountDown bool
	accountHits int
}

func (s *rdServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch p := strings.TrimPrefix(r.URL.Path, "/rest/1.0"); {
	case p == "/user":
		s.accountHits++
		if s.accountDown {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, `{"type":"premium","premium":86400,"points":10}`)
	case p == "/torrents/activeCount":
		io.WriteString(w, `{"nb":0,"limit":100}`)
	case strings.HasPrefix(p, "/torrents/info/"):
		id := strings.TrimPrefix(p, "/torrents/info/")
		if s.broken[id] {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, `{"id":%q,"filename":"Movie","hash":%q,"bytes":1000,"progress":50,"status":"downloading"}`,
			id, rdHash(id))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func rdHash(id string) string { return fmt.Sprintf("%040s", strings.TrimPrefix(id, "t")) }

// withRealRD replaces the fake RD client with the real one, sending through a real gateway (on the test clock) to
// srv on loopback.
func withRealRD(t *testing.T, e *env, srv http.Handler) {
	t.Helper()
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "rd.test:80" {
			return nil, fmt.Errorf("unexpected dial to %s", addr)
		}
		var d net.Dialer
		return d.DialContext(ctx, "tcp", ts.Listener.Addr().String())
	}
	gw, err := gateway.New(e.ctx, e.pol, gateway.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test", DialContext: dial, Now: func() time.Time { return e.clock }})
	if err != nil {
		t.Fatal(err)
	}
	rd, err := provider.New("rd", e.pol.Providers["rd"], "test-rd-key", gw)
	if err != nil {
		t.Fatal(err)
	}
	e.r.provs["rd"] = rd
}

// A few torrents answering 5xx while the provider is otherwise up stall normally (blocklisted), even after their
// reads back the provider off in the gateway: the account check goes through that back-off (gateway.HealthCheck)
// and the held reads are read again. Most of a provider's torrents failing at once is its outage,
// whatever the account says, and so is a failing account check: zurg-style, the stall
// clock runs through it, and its downloads end at the limit as provider unavailable, routed again and not
// blocklisted. The real gateway and RD client on loopback.
func TestTorrent5xxThroughRealGateway(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		broken, healthy    int
		accountDown        bool
		wantStalled        int
		wantUnavailable    int
		wantAccountChecked bool
	}{
		{name: "three of eight broken", broken: 3, healthy: 5, wantStalled: 3, wantAccountChecked: true},
		{name: "one broken alone", broken: 1, wantStalled: 1, wantAccountChecked: true},
		{name: "one broken, account down", broken: 1, accountDown: true, wantUnavailable: 1, wantAccountChecked: true},
		{name: "six of eight broken", broken: 6, healthy: 2, wantUnavailable: 6, wantAccountChecked: true},
		// The first pass needs no account check; later ones do, when the gateway's failure streak (kept across
		// passes) refuses the reads after the first: the check answers, the held reads fail, and it is an outage.
		{name: "three of three broken", broken: 3, wantUnavailable: 3, wantAccountChecked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, true, policy.ModeActive)
			withQueue(e)
			e.r.set.DownloadingTimeout = 0 // the healthy downloads stay downloading
			srv := &rdServer{broken: map[string]bool{}, accountDown: tc.accountDown}
			withRealRD(t, e, srv)
			for i := 1; i <= tc.broken+tc.healthy; i++ {
				id := fmt.Sprintf("t%d", i)
				state := "downloading"
				if i <= tc.broken {
					srv.broken[id] = true // read first in the first pass (by id): their failures trip the back-off
					state = "added"
				}
				e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
					provider, provider_torrent_id, state, added_at, progress, progress_at, created_at, updated_at)
					VALUES (?, 1, 10, 'movie', ?, 'Movie', 'pub', 'rd', ?, ?, ?, 50, ?, 0, 0)`,
					i, rdHash(id), id, state, store.Millis(e.clock), store.Millis(e.clock))
			}
			end := e.clock.Add(10 * time.Hour)
			for e.clock.Before(end) {
				if _, err := e.r.Poll(e.ctx); err != nil {
					t.Fatal(err)
				}
				e.clock = e.clock.Add(5 * time.Minute)
			}
			stalled := e.count(`SELECT COUNT(*) FROM download WHERE state = 'failed' AND error LIKE 'stalled%'`)
			if stalled != tc.wantStalled {
				t.Fatalf("stalled %d, want %d", stalled, tc.wantStalled)
			}
			if n := e.count(`SELECT COUNT(*) FROM router_blocklist`); n != tc.wantStalled {
				t.Fatalf("blocklisted %d, want %d", n, tc.wantStalled)
			}
			unavailable := e.count(`SELECT COUNT(*) FROM download WHERE state = 'queued'
				AND error LIKE 'provider unavailable%'`)
			if unavailable != tc.wantUnavailable {
				t.Fatalf("ended as provider unavailable %d, want %d", unavailable, tc.wantUnavailable)
			}
			if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = ? AND state = 'queued'`, JobRoute); n != tc.wantUnavailable {
				t.Fatalf("route jobs %d, want %d", n, tc.wantUnavailable)
			}
			if n := e.count(`SELECT COUNT(*) FROM download WHERE state = 'downloading'`); n != tc.healthy {
				t.Fatalf("healthy downloads left downloading: %d of %d", n, tc.healthy)
			}
			srv.mu.Lock()
			hits := srv.accountHits
			srv.mu.Unlock()
			if (hits > 0) != tc.wantAccountChecked {
				t.Fatalf("account checks %d", hits)
			}
		})
	}
}

// An RD add waiting for its file selection while grab is off cannot be selected (the write gate): its stall clock
// waits with it instead of blocklisting the release as stalled, and runs again once grab is back on and the
// selection goes out.
func TestGatedSelectionPausesStallClock(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	e.r.set.CachedOnly = false
	e.pr["rd"].set("rd-aaaa", provider.Torrent{Hash: h("a"), State: provider.StateQueued, Status: "magnet_conversion"})
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.Outcome != OutcomeAdded {
		t.Fatalf("%+v %v", res, err)
	}
	e.r.set.Enabled = false // the user turns Pause on
	e.pr["rd"].set("rd-aaaa", provider.Torrent{Hash: h("a"), State: provider.StateQueued, Status: rdWaitingFiles,
		Files: []provider.File{{ID: "1", Path: "Movie.mkv", Bytes: 10 << 30}}})
	stall := e.r.stallAfter("rd")
	gatedFor := 3 * stall
	for end := e.clock.Add(gatedFor); e.clock.Before(end); e.clock = e.clock.Add(time.Minute) {
		if _, err := e.r.Poll(e.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist WHERE info_hash = ?`, h("a")); n != 0 {
		t.Fatal("release blocklisted as stalled while the gate refused its selection")
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'added' AND poll_outage_since IS NOT NULL`,
		res.DownloadID); n != 1 {
		t.Fatal("the gated wait did not stop the stall clock")
	}
	if len(e.ad["rd"].selects) != 0 {
		t.Fatal("selected through the gate")
	}
	// Pause off again: the selection goes out and the gated time stays off the clock.
	e.r.set.Enabled = true
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if len(e.ad["rd"].selects) != 1 {
		t.Fatalf("selects %d after grab came back", len(e.ad["rd"].selects))
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state IN ('added', 'downloading')
		AND poll_outage_since IS NULL AND stall_offset >= ?`, res.DownloadID, gatedFor.Milliseconds()-60000); n != 1 {
		t.Fatal("the gated time was not taken off the stall clock")
	}
}
