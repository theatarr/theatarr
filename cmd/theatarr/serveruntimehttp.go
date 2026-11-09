package main

import (
	"context"
	"net/http"
	"sync"

	"github.com/theatarr/theatarr/internal/arrview"
	"github.com/theatarr/theatarr/internal/notify"
	"github.com/theatarr/theatarr/internal/server"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/web"
)

// Each generation owns its policy readers. Shared services keep their operation runtime through the context.
type serveHTTPRuntime struct {
	mux      *http.ServeMux
	ui       *web.Web
	av       *arrview.Service
	services *serveHTTPServices
}

func newServeHTTPRuntime(ctx context.Context, avConfig arrview.Config, uiConfig web.Config, services *serveHTTPServices) (*serveHTTPRuntime, error) {
	av, err := arrview.New(ctx, avConfig)
	if err != nil {
		return nil, err
	}
	if health, ok := uiConfig.Health.(arrview.HealthSource); ok {
		av.SetHealth(health)
	}
	uiConfig.Devices, uiConfig.Ops = av, serveOps{av}
	ui, err := web.New(uiConfig)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	ui.Mount(mux)
	av.Mount(mux)
	return &serveHTTPRuntime{mux: mux, ui: ui, av: av, services: services}, nil
}

// runtimeArrView resolves the arr views of the settings generation an operation captured, else the current one, so
// notification payloads and the Health webhook name the versions and views the running settings have, not the
// startup ones. Without a settings runtime (YAML mode) it is the startup views.
type runtimeArrView struct {
	manager  *settings.RuntimeCoordinator
	fallback *arrview.Service
}

func (v runtimeArrView) current(ctx context.Context) *arrview.Service {
	if v.manager != nil {
		if runtime, err := runtimeForSearch(ctx, v.manager); err == nil && runtime.HTTP != nil && runtime.HTTP.av != nil {
			return runtime.HTTP.av
		}
	}
	return v.fallback
}

func (v runtimeArrView) Payloads(ctx context.Context, ev notify.Event) ([]notify.Payload, error) {
	return v.current(ctx).Payloads(ctx, ev)
}

func (v runtimeArrView) HealthItems(ctx context.Context) []notify.HealthItem {
	return v.current(ctx).HealthItems(ctx)
}

func runtimeHTTPHandler(manager *settings.RuntimeCoordinator, fallback http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if manager != nil {
			ctx := manager.Capture(req.Context())
			if runtime, err := runtimeForSearch(ctx, manager); err == nil && runtime.HTTP != nil {
				if _, pattern := runtime.HTTP.mux.Handler(req); pattern != "" {
					server.SetRoute(req, pattern)
				}
				runtime.HTTP.mux.ServeHTTP(w, req.WithContext(ctx))
				return
			}
		}
		if mux, ok := fallback.(*http.ServeMux); ok {
			if _, pattern := mux.Handler(req); pattern != "" {
				server.SetRoute(req, pattern)
			}
		}
		fallback.ServeHTTP(w, req)
	})
}

// A publication closes the old event stream so clients reconnect to the new generation.
type serveHTTPServices struct {
	ctx     context.Context
	mu      sync.Mutex
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	stopped bool
	run     func(context.Context)
}

func (s *serveHTTPServices) Start(h *serveHTTPRuntime) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	if s.cancel != nil {
		s.cancel()
		s.wg.Wait()
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.cancel = cancel
	run := s.run
	if run == nil {
		run = h.ui.Run
	}
	s.wg.Go(func() { run(ctx) })
}

func (s *serveHTTPServices) Close() {
	s.mu.Lock()
	s.stopped = true
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}
