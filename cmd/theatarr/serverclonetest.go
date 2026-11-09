package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/nowplaying"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/web"
)

func rcloneTestGateway(manager *settings.RuntimeCoordinator, fallback *gateway.Gateway, log *slog.Logger) web.RcloneTestGateway {
	return func(ctx context.Context, p *policy.Policy, secrets []string) (nowplaying.Doer, error) {
		previous := fallback
		if rt, err := runtimeForSearch(ctx, manager); err == nil && rt.Gateway != nil {
			previous = rt.Gateway
		}
		// Like the tracker, use the rclone target and its configured budget. Previous
		// retains budget counters and egress state; only this request sees the candidate.
		gw, err := gateway.New(ctx, p, gateway.Options{Previous: previous, Logger: log, Secrets: secrets})
		if err != nil {
			return nil, err
		}
		return pinnedGateway{gw: gw, rt: &settings.Runtime{Policy: p, Components: &serveRuntime{Gateway: gw}}}, nil
	}
}

// pinnedGateway sends through its own gateway: an unpinned one hands requests to the live runtime's gateway, which
// lacks an unsaved rclone_rc target.
type pinnedGateway struct {
	gw *gateway.Gateway
	rt *settings.Runtime
}

func (p pinnedGateway) Do(ctx context.Context, t gateway.Target, req *http.Request) (*http.Response, error) {
	return p.gw.Do(settings.WithRuntime(ctx, p.rt), t, req)
}
