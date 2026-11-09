package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/errinbox"
	"github.com/theatarr/theatarr/internal/health"
)

// serveWorker is one long-lived loop of theatarr serve whose schedule or source comes from a settings section
// (docs/design/settings.md "Sections and views"): the library's zurg-log watch, dangling report and relink sweep, the
// provider sync schedule, the shadow import schedule, and so on.
//
// key names the configuration the worker runs with, computed from a published runtime generation; "" means the
// worker is off. run blocks until ctx ends. prev is the key the worker ran with before ("" at the first start).
// A run that returns an error before its context ends counts as a failed start: it is logged, shown in health
// (LiveSettingsCheck), and started again at the next publication.
type serveWorker struct {
	name string
	key  func(*serveRuntime) string
	run  func(ctx context.Context, rt *serveRuntime, prev string) error
}

// serveWorkers supervises the serve workers across settings generations, the way the HTTP event loop and the egress
// checks follow a publication (serveHTTPServices, serveEgressChecks). Apply is called for every published runtime:
// a worker whose key changed is stopped, waited for, and started again on the new runtime; a worker whose key did
// not change keeps running, so an unrelated save restarts nothing. Each worker has one supervising goroutine, so two
// runs of one worker never overlap, and several quick saves collapse into one restart on the newest runtime.
type serveWorkers struct {
	ctx    context.Context
	cancel context.CancelFunc
	log    *slog.Logger
	// stuckAfter is how long a stop may take before health reports the worker as not stopping; the restart keeps
	// waiting for it.
	stuckAfter time.Duration

	mu      sync.Mutex
	started bool
	closed  bool
	slots   []*workerSlot
	wg      sync.WaitGroup
}

type workerSlot struct {
	spec serveWorker
	wake chan struct{}

	mu   sync.Mutex
	want *serveRuntime
	st   serveWorkerStatus
}

// serveWorkerStatus is one worker's state for health and tests.
type serveWorkerStatus struct {
	Name       string    `json:"name"`
	Running    bool      `json:"running"`
	Generation int64     `json:"generation"`
	Starts     int       `json:"starts"`
	Stopping   bool      `json:"stopping,omitempty"`
	Stuck      bool      `json:"stuck,omitempty"`
	Error      string    `json:"error,omitempty"`
	ErrorAt    time.Time `json:"error_at,omitzero"`
}

func newServeWorkers(ctx context.Context, log *slog.Logger, specs ...serveWorker) *serveWorkers {
	ctx, cancel := context.WithCancel(ctx)
	s := &serveWorkers{ctx: ctx, cancel: cancel, log: log.With("component", "live-settings"), stuckAfter: 2 * time.Minute}
	for _, spec := range specs {
		s.slots = append(s.slots, &workerSlot{spec: spec, wake: make(chan struct{}, 1), st: serveWorkerStatus{Name: spec.name}})
	}
	return s
}

// Start runs every worker on the startup runtime. Publications before Start are ignored: the startup runtime is
// published before the services the workers run are wired.
func (s *serveWorkers) Start(initial *serveRuntime) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.started || s.closed {
		s.mu.Unlock()
		return
	}
	s.started = true
	for _, slot := range s.slots {
		s.wg.Go(func() { s.supervise(slot) })
	}
	s.mu.Unlock()
	s.Apply(initial)
}

// Apply hands a published runtime to every worker. It never blocks: the restarts happen in the workers' own
// supervising goroutines.
func (s *serveWorkers) Apply(rt *serveRuntime) {
	if s == nil || rt == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started || s.closed {
		return
	}
	for _, slot := range s.slots {
		slot.mu.Lock()
		slot.want = rt
		slot.mu.Unlock()
		select {
		case slot.wake <- struct{}{}:
		default:
		}
	}
}

// Close stops every worker and waits for them.
func (s *serveWorkers) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
}

// Status lists the workers in their registration order.
func (s *serveWorkers) Status() []serveWorkerStatus {
	if s == nil {
		return nil
	}
	out := make([]serveWorkerStatus, 0, len(s.slots))
	for _, slot := range s.slots {
		slot.mu.Lock()
		out = append(out, slot.st)
		slot.mu.Unlock()
	}
	return out
}

