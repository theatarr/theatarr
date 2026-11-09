package library

import (
	"context"
	"fmt"

	"github.com/theatarr/theatarr/internal/jobs"
)

// ScanJobKind is the debounced Plex partial scan job.
const ScanJobKind = "plex.scan"

// Scanner asks Plex to scan one folder (internal/plex.Client).
type Scanner interface {
	Scan(ctx context.Context, section int, path string) error
}

type scanPayload struct {
	Version string `json:"version"`
	Section int    `json:"section"`
	Path    string `json:"path"`
}

// EnqueueScan queues a partial scan of dir for the version's Plex section, debounced: one job per folder runs
// library.plex.debounce after the first change, and later changes inside that window join it.
func (l *Library) EnqueueScan(ctx context.Context, version, dir string) error {
	lv := l.set.Versions[version]
	if l.queue == nil || l.set.Plex == nil || lv == nil || lv.PlexSection <= 0 {
		return nil
	}
	if err := l.gate.CheckPlex(); err != nil {
		return l.record(ctx, nil, action{Kind: "scan", Mode: "dry_run", Version: version, Path: dir, Detail: err.Error()})
	}
	_, _, err := l.queue.Enqueue(ctx, ScanJobKind, scanPayload{Version: version, Section: lv.PlexSection, Path: dir},
		jobs.EnqueueOptions{RunAt: l.now().Add(l.set.PlexDebounce), DedupeKey: fmt.Sprintf("%s/%d/%s", ScanJobKind, lv.PlexSection, dir),
			MaxAttempts: 5})
	return err
}

// RegisterJobs registers the scan handler. A nil scanner (no Plex configured) makes scans no-ops.
func (l *Library) RegisterJobs(w *jobs.Worker, s Scanner) {
	w.Handle(ScanJobKind, func(ctx context.Context, j *jobs.Job) error {
		return l.HandleScan(ctx, j, s)
	})
}

// HandleScan runs a scan with the library captured when the job started.
func (l *Library) HandleScan(ctx context.Context, j *jobs.Job, s Scanner) error {
	var p scanPayload
	if err := j.Decode(&p); err != nil {
		return jobs.Permanent(err)
	}
	if err := l.gate.CheckPlex(); err != nil {
		return l.record(ctx, nil, action{Kind: "scan", Mode: "refused", Version: p.Version, Path: p.Path, Detail: err.Error()})
	}
	if s == nil {
		return nil
	}
	if err := s.Scan(ctx, p.Section, p.Path); err != nil {
		_ = l.record(ctx, nil, action{Kind: "scan", Mode: "failed", Version: p.Version, Path: p.Path, Detail: err.Error()})
		return err
	}
	return l.record(ctx, nil, action{Kind: "scan", Mode: "applied", Version: p.Version, Path: p.Path,
		Detail: fmt.Sprintf("section %d", p.Section)})
}
