package settings

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// ProfileCategories are the kinds of version a default profile is kept for, in display order.
var ProfileCategories = []string{"movie", "series", "anime_movie", "anime_series"}

// ProfileDefaults maps a category to the profile "Add version" preselects for it. Only set categories are present.
type ProfileDefaults map[string]string

// ProfileDefaults reads the stored defaults. A default whose profile no longer exists is still returned; the caller
// shows it as missing.
func (s *Store) ProfileDefaults(ctx context.Context) (ProfileDefaults, error) {
	out := ProfileDefaults{}
	rows, err := s.db.Reader().QueryContext(ctx, "SELECT category, profile FROM setting_profile_default")
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var c, p string
		if err := rows.Scan(&c, &p); err != nil {
			return nil, dbError(err)
		}
		out[c] = p
	}
	return out, dbError(rows.Err())
}

// SetProfileDefaults replaces the defaults: an empty profile clears its category. Every named profile must exist in
// p. It changes no settings generation and is audited at the head generation.
func (s *Store) SetProfileDefaults(ctx context.Context, p *policy.Policy, actor string, defaults ProfileDefaults) (ProfileDefaults, error) {
	if strings.TrimSpace(actor) == "" {
		return nil, errors.New("settings: actor required")
	}
	fields := []policy.FieldError{}
	for c, key := range defaults {
		if !slices.Contains(ProfileCategories, c) {
			fields = append(fields, policy.FieldError{Path: "defaults." + c, Msg: "unknown category"})
			continue
		}
		if key != "" && (p == nil || p.Profiles[key] == nil) {
			fields = append(fields, policy.FieldError{Path: "defaults." + c, Msg: "unknown profile " + key})
		}
	}
	if len(fields) > 0 {
		return nil, &policy.Errors{List: fields}
	}
	before, err := s.ProfileDefaults(ctx)
	if err != nil {
		return nil, err
	}
	after := ProfileDefaults{}
	for _, c := range ProfileCategories {
		if key := defaults[c]; key != "" {
			after[c] = key
		}
	}
	err = s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		gen, _, err := generation(ctx, tx)
		if err != nil {
			return err
		}
		if gen == 0 {
			return ErrUninitialized
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM setting_profile_default"); err != nil {
			return err
		}
		at := s.clock()
		for c, key := range after {
			if _, err := tx.ExecContext(ctx, "INSERT INTO setting_profile_default (category, profile, updated_at) VALUES (?, ?, ?)", c, key, store.Millis(at)); err != nil {
				return err
			}
		}
		return writeAudit(ctx, tx, actor, at, gen, "profile_defaults", "", "update", before, after)
	})
	if err != nil {
		return nil, dbError(err)
	}
	return after, nil
}
