// Package shadow is what is left of the shadow runner: the periodic arr import (job arr.import), which copies the
// import.arr instances into the library. The comparison with the arrs is gone and RSS moved to internal/acquire/rss.
package shadow

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/theatarr/theatarr/internal/importarr"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

// KindImport is the arr import's job kind.
const KindImport = "arr.import"

// ArrSourceFunc gives the arr client for an import.arr instance (nil and an error when its key is not set).
type ArrSourceFunc func(instance string) (importarr.Getter, error)

// Runtime is the settings-dependent part of one immutable serve generation.
type Runtime struct {
	Generation int64
	Settings   *settings.Runtime
	Policy     *policy.Policy
	// Arr gives the import's arr clients from this generation's import section; nil keeps the runner's own.
	Arr ArrSourceFunc
}

// RuntimeSource resolves the runtime an operation captures when it starts.
type RuntimeSource interface {
	Resolve(context.Context, int64) (Runtime, bool)
}

// Config is what New needs. Store and Policy are required.
type Config struct {
	Store  *store.Store
	Policy *policy.Policy
	// Queue is required for scheduling.
	Queue *jobs.Queue
	// Arr gives the import's arr clients; nil turns the import off.
	Arr     ArrSourceFunc
	Logger  *slog.Logger
	Runtime RuntimeSource
	Now     func() time.Time
}

// Runner holds the arr import job.
type Runner struct {
	st  *store.Store
	pol *policy.Policy
	q   *jobs.Queue
	arr ArrSourceFunc
	log *slog.Logger
	now func() time.Time
	set policy.ShadowSettings

	runtime         RuntimeSource
	generation      int64
	settingsRuntime *settings.Runtime
}

// New builds the runner.
func New(cfg Config) *Runner {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Runner{st: cfg.Store, pol: cfg.Policy, q: cfg.Queue, arr: cfg.Arr, log: cfg.Logger.With("component", "shadow"),
		now: cfg.Now, set: cfg.Policy.ShadowSettings(), runtime: cfg.Runtime}
}

// RegisterJobs adds the handler to w.
func (r *Runner) RegisterJobs(w *jobs.Worker) {
	w.Handle(KindImport, func(ctx context.Context, j *jobs.Job) error {
		run, err := r.forOperation(ctx)
		if err != nil {
			return err
		}
		ctx = run.pin(ctx)
		_, err = run.doImport(ctx)
		return err
	})
}

func (r *Runner) forOperation(ctx context.Context) (*Runner, error) {
	if r.runtime == nil {
		return r, nil
	}
	snapshot, ok := r.runtime.Resolve(ctx, 0)
	if !ok || snapshot.Policy == nil {
		return nil, errors.New("shadow: current settings runtime is unavailable")
	}
	view := *r
	view.pol = snapshot.Policy
	view.set = snapshot.Policy.ShadowSettings()
	if snapshot.Arr != nil {
		view.arr = snapshot.Arr
	}
	view.generation = snapshot.Generation
	view.settingsRuntime = snapshot.Settings
	return &view, nil
}

func (r *Runner) pin(ctx context.Context) context.Context {
	return settings.WithRuntime(ctx, r.settingsRuntime)
}

// Schedule schedules arr.import until ctx ends, the first import after importDelay. With a settings runtime, the
// schedule (import.arr and shadow.import_interval) is the one current when it starts; theatarr serve starts it again
// when either changes.
func (r *Runner) Schedule(ctx context.Context, importDelay time.Duration) {
	if r.q == nil {
		return
	}
	start := r
	if r.runtime != nil {
		if active, err := r.forOperation(ctx); err == nil {
			start = active
		}
	}
	if start.arr == nil || start.pol.Import == nil || len(start.pol.Import.Arr) == 0 {
		return
	}
	next, every := r.now().Add(importDelay), start.set.ImportInterval
	r.log.Info("shadow job scheduled", "kind", KindImport, "every", every.String(), "first", next.UTC())
	t := time.NewTimer(time.Second)
	defer t.Stop()
	for {
		now := r.now()
		if !now.Before(next) {
			r.enqueue(ctx, KindImport, nil, jobs.EnqueueOptions{DedupeKey: KindImport, MaxAttempts: 2})
			next = now.Add(every)
		}
		t.Reset(max(next.Sub(now), time.Second))
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (r *Runner) enqueue(ctx context.Context, kind string, payload any, o jobs.EnqueueOptions) {
	id, created, err := r.q.Enqueue(ctx, kind, payload, o)
	switch {
	case err != nil:
		r.log.Warn("enqueue", "kind", kind, "err", err)
	case !created:
		r.log.Debug("already queued or running; not enqueued again", "kind", kind, "job", id)
	}
}

// record writes a run row.
func (r *Runner) record(ctx context.Context, kind string, start time.Time, ok bool, summary any) {
	b, err := jsonString(summary)
	if err != nil {
		r.log.Warn("run summary", "err", err)
		return
	}
	_, err = r.st.Writer().ExecContext(ctx, `INSERT INTO shadow_run (kind, started_at, finished_at, ok, summary)
		VALUES (?, ?, ?, ?, ?)`, kind, store.Millis(start), store.Millis(r.now()), boolInt(ok), b)
	if err != nil {
		r.log.Warn("run record", "err", err)
	}
	if _, err := r.st.Writer().ExecContext(ctx, `DELETE FROM shadow_run WHERE started_at < ?`,
		store.Millis(r.now().Add(-30*24*time.Hour))); err != nil {
		r.log.Warn("run prune", "err", err)
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func sortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
