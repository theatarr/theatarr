// Package health runs Theatarr's health checks, after the arrs' HealthCheckService (Radarr
// src/NzbDrone.Core/HealthCheck): a fixed set of cheap checks, each giving one result of type ok, notice, warning
// or error with a message. The results are cached for a minute, so the native API, the System screen, the arr views'
// GET health and the webhook scan can all ask as often as they like.
//
// The checks (checks.go): the zurg mount the library links into, the dangling-link report, metadata refresh age and
// failures, the job backlog per kind, failed jobs that need attention, the nightly backup's age, the provider registry
// sync, the debrid accounts' premium, indexer back-offs and spent budgets, the VPN egress check, and free disk space
// for the data directory. Each takes what it reads as a function or a store, so this package depends on nothing above
// the store, the queue and the provider types.
//
// Unlike the arrs, a check that passes still reports (type ok, with what it saw), so the System screen can show every
// check; the arr views list only the others, as the arrs do.
package health

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/errinbox"
)

// Types, as the arrs' HealthCheckResult names them (lower case, as their API serialises it).
const (
	OK      = "ok"
	Notice  = "notice"
	Warning = "warning"
	Error   = "error"
)

// Rank orders types: ok 0, notice 1, warning 2, error 3.
func Rank(t string) int {
	switch t {
	case Notice:
		return 1
	case Warning:
		return 2
	case Error:
		return 3
	}
	return 0
}

// Result is one check's verdict.
type Result struct {
	// Source is the check's name in the arrs' style (MountCheck, BackupCheck, ...); stable, for webhooks and filters.
	Source string `json:"source"`
	// Name is a short label for people.
	Name    string `json:"name"`
	Type    string `json:"type"`
	Message string `json:"message"`
	// Detail is what the check read, for the API (ages, counts, paths); never secrets.
	Detail    any       `json:"detail,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
	// TookMS is how long the check ran.
	TookMS int64 `json:"took_ms"`
}

// Func runs one check. It sets Type, Message and Detail; the Checker fills the rest. A check that cannot read what
// it needs returns a warning saying so, never panics.
type Func func(ctx context.Context) Result

type check struct {
	source, name string
	fn           Func
}

// Options tune a Checker. The zero value is fine.
type Options struct {
	// TTL is how long results are reused. Default 1m.
	TTL time.Duration
	// Timeout bounds each check. Default 10s.
	Timeout time.Duration
	Now     func() time.Time
	Logger  *slog.Logger
}

// Checker runs the checks and caches their results.
type Checker struct {
	opts   Options
	log    *slog.Logger
	checks []check

	mu      sync.Mutex // one run at a time; held while checks run
	results []Result
	at      time.Time
}

// New returns a Checker with no checks; add them with Add.
func New(opts Options) *Checker {
	if opts.TTL <= 0 {
		opts.TTL = time.Minute
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Checker{opts: opts, log: opts.Logger.With("component", "health")}
}

// Add registers a check. Checks report in the order they were added.
func (c *Checker) Add(source, name string, fn Func) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checks = append(c.checks, check{source: source, name: name, fn: fn})
	c.at = time.Time{}
}

// Results returns every check's latest result, running the checks again when the cached ones are older than the
// TTL. The checks run concurrently, each under the timeout; a check that overruns reports an error. A run cut
// short by a cancelled ctx is returned but not cached.
func (c *Checker) Results(ctx context.Context) []Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.opts.Now()
	if !c.at.IsZero() && now.Sub(c.at) < c.opts.TTL {
		return slices.Clone(c.results)
	}
	out := make([]Result, len(c.checks))
	var wg sync.WaitGroup
	for i, ch := range c.checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = c.run(ctx, ch)
		}()
	}
	wg.Wait()
	// A caller that gave up cut its checks short: those errors say nothing about the system, so keep them out of
	// the cache (and out of the health events that read it).
	if ctx.Err() == nil {
		c.results, c.at = out, c.opts.Now()
	}
	return slices.Clone(out)
}

// Refresh drops the cache, so the next Results runs every check.
func (c *Checker) Refresh() {
	c.mu.Lock()
	c.at = time.Time{}
	c.mu.Unlock()
}

// Issues is the results that are not ok, worst first (ties in check order): what the arr views' GET health lists.
func (c *Checker) Issues(ctx context.Context) []Result {
	var out []Result
	for _, r := range c.Results(ctx) {
		if r.Type != OK {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b Result) int { return Rank(b.Type) - Rank(a.Type) })
	return out
}

// Worst is the highest type among results; ok for none.
func Worst(rs []Result) string {
	w := OK
	for _, r := range rs {
		if Rank(r.Type) > Rank(w) {
			w = r.Type
		}
	}
	return w
}

func (c *Checker) run(ctx context.Context, ch check) Result {
	start := c.opts.Now()
	cctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	done := make(chan Result, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- Result{Type: Error, Message: "the check failed: internal error"}
				// The error inbox records the panic with its stack; the log line stays out of it.
				errinbox.Panic("health", p, map[string]string{"check": ch.source})
				c.log.ErrorContext(errinbox.Quiet(ctx), "health check panicked", "check", ch.source, "panic", p)
			}
		}()
		done <- ch.fn(cctx)
	}()
	var r Result
	select {
	case r = <-done:
	case <-cctx.Done():
		r = Result{Type: Error, Message: "the check did not finish in " + c.opts.Timeout.String()}
		if ctx.Err() != nil {
			r.Message = "the check was cancelled"
		}
	}
	r.Source, r.Name, r.CheckedAt = ch.source, ch.name, start.UTC()
	r.TookMS = c.opts.Now().Sub(start).Milliseconds()
	if r.Type == "" {
		r.Type = OK
	}
	return r
}
