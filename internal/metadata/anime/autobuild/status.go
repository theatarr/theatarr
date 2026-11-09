package autobuild

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// Job is the latest anime_build job for a franchise or a series: queued, running, done or failed (with its error).
type Job struct {
	ID    int64  `json:"id"`
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

// Active reports a job still to run or running.
func (j *Job) Active() bool { return j != nil && (j.State == "queued" || j.State == "running") }

// Jobs is the latest anime_build job per franchise slug and per TVDB id (discovery).
type Jobs struct {
	BySlug map[string]*Job
	ByTVDB map[int]*Job
}

// LatestJobs reads the latest anime_build job of each dedupe key and indexes it by the payload's slug and TVDB id.
func LatestJobs(ctx context.Context, st *store.Store) (*Jobs, error) {
	out := &Jobs{BySlug: map[string]*Job{}, ByTVDB: map[int]*Job{}}
	rows, err := st.Reader().QueryContext(ctx, `SELECT id, state, COALESCE(last_error, ''), payload FROM job
		WHERE id IN (SELECT max(id) FROM job WHERE kind = ? GROUP BY dedupe_key) ORDER BY id`, JobKind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		j := &Job{}
		var payload string
		if err := rows.Scan(&j.ID, &j.State, &j.Error, &payload); err != nil {
			return nil, err
		}
		if j.State != "failed" {
			j.Error = ""
		}
		var p Payload
		_ = json.Unmarshal([]byte(payload), &p)
		if p.Slug != "" {
			out.BySlug[p.Slug] = j // ids ascend: the latest wins
		}
		if p.TVDBID > 0 {
			out.ByTVDB[p.TVDBID] = j
		}
	}
	return out, rows.Err()
}

// Uncovered is an anime series no franchise maps into, with its last discovery.
type Uncovered struct {
	TitleID int64  `json:"title_id"`
	Name    string `json:"name"`
	TVDBID  int    `json:"tvdb_id"`
	// Outcome is the last discovery's (queued, built, covered, no_mapping, failed); empty when never tried.
	Outcome   string     `json:"outcome,omitempty"`
	Franchise string     `json:"franchise,omitempty"`
	Detail    string     `json:"detail,omitempty"`
	TriedAt   *time.Time `json:"tried_at"`
	Job       *Job       `json:"job"`
}

// ListUncovered lists the anime series no franchise maps into (the loop's discovery candidates), by name.
func ListUncovered(ctx context.Context, st *store.Store, jobs *Jobs) ([]Uncovered, error) {
	rows, err := st.Reader().QueryContext(ctx, `SELECT t.id, COALESCE(t.name, ''), t.tvdb_id, COALESCE(d.outcome, ''),
		COALESCE(d.franchise, ''), COALESCE(d.detail, ''), d.tried_at FROM title t
		LEFT JOIN anime_discovery d ON d.title_id = t.id
		WHERE `+uncoveredWhere+` ORDER BY t.name COLLATE NOCASE, t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Uncovered{}
	for rows.Next() {
		var u Uncovered
		var tried sql.NullInt64
		if err := rows.Scan(&u.TitleID, &u.Name, &u.TVDBID, &u.Outcome, &u.Franchise, &u.Detail, &tried); err != nil {
			return nil, err
		}
		if tried.Valid {
			t := store.FromMillis(tried.Int64)
			u.TriedAt = &t
		}
		if jobs != nil {
			u.Job = jobs.ByTVDB[u.TVDBID]
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
