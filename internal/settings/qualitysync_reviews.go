package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/theatarr/theatarr/internal/store"
)

// storePreview records p as the source's pending review. With supersede, the source's earlier pending reviews are
// superseded now; an automatic apply passes false and supersedes them only when its apply lands (supersedeBefore).
// A draft preview never supersedes: the saved source's pending reviews are not the draft's to replace. Held scores
// are not part of p's review (storeHeld records them apart), and a candidate that only updates the source's score
// state is stored only for a run, which applies it.
func (s *QualitySync) storePreview(ctx context.Context, p *QualityPreview, supersede, run bool) error {
	held, heldFields := p.Held, p.HeldFields
	p.Held, p.HeldFields = []HeldScore{}, []HeldField{}
	preview, _ := json.Marshal(p)
	p.Held, p.HeldFields = held, heldFields
	candidate, _ := json.Marshal(p.candidate)
	supersede = supersede && !p.draft
	return s.Store.db.WriteTx(ctx, func(tx *sql.Tx) error {
		gen, _, err := generation(ctx, tx)
		if err != nil {
			return err
		}
		if gen != p.Generation {
			return ErrConflict
		}
		if supersede {
			if _, err := tx.ExecContext(ctx, "UPDATE settings_quality_review SET state='superseded' WHERE source_key=? AND state='pending'", p.Source); err != nil {
				return err
			}
		}
		if !p.HasChanges() && len(p.Retired) == 0 && !(run && p.scoreState) {
			return nil
		}
		return tx.QueryRowContext(ctx, `INSERT INTO settings_quality_review(source_key,base_generation,commit_id,preview,candidate,created_at) VALUES(?,?,?,?,?,?) RETURNING id`, p.Source, p.Generation, p.Commit, string(preview), string(candidate), store.Millis(s.Store.clock())).Scan(&p.ID)
	})
}

// PreviewAndStore binds approval to the exact candidate and base generation shown in the preview.
func (s *QualitySync) PreviewAndStore(ctx context.Context, source QualitySyncSource) (*QualityPreview, error) {
	return s.PreviewAndStoreAt(ctx, source, 0)
}
func (s *QualitySync) PreviewAndStoreAt(ctx context.Context, source QualitySyncSource, expected int64) (*QualityPreview, error) {
	p, err := s.Preview(ctx, source)
	if err != nil {
		return nil, err
	}
	if err := s.storePreviewAt(ctx, p, expected, true, false); err != nil {
		return nil, err
	}
	if !p.draft {
		if _, err := s.storeHeld(ctx, p); err != nil {
			return nil, err
		}
	}
	return p, nil
}
func (s *QualitySync) storePreviewAt(ctx context.Context, p *QualityPreview, expected int64, supersede, run bool) error {
	if expected != 0 && p.Generation != expected {
		return ErrConflict
	}
	return s.storePreview(ctx, p, supersede, run)
}

// storeHeld records p's held scores and fields as the source's held-score review (kind ReviewScores), after the source's earlier
// pending reviews were superseded or replaced by p's own. It returns the review id, 0 when nothing is held.
func (s *QualitySync) storeHeld(ctx context.Context, p *QualityPreview) (int64, error) {
	if len(p.Held)+len(p.HeldFields) == 0 {
		return 0, nil
	}
	h := QualityPreview{Generation: p.Generation, Source: p.Source, Commit: p.Commit, Kind: ReviewScores, Added: []FormatChange{}, Changed: []FormatChange{}, Retired: []FormatChange{}, Scores: []ScoreChange{}, Held: p.Held, HeldFields: p.HeldFields, Fields: []ProfileFieldChange{}, Profiles: []string{}, Notes: []string{}, Impact: DecisionImpact{Examples: []ImpactExample{}}, State: "pending"}
	preview, _ := json.Marshal(h)
	var id int64
	err := s.Store.db.Writer().QueryRowContext(ctx, `INSERT INTO settings_quality_review(source_key,base_generation,commit_id,preview,candidate,created_at) VALUES(?,?,?,?,'null',?) RETURNING id`, p.Source, p.Generation, p.Commit, string(preview), store.Millis(s.Store.clock())).Scan(&id)
	return id, err
}

