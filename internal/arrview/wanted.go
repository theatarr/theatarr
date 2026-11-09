package arrview

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/store"
)

// wantedPage keeps eligibility and paging in SQL. Only the policy's quality-cutoff judgement needs Go;
// that pass reads 128 version identities at a time and retains matching ids, never the library's resources.
func (s *Service) wantedPage(w http.ResponseWriter, r *request, cutoff bool) {
	ctx := r.Context()
	where, args := scope(r.v, nil)
	q := `WITH vv AS (SELECT v.* FROM version v JOIN title t ON t.id=v.title_id WHERE ` + where + `),
 titles AS (SELECT title_id, MAX(monitored) monitored,
 MAX(state IN ('filled','cutoff_met') OR EXISTS(SELECT 1 FROM version_file f WHERE f.version_id=vv.id)
 OR EXISTS(SELECT 1 FROM link_version lv JOIN link l ON l.id=lv.link_id WHERE lv.version_id=vv.id AND l.state='active')) has_file
 FROM vv GROUP BY title_id)`
	p := r.paging("movieMetadata.sortTitle", "ascending")
	from := ` FROM titles x JOIN title t ON t.id=x.title_id WHERE x.monitored=?`
	args = append(args, r.queryBool("monitored", true))
	id := "t.id"
	order := titleSortSQL
	if r.v.Flavour == Sonarr {
		p = r.paging("episodes.airDateUtc", "ascending")
		q += `, states AS (SELECT s.episode_id, MAX(s.monitored AND v.monitored) monitored,
  MAX(s.state IN ('filled','cutoff_met')) has_file FROM episode_version_state s JOIN vv v ON v.id=s.version_id GROUP BY s.episode_id),
  files AS (SELECT fe.episode_id FROM version_file_episode fe JOIN version_file f ON f.id=fe.version_file_id JOIN vv v ON v.id=f.version_id
  UNION SELECT le.episode_id FROM link_episode le JOIN link l ON l.id=le.link_id JOIN link_version lv ON lv.link_id=l.id JOIN vv v ON v.id=lv.version_id WHERE l.state='active')`
		from = ` FROM episode e JOIN titles x ON x.title_id=e.title_id JOIN title t ON t.id=e.title_id LEFT JOIN states st ON st.episode_id=e.id
  WHERE e.removed_at IS NULL AND (COALESCE(st.monitored,e.monitored AND x.monitored) AND x.monitored)=?`
		id = "e.id"
		order = "e.air_date_utc_at"
		if !cutoff {
			from += ` AND e.air_date_utc_at IS NOT NULL AND e.air_date_utc_at<=? AND COALESCE(st.has_file,0)=0 AND NOT EXISTS(SELECT 1 FROM files f WHERE f.episode_id=e.id)`
			args = append(args, store.Millis(s.now()))
		}
	} else if !cutoff {
		from += ` AND x.has_file=0`
	}
	if cutoff {
		if s.cutoff == nil {
			if r.v.Flavour == Sonarr {
				from += ` AND EXISTS(SELECT 1 FROM episode_version_state ev JOIN vv v ON v.id=ev.version_id WHERE ev.episode_id=e.id AND ev.state='filled')`
			} else {
				from += ` AND EXISTS(SELECT 1 FROM vv v WHERE v.title_id=t.id AND v.state='filled')`
			}
		} else {
			ids, err := s.cutoffIDs(ctx, r.v)
			if err != nil {
				s.internal(w, r, err)
				return
			}
			from += ` AND ` + id + ` IN (SELECT value FROM json_each(?))`
			args = append(args, jsonIDs(ids))
		}
	}
	if k, ok := r.query("sortKey"); ok {
		switch strings.ToLower(k) {
		case "series.sorttitle":
			if r.v.Flavour == Sonarr {
				order = titleSortSQL
				p.SortKey = k
			}
		case "moviemetadata.sorttitle":
			if r.v.Flavour == Radarr {
				p.SortKey = k
			}
		case "episodes.airdateutc":
			if r.v.Flavour == Sonarr {
				p.SortKey = k
			}
		case "moviemetadata.year":
			if r.v.Flavour == Radarr {
				order = "t.year"
				p.SortKey = k
			}
		case "moviemetadata.digitalrelease", "moviemetadata.incinemas", "moviemetadata.physicalrelease":
			if r.v.Flavour == Radarr {
				typ := map[string]string{"moviemetadata.digitalrelease": "digital", "moviemetadata.incinemas": "theatrical", "moviemetadata.physicalrelease": "physical"}[strings.ToLower(k)]
				order = `(SELECT MIN(d.date) FROM title_release_date d WHERE d.title_id=t.id AND d.country='' AND d.type='` + typ + `' AND (d.source='tmdb-art' OR NOT EXISTS(SELECT 1 FROM title_meta m WHERE m.title_id=t.id AND m.movie_dates_at IS NOT NULL)))`
				p.SortKey = k
			}
		case "movies.lastsearchtime", "episodes.lastsearchtime":
			order = `(SELECT MAX(a.created_at) FROM acquire_run a JOIN vv v ON v.id=a.version_id WHERE a.title_id=t.id)`
			if r.v.Flavour == Sonarr {
				order = `(SELECT MAX(a.created_at) FROM acquire_run a JOIN vv v ON v.id=a.version_id WHERE a.title_id=t.id AND (a.season=e.season OR EXISTS(SELECT 1 FROM json_each(a.episodes) j WHERE j.value=e.id)))`
			}
			p.SortKey = k
		}
	}
	if err := s.st.Reader().QueryRowContext(ctx, q+` SELECT COUNT(*)`+from, args...).Scan(&p.TotalRecords); err != nil {
		s.internal(w, r, err)
		return
	}
	dir := " ASC"
	if p.SortDirection == "descending" {
		dir = " DESC"
	}
	rows, err := s.st.Reader().QueryContext(ctx, q+` SELECT `+id+`, t.id`+from+` ORDER BY `+order+dir+`, `+id+dir+` LIMIT ? OFFSET ?`, append(args, p.PageSize, p.offset())...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var ids, tids []int64
	for rows.Next() {
		var id, tid int64
		if err := rows.Scan(&id, &tid); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		ids = append(ids, id)
		tids = append(tids, tid)
	}
	if err := closeRows(rows); err != nil {
		s.internal(w, r, err)
		return
	}
	if tids == nil {
		tids = []int64{}
	}
	es, err := s.load(ctx, r.v, tids)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if r.v.Flavour == Radarr {
		byID := map[int64]*entry{}
		for _, e := range es {
			byID[e.id] = e
		}
		out := make([]movieResource, 0, len(ids))
		for _, id := range ids {
			if e := byID[id]; e != nil {
				out = append(out, s.movieResource(r.v, e))
			}
		}
		p.Records = out
	} else {
		eps, err := s.loadEpisodesWith(ctx, r.v, es, false, ids)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		byID := map[int64]episodeResource{}
		for _, e := range es {
			for _, x := range eps[e.id] {
				byID[x.id] = episodeFrom(e, x)
			}
		}
		out := make([]episodeResource, 0, len(ids))
		for _, id := range ids {
			if ep, ok := byID[id]; ok {
				out = append(out, ep)
			}
		}
		p.Records = out
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Service) cutoffIDs(ctx context.Context, v *View) ([]int64, error) {
	// One standing pin for the whole list (read only if a prefer-on version needs it): every version is judged
	// against the same SeaDex standing.
	ctx, err := decide.WithStoredSeaDex(ctx, s.st.Reader())
	if err != nil {
		return nil, err
	}
	// Candidates as acquire's cutoff search selects them: filled files, and for a prefer-SeaDex version
	// cutoff_met ones too, which a better curated alternative makes wanted again.
	var prefer []string
	for _, k := range v.Versions {
		if vp := s.pol.Versions[k]; vp != nil && vp.PreferSeaDex {
			prefer = append(prefer, k)
		}
	}
	preferJSON, _ := json.Marshal(prefer)
	held := `(%s='filled' OR (%s='cutoff_met' AND v.policy_key IN (SELECT value FROM json_each(?))))`
	var out []int64
	var after int64
	for {
		where, args := scope(v, nil)
		condition := fmt.Sprintf(held, "v.state", "v.state")
		if v.Flavour == Sonarr {
			condition = `EXISTS(SELECT 1 FROM episode_version_state ev WHERE ev.version_id=v.id AND ` +
				fmt.Sprintf(held, "ev.state", "ev.state") + `)`
		}
		args = append(args, string(preferJSON), after)
		rows, err := s.st.Reader().QueryContext(ctx, `SELECT v.id,v.title_id,v.policy_key FROM version v JOIN title t ON t.id=v.title_id WHERE `+where+` AND `+condition+` AND v.id>? ORDER BY v.id LIMIT 128`, args...)
		if err != nil {
			return nil, err
		}
		type candidate struct {
			id, title int64
			key       string
		}
		var batch []candidate
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.id, &c.title, &c.key); err != nil {
				rows.Close()
				return nil, err
			}
			batch = append(batch, c)
		}
		if err := closeRows(rows); err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			return out, nil
		}
		for _, c := range batch {
			after = c.id
			if v.Flavour == Radarr {
				below, err := s.cutoff.BelowQualityCutoff(ctx, c.key, c.id, c.title)
				if err != nil {
					return nil, err
				}
				if below {
					out = append(out, c.title)
				}
			} else {
				below, err := s.cutoff.EpisodesBelowQualityCutoff(ctx, c.key, c.id, c.title)
				if err != nil {
					return nil, err
				}
				// The cutoff interface can return nil for every filled episode.
				ids, err := s.st.Reader().QueryContext(ctx, `SELECT episode_id FROM episode_version_state WHERE version_id=? AND (state='filled' OR (state='cutoff_met' AND ?))`, c.id, slices.Contains(prefer, c.key))
				if err != nil {
					return nil, err
				}
				for ids.Next() {
					var eid int64
					if err := ids.Scan(&eid); err != nil {
						ids.Close()
						return nil, err
					}
					if below == nil || below[eid] {
						out = append(out, eid)
					}
				}
				if err := closeRows(ids); err != nil {
					return nil, err
				}
			}
		}
	}
}
