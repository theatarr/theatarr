// Package rss runs the RSS sync (job shadow.rss, docs/design/rss.md): it reads the RSS feeds of the indexers the Live
// libraries list, decides each new item for the titles that want it and hands what it accepts to the router. A
// Watch-only library gets nothing from it. While downloads are paused (router.enabled false) a run reads nothing and
// routes nothing, so no feed item is lost: the next run after the pause reads the same items.
//
// The names are kept from when RSS ran in the shadow runner: the settings section shadow (fields rss and
// rss_interval), the job kind shadow.rss, the tables shadow_rss_seen, shadow_rss_cursor and shadow_run, the metrics
// and the log texts.
package rss

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

// KindRSS is the RSS sync's job kind.
const KindRSS = "shadow.rss"

// priorityRSS is below the default 0 of the other jobs.
const priorityRSS = -20

// BudgetSource is the gateway's state, for skipping indexers whose budget is spent or backing off.
type BudgetSource interface {
	StateFor(context.Context) gateway.State
}

// Runtime is the settings-dependent part of one immutable serve generation.
type Runtime struct {
	Generation int64
	Settings   *settings.Runtime
	Policy     *policy.Policy
	Engine     *decide.Engine
	Indexers   *indexer.Service
	Budgets    BudgetSource
	Router     Router
}

// RuntimeSource resolves the runtime an operation captures when it starts.
type RuntimeSource interface {
	Resolve(context.Context, int64) (Runtime, bool)
}

// Config is what New needs. Store, Policy and Engine are required.
type Config struct {
	Store  *store.Store
	Policy *policy.Policy
	Engine *decide.Engine
	// Queue is required for scheduling; nil runs nothing on a schedule (tests).
	Queue *jobs.Queue
	// Indexers runs the RSS reads; nil turns them off.
	Indexers *indexer.Service
	// Budgets, when set, lets the runner skip indexers whose budget is capped or backing off.
	Budgets BudgetSource
	// Router gets the accepted items of the Live libraries as router.route jobs. nil: RSS reads for nothing.
	Router     Router
	Logger     *slog.Logger
	Registerer prometheus.Registerer
	Runtime    RuntimeSource
	Now        func() time.Time
}

// Router is the download router's entry for the runner (router.Router).
type Router interface {
	Route(ctx context.Context, req router.Request) (*router.Result, error)
	Enqueue(ctx context.Context, req router.Request, at time.Time) (int64, error)
}

// Runner holds the RSS job.
type Runner struct {
	st      *store.Store
	pol     *policy.Policy
	eng     *decide.Engine
	q       *jobs.Queue
	idx     *indexer.Service
	budgets BudgetSource
	log     *slog.Logger
	now     func() time.Time
	set     policy.ShadowSettings
	lib     *library
	m       *metrics
	active  map[string]bool // Live versions whose profile built, read by RSS for the router (Router set)
	rt      Router

	runtime         RuntimeSource
	generation      int64
	settingsRuntime *settings.Runtime
}

// New builds the runner. It is the engine's film index (Engine.SetLibrary), reloaded at the start of every RSS run.
func New(cfg Config) *Runner {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	r := &Runner{st: cfg.Store, pol: cfg.Policy, eng: cfg.Engine, q: cfg.Queue, idx: cfg.Indexers, budgets: cfg.Budgets,
		log: cfg.Logger.With("component", "shadow"), now: cfg.Now, set: cfg.Policy.ShadowSettings(),
		lib: &library{}, m: newMetrics(cfg.Registerer), active: map[string]bool{}, rt: cfg.Router, runtime: cfg.Runtime}
	r.setVersions(cfg.Policy, cfg.Engine, cfg.Router)
	if cfg.Engine != nil {
		cfg.Engine.SetLibrary(r.lib)
	}
	return r
}

func (r *Runner) setVersions(pol *policy.Policy, eng *decide.Engine, rt Router) {
	r.active = map[string]bool{}
	for _, k := range sortedKeys(pol.Versions) {
		v := pol.Versions[k]
		fl := format.Radarr
		if v.Kind == policy.KindSeries {
			fl = format.Sonarr
		}
		if v.Mode == policy.ModeActive && rt != nil && eng != nil && eng.Profile(v.Profile, fl) != nil {
			r.active[k] = true
		}
	}
}

// ActiveVersions lists the Live versions whose RSS picks go to the router. A run is skipped while Pause is on; a route
// the router's gate still refuses is a dry run: a "would add" plan, no add and no re-search. Items routed this way are not marked seen, so they are
// decided again each sync while they stay in the feed (at most one page, indexer.DefaultPageCap items, per feed and
// sync).
func (r *Runner) ActiveVersions() []string { return sortedKeys(r.active) }

// rssVersions are the versions RSS reads for: the Live ones.
func (r *Runner) rssVersions() map[string]bool { return r.active }

// paused is whether downloads are paused: router.enabled false in the pinned policy, the router gate's own switch.
func (r *Runner) paused() bool { return !r.pol.RouterSettings().Enabled }

