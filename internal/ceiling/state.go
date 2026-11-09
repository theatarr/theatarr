package ceiling

import (
	"context"
	"database/sql"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
)

// State describes one held file against a ceiling, preserving the profile's quality groups.
func State(info *Info, p *profile.Profile, quality, kind string) string {
	if info == nil || info.Tier == "unknown" || p == nil {
		return "below_cutoff"
	}
	fl := format.Radarr
	if kind == "series" {
		fl = format.Sonarr
	}
	q, ok := profile.QualityByName(fl, quality)
	if !ok {
		return "below_cutoff"
	}
	listed := false
	for _, it := range p.Items {
		for _, allowed := range it.Qualities() {
			if allowed.ID == q.ID {
				listed = true
			}
		}
	}
	if !listed {
		return "below_cutoff"
	}
	cutoff, _ := EffectiveCutoff(p, info.Tier)
	if cutoff == 0 {
		return "below_cutoff"
	}
	if p.CompareQualityIDs(q.ID, cutoff, false) < 0 {
		// The better source counts only when the profile can take it: a disc known for a WEB-only profile is not
		// a better source for this version, which then reads by its profile alone.
		if !allowsBetter(p, info.Tier, q.ID) {
			return "below_cutoff"
		}
		return "higher_source_exists"
	}
	if info.Status == "known" || info.Status == "override" {
		return "best_available"
	}
	if info.Status == "likely" {
		return "best_source_seen"
	}
	return "below_cutoff"
}

// allowsBetter reports whether the profile allows a quality of the ceiling's source tier above the held quality.
func allowsBetter(p *profile.Profile, tier string, held int) bool {
	for _, it := range p.Items {
		if !it.Allowed {
			continue
		}
		for _, q := range it.Qualities() {
			if Tier(q.Name) == tier && p.CompareQualityIDs(q.ID, held, false) > 0 {
				return true
			}
		}
	}
	return false
}

// fileState mirrors current-file ownership: active links win over the owning arr's most recent file.
func fileState(ctx context.Context, db DB, vid int64, season int, kind string, info *Info, p *profile.Profile) (string, error) {
	rows, err := db.QueryContext(ctx, `SELECT COALESCE(fe.episode_id,0),COALESCE(e.season,-1),vf.quality,COALESCE(vf.added_at,0),
 COALESCE(es.state,v.state),0 FROM version_file vf JOIN version v ON v.id=vf.version_id
 LEFT JOIN version_source vs ON vs.version_id=v.id LEFT JOIN version_file_episode fe ON fe.version_file_id=vf.id
 LEFT JOIN episode e ON e.id=fe.episode_id LEFT JOIN episode_version_state es ON es.episode_id=e.id AND es.version_id=v.id
 WHERE v.id=? AND (vs.instance IS NULL OR vf.instance=vs.instance)
 UNION ALL SELECT COALESCE(le.episode_id,0),COALESCE(e.season,-1),COALESCE(l.quality,''),l.created_at,COALESCE(es.state,v.state),1
 FROM link l JOIN link_version lv ON lv.link_id=l.id JOIN version v ON v.id=lv.version_id
 LEFT JOIN link_episode le ON le.link_id=l.id LEFT JOIN episode e ON e.id=le.episode_id
 LEFT JOIN episode_version_state es ON es.episode_id=e.id AND es.version_id=v.id WHERE v.id=? AND l.state='active'`, vid, vid)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type file struct {
		q, state string
		at       int64
		link     bool
	}
	files := map[int64]file{}
	for rows.Next() {
		var eid int64
		var s int
		var f file
		if err := rows.Scan(&eid, &s, &f.q, &f.at, &f.state, &f.link); err != nil {
			return "", err
		}
		if s != season {
			continue
		}
		old, ok := files[eid]
		if !ok || (f.link && !old.link) || f.link == old.link && f.at > old.at {
			files[eid] = f
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "none", nil
	}
	states := map[string]bool{}
	for _, f := range files {
		state := "cutoff_met"
		if f.state != "cutoff_met" {
			state = State(info, p, f.q, kind)
		}
		states[state] = true
	}
	for _, s := range []string{"higher_source_exists", "below_cutoff", "best_source_seen", "best_available", "cutoff_met"} {
		if states[s] {
			return s, nil
		}
	}
	return "none", nil
}

var _ DB = (*sql.Tx)(nil)
