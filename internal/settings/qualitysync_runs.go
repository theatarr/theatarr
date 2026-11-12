package settings

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// RunSource records every attempt. A run applies what changed upstream by itself, with the same coordinator save as
// an administrator, and writes one entry to the update log (Changes). An unchanged run (a retired format alone counts
// as unchanged) does not create a settings generation. A tracked source first asks for its branch's newest commit and stops there (outcome
// unchanged, no archive fetched) when that commit and its config are the ones of its last successful run. A failed
// run changes nothing but its own record; health reports it.
func (s *QualitySync) RunSource(ctx context.Context, source QualitySyncSource, force bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := source.Config.validate(); err != nil {
		return err
	}
	head, err := s.Coordinator.Snapshot(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, current := range head.Records.SyncSources {
		if current.ID == source.ID && current.Key == source.Key && reflect.DeepEqual(current.Config, source.Config) {
			found = true
		}
	}
	if !found {
		return ErrConflict
	}
	tracked := source.Config.Origin == OriginGitHub
	digest := configDigest(source.Config)
	now := s.Store.clock()
	var runID int64
	err = s.Store.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if !force {
			var last sql.NullInt64
			var outcome sql.NullString
			if err := tx.QueryRowContext(ctx, "SELECT started_at, outcome FROM settings_quality_sync_run WHERE source_key=? ORDER BY started_at DESC, id DESC LIMIT 1", source.Key).Scan(&last, &outcome); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			interval, _ := time.ParseDuration(source.Config.Schedule)
			if tracked && outcome.String == "error" {
				interval = min(interval, trackedRetry)
			}
			if last.Valid && now.Before(time.UnixMilli(last.Int64).Add(interval)) {
				return nil
			}
		}
		return tx.QueryRowContext(ctx, "INSERT INTO settings_quality_sync_run(source_key,started_at,outcome,config_digest) VALUES(?,?,'running',?) RETURNING id", source.Key, store.Millis(now), digest).Scan(&runID)
	})
	if err != nil || runID == 0 {
		return err
	}
	outcome, commit, review, message := "error", "", int64(0), ""
	resolved, runErr := s.resolve(ctx, source.Config)
	if runErr != nil {
		message = "could not read the newest commit of " + source.Config.Repo + " " + source.Config.Branch + "; the last state is kept"
	}
	var p *QualityPreview
	if runErr == nil && tracked {
		commit = resolved.Commit
		last, err := s.lastGood(ctx, source.Key, runID)
		if err != nil {
			runErr = err
		} else if last.commit == commit && last.digest == digest {
			outcome = "unchanged"
		}
	}
	if runErr == nil && outcome != "unchanged" {
		previewCtx := ctx
		if force { // a run started from the page, not the schedule
			previewCtx = withBundledTrash(ctx)
		}
		p, runErr = s.previewResolved(previewCtx, QualitySyncSource{Record: source.Record, Config: resolved})
		if runErr == nil && p.Generation != head.Generation {
			runErr = ErrConflict
		}
	}
	if runErr == nil && p != nil {
		commit = p.Commit
		outcome = "unchanged"
		if p.HasChanges() || len(p.Held)+len(p.HeldFields) > 0 || p.scoreState {
			if review, runErr = s.applyRun(ctx, p, "sync:"+source.Key); runErr == nil {
				outcome = "applied"
			} else {
				message = "the update could not be applied: " + runErr.Error() + "; the last state is kept"
			}
		}
	}
	if runErr != nil {
		outcome = "error"
		if message == "" {
			message = "sync failed; review the source configuration and retry"
		}
	}
	finishErr := s.Store.db.WriteTx(ctx, func(tx *sql.Tx) error {
		var id any
		if review != 0 {
			id = review
		}
		_, err := tx.ExecContext(ctx, "UPDATE settings_quality_sync_run SET commit_id=?,outcome=?,review_id=?,error=? WHERE id=?", commit, outcome, id, message, runID)
		return err
	})
	return errors.Join(runErr, finishErr)
}

type lastRun struct{ commit, digest string }

// lastGood is the source's newest finished run that did not fail, before run id.
func (s *QualitySync) lastGood(ctx context.Context, source string, before int64) (lastRun, error) {
	var r lastRun
	err := s.Store.db.Reader().QueryRowContext(ctx, "SELECT commit_id, config_digest FROM settings_quality_sync_run WHERE source_key=? AND id<? AND outcome IN ('unchanged','review','applied') ORDER BY id DESC LIMIT 1", source, before).Scan(&r.commit, &r.digest)
	if errors.Is(err, sql.ErrNoRows) {
		return lastRun{}, nil
	}
	return r, err
}

// configDigest identifies a source's config, without the score state Theatarr keeps in it.
func configDigest(c QualitySyncConfig) string {
	c.ScoreState, c.FieldState = nil, nil
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (s *QualitySync) RunDue(ctx context.Context) error {
	snap, err := s.Coordinator.Snapshot(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, source := range snap.Records.SyncSources {
		if source.Config.Enabled {
			if err := s.RunSource(ctx, source, false); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
func (s *QualitySync) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		_ = s.RunDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type QualitySyncRun struct {
	ID        int64  `json:"id"`
	Source    string `json:"source"`
	StartedAt int64  `json:"started_at"`
	Commit    string `json:"commit"`
	Outcome   string `json:"outcome"`
	ReviewID  *int64 `json:"review_id"`
	Error     string `json:"error"`
}

// QualitySourceState is one source's newest run and the commit of its newest run that did not fail (what the
// settings hold), for health and the sync page.
type QualitySourceState struct {
	Source     string         `json:"source"`
	LastRun    QualitySyncRun `json:"last_run"`
	LastCommit string         `json:"last_commit"`
}

// SourceStates lists the latest state of every source that has run, by key.
func (s *QualitySync) SourceStates(ctx context.Context) ([]QualitySourceState, error) {
	rows, err := s.Store.db.Reader().QueryContext(ctx, `SELECT r.id,r.source_key,r.started_at,r.commit_id,r.outcome,r.review_id,r.error,
		COALESCE((SELECT g.commit_id FROM settings_quality_sync_run g WHERE g.source_key=r.source_key AND g.outcome IN ('unchanged','review','applied') ORDER BY g.id DESC LIMIT 1),'')
		FROM settings_quality_sync_run r WHERE r.id=(SELECT MAX(id) FROM settings_quality_sync_run WHERE source_key=r.source_key) ORDER BY r.source_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QualitySourceState{}
	for rows.Next() {
		var st QualitySourceState
		r := &st.LastRun
		if err := rows.Scan(&r.ID, &r.Source, &r.StartedAt, &r.Commit, &r.Outcome, &r.ReviewID, &r.Error, &st.LastCommit); err != nil {
			return nil, err
		}
		st.Source = r.Source
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *QualitySync) Runs(ctx context.Context) ([]QualitySyncRun, error) {
	rows, err := s.Store.db.Reader().QueryContext(ctx, "SELECT id,source_key,started_at,commit_id,outcome,review_id,error FROM settings_quality_sync_run ORDER BY id DESC LIMIT 100")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QualitySyncRun{}
	for rows.Next() {
		var r QualitySyncRun
		if err := rows.Scan(&r.ID, &r.Source, &r.StartedAt, &r.Commit, &r.Outcome, &r.ReviewID, &r.Error); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
