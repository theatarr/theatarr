package shadow

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// FollowComparison compares the grabs made by an arr with actual routes for its follower.
type FollowComparison struct {
	Instance string         `json:"instance"`
	Version  string         `json:"version"`
	Since    time.Time      `json:"since"`
	Pairs    []FollowPair   `json:"pairs"`
	Counts   map[string]int `json:"counts"`
}

// FollowPair is one arr grab, Theatarr route, or match between them.
type FollowPair struct {
	Title           string    `json:"title"`
	Outcome         string    `json:"outcome"`
	Hash            string    `json:"hash,omitempty"`
	ArrHash         string    `json:"arr_hash,omitempty"`
	TheatarrHash    string    `json:"theatarr_hash,omitempty"`
	ArrQuality      string    `json:"arr_quality,omitempty"`
	TheatarrQuality string    `json:"theatarr_quality,omitempty"`
	ArrScore        *int      `json:"arr_score,omitempty"`
	TheatarrScore   int       `json:"theatarr_score,omitempty"`
	First           string    `json:"first,omitempty"`
	By              string    `json:"by,omitempty"`
	At              time.Time `json:"at"`
	State           string    `json:"state,omitempty"`
	Unnecessary     bool      `json:"unnecessary,omitempty"`
}

// CompareFollow reads arr history and route results for a follower. It does not write comparison state.
func CompareFollow(ctx context.Context, db *sql.DB, instance, followed, version string, since time.Time) (*FollowComparison, error) {
	r := &FollowComparison{Instance: instance, Version: version, Since: since, Counts: map[string]int{}}
	type grab struct {
		titleID              int64
		title, hash, quality string
		score                sql.NullInt64
		at                   time.Time
		episodes             map[int64]bool
		state                string
		routeError           string
		theatarr             bool
	}
	var arrs, mms []grab
	rows, err := db.QueryContext(ctx, `SELECT h.title_id, t.name, COALESCE(h.info_hash,''), h.quality,
 h.custom_format_score, h.date_at, CASE WHEN h.arr_episode_id IS NOT NULL THEN COALESCE(a.target_id, -1) END
 FROM arr_history h JOIN title t ON t.id=h.title_id
 LEFT JOIN arr_alias a ON a.instance=h.instance AND a.kind='episode' AND a.arr_id=h.arr_episode_id
 WHERE h.instance=? AND h.event_type='grabbed' AND h.date_at>=? ORDER BY h.date_at,h.arr_id`, instance, store.Millis(since))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var g grab
		var at int64
		var ep sql.NullInt64
		if err := rows.Scan(&g.titleID, &g.title, &g.hash, &g.quality, &g.score, &at, &ep); err != nil {
			rows.Close()
			return nil, err
		}
		g.at = store.FromMillis(at)
		g.episodes = map[int64]bool{}
		if ep.Valid {
			// An unmapped arr episode uses -1, which can never match a Theatarr episode.
			g.episodes[ep.Int64] = true
		}
		arrs = append(arrs, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = db.QueryContext(ctx, `SELECT d.title_id,t.name,d.info_hash,d.quality,d.score,COALESCE(d.added_at,d.created_at),d.episodes,d.state,COALESCE(d.error,'')
 FROM download d JOIN title t ON t.id=d.title_id
 WHERE d.version_key=? AND d.created_at>=? ORDER BY COALESCE(d.added_at,d.created_at),d.id`, version, store.Millis(since))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var g grab
		var at int64
		var eps string
		if err := rows.Scan(&g.titleID, &g.title, &g.hash, &g.quality, &g.score, &at, &eps, &g.state, &g.routeError); err != nil {
			rows.Close()
			return nil, err
		}
		g.theatarr = true
		g.at = store.FromMillis(at)
		g.episodes = map[int64]bool{}
		var ids []int64
		if json.Unmarshal([]byte(eps), &ids) == nil {
			for _, id := range ids {
				g.episodes[id] = true
			}
		}
		mms = append(mms, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	usedA, usedM := map[int]bool{}, map[int]bool{}
	for ai, a := range arrs {
		best := -1
		bestDist := time.Duration(1<<63 - 1)
		for mi, m := range mms {
			if usedM[mi] && len(a.episodes) == 0 || a.titleID != m.titleID {
				continue
			}
			if len(a.episodes) > 0 && !covers(m.episodes, a.episodes) {
				continue
			}
			d := a.at.Sub(m.at)
			if d < 0 {
				d = -d
			}
			if strings.EqualFold(a.hash, m.hash) && a.hash != "" {
				best = mi
				break
			}
			if d < bestDist {
				best, bestDist = mi, d
			}
		}
		if best >= 0 {
			m := mms[best]
			usedA[ai], usedM[best] = true, true
			p := followPair(a, m)
			p.Unnecessary = hasArrFile(ctx, db, instance, followed, m.titleID)
			if strings.EqualFold(a.hash, m.hash) && a.hash != "" {
				p.Outcome = "both_same_hash"
			} else {
				p.Outcome = "both_different_hash"
			}
			r.Pairs = append(r.Pairs, p)
		}
	}
	for i, a := range arrs {
		if !usedA[i] {
			r.Pairs = append(r.Pairs, FollowPair{Title: a.title, Outcome: "arr_only", ArrHash: a.hash, ArrQuality: a.quality, ArrScore: nullScore(a.score), At: a.at})
		}
	}
	for i, m := range mms {
		if usedM[i] {
			continue
		}
		p := FollowPair{Title: m.title, Outcome: "theatarr_only", TheatarrHash: m.hash, TheatarrQuality: m.quality, TheatarrScore: int(m.score.Int64), At: m.at, State: m.state}
		if m.state == "failed" && strings.Contains(m.routeError, "route cancelled:") {
			p.Outcome = "theatarr_cancelled"
		} else if m.state == "failed" {
			p.Outcome = "theatarr_failed"
		}
		p.Unnecessary = hasArrFile(ctx, db, instance, followed, m.titleID)
		r.Pairs = append(r.Pairs, p)
	}
	sort.Slice(r.Pairs, func(i, j int) bool { return r.Pairs[i].At.Before(r.Pairs[j].At) })
	for _, p := range r.Pairs {
		r.Counts[p.Outcome]++
	}
	return r, nil
}

func hasArrFile(ctx context.Context, db *sql.DB, instance, followed string, titleID int64) bool {
	var n int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM version_file vf JOIN version v ON v.id=vf.version_id
 JOIN version_source vs ON vs.version_id=v.id AND vs.instance=? WHERE v.policy_key=? AND vf.title_id=?`, instance, followed, titleID).Scan(&n)
	return n > 0
}

func covers(pack, singles map[int64]bool) bool {
	for id := range singles {
		if !pack[id] {
			return false
		}
	}
	return len(singles) > 0
}
func nullScore(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}
func followPair(a, m struct {
	titleID              int64
	title, hash, quality string
	score                sql.NullInt64
	at                   time.Time
	episodes             map[int64]bool
	state                string
	routeError           string
	theatarr             bool
}) FollowPair { // local row shape
	p := FollowPair{Title: a.title, ArrHash: a.hash, TheatarrHash: m.hash, ArrQuality: a.quality, TheatarrQuality: m.quality, ArrScore: nullScore(a.score), TheatarrScore: int(m.score.Int64), At: a.at, State: m.state}
	d := m.at.Sub(a.at)
	if d < 0 {
		p.First = "theatarr"
		p.By = (-d).String()
	} else if d > 0 {
		p.First = "arr"
		p.By = d.String()
	} else {
		p.First = "same time"
	}
	return p
}
