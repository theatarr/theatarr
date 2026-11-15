package torrents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/tasks"
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

// errBackupBusy is a backup asked for while another runs.
var errBackupBusy = errors.New("torrents: a backup is already running")

// backup takes one backup and journals it, unless another is running (errBackupBusy).
func backup(ctx context.Context, m *Manager, p BackupPayload) error {
	if m.BackupLock != nil {
		if !m.BackupLock.TryLock() {
			return errBackupBusy
		}
		defer m.BackupLock.Unlock()
	}
	b, info, err := m.TakeBackup(ctx, BackupOptions{Reason: p.Reason, By: p.By})
	if err != nil {
		m.journal(ctx, JournalEntry{Action: "backup", Outcome: "failed", Detail: err.Error(), By: p.By})
		return err
	}
	m.journal(ctx, JournalEntry{Action: "backup", BackupID: b.ID, Outcome: "ok", By: p.By,
		Detail: fmt.Sprintf("%s: %d torrents", p.Reason, info.Torrents)})
	return nil
}

// RegisterJobs registers the backup and restore handlers. A backup asked for while the scheduled one runs waits for
// it.
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
		err = backup(ctx, m, p)
		if errors.Is(err, errBackupBusy) {
			return jobs.Later(30 * time.Second)
		}
		return err
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

// NextBackup is when the next scheduled backup is due: torrents.backup.every after the newest scheduled backup, now
// when there is none; zero while scheduled backups are off.
func (m *Manager) NextBackup() time.Time {
	every := m.Policy.TorrentBackupEvery()
	if every <= 0 {
		return time.Time{}
	}
	list, err := m.Backups()
	if err != nil {
		return time.Time{}
	}
	for _, in := range list {
		if in.Reason == ReasonScheduled {
			return in.CreatedAt.Add(every)
		}
	}
	return m.now()
}

// BackupDue reports whether a scheduled backup is due (NextBackup has come).
func (m *Manager) BackupDue() bool {
	next := m.NextBackup()
	return !next.IsZero() && !m.now().Before(next)
}

// RunSchedule runs the scheduled backups as the torrents.backup task while ctx lives, checking every tick with the
// settings in force. Off (and not listed) unless torrents.backup.every is set. A manual backup running then makes the
// scheduled one wait for the next tick, and nothing is recorded.
func RunSchedule(ctx context.Context, tb *tasks.Table, resolve Resolver, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	var every time.Duration
	unregister := func() {}
	defer func() { unregister() }()
	for {
		m, err := resolve(ctx)
		if err == nil && m.Policy.TorrentBackupEvery() != every {
			every = m.Policy.TorrentBackupEvery()
			unregister()
			unregister = func() {}
			if every > 0 {
				unregister = tb.Register(ctx, scheduledTask(resolve, every), m.NextBackup())
			}
		}
		if err == nil && m.BackupDue() {
			_, _ = tb.Run(ctx, JobBackup, m.now().Add(every))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// scheduledTask is the scheduled backup's task, a backup with the manager in force; skipped while a manual one runs.
func scheduledTask(resolve Resolver, every time.Duration) tasks.Task {
	return tasks.Task{Key: JobBackup, Name: "Back up the debrid torrent lists", Every: every, Inbox: true,
		Run: func(ctx context.Context) error {
			m, err := resolve(ctx)
			if err != nil {
				return err
			}
			err = backup(ctx, m, BackupPayload{Reason: ReasonScheduled, By: "schedule"})
			if errors.Is(err, errBackupBusy) {
				m.log().Info("torrents: scheduled backup waits; a backup is running")
				return tasks.ErrSkip
			}
			return err
		}}
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
