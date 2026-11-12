package main

import (
	"context"
	"log/slog"

	"github.com/theatarr/theatarr/internal/arr"
	"github.com/theatarr/theatarr/internal/arrimport"
	"github.com/theatarr/theatarr/internal/importarr"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

// serveArrImport runs the one-time import from Sonarr/Radarr (internal/arrimport) with the services of the settings
// generation the request is pinned to: its policy, TMDb intake, series metadata and library.
type serveArrImport struct {
	manager *settings.RuntimeCoordinator
	st      *store.Store
	log     *slog.Logger
}

func (s serveArrImport) deps(ctx context.Context) (arrimport.Deps, error) {
	active, err := runtimeForSearch(ctx, s.manager)
	if err != nil {
		return arrimport.Deps{}, err
	}
	d := arrimport.Deps{Store: s.st, Policy: active.Policy, Log: s.log,
		Arr: func(url, key string) (importarr.Getter, error) { return arr.New(url, key, arr.Options{}) }}
	if m := active.Metadata; m != nil {
		if m.intake != nil {
			d.Titles = m.intake
		}
		if m.tv != nil {
			d.Series = m.tv
		}
		d.TVDB = m.tvc != nil
	}
	if active.Library != nil && active.Library.lib != nil {
		d.Library = active.Library.lib
	}
	return d, nil
}

func (s serveArrImport) Check(ctx context.Context, flavour, url, key string) (*arrimport.CheckResult, error) {
	d, err := s.deps(ctx)
	if err != nil {
		return nil, err
	}
	return arrimport.Check(ctx, d, flavour, url, key)
}

func (s serveArrImport) Validate(ctx context.Context, req arrimport.Request) error {
	d, err := s.deps(ctx)
	if err != nil {
		return err
	}
	_, err = arrimport.Validate(d, req)
	return err
}

func (s serveArrImport) Run(ctx context.Context, req arrimport.Request, progress func(arrimport.Progress)) (
	*arrimport.Report, error) {
	d, err := s.deps(ctx)
	if err != nil {
		return nil, err
	}
	return arrimport.Run(ctx, d, req, progress)
}