// supersedeOnly supersedes the pending review id.
func (s *QualitySync) supersedeOnly(ctx context.Context, id int64) error {
	_, err := s.Store.db.Writer().ExecContext(ctx, "UPDATE settings_quality_review SET state='superseded' WHERE id=? AND state='pending'", id)
	return err
}

// supersedeBefore supersedes the source's pending reviews other than keep (keep 0: all of them).
func (s *QualitySync) supersedeBefore(ctx context.Context, source string, keep int64) error {
	_, err := s.Store.db.Writer().ExecContext(ctx, "UPDATE settings_quality_review SET state='superseded' WHERE source_key=? AND state='pending' AND id<>?", source, keep)
	return err
}
func (s *QualitySync) Reviews(ctx context.Context) ([]QualityPreview, error) {
	rows, err := s.Store.db.Reader().QueryContext(ctx, "SELECT id, preview FROM settings_quality_review WHERE state='pending' ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QualityPreview{}
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var p QualityPreview
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, err
		}
		p.ID = id
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *QualitySync) Apply(ctx context.Context, id, expected int64, actor string) (*Snapshot, error) {
	var raw, state, kind string
	var generation int64
	if err := s.Store.db.Reader().QueryRowContext(ctx, "SELECT candidate,base_generation,state,COALESCE(json_extract(preview,'$.kind'),'') FROM settings_quality_review WHERE id=?", id).Scan(&raw, &generation, &state, &kind); err != nil {
		return nil, err
	}
	if kind == ReviewScores {
		return nil, errors.New("settings: held scores are taken or kept one by one, not approved as a whole")
	}
	if state != "pending" || generation != expected {
		return nil, ErrConflict
	}
	current, err := s.Coordinator.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if current.Generation != expected {
		return nil, ErrConflict
	}
	var quality Records
	if err := json.Unmarshal([]byte(raw), &quality); err != nil {
		return nil, err
	}
	records := current.Records
	records.Profiles, records.Formats, records.FormatSets, records.Naming = quality.Profiles, quality.Formats, quality.FormatSets, quality.Naming
	// The candidate carries a saved source's new score state; the source's config is the one previewed (same generation).
	for _, q := range quality.SyncSources {
		records.SyncSources = withScoreState(records.SyncSources, q.Key, q.Config.ScoreState, q.Config.FieldState)
	}
	req := SaveRequest{ExpectedGeneration: expected, Actor: actor, Records: records, Base: s.Coordinator.base}
	return s.Coordinator.saveAndApplyWithHook(ctx, req, "quality-import", func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE settings_quality_review SET state='applied',applied_at=? WHERE id=? AND state='pending' AND base_generation=?", store.Millis(s.Store.clock()), id, expected)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err == nil && n != 1 {
			return ErrConflict
		}
		return err
	})
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

// ScoreDecision resolves one held score (Format) or held field (Field): "take" sets the source's value, which
// follows the source from then on; "keep" leaves ours and pins it, asked again only when the source changes it again.
type ScoreDecision struct {
	Profile string `json:"profile"`
	Format  string `json:"format,omitempty"`
	Field   string `json:"field,omitempty"`
	Choice  string `json:"choice"`
}

