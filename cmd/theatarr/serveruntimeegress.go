package main

import (
	"context"
	"sync"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/settings"
)

// serveEgressChecks owns immediate checks, including checks on retired gateways, until shutdown.
type serveEgressChecks struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	wg      sync.WaitGroup
	running map[*gateway.Gateway]bool
	stopped bool
}

func newServeEgressChecks(ctx context.Context) *serveEgressChecks {
	ctx, cancel := context.WithCancel(ctx)
	return &serveEgressChecks{ctx: ctx, cancel: cancel, running: map[*gateway.Gateway]bool{}}
}

func (s *serveEgressChecks) Start(gw *gateway.Gateway) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.running[gw] || s.ctx.Err() != nil {
		return
	}
	s.running[gw] = true
	s.wg.Go(func() {
		defer func() { s.mu.Lock(); delete(s.running, gw); s.mu.Unlock() }()
		ctx := settings.WithRuntime(s.ctx, &settings.Runtime{Components: &serveRuntime{Gateway: gw}})
		gw.CheckEgress(ctx)
	})
}

func (s *serveEgressChecks) Close() {
	s.mu.Lock()
	s.stopped = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
}
