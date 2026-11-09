// Package server is the HTTP side of theatarr serve: the mux, health and readiness, Prometheus metrics, request logging
// and graceful shutdown. API routes (the native API and the arr views) mount on [Server.Mux].
//
// Endpoints:
//   - /healthz: the process is up. Always 200 while serving.
//   - /readyz: the database answers and its schema is at the version this binary expects; 503 otherwise, and
//     during shutdown.
//   - /metrics: Prometheus metrics (Go runtime, process, HTTP requests, build info, jobs).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/theatarr/theatarr/internal/errinbox"
	"github.com/theatarr/theatarr/internal/indexer"
)

// DefaultListen is the default listen address. 7700 clashes with nothing in the stack (arrs 7878-7882 and
// 8989-8994, Prowlarr 9696, Seerr 5055, decypharr 8282, zurg 9998/9999, Portainer 9000/9443, Plex 32400).
const DefaultListen = ":7700"

// Readiness reports whether the service can take traffic; a nil error means ready. detail goes into the /readyz
// body.
type Readiness func(ctx context.Context) (detail map[string]any, err error)

// Config is what New needs.
type Config struct {
	Listen   string
	Logger   *slog.Logger
	Registry *prometheus.Registry
	Ready    Readiness
	Version  string
	// ShutdownTimeout bounds how long in-flight requests get on shutdown. Default 15s.
	ShutdownTimeout time.Duration
	// MetricsGuard, if set, wraps the /metrics handler (the metrics access policy, internal/web.MetricsGuard).
	MetricsGuard func(http.Handler) http.Handler
}

// Server is the HTTP server.
type Server struct {
	cfg      Config
	log      *slog.Logger
	mux      *http.ServeMux
	srv      *http.Server
	stopping atomic.Bool

	requests *prometheus.CounterVec
	latency  *prometheus.HistogramVec
}

// NewRegistry returns a registry with the Go runtime, process and build info collectors.
func NewRegistry(version string) *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name:        "theatarr_build_info",
			Help:        "Build information; always 1.",
			ConstLabels: prometheus.Labels{"version": version},
		}, func() float64 { return 1 }),
	)
	indexer.RegisterSeaDexMetrics(reg)
	return reg
}

// New builds the server and its base routes.
func New(cfg Config) *Server {
	if cfg.Listen == "" {
		cfg.Listen = DefaultListen
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Registry == nil {
		cfg.Registry = NewRegistry(cfg.Version)
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = 15 * time.Second
	}
	s := &Server{cfg: cfg, log: cfg.Logger.With("component", "http"), mux: http.NewServeMux()}
	s.requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "theatarr_http_requests_total", Help: "HTTP requests by route pattern, method and status code.",
	}, []string{"route", "method", "code"})
	s.latency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "theatarr_http_request_seconds", Help: "HTTP request duration by route pattern.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route"})
	cfg.Registry.MustRegister(s.requests, s.latency)

	s.mux.HandleFunc("GET /healthz", s.healthz)
	s.mux.HandleFunc("GET /readyz", s.readyz)
	var metrics http.Handler = promhttp.HandlerFor(cfg.Registry, promhttp.HandlerOpts{Registry: cfg.Registry})
	if cfg.MetricsGuard != nil {
		metrics = cfg.MetricsGuard(metrics)
	}
	s.mux.Handle("GET /metrics", metrics)

	s.srv = &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
	return s
}

// Mux is where later packages mount their routes.
func (s *Server) Mux() *http.ServeMux { return s.mux }

// Handler is the mux wrapped in request logging and metrics.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		inner := &routeHolder{}
		r = r.WithContext(context.WithValue(r.Context(), routeKey{}, inner))
		panicked := s.serve(rec, r)
		took := time.Since(start)
		route := r.Pattern // set by ServeMux on match
		if inner.pattern != "" {
			route = inner.pattern
		}
		if route == "" {
			route = "unmatched"
		}
		ctx := r.Context()
		if rec.code >= 500 && errors.Is(ctx.Err(), context.Canceled) {
			// The client went away (a page reload or navigation cancels its requests): the handler failed only
			// because of that, so it is no server error. Logged and counted as 499, as nginx does.
			rec.code = statusClientClosed
		}
		if rec.code >= 500 {
			// The error inbox records the answer itself (or the panic behind it); the request line stays out of it.
			ctx = errinbox.Quiet(ctx)
			if !panicked && inboxStatus(rec.code) {
				component := route // a pattern carries its method ("GET /api/..."); a catch-all may not
				if !strings.Contains(route, " ") {
					component = r.Method + " " + route
				}
				errinbox.Report(errinbox.Event{Source: errinbox.SourceHTTP, Component: component,
					Message: fmt.Sprintf("answered %d", rec.code), Time: start,
					Context: map[string]string{"method": r.Method, "route": route, "status": fmt.Sprint(rec.code)}})
			}
		}
		s.requests.WithLabelValues(route, r.Method, fmt.Sprint(rec.code)).Inc()
		s.latency.WithLabelValues(route).Observe(took.Seconds())
		level := slog.LevelInfo
		switch {
		case rec.code >= 500:
			level = slog.LevelError
		case strings.HasSuffix(route, "/healthz") || strings.HasSuffix(route, "/readyz") || strings.HasSuffix(route, "/metrics"):
			level = slog.LevelDebug
		}
		// The query string is left out: arr clients put API keys there.
		s.log.Log(ctx, level, "request", "method", r.Method, "path", r.URL.Path, "route", route,
			"code", rec.code, "bytes", rec.bytes, "took_ms", took.Milliseconds(), "remote", r.RemoteAddr)
	})
}