// ResolveScores takes or keeps held scores of a held-score review at the current generation expected, in one audited
// generation (operation quality-scores). The review keeps the scores not decided yet and is applied with the last.
func (s *QualitySync) ResolveScores(ctx context.Context, id, expected int64, decisions []ScoreDecision, actor string) (*Snapshot, error) {
	var raw, state, key string
	if err := s.Store.db.Reader().QueryRowContext(ctx, "SELECT preview,state,source_key FROM settings_quality_review WHERE id=?", id).Scan(&raw, &state, &key); err != nil {
		return nil, err
	}
	var p QualityPreview
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, err
	}
	if p.Kind != ReviewScores {
		return nil, errors.New("settings: this review has no held scores; approve it as a whole")
	}
	if state != "pending" {
		return nil, ErrConflict
	}
	if len(decisions) == 0 {
		return nil, errors.New("settings: choose take or keep for at least one held score or field")
	}
	current, err := s.Coordinator.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if current.Generation != expected {
		return nil, ErrConflict
	}
	source, ok := savedSource(current.Records, key)
	if !ok {
		return nil, ErrConflict
	}
	scoreState := map[string]ProfileScoreState{}
	for k, v := range source.Config.ScoreState {
		scoreState[k] = ProfileScoreState{Set: v.Set, Scores: maps.Clone(v.Scores)}
	}
	fieldState := map[string]map[string]FieldMemory{}
	for k, v := range source.Config.FieldState {
		fieldState[k] = maps.Clone(v)
	}
	records := current.Records
	records.Profiles = slices.Clone(records.Profiles)
	remaining := slices.Clone(p.Held)
	remainingFields := slices.Clone(p.HeldFields)
	for _, d := range decisions {
		if d.Field != "" {
			i := slices.IndexFunc(remainingFields, func(h HeldField) bool { return h.Profile == d.Profile && h.Field == d.Field })
			if i < 0 || (d.Choice != "take" && d.Choice != "keep") {
				return nil, fmt.Errorf("settings: %s / %s is not a held field of this review, or its choice is not take or keep", d.Profile, d.Field)
			}
			h := remainingFields[i]
			remainingFields = slices.Delete(remainingFields, i, i+1)
			j := slices.IndexFunc(records.Profiles, func(r QualityProfile) bool { return r.Key == h.Profile })
			if j < 0 {
				return nil, ErrConflict
			}
			if fieldState[h.Profile] == nil {
				fieldState[h.Profile] = map[string]FieldMemory{}
			}
			fieldState[h.Profile][h.Field] = FieldMemory{Upstream: h.Upstream, Pinned: d.Choice == "keep"}
			if d.Choice == "take" {
				if err := setProfileField(&records.Profiles[j].Config, h.Field, h.Upstream); err != nil {
					return nil, err
				}
			}
			continue
		}
		i := slices.IndexFunc(remaining, func(h HeldScore) bool { return h.Profile == d.Profile && h.Format == d.Format })
		if i < 0 || (d.Choice != "take" && d.Choice != "keep") {
			return nil, fmt.Errorf("settings: %s / %s is not a held score of this review, or its choice is not take or keep", d.Profile, d.Format)
		}
		h := remaining[i]
		remaining = slices.Delete(remaining, i, i+1)
		set, bound := source.Config.Profiles[h.Profile]
		j := slices.IndexFunc(records.Profiles, func(r QualityProfile) bool { return r.Key == h.Profile })
		if !bound || j < 0 {
			// The binding or the profile went away since the run: a fresh run decides again.
			return nil, ErrConflict
		}
		ps := scoreState[h.Profile]
		if ps.Set != set || ps.Scores == nil {
			ps = ProfileScoreState{Set: set, Scores: map[string]TrashScoreState{}}
		}
		ps.Scores[h.Format] = TrashScoreState{Trash: h.Trash, Pinned: d.Choice == "keep"}
		scoreState[h.Profile] = ps
		if d.Choice == "take" {
			formats := maps.Clone(records.Profiles[j].Config.Formats)
			if formats == nil {
				formats = map[string]int{}
			}
			formats[h.Format] = h.Trash
			records.Profiles[j].Config.Formats = formats
		}
	}
	records.SyncSources = withScoreState(records.SyncSources, key, scoreState, fieldState)
	p.Held, p.HeldFields = remaining, remainingFields
	next, _ := json.Marshal(p)
	req := SaveRequest{ExpectedGeneration: expected, Actor: actor, Records: records, Base: s.Coordinator.base}
	return s.Coordinator.saveAndApplyWithHook(ctx, req, "quality-scores", func(ctx context.Context, tx *sql.Tx) error {
		q, args := "UPDATE settings_quality_review SET preview=? WHERE id=? AND state='pending'", []any{string(next), id}
		if len(remaining)+len(remainingFields) == 0 {
			q, args = "UPDATE settings_quality_review SET preview=?,state='applied',applied_at=? WHERE id=? AND state='pending'", []any{string(next), store.Millis(s.Store.clock()), id}
		}
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err == nil && n != 1 {
			return ErrConflict
		}
		return err
	})
}
