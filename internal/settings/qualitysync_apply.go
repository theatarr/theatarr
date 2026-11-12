package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"

	"github.com/theatarr/theatarr/internal/store"
)

// applyRun applies a run's preview p in one audited generation (operation quality-import) at p's base generation
// (else ErrConflict: the run fails and the schedule retries) and records it as one entry of the update log. It
// returns the entry's id.
func (s *QualitySync) applyRun(ctx context.Context, p *QualityPreview, actor string) (int64, error) {
	current, err := s.Coordinator.Snapshot(ctx)
	if err != nil {
		return 0, err
	}
	if current.Generation != p.Generation {
		return 0, ErrConflict
	}
	quality := p.candidate
	records := current.Records
	records.Profiles, records.Formats, records.FormatSets, records.Naming = withOrigins(quality.Profiles, current.Records.Profiles), quality.Formats, quality.FormatSets, quality.Naming
	// The candidate carries the saved source's new score state; the source's config is the one previewed (same generation).
	for _, q := range quality.SyncSources {
		records.SyncSources = withScoreState(records.SyncSources, q.Key, q.Config.ScoreState, q.Config.FieldState)
	}
	p.State = "applied"
	preview, _ := json.Marshal(p)
	var id int64
	req := SaveRequest{ExpectedGeneration: p.Generation, Actor: actor, Records: records, Base: s.Coordinator.base}
	_, err = s.Coordinator.saveAndApplyWithHook(ctx, req, "quality-import", func(ctx context.Context, tx *sql.Tx) error {
		now := store.Millis(s.Store.clock())
		return tx.QueryRowContext(ctx, `INSERT INTO settings_quality_review(source_key,base_generation,commit_id,preview,candidate,state,created_at,applied_at) VALUES(?,?,?,?,'null','applied',?,?) RETURNING id`, p.Source, p.Generation, p.Commit, string(preview), now, now).Scan(&id)
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// withOrigins returns profiles with the stored preset origins of current carried by key (the candidate is built from
// the policy, which has none).
func withOrigins(profiles, current []QualityProfile) []QualityProfile {
	origins := map[string]*ProfileOrigin{}
	for _, p := range current {
		origins[p.Key] = p.Origin
	}
	out := slices.Clone(profiles)
	for i := range out {
		if out[i].Origin == nil {
			out[i].Origin = origins[out[i].Key]
		}
	}
	return out
}

// Changes is the update log: the updates runs applied, newest first, at most limit. Held-score decisions of the former
// review (kind "scores") are left out.
func (s *QualitySync) Changes(ctx context.Context, limit int) ([]QualityPreview, error) {
	rows, err := s.Store.db.Reader().QueryContext(ctx, `SELECT id, preview, COALESCE(applied_at,0) FROM settings_quality_review
		WHERE state='applied' AND COALESCE(json_extract(preview,'$.kind'),'')<>'scores' ORDER BY COALESCE(applied_at,0) DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QualityPreview{}
	for rows.Next() {
		var id, at int64
		var raw string
		if err := rows.Scan(&id, &raw, &at); err != nil {
			return nil, err
		}
		var p QualityPreview
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, err
		}
		p.ID, p.AppliedAt, p.State = id, at, "applied"
		out = append(out, p)
	}
	return out, rows.Err()
}

// withScoreState returns sources with the score and field state of source key replaced (a copy; snapshots stay
// immutable).
func withScoreState(sources []QualitySyncSource, key string, state map[string]ProfileScoreState, fields map[string]map[string]FieldMemory) []QualitySyncSource {
	out := slices.Clone(sources)
	for i := range out {
		if out[i].Key == key {
			out[i].Config.ScoreState, out[i].Config.FieldState = state, fields
		}
	}
	return out
}
