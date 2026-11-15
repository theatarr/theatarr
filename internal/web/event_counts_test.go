package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/tasks"
)

type snapshotWriter struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

type agingGateway struct {
	age atomic.Int64
	bad atomic.Bool
}

func (g *agingGateway) EgressSummary() map[string]any {
	return map[string]any{"vpn_ok": !g.bad.Load(), "paths": []map[string]any{{"age_s": g.age.Load()}}}
}

func (g *agingGateway) State() gateway.State { return gateway.State{Egress: g.EgressSummary()} }

func TestEventsGatewayIgnoresAgesAndEmitsFullSnapshot(t *testing.T) {
	g := &agingGateway{}
	g.age.Store(1)
	e := newEnvWith(t, func(c *Config) { c.Egress = g })
	ch := e.web.hub.subscribe()
	defer e.web.hub.unsubscribe(ch)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.web.runEvents(ctx) }()
	defer func() { cancel(); <-done }()
	waitPolls := func(n int64) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for e.web.polls.Load() < n {
			if time.Now().After(deadline) {
				t.Fatal("event poll did not finish")
			}
			time.Sleep(time.Millisecond)
		}
	}
	waitPolls(1)
	g.age.Store(42)
	waitPolls(3)
	select {
	case ev := <-ch:
		t.Fatalf("age alone published %s: %s", ev.name, ev.data)
	default:
	}
	g.bad.Store(true)
	select {
	case ev := <-ch:
		var gs GatewayState
		if ev.name != "gateway" || json.Unmarshal(ev.data, &gs) != nil || gs.Egress["vpn_ok"] != false ||
			!strings.Contains(string(ev.data), `"age_s":42`) {
			t.Fatalf("full changed snapshot: %s %s", ev.name, ev.data)
		}
	case <-time.After(time.Second):
		t.Fatal("no gateway change")
	}
}

func (w snapshotWriter) Flush() {
	w.ResponseRecorder.Flush()
	if strings.Contains(w.Body.String(), "event:") {
		w.cancel()
	}
}

// Counts do not depend on fields used only to render individual jobs. Dropping one also catches an accidental
// return to listJobs(..., 1) in the stream's initial snapshot without relying on query timings.
func TestEventsSnapshotReadsOnlyCounts(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.exec(`ALTER TABLE job DROP COLUMN last_error`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx = auth.WithIdentity(ctx, &auth.Identity{UserID: 1, Perms: auth.PermAdmin})
	rec := httptest.NewRecorder()
	e.web.apiEvents(snapshotWriter{rec, cancel}, httptest.NewRequest("GET", APIBase+"/events", nil).WithContext(ctx))
	if body := rec.Body.String(); !strings.Contains(body, `event: jobs`) || !strings.Contains(body, `"queued":1`) || !strings.Contains(body, `"attention":1`) {
		t.Fatalf("missing counts snapshot: %s", body)
	}
}

// A scheduled task's run publishes the jobs event with tasks set and its failure in the attention count, so open
// Tasks and Status pages refresh without polling.
func TestEventsTaskRunPublishesJobs(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	tb := tasks.New(ctx, e.web.st, nil)
	e.web.tasks = tb
	defer tb.Register(ctx, tasks.Task{Key: "k", Name: "K", Every: time.Hour,
		Run: func(context.Context) error { return errors.New("boom") }}, time.Time{})()
	ch := e.web.hub.subscribe()
	defer e.web.hub.unsubscribe(ch)
	done := make(chan struct{})
	go func() { defer close(done); e.web.runEvents(ctx) }()
	defer func() { cancel(); <-done }()
	for deadline := time.Now().Add(time.Second); e.web.polls.Load() < 1; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("event poll did not finish")
		}
	}
	before, err := e.web.jobCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tb.Run(ctx, "k", time.Time{}); err == nil {
		t.Fatal("the run worked")
	}
	for deadline := time.After(2 * time.Second); ; {
		select {
		case ev := <-ch:
			var d struct {
				Counts map[string]int `json:"counts"`
				Tasks  bool           `json:"tasks"`
			}
			if ev.name == "jobs" && json.Unmarshal(ev.data, &d) == nil && d.Tasks && d.Counts["attention"] == before["attention"]+1 {
				return
			}
		case <-deadline:
			t.Fatal("no jobs event for the task run")
		}
	}
}
