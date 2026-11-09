package shadow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/theatarr/theatarr/internal/ceiling"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/importarr"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

// Job kinds.
const (
	KindImport = "arr.import"
	KindGrab   = "shadow.grab"
	KindRSS    = "shadow.rss"
)

// Job priorities: below the default 0 of the other jobs; RSS lowest.
const (
	priorityGrab = -10
	priorityRSS  = -20
)

// maxDefer is how long a grab may wait for a search (hourly cap, every indexer failing) before it is decided
// without one.
const maxDefer = 24 * time.Hour

// ArrSourceFunc gives the arr client for an import.arr instance (nil and an error when its key is not set).
type ArrSourceFunc func(instance string) (importarr.Getter, error)

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
	// Arr gives the import's arr clients from this generation's import section; nil keeps the runner's own.
	Arr ArrSourceFunc
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
	// Queue is required for scheduling and for grabs found by the import; nil handles everything inline (tests,
	// theatarr shadow run-once).
	Queue *jobs.Queue
	// Indexers runs our searches and the RSS reads; nil turns both off.
	Indexers *indexer.Service
	// Budgets, when set, lets the runner skip indexers whose budget is capped or backing off.
	Budgets BudgetSource
	// Arr gives the import's arr clients; nil turns the import off.
	Arr ArrSourceFunc
	// Router, when set, gets a dry-run Route for every grab decision and RSS pick of a shadow version (it never
	// writes for those; the plan goes to router_plan and the diff detail), and the RSS picks of active versions as
	// router.route jobs. nil: no plans, and RSS reads for shadow versions only.
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

// Runner holds the shadow jobs.
type Runner struct {
	st       *store.Store
	pol      *policy.Policy
	eng      *decide.Engine
	q        *jobs.Queue
	idx      *indexer.Service
	budgets  BudgetSource
	arr      ArrSourceFunc
	log      *slog.Logger
	now      func() time.Time
	set      policy.ShadowSettings
	lib      *library
	m        *metrics
	versions map[string]bool // shadow versions whose profile built
	active   map[string]bool // active versions whose profile built, read by RSS for the router (Router set)
	rt       Router
	// followerRefresh, when set, runs after each instance's import (SetFollowerRefresh).
	followerRefresh func(ctx context.Context, followed string, titles []int64) error
	// followerCatchUp, when set, refreshes the followers over every title on a schedule (SetFollowerCatchUp).
	// A pointer: the per-operation views share it.
	followerCatchUp *catchUp

	searchMu        *sync.Mutex // one shadow search at a time
	cacheMu         *sync.Mutex
	searches        map[string]*cachedSearch // by title id, plus the seasons for a series
	runtime         RuntimeSource
	generation      int64
	settingsRuntime *settings.Runtime
}

type cachedSearch struct {
	coverage []ceiling.Coverage
	at       time.Time
	releases []indexer.Release
	notes    []string
	indexers []string
	requests int
}

// New builds the runner. Versions in shadow mode whose profile the engine built are shadowed, movies and series; the
// others are listed in the returned warnings.
func New(cfg Config) (*Runner, []string) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	r := &Runner{st: cfg.Store, pol: cfg.Policy, eng: cfg.Engine, q: cfg.Queue, idx: cfg.Indexers, budgets: cfg.Budgets,
		arr: cfg.Arr, log: cfg.Logger.With("component", "shadow"), now: cfg.Now, set: cfg.Policy.ShadowSettings(),
		lib: &library{}, m: newMetrics(cfg.Registerer), versions: map[string]bool{}, active: map[string]bool{},
		rt: cfg.Router, searches: map[string]*cachedSearch{}, searchMu: &sync.Mutex{}, cacheMu: &sync.Mutex{}, runtime: cfg.Runtime}
	warns := r.setVersions(cfg.Policy, cfg.Engine, cfg.Router)
	if cfg.Engine != nil {
		cfg.Engine.SetLibrary(r.lib)
	}
	return r, warns
}

func (r *Runner) setVersions(pol *policy.Policy, eng *decide.Engine, rt Router) []string {
	r.versions, r.active = map[string]bool{}, map[string]bool{}
	var warns []string
	for _, k := range sortedKeys(pol.Versions) {
		v := pol.Versions[k]
		fl := format.Radarr
		if v.Kind == policy.KindSeries {
			fl = format.Sonarr
		}
		switch {
		case v.Mode == policy.ModeActive && rt != nil && eng != nil && eng.Profile(v.Profile, fl) != nil:
			r.active[k] = true
		case v.Mode != policy.ModeShadow:
		case eng == nil || eng.Profile(v.Profile, fl) == nil:
			warns = append(warns, fmt.Sprintf("version %s: profile %s was not built, so it is not shadowed", k, v.Profile))
		default:
			r.versions[k] = true
		}
	}
	return warns
}

// Versions lists the shadowed versions.
func (r *Runner) Versions() []string { return sortedKeys(r.versions) }

// ActiveVersions lists the active versions whose RSS picks go to the router. With grab off (or router.enabled false)
// the router's gate makes each route a dry run: a "would add" plan, no add and no re-search. The feeds are read for
// the shadowed versions anyway, and those plans are the evidence for turning grab on. Items routed this way are not
// marked seen, so they are decided again each sync while they stay in the feed (at most one page,
// indexer.DefaultPageCap items, per feed and sync).
func (r *Runner) ActiveVersions() []string { return sortedKeys(r.active) }

// rssVersions are the versions RSS reads for: the shadowed ones (picks for the report) and the active ones (picks
// for the router).
func (r *Runner) rssVersions() map[string]bool {
	out := map[string]bool{}
	for k := range r.versions {
		out[k] = true
	}
	for k := range r.active {
		out[k] = true
	}
	return out
}

