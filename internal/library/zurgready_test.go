package library

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
)

// TestZurgReadyHoldsSweeps: while zurg's /ready answers 503 (library still loading,
// https://notes.debridmediamanager.com/zurg/setup/docker/) the mount counts as down: the health checks report
// ErrMountDown and relink, the dangling-link sweep, verify and the trash purge refuse. A 503 is asked again after
// readyLoadingTTL; a 200 is reused for the interval.
func TestZurgReadyHoldsSweeps(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release("Some.Release", map[string]float64{"movie.mkv": 7000})
	srv := newZurgServer(t)
	srv.ready = http.StatusServiceUnavailable
	e.withZurg(srv)

	if err := e.lib.MountHealthy(e.ctx); !errors.Is(err, ErrMountDown) {
		t.Fatalf("MountHealthy while loading: %v", err)
	}
	if err := e.lib.MountListed(e.ctx); !errors.Is(err, ErrMountDown) {
		t.Errorf("MountListed while loading: %v", err)
	}
	if _, err := e.lib.Relink(e.ctx, "", false); !errors.Is(err, ErrMountDown) {
		t.Errorf("relink while loading: %v", err)
	}
	if _, err := e.lib.DanglingReport(e.ctx); !errors.Is(err, ErrMountDown) {
		t.Errorf("dangling sweep while loading: %v", err)
	}
	// Verify is a read-only report: it records the mount as down on each scan instead of failing.
	if scans, err := e.lib.Verify(e.ctx, ""); err != nil || len(scans) == 0 {
		t.Errorf("verify while loading: %v, %d scans", err, len(scans))
	} else {
		for _, sc := range scans {
			if !strings.Contains(sc.Mount, "still loading") {
				t.Errorf("verify scan of %s while loading: mount %q", sc.Root, sc.Mount)
			}
		}
	}
	if _, err := e.lib.PurgeTrash(e.ctx); !errors.Is(err, ErrMountDown) {
		t.Errorf("trash purge while loading: %v", err)
	}
	if n := srv.count("/ready"); n != 1 {
		t.Errorf("/ready asked %d times within %s, want 1", n, readyLoadingTTL)
	}
	if srv.count("/debug/torrents") != 0 || srv.count("/torrents/repair/status") != 0 {
		t.Errorf("a refused sweep read the API: %v", srv.hits)
	}

	srv.set(func(s *zurgServer) { s.ready = 0 })
	e.now = e.now.Add(readyLoadingTTL)
	if err := e.lib.MountListed(e.ctx); err != nil {
		t.Fatalf("MountListed once loaded: %v", err)
	}
	if _, err := e.lib.Relink(e.ctx, "", true); err != nil {
		t.Errorf("relink once loaded: %v", err)
	}
	e.now = e.now.Add(30 * time.Second)
	if err := e.lib.MountHealthy(e.ctx); err != nil || srv.count("/ready") != 2 {
		t.Errorf("a 200 not reused for the interval: %v, %d asks", err, srv.count("/ready"))
	}
}

// TestZurgReadyNoAnswerFallsBack: a zurg without /ready (404), one that fails it (500) or one that does not answer
// at all leaves the mount checks alone in charge, and does not keep the other endpoints down.
func TestZurgReadyNoAnswerFallsBack(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError} {
		e := newEnv(t, true, policy.ModeActive)
		e.release("Some.Release", map[string]float64{"movie.mkv": 7000})
		srv := newZurgServer(t)
		srv.ready = status
		z := e.withZurg(srv)
		if err := e.lib.MountListed(e.ctx); err != nil {
			t.Errorf("/ready %d: MountListed %v", status, err)
		}
		if _, err := e.lib.Relink(e.ctx, "", true); err != nil {
			t.Errorf("/ready %d: relink %v", status, err)
		}
		if _, err := z.Torrents(e.ctx); err != nil {
			t.Errorf("/ready %d left the API down: %v", status, err)
		}
	}

	e := newEnv(t, true, policy.ModeActive)
	e.release("Some.Release", map[string]float64{"movie.mkv": 7000})
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	p := &policy.Policy{
		Budgets: map[string]*policy.Budget{"zurg-api": {Requests: 6000, Per: policy.Duration(time.Minute)}},
		Library: &policy.Library{Mount: policy.LibraryMount{ZurgAPI: gone.URL, ZurgAPIBudget: "zurg-api"}},
	}
	gw, err := gateway.New(e.ctx, p, gateway.Options{})
	if err != nil {
		t.Fatal(err)
	}
	z := &ZurgAPI{URL: gone.URL, GW: gw, AllDir: "__all__", Interval: time.Minute, Now: func() time.Time { return e.now }}
	e.lib.zurg = z
	if err := e.lib.MountListed(e.ctx); err != nil {
		t.Errorf("unreachable zurg: MountListed %v", err)
	}
	z.mu.Lock()
	down := z.now().Before(z.downUntil)
	z.mu.Unlock()
	if down {
		t.Error("an unanswered /ready put the API in backoff")
	}
}

// TestZurgReadyOffWithoutAPI: without zurg_api nothing asks /ready and the mount checks are as before.
func TestZurgReadyOffWithoutAPI(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release("Some.Release", map[string]float64{"movie.mkv": 7000})
	if err := e.lib.MountListed(e.ctx); err != nil {
		t.Fatal(err)
	}
	var z *ZurgAPI
	if err := z.Ready(e.ctx); err != nil {
		t.Fatal(err)
	}
}
