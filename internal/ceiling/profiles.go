package ceiling

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/policy"
)

// ProfileVersion identifies the quality profile needed for a version's ceiling explanation.
type ProfileVersion struct {
	ID        int64
	Key, Kind string
}

// Profiles is Profile for a page of versions. Owning arr profiles take precedence over policy profiles;
// each distinct profile is decoded once per flavour, with caches scoped to this call.
func Profiles(ctx context.Context, db DB, pol *policy.Policy, vs []ProfileVersion) (map[int64]*profile.Profile, error) {
	out := map[int64]*profile.Profile{}
	type profileKey struct {
		instance string
		id       int64
		name     string
		fl       format.Flavour
	}
	cache := map[profileKey]*profile.Profile{}
	byID := map[int64]ProfileVersion{}
	for _, v := range vs {
		byID[v.ID] = v
	}
	// Three parameters per version, below even SQLite's older 999-variable limit.
	for start := 0; start < len(vs); start += 300 {
		batch := vs[start:min(start+300, len(vs))]
		var args []any
		for _, v := range batch {
			var inst, name string
			if pol != nil && pol.Versions[v.Key] != nil {
				if pr := pol.Profiles[pol.Versions[v.Key].Profile]; pr != nil {
					if i, n, ok := policy.SplitFromArr(pr.FromArr); ok {
						inst, name = i, n
					}
				}
			}
			args = append(args, v.ID, inst, name)
		}
		rows, err := db.QueryContext(ctx, `WITH requested(version_id,instance,name) AS (VALUES `+
			strings.TrimSuffix(strings.Repeat("(?,?,?),", len(batch)), ",")+`), selected AS (
 SELECT r.version_id,COALESCE(o.instance,f.instance) AS instance,COALESCE(o.arr_id,f.arr_id) AS arr_id
 FROM requested r LEFT JOIN version_source vs ON vs.version_id=r.version_id
 LEFT JOIN arr_title a ON a.instance=vs.instance AND a.arr_id=vs.arr_id
 LEFT JOIN arr_quality_profile o ON o.instance=a.instance AND o.arr_id=a.quality_profile_id
 LEFT JOIN arr_quality_profile f ON o.arr_id IS NULL AND f.instance=r.instance AND f.arr_id=(
  SELECT arr_id FROM arr_quality_profile WHERE instance=r.instance AND name=r.name LIMIT 1))
 SELECT p.instance,p.arr_id,p.quality_items,p.cutoff,json_group_array(s.version_id)
 FROM selected s LEFT JOIN arr_quality_profile p ON p.instance=s.instance AND p.arr_id=s.arr_id
 GROUP BY p.instance,p.arr_id`, args...)
		if err != nil {
			return nil, err
		}
		err = func() error {
			defer rows.Close()
			for rows.Next() {
				var inst, items, cutoff sql.NullString
				var id sql.NullInt64
				var raw string
				if err := rows.Scan(&inst, &id, &items, &cutoff, &raw); err != nil {
					return err
				}
				var ids []int64
				if err := json.Unmarshal([]byte(raw), &ids); err != nil {
					return err
				}
				for _, vid := range ids {
					v := byID[vid]
					fl := format.Radarr
					if v.Kind == "series" {
						fl = format.Sonarr
					}
					key := profileKey{instance: inst.String, id: id.Int64, fl: fl}
					if !id.Valid {
						if pol == nil || pol.Versions[v.Key] == nil {
							continue
						}
						key.name = pol.Versions[v.Key].Profile
						pr := pol.Profiles[key.name]
						if pr == nil {
							continue
						}
						if _, _, ok := policy.SplitFromArr(pr.FromArr); ok {
							continue // A missing from_arr profile has no policy fallback.
						}
					}
					p, ok := cache[key]
					if !ok {
						var err error
						if id.Valid {
							p, err = arrProfile(items.String, cutoff.String, fl)
						} else {
							p, err = policyProfile(pol, v.Key, fl)
						}
						if err != nil {
							return err
						}
						cache[key] = p
					}
					out[vid] = p
				}
			}
			return rows.Err()
		}()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
