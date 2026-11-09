package torrents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
)

// Job kinds. Backups and restores run as jobs: a backup reads every torrent's detail within a budget, and a restore
// waits for pacing. A delete runs in the request (it is bounded by MaxDelete).
const (
	JobBackup  = "torrents.backup"
	JobRestore = "torrents.restore"
)

// BackupPayload is a backup job's payload.
type BackupPayload struct {
	Reason string `json:"reason"`
	By     string `json:"by,omitempty"`
}

// RestorePayload is a restore job's payload: the request and the re-adds the person confirmed (target|hash).
type RestorePayload struct {
	Request   RestoreRequest `json:"request"`
	Confirmed []string       `json:"confirmed"`
	By        string         `json:"by,omitempty"`
}

// Resolver gives the manager of the settings generation a job runs under.
type Resolver func(ctx context.Context) (*Manager, error)

// EnqueueBackup queues a backup; one at a time.
func EnqueueBackup(ctx context.Context, q *jobs.Queue, reason, by string) (int64, error) {
	if q == nil {
		return 0, errors.New("torrents: no job queue")
	}
	if reason != ReasonManual && reason != ReasonScheduled {
		return 0, fmt.Errorf("torrents: backup reason %q", reason)
	}
	id, _, err := q.Enqueue(ctx, JobBackup, BackupPayload{Reason: reason, By: by},
		jobs.EnqueueOptions{DedupeKey: JobBackup, MaxAttempts: 1})
	return id, err
}

// EnqueueRestore queues a confirmed restore (CheckRestore first).
func EnqueueRestore(ctx context.Context, q *jobs.Queue, plan *RestorePlan, req RestoreRequest, by string) (int64,
	error) {
	if q == nil {
		return 0, errors.New("torrents: no job queue")
	}
	c := plan.Confirmed()
	if len(c) == 0 {
		return 0, errors.New("torrents: nothing to re-add")
	}
	id, _, err := q.Enqueue(ctx, JobRestore, RestorePayload{Request: req, Confirmed: c, By: by},
		jobs.EnqueueOptions{MaxAttempts: 1})
	return id, err
}

// RegisterJobs registers the backup and restore handlers.
func RegisterJobs(w *jobs.Worker, resolve Resolver) {
	w.Handle(JobBackup, func(ctx context.Context, j *jobs.Job) error {
		var p BackupPayload
		if err := j.Decode(&p); err != nil {
			return jobs.Permanent(err)
		}
		m, err := resolve(ctx)
		if err != nil {
			return err
		}
		b, info, err := m.TakeBackup(ctx, BackupOptions{Reason: p.Reason, By: p.By})
		if err != nil {
			m.journal(ctx, JournalEntry{Action: "backup", Outcome: "failed", Detail: err.Error(), By: p.By})
			return err
		}
		m.journal(ctx, JournalEntry{Action: "backup", BackupID: b.ID, Outcome: "ok", By: p.By,
			Detail: fmt.Sprintf("%s: %d torrents", p.Reason, info.Torrents)})
		return nil
	})
	w.Handle(JobRestore, func(ctx context.Context, j *jobs.Job) error {
		var p RestorePayload
		if err := j.Decode(&p); err != nil {
			return jobs.Permanent(err)
		}
		m, err := resolve(ctx)
		if err != nil {
			return err
		}
		plan, err := m.Restore(ctx, p.Request, p.Confirmed, p.By)
		if err != nil {
			return jobs.Permanent(err)
		}
		return restoreJobError(plan, len(p.Confirmed))
	})
}

// BackupDue reports whether a scheduled backup is due: torrents.backup.every is set and the newest scheduled backup
// is older than it.
func (m *Manager) BackupDue() bool {
	every := m.Policy.TorrentBackupEvery()
	if every <= 0 {
		return false
	}
	list, err := m.Backups()
	if err != nil {
		return false
	}
	for _, in := range list {
		if in.Reason == ReasonScheduled {
			return m.now().Sub(in.CreatedAt) >= every
		}
	}
	return true
}

// RunSchedule queues scheduled backups while ctx lives, checking every tick. Off unless torrents.backup.every is
// set.
func RunSchedule(ctx context.Context, q *jobs.Queue, resolve Resolver, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if m, err := resolve(ctx); err == nil && m.BackupDue() {
			if _, err := EnqueueBackup(ctx, q, ReasonScheduled, "schedule"); err != nil {
				m.log().Warn("torrents: queueing the scheduled backup", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// restoreJobError is the restore job's error: its failed re-adds, and the torrents still waiting for their file
// selection, which fail the job too: they are not restored yet.
func restoreJobError(plan *RestorePlan, confirmed int) error {
	failed, incomplete := 0, 0
	for _, it := range plan.Items {
		switch it.Outcome {
		case OutFailed:
			failed++
		case OutIncomplete:
			incomplete++
		}
	}
	var why []string
	if failed > 0 {
		why = append(why, fmt.Sprintf("%d of %d re-adds failed", failed, confirmed))
	}
	if incomplete > 0 {
		why = append(why, fmt.Sprintf("%d of %d still wait for their file selection (restore again to resume, or "+
			"select on the provider's site)", incomplete, confirmed))
	}
	if len(why) == 0 {
		return nil
	}
	return jobs.Permanent(fmt.Errorf("torrents: restore: %s (see the torrent journal)", strings.Join(why, "; ")))
}
