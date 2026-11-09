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
	"github.com/theatarr/theatarr/internal/uipages"
)

// RunSource records every attempt. Review mode writes only a pending review; auto uses the same coordinator save
// as an administrator, and auto_definitions does so only for a definitions-only preview. An unchanged run does not
// create a settings generation. A tracked source first asks for its branch's newest commit and stops there (outcome
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
	apply := false
	if runErr == nil && outcome != "unchanged" {
		p, runErr = s.previewResolved(ctx, QualitySyncSource{Record: source.Record, Config: resolved})
		if runErr == nil {
			apply = autoApplies(source.Config, p)
			// Earlier pending reviews stay until the automatic apply lands (or its review replaces them below).
			runErr = s.storePreviewAt(ctx, p, head.Generation, !apply, true)
		}
	}
	if runErr == nil && p != nil {
		commit, review = p.Commit, p.ID
		outcome = "unchanged"
		if p.HasChanges() || len(p.Retired) > 0 || p.scoreState {
			outcome = "review"
			if apply {
				_, runErr = s.Apply(ctx, p.ID, p.Generation, "sync:"+source.Key)
				if runErr == nil {
					outcome = "applied"
					runErr = s.supersedeBefore(ctx, source.Key, p.ID)
				}
			}
		}
	}
	var confirmation *ConfirmationError
	if errors.As(runErr, &confirmation) {
		// An automatic run never confirms widening an active library: the review stays pending for an administrator,
		// and replaces the source's earlier ones as a stored review does.
		outcome, runErr = "review", s.supersedeBefore(ctx, source.Key, p.ID)
		message = "the change widens an active library and needs confirmation: apply the review in " + uipages.QualitySync
	} else if runErr != nil && apply && p != nil && p.ID != 0 {
		// The failed apply's own review is not left pending (its base generation may be gone); earlier ones stay.
		runErr = errors.Join(runErr, s.supersedeOnly(ctx, p.ID))
	}
	if runErr == nil && p != nil && len(p.Held)+len(p.HeldFields) > 0 {
		// Only the held scores wait; whatever else the run had is applied or pending above.
		var held int64
		if held, runErr = s.storeHeld(ctx, p); runErr == nil {
			if outcome != "review" {
				review = held
			}
			outcome = "review"
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

// autoApplies reports whether a run applies p by itself. Held scores are never part of p's candidate. auto applies
// every change (retirement alone stays a review); auto_definitions applies specification changes of formats already
// held and the scores that follow TRaSH; a candidate that only updates the source's score state applies in every mode.
// A Recyclarr source's scores are values it manages: an upstream score change always waits for approval, like its
// other profile values; following scores apply by themselves for TRaSH-kind sources only.
func autoApplies(c QualitySyncConfig, p *QualityPreview) bool {
	if c.Kind == "recyclarr" && len(p.Scores) > 0 {
		return false
	}
	switch c.Mode {
	case "auto":
		// A profile change (managed fields, sizes, a new profile) always waits for an administrator.
		return len(p.Profiles) == 0 && (p.HasChanges() || (p.scoreState && len(p.Retired) == 0))
	case ModeAutoDefinitions:
		return p.FollowsOnly(c)
	}
	return p.scoreState && !p.HasChanges() && len(p.Retired) == 0
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
// settings hold or a pending review shows), for health and the sync page.
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
