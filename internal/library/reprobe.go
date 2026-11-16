package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/library/probe"
	"github.com/theatarr/theatarr/internal/store"
)

// One re-read of stored HEVC probes (docs/design/library.md "File check"): a probe made before the reader took the
// colour from the HEVC SPS and read the first frame of every HDR base layer carries no version mark (probe.Version).
// After start-up one library.reprobe job reads the content files of active links whose stored probe is HEVC without
// the mark again, through the mount as an import's probe reads, one file at a time with reprobeGap between them,
// and replaces content_file.probe and probed_at. Nothing else changes: no rename, no link, no decision. A file that
// cannot be read keeps its probe and is left for the next start. It waits while the library is off (Pause, forced
// off) or the mount does not answer.

// ReprobeJobKind is the start-up re-read of stored HEVC probes.
const ReprobeJobKind = "library.reprobe"

// reprobeGap is the pause between two reads; reprobeWait how long the job waits for the library or the mount.
var (
	reprobeGap  = 2 * time.Second
	reprobeWait = 10 * time.Minute
)

// EnqueueReprobe queues the re-read (one at a time: a queued or running one takes the call).
func (l *Library) EnqueueReprobe(ctx context.Context) error {
	if l.queue == nil {
		return nil
	}
	_, _, err := l.queue.Enqueue(ctx, ReprobeJobKind, struct{}{}, jobs.EnqueueOptions{DedupeKey: ReprobeJobKind})
	return err
}

// RegisterReprobeJob registers the library.reprobe handler.
func (l *Library) RegisterReprobeJob(w *jobs.Worker) {
	w.Handle(ReprobeJobKind, l.HandleReprobe)
}

// HandleReprobe reads the unmarked stored HEVC probes again. Before each file it takes the current settings
// generation (the runtime resolver), so a Pause switched on while it runs stops it.
func (l *Library) HandleReprobe(ctx context.Context, _ *jobs.Job) error {
	type file struct {
		id     int64
		target string
	}
	var files []file
	if err := scanRows(ctx, l.st.Reader(), `SELECT cf.id, MIN(l.target) FROM content_file cf
		JOIN link l ON l.content_file_id = cf.id AND l.state = 'active' AND l.kind = 'symlink'
		WHERE json_valid(cf.probe) AND json_extract(cf.probe, '$.video.codec') = 'hevc'
		AND COALESCE(json_extract(cf.probe, '$.v'), 0) < ? GROUP BY cf.id ORDER BY cf.id`, []any{probe.Version},
		func(r *sql.Rows) error {
			var f file
			if err := r.Scan(&f.id, &f.target); err != nil {
				return err
			}
			files = append(files, f)
			return nil
		}); err != nil {
		return err
	}
	read := 0
	for i, f := range files {
		cur, pctx := l, ctx
		if l.runtime != nil {
			pctx, cur = l.runtime(ctx)
		}
		if !cur.gate.Enabled() {
			return jobs.Later(reprobeWait)
		}
		if err := cur.MountHealthy(pctx); err != nil {
			return jobs.Later(reprobeWait)
		}
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(reprobeGap):
			}
		}
		path, ok := cur.mount.FromLink(f.target)
		if !ok {
			continue
		}
		res, err := cur.mount.ProbeFile(pctx, path, cur.set.ProbeMaxBytes, cur.probe)
		if errors.Is(err, ErrMountDown) {
			return jobs.Later(reprobeWait)
		}
		if err != nil {
			l.log.Debug("library: re-read of a stored probe failed; it is kept", "path", path, "err", err)
			continue
		}
		res.V = probe.Version
		b, err := json.Marshal(res)
		if err != nil {
			return err
		}
		// A probe an import stored meanwhile is newer: left as it is.
		if _, err := l.st.Writer().ExecContext(ctx, `UPDATE content_file SET probe = ?, probed_at = ?
			WHERE id = ? AND COALESCE(json_extract(probe, '$.v'), 0) < ?`,
			string(b), store.Millis(l.now()), f.id, probe.Version); err != nil {
			return err
		}
		read++
	}
	if len(files) > 0 {
		l.log.Info("library: stored HEVC probes read again", "read", read, "of", len(files))
	}
	return nil
}