// serve runs the mux, recovering a handler's panic: the inbox records it with its stack, the client gets a 500
// (when nothing was written yet; otherwise the connection is aborted) and the server keeps serving.
// http.ErrAbortHandler, the deliberate way to abort a response, panics on to net/http as before.
func (s *Server) serve(rec *statusRecorder, r *http.Request) (panicked bool) {
	defer func() {
		p := recover()
		if p == nil {
			return
		}
		if p == http.ErrAbortHandler {
			panic(p)
		}
		panicked = true
		route := r.Pattern
		if h, ok := r.Context().Value(routeKey{}).(*routeHolder); ok && h.pattern != "" {
			route = h.pattern
		}
		errinbox.Panic("http", p, map[string]string{"method": r.Method, "route": route})
		s.log.ErrorContext(errinbox.Quiet(r.Context()), "handler panicked", "method", r.Method, "route", route, "panic", fmt.Sprint(p))
		if rec.wroteHeader {
			// Part of the answer is out: abort the connection as net/http would, so the client cannot take it
			// for a whole one.
			panic(http.ErrAbortHandler)
		}
		http.Error(rec, "internal error", http.StatusInternalServerError)
	}()
	s.mux.ServeHTTP(rec, r)
	return false
}

// inboxStatus is a 5xx the error inbox records: 502, 503 and 504 say a dependency is down or switched off (no policy
// loaded, the engine not running, an upstream that did not answer), which the health checks report.
func inboxStatus(code int) bool {
	return code >= 500 && code != http.StatusBadGateway && code != http.StatusServiceUnavailable &&
		code != http.StatusGatewayTimeout
}

// statusClientClosed is nginx's 499: the client closed the connection before the answer.
const statusClientClosed = 499

type routeKey struct{}

type routeHolder struct{ pattern string }

// SetRoute names the route of a request that a handler mounted on a catch-all pattern passes to its own mux (the
// settings runtime's): metrics and the request log use it instead of the catch-all. r is the request the server's
// handler passed down, or one derived from it. The first route set wins: a request a handler serves in-process on the
// same handler with its own context (the preflight probing the arr views) must not relabel the request it serves.
func SetRoute(r *http.Request, pattern string) {
	if h, ok := r.Context().Value(routeKey{}).(*routeHolder); ok && h.pattern == "" {
		h.pattern = pattern
	}
}

type statusRecorder struct {
	http.ResponseWriter
	code        int
	bytes       int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.code, r.wroteHeader = code, true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.wroteHeader = true
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, "ok\n")
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{"version": s.cfg.Version}
	code := http.StatusOK
	if s.stopping.Load() {
		code = http.StatusServiceUnavailable
		body["status"] = "stopping"
	} else if s.cfg.Ready != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		detail, err := s.cfg.Ready(ctx)
		for k, v := range detail {
			body[k] = v
		}
		if err != nil {
			code = http.StatusServiceUnavailable
			body["status"] = "not ready"
			body["error"] = err.Error()
		}
	}
	if code == http.StatusOK {
		body["status"] = "ready"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(body)
}

// Run listens and serves until ctx is cancelled, then shuts down gracefully: /readyz turns 503 and in-flight requests
// get ShutdownTimeout to finish. It returns nil after a clean shutdown.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

// Serve is Run on an existing listener (tests use port 0).
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	s.log.Info("listening", "addr", ln.Addr().String())
	errc := make(chan error, 1)
	go func() { errc <- s.srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	s.stopping.Store(true)
	s.log.Info("shutting down", "timeout", s.cfg.ShutdownTimeout.String())
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.ShutdownTimeout)
	defer cancel()
	err := s.srv.Shutdown(sctx)
	if serr := <-errc; serr != nil && !errors.Is(serr, http.ErrServerClosed) && err == nil {
		err = serr
	}
	return err
}

// LogLevel controls the process log threshold. It resets to the configured value on restart.
type LogLevel struct{ value slog.LevelVar }

func (l *LogLevel) Level() string { return l.value.Level().String() }

func (l *LogLevel) Set(level string) error {
	switch strings.ToLower(level) {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log level %q: want debug, info, warn or error", level)
	}
	var v slog.Level
	if err := v.UnmarshalText([]byte(level)); err != nil {
		return fmt.Errorf("log level %q: want debug, info, warn or error", level)
	}
	l.value.Set(v)
	return nil
}

// NewRuntimeLogger returns a JSON logger and its live level controller.
func NewRuntimeLogger(w io.Writer, level string) (*slog.Logger, *LogLevel, error) {
	ctl := &LogLevel{}
	if err := ctl.Set(level); err != nil {
		return nil, nil, err
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: &ctl.value})), ctl, nil
}