// RegisterJobs adds the RSS handler to w.
func (r *Runner) RegisterJobs(w *jobs.Worker) {
	w.Handle(KindRSS, func(ctx context.Context, j *jobs.Job) error {
		run, err := r.forOperation(ctx)
		if err != nil {
			return err
		}
		_, err = run.rssJob(run.pin(ctx))
		return err
	})
}

func (r *Runner) forOperation(ctx context.Context) (*Runner, error) {
	if r.runtime == nil {
		return r, nil
	}
	snapshot, ok := r.runtime.Resolve(ctx, 0)
	if !ok || snapshot.Policy == nil || snapshot.Engine == nil {
		return nil, errors.New("shadow: current settings runtime is unavailable")
	}
	view := *r
	view.pol, view.eng, view.idx, view.budgets = snapshot.Policy, snapshot.Engine, snapshot.Indexers, snapshot.Budgets
	view.set = snapshot.Policy.ShadowSettings()
	view.rt = snapshot.Router
	view.setVersions(view.pol, view.eng, view.rt)
	view.generation = snapshot.Generation
	view.settingsRuntime = snapshot.Settings
	return &view, nil
}

func (r *Runner) pin(ctx context.Context) context.Context {
	return settings.WithRuntime(ctx, r.settingsRuntime)
}

// Schedule schedules shadow.rss until ctx ends, the first read after rssDelay (so the egress check has run). With a
// settings runtime the schedule follows each generation by itself: its interval, and whether any feed is eligible.
func (r *Runner) Schedule(ctx context.Context, rssDelay time.Duration) {
	if r.q == nil {
		return
	}
	// The film index the decision engine searches with is loaded now and on every due tick no read is queued for (RSS
	// off, no Live feed), so it never depends on RSS running; a queued read reloads it itself.
	r.reloadIndex(ctx)
	fresh := true
	var generation int64
	if active, err := r.forOperation(ctx); err == nil {
		generation = active.generation
	}
	next, every := r.now().Add(rssDelay), r.set.RSSInterval
	r.log.Info("shadow job scheduled", "kind", KindRSS, "every", every.String(), "first", next.UTC())
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		now := r.now()
		// A new generation (another interval, say) is noticed within a second and runs at once.
		active, err := r.forOperation(ctx)
		if err == nil && active.generation != generation {
			generation = active.generation
			every = active.set.RSSInterval
			next = now
		}
		if !now.Before(next) {
			if err == nil && active.eligible(ctx) {
				r.enqueue(ctx, KindRSS, nil, jobs.EnqueueOptions{DedupeKey: KindRSS, Priority: priorityRSS, MaxAttempts: 2})
			} else if !fresh {
				r.reloadIndex(ctx)
			}
			next = now.Add(every)
		}
		fresh = false
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// eligible is whether an RSS read has anything to do: RSS on, an indexer service, a Live version and a feed it lists.
func (r *Runner) eligible(ctx context.Context) bool {
	return r.set.RSS && r.idx != nil && len(r.rssVersions()) > 0 && r.hasRSSFeeds(ctx)
}

// QueueRSS queues an RSS read now (the arr RssSync command), deduped with the scheduled one so two never overlap.
func (r *Runner) QueueRSS(ctx context.Context) error {
	if r.q == nil {
		return errors.New("shadow: no job queue")
	}
	_, _, err := r.q.Enqueue(ctx, KindRSS, nil, jobs.EnqueueOptions{DedupeKey: KindRSS, Priority: priorityRSS, MaxAttempts: 2})
	return err
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

// library is the engine's Library over our movies, swapped after each reload (the engine reads it concurrently).
type library struct {
	mu    sync.RWMutex
	inner decide.Library
}

func (l *library) ByIMDb(id string) *decide.Title {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.inner == nil {
		return nil
	}
	return l.inner.ByIMDb(id)
}

func (l *library) ByTMDb(id int) *decide.Title {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.inner == nil {
		return nil
	}
	return l.inner.ByTMDb(id)
}

// reloadIndex reloads the film index outside a run, logging a failure.
func (r *Runner) reloadIndex(ctx context.Context) {
	if _, err := r.refresh(ctx); err != nil && ctx.Err() == nil {
		r.log.Warn("shadow rss: film index not reloaded", "err", err)
	}
}

// refresh reloads our movies.
func (r *Runner) refresh(ctx context.Context) (map[int64]decide.Title, error) {
	titles, err := decide.LoadMovies(ctx, r.st.Reader())
	if err != nil {
		return nil, err
	}
	r.lib.mu.Lock()
	r.lib.inner = decide.NewLibrary(titles)
	r.lib.mu.Unlock()
	return titles, nil
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

// hasRSSFeeds is whether the Live versions list a feed the RSS sync can read.
func (r *Runner) hasRSSFeeds(ctx context.Context) bool {
	feeds, _ := r.rssIndexers(ctx)
	return len(feeds) > 0
}

func sortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