// RegisterJobs adds the handlers to w.
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
	w.Handle(KindGrab, r.handleGrab)
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
	if snapshot.Arr != nil {
		view.arr = snapshot.Arr
	}
	view.setVersions(view.pol, view.eng, view.rt)
	view.generation = snapshot.Generation
	view.settingsRuntime = snapshot.Settings
	return &view, nil
}

func (r *Runner) pin(ctx context.Context) context.Context {
	return settings.WithRuntime(ctx, r.settingsRuntime)
}

// Run classifies the stored grabs again (Reclassify), then schedules arr.import and shadow.rss until ctx ends. The
// first import runs after importDelay, the first RSS read after rssDelay (so the egress check has run).
func (r *Runner) Run(ctx context.Context, importDelay, rssDelay time.Duration) {
	if r.q == nil {
		return
	}
	// The classification rules may have changed since the stored grabs were decided.
	if _, err := r.Reclassify(ctx); err != nil {
		r.log.Warn("shadow reclassify", "err", err)
		if err := r.RefreshMetrics(ctx); err != nil {
			r.log.Warn("shadow metrics", "err", err)
		}
	}
	r.Schedule(ctx, importDelay, rssDelay)
}

// Schedule is Run without the reclassification: it schedules arr.import and shadow.rss until ctx ends. With a
// settings runtime, the import schedule (import.arr and shadow.import_interval) is the one current when it starts;
// theatarr serve starts it again when either changes. The RSS schedule follows each generation by itself.
func (r *Runner) Schedule(ctx context.Context, importDelay, rssDelay time.Duration) {
	if r.q == nil {
		return
	}
	start := r
	if r.runtime != nil {
		if active, err := r.forOperation(ctx); err == nil {
			start = active
		}
	}
	type sched struct {
		kind       string
		priority   int
		next       time.Time
		every      time.Duration
		generation int64
	}
	var ss []*sched
	now := r.now()
	if start.arr != nil && start.pol.Import != nil && len(start.pol.Import.Arr) > 0 {
		ss = append(ss, &sched{kind: KindImport, next: now.Add(importDelay), every: start.set.ImportInterval})
	}
	if r.runtime != nil || (r.set.RSS && r.idx != nil && r.hasRSSFeeds(ctx) && len(r.rssVersions()) > 0) {
		var generation int64
		if r.runtime != nil {
			if active, err := r.forOperation(ctx); err == nil {
				generation = active.generation
			}
		}
		ss = append(ss, &sched{kind: KindRSS, priority: priorityRSS, next: now.Add(rssDelay),
			every: r.set.RSSInterval, generation: generation})
	}
	for _, s := range ss {
		r.log.Info("shadow job scheduled", "kind", s.kind, "every", s.every.String(), "first", s.next.UTC())
	}
	if len(ss) == 0 {
		return
	}
	t := time.NewTimer(time.Second)
	defer t.Stop()
	for {
		now := r.now()
		next := now.Add(time.Hour)
		for _, s := range ss {
			var rssEligible bool
			if s.kind == KindRSS && r.runtime != nil {
				active, err := r.forOperation(ctx)
				if err == nil {
					rssEligible = active.set.RSS && active.idx != nil && active.hasRSSFeeds(ctx) && len(active.rssVersions()) > 0
					if active.generation != s.generation {
						s.generation = active.generation
						s.every = active.set.RSSInterval
						s.next = now
					}
				}
			}
			if !now.Before(s.next) {
				if s.kind == KindRSS && r.runtime != nil {
					if rssEligible {
						r.enqueue(ctx, s.kind, nil, jobs.EnqueueOptions{DedupeKey: s.kind, Priority: s.priority, MaxAttempts: 2})
					}
				} else {
					r.enqueue(ctx, s.kind, nil, jobs.EnqueueOptions{DedupeKey: s.kind, Priority: s.priority, MaxAttempts: 2})
				}
				s.next = now.Add(s.every)
			}
			if s.next.Before(next) {
				next = s.next
			}
		}
		wait := next.Sub(now)
		if r.runtime != nil && wait > time.Second {
			wait = time.Second
		}
		t.Reset(max(wait, time.Second))
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
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

// library is the engine's Library over our movies, swapped after each import (the engine reads it concurrently).
type library struct {
	mu     sync.RWMutex
	titles map[int64]decide.Title
	inner  decide.Library
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

func (l *library) all() map[int64]decide.Title {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.titles
}

// refresh reloads our movies.
func (r *Runner) refresh(ctx context.Context) (map[int64]decide.Title, error) {
	titles, err := decide.LoadMovies(ctx, r.st.Reader())
	if err != nil {
		return nil, err
	}
	r.lib.mu.Lock()
	r.lib.titles, r.lib.inner = titles, decide.NewLibrary(titles)
	r.lib.mu.Unlock()
	return titles, nil
}

// titles returns our movies, loading them on first use.
func (r *Runner) titles(ctx context.Context) (map[int64]decide.Title, error) {
	if t := r.lib.all(); t != nil {
		return t, nil
	}
	return r.refresh(ctx)
}

// versionForInstance is the version an import.arr instance's titles become, when it is shadowed.
func (r *Runner) versionForInstance(instance string) (string, bool) {
	if r.pol.Import == nil || r.pol.Import.Arr[instance] == nil {
		return "", false
	}
	k := r.pol.Import.Arr[instance].Version
	return k, r.versions[k]
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

// hasRSSFeeds is whether the shadowed versions list a feed the RSS sync can read.
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

func nullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
