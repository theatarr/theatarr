package arrview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

const maintenanceJob = "arrview.maintenance"

type maintenancePayload struct {
	Poll bool `json:"poll,omitempty"`
	// Rescan (RescanMovie, RescanSeries) checks the titles' links against the disk before importing.
	Rescan   bool     `json:"rescan,omitempty"`
	TitleIDs []int64  `json:"title_ids,omitempty"`
	Versions []string `json:"versions,omitempty"`
	Path     string   `json:"path,omitempty"`
}

type downloadPoller interface {
	Poll(context.Context) (router.PollSummary, error)
}

// linkReconciler is the library's check of its links against the disk (library.Library.ReconcileLinks): a link
// whose symlink is gone stops counting as the title's file.
type linkReconciler interface {
	ReconcileLinks(ctx context.Context, version string, titleIDs []int64) ([]*library.LinkScan, error)
}

// SetMaintenance supplies the shared job queue and metadata service before serving requests.
func (s *Service) SetMaintenance(q *jobs.Queue, metadata Metadata) {
	s.jobs, s.metadata = q, metadata
}

// RegisterJobs registers the import-pass and download-poll commands. Metadata commands use meta.art directly.
func (s *Service) RegisterJobs(w *jobs.Worker) {
	w.Handle(maintenanceJob, s.handleMaintenance)
}

// RegisterRuntimeJobs captures the current service when a queued maintenance command starts.
func (s *Service) RegisterRuntimeJobs(w *jobs.Worker, resolve func(context.Context) (context.Context, *Service)) {
	w.Handle(maintenanceJob, func(ctx context.Context, j *jobs.Job) error {
		ctx, service := resolve(ctx)
		return service.handleMaintenance(ctx, j)
	})
}

func (s *Service) maintenanceTitles(ctx context.Context, v *View, body map[string]any) ([]int64, error) {
	sc, err := s.commandScope(ctx, v, "", body)
	if err != nil {
		return nil, err
	}
	for k := range body {
		switch strings.ToLower(k) {
		case "movieid", "movieids", "seriesid", "seriesids", "episodeids":
			if len(sc.sel.TitleIDs) == 0 {
				return nil, errors.New("no titles in this view match the command")
			}
		}
	}
	es, err := s.load(ctx, v, sc.sel.TitleIDs)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for _, e := range es {
		ids = append(ids, e.id)
	}
	if len(ids) == 0 {
		return nil, errors.New("no titles in this view match the command")
	}
	return ids, nil
}

