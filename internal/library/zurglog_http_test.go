package library

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
)

// logServer serves a zurg-like /logs: the whole current file as text/plain, no Range support.
type logServer struct {
	mu       sync.Mutex
	body     string
	etag     bool // send an ETag and answer If-None-Match with 304
	fail     int  // answer this many requests with 500 first
	requests int
	notMod   int
}

func (s *logServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests++
	if s.fail > 0 {
		s.fail--
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	if s.etag {
		tag := fmt.Sprintf(`"%d-%x"`, len(s.body), len(s.body)*31+strings.Count(s.body, "R"))
		if r.Header.Get("If-None-Match") == tag {
			s.notMod++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", tag)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, s.body)
}

func (s *logServer) set(body string)    { s.mu.Lock(); s.body = body; s.mu.Unlock() }
func (s *logServer) append(more string) { s.mu.Lock(); s.body += more; s.mu.Unlock() }
func (s *logServer) count() (int, int)  { s.mu.Lock(); defer s.mu.Unlock(); return s.requests, s.notMod }

// plainDoer sends requests straight to the test server (the gateway has its own test below).
type plainDoer struct{ c *http.Client }

func (d plainDoer) Do(_ context.Context, t gateway.Target, req *http.Request) (*http.Response, error) {
	if t != ZurgLogTarget {
		return nil, fmt.Errorf("target %v", t)
	}
	return d.c.Do(req)
}

func renameLine(o, n string) string {
	return fmt.Sprintf("2026-10-26T10:00:00Z INFO Renaming %q to %q (another release shares its name)\n", o, n)
}

// polled runs one poll and returns the renames it recorded.
func polled(t *testing.T, z *ZurgLogHTTPFollower) map[string]string {
	t.Helper()
	if err := z.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := z.batch.pending
	z.batch.pending = nil
	return m
}

func TestZurgLogHTTPOffsets(t *testing.T) {
	t.Parallel()
	srv := &logServer{body: "zurg started\n" + renameLine("Old", "History")}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	rotations := 0
	z := &ZurgLogHTTPFollower{URL: ts.URL + "/logs", GW: plainDoer{ts.Client()},
		OnRotate: func(context.Context) { rotations++ }}

	// The first poll only finds the end: the history is the sweep's.
	if m := polled(t, z); len(m) != 0 {
		t.Fatalf("first poll replayed %v", m)
	}
	// Growing, with a line cut in the middle: the partial line waits for the next poll.
	srv.append("noise\n" + renameLine("A", "B") + `Release "B" is filed as "C" now that its preferred`)
	if m := polled(t, z); len(m) != 1 || m["A"] != "B" {
		t.Fatalf("growing: %v", m)
	}
	srv.append(" account names it\n")
	if m := polled(t, z); len(m) != 1 || m["B"] != "C" {
		t.Fatalf("completed line: %v", m)
	}
	// Unchanged body (no ETag): nothing again.
	if m := polled(t, z); len(m) != 0 {
		t.Fatalf("unchanged: %v", m)
	}
	// Rotation: a new file, already longer than the old offset, with a different head.
	newFile := "zurg rotated at 10:05\n" + renameLine("R1", "R2") + strings.Repeat("filler line\n", 40)
	srv.set(newFile)
	if m := polled(t, z); len(m) != 1 || m["R1"] != "R2" || rotations != 1 {
		t.Fatalf("rotation: %v, rotations %d", m, rotations)
	}
	srv.append(renameLine("R3", "R4"))
	if m := polled(t, z); len(m) != 1 || m["R3"] != "R4" || rotations != 1 {
		t.Fatalf("after rotation: %v, rotations %d", m, rotations)
	}
	// Truncation: shorter than the offset, read from its start.
	srv.set(renameLine("T1", "T2"))
	if m := polled(t, z); len(m) != 1 || m["T1"] != "T2" || rotations != 2 {
		t.Fatalf("truncation: %v, rotations %d", m, rotations)
	}
	// The log removed: zurg serves an empty body; then a fresh file.
	srv.set("")
	if m := polled(t, z); len(m) != 0 || rotations != 3 {
		t.Fatalf("empty: %v, rotations %d", m, rotations)
	}
	srv.set(renameLine("E1", "E2"))
	if m := polled(t, z); len(m) != 1 || m["E1"] != "E2" || rotations != 3 {
		t.Fatalf("after empty: %v, rotations %d", m, rotations)
	}
}

func TestZurgLogHTTPShortHead(t *testing.T) {
	t.Parallel()
	// The old file was shorter than the fingerprint and ended in a partial line; the new one is shorter than the
	// old body but longer than the offset.
	srv := &logServer{body: "start\npartial"}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	z := &ZurgLogHTTPFollower{URL: ts.URL, GW: plainDoer{ts.Client()}}
	polled(t, z)
	srv.set(renameLine("N1", "N2")[:10])
	if m := polled(t, z); len(m) != 0 {
		t.Fatalf("%v", m)
	}
	srv.set(renameLine("N1", "N2"))
	if m := polled(t, z); len(m) != 1 || m["N1"] != "N2" {
		t.Fatalf("%v", m)
	}
}

func TestZurgLogHTTPConditional(t *testing.T) {
	t.Parallel()
	srv := &logServer{body: "start\n", etag: true}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	z := &ZurgLogHTTPFollower{URL: ts.URL, GW: plainDoer{ts.Client()}}
	polled(t, z)
	if m := polled(t, z); len(m) != 0 {
		t.Fatalf("%v", m)
	}
	if _, nm := srv.count(); nm != 1 {
		t.Fatalf("an unchanged body with an ETag should be a 304, got %d", nm)
	}
	srv.append(renameLine("C1", "C2"))
	if m := polled(t, z); len(m) != 1 || m["C1"] != "C2" {
		t.Fatalf("after change: %v", m)
	}
	if m := polled(t, z); len(m) != 0 {
		t.Fatalf("%v", m)
	}
	if req, nm := srv.count(); nm != 2 || req != 4 {
		t.Fatalf("requests %d, 304s %d", req, nm)
	}
}

func TestZurgLogHTTPRunBatchesAndBacksOff(t *testing.T) {
	srv := &logServer{body: renameLine("Before", "Start")}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	got := make(chan map[string]string, 4)
	z := &ZurgLogHTTPFollower{URL: ts.URL, GW: plainDoer{ts.Client()}, Interval: 5 * time.Millisecond,
		Batch: 40 * time.Millisecond, MaxBackoff: 80 * time.Millisecond,
		OnRenames: func(_ context.Context, m map[string]string) { got <- m }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go z.Run(ctx)
	waitFor(t, func() bool { n, _ := srv.count(); return n >= 2 })
	srv.append(renameLine("A", "B"))
	srv.append(`Release "B" is filed as "C" now that its preferred account names it` + "\n" + renameLine("X", "Y"))
	select {
	case m := <-got:
		if len(m) != 3 || m["A"] != "C" || m["B"] != "C" || m["X"] != "Y" {
			t.Errorf("batch %v (the line before the start must not count; a->b->c chains)", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no renames")
	}

	// Errors: the position is kept, the polls back off, and nothing is lost or replayed after.
	srv.mu.Lock()
	srv.fail = 4
	before := srv.requests
	srv.mu.Unlock()
	start := time.Now()
	srv.append(renameLine("D1", "D2"))
	select {
	case m := <-got:
		if len(m) != 1 || m["D1"] != "D2" {
			t.Errorf("after errors %v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no renames after errors")
	}
	n, _ := srv.count()
	// 4 failures back off 10, 20, 40, 80 ms (capped at 80): at least 150 ms before the fifth request.
	if took := time.Since(start); n-before < 5 || took < 150*time.Millisecond {
		t.Errorf("requests %d over %v: no back-off?", n-before, took)
	}
	select {
	case m := <-got:
		t.Errorf("replayed %v", m)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestZurgLogHTTPThroughGateway(t *testing.T) {
	t.Parallel()
	srv := &logServer{body: "start\n"}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	p := &policy.Policy{
		Budgets: map[string]*policy.Budget{"zurg-log": {Requests: 600, Per: policy.Duration(time.Minute)}},
		Library: &policy.Library{Mount: policy.LibraryMount{ZurgLog: ts.URL + "/logs", ZurgLogBudget: "zurg-log"}},
	}
	gw, err := gateway.New(context.Background(), p, gateway.Options{})
	if err != nil {
		t.Fatal(err)
	}
	z := &ZurgLogHTTPFollower{URL: ts.URL + "/logs", GW: gw}
	polled(t, z)
	srv.append(renameLine("G1", "G2"))
	if m := polled(t, z); len(m) != 1 || m["G1"] != "G2" {
		t.Fatalf("%v", m)
	}
	// Only the configured host is allowed for the target.
	other := &ZurgLogHTTPFollower{URL: "http://other-zurg:9999/logs", GW: gw}
	var r *gateway.Refused
	if err := other.poll(context.Background()); err == nil || !errors.As(err, &r) || r.Reason != gateway.ReasonHost {
		t.Fatalf("another host: %v", err)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out")
}