// Health is LiveSettingsCheck: a worker that failed to start on its new settings, or does not stop for a restart.
func (s *serveWorkers) Health(context.Context) health.Result {
	var failed, stuck []string
	detail := map[string]any{}
	for _, st := range s.Status() {
		switch {
		case st.Error != "":
			failed = append(failed, st.Name)
			detail[st.Name] = st.Error
		case st.Stuck:
			stuck = append(stuck, st.Name)
			detail[st.Name] = "not stopping for a settings restart"
		}
	}
	switch {
	case len(failed) > 0:
		return health.Result{Type: health.Error, Message: fmt.Sprintf("Did not start on the saved settings: %v; "+
			"save the section again or check the log", failed), Detail: detail}
	case len(stuck) > 0:
		return health.Result{Type: health.Warning, Message: fmt.Sprintf("Waiting to stop for a settings change: %v", stuck),
			Detail: detail}
	}
	return health.Result{Type: health.OK, Message: "Every settings-driven worker runs on the published settings"}
}

func (slot *workerSlot) update(f func(*serveWorkerStatus)) {
	slot.mu.Lock()
	f(&slot.st)
	slot.mu.Unlock()
}

func (slot *workerSlot) key(rt *serveRuntime) (key string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("reading its settings: %v", r)
		}
	}()
	return slot.spec.key(rt), nil
}

// supervise is a worker's only goroutine that starts or stops it.
func (s *serveWorkers) supervise(slot *workerSlot) {
	var (
		cancel  context.CancelFunc
		done    chan struct{}
		failed  chan error
		current string
		running bool
		exited  bool // returned on its own, without an error, on the current key
	)
	stop := func() {
		if !running {
			return
		}
		slot.update(func(st *serveWorkerStatus) { st.Stopping = true })
		cancel()
		t := time.NewTimer(s.stuckAfter)
		select {
		case <-done:
			t.Stop()
		case <-t.C:
			slot.update(func(st *serveWorkerStatus) { st.Stuck = true })
			s.log.Error("worker did not stop for a settings change; its restart waits", "worker", slot.spec.name)
			<-done
		}
		running = false
		slot.update(func(st *serveWorkerStatus) { st.Running, st.Stopping, st.Stuck = false, false, false })
	}
	for {
		select {
		case <-s.ctx.Done():
			stop()
			return
		case <-slot.wake:
		}
		slot.mu.Lock()
		rt := slot.want
		slot.mu.Unlock()
		key, err := slot.key(rt)
		if err != nil {
			s.log.Error("worker keeps its old settings", "worker", slot.spec.name, "generation", rt.Generation, "err", err)
			slot.update(func(st *serveWorkerStatus) { st.Error, st.ErrorAt = err.Error(), time.Now() })
			continue
		}
		if running {
			select {
			case <-done:
				// It returned on its own: with nothing to do on these settings, or after a failed start, which the
				// next publication retries.
				running = false
				exited = <-failed == nil
				cancel()
			default:
			}
		}
		if key == current && (running || exited) {
			slot.update(func(st *serveWorkerStatus) { st.Generation = rt.Generation })
			continue
		}
		if key == "" && current == "" {
			slot.update(func(st *serveWorkerStatus) { st.Generation = rt.Generation })
			continue
		}
		stop()
		prev := current
		current, exited = key, false
		if key == "" {
			s.log.Info("worker off after a settings change", "worker", slot.spec.name, "generation", rt.Generation)
			slot.update(func(st *serveWorkerStatus) { st.Generation, st.Error = rt.Generation, "" })
			continue
		}
		ctx, stopRun := context.WithCancel(s.ctx)
		cancel = stopRun // called by stop, or once the run returns by itself
		done, failed, running = make(chan struct{}), make(chan error, 1), true
		slot.update(func(st *serveWorkerStatus) {
			st.Running, st.Generation, st.Error, st.ErrorAt = true, rt.Generation, "", time.Time{}
			st.Starts++
		})
		if prev != "" {
			s.log.Info("worker restarted on saved settings", "worker", slot.spec.name, "generation", rt.Generation)
		}
		go func(ctx context.Context, done chan struct{}, failed chan error) {
			var err error
			defer func() {
				lctx := context.Background()
				if r := recover(); r != nil {
					err = fmt.Errorf("panic: %v", r)
					// The error inbox records the panic with its stack; the log line stays out of it.
					errinbox.Panic("live-settings", r, map[string]string{"worker": slot.spec.name})
					lctx = errinbox.Quiet(lctx)
				}
				if err != nil && ctx.Err() == nil {
					s.log.ErrorContext(lctx, "worker failed on saved settings", "worker", slot.spec.name, "generation", rt.Generation, "err", err)
					slot.update(func(st *serveWorkerStatus) { st.Error, st.ErrorAt = err.Error(), time.Now() })
				} else {
					err = nil
				}
				slot.update(func(st *serveWorkerStatus) { st.Running = false })
				failed <- err
				close(done)
			}()
			err = slot.spec.run(ctx, rt, prev)
		}(ctx, done, failed)
	}
}
