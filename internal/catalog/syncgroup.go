package catalog

import (
	"context"
	"database/sql"
	"maps"
	"time"

	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// SyncGain is a title that Bring in sync gives copies (docs/design/ui.md "Keep in sync"): the group's libraries Keys
// it gains, following its copy Source.
type SyncGain struct {
	TitleID int64
	Name    string
	Kind    policy.Kind
	// Source is the library whose copy the new ones follow: the group's first monitored copy of the title, else its
	// first copy (removed copies aside); Monitored is that copy's monitored flag.
	Source    string
	Monitored bool
	Keys      []string
}

// SyncGains are the titles that hold a copy (not removed) in a library of group and would gain copies in its other
// libraries by SyncPartners, as if the source copy were added now: a removed or existing unmonitored copy is left as
// it is, and a library that does not fit the title is left out. Sorted by name.
func SyncGains(ctx context.Context, q RowsQuerier, pol *policy.Policy, group []string) ([]SyncGain, error) {
	if len(group) < 2 {
		return nil, nil
	}
	args := make([]any, len(group))
	for i, k := range group {
		args[i] = k
	}
	rows, err := q.QueryContext(ctx, `SELECT t.id, t.name, t.kind, t.anime OR `+store.SeriesTypeSQL("t")+` = 'anime',
		v.policy_key, v.monitored, EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id)
		FROM title t JOIN version v ON v.title_id = t.id WHERE t.id IN (SELECT g.title_id FROM version g
		WHERE g.policy_key IN (`+store.Placeholders(len(group))+`) AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r
		WHERE r.version_id = g.id)) ORDER BY t.name COLLATE NOCASE, t.id`, args...)
	if err != nil {
		return nil, err
	}
	type title struct {
		gain      SyncGain
		anime     bool
		held      Copies
		monitored map[string]bool
	}
	var titles []*title
	for rows.Next() {
		var id int64
		var name, kind, key string
		var anime, monitored, removed bool
		if err := rows.Scan(&id, &name, &kind, &anime, &key, &monitored, &removed); err != nil {
			rows.Close()
			return nil, err
		}
		if len(titles) == 0 || titles[len(titles)-1].gain.TitleID != id {
			titles = append(titles, &title{gain: SyncGain{TitleID: id, Name: name, Kind: policy.Kind(kind)}, anime: anime,
				held: Copies{}, monitored: map[string]bool{}})
		}
		t := titles[len(titles)-1]
		t.held[key] = removed
		t.monitored[key] = monitored
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var out []SyncGain
	for _, t := range titles {
		src := ""
		for _, k := range group {
			if removed, has := t.held[k]; has && !removed && (src == "" || t.monitored[k] && !t.monitored[src]) {
				src = k
			}
		}
		if src == "" {
			continue
		}
		// The rule of an add, as if the title gained its source copy now.
		held := maps.Clone(t.held)
		delete(held, src)
		if t.gain.Keys = SyncPartners(pol, t.gain.Kind, t.anime, held, []string{src}); len(t.gain.Keys) > 0 {
			t.gain.Source, t.gain.Monitored = src, t.monitored[src]
			out = append(out, t.gain)
		}
	}
	return out, nil
}

// BringInSync gives every title of SyncGains its missing copies in group, in tx: each new copy monitored like its
// source copy; a series' new copy takes the source's season and episode monitoring and monitor-new choice
// (CopySeasonIntent); a movie's takes the source's explicit minimum availability unless the library has its own
// (ChoiceKeys). Existing copies, removed ones included, are not touched. Nothing is searched here.
func BringInSync(ctx context.Context, tx *sql.Tx, pol *policy.Policy, group []string, now time.Time) ([]SyncGain,
	error) {
	gains, err := SyncGains(ctx, tx, pol, group)
	if err != nil {
		return nil, err
	}
	for _, g := range gains {
		if _, err := TurnOn(ctx, tx, g.TitleID, g.Keys, g.Monitored, now); err != nil {
			return nil, err
		}
		if g.Kind == policy.KindSeries {
			if err := CopySeasonIntent(ctx, tx, g.TitleID, []string{g.Source}, g.Keys, now); err != nil {
				return nil, err
			}
		} else {
			rules, err := availability.Rules(ctx, tx, pol, []int64{g.TitleID})
			if err != nil {
				return nil, err
			}
			var src int64
			if err := tx.QueryRowContext(ctx, `SELECT id FROM version WHERE title_id = ? AND policy_key = ?`,
				g.TitleID, g.Source).Scan(&src); err != nil {
				return nil, err
			}
			if r := rules[src]; r.Source == availability.SourceChoice {
				if err := availability.Choose(ctx, tx, g.TitleID, ChoiceKeys(pol, g.Keys, g.Keys), r.Minimum,
					now); err != nil {
					return nil, err
				}
			}
		}
		if err := SyncMonitored(ctx, tx, g.TitleID, now); err != nil {
			return nil, err
		}
	}
	return gains, nil
}
