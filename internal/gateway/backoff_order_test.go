package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// A request that was already in flight when another got a 429 (Retry-After) answers 200 afterwards: its success
// does not clear the back-off the 429 just set.
func TestInFlightSuccessKeepsFresh429(t *testing.T) {
	e := newEnv(t)
	release := make(chan struct{})
	slowStarted := make(chan struct{})
	var once sync.Once
	e.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			once.Do(func() { close(slowStarted) })
			<-release
			io.WriteString(w, "ok")
			return
		}
		w.Header().Set("Retry-After", "300")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	p := testPolicy()
	p.Budgets["dmm-key"] = &policy.Budget{Requests: 100, Per: policy.Duration(time.Second), Burst: 10}
	p.Indexers["dmm"].Budget = "dmm-key"
	g := e.gateway(p, nil)
	g.CheckEgress(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := get(g, dmm, "http://dmm.test/slow")
		done <- err
	}()
	<-slowStarted
	resp, err := g.Get(context.Background(), dmm, "http://dmm.test/fast")
	if err != nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("fast: %v %v", resp, err)
	}
	resp.Body.Close()
	if _, err := get(g, dmm, "http://dmm.test/fast"); !errors.Is(err, ErrBackoff) {
		t.Fatalf("right after the 429: want back-off, got %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := get(g, dmm, "http://dmm.test/fast"); !errors.Is(err, ErrBackoff) {
		t.Fatalf("the in-flight request's 200 cleared the fresh 429 back-off (Retry-After 300): got %v", err)
	}
	// A request sent after the back-off was set clears it once it succeeds (after Retry-After).
	e.advance(301 * time.Second)
	g.CheckEgress(context.Background())
	if _, err := get(g, dmm, "http://dmm.test/slow"); err != nil {
		t.Fatal(err)
	}
	if b, _ := g.state.snapshot(); b["budget:dmm-key"].reason != "" {
		t.Error("a later success left the budget back-off in place")
	}
}

// The daily cap holds under concurrent requests: the check and the count are one step.
func TestBudgetDailyCapConcurrent(t *testing.T) {
	e := newEnv(t)
	e.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	p := testPolicy()
	p.Budgets["dmm-key"] = &policy.Budget{Requests: 10000, Per: policy.Duration(time.Second), Burst: 10000, Daily: 5}
	p.Indexers["dmm"].Budget = "dmm-key"
	g := e.gateway(p, nil)
	ti := g.targets[dmm.String()]
	if ti == nil {
		t.Fatal("no dmm target")
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	start := make(chan struct{})
	for range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := g.acquire(context.Background(), ti); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if ok != 5 {
		t.Errorf("%d requests passed a daily cap of 5", ok)
	}
}

// Back-off rows written concurrently end up as memory has them: a stale write never lands after a newer one.
func TestBackoffPersistedInOrder(t *testing.T) {
	st := openStore(t, filepath.Join(t.TempDir(), "theatarr.db"))
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	s, err := newState(context.Background(), st, slog.New(slog.NewTextHandler(io.Discard, nil)), now)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.setBackoff(context.Background(), "x", now.Add(time.Duration(i)*time.Second), "r", true, now)
		}()
	}
	wg.Wait()
	var failures int
	var until int64
	if err := st.Reader().QueryRow(`SELECT failures, until_at FROM gateway_backoff WHERE subject = 'x'`).Scan(&failures,
		&until); err != nil {
		t.Fatal(err)
	}
	if failures != 100 || store.FromMillis(until) != now.Add(99*time.Second) {
		t.Errorf("persisted failures %d until %v, memory has 100 and %v", failures, store.FromMillis(until),
			now.Add(99*time.Second))
	}
}

// MaxWait bounds all of a request's budget waits together: a back-off wait followed by a token wait that would end
// past it is refused, not slept through.
func TestBudgetWaitsShareOneDeadline(t *testing.T) {
	e := newEnv(t)
	p := testPolicy()
	p.Budgets["dmm-key"] = &policy.Budget{Requests: 1, Per: policy.Duration(100 * time.Second), Burst: 1}
	p.Indexers["dmm"].Budget = "dmm-key"
	g := e.gateway(p, nil)
	ti := g.targets[dmm.String()]
	if _, err := g.acquire(context.Background(), ti); err != nil {
		t.Fatal(err)
	}
	g.state.setBackoff(context.Background(), "budget:dmm-key", e.clock().Add(55*time.Second), "test", false, e.clock())
	start := e.clock()
	_, err := g.acquire(context.Background(), ti)
	if err == nil {
		t.Fatalf("acquired after %v of waits, over the 1 m MaxWait", e.clock().Sub(start))
	}
}