func (s *Service) queueMaintenance(ctx context.Context, v *View, name string, body map[string]any) ([]int64, error) {
	if strings.HasPrefix(name, "Rename") {
		return nil, errors.New("theatarr does not rename existing files")
	}
	if name == "RefreshMovie" || name == "RefreshSeries" {
		if s.metadata == nil {
			return nil, errors.New("metadata refresh is off (no TMDb key)")
		}
		ids, err := s.maintenanceTitles(ctx, v, body)
		if err != nil {
			return nil, err
		}
		var refs []int64
		for _, id := range ids {
			// A title whose anime check failed at its add is asked about again.
			if err := s.reclassify(ctx, v, id); err != nil {
				return nil, err
			}
			job, err := s.metadata.Refresh(ctx, id)
			if err != nil {
				return nil, err
			}
			refs = append(refs, job)
		}
		return refs, nil
	}
	if s.jobs == nil {
		return nil, errors.New("maintenance job queue is unavailable")
	}
	p := maintenancePayload{Versions: v.Versions}
	if name == "RefreshMonitoredDownloads" {
		if _, ok := s.router.(downloadPoller); !ok {
			return nil, errors.New("download polling is unavailable")
		}
		p.Poll = true
		var id int64
		// One global poll, including across views and runtime reloads; completed/failed jobs cool down too.
		err := s.st.WriteTx(ctx, func(tx *sqlTx) error {
			err := tx.QueryRowContext(ctx, `SELECT id FROM job WHERE dedupe_key='arrview.download-poll'
   AND (state IN ('queued','running') OR run_at>?) ORDER BY id DESC LIMIT 1`, store.Millis(s.now().Add(-30*time.Second))).Scan(&id)
			if err == nil {
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			id, _, err = s.jobs.EnqueueTx(ctx, tx, maintenanceJob, p, jobs.EnqueueOptions{MaxAttempts: 1, DedupeKey: "arrview.download-poll", RunAt: s.now()})
			return err
		})
		return []int64{id}, err
	} else {
		if s.library == nil {
			return nil, errors.New("library import is unavailable")
		}
		var err error
		p.TitleIDs, err = s.maintenanceTitles(ctx, v, body)
		if err != nil {
			return nil, err
		}
		p.Path, _ = body["path"].(string)
		p.Rescan = strings.HasPrefix(name, "Rescan")
	}
	id, _, err := s.jobs.Enqueue(ctx, maintenanceJob, p, jobs.EnqueueOptions{MaxAttempts: 1})
	return []int64{id}, err
}

func (s *Service) handleMaintenance(ctx context.Context, j *jobs.Job) error {
	if err := s.TrimCommands(ctx, s.now()); err != nil {
		s.log.Warn("command trim", "err", err)
	}
	var p maintenancePayload
	if err := j.Decode(&p); err != nil {
		return jobs.Permanent(err)
	}
	if p.Poll {
		poller, ok := s.router.(downloadPoller)
		if !ok {
			return errors.New("download polling is unavailable")
		}
		_, err := poller.Poll(ctx)
		return err
	}
	if s.library == nil {
		return errors.New("library import is unavailable")
	}
	// Rescan, as the arrs' DiskScanService: files missing from disk are dropped first (a root that is unavailable
	// or empty is skipped, as theirs is), then whatever is ready is imported.
	if p.Rescan {
		if rc, ok := s.library.(linkReconciler); ok {
			for _, version := range p.Versions {
				scans, err := rc.ReconcileLinks(ctx, version, p.TitleIDs)
				if err != nil {
					return err
				}
				for _, sc := range scans {
					if len(sc.Vanished) > 0 || sc.Skipped != "" {
						s.log.Info("rescan: links reconciled", "version", sc.Version, "root", sc.Root,
							"vanished", len(sc.Vanished), "dry_run", sc.DryRun, "skipped", sc.Skipped)
					}
				}
			}
		}
	}
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT d.id, d.title_id, d.version_key, COALESCE(i.mount_folder, '')
		FROM download d LEFT JOIN library_import i ON i.download_id = d.id
		WHERE d.state IN ('ready', 'importing') AND NOT EXISTS
		(SELECT 1 FROM arrview_removed_version r WHERE r.version_id = d.version_id) ORDER BY d.id`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id, titleID int64
		var version, folder string
		if err := rows.Scan(&id, &titleID, &version, &folder); err != nil {
			rows.Close()
			return err
		}
		if !slices.Contains(p.TitleIDs, titleID) || !slices.Contains(p.Versions, version) {
			continue
		}
		if p.Path != "" {
			if folder == "" {
				continue
			}
			rel, err := filepath.Rel(filepath.Clean(p.Path), folder)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
		}
		ids = append(ids, id)
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	if len(ids) == 0 {
		if p.Rescan {
			return nil
		}
		return errors.New("no ready downloads match these titles or path; Theatarr imports its own downloads only")
	}
	for _, id := range ids {
		plan, err := s.library.Import(ctx, id, false)
		if err != nil {
			return err
		}
		if plan == nil || plan.Waiting {
			return fmt.Errorf("download %d is waiting for the library mount", id)
		}
		if plan.DryRun || plan.Refused != "" {
			return fmt.Errorf("download %d was not imported: %s", id, plan.Refused)
		}
		if len(plan.Links) == 0 {
			return fmt.Errorf("download %d was not imported: no files matched or passed the library guards", id)
		}
	}
	return nil
}

// maintenanceStatus follows all jobs, including retries, and saves terminal status before jobs can be cleared.
func (s *Service) maintenanceStatus(ctx context.Context, c *commandResource, ids []int64, started, ended *sql.NullInt64) error {
	pending, failed, attempted := false, false, false
	var finished int64
	message := ""
	for _, id := range ids {
		var state, lastError string
		var attempts int
		var end sql.NullInt64
		err := s.st.Reader().QueryRowContext(ctx, `SELECT state, attempts, COALESCE(last_error, ''), finished_at FROM job WHERE id = ?`, id).
			Scan(&state, &attempts, &lastError, &end)
		if errors.Is(err, sql.ErrNoRows) {
			failed, message = true, "Command job is no longer available"
			continue
		}
		if err != nil {
			return err
		}
		attempted = attempted || attempts > 0
		pending = pending || state == jobs.StateQueued || state == jobs.StateRunning
		if state == jobs.StateFailed {
			failed, message = true, lastError
		}
		finished = max(finished, end.Int64)
	}
	if attempted && !started.Valid {
		*started = sql.NullInt64{Int64: store.Millis(s.now()), Valid: true}
	}
	if pending {
		if attempted {
			c.Status = "started"
		}
	} else {
		c.Status, c.Result, c.Message = "completed", "successful", "Finished "+splitCamel(c.Name)
		if failed {
			c.Status, c.Result, c.Message = "failed", "unsuccessful", message
		}
		if finished == 0 {
			finished = store.Millis(s.now())
		}
		*ended = sql.NullInt64{Int64: finished, Valid: true}
		if !started.Valid || started.Int64 > finished {
			*started = *ended
		}
	}
	_, err := s.st.Writer().ExecContext(ctx, `UPDATE arrview_command SET status = ?, result = ?, message = ?,
		started_at = ?, ended_at = ? WHERE id = ?`, c.Status, c.Result, c.Message, *started, *ended, c.ID)
	return err
}
