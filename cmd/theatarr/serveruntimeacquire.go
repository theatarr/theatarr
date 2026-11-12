package main

import (
	"context"

	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/settings"
)

// Search evidence and decisions must use the same policy as the captured indexer service.
type serveAcquirer struct {
	manager  *settings.RuntimeCoordinator
	fallback *acquire.Acquirer
}

func (s *serveAcquirer) prepare(cfg acquire.Config, runtime *serveRuntime) error {
	cfg.Policy, cfg.Engine, cfg.Indexers = runtime.Policy, runtime.Engine, runtime.Indexers
	next, err := acquire.New(cfg)
	if err != nil {
		return err
	}
	runtime.Acquirer = next
	return nil
}

func (s *serveAcquirer) forOperation(ctx context.Context) (context.Context, *acquire.Acquirer) {
	if s.manager != nil {
		ctx = s.manager.Capture(ctx)
		if runtime, err := runtimeForSearch(ctx, s.manager); err == nil && runtime.Acquirer != nil {
			return ctx, runtime.Acquirer
		}
	}
	return ctx, s.fallback
}

func (s *serveAcquirer) Submit(ctx context.Context, commandID int64, sel acquire.Selection) (int, int, error) {
	ctx, ac := s.forOperation(ctx)
	return ac.Submit(ctx, commandID, sel)
}

func (s *serveAcquirer) Interactive(ctx context.Context, titleID int64, version string, season *int,
	episodes []int64) (*router.Searched, error) {
	ctx, ac := s.forOperation(ctx)
	return ac.Interactive(ctx, titleID, version, season, episodes)
}

func (s *serveAcquirer) Decide(ctx context.Context, rel decide.Release, titleID int64, version string) (*decide.Decision, error) {
	ctx, ac := s.forOperation(ctx)
	return ac.Decide(ctx, rel, titleID, version)
}

func (s *serveAcquirer) BelowQualityCutoff(ctx context.Context, key string, versionID, titleID int64) (bool, error) {
	ctx, ac := s.forOperation(ctx)
	return ac.BelowQualityCutoff(ctx, key, versionID, titleID)
}

func (s *serveAcquirer) EpisodesBelowQualityCutoff(ctx context.Context, key string, versionID, titleID int64) (map[int64]bool, error) {
	ctx, ac := s.forOperation(ctx)
	return ac.EpisodesBelowQualityCutoff(ctx, key, versionID, titleID)
}

func (s *serveAcquirer) ReleaseWaiting(ctx context.Context, titleID int64) error {
	ctx, ac := s.forOperation(ctx)
	return ac.ReleaseWaiting(ctx, titleID)
}

func (s *serveAcquirer) RegisterJobs(w *jobs.Worker) {
	w.Handle(acquire.JobSearch, func(ctx context.Context, j *jobs.Job) error {
		var p acquire.SearchJob
		if err := j.Decode(&p); err != nil {
			return jobs.Permanent(err)
		}
		ctx, ac := s.forOperation(ctx)
		return ac.RunJob(ctx, p)
	})
	w.Handle(acquire.JobWanted, func(ctx context.Context, j *jobs.Job) error {
		if len(j.Payload) > 0 {
			if err := j.Decode(&struct{}{}); err != nil {
				return jobs.Permanent(err)
			}
		}
		ctx, ac := s.forOperation(ctx)
		_, err := ac.Wanted(ctx)
		return err
	})
	w.Handle(router.JobResearch, func(ctx context.Context, j *jobs.Job) error {
		var p router.ResearchRequest
		if err := j.Decode(&p); err != nil {
			return jobs.Permanent(err)
		}
		ctx, ac := s.forOperation(ctx)
		_, err := ac.Research(ctx, p)
		return err
	})
	w.Handle(acquire.JobSearchNow, func(ctx context.Context, _ *jobs.Job) error {
		ctx, ac := s.forOperation(ctx)
		return ac.HandleSearchNow(ctx)
	})
}

func (s *serveAcquirer) WantedList(ctx context.Context, q acquire.WantedQuery) (*acquire.WantedList, error) {
	ctx, ac := s.forOperation(ctx)
	return ac.WantedList(ctx, q)
}

func (s *serveAcquirer) RequestSearchNow(ctx context.Context, only string, items []acquire.SearchNowItem) (*acquire.SearchNowResult, error) {
	ctx, ac := s.forOperation(ctx)
	return ac.RequestSearchNow(ctx, only, items)
}

func (s *serveAcquirer) RequestSearchNowAll(ctx context.Context, q acquire.WantedQuery) (*acquire.SearchNowResult, error) {
	ctx, ac := s.forOperation(ctx)
	return ac.RequestSearchNowAll(ctx, q)
}

func (s *serveAcquirer) ClearSearchNow(ctx context.Context) (int, error) {
	ctx, ac := s.forOperation(ctx)
	return ac.ClearSearchNow(ctx)
}
